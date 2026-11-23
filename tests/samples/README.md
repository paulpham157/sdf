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
it reaches the database.

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
