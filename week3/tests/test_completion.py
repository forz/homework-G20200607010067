"""H1-H3: completion is an evidence check, not the model's assertion."""

import json

import pytest

from guarded_harness.harness import Harness
from guarded_harness.models import FakeModel


def action(call_id, name, **arguments):
    return {"id": call_id, "name": name, "arguments": arguments}


def make_workspace(tmp_path):
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    (workspace / "app.py").write_text("def add(a, b):\n    return a + b\n", encoding="utf-8")
    (workspace / "test_app.py").write_text("from app import add\n\ndef test_add():\n    assert add(2, 3) == 5\n", encoding="utf-8")
    return workspace


def test_h1_final_answer_without_deliverable_is_rejected(tmp_path):
    harness = Harness(make_workspace(tmp_path), tmp_path / "run", FakeModel([
        action("plan", "plan_task", steps=["understand source"]),
        action("read", "read_file", path="app.py"),
        action("premature", "final_answer", answer="Everything is finished."),
    ]), task="Understand add", max_steps=3)
    state = harness.run()
    assert state.status == "STOPPED"
    assert state.stop_reason == "MAX_STEPS"
    assert state.final_answer is None
    assert "premature" not in state.executed
    rejection = next(r for r in state.history if r["tool_call_id"] == "premature")
    assert rejection["result"]["error"]["code"] == "COMPLETION_REJECTED"
    assert "Missing deliverable" in rejection["result"]["error"]["message"]


def test_h2_source_write_invalidates_test_evidence_and_requires_rerun(tmp_path):
    workspace = make_workspace(tmp_path)
    report = "# Repair\nAdd was reviewed and documented. The current source and positive-input test are verified after the source change.\n"
    harness = Harness(workspace, tmp_path / "run", FakeModel([
        action("plan", "plan_task", steps=["inspect", "document", "update source", "verify"]),
        action("read", "read_file", path="app.py"),
        action("report", "write_file", path="artifacts/repair.md", content=report),
        action("old-test", "run_test", command="unit"),
        action("edit", "write_file", path="app.py", content="def add(a, b):\n    # Add the supplied numbers.\n    return a + b\n"),
        action("premature", "final_answer", answer="The old test passed."),
        action("new-test", "run_test", command="unit"),
        action("finish", "final_answer", answer="The current version was tested successfully."),
    ]), task="Document and verify add", kind="test_fix")
    assert harness.run().status == "WAITING_APPROVAL"
    harness.approve("report", "reviewer")
    assert harness.run().status == "WAITING_APPROVAL"
    assert any(e["tool_call_id"] == "old-test" and e["exit_code"] == 0 for e in harness.state.evidence)
    harness.approve("edit", "reviewer")
    state = harness.run()
    assert state.status == "COMPLETED"
    assert "premature" not in state.executed
    rejection = next(r for r in state.history if r["tool_call_id"] == "premature")
    assert rejection["result"]["error"]["code"] == "COMPLETION_REJECTED"
    assert "current workspace" in rejection["result"]["error"]["message"]
    assert not any(e["tool_call_id"] == "old-test" for e in state.evidence)
    assert any(e["tool_call_id"] == "new-test" and e["exit_code"] == 0 for e in state.evidence)
    assert json.loads((harness.run_dir / "diff.json").read_text())["changed_files"] == ["app.py"]
    assert "+    # Add the supplied numbers.\n" in (harness.run_dir / "changes.patch").read_text()


@pytest.mark.parametrize("budgets,reason,steps,calls", [
    ({"max_steps": 2}, "MAX_STEPS", 2, 2),
    ({"max_calls": 1}, "TOOL_BUDGET_EXHAUSTED", 1, 1),
])
def test_h3_budget_exhaustion_has_explicit_non_success_stop(tmp_path, budgets, reason, steps, calls):
    harness = Harness(make_workspace(tmp_path), tmp_path / "run", FakeModel([
        action("read", "read_file", path="app.py"),
        action("list", "list_files"),
        action("unreachable", "final_answer", answer="Done"),
    ]), task="Inspect", **budgets)
    state = harness.run()
    assert state.status == "STOPPED"
    assert state.stop_reason == reason
    assert (state.step, state.calls) == (steps, calls)
    assert state.final_answer is None
    assert "unreachable" not in state.executed
    assert harness.checkpoint.load().stop_reason == reason


