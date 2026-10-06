#!/usr/bin/env python3
"""Black-box acceptance through the real gateway and both wire-protocol SDKs.

Only Python's standard library is required. Local fixtures never contact a model
provider. --mode all/live uses DEEPSEEK_API_KEY and makes billable model calls.
Failures are evidence: they remain FAIL and make the process exit nonzero.
"""

import argparse
import contextlib
import datetime
import hashlib
import http.server
import json
import os
from pathlib import Path
import platform
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request


ROOT = Path(__file__).resolve().parents[1]
MODELS = ("deepseek-v4-pro", "deepseek-v4-flash")
PROTOCOLS = ("openai_responses", "anthropic_messages")
SCHEMA = {
    "type": "json_schema",
    "json_schema": {
        "name": "person", "strict": True,
        "schema": {"type": "object", "properties": {"name": {"type": "string"}},
                   "required": ["name"], "additionalProperties": False},
    },
}
SECRETS = [os.environ.get(key, "") for key in ("DEEPSEEK_API_KEY", "GATEWAY_API_KEY")]
HTTP = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def redact(text):
    for secret in SECRETS:
        if secret:
            text = text.replace(secret, "[REDACTED]")
    return text


def check(condition, message):
    if not condition:
        raise AssertionError(message)


def request(base, path, payload=None, token="acceptance-gateway-key"):
    headers = {"Content-Type": "application/json", "Authorization": "Bearer " + token}
    data = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(base + path, data=data, headers=headers)
    started = time.monotonic()
    try:
        response = HTTP.open(req, timeout=150)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        result = {"status": response.code,
                  "request_id": response.headers.get("X-Request-ID"),
                  "content_type": response.headers.get("Content-Type", "")}
        if "text/event-stream" in result["content_type"]:
            events, raw_lines = [], []
            for line in response:
                line = line.decode("utf-8")
                raw_lines.append(line)
                if line.startswith("data:"):
                    data = line[5:].strip()
                    events.append({"at_ms": round((time.monotonic() - started) * 1000, 3),
                                   "data": data if data == "[DONE]" else json.loads(data)})
            result.update(events=events, raw_sse="".join(raw_lines))
        else:
            body = response.read().decode("utf-8")
            try:
                result["body"] = json.loads(body)
            except ValueError:
                result["body"] = body
        result["elapsed_ms"] = round((time.monotonic() - started) * 1000, 3)
    return result


class Evidence:
    def __init__(self, output, mode):
        self.output = output
        self.cases = []
        self.current = None
        self.report = {"started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                       "mode": mode, "platform": platform.platform(), "cases": self.cases}

    def case(self, name, action):
        self.current = {"name": name, "status": "PASS", "evidence": {}}
        self.cases.append(self.current)
        started = time.monotonic()
        try:
            action()
        except Exception as error:
            self.current.update(status="FAIL", error=redact(str(error)))
        self.current["duration_ms"] = round((time.monotonic() - started) * 1000, 3)
        print(self.current["status"] + " " + name +
              (": " + self.current["error"] if "error" in self.current else ""), flush=True)
        return self.current["status"] == "PASS"

    def save(self, name, value):
        self.current["evidence"][name] = value
        return value

    def call(self, base, path, payload=None, label="http", token="acceptance-gateway-key"):
        self.save(label + "_request", {"path": path, "payload": payload})
        return self.save(label, request(base, path, payload, token))

    def finish(self):
        counts = {status: sum(c["status"] == status for c in self.cases) for status in ("PASS", "FAIL")}
        self.report.update(counts=counts, finished_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),
                           result="FAIL" if counts["FAIL"] else "PASS")
        self.output.mkdir(parents=True, exist_ok=True)
        (self.output / "report.json").write_text(
            redact(json.dumps(self.report, ensure_ascii=False, indent=2)) + "\n", encoding="utf-8")
        lines = ["# 自动验收执行记录", "", "- 时间：" + self.report["started_at"],
                 "- 模式：`" + self.report["mode"] + "`",
                 "- 结果：**" + self.report["result"] + "**；通过 " + str(counts["PASS"]) +
                 "，失败 " + str(counts["FAIL"]),
                 "- 逐请求证据、SSE 原文、Usage 与故障注入轨迹：[report.json](report.json)",
                 "- Go 测试日志：[go-test.log](go-test.log)",
                 "- `local.*` 使用本机协议桩；`live.*` 调用真实配置上游；两者不能互相替代。", "",
                 "| 用例 | 结果 | 失败断言 |", "|---|---|---|"]
        for case in self.cases:
            error = case.get("error", "").replace("|", "\\|").replace("\n", " ")
            lines.append("| " + case["name"] + " | " + case["status"] + " | " + error + " |")
        (self.output / "report.md").write_text("\n".join(lines) + "\n", encoding="utf-8")
        print("Report: " + str(self.output / "report.md"), flush=True)
        return 1 if counts["FAIL"] else 0


