"""Explicit run / inspect / approve / resume commands for a human operator."""

import argparse
import json
from pathlib import Path

from .audit import AuditLog
from .checkpoint import Checkpoint
from .gate import HarnessStop
from .harness import Harness
from .models import FakeModel, real_model


def build_model(args):
    if args.model == "real":
        return real_model()
    if not args.actions:
        raise ValueError("--actions is required for the fake model")
    return FakeModel(json.loads(Path(args.actions).read_text(encoding="utf-8")))


def from_checkpoint(directory, model):
    state = Checkpoint(Path(directory)).load()
    policy = state.policy
    return Harness(state.workspace, directory, model, resume=True, kind=state.kind,
                   max_steps=state.max_steps, max_calls=state.max_calls,
                   commands=policy["commands"], timeout=policy["timeout"],
                   max_output_bytes=policy["max_output_bytes"], permissions=policy["permissions"])


def summary(state):
    return {"run_id": state.run_id, "status": state.status, "stop_reason": state.stop_reason,
            "step": state.step, "calls": state.calls, "workspace_hash": state.workspace_hash,
            "pending": state.pending, "final_answer": state.final_answer}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    for command in ("run", "resume", "approve", "inspect", "verify-audit"):
        child = sub.add_parser(command)
        child.add_argument("--run-dir", required=True)
        if command in {"run", "resume"}:
            child.add_argument("--model", choices=["fake", "real"], default="fake")
            child.add_argument("--actions", help="JSON array of FakeModel actions")
        if command == "run":
            child.add_argument("--workspace", required=True)
            child.add_argument("--task", required=True)
            child.add_argument("--kind", choices=["code_understanding", "test_fix"], default="code_understanding")
            child.add_argument("--max-steps", type=int, default=20)
            child.add_argument("--max-calls", type=int, default=40)
        if command == "approve":
            child.add_argument("--call-id", required=True)
            child.add_argument("--actor", required=True)
    args = parser.parse_args(argv)
    try:
        if args.command == "verify-audit":
            path = Path(args.run_dir) / "audit.jsonl"
            valid = path.is_file() and path.stat().st_size > 0 and AuditLog(path).verify()
            print(json.dumps({"valid": valid}))
            return 0 if valid else 1
        if args.command == "inspect":
            print(json.dumps(Checkpoint(Path(args.run_dir)).load().to_dict(), ensure_ascii=False, indent=2))
            return 0
        if args.command == "approve":
            harness = from_checkpoint(args.run_dir, FakeModel([]))
            harness.approve(args.call_id, args.actor)
            print(json.dumps(summary(harness.state), ensure_ascii=False, indent=2))
            return 0
        model = build_model(args)
        if args.command == "run":
            harness = Harness(args.workspace, args.run_dir, model, task=args.task, kind=args.kind,
                              max_steps=args.max_steps, max_calls=args.max_calls)
        else:
            harness = from_checkpoint(args.run_dir, model)
        state = harness.run()
        print(json.dumps(summary(state), ensure_ascii=False, indent=2))
        return 0 if state.status == "COMPLETED" else 2 if state.status == "WAITING_APPROVAL" else 1
    except HarnessStop as error:
        print(json.dumps({"error": str(error)}))
        return 1
    except Exception as error:
        # Provider exceptions can embed request contents; expose only their type.
        print(json.dumps({"error_type": type(error).__name__}))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
