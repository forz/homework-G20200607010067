#!/usr/bin/env python3
"""Reproduce offline CLI acceptance using only this script's disposable fixtures.

The fixture reviewer approves predefined exact writes in copied fixtures. It is
not an approval service for user repositories, and never enables CLI auto-approval.
"""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import time
import uuid


REPO = Path(__file__).resolve().parents[1]
PYTHON = REPO / ".venv" / "bin" / "python"
REVIEWER = "fixture-reviewer:offline-acceptance"
FIXED_SOURCE = '''"""Repaired arithmetic exercise fixture."""


def add(left: int, right: int) -> int:
    return left + right
'''
ANALYSIS = """# Code understanding

calculator.add returns the sum of its two integer arguments. calculator.divide
returns floating-point division and raises ValueError when the denominator is
zero. The tests exercise positive and cancelling addition, fractional division,
and the zero-denominator error. Source and tests remain unchanged. The configured
unit command is run after this approved report to establish current-version evidence.
"""
REPAIR = """# Test repair

The initial add implementation subtracted the right operand, so positive and
negative addition cases failed while the zero case happened to pass. The repair
changes the implementation to left + right. Existing tests are preserved exactly;
they check positive, negative, and zero operands. The configured unit command is
rerun after this report so its result is bound to the final workspace version.
"""


class AcceptanceFailure(RuntimeError):
    pass


def write_json(path: Path, value) -> None:
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def action(identifier: str, name: str, **arguments) -> dict:
    return {"id": identifier, "name": name, "arguments": arguments}


def snapshot(state: dict) -> dict:
    executed = state.get("executed", {})
    writes = sorted(key for key, item in executed.items() if item["tool"] == "write_file")
    return {"status": state["status"], "step": state["step"], "calls": state["calls"],
            "completed_call_ids": sorted(executed), "completed_write_ids": writes,
            "completed_write_count": len(writes), "workspace_hash": state["workspace_hash"]}


