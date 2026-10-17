"""Durable checkpoints and an exclusive lock covering a complete run."""

from __future__ import annotations

from contextlib import contextmanager
import fcntl
import os
from pathlib import Path
from typing import Iterator

from .audit import AuditLog
from .state_store import RunState, StateStore


class CheckpointBusyError(RuntimeError):
    """Another runner already owns this checkpoint directory."""


class Checkpoint(StateStore):
    def __init__(self, directory: Path, audit: AuditLog | None = None) -> None:
        super().__init__(directory, filename="checkpoint.json")
        self.audit = audit

    def save(self, state: RunState) -> None:
        super().save(state)
        if self.audit is not None:
            self.audit.append(state.run_id, "checkpoint_saved", status=state.status,
                              step=state.step, calls=state.calls)

    @contextmanager
    def lock(self) -> Iterator[None]:
        self.directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        descriptor = os.open(self.directory / ".run.lock", os.O_CREAT | os.O_RDWR, 0o600)
        acquired = False
        try:
            try:
                fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
                acquired = True
            except BlockingIOError as exc:
                raise CheckpointBusyError("another process is already running this checkpoint") from exc
            yield
        finally:
            if acquired:
                fcntl.flock(descriptor, fcntl.LOCK_UN)
            os.close(descriptor)
