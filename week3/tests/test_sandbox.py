from __future__ import annotations

import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time

import pytest

from guarded_harness.sandbox import Sandbox, SandboxError


@pytest.fixture
def sandbox(tmp_path):
    return Sandbox(tmp_path)


@pytest.mark.parametrize("path", ["artifacts/../../.env", "../outside", "/etc/passwd", "C:/outside", "dir\\outside"])
def test_s1_traversal_rejected_before_execution(sandbox, path):
    with pytest.raises(SandboxError) as caught:
        sandbox.validate_call("write_file", {"path": path, "content": "unsafe"})
    assert caught.value.code == "path_escape"
    assert list(sandbox.root.iterdir()) == []


@pytest.mark.parametrize("path", [".env", ".env.production", ".git/config", "nested/.ssh/id_rsa", "secrets/token.txt", "config/credentials.json", "private.pem"])
def test_s2_sensitive_paths_rejected(sandbox, path):
    with pytest.raises(SandboxError) as caught:
        sandbox.resolve_path(path, write=True)
    assert caught.value.code == "sensitive_path"


def test_s2_symlink_escape_and_internal_symlink_rejected(sandbox, tmp_path):
    outside = tmp_path.parent / (tmp_path.name + "-outside")
    outside.mkdir()
    (outside / "private.txt").write_text("private")
    (tmp_path / "escape").symlink_to(outside, target_is_directory=True)
    (tmp_path / "source.txt").write_text("source")
    (tmp_path / "link.txt").symlink_to(tmp_path / "source.txt")
    for path in ["escape/private.txt", "escape/new.txt", "link.txt"]:
        with pytest.raises(SandboxError) as caught:
            sandbox.resolve_path(path, write=True)
        assert caught.value.code == "symlink_denied"
    assert (outside / "private.txt").read_text() == "private"


def test_hardlinks_and_nonregular_files_rejected(sandbox):
    original = sandbox.root / "original.txt"
    original.write_text("private")
    os.link(original, sandbox.root / "hardlink.txt")
    for path in ["original.txt", "hardlink.txt"]:
        with pytest.raises(SandboxError, match="hard links"):
            sandbox.read_file(path)
        with pytest.raises(SandboxError, match="hard links"):
            sandbox.write_file(path, "changed")
    os.mkfifo(sandbox.root / "fifo")
    with pytest.raises(SandboxError) as caught:
        sandbox.read_file("fifo")
    assert caught.value.code == "not_regular_file"
    assert original.read_text() == "private"


def test_read_write_utf8_atomic_file_and_manifest(sandbox):
    before = sandbox.fingerprint()
    assert sandbox.write_file("src/main.py", "answer = '中文'\n") == {"path": "src/main.py", "bytes": len("answer = '中文'\n".encode())}
    assert sandbox.read_file("src/main.py") == "answer = '中文'\n"
    assert sandbox.fingerprint() != before
    assert set(sandbox.manifest()) == {"src/main.py"}
    unchanged = sandbox.fingerprint()
    sandbox.write_file("artifacts/report.md", "report")
    assert sandbox.fingerprint() != unchanged
    assert set(sandbox.manifest()) == {"src/main.py"}
    assert set(sandbox.manifest(include_artifacts=True)) == {"src/main.py", "artifacts/report.md"}
    unchanged = sandbox.fingerprint()
    (sandbox.root / ".env").write_text("secret")
    (sandbox.root / "__pycache__").mkdir()
    (sandbox.root / "__pycache__/cache.pyc").write_bytes(b"cache")
    assert sandbox.fingerprint() == unchanged
    sandbox.write_file("src/main.py", "answer = 42\n")
    assert sandbox.fingerprint() != unchanged
    assert not list((sandbox.root / "src").glob(".guarded-*"))


def test_fingerprint_covers_artifact_edits_and_symlink_without_following(sandbox):
    sandbox.write_file("artifacts/report.md", "first")
    before = sandbox.fingerprint()
    sandbox.write_file("artifacts/report.md", "human edit")
    assert sandbox.fingerprint() != before
    before = sandbox.fingerprint()
    (sandbox.root / "artifacts/report.md").unlink()
    (sandbox.root / "artifacts/report.md").symlink_to("/etc/passwd")
    assert sandbox.fingerprint() != before
    assert sandbox.manifest() == {}


def test_failed_atomic_replace_keeps_previous_content_and_cleans_temp(sandbox, monkeypatch):
    sandbox.write_file("source.py", "old")

    def fail_replace(*args, **kwargs):
        raise OSError("injected replace failure")

    monkeypatch.setattr(os, "replace", fail_replace)
    with pytest.raises(SandboxError) as caught:
        sandbox.write_file("source.py", "new")
    assert caught.value.code == "filesystem_error"
    assert sandbox.read_file("source.py") == "old"
    assert not list(sandbox.root.glob(".guarded-*"))


