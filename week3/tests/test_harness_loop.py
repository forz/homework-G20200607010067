"""Real upstream ToolCallingAgent loops driven by a deterministic FakeModel."""

import json
from unittest.mock import Mock

import pytest
from smolagents import ToolCallingAgent

from guarded_harness.harness import Harness
from guarded_harness.models import FakeModel


def action(tool_call_id, name, **arguments):
    return {"id": tool_call_id, "name": name, "arguments": arguments}


def make_workspace(tmp_path):
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    (workspace / "app.py").write_text("def add(a, b):\n    return a + b\n", encoding="utf-8")
    (workspace / "test_app.py").write_text("from app import add\n\ndef test_add():\n    assert add(2, 3) == 5\n", encoding="utf-8")
    return workspace


def test_complete_understanding_task_uses_real_agent_and_correlates_audit(tmp_path):
    workspace = make_workspace(tmp_path)
    report = "# Code understanding\nThe add function returns the sum of both inputs. test_add covers two positive numbers.\n"
    model = FakeModel([
        action("plan", "plan_task", steps=["read source", "record findings", "run tests"]),
        action("read", "read_file", path="app.py"),
        action("report", "write_file", path="artifacts/analysis.md", content=report),
        action("test", "run_test", command="unit"),
        action("finish", "final_answer", answer="The code and its positive-number test are explained in artifacts/analysis.md."),
    ])
    harness = Harness(workspace, tmp_path / "run", model, task="Explain add")
    assert isinstance(harness.agent, ToolCallingAgent)
    before = (workspace / "app.py").read_bytes()
    assert harness.run().status == "WAITING_APPROVAL"
    assert not (workspace / "artifacts/analysis.md").exists()
    harness.approve("report", actor="reviewer")
    state = harness.run()
    assert state.status == state.stop_reason == "COMPLETED"
    assert state.step == state.calls == 5
    assert set(state.executed) == {"plan", "read", "report", "test", "finish"}
    assert (workspace / "app.py").read_bytes() == before
    assert (workspace / "artifacts/analysis.md").read_text() == report
    assert json.loads((harness.run_dir / "diff.json").read_text())["changed_files"] == []
    assert any(e["tool"] == "run_test" and e["exit_code"] == 0 and "1 passed" in e["output"] for e in state.evidence)
    records = harness.audit.read(state.run_id)
    report_records = [r for r in records if r["tool_call_id"] == "report"]
    assert {"gate", "sandbox", "approval", "execution_started", "execution_result"} <= {r["event"] for r in report_records}
    assert any(r["event"] == "checkpoint_saved" for r in records)
    assert sum(r["event"] == "execution_started" for r in report_records) == 1
    assert any(r["event"] == "approval" and r["actor"] == "reviewer" for r in report_records)
    assert harness.audit.verify()
    assert harness.checkpoint.load().to_dict() == state.to_dict()


def test_invalid_model_tool_arguments_still_go_through_gate(tmp_path):
    workspace = make_workspace(tmp_path)
    harness = Harness(workspace, tmp_path / "run", FakeModel([
        action("bad", "read_file", path=123),
        action("valid", "read_file", path="app.py"),
    ]), task="Read source", max_steps=2)
    handler = Mock(wraps=harness.sandbox.read_file)
    harness.gate.handlers["read_file"] = handler
    state = harness.run()
    assert state.status == "STOPPED"
    assert state.stop_reason == "MAX_STEPS"
    assert handler.call_count == 1
    assert "bad" not in state.executed and "valid" in state.executed
    assert any(r["tool_call_id"] == "bad" and r["rule"] == "INVALID_ARGUMENTS" for r in harness.audit.read())


@pytest.mark.parametrize("arguments", [[], 0, False, None])
def test_falsy_non_object_arguments_are_not_normalized_to_valid_empty_args(tmp_path, arguments):
    harness = Harness(make_workspace(tmp_path), tmp_path / "run", FakeModel([
        {"id": "bad-empty", "name": "list_files", "arguments": arguments},
    ]), task="List source files", max_steps=1)
    handler = Mock()
    harness.gate.handlers["list_files"] = handler
    state = harness.run()
    handler.assert_not_called()
    assert state.status != "COMPLETED"
    assert "bad-empty" not in state.executed
    assert any(r["tool_call_id"] == "bad-empty" and r["rule"] == "INVALID_ARGUMENTS"
               for r in harness.audit.read())


def test_parallel_model_calls_are_rejected_before_any_handler(tmp_path):
    workspace = make_workspace(tmp_path)
    harness = Harness(workspace, tmp_path / "run", FakeModel([[
        action("one", "read_file", path="app.py"),
        action("two", "list_files"),
    ]]), task="Read source", max_steps=1)
    read, listing = Mock(), Mock()
    harness.gate.handlers["read_file"] = read
    harness.gate.handlers["list_files"] = listing
    state = harness.run()
    read.assert_not_called()
    listing.assert_not_called()
    assert state.status != "COMPLETED"
    assert len([r for r in harness.audit.read() if r["rule"] == "ONE_CALL_PER_STEP"]) == 2


def test_model_cannot_call_an_approval_tool(tmp_path):
    workspace = make_workspace(tmp_path)
    harness = Harness(workspace, tmp_path / "run", FakeModel([
        action("approve", "approve", call_id="write", actor="model"),
    ]), task="Inspect", max_steps=1)
    state = harness.run()
    assert state.approvals == {}
    assert any(r["tool_call_id"] == "approve" and r["rule"] == "UNKNOWN_TOOL" for r in harness.audit.read())


def test_stale_harness_cannot_overwrite_newer_checkpoint(tmp_path):
    workspace = make_workspace(tmp_path)
    first = Harness(workspace, tmp_path / "run", FakeModel([action("read", "read_file", path="app.py")]),
                    task="Read", max_steps=1)
    second = Harness(workspace, tmp_path / "run", FakeModel([]), resume=True)
    first.run()
    with pytest.raises(RuntimeError, match="stale Harness"):
        second.run()