class Fixture(http.server.ThreadingHTTPServer):
    """Small deterministic upstream, with wire-level call counts and timing."""
    daemon_threads = True

    def __init__(self):
        super().__init__(("127.0.0.1", 0), FixtureHandler)
        self.scenario = "success"
        self.records = []
        self.lock = threading.Lock()

    def reset(self, scenario="success"):
        with self.lock:
            self.scenario = scenario
            self.records = []


class FixtureHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        try:
            self.respond()
        except (BrokenPipeError, ConnectionResetError):
            pass

    def respond(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        pro = self.path == "/responses"
        valid = self.path in ("/responses", "/v1/messages")
        valid = valid and body.get("model") == MODELS[0 if pro else 1]
        valid = valid and (("input" in body and self.headers.get("Authorization") == "Bearer acceptance-fixture-key")
                           if pro else ("messages" in body and self.headers.get("x-api-key") == "acceptance-fixture-key"
                                        and self.headers.get("anthropic-version") == "2023-06-01"))
        with self.server.lock:
            scenario = self.server.scenario
            attempt = len(self.server.records) + 1
            status = 200
            if not valid:
                status = 400
            elif scenario == "recover" and attempt <= 2:
                status = 500
            elif scenario in ("always500", "always429", "always401"):
                status = int(scenario[-3:])
            elif scenario == "correction_budget" and attempt != 3:
                status = 500
            self.server.records.append({"path": self.path, "model": body.get("model"),
                                        "attempt": attempt, "at_monotonic": time.monotonic(),
                                        "status": status, "valid_protocol_auth": bool(valid), "body": body})
        if status != 200:
            self.send_json(status, {"error": {"type": "fixture_error", "message": "injected failure"}})
            return
        structured = bool(body.get("text", {}).get("format")) if pro else bool(body.get("output_config", {}).get("format"))
        content = '{"name":"Alice"}' if structured else "ACCEPTANCE_OK"
        if scenario == "empty_length":
            content = ""
        if scenario in ("invalid_json", "correction_budget") or (scenario == "correct_json" and attempt == 1):
            content = "this is not JSON"
        if pro:
            usage = {"input_tokens": 20, "output_tokens": 10, "total_tokens": 30,
                     "input_tokens_details": {"cached_tokens": 4}, "output_tokens_details": {"reasoning_tokens": 3}}
            message = {"id": "msg_fixture", "type": "message", "status": "completed", "role": "assistant",
                       "content": [{"type": "output_text", "text": content, "annotations": []}]}
            response = {"id": "resp_fixture", "object": "response", "created_at": 1700000000,
                        "model": body["model"], "status": "completed", "output": [message], "usage": usage}
            if scenario == "empty_length":
                response.update(status="incomplete", incomplete_details={"reason": "max_output_tokens"})
        else:
            usage = {"input_tokens": 20, "output_tokens": 10, "cache_read_input_tokens": 4,
                     "cache_creation_input_tokens": 2, "output_tokens_details": {"thinking_tokens": 3}}
            response = {"id": "msg_fixture", "type": "message", "role": "assistant", "model": body["model"],
                        "content": [{"type": "text", "text": content}], "stop_reason": "end_turn",
                        "stop_sequence": None, "usage": usage}
            if scenario == "empty_length":
                response["stop_reason"] = "max_tokens"
        if not body.get("stream"):
            self.send_json(200, response)
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        if pro:
            self.event("response.created", response=dict(response, output=[], usage=None, status="in_progress"))
            self.event("response.output_item.added", output_index=0, item=dict(message, content=[]))
            self.event("response.content_part.added", output_index=0, content_index=0, item_id="msg_fixture",
                       part={"type": "output_text", "text": "", "annotations": []})
        else:
            self.event("message_start", message=dict(response, content=[], stop_reason=None, usage=dict(usage, output_tokens=0)))
            self.event("content_block_start", index=0, content_block={"type": "text", "text": ""})
        for delta in (content[:len(content)//2], content[len(content)//2:]):
            time.sleep(0.04)
            if pro:
                self.event("response.output_text.delta", output_index=0, content_index=0, item_id="msg_fixture", delta=delta)
            else:
                self.event("content_block_delta", index=0, delta={"type": "text_delta", "text": delta})
        if scenario == "truncated":
            return
        if pro:
            if scenario == "failed_event":
                self.event("response.failed", response=dict(response, status="failed", error={"code": "server_error", "message": "injected failure"}))
            else:
                self.event("response.completed", response=response)
        else:
            self.event("content_block_stop", index=0)
            # Anthropic permits only cumulative output usage in message_delta.
            delta_usage = {"output_tokens": 10} if scenario == "partial_usage" else usage
            self.event("message_delta", delta={"stop_reason": "end_turn", "stop_sequence": None}, usage=delta_usage)
            self.event("message_stop")

    def send_json(self, status, value):
        data = json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def event(self, kind, **value):
        value["type"] = kind
        value["sequence_number"] = getattr(self, "sequence", 0)
        self.sequence = value["sequence_number"] + 1
        self.wfile.write(("event: " + kind + "\ndata: " + json.dumps(value) + "\n\n").encode())
        self.wfile.flush()


@contextlib.contextmanager
def gateway(binary, temp, name, fixture=None, rate=False):
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    env = os.environ.copy()
    env.update(GATEWAY_ADDRESS="127.0.0.1:" + str(port), GATEWAY_DATABASE_PATH=str(temp / (name + ".db")),
               GATEWAY_CONFIG=str(ROOT / "configs/gateway.yaml"), GATEWAY_API_KEY="acceptance-gateway-key",
               GATEWAY_MAX_RETRIES="3")
    if fixture:
        base = "http://127.0.0.1:" + str(fixture.server_port)
        env.update(DEEPSEEK_API_KEY="acceptance-fixture-key", OPENAI_BASE_URL=base,
                   ANTHROPIC_BASE_URL=base, OPENAI_RESPONSES_MODEL_ID=MODELS[0],
                   ANTHROPIC_MESSAGES_MODEL_ID=MODELS[1], OPENAI_RESPONSES_TIMEOUT="30s", ANTHROPIC_MESSAGES_TIMEOUT="30s")
    for prefix in ("DEEPSEEK_V4_PRO", "DEEPSEEK_V4_FLASH"):
        env[prefix + "_RATE_PER_SECOND"] = "0.000001" if rate else "100"
        env[prefix + "_BURST"] = "1" if rate else "100"
    log_path = temp / (name + ".log")
    with log_path.open("w") as log:
        process = subprocess.Popen([str(binary)], cwd=ROOT, env=env, stdout=log, stderr=log)
        try:
            base = "http://127.0.0.1:" + str(port)
            for _ in range(100):
                if process.poll() is not None:
                    raise RuntimeError("gateway startup failed: " + redact(log_path.read_text())[-1500:])
                try:
                    with HTTP.open(base + "/readyz", timeout=0.5) as response:
                        if response.code == 200:
                            break
                except OSError:
                    time.sleep(0.1)
            else:
                raise RuntimeError("gateway readiness timeout")
            yield base
        finally:
            process.terminate()
            try:
                process.wait(timeout=8)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


def chat(model, stream=False, structured=False):
    body = {"model": model, "messages": [{"role": "user", "content": "Reply with exactly: ACCEPTANCE_OK"}], "max_tokens": 1024}
    if stream:
        body["stream"] = True
        body["messages"][0]["content"] = "Print the numbers 1 through 20 separated by spaces. No explanation."
    if structured:
        body["response_format"] = SCHEMA
        body["messages"][0]["content"] = 'Extract the name of this person: Alice.'
    return body


def success(response, model, stream=False):
    check(response["status"] == 200, "expected HTTP 200, got " + str(response))
    if not stream:
        body = response["body"]
        check(body.get("object") == "chat.completion" and body.get("model") == model, "invalid unified completion")
        content = body["choices"][0]["message"]["content"]
        usage = body["usage"]
        check(body["choices"][0]["finish_reason"] == "stop", "generation did not complete normally")
    else:
        check("text/event-stream" in response["content_type"], "missing SSE content type")
        events = response["events"]
        check(events and events[-1]["data"] == "[DONE]", "missing terminal [DONE]")
        objects = [e["data"] for e in events if isinstance(e["data"], dict)]
        check(not any("error" in e for e in objects), "unexpected SSE error")
        check(all(e.get("object") == "chat.completion.chunk" and e.get("model") == model for e in objects), "invalid chunk contract")
        chunks = [e for e in objects if e.get("choices") and e["choices"][0].get("delta", {}).get("content")]
        check(len(chunks) >= 2, "expected at least two nonempty text chunks")
        content = "".join(e["choices"][0]["delta"]["content"] for e in chunks)
        usages = [e["usage"] for e in objects if "usage" in e]
        check(len(usages) == 1, "expected one terminal usage chunk")
        check(objects[-1]["choices"][0]["finish_reason"] == "stop", "missing successful terminal finish reason")
        usage = usages[0]
    check(bool(content.strip()), "empty assistant text")
    check(usage["total_tokens"] == usage["prompt_tokens"] + usage["completion_tokens"], "inconsistent token totals")
    check(usage["prompt_tokens"] > 0 and usage["completion_tokens"] > 0, "missing input/output usage")
    check(not usage.get("incomplete"), "usage marked incomplete")
    for field in ("cached_tokens", "cache_write_tokens"):
        check(isinstance(usage["prompt_tokens_details"][field], int), "missing token category " + field)
    check(isinstance(usage["completion_tokens_details"]["reasoning_tokens"], int), "missing reasoning category")
    return content, usage


def usage_for(evidence, base, response, model, stream=False, status="success"):
    records = evidence.call(base, "/admin/usage?model=" + model + "&limit=100", label="usage")["body"]["data"]
    matches = [row for row in records if row["request_id"] == response["request_id"]]
    check(len(matches) == 1, "expected exactly one usage record for request")
    record = evidence.save("matched_usage", matches[0])
    check(record["model"] == model and record["protocol"] == PROTOCOLS[MODELS.index(model)], "wrong route in usage")
    check(record["status"] == status, "wrong terminal usage status: " + record["status"])
    check(record["latency_ms"] > 0, "missing total latency")
    if stream and status == "success":
        check(record["first_token_ms"] is not None and 0 < record["first_token_ms"] <= record["latency_ms"], "invalid TTFT")
    return record


def error_response(response, status, code):
    check(response["status"] == status, "expected HTTP %s, got %s" % (status, response["status"]))
    error = response["body"].get("error", {})
    check(error.get("code") == code, "expected error code " + code + ", got " + str(error))
    check(error.get("request_id") == response["request_id"] and bool(error.get("request_id")), "missing error request ID")
    check(all(key in error for key in ("type", "message", "retryable")), "incomplete error envelope")


def suite(evidence, base, label, fixture=None):
    def models():
        response = evidence.call(base, "/v1/models")
        check(response["status"] == 200, "models endpoint failed")
        check(set(row["id"] for row in response["body"]["data"]) == set(MODELS), "wrong model aliases")
    evidence.case(label + ".models", models)
    for model in MODELS:
        for kind in ("generate", "stream", "structured", "structured_stream"):
            def completion(model=model, kind=kind):
                if fixture:
                    fixture.reset()
                stream, structured = "stream" in kind, "structured" in kind
                response = evidence.call(base, "/v1/chat/completions", chat(model, stream, structured))
                content, usage = success(response, model, stream)
                if structured:
                    check(json.loads(content) == {"name": "Alice"}, "JSON output violates requested schema/value")
                record = usage_for(evidence, base, response, model, stream)
                check(all(record["usage"][key] == usage[key] for key in ("prompt_tokens", "completion_tokens", "total_tokens")), "usage persistence mismatch")
                if fixture:
                    trace = evidence.save("upstream", list(fixture.records))
                    check(len(trace) == 1 and trace[0]["valid_protocol_auth"], "wrong protocol/auth or unexpected calls")
                    expected = (20, 10, 4, 0, 3) if model == MODELS[0] else (26, 10, 4, 2, 3)
                    actual = (usage["prompt_tokens"], usage["completion_tokens"], usage["prompt_tokens_details"]["cached_tokens"],
                              usage["prompt_tokens_details"]["cache_write_tokens"], usage["completion_tokens_details"]["reasoning_tokens"])
                    check(actual == expected, "token category mapping differs: " + str(actual))
                    if stream:
                        check(response["events"][-1]["at_ms"] - response["events"][0]["at_ms"] >= 20, "SSE appears buffered until completion")
            evidence.case(label + "." + model + "." + kind, completion)

    def prompts():
        for version in (1, 2):
            created = evidence.call(base, "/v1/prompts", {"id": "acceptance", "name": "Acceptance",
                                    "content": "You are a greeting service. Reply to any greeting with exactly V" + str(version) + "_{name} and nothing else.", "activate": True}, label="create_v" + str(version))
            check(created["status"] == 201 and created["body"]["version"] == version, "prompt version creation failed")
        history = evidence.call(base, "/v1/prompts?prompt_id=acceptance", label="history")["body"]["data"]
        check(len(history) == 2, "missing immutable prompt history")
        active = evidence.call(base, "/v1/prompts/acceptance", label="active")["body"]
        check(active["version"] == 2, "active prompt did not advance")
        old = evidence.call(base, "/v1/prompts/acceptance?version=1", label="v1")["body"]
        check(old["content"] == "You are a greeting service. Reply to any greeting with exactly V1_{name} and nothing else.", "old prompt mutated")
        rendered = evidence.call(base, "/v1/prompts/acceptance/render", {"version": 1, "variables": {"name": "Alice"}}, label="render")
        check(rendered["body"]["content"] == "You are a greeting service. Reply to any greeting with exactly V1_Alice and nothing else.", "variable substitution failed")
        missing = evidence.call(base, "/v1/prompts/acceptance/render", {"variables": {}}, label="missing")
        error_response(missing, 422, "missing_prompt_variables")
    evidence.case(label + ".prompt_versions", prompts)
    for model in MODELS:
        for version in (1, None):
            def reference(model=model, version=version):
                if fixture:
                    fixture.reset()
                payload = chat(model)
                payload["messages"][0]["content"] = "Hello!"
                payload["prompt_ref"] = {"id": "acceptance", "variables": {"name": "Alice"}}
                if version is not None:
                    payload["prompt_ref"]["version"] = version
                response = evidence.call(base, "/v1/chat/completions", payload)
                content, _ = success(response, model)
                record = usage_for(evidence, base, response, model)
                actual_version = version or 2
                check(record.get("prompt_id") == "acceptance" and record.get("prompt_version") == actual_version, "wrong prompt reference recorded")
                expected_text = "V" + str(actual_version) + "_Alice"
                if fixture:
                    trace = evidence.save("upstream", list(fixture.records))
                    check(expected_text in json.dumps(trace[0]["body"]), "rendered prompt did not reach adapter")
                else:
                    check(expected_text in content, "model did not use the referenced prompt")
            evidence.case(label + "." + model + ".prompt_" + str(version or "active"), reference)

    evidence.case(label + ".unknown_model", lambda: error_response(evidence.call(
        base, "/v1/chat/completions", chat("unknown")), 404, "model_not_found"))
    evidence.case(label + ".authentication", lambda: error_response(evidence.call(
        base, "/v1/models", token="wrong"), 401, "invalid_api_key"))


def faults(evidence, base, fixture):
    for model in MODELS:
        def empty_length(model=model):
            fixture.reset("empty_length")
            response = evidence.call(base, "/v1/chat/completions", chat(model))
            evidence.save("upstream", list(fixture.records))
            check(response["status"] == 200, "token-limited empty output must preserve the completion and usage")
            choice = response["body"]["choices"][0]
            check(choice["message"]["content"] == "" and choice["finish_reason"] == "length", "missing length finish reason")
            record = usage_for(evidence, base, response, model)
            check(record["usage"]["total_tokens"] > 0, "lost usage for a reasoning-only response")
        evidence.case("local." + model + ".empty_length_usage", empty_length)
        for scenario in ("recover", "always500", "always429", "always401"):
            def retry(model=model, scenario=scenario):
                fixture.reset(scenario)
                response = evidence.call(base, "/v1/chat/completions", chat(model))
                trace = evidence.save("upstream", list(fixture.records))
                attempts = {"recover": 3, "always500": 4, "always429": 4, "always401": 1}[scenario]
                record = usage_for(evidence, base, response, model, status="success" if scenario == "recover" else "error")
                evidence.save("attempt_gaps_ms", [round((b["at_monotonic"] - a["at_monotonic"]) * 1000, 3) for a, b in zip(trace, trace[1:])])
                check(len(trace) == attempts, "expected %d upstream attempts, got %d; usage transport_retries=%s" % (attempts, len(trace), record["transport_retries"]))
                check(record["transport_retries"] == attempts - 1, "retry accounting mismatch")
                if scenario == "recover":
                    success(response, model)
                else:
                    status, code = {"always500": (502, "upstream_failure"), "always429": (429, "upstream_rate_limited"),
                                    "always401": (502, "upstream_authentication_failed")}[scenario]
                    error_response(response, status, code)
            evidence.case("local." + model + ".retry_" + scenario, retry)
        for scenario in ("recover", "always500"):
            def stream_retry(model=model, scenario=scenario):
                fixture.reset(scenario)
                response = evidence.call(base, "/v1/chat/completions", chat(model, stream=True))
                trace = evidence.save("upstream", list(fixture.records))
                attempts = 3 if scenario == "recover" else 4
                check(len(trace) == attempts, "wrong upstream stream attempt count: " + str(len(trace)))
                record = usage_for(evidence, base, response, model, stream=True, status="success" if scenario == "recover" else "error")
                check(record["transport_retries"] == attempts - 1, "stream retry accounting mismatch")
                if scenario == "recover":
                    success(response, model, stream=True)
                else:
                    error_response(response, 502, "upstream_failure")
            evidence.case("local." + model + ".stream_retry_" + scenario, stream_retry)

        def correction_budget(model=model):
            fixture.reset("correction_budget")
            response = evidence.call(base, "/v1/chat/completions", chat(model, structured=True))
            trace = evidence.save("upstream", list(fixture.records))
            record = usage_for(evidence, base, response, model, status="error")
            error_response(response, 502, "upstream_failure")
            check(len(trace) == 5 and record["transport_retries"] == 3, "correction must share the three-retry budget")
            check(record["structured_corrections"] == 1 and record["usage"]["total_tokens"] > 0, "lost successful attempt usage")
            check(record["usage"]["incomplete"], "failed correction must mark remaining usage unknown")
        evidence.case("local." + model + ".shared_correction_retry_budget", correction_budget)
        for scenario in ("correct_json", "invalid_json"):
            def structured_error(model=model, scenario=scenario):
                fixture.reset(scenario)
                response = evidence.call(base, "/v1/chat/completions", chat(model, structured=True))
                trace = evidence.save("upstream", list(fixture.records))
                check(len(trace) == 2, "expected one structured correction")
                record = usage_for(evidence, base, response, model, status="success" if scenario == "correct_json" else "error")
                check(record["structured_corrections"] == 1, "correction not recorded")
                if scenario == "correct_json":
                    content, _ = success(response, model)
                    check(json.loads(content) == {"name": "Alice"}, "correction invalid")
                else:
                    error_response(response, 422, "invalid_structured_output")
                    check(record["usage"]["total_tokens"] > 0, "completed upstream calls lost their token usage on validation failure")
            evidence.case("local." + model + "." + scenario, structured_error)
        for scenario in ("truncated", "failed_event") if model == MODELS[0] else ("truncated",):
            def bad_stream(model=model, scenario=scenario):
                fixture.reset(scenario)
                response = evidence.call(base, "/v1/chat/completions", chat(model, stream=True))
                trace = evidence.save("upstream", list(fixture.records))
                check(len(trace) == 1, "stream retried after emitting text")
                record = usage_for(evidence, base, response, model, stream=True, status="error")
                errors = [item["data"] for item in response.get("events", []) if isinstance(item["data"], dict) and "error" in item["data"]]
                check(errors and record["error_code"], "upstream failure incorrectly ended as success")
            evidence.case("local." + model + ".stream_" + scenario, bad_stream)

    def partial_usage():
        fixture.reset("partial_usage")
        response = evidence.call(base, "/v1/chat/completions", chat(MODELS[1], stream=True))
        evidence.save("upstream", list(fixture.records))
        usage_for(evidence, base, response, MODELS[1], stream=True)
        _, usage = success(response, MODELS[1], stream=True)
        check(usage["prompt_tokens"] == 26, "message_start input usage was lost")
    evidence.case("local.deepseek-v4-flash.stream_partial_usage", partial_usage)


def upstream_identities(evidence):
    actual_models = []
    for index, prefix in enumerate(("OPENAI_RESPONSES", "ANTHROPIC_MESSAGES")):
        default = "https://api.deepseek.com" + ("" if index == 0 else "/anthropic")
        base = os.environ.get(prefix + "_BASE_URL", default).rstrip("/")
        model = os.environ.get(prefix + "_MODEL_ID", MODELS[index])
        headers = {"Content-Type": "application/json"}
        if index == 0:
            path = "/responses"
            headers["Authorization"] = "Bearer " + os.environ["DEEPSEEK_API_KEY"]
            payload = {"model": model, "input": "Reply exactly OK", "max_output_tokens": 1024}
        else:
            path = "/v1/messages"
            headers.update({"x-api-key": os.environ["DEEPSEEK_API_KEY"], "anthropic-version": "2023-06-01"})
            payload = {"model": model, "messages": [{"role": "user", "content": "Reply exactly OK"}], "max_tokens": 1024}
        parsed = urllib.parse.urlsplit(base)
        safe_url = urllib.parse.urlunsplit((parsed.scheme, parsed.hostname or "", parsed.path + path, "", ""))
        evidence.save(prefix + "_request", {"url": safe_url, "payload": payload})
        req = urllib.request.Request(base + path, json.dumps(payload).encode(), headers)
        with urllib.request.urlopen(req, timeout=60) as response:
            body = json.load(response)
        evidence.save(prefix + "_response", body)
        check(bool(body.get("model")), "provider response did not identify the serving model")
        actual_models.append(body["model"])
    check(len(set(actual_models)) == 2, "both upstream protocols resolved to the same serving model: " + str(actual_models))


def rate_limits(evidence, base, fixture):
    # Fresh process with one token per model and effectively no refill.
    for model in MODELS:
        def rate(model=model):
            fixture.reset()
            first = evidence.call(base, "/v1/chat/completions", chat(model), label="first")
            success(first, model)
            second = evidence.call(base, "/v1/chat/completions", chat(model), label="second")
            error_response(second, 429, "model_rate_limited")
            trace = evidence.save("upstream", list(fixture.records))
            check(len(trace) == 1, "limited request reached upstream")
            usage_for(evidence, base, second, model, status="error")
        evidence.case("local." + model + ".independent_rate_limit", rate)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mode", choices=("local", "live", "all"), default="local")
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    output = args.output or ROOT / "artifacts/acceptance" / datetime.datetime.now().strftime("%Y%m%dT%H%M%S")
    output = output.resolve()
    check(not output.exists(), "output directory already exists; use a fresh path to preserve evidence")
    output.mkdir(parents=True)
    evidence = Evidence(output, args.mode)
    sources = sorted(list(ROOT.glob("**/*.go")) + [ROOT / "go.mod", ROOT / "go.sum", ROOT / "configs/gateway.yaml", Path(__file__).resolve()])
    evidence.report["source_sha256"] = {str(path.relative_to(ROOT)): hashlib.sha256(path.read_bytes()).hexdigest() for path in sources}
    env = os.environ.copy()
    env.setdefault("GOTOOLCHAIN", "go1.24.13")

    def command(name, arguments, timeout=300):
        result = subprocess.run(arguments, cwd=ROOT, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=timeout)
        text = redact(result.stdout)
        (output / (name + ".log")).write_text(text, encoding="utf-8")
        evidence.save("command", arguments)
        evidence.save("exit_code", result.returncode)
        check(result.returncode == 0, name + " failed; see " + name + ".log")

    try:
        evidence.report["go_version"] = subprocess.check_output(["go", "version"], env=env, text=True).strip()
        evidence.case("go.tests", lambda: command("go-test", ["go", "test", "-count=1", "-v", "./..."]))
        with tempfile.TemporaryDirectory(prefix="llm-acceptance-") as directory:
            temp = Path(directory)
            binary = temp / "gateway"
            built = evidence.case("go.build", lambda: command("go-build", ["go", "build", "-o", str(binary), "./cmd/gateway"]))
            if built and args.mode in ("local", "all"):
                with Fixture() as fixture:
                    thread = threading.Thread(target=fixture.serve_forever, daemon=True)
                    thread.start()
                    try:
                        with gateway(binary, temp, "local", fixture) as base:
                            suite(evidence, base, "local", fixture)
                            faults(evidence, base, fixture)
                        with gateway(binary, temp, "rate", fixture, rate=True) as base:
                            rate_limits(evidence, base, fixture)
                    finally:
                        fixture.shutdown()
                        thread.join(timeout=2)
            if built and args.mode in ("live", "all"):
                key_present = evidence.case("live.credentials", lambda: check(bool(os.environ.get("DEEPSEEK_API_KEY", "").strip()), "DEEPSEEK_API_KEY is required; live checks were not executed"))
                if key_present:
                    with gateway(binary, temp, "live") as base:
                        suite(evidence, base, "live")
                    evidence.case("live.distinct_upstream_models", lambda: upstream_identities(evidence))
    except Exception as error:
        message = redact(str(error))
        evidence.case("harness.execution", lambda: check(False, message))
    return evidence.finish()


if __name__ == "__main__":
    raise SystemExit(main())
