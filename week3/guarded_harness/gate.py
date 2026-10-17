"""The sole tool-dispatch boundary; no business handler is registered on the agent."""

from __future__ import annotations

import hashlib
import json
import threading
from pathlib import PurePosixPath
from typing import Callable

from .sandbox import SandboxError


class HarnessStop(BaseException):
    """Control flow that smolagents must not convert into a retryable tool error."""


SCHEMAS = {
    "plan_task": {"steps": list},
    "list_files": {},
    "read_file": {"path": str},
    "write_file": {"path": str, "content": str},
    "run_test": {"command": str},
    "final_answer": {"answer": str},
}
PERMISSIONS = {
    "plan_task": "read", "list_files": "read", "read_file": "read",
    "write_file": "write", "run_test": "test", "final_answer": "read",
}


def signature(name: str, arguments: dict) -> str:
    raw = json.dumps([name, arguments], sort_keys=True, ensure_ascii=False, separators=(",", ":"))
    return hashlib.sha256(raw.encode()).hexdigest()


class Gate:
    def __init__(self, state, sandbox, checkpoint, audit, completion: Callable,
                 permissions=None, after_write=None, cancel_event=None):
        self.state, self.sandbox = state, sandbox
        self.checkpoint, self.audit = checkpoint, audit
        self.completion = completion
        self.permissions = set(permissions if permissions is not None else ("read", "write", "test"))
        self.after_write = after_write  # Failure injection at the real crash window, only supplied by tests.
        self.cancel_event = cancel_event
        self._mutex = threading.RLock()
        self.handlers = {
            "plan_task": self._plan, "list_files": self._list,
            "read_file": self.sandbox.read_file, "write_file": self.sandbox.write_file,
            "run_test": lambda command: self.sandbox.run_test(command, cancel_event=self.cancel_event),
            "final_answer": self.completion,
        }

    def _plan(self, steps):
        self.state.plan = steps
        return {"steps": steps}

    def _list(self):
        return {"files": list(self.sandbox.manifest())}

    def _event(self, event, call_id, **details):
        return self.audit.append(self.state.run_id, event, tool_call_id=call_id, **details)

    def _save(self):
        self.checkpoint.save(self.state)

    def reject(self, call_id, rule, message, *, actor="model", event="gate", **details):
        result = {"ok": False, "error": {"code": rule, "message": message}, "tool_call_id": call_id}
        self._event(event, call_id, actor=actor, decision="deny", rule=rule, **details)
        self.state.history.append({"tool_call_id": call_id, "result": result})
        self._save()
        return result

    def _stop(self, reason, status="STOPPED"):
        self.state.status, self.state.stop_reason = status, reason
        self._save()
        raise HarnessStop(reason)

    def approve(self, call_id: str, actor: str):
        """Host-side approval API. It is intentionally never a model tool."""
        with self._mutex:
            pending = self.state.pending
            if not actor.strip() or not pending or pending["tool_call_id"] != call_id:
                raise ValueError("approval must identify the pending call and a human actor")
            if self.state.status != "WAITING_APPROVAL":
                raise ValueError("run is not waiting for approval")
            if self.sandbox.fingerprint() != pending["workspace_hash"]:
                self.state.approvals.clear()
                self._stop("WORKSPACE_CHANGED", "WAITING_REVIEW")
            self.state.approvals[call_id] = {
                **pending, "actor": actor, "used": False, "run_id": self.state.run_id,
            }
            self._event("approval", call_id, actor=actor, decision="allow", rule="human_approval",
                        signature=pending["signature"], workspace_hash=pending["workspace_hash"])
            self._save()

    def invoke(self, name, arguments, call_id, *, approved_replay=False):
        with self._mutex:
            return self._invoke(name, arguments, call_id, approved_replay=approved_replay)

    def _invoke(self, name, arguments, call_id, *, approved_replay=False):
        state = self.state
        if not isinstance(call_id, str) or not call_id.strip():
            return self.reject(str(call_id), "INVALID_CALL_ID", "tool call id must be a non-empty string")
        if state.status not in ("RUNNING", "WAITING_APPROVAL"):
            return self.reject(call_id, "RUN_NOT_ACTIVE", f"run status is {state.status}")
        if state.status == "WAITING_APPROVAL" and not approved_replay:
            self._stop("APPROVAL_REQUIRED", "WAITING_APPROVAL")
        if state.calls >= state.max_calls and not approved_replay:
            self._stop("TOOL_BUDGET_EXHAUSTED")
        if not approved_replay:
            state.calls += 1
        if name not in SCHEMAS:
            return self.reject(call_id, "UNKNOWN_TOOL", "tool is not registered")
        fields = SCHEMAS[name]
        if (not isinstance(arguments, dict) or set(arguments) != set(fields)
                or any(type(arguments[key]) is not kind for key, kind in fields.items())):
            return self.reject(call_id, "INVALID_ARGUMENTS", f"expected exactly {list(fields)} with declared types")
        if name == "plan_task" and (not arguments["steps"] or any(type(s) is not str or not s.strip() for s in arguments["steps"])):
            return self.reject(call_id, "INVALID_ARGUMENTS", "steps must contain non-empty strings")
        if any(isinstance(v, str) and len(v.encode()) > 256_000 for v in arguments.values()):
            return self.reject(call_id, "INVALID_ARGUMENTS", "argument exceeds size limit")
        sig = signature(name, arguments)
        if call_id in state.executed:
            return self.reject(call_id, "DUPLICATE_CALL", "this call id has already executed")
        if state.inflight:
            self._stop("UNKNOWN_RESULT", "UNKNOWN_RESULT")
        if not approved_replay:
            state.repeat_count = state.repeat_count + 1 if sig == state.last_signature else 1
            state.last_signature = sig
        if state.repeat_count >= 3:
            return self.reject(call_id, "REPEATED_ACTION", "third consecutive identical action blocked")
        if PERMISSIONS[name] not in self.permissions:
            return self.reject(call_id, "PERMISSION_DENIED", "required tool capability is not granted")
        current = self.sandbox.fingerprint()
        if current != state.workspace_hash:
            state.evidence.clear()
            state.approvals.clear()
            self._event("workspace", call_id, decision="deny", rule="WORKSPACE_CHANGED")
            self._stop("WORKSPACE_CHANGED", "WAITING_REVIEW")
        try:
            if name in {"read_file", "write_file", "run_test"}:
                self.sandbox.validate_call(name, arguments)
        except SandboxError as error:
            return self.reject(call_id, error.code, str(error), event="sandbox")
        self._event("sandbox", call_id, decision="allow", rule="boundary_checked", tool=name)
        if name == "write_file":
            parts = PurePosixPath(arguments["path"]).parts
            if ("tests" in parts or PurePosixPath(arguments["path"]).name.startswith("test_")
                    or PurePosixPath(arguments["path"]).name.endswith("_test.py")
                    or "pytest" in parts
                    or PurePosixPath(arguments["path"]).name in {
                        "conftest.py", "pyproject.toml", "pytest.ini", "setup.cfg", "tox.ini",
                        "pytest.py", "sitecustomize.py", "usercustomize.py"}):
                return self.reject(call_id, "PROTECTED_TEST", "test oracle and test configuration are read-only")
            approval = state.approvals.get(call_id)
            if not approval:
                state.pending = {"tool_call_id": call_id, "name": name, "arguments": arguments,
                                 "signature": sig, "workspace_hash": current}
                state.status, state.stop_reason = "WAITING_APPROVAL", "APPROVAL_REQUIRED"
                self._event("gate", call_id, actor="model", decision="wait", rule="approval_required",
                            tool=name, signature=sig, risk="WRITE")
                self._save()
                raise HarnessStop("APPROVAL_REQUIRED")
            if (approval["used"] or approval["signature"] != sig or approval["workspace_hash"] != current
                    or approval["run_id"] != state.run_id):
                return self.reject(call_id, "INVALID_APPROVAL", "approval is consumed or bound to another action/version")
            approval["used"] = True
            state.inflight = {"tool_call_id": call_id, "name": name, "signature": sig, "workspace_hash": current}
            state.status, state.stop_reason = "RUNNING", None
            state.pending = None
        self._event("gate", call_id, actor="model", decision="allow", rule="checks_passed",
                    tool=name, signature=sig, risk="WRITE" if name == "write_file" else "READ_OR_TEST")
        self._save()  # Write intent and consumed approval must reach disk BEFORE executing the handler.
        self._event("execution_started", call_id, tool=name)
        try:
            output = self.handlers[name](**arguments)
        except Exception as error:
            self._event("execution_failed", call_id, tool=name, error_type=type(error).__name__)
            if name == "write_file":
                self._stop("UNKNOWN_RESULT", "UNKNOWN_RESULT")
            return self.reject(call_id, getattr(error, "code", "HANDLER_ERROR"), str(error))
        if name == "write_file" and self.after_write:
            self.after_write()  # BaseException/os._exit simulate a process dying before result persistence.
        if name != "write_file" and self.sandbox.fingerprint() != current:
            state.evidence.clear()
            state.approvals.clear()
            reason = "WORKSPACE_CHANGED_DURING_TEST" if name == "run_test" else "WORKSPACE_CHANGED_DURING_TOOL"
            self._event("execution_result", call_id, decision="deny", rule=reason,
                        tool=name, result=output)
            self._stop(reason, "WAITING_REVIEW")
        if name == "final_answer" and not output.get("accepted"):
            return self.reject(call_id, "COMPLETION_REJECTED", output["reason"])
        state.workspace_hash = self.sandbox.fingerprint()
        evidence = {"tool_call_id": call_id, "tool": name, "workspace_hash": state.workspace_hash}
        if name == "run_test":
            evidence.update(output)
        elif name in ("read_file", "write_file"):
            evidence["path"] = arguments["path"]
            if name == "write_file":
                evidence["sha256"] = hashlib.sha256(arguments["content"].encode()).hexdigest()
                state.evidence = [e for e in state.evidence if e.get("tool") != "run_test"]
        state.evidence.append(evidence)
        result = {"ok": True, "tool_call_id": call_id, "output": output}
        state.executed[call_id] = {"signature": sig, "tool": name}
        state.inflight = None
        state.history.append({"tool_call_id": call_id, "tool": name, "result": result})
        if name == "final_answer":
            state.status, state.stop_reason = "COMPLETED", "COMPLETED"
            state.final_answer = arguments["answer"]
        self._event("execution_result", call_id, decision="success", tool=name,
                    workspace_hash=state.workspace_hash, result=output)
        self._save()
        return result
