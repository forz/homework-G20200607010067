"""C2-C4 use real process boundaries and durable on-disk state."""

import json
import os
from pathlib import Path
import subprocess
import sys

import pytest

from guarded_harness.audit import AuditLog
from guarded_harness.checkpoint import Checkpoint
from guarded_harness.harness import Harness
from guarded_harness.models import FakeModel


ROOT = Path(__file__).resolve().parents[1]
REPORT = "# Repair\nThe add function subtracted its inputs. It now returns their sum, and the positive-input regression test passes.\n"


def make_workspace(tmp_path):
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    (workspace / "app.py").write_text("def add(a, b):\n    return a - b\n", encoding="utf-8")
    (workspace / "test_app.py").write_text("from app import add\n\ndef test_add():\n    assert add(2, 3) == 5\n", encoding="utf-8")
    return workspace


def child(code, workspace, run_dir):
    environment = dict(os.environ)
    environment["PYTHONPATH"] = str(ROOT)
    environment["PYTEST_DISABLE_PLUGIN_AUTOLOAD"] = "1"
    return subprocess.run([sys.executable, "-c", code, str(workspace), str(run_dir)],
                          cwd=ROOT, env=environment, capture_output=True, text=True, timeout=30)


PREAMBLE = '''
import json, os, sys
from pathlib import Path
from guarded_harness.harness import Harness
from guarded_harness.models import FakeModel
workspace, run_dir = map(Path, sys.argv[1:])
def a(call_id, name, **arguments):
    return {"id": call_id, "name": name, "arguments": arguments}
'''


def test_c3_new_process_resume_preserves_progress_and_does_not_repeat_write(tmp_path):
    workspace, run_dir = make_workspace(tmp_path), tmp_path / "run"
    first = child(PREAMBLE + f'''
h = Harness(workspace, run_dir, FakeModel([
    a("plan", "plan_task", steps=["inspect", "repair", "report", "test"]),
    a("read", "read_file", path="app.py"),
    a("fix", "write_file", path="app.py", content="def add(a, b):\\n    return a + b\\n"),
    a("report", "write_file", path="artifacts/repair.md", content={REPORT!r}),
]), task="Fix add", kind="test_fix")
assert h.run().status == "WAITING_APPROVAL"
h.approve("fix", "first-reviewer")
assert h.run().status == "WAITING_APPROVAL"
print(json.dumps({{"step": h.state.step, "calls": h.state.calls, "pid": os.getpid()}}))
''', workspace, run_dir)
    assert first.returncode == 0, first.stdout + first.stderr
    saved = Checkpoint(run_dir).load()
    assert (saved.step, saved.calls) == (4, 4)
    assert "fix" in saved.executed and saved.pending["tool_call_id"] == "report"
    source_mtime = (workspace / "app.py").stat().st_mtime_ns
    second = child(PREAMBLE + '''
h = Harness(workspace, run_dir, FakeModel([
    a("fix", "write_file", path="app.py", content="def add(a, b):\\n    return a + b\\n"),
    a("test", "run_test", command="unit"),
    a("finish", "final_answer", answer="The source repair and current tests are verified."),
]), kind="test_fix", resume=True)
assert (h.state.step, h.state.calls) == (4, 4)
h.approve("report", "second-reviewer")
assert h.run().status == "COMPLETED"
print(json.dumps({"step": h.state.step, "calls": h.state.calls, "pid": os.getpid()}))
''', workspace, run_dir)
    assert second.returncode == 0, second.stdout + second.stderr
    completed = Checkpoint(run_dir).load()
    assert (completed.step, completed.calls) == (7, 7)
    assert completed.run_id == saved.run_id
    assert (workspace / "app.py").stat().st_mtime_ns == source_mtime
    records = AuditLog(run_dir / "audit.jsonl").read()
    assert sum(r["event"] == "execution_started" and r["tool_call_id"] == "fix" for r in records) == 1
    assert any(r["rule"] == "DUPLICATE_CALL" and r["tool_call_id"] == "fix" for r in records)
    assert any(r["event"] == "recovery" and r["decision"] == "resume" for r in records)
    assert json.loads(first.stdout.strip().splitlines()[-1])["pid"] != json.loads(second.stdout.strip().splitlines()[-1])["pid"]


