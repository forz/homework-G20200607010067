"""Supplemental acceptance checks through the public tool execution boundary.

The five supplied assignment tests remain unchanged. These checks cover cases
where authorization, tenant isolation, and failed transfers must preserve funds.
"""

from __future__ import annotations

import asyncio
import functools
import json
from collections.abc import Awaitable, Callable, Iterator, Mapping
from dataclasses import asdict
from typing import Any

import pytest

import tool_governance_demo as demo


FROM_ACCOUNT = "ACC-A-123456"
TO_ACCOUNT = "ACC-A-654321"
SMALL_ACCOUNT = "ACC-A-888888"
OTHER_TENANT_ACCOUNT = "ACC-B-111111"
MISSING_ACCOUNT = "ACC-A-000001"


def async_test(test: Callable[..., Awaitable[None]]) -> Callable[..., None]:
    @functools.wraps(test)
    def run(*args: Any, **kwargs: Any) -> None:
        asyncio.run(test(*args, **kwargs))

    return run


def context(**overrides: Any) -> demo.ExecutionContext:
    defaults = {
        "permissions": frozenset({"order:read", "refund:create", "shell:run", "transfer:execute"}),
        "allowed_tools": frozenset({"get_order", "create_refund", "run_shell", "transfer"}),
    }
    return demo.base_context(**{**defaults, **overrides})


def arguments(**overrides: Any) -> dict[str, Any]:
    return {"from_account": FROM_ACCOUNT, "to_account": TO_ACCOUNT, "amount": 100.0, **overrides}


async def invoke_transfer(
    runtime: demo.ToolRuntime,
    args: Mapping[str, Any],
    *,
    call_id: str = "call_boundary_000001",
    **overrides: Any,
) -> demo.ToolResult:
    return await runtime.invoke(demo.ToolCall(call_id, "transfer", args), context(**overrides))


def approve(
    store: demo.ApprovalStore,
    args: Mapping[str, Any],
    approval_id: str = "approval_boundary",
    **context_overrides: Any,
) -> None:
    store.approve(approval_id, context(**context_overrides), "transfer", args)


@pytest.fixture(autouse=True)
def isolated_state() -> Iterator[None]:
    snapshot = dict(demo.ACCOUNTS)
    side_effects = dict(demo.SIDE_EFFECTS)
    demo.reset_side_effects()
    yield
    demo.ACCOUNTS.clear()
    demo.ACCOUNTS.update(snapshot)
    demo.SIDE_EFFECTS.clear()
    demo.SIDE_EFFECTS.update(side_effects)


@pytest.mark.parametrize(
    "amount,from_account,expected_code",
    [
        (0.01, FROM_ACCOUNT, "OK"),
        (50_000.0, FROM_ACCOUNT, "OK"),
        (50_000.01, SMALL_ACCOUNT, "EXCEED_LIMIT"),
        (80_000.0, SMALL_ACCOUNT, "EXCEED_LIMIT"),
        (80_000.01, SMALL_ACCOUNT, "INSUFFICIENT_BALANCE"),
        (100_000.0, SMALL_ACCOUNT, "INSUFFICIENT_BALANCE"),
    ],
)
@async_test
async def test_transfer_amount_boundaries(
    amount: float, from_account: str, expected_code: str
) -> None:
    """The teaching interval is open at 50,000 and closed at 80,000.

    A poor source also proves interval denial takes precedence over balance
    denial. Above 80,000, insufficient funds avoid the intentional slow handler;
    the supplied timeout test separately exercises that handler branch.
    """
    runtime, approvals, _audit = demo.build_runtime()
    args = arguments(amount=amount, from_account=from_account)
    before = dict(demo.ACCOUNTS)
    approve(approvals, args)

    result = await invoke_transfer(runtime, args, approval_id="approval_boundary")

    assert result.code == expected_code
    assert result.ok is (expected_code == "OK")
    if result.ok:
        assert demo.ACCOUNTS[("tenant_a", from_account)] == pytest.approx(
            before[("tenant_a", from_account)] - amount
        )
        assert demo.ACCOUNTS[("tenant_a", TO_ACCOUNT)] == pytest.approx(
            before[("tenant_a", TO_ACCOUNT)] + amount
        )
        assert sum(demo.ACCOUNTS.values()) == pytest.approx(sum(before.values()))
    else:
        assert result.action is demo.DecisionAction.DENY
        assert demo.ACCOUNTS == before


