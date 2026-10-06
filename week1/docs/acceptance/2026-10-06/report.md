# 自动验收执行记录

- 时间：2026-10-06T09:54:02.119903+00:00
- 模式：`all`
- 结果：**PASS**；通过 62，失败 0
- 逐请求证据、SSE 原文、Usage 与故障注入轨迹：[report.json](report.json)
- Go 测试日志：[go-test.log](go-test.log)
- `local.*` 使用本机协议桩；`live.*` 调用真实配置上游；两者不能互相替代。

| 用例 | 结果 | 失败断言 |
|---|---|---|
| go.tests | PASS |  |
| go.build | PASS |  |
| local.models | PASS |  |
| local.deepseek-v4-pro.generate | PASS |  |
| local.deepseek-v4-pro.stream | PASS |  |
| local.deepseek-v4-pro.structured | PASS |  |
| local.deepseek-v4-pro.structured_stream | PASS |  |
| local.deepseek-v4-flash.generate | PASS |  |
| local.deepseek-v4-flash.stream | PASS |  |
| local.deepseek-v4-flash.structured | PASS |  |
| local.deepseek-v4-flash.structured_stream | PASS |  |
| local.prompt_versions | PASS |  |
| local.deepseek-v4-pro.prompt_1 | PASS |  |
| local.deepseek-v4-pro.prompt_active | PASS |  |
| local.deepseek-v4-flash.prompt_1 | PASS |  |
| local.deepseek-v4-flash.prompt_active | PASS |  |
| local.unknown_model | PASS |  |
| local.authentication | PASS |  |
| local.deepseek-v4-pro.empty_length_usage | PASS |  |
| local.deepseek-v4-pro.retry_recover | PASS |  |
| local.deepseek-v4-pro.retry_always500 | PASS |  |
| local.deepseek-v4-pro.retry_always429 | PASS |  |
| local.deepseek-v4-pro.retry_always401 | PASS |  |
| local.deepseek-v4-pro.stream_retry_recover | PASS |  |
| local.deepseek-v4-pro.stream_retry_always500 | PASS |  |
| local.deepseek-v4-pro.shared_correction_retry_budget | PASS |  |
| local.deepseek-v4-pro.correct_json | PASS |  |
| local.deepseek-v4-pro.invalid_json | PASS |  |
| local.deepseek-v4-pro.stream_truncated | PASS |  |
| local.deepseek-v4-pro.stream_failed_event | PASS |  |
| local.deepseek-v4-flash.empty_length_usage | PASS |  |
| local.deepseek-v4-flash.retry_recover | PASS |  |
| local.deepseek-v4-flash.retry_always500 | PASS |  |
| local.deepseek-v4-flash.retry_always429 | PASS |  |
| local.deepseek-v4-flash.retry_always401 | PASS |  |
| local.deepseek-v4-flash.stream_retry_recover | PASS |  |
| local.deepseek-v4-flash.stream_retry_always500 | PASS |  |
| local.deepseek-v4-flash.shared_correction_retry_budget | PASS |  |
| local.deepseek-v4-flash.correct_json | PASS |  |
| local.deepseek-v4-flash.invalid_json | PASS |  |
| local.deepseek-v4-flash.stream_truncated | PASS |  |
| local.deepseek-v4-flash.stream_partial_usage | PASS |  |
| local.deepseek-v4-pro.independent_rate_limit | PASS |  |
| local.deepseek-v4-flash.independent_rate_limit | PASS |  |
| live.credentials | PASS |  |
| live.models | PASS |  |
| live.deepseek-v4-pro.generate | PASS |  |
| live.deepseek-v4-pro.stream | PASS |  |
| live.deepseek-v4-pro.structured | PASS |  |
| live.deepseek-v4-pro.structured_stream | PASS |  |
| live.deepseek-v4-flash.generate | PASS |  |
| live.deepseek-v4-flash.stream | PASS |  |
| live.deepseek-v4-flash.structured | PASS |  |
| live.deepseek-v4-flash.structured_stream | PASS |  |
| live.prompt_versions | PASS |  |
| live.deepseek-v4-pro.prompt_1 | PASS |  |
| live.deepseek-v4-pro.prompt_active | PASS |  |
| live.deepseek-v4-flash.prompt_1 | PASS |  |
| live.deepseek-v4-flash.prompt_active | PASS |  |
| live.unknown_model | PASS |  |
| live.authentication | PASS |  |
| live.distinct_upstream_models | PASS |  |
