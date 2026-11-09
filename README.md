# SDF

SDF collects traces of coding-agent work from the OpenTelemetry support built
into the agents, and summarizes a session from that telemetry.

It does not launch, route, or sandbox agents. Codex CLI and Claude Code export
their own telemetry. SDF listens for it.

The previous implementation is preserved at the git tag **`legacy-v1`**
(commit `38f45e80ed05fd72e9387c2327d7fd7286bcfac3` on `main` at the time of the
tag). That tag is the last snapshot of the orchestrator. This tree is a new v0.

Checked against the docs and the Codex sources below on 2026-11-09. Codex
source citations are from `openai/codex` commit `8703d32`.

## What each tool actually exports

The two tools do not emit the same signals. A field the summary cannot find
is reported as not present. SDF does not estimate tokens, cost, or duration.

### Claude Code

Docs: [Monitoring](https://code.claude.com/docs/en/monitoring-usage).

Telemetry is off until `CLAUDE_CODE_ENABLE_TELEMETRY=1`. Metrics, logs, and
traces are separate exporters. Traces are beta.

| Signal | Exporter variable | What it carries |
| --- | --- | --- |
| Metrics | `OTEL_METRICS_EXPORTER=otlp` | Counters. Claude Code's default temporality is delta. |
| Logs | `OTEL_LOGS_EXPORTER=otlp` | Events. This is the logs/events protocol, not the metrics protocol. |
| Traces | `OTEL_TRACES_EXPORTER=otlp` plus `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1` | Spans. Off unless both are set. |

`service.name` is `claude-code` for terminal sessions and `claude-code-desktop`
for local Desktop Code-tab sessions.

OTLP protocol is `grpc`, `http/protobuf`, or `http/json`. Claude Code has no
default protocol, so set one. This receiver accepts HTTP JSON and HTTP
protobuf, not gRPC.

Metrics (unit in parentheses when the docs attach one):

- `claude_code.session.count`
- `claude_code.lines_of_code.count` (`type` `added` or `removed`, plus `model`)
- `claude_code.pull_request.count`
- `claude_code.commit.count`
- `claude_code.cost.usage` (USD)
- `claude_code.token.usage` (tokens; `type` is `input`, `output`, `cacheRead`, or `cacheCreation`)
- `claude_code.code_edit_tool.decision`
- `claude_code.active_time.total` (seconds; `type` `user` or `cli`)

`session.id` is on metrics by default (`OTEL_METRICS_INCLUDE_SESSION_ID=true`).
`model` is on the token, cost, and lines-of-code metrics.

Events the docs list, exported as logs when `OTEL_LOGS_EXPORTER` is set
(attributes always include a short `event.name`, and the log body used by
current builds is the dotted name):

`claude_code.user_prompt`, `assistant_response`, `tool_result`, `api_request`,
`api_error`, `api_refusal`, `api_request_body`, `api_response_body`,
`tool_decision`, `safety_event`, `permission_mode_changed`, `auth`,
`mcp_server_connection`, `internal_error`, `plugin_installed`, `plugin_loaded`,
`skill_activated`, `at_mention`, `api_retries_exhausted`, `hook_registered`,
`hook_execution_start`, `hook_execution_complete`, `hook_plugin_metrics`,
`compaction`, `subagent_completed`, `feedback_survey`, `retention_sweep`,
`managed_settings_resolved`.

`api_request` carries `model`, `cost_usd`, `duration_ms`, `input_tokens`,
`output_tokens`, `cache_read_tokens`, and `cache_creation_tokens`.
`tool_result` carries `tool_name`, `tool_use_id`, `success`, and `duration_ms`.
Prompt text, tool arguments, and tool output stay redacted unless you set
`OTEL_LOG_USER_PROMPTS`, `OTEL_LOG_TOOL_DETAILS`, or `OTEL_LOG_TOOL_CONTENT`.
The setup below does not set those.

Beta trace spans, when tracing is on:

```text
claude_code.interaction
├── claude_code.llm_request
└── claude_code.tool
    ├── claude_code.tool.blocked_on_user
    └── claude_code.tool.execution
```

`claude_code.hook` is not part of that default hierarchy. It requires detailed
beta tracing (`ENABLE_BETA_TRACING_DETAILED` and `BETA_TRACING_ENDPOINT`),
which also redirects where logs and traces go. Leave that off for this receiver.

`claude_code.llm_request` carries `model`, token counts, and `duration_ms`.
`claude_code.interaction` carries the turn's wall clock. Standard attributes
(`session.id`, `user.id`, `user.email` when signed in, `terminal.type`, and
others) are in the monitoring doc.

### Codex CLI

Config keys: [Configuration reference](https://developers.openai.com/codex/config-reference)
(`otel.exporter`, `otel.trace_exporter`, `otel.metrics_exporter`, and the
`log_user_prompt` / `log_agent_responses` / `log_guardian_assessments` switches).

Crate overview, including which events are opt-in:
[`codex-rs/otel/README.md`](https://github.com/openai/codex/blob/main/codex-rs/otel/README.md).

The config reference documents the exporter keys. It does not list the event
or metric catalog. The names below are the ones the `codex-otel` crate emits
in `openai/codex` at `8703d32` (2026-11-09):

- metrics: [`codex-rs/otel/src/metrics/names.rs`](https://github.com/openai/codex/blob/main/codex-rs/otel/src/metrics/names.rs)
- session logs: [`codex-rs/otel/src/events/session_telemetry.rs`](https://github.com/openai/codex/blob/main/codex-rs/otel/src/events/session_telemetry.rs)

Codex does not use the standard `OTEL_EXPORTER_OTLP_*` environment variables
for its own export. Put an `[otel]` table in the user config
(`$CODEX_HOME/config.toml`, `~/.codex/config.toml` by default). A project-local
`.codex/config.toml` cannot set `otel`; Codex ignores it there.

Each signal has its own exporter, and the defaults are not the same:

| Signal | Config key | Default | OTLP/HTTP endpoint |
| --- | --- | --- | --- |
| Logs | `otel.exporter` | `none` | full URL, including `/v1/logs` |
| Traces | `otel.trace_exporter` | `none` | full URL, including `/v1/traces` |
| Metrics | `otel.metrics_exporter` | `statsig` | full URL, including `/v1/metrics` |

`statsig` sends metrics to Codex's built-in destination, not to this receiver.
Set `metrics_exporter` explicitly or the metrics never arrive here. HTTP
`protocol` is `binary` (protobuf) or `json`. gRPC exists (`otlp-grpc`); this
receiver does not speak it.

Resource attributes are `service.name`, `service.version`, and `env` (from
`otel.environment`, default `dev`). The crate's own examples and loopback test
use `service.name = codex-cli`. Log events also carry `originator` (the CLI's
originator is `codex_cli_rs`), `conversation.id`, `model`, `app.version`,
`auth_mode`, `terminal.type`, and `slug`. `host.name` is attached on logs only.

Logs are the supported session record. Every log from `SessionTelemetry`
includes `conversation.id` and `model`. Event names:

- `codex.conversation_starts`
- `codex.user_prompt` (prompt text is `[REDACTED]` unless `otel.log_user_prompt = true`)
- `codex.api_request` (`duration_ms`, `http.response.status_code`, `attempt`)
- `codex.sse_event` (a `response.completed` event carries `input_token_count`, `output_token_count`, `cached_token_count`, `cache_write_token_count`, `reasoning_token_count`, `tool_token_count`)
- `codex.tool_decision` (`tool_name`, `call_id`, `decision`, `source`)
- `codex.tool_result` (`tool_name`, `call_id`, `duration_ms`, `success`, truncated `arguments` and `output`)
- `codex.sandbox_outcome`
- `codex.websocket_connect`, `codex.websocket_request`, `codex.auth_recovery`
- `codex.turn_cost` (`usage.estimated_usd`)
- `codex.turn_ttft`, `codex.startup_phase`
- `codex.plugin_install_elicitation_sent`, `codex.plugin_install_suggestion`
- `codex.skill_invocation`
- `codex.agent_response` only when `otel.log_agent_responses = true`
- `codex.guardian_assessment` only when `otel.log_guardian_assessments = true`

Traces, when `trace_exporter` is set, are the process's `tracing` spans plus
trace-safe events. There is no documented product span schema comparable to
Claude Code's `claude_code.interaction` tree. Span names are internal. A
completed model response records `gen_ai.usage.input_tokens`,
`gen_ai.usage.output_tokens`, cache token attributes, and
`codex.usage.total_tokens` on the responses span. User prompt text is not
copied onto the trace-safe event.

Metrics are a large in-process catalog. OTLP metrics default to delta
temporality unless `OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE` is set.
Names include `codex.turn.e2e_duration_ms`, `codex.turn.token_usage`
(histogram, `token_type` tag in the loopback test), `codex.turn.cost_microusd`
(counter, micro-USD, and this one is tagged with `conversation.id`),
`codex.tool.call`, `codex.tool.call.duration_ms`, `codex.api_request`, and
`codex.websocket.event`. The full list is `names.rs`. Many of those metrics
do not carry `conversation.id`, so they cannot be joined back to a
conversation. SDF attaches a metric to a session only when `session.id` or
`conversation.id` is on the datapoint or the resource.

`tool_token_count` on `codex.sse_event` / `response.completed` is recorded from
`total_tokens` in `session_telemetry.rs` at the cited commit. The summary
does not add that field on top of the input and output counts.

## Run the receiver

From this repository:

```bash
uv sync
uv run sdf serve
```

That listens on `http://127.0.0.1:4318` and writes `sdf-telemetry.sqlite` in
the current directory. The port is the OTLP/HTTP default. There is no
authentication; keep the bind address on localhost. Override with `--host`,
`--port`, and `--db`.

`GET /health` returns `{"status":"ok"}`. Exports are `POST /v1/traces`,
`POST /v1/metrics`, and `POST /v1/logs`, `Content-Type: application/json` or
`application/x-protobuf`.

Rows are one span, one metric point, or one log record. Inspect them with
SQLite:

```bash
sqlite3 sdf-telemetry.sqlite \
  "select signal, session_id, name from records order by id desc limit 20"
```

## Point Claude Code at it

Set these in the shell that launches `claude`, or in `~/.claude/settings.json`.
A repository `.claude/settings.json` cannot turn these exporters on; Claude
Code ignores the exporter variables there.

```bash
export CLAUDE_CODE_ENABLE_TELEMETRY=1
export CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1
export OTEL_TRACES_EXPORTER=otlp
export OTEL_METRICS_EXPORTER=otlp
export OTEL_LOGS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces
export OTEL_EXPORTER_OTLP_METRICS_ENDPOINT=http://127.0.0.1:4318/v1/metrics
export OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://127.0.0.1:4318/v1/logs
export OTEL_METRIC_EXPORT_INTERVAL=10000
export OTEL_LOGS_EXPORT_INTERVAL=5000
export OTEL_TRACES_EXPORT_INTERVAL=5000
```

Metrics and logs do not need `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA`. Traces do.
The metric interval defaults to 60 seconds; the shorter value above is so a
local session shows up while you are still looking. `claude_code.session.count`
is the metric the docs say to look for after a session starts.
`claude_code.user_prompt` is the event to look for after you submit a prompt.

To confirm the receiver saw them:

```bash
sqlite3 sdf-telemetry.sqlite \
  "select name from records where name like 'claude_code.%'"
```

## Point Codex at it

In `$CODEX_HOME/config.toml` (not in a project `.codex/config.toml`):

```toml
[otel]
environment = "dev"
log_user_prompt = false
exporter = { otlp-http = { endpoint = "http://127.0.0.1:4318/v1/logs", protocol = "binary" } }
trace_exporter = { otlp-http = { endpoint = "http://127.0.0.1:4318/v1/traces", protocol = "binary" } }
metrics_exporter = { otlp-http = { endpoint = "http://127.0.0.1:4318/v1/metrics", protocol = "binary" } }
```

Each endpoint is the full signal URL. Codex does not append `/v1/logs` for
you. `protocol = "json"` also works. Header values in this file are literal;
`${ENV_VAR}` is not expanded.

Quit Codex so the exporter flushes. Then:

```bash
sqlite3 sdf-telemetry.sqlite \
  "select name, session_id from records where name like 'codex.%'"
```

`session_id` in that table is `conversation.id` for Codex and `session.id` for
Claude Code, taken from the datapoint attributes or the resource.

## Summarize a session

```bash
uv run sdf summary
uv run sdf summary --session sample-claude-1
uv run sdf summary --json
```

One block per session:

- tool (`claude-code` or `codex`)
- service name, as exported
- model, from a `model` or `gen_ai.request.model` attribute
- duration, and which signal it came from
- token breakdown, and which signal it came from
- cost in USD, only when a cost metric or cost attribute was exported
- tool calls (`tool_name`, id, success, duration) when a tool-result log or
  Claude tool span was exported

Precedence, so the same numbers are not added twice:

- Duration: Claude `claude_code.interaction` span, else Codex
  `codex.turn.e2e_duration_ms`, else Claude `claude_code.active_time.total`
  (active seconds, not wall clock), else the min-to-max timestamp across the
  session (`observed_window`).
- Tokens: `claude_code.token.usage` by `type`, else `codex.turn.token_usage`
  by `token_type`, else Claude `api_request` token attributes, else Codex
  `codex.sse_event` / `response.completed` token counts.
- Cost: `claude_code.cost.usage` (USD), else `codex.turn.cost_microusd`
  divided by 1,000,000, else `cost_usd` on `claude_code.api_request`, else
  `usage.estimated_usd` on `codex.turn_cost`.
- Tool calls: `claude_code.tool_result` and `codex.tool_result` logs. A
  `claude_code.tool` span is added when its id is not already in those logs.
  If neither exists, `codex.tool.call` counts are listed.

Delta points are summed. Cumulative points and gauges keep the latest point
for that attribute set. A metric with no `session.id` and no `conversation.id`
is not folded into a session that happens to be open. It is reported on its
own only when it actually carries model, token, cost, duration, or tool-call
data.

## Tests

```bash
uv run pytest
```

`tests/samples/` is sample OTLP, marked `sdf.fixture=sample` and described in
`tests/samples/README.md`. Nothing there was captured from a live agent.

## Gaps

- OTLP/HTTP only. gRPC clients, including Claude's quick-start `grpc` snippet
  and Codex `otlp-grpc`, will not connect. Use the HTTP setup above.
- Codex's published config reference does not catalog events or metrics. The
  Codex lists in this file can drift from a newer CLI. The Claude list follows
  the monitoring doc, which is versioned by Claude Code release in places
  (several attributes require a stated minimum version).
- Codex traces have no stable span names. The summary does not pretend they do.
- Codex metrics often omit `conversation.id`. Those points stay unscoped.
  `codex.turn.cost_microusd` is an exception in the current sources: it is
  tagged with `conversation.id`.
- Retried delta exports can be double-counted. Cumulative series use the
  latest point.
- Cost figures are whatever the tool put on the wire (Claude USD, Codex
  micro-USD or `usage.estimated_usd`). They are not an invoice.
- Prompt and tool content are not requested by the setup above. If you turn
  those flags on, the text is stored in this local SQLite file.
- No live capture from either CLI is part of this v0. The tests use the
  sample payloads only.
