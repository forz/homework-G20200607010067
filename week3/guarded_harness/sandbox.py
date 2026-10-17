"""Bounded filesystem and process capabilities for a trusted local repository.

This module is a capability boundary, not an operating-system sandbox. A configured
test command executes trusted repository code with the current user's OS rights.
"""

from __future__ import annotations

from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path, PureWindowsPath
import selectors
import signal
import stat
import subprocess
import sys
import threading
import time
import uuid


class SandboxError(Exception):
    """A rejected capability request or a safely contained execution failure."""

    def __init__(self, code: str, message: str):
        self.code = code
        super().__init__(message)


class Sandbox:
    GENERATED = frozenset({
        ".git", ".venv", "venv", ".pytest_cache", "__pycache__", "artifacts",
        ".mypy_cache", ".ruff_cache", ".tox", ".nox", "htmlcov", "dist", "build",
        "node_modules", "tmp", ".coverage", ".DS_Store",
    })
    SENSITIVE = frozenset({
        ".git", ".ssh", ".aws", ".gnupg", ".kube", "secrets", "credentials",
        ".netrc", ".npmrc", ".pypirc", ".gitconfig", "id_rsa", "id_dsa",
        "id_ecdsa", "id_ed25519", "authorized_keys",
    })

    def __init__(
        self,
        root: Path,
        commands: dict[str, list[str]] | None = None,
        timeout: float = 10,
        max_output_bytes: int = 32768,
    ):
        candidate = Path(root)
        if candidate.is_symlink() or not candidate.is_dir():
            raise SandboxError("invalid_root", "Sandbox root must be a real directory")
        self.root = candidate.resolve()
        if not isinstance(timeout, (int, float)) or isinstance(timeout, bool) or not 0 < timeout < float("inf"):
            raise SandboxError("invalid_configuration", "timeout must be positive and finite")
        if not isinstance(max_output_bytes, int) or isinstance(max_output_bytes, bool) or max_output_bytes < 1:
            raise SandboxError("invalid_configuration", "max_output_bytes must be a positive integer")
        self.timeout = float(timeout)
        self.max_output_bytes = max_output_bytes
        configured = {"unit": [sys.executable, "-I", "-B", "-m", "pytest", "-q", "-p", "no:cacheprovider", "-c", os.devnull, "--rootdir", ".", "-o", "pythonpath=."]} if commands is None else commands
        if not isinstance(configured, dict):
            raise SandboxError("invalid_configuration", "commands must be an allowlist mapping")
        self.commands = {}
        for key, argv in configured.items():
            if not isinstance(key, str) or not key or not isinstance(argv, list) or not argv or any(not isinstance(arg, str) or "\0" in arg for arg in argv):
                raise SandboxError("invalid_configuration", "Each command needs a name and a nonempty argv list")
            self.commands[key] = list(argv)

    @classmethod
    def _sensitive(cls, name: str) -> bool:
        lowered = name.lower()
        return lowered in cls.SENSITIVE or lowered.startswith((".env", "secrets.", "credentials.")) or lowered.endswith((".pem", ".key"))

    def _parts(self, path: str) -> tuple[str, ...]:
        if not isinstance(path, str) or not path or "\0" in path:
            raise SandboxError("invalid_arguments", "path must be a nonempty string")
        parsed = Path(path)
        if parsed.is_absolute() or PureWindowsPath(path).drive or "\\" in path or ".." in parsed.parts:
            raise SandboxError("path_escape", "Only relative paths without '..' are allowed")
        parts = parsed.parts
        if not parts:
            raise SandboxError("invalid_arguments", "path must name a file")
        if any(self._sensitive(part) for part in parts):
            raise SandboxError("sensitive_path", "Sensitive paths are not accessible")
        return parts

    @staticmethod
    def _check_file(info: os.stat_result) -> None:
        if stat.S_ISLNK(info.st_mode):
            raise SandboxError("symlink_denied", "Symbolic links are not accessible")
        if not stat.S_ISREG(info.st_mode):
            raise SandboxError("not_regular_file", "Only regular files are accessible")
        if info.st_nlink != 1:
            raise SandboxError("hardlink_denied", "Files with multiple hard links are not accessible")

    def resolve_path(self, path: str, write: bool = False) -> Path:
        parts = self._parts(path)
        current = self.root
        for index, part in enumerate(parts):
            current = current / part
            try:
                info = current.lstat()
            except FileNotFoundError:
                if write:
                    return self.root.joinpath(*parts)
                raise SandboxError("not_found", "Requested file does not exist") from None
            except OSError as exc:
                raise SandboxError("filesystem_error", "Cannot inspect requested path") from exc
            if stat.S_ISLNK(info.st_mode):
                raise SandboxError("symlink_denied", "Symbolic links are not accessible")
            if index < len(parts) - 1:
                if not stat.S_ISDIR(info.st_mode):
                    raise SandboxError("not_regular_file", "A parent path is not a directory")
            else:
                self._check_file(info)
        return current

    def validate_call(self, name: str, args: dict) -> None:
        required = {"read_file": {"path"}, "write_file": {"path", "content"}, "run_test": {"command"}}
        if name not in required:
            raise SandboxError("unknown_tool", "Unknown sandbox tool")
        if not isinstance(args, dict) or set(args) != required[name]:
            raise SandboxError("invalid_arguments", "Tool arguments do not match its schema")
        if name == "run_test":
            command = args["command"]
            if not isinstance(command, str) or command not in self.commands:
                raise SandboxError("command_denied", "Command is not in the configured allowlist")
        else:
            self.resolve_path(args["path"], write=name == "write_file")
            if name == "write_file":
                if not isinstance(args["content"], str):
                    raise SandboxError("invalid_arguments", "content must be a string")
                try:
                    args["content"].encode("utf-8")
                except UnicodeEncodeError:
                    raise SandboxError("invalid_arguments", "content must be valid UTF-8") from None

    @contextmanager
    def _parent_fd(self, path: str, create: bool = False):
        """Open each component without following links, also at execution time."""
        parts = self._parts(path)
        fd = None
        try:
            flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
            fd = os.open(self.root, flags)
            for part in parts[:-1]:
                if create:
                    try:
                        os.mkdir(part, 0o755, dir_fd=fd)
                    except FileExistsError:
                        pass
                child = os.open(part, flags, dir_fd=fd)
                os.close(fd)
                fd = child
            yield fd, parts[-1]
        except SandboxError:
            raise
        except OSError as exc:
            raise SandboxError("filesystem_error", "Filesystem operation was rejected or failed") from exc
        finally:
            if fd is not None:
                os.close(fd)

    def read_file(self, path: str) -> str:
        self.validate_call("read_file", {"path": path})
        with self._parent_fd(path) as (parent, name):
            fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
            with os.fdopen(fd, "rb") as stream:
                self._check_file(os.fstat(stream.fileno()))
                data = stream.read(self.max_output_bytes + 1)
                if len(data) > self.max_output_bytes:
                    raise SandboxError("output_limit", "Requested file exceeds the configured output byte limit")
        try:
            return data.decode("utf-8")
        except UnicodeDecodeError:
            raise SandboxError("invalid_encoding", "Requested file is not UTF-8 text") from None

    def write_file(self, path: str, content: str) -> dict:
        self.validate_call("write_file", {"path": path, "content": content})
        data = content.encode("utf-8")
        with self._parent_fd(path, create=True) as (parent, name):
            temporary = ".guarded-" + uuid.uuid4().hex
            try:
                mode = 0o644
                try:
                    previous = os.stat(name, dir_fd=parent, follow_symlinks=False)
                except FileNotFoundError:
                    pass
                else:
                    self._check_file(previous)
                    mode = stat.S_IMODE(previous.st_mode)
                fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode, dir_fd=parent)
                with os.fdopen(fd, "wb") as stream:
                    stream.write(data)
                    stream.flush()
                    os.fsync(stream.fileno())
                # Recheck after writing; os.replace itself never follows the target.
                try:
                    previous = os.stat(name, dir_fd=parent, follow_symlinks=False)
                except FileNotFoundError:
                    pass
                else:
                    self._check_file(previous)
                os.replace(temporary, name, src_dir_fd=parent, dst_dir_fd=parent)
                os.fsync(parent)
            finally:
                try:
                    os.unlink(temporary, dir_fd=parent)
                except FileNotFoundError:
                    pass
        return {"path": Path(path).as_posix(), "bytes": len(data)}

    @staticmethod
    def _environment() -> dict[str, str]:
        allowed = {"PATH", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "SYSTEMROOT", "WINDIR"}
        environment = {key: value for key, value in os.environ.items() if key in allowed}
        environment.update({"PYTEST_DISABLE_PLUGIN_AUTOLOAD": "1", "PYTHONDONTWRITEBYTECODE": "1"})
        return environment

    @staticmethod
    def _kill_group(pid: int) -> None:
        try:
            os.killpg(pid, signal.SIGKILL)
        except ProcessLookupError:
            pass

    def run_test(self, command: str, cancel_event: threading.Event | None = None) -> dict:
        self.validate_call("run_test", {"command": command})
        argv = list(self.commands[command])
        result = {"command": command, "argv": argv, "exit_code": None, "output": "", "timed_out": False, "cancelled": False, "truncated": False}
        if cancel_event is not None and cancel_event.is_set():
            result["cancelled"] = True
            return result
        try:
            process = subprocess.Popen(argv, cwd=self.root, env=self._environment(), stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, shell=False, start_new_session=True)
        except OSError as exc:
            raise SandboxError("execution_failed", "Configured command could not be started") from exc
        output = bytearray()
        selector = selectors.DefaultSelector()
        deadline = time.monotonic() + self.timeout
        try:
            assert process.stdout is not None
            os.set_blocking(process.stdout.fileno(), False)
            selector.register(process.stdout, selectors.EVENT_READ)
            while selector.get_map():
                if cancel_event is not None and cancel_event.is_set():
                    result["cancelled"] = True
                    break
                if time.monotonic() >= deadline:
                    result["timed_out"] = True
                    break
                for key, _ in selector.select(min(0.05, max(0, deadline - time.monotonic()))):
                    block = os.read(key.fileobj.fileno(), 65536)
                    if not block:
                        selector.unregister(key.fileobj)
                        continue
                    remaining = self.max_output_bytes - len(output)
                    output.extend(block[:remaining])
                    if len(block) > remaining:
                        result["truncated"] = True
            # stdout can close before the process exits, so wait under the same budget.
            while process.poll() is None and not result["timed_out"] and not result["cancelled"]:
                if cancel_event is not None and cancel_event.is_set():
                    result["cancelled"] = True
                    break
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    result["timed_out"] = True
                    break
                try:
                    process.wait(timeout=min(0.05, remaining))
                except subprocess.TimeoutExpired:
                    pass
        finally:
            # Also remove descendants left behind by an already exited leader.
            self._kill_group(process.pid)
            process.wait()
            selector.close()
            if process.stdout is not None:
                process.stdout.close()
        result["exit_code"] = process.returncode
        # Do not let a partial multibyte sequence expand beyond the byte budget.
        result["output"] = bytes(output).decode("utf-8", errors="replace").encode("utf-8")[:self.max_output_bytes].decode("utf-8", errors="ignore")
        return result

    def manifest(self, *, include_artifacts: bool = False) -> dict[str, str]:
        result = {}
        excluded = self.GENERATED - {"artifacts"} if include_artifacts else self.GENERATED
        for directory, subdirs, filenames in os.walk(self.root, followlinks=False):
            subdirs[:] = sorted(name for name in subdirs if name not in excluded and not self._sensitive(name))
            for name in subdirs:
                entry = Path(directory) / name
                if entry.is_symlink():
                    relative = entry.relative_to(self.root).as_posix()
                    result[relative] = self._metadata_digest(entry.lstat(), target=os.readlink(entry))
            for name in sorted(filenames):
                if name in excluded or self._sensitive(name) or name.endswith((".pyc", ".pyo")):
                    continue
                relative = (Path(directory) / name).relative_to(self.root).as_posix()
                digest = hashlib.sha256()
                with self._parent_fd(relative) as (parent, filename):
                    info = os.stat(filename, dir_fd=parent, follow_symlinks=False)
                    if stat.S_ISLNK(info.st_mode):
                        result[relative] = self._metadata_digest(info, target=os.readlink(filename, dir_fd=parent))
                        continue
                    if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
                        result[relative] = self._metadata_digest(info)
                        continue
                    fd = os.open(filename, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
                    with os.fdopen(fd, "rb") as stream:
                        self._check_file(os.fstat(stream.fileno()))
                        for block in iter(lambda: stream.read(65536), b""):
                            digest.update(block)
                result[relative] = digest.hexdigest()
        return dict(sorted(result.items()))

    @staticmethod
    def _metadata_digest(info: os.stat_result, target: str | None = None) -> str:
        # Observe disallowed entries without dereferencing or reading them. Their
        # presence must not prevent the gate from auditing a rejected access.
        metadata = {"mode": info.st_mode, "inode": info.st_ino, "device": info.st_dev,
                    "links": info.st_nlink, "size": info.st_size, "modified_ns": info.st_mtime_ns,
                    "target": target}
        return hashlib.sha256(json.dumps(metadata, sort_keys=True, separators=(",", ":")).encode()).hexdigest()

    def fingerprint(self) -> str:
        serialized = json.dumps(self.manifest(include_artifacts=True), sort_keys=True, separators=(",", ":")).encode("utf-8")
        return hashlib.sha256(serialized).hexdigest()