@pytest.mark.parametrize(
    "amount", [float("nan"), float("inf"), float("-inf"), "100.0", True, False, None]
)
@async_test
async def test_transfer_rejects_nonfinite_and_coerced_amounts(amount: Any) -> None:
    runtime, _approvals, audit = demo.build_runtime()
    before = dict(demo.ACCOUNTS)

    result = await invoke_transfer(runtime, arguments(amount=amount))

    assert result.code == "INVALID_ARGUMENT"
    assert result.action is demo.DecisionAction.DENY
    assert demo.ACCOUNTS == before
    assert [(record.phase, record.code) for record in audit.records] == [
        ("decision", "INVALID_ARGUMENT")
    ]


@pytest.mark.parametrize(
    "field,value",
    [
        ("from_account", "ACC-a-123456"),
        ("to_account", "ACC-A-6543210"),
        ("to_account", "ACC-A-654321\n"),
        ("from_account", 123456),
    ],
)
@async_test
async def test_transfer_rejects_malformed_accounts(field: str, value: Any) -> None:
    runtime, _approvals, _audit = demo.build_runtime()
    before = dict(demo.ACCOUNTS)

    result = await invoke_transfer(runtime, arguments(**{field: value}))

    assert result.code == "INVALID_ARGUMENT"
    assert demo.ACCOUNTS == before


@async_test
async def test_transfer_missing_target_preserves_all_balances_and_records_failure() -> None:
    runtime, approvals, audit = demo.build_runtime()
    args = arguments(to_account=MISSING_ACCOUNT)
    before = dict(demo.ACCOUNTS)
    approve(approvals, args)

    result = await invoke_transfer(runtime, args, approval_id="approval_boundary")

    assert result.code == "ACCOUNT_NOT_FOUND"
    assert result.action is demo.DecisionAction.DENY
    assert demo.ACCOUNTS == before
    assert ("tenant_a", MISSING_ACCOUNT) not in demo.ACCOUNTS
    assert [(record.phase, record.code) for record in audit.records] == [
        ("decision", "APPROVED"),
        ("execution", "ACCOUNT_NOT_FOUND"),
    ]


@pytest.mark.parametrize(
    "field,expected_code",
    [("from_account", "INSUFFICIENT_BALANCE"), ("to_account", "ACCOUNT_NOT_FOUND")],
)
@async_test
async def test_transfer_cannot_access_another_tenants_source_or_target(
    field: str, expected_code: str
) -> None:
    runtime, approvals, _audit = demo.build_runtime()
    args = arguments(**{field: OTHER_TENANT_ACCOUNT})
    before = dict(demo.ACCOUNTS)
    assert ("tenant_b", OTHER_TENANT_ACCOUNT) in before
    assert ("tenant_a", OTHER_TENANT_ACCOUNT) not in before
    approve(approvals, args)

    result = await invoke_transfer(runtime, args, approval_id="approval_boundary")

    assert result.code == expected_code
    assert result.action is demo.DecisionAction.DENY
    assert demo.ACCOUNTS == before


@async_test
async def test_transfer_to_same_account_preserves_balance() -> None:
    runtime, approvals, _audit = demo.build_runtime()
    args = arguments(to_account=FROM_ACCOUNT, amount=1_234.5)
    before = dict(demo.ACCOUNTS)
    approve(approvals, args)

    result = await invoke_transfer(runtime, args, approval_id="approval_boundary")

    assert result.ok is True
    assert result.code == "OK"
    assert result.content["from"] == result.content["to"] == "ACC-A-****3456"
    assert demo.ACCOUNTS == before


@pytest.mark.parametrize("identity", [{"user_id": "u_other"}, {"tenant_id": "tenant_b"}])
@async_test
async def test_transfer_approval_is_bound_to_user_and_tenant(identity: dict[str, str]) -> None:
    runtime, approvals, _audit = demo.build_runtime()
    args = arguments()
    before = dict(demo.ACCOUNTS)
    approve(approvals, args, **identity)

    result = await invoke_transfer(runtime, args, approval_id="approval_boundary")

    assert result.code == "APPROVAL_REQUIRED"
    assert result.action is demo.DecisionAction.CONFIRM
    assert demo.ACCOUNTS == before


@async_test
async def test_transfer_cannot_reuse_an_expired_approval() -> None:
    runtime, approvals, _audit = demo.build_runtime()
    args = arguments()
    before = dict(demo.ACCOUNTS)
    approvals.approve("expired", context(), "transfer", args, ttl_seconds=-1.0)

    result = await invoke_transfer(runtime, args, approval_id="expired")

    assert result.code == "APPROVAL_REQUIRED"
    assert result.action is demo.DecisionAction.CONFIRM
    assert demo.ACCOUNTS == before


@async_test
async def test_transfer_approval_cannot_authorize_a_different_tool() -> None:
    runtime, approvals, _audit = demo.build_runtime()
    args = arguments()
    before = dict(demo.ACCOUNTS)
    approvals.approve("wrong_tool", context(), "create_refund", args)

    result = await invoke_transfer(runtime, args, approval_id="wrong_tool")

    assert result.code == "APPROVAL_REQUIRED"
    assert result.action is demo.DecisionAction.CONFIRM
    assert demo.ACCOUNTS == before


