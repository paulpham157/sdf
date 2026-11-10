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
`otel.environment`, default `dev`). What `service.name` is depends on which
Codex binary is running. Codex 0.167.0 `codex exec` exports
`service.name=codex_exec`. The otel crate's examples and loopback test use
`codex-cli`. Both classify as tool `codex`. Log events also carry `originator`
(the CLI's originator is `codex_cli_rs`; `codex exec` uses `codex_exec`),
`conversation.id`, `model`, `app.version`, `auth_mode`, `terminal.type`, and
`slug`. `host.name` is attached on logs only.

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
do not carry `conversation.id`. SDF still stores them. It attaches one to a
session when the datapoint or resource has `session.id` or `conversation.id`,
or when its trace id (or span id) is already on exactly one session, or when
its timestamp falls inside exactly one session of the same tool. Anything
left over is reported as unattributed, not as `session (none)`.

Session metrics are recorded only if every tag passes
`validate_tag_value` in Codex 0.167.0
(`codex-rs/otel/src/metrics/validation.rs` at `rust-v0.167.0`, commit
`086ccc83`). A tag value may contain ASCII letters, digits, `.`, `_`, `-`,
`/`, and `+`. A colon is rejected. `SessionMetricTagValues` puts the wire
`model` on those instruments and does not run it through
`sanitize_metric_tag_value`. There is no config key that keeps the provider
model id for the API call and substitutes a different string on the metric
tag: `model` is that id. An OpenRouter slug such as
`recursive/flint-v1.0:free` therefore drops `codex.turn.token_usage`,
`codex.turn.cost_microusd`, and `codex.turn.e2e_duration_ms`. Codex logs
`metrics histogram [codex.turn.token_usage] failed: tag value contains invalid
characters: ...` instead. A slash-only id such as `openai/gpt-4.1-mini` passes
the check. Process metrics that do not carry the model tag, such as
`codex.process.start`, can still arrive.

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

## OpenRouter

Both tools can call a free OpenRouter model. The id is the provider slug,
including the `:free` suffix. Do not put the key in a settings file.

Claude Code, in the shell that launches `claude` (and the telemetry variables
from the section above):

```bash
export ANTHROPIC_BASE_URL=https://openrouter.ai/api
export ANTHROPIC_AUTH_TOKEN="$OPENROUTER_API_KEY"
export ANTHROPIC_API_KEY=""
export ANTHROPIC_DEFAULT_SONNET_MODEL="recursive/flint-v1.0:free"
export ANTHROPIC_DEFAULT_HAIKU_MODEL="recursive/flint-v1.0:free"
export ANTHROPIC_DEFAULT_OPUS_MODEL="recursive/flint-v1.0:free"
export ANTHROPIC_DEFAULT_FABLE_MODEL="recursive/flint-v1.0:free"
export CLAUDE_CODE_SUBAGENT_MODEL="recursive/flint-v1.0:free"
claude -p --model "recursive/flint-v1.0:free" "..."
```

`ANTHROPIC_API_KEY=""` keeps Claude from also sending a first-party key.
Claude will warn that the slug is not an Anthropic model. Its `cost_usd` is
Claude Code's own estimate, not OpenRouter's price. A `:free` model is $0 at
OpenRouter.

Codex, in `$CODEX_HOME/config.toml`:

```toml
model = "recursive/flint-v1.0:free"
model_provider = "openrouter"

[model_providers.openrouter]
name = "OpenRouter"
base_url = "https://openrouter.ai/api/v1"
wire_api = "responses"
env_key = "OPENROUTER_API_KEY"
supports_websockets = false

[otel]
environment = "dev"
log_user_prompt = false
exporter = { otlp-http = { endpoint = "http://127.0.0.1:4318/v1/logs", protocol = "binary" } }
trace_exporter = { otlp-http = { endpoint = "http://127.0.0.1:4318/v1/traces", protocol = "binary" } }
metrics_exporter = { otlp-http = { endpoint = "http://127.0.0.1:4318/v1/metrics", protocol = "binary" } }
```

Run with `codex exec`. Traces and logs arrive. The turn metrics do not, for
the colon reason above. The summary says so and uses `run_turn` for duration
and `codex.sse_event` / `response.completed` for tokens. No `codex.turn_cost`
log was emitted for this model in the 2026-11-10 capture, so cost stays unset.

## Summarize a session

```bash
uv run sdf summary
uv run sdf summary --session sample-claude-1
uv run sdf summary --json
uv run sdf summary --all-spans
```

One block per real session (`session.id` or `conversation.id`), then an
`unattributed` block for records that could not be joined. Each block has:

- signal counts (`traces`, `logs`, `metrics`)
- duration, tokens, and cost, each labeled with the signal it came from
- a span tree (turn or interaction, then model requests, then tool calls with
  wait and execution when those child spans exist)
- log events, with `codex.sse_event` collapsed to a count
- the metric points that arrived
- `missing`, naming any signal or Codex session metric that is absent and why

The default span tree keeps Claude's `claude_code.*` spans and the Codex
spans `run_turn`, `codex.sampling`, `codex.turn_input`, `exec_command`,
`unified_exec.exec_command`, and `codex.tool_blocking`, plus any other span
that carries `call_id` or `tool_name`. Internal names such as `append_items`
stay in the count and out of the tree. `--all-spans` prints them.

Tool calls join `exec_command` or `claude_code.tool` to `codex.tool_result` /
`claude_code.tool_result` on `call_id` or `tool_use_id`. Success comes from
the log when the span has none. Duration prefers the execution child span,
then the tool span, then the log.

Precedence, so the same numbers are not added twice:

- Duration: sum of `claude_code.interaction` spans, else `codex.turn.e2e_duration_ms`,
  else sum of `run_turn` spans, else `claude_code.active_time.total` (active
  seconds, not wall clock), else the min-to-max timestamp (`observed_window`).
- Tokens: `claude_code.token.usage` by `type`, else `codex.turn.token_usage`
  by `token_type`, else the sum of Claude `api_request` token attributes,
  else Codex `response.completed` with the rule below.
- Cost: `claude_code.cost.usage` (USD), else `codex.turn.cost_microusd`
  divided by 1,000,000, else `cost_usd` on `claude_code.api_request`, else
  `usage.estimated_usd` on `codex.turn_cost`. Claude's figure is labeled as
  the agent's estimate, not the provider price.

Codex `response.completed` token fields are not all additive. `input_token_count`,
`cached_token_count`, and `cache_write_token_count` already include the
conversation prefix, which the next request sends again. The summary takes
the max of those. `output_token_count` and `reasoning_token_count` are new
tokens and are summed. When `turn.id` is present, only the last
`response.completed` in that turn is used, so a retry does not add the
attempt again. `tool_token_count` is ignored: at the cited Codex commit it is
recorded from `total_tokens`.

Delta metric points are summed. Cumulative points and gauges keep the latest
point for that attribute set. A Codex model id that contains `:` leaves the
turn metrics missing; the summary quotes the exporter error and uses the log
and span fallbacks above.

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
- Codex has no documented product span schema. The default tree is an
  allowlist from the spans observed on Codex 0.167.0 (`run_turn`,
  `exec_command`, `codex.sampling`, `codex.tool_blocking`, and
  `unified_exec.exec_command`), not a promise that those names stay stable.
  `--all-spans` shows the rest.
- Codex session metrics are not emitted for a model id that contains `:`.
  That includes OpenRouter `:free` slugs. No config alias was found that
  changes only the metric tag. Process metrics can still arrive, and the
  summary falls back to logs and spans for tokens and duration.
- Records with no session id are attached by trace id, span id, or a
  timestamp that sits inside exactly one session. The rest are unattributed.
  Two overlapping sessions leave an ambiguous span unattributed.
- Retried delta exports can be double-counted. Cumulative series use the
  latest point. Codex input tokens use the max, not the sum, as described
  above.
- Cost figures are whatever the tool put on the wire (Claude USD, Codex
  micro-USD or `usage.estimated_usd`). They are not an invoice, and they are
  not OpenRouter's price.
- Prompt and tool content are not requested by the setup above. If you turn
  those flags on, the text is stored in this local SQLite file.
- No live capture from either CLI is part of this v0. The tests use the
  sample payloads only.
