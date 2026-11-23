# Sample OTLP payloads

These JSON files are hand-written sample OpenTelemetry data. They are not
recordings of a real Codex or Claude Code session, and they contain no
prompts.

Every resource carries `sdf.fixture=sample`. The shapes follow the attribute
names documented for each tool (see the repository README) so ingest and
summary tests can run without either CLI.

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
