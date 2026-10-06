.DEFAULT_GOAL := help

SHELL := /bin/sh

GATEWAY_URL ?= http://127.0.0.1:8080
GOTOOLCHAIN ?= go1.24.13
export GOTOOLCHAIN
PYTHON ?= python3
ACCEPTANCE_OUTPUT ?=
ACCEPTANCE_OUTPUT_ARG = $(if $(ACCEPTANCE_OUTPUT),--output "$(ACCEPTANCE_OUTPUT)",)

.PHONY: help deps run build health test verify acceptance

help:
	@printf '%s\n' \
	  'make deps      下载 Go 依赖' \
	  'make run       使用当前环境配置启动 Gateway，需设置 DEEPSEEK_API_KEY' \
	  'make build     编译 Gateway 到 bin/' \
	  'make health    检查 Gateway 的 healthz 和 readyz' \
	  'make test      执行全部 Go 单元测试和缺陷回归测试' \
	  'make verify    执行本地 HTTP 验收与故障注入，无需真实密钥' \
	  'make acceptance 执行完整验收，含真实双模型调用，需 DEEPSEEK_API_KEY' \
	  '' \
	  '程序不会自动加载 .env；请先 export 所需环境变量。' \
	  '可覆盖 GATEWAY_URL 指定健康检查地址。'

test:
	go test -count=1 ./...

verify:
	$(PYTHON) scripts/acceptance.py --mode local $(ACCEPTANCE_OUTPUT_ARG)

acceptance:
	$(PYTHON) scripts/acceptance.py --mode all $(ACCEPTANCE_OUTPUT_ARG)

deps:
	go mod download

run:
	go run ./cmd/gateway

build:
	mkdir -p bin
	go build -o bin/gateway ./cmd/gateway

health:
	curl -fsS --max-time 5 "$(GATEWAY_URL)/healthz"
	@printf '\n'
	curl -fsS --max-time 5 "$(GATEWAY_URL)/readyz"
	@printf '\n'
