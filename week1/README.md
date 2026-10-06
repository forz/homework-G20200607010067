# LLM Unified Gateway

一个可独立运行的 Go 训练项目：对调用方提供统一的 OpenAI Chat Completions 风格接口，再按公开 `model` 别名动态路由到两种不同上游协议。

| 公开模型 | 上游协议 | 适配器内部实现 |
|---|---|---|
| `deepseek-v4-pro` | OpenAI Responses API | `agenticopenai.ResponsesModel` |
| `deepseek-v4-flash` | Anthropic Messages API | 官方 `anthropic-sdk-go` Messages SDK |

网关使用 Hertz 承载 HTTP/SSE，Eino 提供 Responses 模型组件和 Prompt 模板渲染，SQLite 保存不可变 Prompt 版本和脱敏 Usage 证据。两种上游都实现 [`domain.ModelAdapter`](internal/domain/model.go)，由 [`ModelRouter`](internal/services/router.go) 根据请求的 `model` 选择适配器；鉴权、请求体、流事件和 Token 格式在适配器内部转换。

## 快速启动

仓库已提供编译好的 [`bin/gateway`](bin/gateway)，适用于 **macOS Apple Silicon（darwin/arm64）**，直接运行无需安装 Go 或构建工具。真实模型调用需要 DeepSeek API Key；其他操作系统或 CPU 架构请按下方「重新编译与验收」生成对应二进制。

从仓库根目录进入 `week1` 后启动；如果已经在该目录，则跳过 `cd week1`：

```bash
cd week1
export DEEPSEEK_API_KEY='你的 DeepSeek API Key'
./bin/gateway
```

Gateway 在前台运行，按 `Ctrl+C` 停止。请在 `week1` 目录运行，默认从当前目录读取 `configs/gateway.yaml`，并将数据库写入 `data/`。两条 Base URL 已配置为 YAML 默认值，上面的环境变量用于显式指定地址；详见下方「真实上游配置」。

默认监听：

- Gateway：`http://127.0.0.1:8080`
- SQLite：`data/gateway.db`

健康检查：

```bash
curl -fsS --max-time 5 http://127.0.0.1:8080/healthz
curl -fsS --max-time 5 http://127.0.0.1:8080/readyz
```

自定义 Gateway 地址后，将上述 URL 中的地址和端口替换为实际监听地址。

若设置了 `GATEWAY_API_KEY`，除健康检查外的请求都需增加 `Authorization: Bearer <key>`。

## 重新编译与验收

以下命令均在 `week1` 目录执行。仅重新编译或运行自动验收时需要 Go；验收脚本另需 Python 3。使用已验证的 Go 1.24.13 工具链，首次使用时 Go 可能自动下载该版本。

从当前源码重新生成 `bin/gateway`（修改源码后需重新编译）：

```bash
export GOTOOLCHAIN=go1.24.13
go mod download
mkdir -p bin
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/gateway ./cmd/gateway
```

该命令默认生成当前操作系统和 CPU 架构的程序。生成其他平台的程序时，可在构建命令前指定 `GOOS` 和 `GOARCH`。

本地验收包含 Go 测试、临时二进制编译、两种协议的 HTTP 验证和故障注入，无需真实 API Key：

```bash
GOTOOLCHAIN=go1.24.13 python3 scripts/acceptance.py --mode local
```

已导出真实 `DEEPSEEK_API_KEY` 时，可执行包含真实双模型调用的完整验收：

```bash
GOTOOLCHAIN=go1.24.13 python3 scripts/acceptance.py --mode all
```

脚本自动启动并清理临时服务，不需要预先启动 Gateway。每次运行的 `report.md`、`report.json` 和构建/测试日志默认保存在 `artifacts/acceptance/<时间戳>/`，任一检查失败时返回非零退出码。可用 `--output <新目录>` 指定报告路径。历史验收证据见 [`docs/acceptance/2026-10-06/report.md`](docs/acceptance/2026-10-06/report.md)。

## 两模型基础调用

OpenAI Responses 路由：

```bash
curl -sS --url http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hello"}]}'
```

Anthropic Messages 路由：

```bash
curl -sS --url http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hello"}]}'
```

可用模型：

```bash
curl -sS --url http://127.0.0.1:8080/v1/models
```

## 流式 SSE