def test_execution_rechecks_parent_after_validation_race(sandbox, tmp_path, monkeypatch):
    outside = tmp_path.parent / (tmp_path.name + "-race-outside")
    outside.mkdir()
    (outside / "file.txt").write_text("private")
    sandbox.write_file("folder/file.txt", "public")
    original = sandbox.resolve_path

    def replace_parent_after_validation(path, write=False):
        result = original(path, write=write)
        (tmp_path / "folder").rename(tmp_path / "old-folder")
        (tmp_path / "folder").symlink_to(outside, target_is_directory=True)
        return result

    monkeypatch.setattr(sandbox, "resolve_path", replace_parent_after_validation)
    with pytest.raises(SandboxError):
        sandbox.read_file("folder/file.txt")
    assert (outside / "file.txt").read_text() == "private"


def test_validation_does_not_create_directory_or_start_process(sandbox, monkeypatch):
    calls = []
    monkeypatch.setattr(subprocess, "Popen", lambda *a, **k: calls.append((a, k)))
    sandbox.validate_call("write_file", {"path": "nested/new.py", "content": "pass"})
    sandbox.validate_call("run_test", {"command": "unit"})
    assert not (sandbox.root / "nested").exists()
    assert calls == []


@pytest.mark.parametrize("name,args", [
    ("read_file", {"path": "missing", "extra": True}),
    ("write_file", {"path": "new", "content": 123}),
    ("write_file", {"path": "new", "content": "\ud800"}),
    ("run_test", {"command": ["unit"]}),
    ("shell", {"command": "echo unsafe"}),
])
def test_invalid_tool_arguments_fail_closed(sandbox, name, args):
    with pytest.raises(SandboxError):
        sandbox.validate_call(name, args)
    assert list(sandbox.root.iterdir()) == []


@pytest.mark.parametrize("command", ["unit; touch unsafe", "unit && echo unsafe", " unit", "python -c 'print(1)'", "UNIT"])
def test_s3_only_exact_configured_command_key_runs(sandbox, command, monkeypatch):
    calls = []
    monkeypatch.setattr(subprocess, "Popen", lambda *a, **k: calls.append((a, k)))
    with pytest.raises(SandboxError) as caught:
        sandbox.run_test(command)
    assert caught.value.code == "command_denied"
    assert calls == []


def test_test_environment_excludes_secrets_and_python_injection(tmp_path, monkeypatch):
    for name in ["OPENAI_API_KEY", "AWS_SECRET_ACCESS_KEY", "INTERNAL_TOKEN", "PYTHONPATH", "PYTHONHOME", "PYTEST_ADDOPTS", "HOME"]:
        monkeypatch.setenv(name, "DO_NOT_LEAK")
    sandbox = Sandbox(tmp_path, commands={"env": [sys.executable, "-c", "import json,os; print(json.dumps(dict(os.environ)))"]})
    result = sandbox.run_test("env")
    assert result["exit_code"] == 0
    assert not result["timed_out"]
    assert "DO_NOT_LEAK" not in result["output"]
    environment = json.loads(result["output"])
    assert environment["PYTEST_DISABLE_PLUGIN_AUTOLOAD"] == "1"
    assert environment["PYTHONDONTWRITEBYTECODE"] == "1"
    assert "PYTHONPATH" not in environment
    assert "PYTHONHOME" not in environment


def test_process_output_bound_does_not_deadlock_large_stdout_stderr(tmp_path):
    sandbox = Sandbox(tmp_path, commands={"noisy": [sys.executable, "-c", "import os; os.write(1, b'a'*1000000); os.write(2, b'b'*1000000)"]}, max_output_bytes=257)
    result = sandbox.run_test("noisy")
    assert result["exit_code"] == 0
    assert result["truncated"]
    assert len(result["output"].encode()) == 257
    assert not result["timed_out"]


def test_invalid_utf8_output_remains_within_byte_bound(tmp_path):
    sandbox = Sandbox(tmp_path, commands={"binary": [sys.executable, "-c", "import os; os.write(1, b'\\xff'*100)"]}, max_output_bytes=17)
    result = sandbox.run_test("binary")
    assert result["exit_code"] == 0
    assert result["truncated"]
    assert len(result["output"].encode()) <= 17


def _process_tree_command():
    child = "import pathlib,time; time.sleep(0.7); pathlib.Path('child-survived').write_text('unsafe'); time.sleep(20)"
    parent = (
        "import subprocess,sys,os,json,time; "
        f"child=subprocess.Popen([sys.executable,'-c',{child!r}]); "
        "print(json.dumps({'parent':os.getpid(),'child':child.pid,'group':os.getpgrp()}),flush=True); "
        "time.sleep(20)"
    )
    return [sys.executable, "-c", parent]


