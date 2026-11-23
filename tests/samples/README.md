# Sample OTLP payloads

These JSON files are hand-written sample OpenTelemetry data. They are not
recordings of a real Codex or Claude Code session, and they contain no
prompts.

`qwen_*.json` follow Qwen Code v0.28.4's telemetry docs and source
(`QwenLM/qwen-code`, `packages/core/src/telemetry`) with `logPrompts: false`.
They were not captured from a live session: ids are fake and there is no
prompt, request, or response text. The `qwen-code.tool_call` logs do carry
made-up tool arguments and error text (a shell command, a `write_file` body),
because Qwen Code sends those whatever `logPrompts` says. Every such value
contains `SDF-FIXTURE-SECRET`, and the store and e2e tests check that none of
it reaches the database. `qwen_sensitive_*.json` is one more session as sent
when a workspace or system settings file turns `includeSensitiveSpanAttributes`
and `logPrompts` back on: `gen_ai.tool.call.arguments` and `.result` on
`qwen-code.tool`, `gen_ai.input.messages`, `gen_ai.output.messages`, and
`gen_ai.system_instructions` on `qwen-code.llm_request` and a
`gen_ai.client.inference.operation.details` log, and a failing
`qwen-code.hook` span, every value marked `SDF-FIXTURE-SECRET`. It is
written by hand from the GenAI semantic conventions, not captured.

Every resource carries `sdf.fixture=sample`. The shapes follow the attribute
names documented for each tool (see the repository README) so ingest and
summary tests can run without either CLI.

`copilot_traces.json` and `copilot_metrics.json` follow the GitHub Copilot
CLI OpenTelemetry reference on docs.github.com and `copilot help monitoring`
from `@github/copilot` 1.0.114. They were not captured from a live session
(that needs a Copilot subscription). Ids are fake and there is no prompt or
tool content. Copilot CLI has no log signal, so there is no
`copilot_logs.json`.

The Go tests in `internal/summary/live_shapes_test.go` build a second sample
in code, also marked `sdf.fixture=sample`, whose field names follow a live
OpenRouter capture. It covers Claude Code and Codex. The Codex half uses
`service.name=codex_exec`, `exec_command` spans, and exporter error logs for
`codex.turn.token_usage`, `codex.turn.cost_microusd`, and
`codex.turn.e2e_duration_ms` on the model id `recursive/flint-v1.0:free`. It is
not that capture: the ids are fake and there is no prompt text.

`opencode_*.json` and `pi_*.json` follow the attribute names seen from
OpenCode v2 (`@opencode/cli`) and from Pi with the `pi-otel` extension.
They are hand-written too: session ids, span ids, and tool call ids are
fake, and there is no prompt text and no credential.

`gemini_*.json` follow Gemini CLI's published telemetry
([docs/cli/telemetry.md](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/telemetry.md))
and its source (`packages/core/src/telemetry`) at
google-gemini/gemini-cli `eecccfcd5aff25de5e78135ca20f7abe3f566955`
(0.71.0-nightly): `service.name=gemini-cli`, `event.name` on every log,
cumulative metrics with `session.id`, and `llm_call` / `tool_call` spans with
`gen_ai.conversation.id`, as sent over OTLP/HTTP JSON with
`telemetry.logPrompts=false` and `telemetry.traces=false`. They were not
captured from a live session. Token counts are made up; the ids are fake.

`grok_traces.json` holds Grok CLI internal spans (the pipeline sent when
`GROK_INTERNAL_OTLP_TRACES_ENDPOINT` points at sdf). Its span names and
attribute keys follow what grok 1.0.92 sends: one `agent.prompt` trace with
four model requests and three `tool.execution` calls, a `session` teardown
trace, and one parentless `sampling.stream_decode` span. It was not captured
from a live session: timestamps and token counts are made up, the ids are
fake, and there is no prompt text, path, or credential.