服务端使用 [Hertz 官方 SSE writer](https://www.cloudwego.io/zh/docs/hertz/tutorials/basic-feature/sse/) 逐条发送并刷新事件；writer 在 handler 返回前关闭。首条事件发送前的错误使用普通 HTTP JSON 响应，发送后的错误使用 `data: {"error":...}` 并以 `[DONE]` 结束。

两个模型均返回统一的 `chat.completion.chunk`，终态依次包含 Usage 和 `data: [DONE]`：

```bash
curl -N --url http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-pro","stream":true,"messages":[{"role":"user","content":"stream"}]}'
```

将模型改为 `deepseek-v4-flash` 即可验证 Anthropic 流。客户端断开后关闭上游流；网关只在首个非空文本发出前重试。客户端应检查 SSE 中的 `error` 和 `finish_reason`，不能仅凭 HTTP 200 或 `[DONE]` 判定成功。

## 结构化输出

同一动态 JSON Schema 会分别转换成 Responses `text.format` 和 Messages `output_config.format`，并在网关终态再次校验。DeepSeek 的 [Messages 兼容性文档](https://api-docs.deepseek.com/zh-cn/guides/anthropic_api/)目前仅声明 `output_config.effort` 支持，因此 Messages 适配器也将 Schema 加入系统指令，最终以网关校验结果判断成功：

```bash
curl -sS --url http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"deepseek-v4-pro",
    "messages":[{"role":"user","content":"extract Alice"}],
    "response_format":{"type":"json_schema","json_schema":{
      "name":"person","strict":true,
      "schema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}
    }}
  }'
```

非流首次非法输出会增加一条精简纠错消息并再调用一次；第二次仍非法返回 422。流式结构化输出不会纠错，非法终态通过 SSE `error` 事件结束。

## Prompt 版本与引用

创建第一个版本（首版本总会激活）：

```bash
curl -sS --url http://127.0.0.1:8080/v1/prompts \
  -H 'Content-Type: application/json' \
  -d '{"id":"greeter","name":"Greeter","role":"system","content":"Reply to {name} in {language}","activate":true}'
```

创建不可变新版本、列出历史、读取激活版本：

```bash
curl -sS --url http://127.0.0.1:8080/v1/prompts \
  -H 'Content-Type: application/json' \
  -d '{"id":"greeter","name":"Greeter v2","role":"system","content":"Greet {name} using {language}","activate":true}'
curl -sS --url 'http://127.0.0.1:8080/v1/prompts?prompt_id=greeter&limit=10'
curl -sS --url 'http://127.0.0.1:8080/v1/prompts/greeter'
curl -sS --url 'http://127.0.0.1:8080/v1/prompts/greeter?version=1'
```

独立渲染会在进入 Eino FString 前检查所有缺失变量：

```bash
curl -sS --url http://127.0.0.1:8080/v1/prompts/greeter/render \
  -H 'Content-Type: application/json' \
  -d '{"variables":{"name":"Alice","language":"Chinese"}}'
```

在模型请求中引用激活或显式版本：

```bash
curl -sS --url http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"deepseek-v4-flash",
    "messages":[{"role":"user","content":"start"}],
    "prompt_ref":{"id":"greeter","version":1,"variables":{"name":"Alice","language":"Chinese"},"position":"prepend"}
  }'
```

## Usage 与延迟证据

```bash
curl -sS --url 'http://127.0.0.1:8080/admin/usage?limit=20'
curl -sS --url 'http://127.0.0.1:8080/admin/usage?model=deepseek-v4-pro&limit=20'
```

每个 `request_id` 最多一条终态记录，字段包括输入/输出/缓存写入/缓存读取/推理 Token、总延迟、流式 TTFT、传输重试次数、结构化纠错次数、状态和实际 Prompt 版本。数据库不保存消息、模板渲染结果、模型输出、Authorization 或 API Key；provider metadata 只允许固定标量键并限制长度。

## 重试行为

上游返回 HTTP 408、409、429、5xx，或发生可重试的网络超时、临时网络错误时，网关按配置执行传输重试。请求 context 取消或超时后停止重试；流式请求只在首个非空文本发出前重试。

配置中的 `max_retries: 3` 表示一次初始尝试加最多三次传输重试；退避采用可取消的 full-jitter 指数策略。默认每次等待从 `[0, 50ms]`、`[0, 100ms]`、`[0, 200ms]` 抽取，所以实测间隔不要求单调递增。OpenAI 和 Anthropic SDK 内部重试均关闭。结构化纠错最多额外生成一次，两轮生成共享三次传输重试预算；`transport_retries` 和 `structured_corrections` 分别记录，可通过 `/admin/usage` 查询。

## 按模型独立限流

编辑 `configs/gateway.yaml`，将某模型的 `rate_per_second` 和 `burst` 调低后重启 Gateway，再快速连续请求。超限模型返回 HTTP 429 和 `model_rate_limited`；另一模型使用独立 Token Bucket，不受影响。一个客户端请求只扣一次，内部传输重试和结构化纠错不重复扣减。