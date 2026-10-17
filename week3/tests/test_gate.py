"""G1-G4 and the handler-side evidence for S1-S3/A1."""

from unittest.mock import Mock

import pytest

from guarded_harness.gate import HarnessStop
from guarded_harness.harness import Harness
from guarded_harness.models import FakeModel


@pytest.fixture
def harness(tmp_path):
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    (workspace / "app.py").write_text("VALUE = 1\n", encoding="utf-8")
    return Harness(workspace, tmp_path / "run", FakeModel([]), task="Understand app")


@pytest.mark.parametrize("arguments", [{}, {"path": 1}, {"path": None},
                                           {"path": "app.py", "extra": "ignored?"},
                                           ["app.py"]])
def test_g1_invalid_arguments_never_reach_handler(harness, arguments):
    handler = Mock()
    harness.gate.handlers["read_file"] = handler
    result = harness.gate.invoke("read_file", arguments, "invalid")
    assert result["ok"] is False
    assert result["error"]["code"] == "INVALID_ARGUMENTS"
    assert result["error"]["message"]
    handler.assert_not_called()
    record = next(r for r in harness.audit.read() if r["rule"] == "INVALID_ARGUMENTS")
    assert record["run_id"] == harness.state.run_id
    assert record["toolCallId"] == record["tool_call_id"] == "invalid"
    assert record["actor"] == "model"
    assert record["decision"] == "deny"


def test_g2_third_consecutive_identical_action_is_blocked_before_handler(harness):
    handler = Mock(wraps=harness.sandbox.read_file)
    harness.gate.handlers["read_file"] = handler
    results = [harness.gate.invoke("read_file", {"path": "app.py"}, f"read-{i}")
               for i in range(3)]
    assert [r["ok"] for r in results] == [True, True, False]
    assert results[-1]["error"]["code"] == "REPEATED_ACTION"
    assert handler.call_count == 2
    assert harness.checkpoint.load().repeat_count == 3


def test_different_action_resets_consecutive_repeat_counter(harness):
    for call_id in ("read-1", "read-2"):
        assert harness.gate.invoke("read_file", {"path": "app.py"}, call_id)["ok"]
    assert harness.gate.invoke("list_files", {}, "list")["ok"]
    assert harness.gate.invoke("read_file", {"path": "app.py"}, "read-3")["ok"]
    assert harness.state.repeat_count == 1


def test_g3_unapproved_write_waits_and_leaves_file_unchanged(harness):
    target = harness.workspace / "app.py"
    before = target.read_bytes()
    handler = Mock(wraps=harness.sandbox.write_file)
    harness.gate.handlers["write_file"] = handler
    with pytest.raises(HarnessStop, match="APPROVAL_REQUIRED"):
        harness.gate.invoke("write_file", {"path": "app.py", "content": "VALUE = 2\n"}, "write")
    assert harness.state.status == "WAITING_APPROVAL"
    assert harness.checkpoint.load().pending["tool_call_id"] == "write"
    assert target.read_bytes() == before
    handler.assert_not_called()


def test_g4_approved_call_executes_once_and_duplicate_is_rejected(harness):
    arguments = {"path": "app.py", "content": "VALUE = 2\n"}
    handler = Mock(wraps=harness.sandbox.write_file)
    harness.gate.handlers["write_file"] = handler
    with pytest.raises(HarnessStop):
        harness.gate.invoke("write_file", arguments, "write")
    harness.approve("write", actor="human-reviewer")
    assert harness.gate.invoke("write_file", arguments, "write", approved_replay=True)["ok"]
    result = harness.gate.invoke("write_file", arguments, "write")
    assert result["error"]["code"] == "DUPLICATE_CALL"
    assert handler.call_count == 1
    assert harness.state.approvals["write"]["used"] is True
    assert (harness.workspace / "app.py").read_text() == "VALUE = 2\n"


def test_approval_cannot_be_reused_for_changed_arguments(harness):
    with pytest.raises(HarnessStop):
        harness.gate.invoke("write_file", {"path": "app.py", "content": "VALUE = 2\n"}, "write")
    harness.approve("write", actor="human-reviewer")
    handler = Mock(wraps=harness.sandbox.write_file)
    harness.gate.handlers["write_file"] = handler
    result = harness.gate.invoke("write_file", {"path": "app.py", "content": "VALUE = 99\n"},
                                 "write", approved_replay=True)
    assert result["error"]["code"] == "INVALID_APPROVAL"
    handler.assert_not_called()
    assert (harness.workspace / "app.py").read_text() == "VALUE = 1\n"


@pytest.mark.parametrize("path,code", [("artifacts/../../.env", "path_escape"),
                                       (".env", "sensitive_path"),
                                       (".ssh/id_rsa", "sensitive_path")])
def test_s1_s2_unsafe_path_is_rejected_before_handler_or_approval(harness, path, code):
    handler = Mock()
    harness.gate.handlers["write_file"] = handler
    result = harness.gate.invoke("write_file", {"path": path, "content": "forbidden"}, "escape")
    assert result["error"]["code"] == code
    assert harness.state.status == "RUNNING"
    assert harness.state.pending is None
    handler.assert_not_called()
    record = next(r for r in harness.audit.read() if r["tool_call_id"] == "escape")
    assert (record["event"], record["decision"], record["rule"]) == ("sandbox", "deny", code)


def test_s2_symlink_escape_is_rejected_and_audited(tmp_path):
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    secret = tmp_path / "outside.txt"
    secret.write_text("outside", encoding="utf-8")
    (workspace / "link.txt").symlink_to(secret)
    harness = Harness(workspace, tmp_path / "run", FakeModel([]), task="Inspect")
    handler = Mock()
    harness.gate.handlers["read_file"] = handler
    result = harness.gate.invoke("read_file", {"path": "link.txt"}, "symlink")
    assert result["error"]["code"] == "symlink_denied"
    handler.assert_not_called()
    assert any(r["event"] == "sandbox" and r["rule"] == "symlink_denied"
               and r["tool_call_id"] == "symlink" for r in harness.audit.read())


@pytest.mark.parametrize("command", ["rm -rf /", "unit; touch marker", "python", ""])
def test_s3_unconfigured_command_never_reaches_handler(harness, command):
    handler = Mock()
    harness.gate.handlers["run_test"] = handler
    result = harness.gate.invoke("run_test", {"command": command}, "command")
    assert result["error"]["code"] == "command_denied"
    handler.assert_not_called()


def test_missing_capability_is_denied_before_handler(harness):
    harness.gate.permissions.remove("test")
    handler = Mock()
    harness.gate.handlers["run_test"] = handler
    result = harness.gate.invoke("run_test", {"command": "unit"}, "test")
    assert result["error"]["code"] == "PERMISSION_DENIED"
    handler.assert_not_called()


@pytest.mark.parametrize("path", ["tests/test_app.py", "test_app.py", "conftest.py", "pytest.ini", "pyproject.toml",
                                  "app_test.py", "tox.ini", "pytest.py", "sitecustomize.py", "usercustomize.py",
                                  "pytest/__init__.py"])
def test_test_oracle_is_protected_before_approval(harness, path):
    handler = Mock()
    harness.gate.handlers["write_file"] = handler
    result = harness.gate.invoke("write_file", {"path": path, "content": "pass\n"}, "oracle")
    assert result["error"]["code"] == "PROTECTED_TEST"
    assert harness.state.pending is None
    handler.assert_not_called()