def test_failed_tests_cannot_support_completion(tmp_path):
    workspace = make_workspace(tmp_path)
    (workspace / "app.py").write_text("def add(a, b):\n    return a - b\n", encoding="utf-8")
    harness = Harness(workspace, tmp_path / "run", FakeModel([
        action("plan", "plan_task", steps=["inspect", "report", "test"]),
        action("read", "read_file", path="app.py"),
        action("report", "write_file", path="artifacts/analysis.md", content="# Findings\nThe add function subtracts its inputs and does not satisfy its positive input test.\n"),
        action("failed-test", "run_test", command="unit"),
        action("premature", "final_answer", answer="Done despite a test failure."),
    ]), task="Inspect", max_steps=5)
    assert harness.run().status == "WAITING_APPROVAL"
    harness.approve("report", "reviewer")
    state = harness.run()
    assert state.status != "COMPLETED"
    assert any(e["tool_call_id"] == "failed-test" and e["exit_code"] != 0 for e in state.evidence)
    assert any(r["tool_call_id"] == "premature" and r["rule"] == "COMPLETION_REJECTED" for r in harness.audit.read())


def test_source_change_during_test_cannot_become_passing_evidence(tmp_path):
    workspace = make_workspace(tmp_path)
    harness = Harness(workspace, tmp_path / "run", FakeModel([
        action("plan", "plan_task", steps=["inspect", "test"]),
        action("read", "read_file", path="app.py"),
        action("racing-test", "run_test", command="unit"),
        action("unreachable", "final_answer", answer="The previous code passed."),
    ]), task="Inspect and test", max_steps=4)

    def test_handler(command):
        assert command == "unit"
        (workspace / "app.py").write_text("def add(a, b):\n    return a - b\n", encoding="utf-8")
        return {"exit_code": 0, "output": "1 passed", "timed_out": False, "cancelled": False}

    harness.gate.handlers["run_test"] = test_handler
    state = harness.run()
    assert state.status == "WAITING_REVIEW"
    assert state.stop_reason == "WORKSPACE_CHANGED_DURING_TEST"
    assert state.evidence == []
    assert state.approvals == {}
    assert "unreachable" not in state.executed
    assert "racing-test" not in state.executed
    assert state.final_answer is None


def test_source_change_during_final_check_cannot_mark_run_completed(tmp_path):
    workspace = make_workspace(tmp_path)
    harness = Harness(workspace, tmp_path / "run", FakeModel([
        action("plan", "plan_task", steps=["inspect", "report", "test"]),
        action("read", "read_file", path="app.py"),
        action("report", "write_file", path="artifacts/analysis.md",
               content="# Findings\nThe add function sums its inputs and test_add checks its result for positive integer inputs.\n"),
        action("test", "run_test", command="unit"),
        action("finish", "final_answer", answer="Current code and tests have been reviewed."),
    ]), task="Understand add")
    completion_results = []

    def completion_handler(answer):
        result = harness._complete(answer)
        completion_results.append(result)
        (workspace / "app.py").write_text("def add(a, b):\n    return a - b\n", encoding="utf-8")
        return result

    harness.gate.handlers["final_answer"] = completion_handler
    assert harness.run().status == "WAITING_APPROVAL"
    harness.approve("report", "reviewer")
    state = harness.run()
    assert completion_results[0]["accepted"] is True
    assert state.status == "WAITING_REVIEW"
    assert state.stop_reason == "WORKSPACE_CHANGED_DURING_TOOL"
    assert state.evidence == []
    assert state.approvals == {}
    assert "finish" not in state.executed
    assert state.final_answer is None
