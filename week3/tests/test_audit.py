"""Audit integrity, credential redaction, and multi-process append coverage."""

import json
import multiprocessing
from pathlib import Path
import tempfile
import unittest

from guarded_harness.audit import AuditIntegrityError, AuditLog


def _append_records(path: str, worker: int) -> None:
    audit = AuditLog(Path(path))
    for index in range(10):
        audit.append(f"run-{worker}", "tool_result", tool_call_id=f"call-{worker}-{index}",
                     actor="tool_executor", decision="allow", rule="workspace_only", index=index)


class AuditTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.audit = AuditLog(Path(self.temporary.name) / "audit.jsonl")

    def test_schema_chain_and_run_filter(self):
        first = self.audit.append("run-a", "requested", tool_call_id="call-1", actor="agent",
                                  decision="pending", rule="write_requires_approval", path="answer.py")
        second = self.audit.append("run-b", "denied", tool_call_id="call-2", actor="permission_gate",
                                   decision="deny", rule="outside_workspace")
        self.assertEqual(first["seq"], 1)
        self.assertEqual(second["prev_hash"], first["hash"])
        self.assertEqual(first["toolCallId"], first["tool_call_id"])
        self.assertEqual(first["details"]["path"], "answer.py")
        self.assertEqual(self.audit.read("run-a"), [first])
        self.assertTrue(self.audit.verify())

    def test_nested_credentials_are_redacted_before_persistence(self):
        self.audit.append("run", "request", payload={
            "api_key": "raw-api-value", "Token": "raw-token-value", "password": "raw-password-value",
            "OPENAI_API_KEY": "raw-env-api-value", "access_token": "raw-access-token-value",
            "nested": [{"SECRET": "raw-secret-value", "Authorization": "Basic raw-auth-value"}],
            "note": "call failed Bearer eyJabc.def_123 and sk-proj-abcdefg123456789",
            "source": "def tokenize(text): return text.split()",
        })
        text = self.audit.path.read_text()
        for secret in ("raw-api-value", "raw-token-value", "raw-password-value", "raw-secret-value",
                       "raw-auth-value", "eyJabc.def_123", "sk-proj-abcdefg123456789"):
            self.assertNotIn(secret, text)
        self.assertNotIn("raw-env-api-value", text)
        self.assertNotIn("raw-access-token-value", text)
        payload = self.audit.read()[0]["details"]["payload"]
        self.assertEqual(payload["source"], "def tokenize(text): return text.split()")
        self.assertEqual(payload["api_key"], "[REDACTED]")
        self.assertTrue(self.audit.verify())

    def test_tampering_is_detected_and_append_refuses_corrupt_history(self):
        self.audit.append("run", "denied", decision="deny")
        record = json.loads(self.audit.path.read_text())
        record["decision"] = "allow"
        self.audit.path.write_text(json.dumps(record) + "\n", encoding="utf-8")
        before = self.audit.path.read_bytes()
        self.assertFalse(self.audit.verify())
        with self.assertRaises(AuditIntegrityError):
            self.audit.read()
        with self.assertRaises(AuditIntegrityError):
            self.audit.append("run", "another_event")
        self.assertEqual(self.audit.path.read_bytes(), before)

    def test_middle_record_deletion_and_reordering_break_chain(self):
        for index in range(3):
            self.audit.append("run", "event", index=index)
        lines = self.audit.path.read_text().splitlines(keepends=True)
        for altered in ([lines[0], lines[2]], [lines[1], lines[0], lines[2]]):
            with self.subTest(altered=altered):
                self.audit.path.write_text("".join(altered))
                self.assertFalse(self.audit.verify())

    def test_truncated_record_is_not_silently_ignored(self):
        self.audit.append("run", "event")
        with self.audit.path.open("a") as stream:
            stream.write('{"seq":2')
        self.assertFalse(self.audit.verify())
        with self.assertRaises(AuditIntegrityError):
            self.audit.append("run", "event")

    def test_duplicate_keys_and_non_utf8_are_rejected(self):
        self.audit.append("run", "event")
        original = self.audit.path.read_text()
        self.audit.path.write_text('{"seq":999,' + original[1:])
        self.assertFalse(self.audit.verify())
        self.audit.path.write_bytes(b"\xff\n")
        self.assertFalse(self.audit.verify())
        with self.assertRaises(AuditIntegrityError):
            self.audit.append("run", "event")

    def test_concurrent_processes_do_not_lose_or_interleave_records(self):
        context = multiprocessing.get_context("spawn")
        processes = [context.Process(target=_append_records, args=(str(self.audit.path), worker))
                     for worker in range(3)]
        for process in processes:
            process.start()
        for process in processes:
            process.join(20)
            if process.is_alive():
                process.terminate()
                process.join()
        self.assertEqual([process.exitcode for process in processes], [0, 0, 0])
        records = self.audit.read()
        self.assertEqual(len(records), 30)
        self.assertEqual([record["seq"] for record in records], list(range(1, 31)))
        self.assertEqual(len({record["tool_call_id"] for record in records}), 30)
        self.assertTrue(self.audit.verify())

    def test_empty_log_is_valid(self):
        self.assertEqual(self.audit.read(), [])
        self.assertTrue(self.audit.verify())


if __name__ == "__main__":
    unittest.main()
