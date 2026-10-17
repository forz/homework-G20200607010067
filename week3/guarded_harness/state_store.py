"""Validated run state and atomic, durable JSON persistence."""

from __future__ import annotations

from contextlib import contextmanager
from dataclasses import asdict, dataclass, field, fields
import fcntl
import json
import math
import os
from pathlib import Path
import tempfile
from typing import Any, Iterator


class StateValidationError(ValueError):
    """The serialized state does not satisfy the run-state schema."""


def _json_value(value: Any, location: str) -> None:
    if value is None or isinstance(value, (str, bool, int)):
        return
    if isinstance(value, float) and math.isfinite(value):
        return
    if isinstance(value, list):
        for index, item in enumerate(value):
            _json_value(item, f"{location}[{index}]")
        return
    if isinstance(value, dict) and all(isinstance(key, str) for key in value):
        for key, item in value.items():
            _json_value(item, f"{location}.{key}")
        return
    raise StateValidationError(f"{location} must contain JSON-compatible values")


@dataclass
class RunState:
    run_id: str
    task: str
    kind: str
    workspace: str
    workspace_hash: str
    status: str = "RUNNING"
    stop_reason: str | None = None
    step: int = 0
    max_steps: int = 20
    calls: int = 0
    max_calls: int = 40
    plan: list[str] = field(default_factory=list)
    evidence: list[dict] = field(default_factory=list)
    pending: dict | None = None
    approvals: dict = field(default_factory=dict)
    executed: dict = field(default_factory=dict)
    inflight: dict | None = None
    history: list[dict] = field(default_factory=list)
    last_signature: str = ""
    repeat_count: int = 0
    initial_manifest: dict = field(default_factory=dict)
    policy: dict = field(default_factory=dict)
    final_answer: str | None = None

    def _validate(self) -> None:
        for name in ("run_id", "task", "kind", "workspace", "workspace_hash", "status"):
            value = getattr(self, name)
            if not isinstance(value, str) or not value.strip():
                raise StateValidationError(f"{name} must be a non-empty string")
        if not isinstance(self.last_signature, str):
            raise StateValidationError("last_signature must be a string")
        for name in ("stop_reason", "final_answer"):
            value = getattr(self, name)
            if value is not None and not isinstance(value, str):
                raise StateValidationError(f"{name} must be a string or null")
        for name in ("step", "calls", "repeat_count", "max_steps", "max_calls"):
            value = getattr(self, name)
            minimum = 1 if name.startswith("max_") else 0
            if type(value) is not int or value < minimum:
                raise StateValidationError(f"{name} must be an integer >= {minimum}")
        if not isinstance(self.plan, list) or not all(isinstance(item, str) for item in self.plan):
            raise StateValidationError("plan must be a list of strings")
        for name in ("evidence", "history"):
            value = getattr(self, name)
            if not isinstance(value, list) or not all(isinstance(item, dict) for item in value):
                raise StateValidationError(f"{name} must be a list of objects")
        for name in ("approvals", "executed", "initial_manifest", "policy"):
            if not isinstance(getattr(self, name), dict):
                raise StateValidationError(f"{name} must be an object")
        for name in ("pending", "inflight"):
            value = getattr(self, name)
            if value is not None and not isinstance(value, dict):
                raise StateValidationError(f"{name} must be an object or null")
        for item in fields(self):
            _json_value(getattr(self, item.name), item.name)

    def to_dict(self) -> dict:
        self._validate()
        return asdict(self)

    @classmethod
    def from_dict(cls, data: dict) -> RunState:
        if not isinstance(data, dict):
            raise StateValidationError("run state must be an object")
        known = {item.name for item in fields(cls)}
        if set(data) - known:
            raise StateValidationError("run state contains unknown fields")
        try:
            state = cls(**data)
        except TypeError as exc:
            raise StateValidationError("run state is missing required fields") from exc
        state._validate()
        return state


def _unique_object(pairs: list[tuple[str, Any]]) -> dict:
    result = {}
    for key, value in pairs:
        if key in result:
            raise StateValidationError("JSON contains duplicate object keys")
        result[key] = value
    return result


def fsync_directory(directory: Path) -> None:
    descriptor = os.open(directory, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


class StateStore:
    """Keep state outside the editable workspace and replace it atomically."""

    def __init__(self, directory: Path, *, filename: str = "state.json") -> None:
        if Path(filename).name != filename:
            raise ValueError("state filename must be a plain filename")
        self.directory = Path(directory).expanduser().resolve()
        self.path = self.directory / filename
        self._lock_path = self.directory / f".{filename}.lock"

    def _check_location(self, state: RunState) -> None:
        workspace = Path(state.workspace).expanduser().resolve()
        if self.directory == workspace or self.directory.is_relative_to(workspace):
            raise StateValidationError("state directory must be outside the workspace")

    @contextmanager
    def _locked(self, *, shared: bool = False) -> Iterator[None]:
        self.directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        descriptor = os.open(self._lock_path, os.O_CREAT | os.O_RDWR, 0o600)
        try:
            fcntl.flock(descriptor, fcntl.LOCK_SH if shared else fcntl.LOCK_EX)
            yield
        finally:
            fcntl.flock(descriptor, fcntl.LOCK_UN)
            os.close(descriptor)

    def save(self, state: RunState) -> None:
        payload = json.dumps(state.to_dict(), ensure_ascii=False, sort_keys=True, indent=2,
                             allow_nan=False) + "\n"
        self._check_location(state)
        with self._locked():
            descriptor, temporary_name = tempfile.mkstemp(prefix=f".{self.path.name}.",
                                                          suffix=".tmp", dir=self.directory)
            temporary_path = Path(temporary_name)
            try:
                with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
                    stream.write(payload)
                    stream.flush()
                    os.fsync(stream.fileno())
                os.replace(temporary_path, self.path)
                fsync_directory(self.directory)
            finally:
                temporary_path.unlink(missing_ok=True)

    def load(self) -> RunState:
        with self._locked(shared=True):
            try:
                data = json.loads(self.path.read_text(encoding="utf-8"),
                                  object_pairs_hook=_unique_object)
            except (json.JSONDecodeError, UnicodeDecodeError) as exc:
                raise StateValidationError("state file is not valid JSON") from exc
            state = RunState.from_dict(data)
            self._check_location(state)
            return state
