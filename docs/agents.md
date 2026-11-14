# Agents

Checked against the docs and the Codex sources below on 2026-11-09. Codex
source citations are from `openai/codex` commit `8703d32`. OpenCode and Pi
attribute names were checked on 2026-11-10 against `@opencode/cli` 2.2.6 and
`pi-otel` 0.3.0 on `@earendil-works/pi-coding-agent` 1.5.1.

The table of what each tool exports is in the [README](../README.md).

## Claude Code

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
default protocol, so set one. This receiver accepts all three. HTTP JSON and
HTTP protobuf use port 4318. gRPC uses port 4317. `Content-Encoding: gzip`
is inflated, including on a chunked body. The decompressed payload has the
same 8,000,000 byte cap as the raw body. Any other content encoding is
rejected with HTTP 415. gRPC `grpc-encoding: gzip` is accepted.

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

## Codex CLI

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
`.codex/config.toml` cannot set `otel`; Codex ignores it there. `sdf init codex
--write` also writes `hooks.json` with a `SessionStart` and `SessionEnd` hook.
The hook posts only when the session id equals the thread id, so a descendant
thread reports nothing.

Each signal has its own exporter, and the defaults are not the same:

| Signal | Config key | Default | OTLP/HTTP endpoint |
| --- | --- | --- | --- |
| Logs | `otel.exporter` | `none` | full URL, including `/v1/logs` |
| Traces | `otel.trace_exporter` | `none` | full URL, including `/v1/traces` |
| Metrics | `otel.metrics_exporter` | `statsig` | full URL, including `/v1/metrics` |

`statsig` sends metrics to Codex's built-in destination, not to this receiver.
Set `metrics_exporter` explicitly or the metrics never arrive here. HTTP
`protocol` is `binary` (protobuf) or `json`. gRPC (`otlp-grpc`) is accepted on
port 4317. The HTTP setup below is the one checked against Codex; point an
`otlp-grpc` endpoint at `http://127.0.0.1:4317` if you use that exporter.

Resource attributes are `service.name`, `service.version`, and `env` (from
`otel.environment`, default `dev`). What `service.name` is depends on which
Codex process exports. Checked against the Codex 0.167.0 tag
(`rust-v0.167.0`): `codex exec` calls `set_default_originator("codex_exec")`
and passes no service-name override, so it exports `service.name=codex_exec`.
A live `codex exec` on 2026-11-11 exported `codex_exec`. The git-hook event
for that commit used the same name.
`CODEX_INTERNAL_ORIGINATOR_OVERRIDE`, when set, replaces that originator and
therefore the exec service name. The interactive CLI's exporter is the app
server, which passes a service-name override of `codex-app-server`, so the
override does not rename it. The otel crate's examples and loopback test use
`codex-cli`. The CLI's default originator, when nothing overrides it, is
`codex_cli_rs`. Log events also carry `originator` (`codex_cli_rs` for the
CLI, `codex_exec` for `codex exec`), `conversation.id`, `model`,
`app.version`, `auth_mode`, `terminal.type`, and `slug`. `host.name` is
attached on logs only. All of these names classify as tool `codex`.

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

## OpenCode v2

OpenCode v2 is the `@opencode/cli` package, not npm `opencode-ai` (that package
is still v1). v1's `experimental.openTelemetry` flag is ignored by v2. Export
turns on when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. `sdf init opencode --write`
also installs a plugin that posts when a session is created. OpenCode has no
event for a session ending.

The bundled exporter posts traces to `${endpoint}/v1/traces` and logs to
`${endpoint}/v1/logs` as `application/json`. Trace batches use
`Transfer-Encoding: chunked` and omit `Content-Length`. There is no metrics
exporter. `service.name` is `opencode`.

Session spans (`SessionRunner.drain`, `SessionRunner.runStep`,
`SessionStep.attempt`, `Tool.execute`) carry `session.id`. Startup spans and
the process logs do not, so the summary leaves them unattributed instead of
attaching them because the timestamps overlap. `SessionStep.attempt` carries
`gen_ai.request.model` and `gen_ai.usage.*`. Input and cache counts already
include the conversation prefix, so the summary takes the max. Output is
summed. OpenCode does not emit a cost attribute. `Tool.execute` carries
`gen_ai.tool.name` and `gen_ai.tool.call.id` and no success flag, so the tool
call is `unknown`.

## Pi