@pytest.mark.parametrize("field", ["from_account", "to_account"])
@async_test
async def test_transfer_approval_binds_both_accounts(field: str) -> None:
    runtime, approvals, _audit = demo.build_runtime()
    args = arguments()
    before = dict(demo.ACCOUNTS)
    approve(approvals, args)

    rejected = await invoke_transfer(
        runtime, {**args, field: SMALL_ACCOUNT}, approval_id="approval_boundary"
    )
    assert rejected.code == "APPROVAL_REQUIRED"
    assert demo.ACCOUNTS == before

    accepted = await invoke_transfer(runtime, args, approval_id="approval_boundary")
    assert accepted.ok is True
    assert demo.ACCOUNTS[("tenant_a", FROM_ACCOUNT)] == before[("tenant_a", FROM_ACCOUNT)] - 100
    assert demo.ACCOUNTS[("tenant_a", TO_ACCOUNT)] == before[("tenant_a", TO_ACCOUNT)] + 100


@async_test
async def test_transfer_concurrent_replay_consumes_approval_only_once() -> None:
    runtime, approvals, _audit = demo.build_runtime()
    args = arguments()
    before = dict(demo.ACCOUNTS)
    approve(approvals, args)

    results = await asyncio.gather(
        invoke_transfer(runtime, args, call_id="concurrent_000001", approval_id="approval_boundary"),
        invoke_transfer(runtime, args, call_id="concurrent_000002", approval_id="approval_boundary"),
    )

    assert sorted(result.code for result in results) == ["APPROVAL_REQUIRED", "OK"]
    assert sum(result.ok for result in results) == 1
    assert demo.ACCOUNTS[("tenant_a", FROM_ACCOUNT)] == before[("tenant_a", FROM_ACCOUNT)] - 100
    assert demo.ACCOUNTS[("tenant_a", TO_ACCOUNT)] == before[("tenant_a", TO_ACCOUNT)] + 100
    assert sum(demo.ACCOUNTS.values()) == pytest.approx(sum(before.values()))


@async_test
async def test_transfer_competing_approvals_cannot_overdraw_the_source() -> None:
    runtime, approvals, _audit = demo.build_runtime()
    args = arguments(from_account=SMALL_ACCOUNT, amount=15_000.0)
    before = dict(demo.ACCOUNTS)
    approve(approvals, args, "first")
    approve(approvals, args, "second")

    results = await asyncio.gather(
        invoke_transfer(runtime, args, call_id="competing_000001", approval_id="first"),
        invoke_transfer(runtime, args, call_id="competing_000002", approval_id="second"),
    )

    assert sorted(result.code for result in results) == ["INSUFFICIENT_BALANCE", "OK"]
    assert demo.ACCOUNTS[("tenant_a", SMALL_ACCOUNT)] == 5_000.0
    assert demo.ACCOUNTS[("tenant_a", TO_ACCOUNT)] == 20_000.0
    assert demo.ACCOUNTS[("tenant_b", OTHER_TENANT_ACCOUNT)] == before[
        ("tenant_b", OTHER_TENANT_ACCOUNT)
    ]
    assert sum(demo.ACCOUNTS.values()) == pytest.approx(sum(before.values()))


@pytest.mark.parametrize(
    "mode,expected_action",
    [
        (demo.PermissionMode.BYPASS_PERMISSIONS, demo.DecisionAction.CONFIRM),
        (demo.PermissionMode.DONT_ASK, demo.DecisionAction.DENY),
    ],
)
@async_test
async def test_transfer_modes_cannot_bypass_high_risk_approval(
    mode: demo.PermissionMode, expected_action: demo.DecisionAction
) -> None:
    runtime, approvals, _audit = demo.build_runtime()
    args = arguments()
    before = dict(demo.ACCOUNTS)

    pending = await invoke_transfer(runtime, args, mode=mode)
    assert pending.code == "APPROVAL_REQUIRED"
    assert pending.action is expected_action
    assert demo.ACCOUNTS == before

    approve(approvals, args)
    accepted = await invoke_transfer(runtime, args, mode=mode, approval_id="approval_boundary")
    assert accepted.ok is True


