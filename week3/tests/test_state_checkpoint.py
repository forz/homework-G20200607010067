"""Regression coverage for durable state and concurrent runner ownership."""

import json
import multiprocessing
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

from guarded_harness.audit import AuditLog
from guarded_harness.checkpoint import Checkpoint, CheckpointBusyError
from guarded_harness.state_store import RunState, StateStore, StateValidationError


def _make_state(workspace: Path, **changes) -> RunState:
    arguments = dict(run_id="run-1", task="repair test", kind="coding", workspace=str(workspace),
                     workspace_hash="a" * 64)
    arguments.update(changes)
    return RunState(**arguments)


def _hold_checkpoint(directory: str, ready, release) -> None:
    with Checkpoint(Path(directory)).lock():
        ready.set()
        if not release.wait(20):
            raise RuntimeError("test did not release checkpoint lock")


def _write_states(directory: str, workspace: str, worker: int) -> None:
    store = StateStore(Path(directory))
    for index in range(15):
        store.save(_make_state(Path(workspace), run_id=f"worker-{worker}", step=index,
                               history=[{"worker": worker, "iteration": index}]))


class StateCheckpointTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.workspace = self.root / "workspace"
        self.workspace.mkdir()
        self.state = _make_state(self.workspace)
        self.store = StateStore(self.root / "state")

    def test_roundtrip_preserves_resume_information_and_private_file(self):
        self.state.plan = ["inspect", "fix", "verify"]
        self.state.pending = {"tool_call_id": "call-1", "arguments": {"path": "app.py"}}
        self.state.approvals = {"call-1": {"decision": "allow", "arguments_hash": "f" * 64}}
        self.state.executed = {"call-0": {"ok": True}}
        self.state.policy = {"allowed_commands": [["python", "-m", "unittest"]]}
        self.store.save(self.state)
        self.assertEqual(self.state.to_dict(), self.store.load().to_dict())
        self.assertEqual(os.stat(self.store.path).st_mode & 0o777, 0o600)

    def test_validation_rejects_wrong_types_and_nonfinite_values(self):
        changes = [dict(step=True), dict(max_calls=0), dict(evidence={}),
                   dict(plan=[{"step": "bad"}]), dict(pending=[]),
                   dict(policy={"timeout": float("nan")}), dict(run_id="")]
        for values in changes:
            with self.subTest(values=values), self.assertRaises(StateValidationError):
                RunState.from_dict(self.state.to_dict() | values)
        with self.assertRaises(StateValidationError):
            RunState.from_dict({"run_id": "missing-fields"})
        with self.assertRaises(StateValidationError):
            RunState.from_dict(self.state.to_dict() | {"unrecognized": 1})

    def test_mutable_defaults_do_not_leak_between_runs(self):
        other = _make_state(self.workspace)
        self.state.history.append({"event": "one"})
        self.state.approvals["call"] = True
        self.assertEqual(other.history, [])
        self.assertEqual(other.approvals, {})

    def test_replace_failure_keeps_old_checkpoint_and_cleans_temporary(self):
        self.store.save(self.state)
        before = self.store.path.read_bytes()
        self.state.step = 1
        with mock.patch("guarded_harness.state_store.os.replace", side_effect=OSError("injected")):
            with self.assertRaisesRegex(OSError, "injected"):
                self.store.save(self.state)
        self.assertEqual(before, self.store.path.read_bytes())
        self.assertEqual(list(self.store.directory.glob("*.tmp")), [])
        self.assertEqual(self.store.load().step, 0)

    def test_file_fsync_precedes_replace_and_directory_fsync_follows(self):
        operations = []
        real_fsync, real_replace = os.fsync, os.replace

        def sync(descriptor):
            operations.append("fsync")
            return real_fsync(descriptor)

        def replace(source, destination):
            operations.append("replace")
            return real_replace(source, destination)

        with mock.patch("guarded_harness.state_store.os.fsync", side_effect=sync), \
                mock.patch("guarded_harness.state_store.os.replace", side_effect=replace):
            self.store.save(self.state)
        self.assertEqual(operations, ["fsync", "replace", "fsync"])

    def test_state_inside_workspace_is_rejected_including_symlink(self):
        for directory in (self.workspace, self.workspace / "state"):
            with self.subTest(directory=directory), self.assertRaises(StateValidationError):
                StateStore(directory).save(self.state)
        linked = self.root / "linked-workspace"
        linked.symlink_to(self.workspace, target_is_directory=True)
        with self.assertRaises(StateValidationError):
            StateStore(linked / "state").save(self.state)
        self.assertFalse((self.workspace / "state").exists())

    def test_load_rejects_truncated_and_duplicate_key_json(self):
        self.store.save(self.state)
        self.store.path.write_text('{"run_id":', encoding="utf-8")
        with self.assertRaises(StateValidationError):
            self.store.load()
        payload = json.dumps(self.state.to_dict())
        self.store.path.write_text('{"run_id":"duplicate",' + payload[1:], encoding="utf-8")
        with self.assertRaises(StateValidationError):
            self.store.load()

    def test_checkpoint_records_save_without_copying_sensitive_state(self):
        audit = AuditLog(self.root / "audit.jsonl")
        checkpoint = Checkpoint(self.root / "checkpoint", audit)
        self.state.pending = {"token": "do-not-copy-me"}
        checkpoint.save(self.state)
        self.assertEqual(checkpoint.path.name, "checkpoint.json")
        self.assertEqual(checkpoint.load().to_dict(), self.state.to_dict())
        record = audit.read()[0]
        self.assertEqual(record["event"], "checkpoint_saved")
        self.assertNotIn("do-not-copy-me", audit.path.read_text())

    def test_run_lock_excludes_another_process_and_releases_on_error(self):
        context = multiprocessing.get_context("spawn")
        ready, release = context.Event(), context.Event()
        process = context.Process(target=_hold_checkpoint, args=(str(self.root / "checkpoint"), ready, release))
        process.start()
        try:
            self.assertTrue(ready.wait(10), "child did not acquire checkpoint lock")
            with self.assertRaises(CheckpointBusyError):
                with Checkpoint(self.root / "checkpoint").lock():
                    self.fail("concurrent lock was acquired")
        finally:
            release.set()
            process.join(10)
            if process.is_alive():
                process.terminate()
                process.join()
        self.assertEqual(process.exitcode, 0)
        with self.assertRaisesRegex(RuntimeError, "injected"):
            with Checkpoint(self.root / "checkpoint").lock():
                raise RuntimeError("injected")
        with Checkpoint(self.root / "checkpoint").lock():
            pass

    def test_concurrent_writers_never_expose_partial_state(self):
        self.store.save(self.state)
        context = multiprocessing.get_context("spawn")
        processes = [context.Process(target=_write_states, args=(str(self.store.directory), str(self.workspace), worker))
                     for worker in range(2)]
        for process in processes:
            process.start()
        try:
            for _ in range(30):
                state = self.store.load()
                if state.history:
                    self.assertEqual(state.step, state.history[0]["iteration"])
                    self.assertEqual(state.run_id, f"worker-{state.history[0]['worker']}")
        finally:
            for process in processes:
                process.join(15)
                if process.is_alive():
                    process.terminate()
                    process.join()
        self.assertEqual([process.exitcode for process in processes], [0, 0])
        self.assertEqual(self.store.load().step, 14)


if __name__ == "__main__":
    unittest.main()