class AcceptanceRunner:
    def __init__(self, output: Path):
        requested = Path(output)
        if requested.is_symlink():
            raise AcceptanceFailure("Output must not be a symbolic link")
        self.output = requested.resolve()
        if self.output.exists() and (not self.output.is_dir() or any(self.output.iterdir())):
            raise AcceptanceFailure("Output directory already exists and is not empty")
        self.output.mkdir(parents=True, exist_ok=True)
        self.transcript = []
        self.recovery = []
        self.summary = {"passed": False, "mode": "offline_fake_model", "output": str(self.output),
                        "approval_scope": "Only exact predefined writes in fixtures created by this run",
                        "fixture_reviewer": REVIEWER, "pytest": {"status": "not_run"}, "cases": {}}
        self._save()

    def _save(self):
        write_json(self.output / "transcript.json", self.transcript)
        write_json(self.output / "recovery.json", self.recovery)
        write_json(self.output / "summary.json", self.summary)

    def _execute(self, label: str, argv: list[str], expected: int, timeout=90) -> subprocess.CompletedProcess:
        allowed = {"PATH", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "SYSTEMROOT", "WINDIR"}
        environment = {key: value for key, value in os.environ.items() if key in allowed}
        environment.update({"PYTEST_DISABLE_PLUGIN_AUTOLOAD": "1", "PYTHONDONTWRITEBYTECODE": "1",
                            "PYTHONNOUSERSITE": "1"})
        started = time.monotonic()
        try:
            process = subprocess.run(argv, cwd=REPO, env=environment, text=True, capture_output=True,
                                     timeout=timeout, check=False)
        except subprocess.TimeoutExpired as exc:
            stdout = exc.stdout.decode(errors="replace") if isinstance(exc.stdout, bytes) else exc.stdout or ""
            stderr = exc.stderr.decode(errors="replace") if isinstance(exc.stderr, bytes) else exc.stderr or ""
            self.transcript.append({"phase": label, "argv": argv, "returncode": None,
                                    "expected_returncode": expected, "stdout": stdout, "stderr": stderr,
                                    "timed_out": True, "duration_seconds": round(time.monotonic() - started, 3)})
            if label == "pytest":
                (self.output / "pytest.txt").write_text(stdout + stderr + "\nTimed out.\n", encoding="utf-8")
                self.summary["pytest"] = {"status": "failed", "reason": "timeout"}
            self._save()
            raise AcceptanceFailure(f"{label}: subprocess timed out; see transcript.json") from exc
        self.transcript.append({"phase": label, "argv": argv, "returncode": process.returncode,
                                "expected_returncode": expected, "stdout": process.stdout,
                                "stderr": process.stderr, "duration_seconds": round(time.monotonic() - started, 3)})
        if label == "pytest":
            (self.output / "pytest.txt").write_text(process.stdout + process.stderr, encoding="utf-8")
            self.summary["pytest"] = {"status": "passed" if process.returncode == expected else "failed", "returncode": process.returncode}
        self._save()
        if process.returncode != expected:
            raise AcceptanceFailure(f"{label}: exit {process.returncode}, expected {expected}; see transcript.json")
        return process

    def _cli(self, label: str, arguments: list[str], expected=0) -> dict:
        result = self._execute(label, [str(PYTHON), "-m", "guarded_harness.cli", *arguments], expected)
        try:
            payload = json.loads(result.stdout)
        except json.JSONDecodeError as exc:
            raise AcceptanceFailure(f"{label}: CLI did not return JSON") from exc
        if not isinstance(payload, dict):
            raise AcceptanceFailure(f"{label}: CLI returned an invalid result")
        return payload

    def _inspect(self, kind: str, run_dir: Path, phase: str) -> dict:
        return self._cli(f"{kind}:{phase}:inspect", ["inspect", "--run-dir", str(run_dir)])

    def _review(self, state: dict, workspace: Path, expected_call: dict, expected_files: dict[str, str]):
        """Approve no data-driven proposal beyond the fixed fixture action."""
        pending = state.get("pending")
        if (state["status"] != "WAITING_APPROVAL" or state["workspace"] != str(workspace.resolve())
                or workspace.is_symlink() or not pending or pending["name"] != "write_file"
                or pending["tool_call_id"] != expected_call["id"]
                or pending["arguments"] != expected_call["arguments"]):
            raise AcceptanceFailure("Fixture reviewer refused an unexpected pending write")
        entries = list(workspace.rglob("*"))
        if any(path.is_symlink() for path in entries):
            raise AcceptanceFailure("Fixture reviewer refused a symbolic link in the fixture")
        actual_files = {path.relative_to(workspace).as_posix() for path in entries if path.is_file()}
        if actual_files != set(expected_files):
            raise AcceptanceFailure("Fixture reviewer refused unexpected workspace files")
        for relative, content in expected_files.items():
            candidate = workspace / relative
            if candidate.is_symlink() or candidate.read_text(encoding="utf-8") != content:
                raise AcceptanceFailure("Fixture reviewer refused modified fixture contents")

    def _resume_approved(self, kind: str, run_dir: Path, workspace: Path, state: dict,
                         pending_action: dict, expected_files: dict[str, str], actions: Path,
                         phase: str, expected_status: str) -> dict:
        self._review(state, workspace, pending_action, expected_files)
        before = snapshot(state)
        call_id = pending_action["id"]
        approved = self._cli(f"{kind}:{phase}:approve", ["approve", "--run-dir", str(run_dir),
                             "--call-id", call_id, "--actor", REVIEWER])
        if approved["step"] != before["step"] or approved["calls"] != before["calls"]:
            raise AcceptanceFailure("Approving a pending action reset or consumed execution budgets")
        self._cli(f"{kind}:{phase}:resume", ["resume", "--run-dir", str(run_dir), "--model", "fake",
                  "--actions", str(actions)], expected=0 if expected_status == "COMPLETED" else 2)
        after_state = self._inspect(kind, run_dir, f"{phase}:after_resume")
        after = snapshot(after_state)
        preserved = set(before["completed_call_ids"]) <= set(after["completed_call_ids"])
        if (after["status"] != expected_status or not preserved or after["step"] <= before["step"]
                or after["calls"] <= before["calls"] or after["completed_write_count"] != before["completed_write_count"] + 1
                or call_id not in after["completed_write_ids"]):
            raise AcceptanceFailure("Checkpoint recovery failed its budget or exactly-once write checks")
        self.recovery.append({"case": kind, "approved_call_id": call_id, "before": before,
                              "after_approval": {"step": approved["step"], "calls": approved["calls"]},
                              "after_resume": after, "completed_calls_preserved": preserved,
                              "write_executed_once": True})
        self._save()
        return after_state

    def run_case(self, kind: str) -> dict:
        if kind not in {"code_understanding", "test_fix"}:
            raise AcceptanceFailure("Unsupported fixture case")
        case_dir = self.output / kind
        workspace, run_dir, actions_dir = case_dir / "workspace", case_dir / "run", case_dir / "actions"
        actions_dir.mkdir(parents=True)
        shutil.copytree(REPO / "examples" / ("fix" if kind == "test_fix" else "understand"), workspace,
                        ignore=shutil.ignore_patterns("__pycache__", ".pytest_cache", "*.pyc"))
        expected_files = {path.name: path.read_text(encoding="utf-8") for path in workspace.iterdir() if path.is_file()}
        original_tests = hashlib.sha256((workspace / "test_calculator.py").read_bytes()).hexdigest()
        prefix = "fix" if kind == "test_fix" else "understand"
        report_path = "artifacts/repair.md" if kind == "test_fix" else "artifacts/analysis.md"
        report = REPAIR if kind == "test_fix" else ANALYSIS
        report_action = action(f"{prefix}-write-report", "write_file", path=report_path, content=report)
        first = [action(f"{prefix}-plan", "plan_task", steps=["Read source and tests", "Inspect behavior and prepare the required report", "Run the configured tests on the final workspace"]),
                 action(f"{prefix}-read-source", "read_file", path="calculator.py"),
                 action(f"{prefix}-read-tests", "read_file", path="test_calculator.py")]
        final = [action(f"{prefix}-test-final", "run_test", command="unit"),
                 action(f"{prefix}-final", "final_answer", answer="Completed the required approved report and current-version tests; see the durable evidence and diff.")]
        if kind == "test_fix":
            source_action = action("fix-write-source", "write_file", path="calculator.py", content=FIXED_SOURCE)
            first += [action("fix-test-before", "run_test", command="unit"), source_action]
            phases = [first, [report_action], final]
        else:
            phases = [first + [report_action], final]
        phase_paths = []
        for index, phase_actions in enumerate(phases):
            path = actions_dir / f"phase-{index}.json"
            write_json(path, phase_actions)
            phase_paths.append(path)
        self._cli(f"{kind}:initial:run", ["run", "--run-dir", str(run_dir), "--workspace", str(workspace),
                  "--task", "Repair the faulty addition and document the change" if kind == "test_fix" else "Explain calculator behavior and test coverage without modifying source",
                  "--kind", kind, "--model", "fake", "--actions", str(phase_paths[0])], expected=2)
        state = self._inspect(kind, run_dir, "initial")
        if kind == "test_fix":
            failing = [e for e in state["evidence"] if e.get("tool_call_id") == "fix-test-before"]
            if len(failing) != 1 or failing[0]["exit_code"] == 0:
                raise AcceptanceFailure("Broken fixture did not demonstrate a failing baseline test")
            state = self._resume_approved(kind, run_dir, workspace, state, source_action, expected_files,
                                          phase_paths[1], "source", "WAITING_APPROVAL")
            expected_files["calculator.py"] = FIXED_SOURCE
        state = self._resume_approved(kind, run_dir, workspace, state, report_action, expected_files,
                                      phase_paths[-1], "report", "COMPLETED")
        expected_files[report_path] = report
        verification = self._cli(f"{kind}:verify-audit", ["verify-audit", "--run-dir", str(run_dir)])
        audit = [json.loads(line) for line in (run_dir / "audit.jsonl").read_text(encoding="utf-8").splitlines()]
        diff = json.loads((run_dir / "diff.json").read_text(encoding="utf-8"))
        writes = [record["tool_call_id"] for record in audit if record["event"] == "execution_started"
                  and record["details"].get("tool") == "write_file"]
        expected_writes = (["fix-write-source"] if kind == "test_fix" else []) + [report_action["id"]]
        tests = [e for e in state["evidence"] if e.get("tool") == "run_test"]
        approval_records = [record for record in audit if record["event"] == "approval"]
        checks = {"completed": state["status"] == "COMPLETED" and state["pending"] is None and state["inflight"] is None,
                  "audit_valid": verification.get("valid") is True,
                  "exactly_once_writes": sorted(writes) == sorted(expected_writes),
                  "scoped_fixture_approvals": sorted(record["tool_call_id"] for record in approval_records) == sorted(expected_writes)
                      and all(record["actor"] == REVIEWER for record in approval_records),
                  "test_oracle_unchanged": hashlib.sha256((workspace / "test_calculator.py").read_bytes()).hexdigest() == original_tests,
                  "expected_source_diff": diff["changed_files"] == (["calculator.py"] if kind == "test_fix" else []),
                  "current_tests_pass": bool(tests) and tests[-1]["exit_code"] == 0 and not tests[-1]["timed_out"]
                      and not tests[-1]["cancelled"] and tests[-1]["workspace_hash"] == state["workspace_hash"],
                  "exact_fixture_contents": all((workspace / path).read_text(encoding="utf-8") == content for path, content in expected_files.items())}
        if not all(checks.values()):
            raise AcceptanceFailure(f"{kind}: failed checks: {[name for name, passed in checks.items() if not passed]}")
        result = {"passed": True, "workspace": str(workspace), "run_dir": str(run_dir), "run_id": state["run_id"],
                  "checks": checks, "state": snapshot(state), "diff": diff, "evidence": state["evidence"],
                  "audit_records": len(audit), "audit_valid": True, "approved_write_ids": expected_writes,
                  "files": {"report": str(workspace / report_path), "audit": str(run_dir / "audit.jsonl"),
                            "checkpoint": str(run_dir / "checkpoint.json"), "patch": str(run_dir / "changes.patch")}}
        self.summary["cases"][kind] = result
        self._save()
        return result

    def run(self, *, skip_pytest=False, cases=("code_understanding", "test_fix")) -> dict:
        try:
            if skip_pytest:
                self.summary["pytest"] = {"status": "skipped", "reason": "--skip-pytest"}
                (self.output / "pytest.txt").write_text("Skipped explicitly with --skip-pytest.\n", encoding="utf-8")
            else:
                result = self._execute("pytest", [str(PYTHON), "-m", "pytest", "-q", "-p", "no:cacheprovider"], 0, timeout=300)
                (self.output / "pytest.txt").write_text(result.stdout + result.stderr, encoding="utf-8")
                self.summary["pytest"] = {"status": "passed", "returncode": result.returncode}
            for kind in cases:
                self.run_case(kind)
            self.summary["passed"] = True
        except Exception as exc:
            self.summary["error"] = str(exc)
            self.summary["passed"] = False
        self._save()
        return self.summary


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, help="New or empty output directory")
    parser.add_argument("--skip-pytest", action="store_true", help="Skip the repository suite, for a prior run or tests")
    args = parser.parse_args(argv)
    output = args.output or REPO / "output" / "acceptance" / (datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ") + "-" + uuid.uuid4().hex[:8])
    try:
        runner = AcceptanceRunner(output)
        summary = runner.run(skip_pytest=args.skip_pytest)
    except Exception as exc:
        print(json.dumps({"passed": False, "error": str(exc)}))
        return 1
    print(json.dumps({"passed": summary["passed"], "output": str(runner.output),
                      "summary": str(runner.output / "summary.json")}, ensure_ascii=False))
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
