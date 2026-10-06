# Week 2：AI Agent 转账工具治理

在附件的 Python 治理框架上实现 `transfer` 工具，贯通参数校验、业务预检、权限判断、人工审批、超时处理、结果脱敏与审计追踪。

本项目默认离线运行，账户和资金均为内存模拟数据，无需模型服务、API Key 或外部数据库。原始附件保留在作业目录中。

## 环境准备

需要 **Python 3.11 及以上**，建议使用本次验收的 Python 3.12。原框架使用 Python 3.11 新增的 [`asyncio.timeout`](https://docs.python.org/3/library/asyncio-task.html#asyncio.timeout) 以及 `StrEnum`，系统 Python 3.9 无法运行。

在 `week2` 目录执行（下面以已安装的 Python 3.12 为例）：

```bash
python3.12 -m venv .venv
source .venv/bin/activate
python -m pip install -r requirements-lock.txt
python --version
python -m pip check
```

本次工作区已经准备了 `.venv`，直接 `source .venv/bin/activate` 即可运行。若电脑没有 Python 3.11+，可先安装对应版本；使用已安装的 uv 时，也可执行 `uv venv --python 3.12 --seed .venv` 后继续上述激活和安装命令。


## 开发实现

| 作业任务 | 实现 |
| --- | --- |
| 1. 模拟账户 | `ACCOUNTS` 使用 `(tenant_id, account_id)` 作为键，初始化作业指定的四个浮点余额 |
| 2. 参数模型 | `TransferArgs` 继承 `StrictArgs`；账户正则为 `^ACC-[A-Z]-[0-9]{6}$`，金额为 `0 < amount <= 100000` |
| 3. 业务预检 | 先拦截 `50000 < amount <= 80000`，再检查当前租户转出账户余额；预检不修改余额 |
| 4. 转账执行 | `amount > 80000` 先休眠 3 秒；转入账户缺失先拒绝，写入前复检余额，然后扣款和入账 |
| 5. 工具注册 | `WRITE / HIGH / transfer:execute`，必须审批，超时 2 秒，非幂等，自动重试次数 0 |
| 6. 账号脱敏 | `ACC-A-123456` → `ACC-A-****3456`，保留现有邮箱和敏感字段脱敏 |

金额区间为作业的教学规则：`50000` 可通过区间检查，`80000` 被拒绝；超过 `80000` 仍须经过余额和审批检查，然后用于超时演示。

固定权限方法 `PermissionEngine.decide` 与原附件逐字一致。`TransferArgs` 保留继承的 [`extra="forbid"` 和严格模式](https://docs.pydantic.dev/latest/api/config/#pydantic.config.ConfigDict.extra)，额外注入 `approved`、`user_id` 等字段会被拒绝。所有业务调用均走 `ToolRuntime.invoke()`。

审批绑定用户、租户、工具和**完整规范化参数摘要**，一次性消费且默认 300 秒有效。`canonical_target` 用于权限规则目标匹配和脱敏审计展示；审批摘要由 `_approval_digest` 计算。接入审批时应先用 `TransferArgs.model_validate(arguments).model_dump(mode="json")` 规范化，确保整数金额 `1200` 与执行时的浮点金额 `1200.0` 不会产生摘要差异。认证身份和权限来自 `ExecutionContext`，不由工具参数提供。

原附件审计仅有参数名。本实现增加可选 `target` 字段，在记录合法结构的转账决策及执行时写入脱敏目标，例如 `ACC-A-****3456->ACC-A-****4321:1200.0`；不保存原始参数值或请求整包。参数校验失败的记录保留参数名，`target` 为 `null`。

## 项目验收流程

### 1. 执行作业规定的 5 个测试

在激活虚拟环境后的 `week2` 根目录执行原验收命令：

```bash
python -m pytest tests/test_tool_governance.py -v -k "transfer"
```

标准：`5 passed`，没有失败、跳过或绕过治理入口的调用。测试覆盖参数注入、预检、权限与白名单、审批绑定和账号脱敏、超时且余额未变。

### 2. 执行补充测试和框架约束核验

```bash
python -m pytest -v
python scripts/verify_constraints.py
```

标准：全部 `42 passed`；约束核验输出四条 `PASS`。补充用例验证缺失目标、跨租户访问、金额边界、资金守恒、同账户转账、审批过期与身份绑定、并发审批消费、权限优先级、审计脱敏，并回归订单查询、退款及模拟 Shell。

### 3. 运行离线演示并检查审计

```bash
python tool_governance_demo.py
```

转账部分依次出现：

| 调用 | 预期 action / code | 资金效果 |
| --- | --- | --- |
| `call_tr_000001`：无审批 | `confirm / APPROVAL_REQUIRED` | 无 |
| `call_tr_000002`：篡改已审批金额 | `confirm / APPROVAL_REQUIRED` | 无 |
| `call_tr_000003`：匹配审批 | `allow / OK` | 转出账户 100000 → 98800；转入账户 5000 → 6200 |
| `call_tr_000004`：审批重放 | `confirm / APPROVAL_REQUIRED` | 无 |
| `call_tr_timeout`：90000 元 | `deny / TIMEOUT_UNKNOWN` | 本教学实现的休眠在扣款前，余额未改变 |

代码枚举 `DecisionAction.CONFIRM` 在 JSON 中序列化为小写 `"confirm"`。成功结果的 `from`、`to` 分别为 `ACC-A-****3456`、`ACC-A-****4321`。真实审计行以 `{"audit": ...}` 输出，包含 `trace_id`、`tool_call_id`、决策/执行阶段、结果码和脱敏后的 `target`；共有 15 条审计，其中 7 条属于转账。

演示结束时会恢复进入演示前的账户快照。最后一行的 `transfer_balances_before_demo_cleanup` 展示恢复前的余额，方便确认本次实际资金变化。