"""One independent-process repair proves the acceptance runner's durable records."""

import importlib.util
import json
from pathlib import Path
import subprocess

import pytest


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "run_acceptance.py"
spec = importlib.util.spec_from_file_location("acceptance_runner", SCRIPT)
acceptance = importlib.util.module_from_spec(spec)
spec.loader.exec_module(acceptance)


def test_offline_repair_cli_records_approval_recovery_diff_and_audit(tmp_path):
    runner = acceptance.AcceptanceRunner(tmp_path / "proof")
    summary = runner.run(skip_pytest=True, cases=("test_fix",))
    assert summary["passed"], summary.get("error")
    assert summary["pytest"]["status"] == "skipped"
    result = summary["cases"]["test_fix"]
    assert all(result["checks"].values())
    assert result["diff"]["changed_files"] == ["calculator.py"]
    assert result["state"]["completed_write_count"] == 2
    assert result["state"]["step"] == result["state"]["calls"] == 8
    recovery = json.loads((runner.output / "recovery.json").read_text())
    assert len(recovery) == 2
    assert [(item["before"]["completed_write_count"], item["after_resume"]["completed_write_count"]) for item in recovery] == [(0, 1), (1, 2)]
    assert all(item["before"]["step"] == item["after_approval"]["step"] for item in recovery)
    assert all(item["completed_calls_preserved"] and item["write_executed_once"] for item in recovery)
    transcript = json.loads((runner.output / "transcript.json").read_text())
    assert transcript[0]["returncode"] == 2
    assert all(item["returncode"] == item["expected_returncode"] for item in transcript)
    assert all(item["argv"][1:3] == ["-m", "guarded_harness.cli"] for item in transcript)
    assert any(item["phase"].endswith(":verify-audit") and json.loads(item["stdout"])["valid"] for item in transcript)
    assert Path(result["files"]["patch"]).read_text().count("+    return left + right") == 1
    assert Path(result["files"]["report"]).read_text() == acceptance.REPAIR
    audit = [json.loads(line) for line in Path(result["files"]["audit"]).read_text().splitlines()]
    baseline = [item for item in audit if item["event"] == "execution_result" and item["tool_call_id"] == "fix-test-before"]
    assert baseline[0]["details"]["result"]["exit_code"] == 1


def test_existing_nonempty_output_is_refused_without_mutation(tmp_path):
    marker = tmp_path / "keep.txt"
    marker.write_text("keep")
    with pytest.raises(acceptance.AcceptanceFailure, match="not empty"):
        acceptance.AcceptanceRunner(tmp_path)
    assert list(tmp_path.iterdir()) == [marker]
    assert marker.read_text() == "keep"


def test_fixture_reviewer_rejects_any_unexpected_write(tmp_path):
    runner = acceptance.AcceptanceRunner(tmp_path / "proof")
    workspace = runner.output / "fixture"
    workspace.mkdir()
    (workspace / "source.py").write_text("original")
    expected = acceptance.action("write", "write_file", path="source.py", content="approved exact text")
    state = {"status": "WAITING_APPROVAL", "workspace": str(workspace),
             "pending": {"tool_call_id": "write", "name": "write_file", "arguments": {"path": "source.py", "content": "different content"}}}
    with pytest.raises(acceptance.AcceptanceFailure, match="unexpected pending write"):
        runner._review(state, workspace, expected, {"source.py": "original"})
    assert (workspace / "source.py").read_text() == "original"


@pytest.mark.parametrize("empty_file", [False, True])
def test_verify_audit_cli_rejects_missing_or_empty_evidence(tmp_path, empty_file):
    run_dir = tmp_path / "run"
    run_dir.mkdir()
    if empty_file:
        (run_dir / "audit.jsonl").touch()
    result = subprocess.run([str(acceptance.PYTHON), "-m", "guarded_harness.cli", "verify-audit",
                             "--run-dir", str(run_dir)], cwd=acceptance.REPO,
                            capture_output=True, text=True, check=False, timeout=30)
    assert result.returncode == 1
    assert json.loads(result.stdout)["valid"] is False