def _assert_tree_stopped(result, tmp_path):
    identifiers = json.loads(result["output"].splitlines()[0])
    assert identifiers["group"] == identifiers["parent"]
    assert result["exit_code"] == -signal.SIGKILL
    # A descendant must not survive long enough to perform its delayed write.
    time.sleep(0.8)
    assert not (tmp_path / "child-survived").exists()
    for key in ["parent", "child"]:
        state = subprocess.run(["ps", "-o", "stat=", "-p", str(identifiers[key])], capture_output=True, text=True, check=False).stdout.strip()
        assert not state or state.startswith("Z"), f"{key} process still executing: {state}"


def test_s4_timeout_kills_actual_parent_and_child_process_group(tmp_path):
    sandbox = Sandbox(tmp_path, commands={"tree": _process_tree_command()}, timeout=0.25)
    started = time.monotonic()
    result = sandbox.run_test("tree")
    assert time.monotonic() - started < 2
    assert result["timed_out"]
    assert not result["cancelled"]
    _assert_tree_stopped(result, tmp_path)


def test_s4_cancellation_kills_actual_parent_and_child_process_group(tmp_path):
    sandbox = Sandbox(tmp_path, commands={"tree": _process_tree_command()}, timeout=10)
    cancelled = threading.Event()
    timer = threading.Timer(0.25, cancelled.set)
    timer.start()
    try:
        result = sandbox.run_test("tree", cancel_event=cancelled)
    finally:
        timer.cancel()
        timer.join()
    assert result["cancelled"]
    assert not result["timed_out"]
    _assert_tree_stopped(result, tmp_path)


def test_pre_cancelled_command_never_starts_process(sandbox, monkeypatch):
    cancelled = threading.Event()
    cancelled.set()
    calls = []
    monkeypatch.setattr(subprocess, "Popen", lambda *a, **k: calls.append((a, k)))
    result = sandbox.run_test("unit", cancel_event=cancelled)
    assert result["cancelled"]
    assert result["exit_code"] is None
    assert calls == []


def test_timeout_applies_when_process_closes_output_before_sleeping(tmp_path):
    command = [sys.executable, "-c", "import os,time; os.close(1); os.close(2); time.sleep(20)"]
    result = Sandbox(tmp_path, commands={"silent": command}, timeout=0.2).run_test("silent")
    assert result["timed_out"]
    assert result["exit_code"] == -signal.SIGKILL


def test_default_unit_command_is_fixed_argv(sandbox):
    assert sandbox.commands["unit"] == [sys.executable, "-I", "-B", "-m", "pytest", "-q", "-p", "no:cacheprovider", "-c", os.devnull, "--rootdir", ".", "-o", "pythonpath=."]


def test_workspace_pytest_module_cannot_replace_trusted_test_runner(sandbox):
    sandbox.write_file("pytest.py", "print('FAKE_TEST_SUCCESS')\n")
    sandbox.write_file("test_must_fail.py", "def test_failure():\n    assert False, 'REAL_TEST_ORACLE'\n")
    result = sandbox.run_test("unit")
    assert result["exit_code"] == 1
    assert "REAL_TEST_ORACLE" in result["output"]
    assert "FAKE_TEST_SUCCESS" not in result["output"]


def test_manifest_includes_configuration_and_detects_file_removal(sandbox):
    sandbox.write_file("src/app.py", "answer=42")
    sandbox.write_file("pyproject.toml", "[project]\nname='example'\n")
    before = sandbox.fingerprint()
    assert set(sandbox.manifest()) == {"pyproject.toml", "src/app.py"}
    (sandbox.root / "pyproject.toml").unlink()
    assert sandbox.fingerprint() != before


def test_manifest_fingerprints_links_without_reading_target(sandbox, tmp_path):
    sandbox.write_file("file.txt", "public")
    (sandbox.root / "link.txt").symlink_to(sandbox.root / "file.txt")
    (sandbox.root / "directory-link").symlink_to(tmp_path.parent, target_is_directory=True)
    os.link(sandbox.root / "file.txt", sandbox.root / "hardlink.txt")
    os.mkfifo(sandbox.root / "fifo")
    manifest = sandbox.manifest()
    assert set(manifest) == {"directory-link", "file.txt", "link.txt", "hardlink.txt", "fifo"}
    before = sandbox.fingerprint()
    (sandbox.root / "link.txt").unlink()
    (sandbox.root / "link.txt").symlink_to("missing-target")
    assert sandbox.fingerprint() != before


def test_read_limit_rejects_large_files_without_silent_truncation(tmp_path):
    sandbox = Sandbox(tmp_path, max_output_bytes=8)
    sandbox.write_file("large.txt", "123456789")
    with pytest.raises(SandboxError) as caught:
        sandbox.read_file("large.txt")
    assert caught.value.code == "output_limit"