def test_c2_os_exit_after_write_recovers_unknown_result_without_replay(tmp_path):
    workspace, run_dir = make_workspace(tmp_path), tmp_path / "run"
    crashed = child(PREAMBLE + '''
h = Harness(workspace, run_dir, FakeModel([
    a("fix", "write_file", path="app.py", content="def add(a, b):\\n    return a + b\\n"),
]), task="Fix add", kind="test_fix", after_write=lambda: os._exit(86))
assert h.run().status == "WAITING_APPROVAL"
h.approve("fix", "reviewer")
h.run()
raise AssertionError("crash injection did not run")
''', workspace, run_dir)
    assert crashed.returncode == 86, crashed.stdout + crashed.stderr
    saved = Checkpoint(run_dir).load()
    assert saved.inflight["tool_call_id"] == "fix"
    assert saved.approvals["fix"]["used"] is True
    assert "fix" not in saved.executed
    assert (workspace / "app.py").read_text() == "def add(a, b):\n    return a + b\n"
    source_mtime = (workspace / "app.py").stat().st_mtime_ns
    resumed = child(PREAMBLE + '''
h = Harness(workspace, run_dir, FakeModel([
    a("fix", "write_file", path="app.py", content="MUST NOT EXECUTE"),
]), kind="test_fix", resume=True)
assert h.run().status == "UNKNOWN_RESULT"
assert h.state.stop_reason == "UNKNOWN_RESULT"
''', workspace, run_dir)
    assert resumed.returncode == 0, resumed.stdout + resumed.stderr
    recovered = Checkpoint(run_dir).load()
    assert (recovered.step, recovered.calls) == (saved.step, saved.calls)
    assert (workspace / "app.py").stat().st_mtime_ns == source_mtime
    records = AuditLog(run_dir / "audit.jsonl").read()
    assert sum(r["event"] == "execution_started" and r["tool_call_id"] == "fix" for r in records) == 1
    assert not any(r["event"] == "execution_result" and r["tool_call_id"] == "fix" for r in records)
    assert any(r["event"] == "recovery" and r["rule"] == "UNKNOWN_RESULT" for r in records)


def test_c4_changed_workspace_invalidates_evidence_and_approval(tmp_path):
    workspace, run_dir = make_workspace(tmp_path), tmp_path / "run"
    actions = [
        {"id": "read", "name": "read_file", "arguments": {"path": "app.py"}},
        {"id": "write", "name": "write_file", "arguments": {"path": "artifacts/analysis.md", "content": REPORT}},
    ]
    harness = Harness(workspace, run_dir, FakeModel(actions), task="Understand source")
    assert harness.run().status == "WAITING_APPROVAL"
    harness.approve("write", "reviewer")
    assert harness.state.evidence and harness.state.approvals
    (workspace / "app.py").write_text("def add(a, b):\n    return a + b + 1\n", encoding="utf-8")
    resumed = Harness(workspace, run_dir, FakeModel([]), resume=True)
    assert resumed.run().status == "WAITING_REVIEW"
    assert resumed.state.stop_reason == "WORKSPACE_CHANGED"
    assert resumed.state.evidence == []
    assert resumed.state.approvals == {}
    assert not (workspace / "artifacts/analysis.md").exists()
    assert resumed.state.calls == harness.state.calls


def test_resume_refuses_changed_execution_policy(tmp_path):
    workspace, run_dir = make_workspace(tmp_path), tmp_path / "run"
    Harness(workspace, run_dir, FakeModel([]), task="Understand source")
    with pytest.raises(ValueError, match="policy mismatch"):
        Harness(workspace, run_dir, FakeModel([]), resume=True, timeout=99)


def test_c4_external_report_edit_invalidates_pending_approved_replacement(tmp_path):
    workspace, run_dir = make_workspace(tmp_path), tmp_path / "run"
    report_path = workspace / "artifacts/analysis.md"
    report_path.parent.mkdir()
    report_path.write_text("Existing report before approval.\n", encoding="utf-8")
    harness = Harness(workspace, run_dir, FakeModel([
        {"id": "report", "name": "write_file", "arguments": {"path": "artifacts/analysis.md", "content": REPORT}},
    ]), task="Update the report")
    assert harness.run().status == "WAITING_APPROVAL"
    harness.approve("report", "reviewer")
    report_path.write_text("A human has edited this report after approval.\n", encoding="utf-8")
    resumed = Harness(workspace, run_dir, FakeModel([]), resume=True)
    assert resumed.run().status == "WAITING_REVIEW"
    assert resumed.state.stop_reason == "WORKSPACE_CHANGED"
    assert resumed.state.approvals == {}
    assert report_path.read_text() == "A human has edited this report after approval.\n"
    assert not any(r["event"] == "execution_started" and r["tool_call_id"] == "report"
                   for r in resumed.audit.read())
