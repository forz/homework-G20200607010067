"""Locked, append-only JSONL audit records with a verifiable hash chain."""

from __future__ import annotations

from datetime import datetime, timezone
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
from typing import Any

from .state_store import fsync_directory


class AuditIntegrityError(ValueError):
    """Audit records are malformed or the hash chain has been modified."""


_GENESIS = "0" * 64
_SENSITIVE_KEYS = {"apikey", "token", "password", "secret", "authorization"}
_BEARER = re.compile(r"\bBearer\s+[A-Za-z0-9._~+/=-]+", re.IGNORECASE)
_API_KEY = re.compile(r"\bsk-[A-Za-z0-9_-]{6,}\b")


def redact(value: Any) -> Any:
    """Preserve useful structure while removing credentials before persistence."""
    if isinstance(value, dict):
        return {
            key: "[REDACTED]" if any(re.sub(r"[_\-\s]", "", str(key)).lower().endswith(suffix)
                                     for suffix in _SENSITIVE_KEYS)
            else redact(item)
            for key, item in value.items()
        }
    if isinstance(value, (list, tuple)):
        return [redact(item) for item in value]
    if isinstance(value, str):
        return _API_KEY.sub("[REDACTED]", _BEARER.sub("Bearer [REDACTED]", value))
    return value


def _record_hash(record: dict) -> str:
    payload = {key: value for key, value in record.items() if key != "hash"}
    encoded = json.dumps(payload, sort_keys=True, ensure_ascii=False, separators=(",", ":"),
                         allow_nan=False).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def _unique_object(pairs: list[tuple[str, Any]]) -> dict:
    result = {}
    for key, value in pairs:
        if key in result:
            raise AuditIntegrityError("audit JSON contains duplicate object keys")
        result[key] = value
    return result


def _read_records(text: str) -> list[dict]:
    if not text:
        return []
    if not text.endswith("\n"):
        raise AuditIntegrityError("audit log has a truncated final record")
    records = []
    previous_hash = _GENESIS
    for sequence, line in enumerate(text.splitlines(), 1):
        try:
            record = json.loads(line, object_pairs_hook=_unique_object)
            if not isinstance(record, dict):
                raise AuditIntegrityError("audit record must be an object")
            if type(record.get("seq")) is not int or record["seq"] != sequence:
                raise AuditIntegrityError("audit sequence is invalid")
            if record.get("prev_hash") != previous_hash or record.get("hash") != _record_hash(record):
                raise AuditIntegrityError("audit hash chain is invalid")
            required = {"run_id", "tool_call_id", "toolCallId", "actor", "event", "decision",
                        "rule", "time", "details"}
            if not required <= record.keys() or not isinstance(record["details"], dict):
                raise AuditIntegrityError("audit record schema is invalid")
            if record["tool_call_id"] != record["toolCallId"]:
                raise AuditIntegrityError("audit tool call identifiers disagree")
        except (json.JSONDecodeError, TypeError, ValueError) as exc:
            if isinstance(exc, AuditIntegrityError):
                raise
            raise AuditIntegrityError(f"invalid audit record at line {sequence}") from exc
        records.append(record)
        previous_hash = record["hash"]
    return records


class AuditLog:
    def __init__(self, path: Path) -> None:
        self.path = Path(path).expanduser().resolve()

    def append(self, run_id: str, event: str, *, tool_call_id: str | None = None,
               actor: str = "system", decision: str | None = None,
               rule: str | None = None, **details: Any) -> dict:
        for label, value in (("run_id", run_id), ("event", event), ("actor", actor)):
            if not isinstance(value, str) or not value:
                raise ValueError(f"{label} must be a non-empty string")
        self.path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        descriptor = os.open(self.path, os.O_CREAT | os.O_RDWR | os.O_APPEND, 0o600)
        with os.fdopen(descriptor, "r+", encoding="utf-8") as stream:
            fcntl.flock(stream.fileno(), fcntl.LOCK_EX)
            try:
                try:
                    records = _read_records(stream.read())
                except UnicodeDecodeError as exc:
                    raise AuditIntegrityError("audit log is not UTF-8") from exc
                record = redact({
                    "seq": len(records) + 1,
                    "prev_hash": records[-1]["hash"] if records else _GENESIS,
                    "run_id": run_id,
                    "tool_call_id": tool_call_id,
                    "toolCallId": tool_call_id,
                    "actor": actor,
                    "event": event,
                    "decision": decision,
                    "rule": rule,
                    "time": datetime.now(timezone.utc).isoformat(),
                    "details": details,
                })
                record["hash"] = _record_hash(record)
                encoded = json.dumps(record, sort_keys=True, ensure_ascii=False, allow_nan=False)
                stream.seek(0, os.SEEK_END)
                stream.write(encoded + "\n")
                stream.flush()
                os.fsync(stream.fileno())
                fsync_directory(self.path.parent)
                return record
            finally:
                fcntl.flock(stream.fileno(), fcntl.LOCK_UN)

    def read(self, run_id: str | None = None) -> list[dict]:
        try:
            stream = self.path.open("r", encoding="utf-8")
        except FileNotFoundError:
            return []
        with stream:
            fcntl.flock(stream.fileno(), fcntl.LOCK_SH)
            try:
                try:
                    records = _read_records(stream.read())
                except UnicodeDecodeError as exc:
                    raise AuditIntegrityError("audit log is not UTF-8") from exc
            finally:
                fcntl.flock(stream.fileno(), fcntl.LOCK_UN)
        return records if run_id is None else [record for record in records if record["run_id"] == run_id]

    def verify(self) -> bool:
        try:
            self.read()
        except (AuditIntegrityError, OSError):
            return False
        return True
