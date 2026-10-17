#!/usr/bin/env python3
"""Verify the four assignment constraints without external files or services."""

from __future__ import annotations

import ast
import hashlib
import importlib.util
from pathlib import Path
import sys
from typing import Callable


ROOT = Path(__file__).resolve().parents[1]

# Baselines computed from the original assignment attachments, not this solution:
# AI Agent 全栈工程师训练营-作业二/图片和附件/
#   tool_governance_demo-无答案.py: full PermissionEngine.decide source lines
#   test_tool_governance.py: complete file bytes
# Keeping the hashes here makes this check reproducible in a fresh checkout.
DECIDE_SHA256 = "c24d66e78d9d8265e901dca0592edf3de8b276ffb72d2ad590a0e2e5ab5c20d0"
SUPPLIED_TEST_SHA256 = "6288fb2c25fad9e0b09d341f449b4ce2c9297aa7469901dff123306ff92f33e2"


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)


def check_permission_method() -> None:
    source = (ROOT / "tool_governance_demo.py").read_bytes().decode("utf-8")
    tree = ast.parse(source)
    classes = [
        node for node in tree.body
        if isinstance(node, ast.ClassDef) and node.name == "PermissionEngine"
    ]
    require(len(classes) == 1, "expected exactly one PermissionEngine class")
    methods = [
        node for node in classes[0].body
        if isinstance(node, ast.AsyncFunctionDef) and node.name == "decide"
    ]
    require(len(methods) == 1, "expected exactly one async PermissionEngine.decide method")
    method = methods[0]
    start = min([method.lineno, *(node.lineno for node in method.decorator_list)])
    text = "".join(source.splitlines(keepends=True)[start - 1:method.end_lineno])
    require(
        hashlib.sha256(text.encode("utf-8")).hexdigest() == DECIDE_SHA256,
        "PermissionEngine.decide differs from the original attachment (including whitespace)",
    )


def check_supplied_tests() -> None:
    contents = (ROOT / "tests" / "test_tool_governance.py").read_bytes()
    require(
        hashlib.sha256(contents).hexdigest() == SUPPLIED_TEST_SHA256,
        "tests/test_tool_governance.py differs from the original attachment",
    )


def check_strict_arguments() -> None:
    # Load the actual implementation, independent of the caller's working directory.
    spec = importlib.util.spec_from_file_location(
        "_week2_constraints_demo", ROOT / "tool_governance_demo.py"
    )
    require(spec is not None and spec.loader is not None, "cannot load tool_governance_demo.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    try:
        spec.loader.exec_module(module)
        require(module.TransferArgs.model_config.get("extra") == "forbid", "TransferArgs must forbid extra fields")
        require(module.TransferArgs.model_config.get("strict") is True, "TransferArgs must retain strict=True")
        arguments = {"from_account": "ACC-A-123456", "to_account": "ACC-A-654321", "amount": 100.0}
        for extra in ({"approved": True}, {"user_id": "injected"}):
            try:
                module.TransferArgs.model_validate({**arguments, **extra})
            except module.ValidationError as error:
                require(
                    any(item["type"] == "extra_forbidden" for item in error.errors()),
                    "extra fields must fail with extra_forbidden",
                )
            else:
                raise ValueError("TransferArgs accepted an injected extra field")
    finally:
        sys.modules.pop(spec.name, None)


def check_test_entrypoint() -> None:
    implementation = ast.parse((ROOT / "tool_governance_demo.py").read_text(encoding="utf-8"))
    handlers = {
        node.name for node in implementation.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
        and node.name.endswith(("_handler", "_precheck"))
    }
    test_files = sorted((ROOT / "tests").rglob("*.py"))
    require(bool(test_files), "no test sources found")
    invokes = 0
    for path in test_files:
        tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
        for node in ast.walk(tree):
            # Reject references as well as direct calls, including imported aliases.
            name = (
                node.id if isinstance(node, ast.Name) else
                node.attr if isinstance(node, ast.Attribute) else
                node.name if isinstance(node, ast.alias) else None
            )
            require(
                name not in handlers,
                f"{path.relative_to(ROOT)}:{getattr(node, 'lineno', 0)} references {name}; use runtime.invoke()",
            )
            if not isinstance(node, ast.Call) or not isinstance(node.func, ast.Attribute):
                continue
            require(
                node.func.attr not in {"handler", "precheck", "decide"},
                f"{path.relative_to(ROOT)}:{node.lineno} bypasses runtime.invoke()",
            )
            if node.func.attr == "invoke":
                invokes += 1
    require(invokes > 0, "tests must call runtime.invoke()")


def main() -> int:
    checks: tuple[tuple[str, Callable[[], None]], ...] = (
        ("PermissionEngine.decide matches the original attachment", check_permission_method),
        ("the five supplied tests are unchanged", check_supplied_tests),
        ("TransferArgs retains extra=forbid and strict=True", check_strict_arguments),
        ("tests use runtime.invoke without direct handler/precheck calls", check_test_entrypoint),
    )
    failures = 0
    for label, check in checks:
        try:
            check()
        except Exception as error:
            failures += 1
            print(f"FAIL {label}: {error}")
        else:
            print(f"PASS {label}")
    return int(failures > 0)


if __name__ == "__main__":
    raise SystemExit(main())