Pi's own CLI does not export OpenTelemetry. `PI_TELEMETRY` only toggles an
install ping and provider attribution headers. The official
`@earendil-works/pi-telemetry` package is an in-process contract with no
exporter. The package to install is `@earendil-works/pi-coding-agent` (the
older `@mariozechner/pi-coding-agent` name is retired).

Traces, logs, and metrics come from the community extension `pi-otel`, not
from Pi itself. `pi-otel` 0.3.0 defaults to traces on, metrics and logs off,
and gRPC at `http://127.0.0.1:4317`. With `sdf serve` already running, that
default reaches the gRPC port. Metrics and logs stay off until you turn them
on with `PI_OTEL_METRICS=1` and `PI_OTEL_LOGS=1`. `PI_OTEL_DISABLED=1` turns
the extension off.

The session id pi-otel exports is the session file's name, which is a
timestamp joined to the id. `sdf init pi --write` installs an extension that
reads the id from the session manager instead and posts the open and the close.
A `session_shutdown` fired because the session was replaced, on `reload`,
`new`, `resume`, or `fork`, is not a close and is not posted.

The same switches can be set in `.pi/agent/settings.json` under `otel`
(`protocol`, `signals.traces`, `signals.metrics`, `signals.logs`,
`spanNaming`, `serviceName`). Leave content capture off; the summary does not
need prompts or tool arguments.

With `PI_OTEL_SPAN_NAMING=genai`, `service.name` is `pi` and the span names
are `invoke_agent <agent>`, `pi.turn`, `chat <model>`, and
`execute_tool <tool>`. Spans set `session.id`, `pi.session.id`, and
`gen_ai.conversation.id` to the same id. Logs set only `pi.session.id` (plus
`gen_ai.system=pi`) and are only `pi.session.start` and `pi.session.end`.
Metrics (`gen_ai.client.token.usage`, `gen_ai.client.operation.duration`,
`gen_ai.client.tool.calls`, `gen_ai.client.tool_calls_per_operation`) have no
session id. The summary attaches a Pi metric when its timestamp falls inside
exactly one Pi session; otherwise the metric stays unattributed.

`chat` spans carry `gen_ai.request.model`, `gen_ai.usage.input_tokens`,
`gen_ai.usage.output_tokens`, and `pi.cost.usd`. On the 2026-11-10 capture the
per-turn input and output were additive (975+103+15+152 input and
530+33+99+170 output, matching `gen_ai.client.token.usage` of 1245 and 832).
The summary sums those span counts and does not add the metric again. Cache
and reasoning attributes stay out of that total. `execute_tool` carries
`gen_ai.tool.name`, `gen_ai.tool.call.id`, and `pi.tool.is_error` (false means
the call succeeded).

## Point Claude Code at it

Set these in the shell that launches `claude`, or in `~/.claude/settings.json`.
A repository `.claude/settings.json` cannot turn these exporters on; Claude
Code ignores the exporter variables there. `sdf init claude-code --write` also
adds a `SessionStart` and `SessionEnd` hook that reports the session boundary.

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

`OTEL_EXPORTER_OTLP_PROTOCOL=grpc` with
`OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4317` also works. The signal
URLs above are not used in that mode; gRPC does not append `/v1/traces`.

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

`session_id` in that table is the first present key among `session.id`,
`conversation.id`, `pi.session.id`, and `gen_ai.conversation.id`, taken from
the datapoint attributes or the resource. Codex uses `conversation.id`.
Claude Code and OpenCode use `session.id`. Pi logs set only `pi.session.id`.

## OpenRouter

Claude Code and Codex can call a free OpenRouter model. The id is the provider slug,
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

## Point OpenCode v2 at it

```bash
npm install -g @opencode/cli
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
opencode run --model "openrouter/recursive/flint-v1.0:free" "..."
```

`OTEL_EXPORTER_OTLP_ENDPOINT` is the only variable this receiver needs.

## Point Pi at it

```bash
npm install -g --ignore-scripts @earendil-works/pi-coding-agent
pi install npm:pi-otel
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4317
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc
export PI_OTEL_METRICS=1
export PI_OTEL_LOGS=1
export PI_OTEL_SPAN_NAMING=genai
pi --print --provider openrouter --model "recursive/flint-v1.0:free" "..."
```

HTTP JSON still works if you would rather not use gRPC. Point the endpoint at
port 4318 and set the protocol:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
export OTEL_EXPORTER_OTLP_PROTOCOL=http/json
```