@async_test
async def test_transfer_deny_rule_wins_over_allow_bypass_and_valid_approval() -> None:
    runtime, approvals, _audit = demo.build_runtime(
        rules=(
            demo.PermissionRule("allow", "transfer"),
            demo.PermissionRule("deny", "transfer", f"{FROM_ACCOUNT}->{TO_ACCOUNT}"),
        )
    )
    args = arguments()
    before = dict(demo.ACCOUNTS)
    approve(approvals, args)

    result = await invoke_transfer(
        runtime,
        args,
        mode=demo.PermissionMode.BYPASS_PERMISSIONS,
        approval_id="approval_boundary",
    )

    assert result.code == "DENY_RULE"
    assert result.action is demo.DecisionAction.DENY
    assert demo.ACCOUNTS == before


@async_test
async def test_transfer_success_audits_traceable_target_without_exposing_account_values() -> None:
    runtime, approvals, audit = demo.build_runtime()
    args = arguments(amount=12.5)
    approve(approvals, args)

    result = await invoke_transfer(runtime, args, approval_id="approval_boundary")

    assert result.ok is True
    assert [record.phase for record in audit.records] == ["decision", "execution"]
    for record in audit.records:
        assert record.target == "ACC-A-****3456->ACC-A-****4321:12.5"
        assert record.argument_keys == ("amount", "from_account", "to_account")
        serialized = json.dumps(asdict(record), ensure_ascii=False)
        assert FROM_ACCOUNT not in serialized
        assert TO_ACCOUNT not in serialized
        assert "arguments" not in asdict(record)
    assert audit.records[-1].latency_ms is not None


@async_test
async def test_transfer_denied_and_failed_audit_targets_also_mask_accounts() -> None:
    runtime, approvals, audit = demo.build_runtime()
    args = arguments(to_account=MISSING_ACCOUNT)

    pending = await invoke_transfer(runtime, args)
    assert pending.code == "APPROVAL_REQUIRED"
    approve(approvals, args)
    failed = await invoke_transfer(runtime, args, approval_id="approval_boundary")
    assert failed.code == "ACCOUNT_NOT_FOUND"

    assert [record.phase for record in audit.records] == ["decision", "decision", "execution"]
    for record in audit.records:
        assert record.target == "ACC-A-****3456->ACC-A-****0001:100.0"
        serialized = json.dumps(asdict(record), ensure_ascii=False)
        assert FROM_ACCOUNT not in serialized
        assert MISSING_ACCOUNT not in serialized


@async_test
async def test_get_order_regression_masks_secrets_and_enforces_tenant_ownership() -> None:
    runtime, _approvals, _audit = demo.build_runtime()

    own = await runtime.invoke(
        demo.ToolCall("read_own", "get_order", {"order_id": "ord_1001"}), context()
    )
    foreign = await runtime.invoke(
        demo.ToolCall("read_other", "get_order", {"order_id": "ord_1001"}),
        context(tenant_id="tenant_b"),
    )

    assert own.ok is True
    assert own.content["customer_email"] == "***@***"
    assert own.content["access_token"] == "***"
    assert foreign.code == "ORDER_NOT_FOUND"
    assert demo.SIDE_EFFECTS == {"refund_executions": 0, "shell_executions": 0}


@async_test
async def test_refund_regression_still_requires_one_time_approval() -> None:
    runtime, approvals, _audit = demo.build_runtime()
    args = {"order_id": "ord_1001", "amount": 399.0, "reason": "商品存在质量问题"}

    pending = await runtime.invoke(demo.ToolCall("refund_pending", "create_refund", args), context())
    approvals.approve("refund_once", context(), "create_refund", args)
    accepted = await runtime.invoke(
        demo.ToolCall("refund_accepted", "create_refund", args), context(approval_id="refund_once")
    )
    replay = await runtime.invoke(
        demo.ToolCall("refund_replay", "create_refund", args), context(approval_id="refund_once")
    )

    assert pending.code == replay.code == "APPROVAL_REQUIRED"
    assert accepted.ok is True
    assert demo.SIDE_EFFECTS["refund_executions"] == 1


@async_test
async def test_shell_regression_keeps_deny_and_plan_boundaries() -> None:
    runtime, _approvals, _audit = demo.build_runtime()

    denied = await runtime.invoke(
        demo.ToolCall("shell_denied", "run_shell", {"command": "rm -rf /tmp/demo"}),
        context(mode=demo.PermissionMode.BYPASS_PERMISSIONS),
    )
    plan = await runtime.invoke(
        demo.ToolCall("shell_plan", "run_shell", {"command": "pytest -q"}),
        context(mode=demo.PermissionMode.PLAN),
    )
    allowed = await runtime.invoke(
        demo.ToolCall("shell_allowed", "run_shell", {"command": "pytest -q"}), context()
    )

    assert denied.code == "DENY_RULE"
    assert plan.code == "PLAN_MODE_DENIED"
    assert allowed.ok is True
    assert allowed.content["simulated"] is True
    assert demo.SIDE_EFFECTS["shell_executions"] == 1
