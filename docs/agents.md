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
HTTP protobuf use port 4318 and send `Content-Type: application/json` or
`application/x-protobuf`. Any other media type is HTTP 415. A request that
carries an `Origin` header is HTTP 403. gRPC uses port 4317. `Content-Encoding: gzip`
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
`gen_ai.request.model` and `gen_ai.usage.*`. One attempt span is one provider
request: OpenCode annotates it with the finish usage of that single
`llm.stream` call (`packages/core/src/session/runner/step.ts`), not a
session running total. Every request is billed its full input, including the
resent conversation prefix, and its own cache reads and writes, so the
summary sums input, cache, output, and reasoning across attempts (issue
#200). A span stored twice (same trace and span id) counts once. OpenCode does not emit a cost attribute. `Tool.execute` carries
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

The same switches can be set under `otel` in the project file
`.pi/settings.json` or the global file `~/.pi/agent/settings.json` (pi-otel
reads the project file first, environment variables override both; this is
what `sdf init pi --write` and `--write --global` update)
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

## Optional receiver token

When `sdf serve` is started with `--otlp-token` or `SDF_OTLP_TOKEN`, exporters
must send the same value as `Authorization: Bearer <token>`.

For agents that honor the standard OpenTelemetry environment variables
(Claude Code, OpenCode v2, and Pi with pi-otel):

```bash
export OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer%20$SDF_OTLP_TOKEN"
```

The space after `Bearer` is written as `%20`, as the OpenTelemetry
specification requires for `OTEL_EXPORTER_OTLP_HEADERS`.

For Grok CLI a literal space always works, as Grok's own guide writes it:

```bash
export OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer $SDF_OTLP_TOKEN"
```

Grok 1.0.92 was also seen to accept `%20` in a live run on 2026-11-23: with
a receiver token set, its logs and metrics POSTs returned 200. Earlier these
docs said `%20` gets a 401, from the source alone: `parse_header_list` in
grok-build `xai-grok-telemetry/src/external/config.rs` (`2bdd1d6`) splits on
`,` and `=` and trims without percent-decoding. Which build decodes it is
not pinned down, so `sdf init grok` prints the literal space.

Codex does not read `OTEL_*`. Put a `headers` table on each `otlp-http`
exporter instead. Those values are literal; `${SDF_OTLP_TOKEN}` is not
expanded:

```toml
exporter = { otlp-http = { endpoint = "http://127.0.0.1:4318/v1/logs", protocol = "binary", headers = { Authorization = "Bearer TOKEN" } } }
trace_exporter = { otlp-http = { endpoint = "http://127.0.0.1:4318/v1/traces", protocol = "binary", headers = { Authorization = "Bearer TOKEN" } } }
metrics_exporter = { otlp-http = { endpoint = "http://127.0.0.1:4318/v1/metrics", protocol = "binary", headers = { Authorization = "Bearer TOKEN" } } }
```

`sdf init --write` does not add the header or the Codex `headers` field.
The session hooks and the `sdf init --git-hooks` hooks send the token
themselves when `SDF_OTLP_TOKEN` or `SDF_OTLP_TOKEN_FILE` is set where the
agent (or `git`) runs; they read it at run time and never store it. See
[docs/receiver.md](receiver.md#hooks-and-the-token).

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

If the receiver has a token set, also export
`OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer $SDF_OTLP_TOKEN"` with a
literal space, which always works; 1.0.92 also accepted `%20` live (see
[Optional receiver token](#optional-receiver-token)). Grok has no
config-file key for headers.

`sdf init grok` prints those lines and the matching `[telemetry]` table
(`otel_enabled`, `otel_metrics_exporter`, `otel_logs_exporter`,
`otel_protocol`, `otel_endpoint`, the names in the guide's "Config file"
section and in `TelemetryConfig`, `xai-grok-telemetry/src/config.rs`), with
`otel_log_user_prompts`, `otel_log_assistant_responses`,
`otel_log_tool_details`, and `otel_log_tool_content` pinned to `false`.
Unset, `otel_log_assistant_responses` follows `otel_log_user_prompts`, so
all four are written; an `OTEL_LOG_*` variable in the environment still
wins over the file, and `sdf init grok` warns about each one Grok would read
as on (`1`/`true`/`yes`/`on`). `otel_log_tool_details` is a metadata gate,
not a body gate (a 4 KB `tool_parameters` preview, full file paths, verbatim
MCP/skill/plugin names); pinning it off is stricter than the guide's
"Recommended fleet gates", which turns it on for SIEM joins. sdf does not
need it for Grok: the session hook supplies the working directory.
`sdf init grok --write` appends that table to `$GROK_HOME/config.toml`
(default `~/.grok/config.toml`) when the file has no `[telemetry]` table;
an existing `[telemetry]` table, in any TOML form (`[ telemetry ]`,
inline `telemetry = {...}`, dotted `telemetry.x` keys), is left unchanged,
and a `config.toml` that is not valid TOML is refused. A project
`.grok/config.toml` cannot set `otel_*`. Environment variables win over the
file. It also installs `hooks/sdf-session.json` beside it, a session hook
that posts the session's open and close to `POST /v1/sessions`. Grok sets
`GROK_SESSION_ID` on that process; the event JSON field is `sessionId`.
User hooks under `~/.grok/hooks/` are trusted. A project `.grok/hooks/`
file is not, until that folder is trusted. The reasons for the hook
command's shape are in [docs/receiver.md](receiver.md#one-step-setup). With
`sdf init --git-hooks`, a commit Grok makes carries an `SDF-Session`
trailer and links to the session, because Grok puts `GROK_SESSION_ID` in
its shell tool's environment
([docs/receiver.md](receiver.md#git-hooks)). These three were written from
grok-build `2bdd1d6` (read 2026-11-23) and then checked against a live,
signed-in `grok` 1.0.92 in headless mode (`grok -p --always-approve`) on
2026-11-23, with a throwaway repository whose path has a space:

- With only the `config.toml` that `--write` produced (no `OTEL_*` or
  `GROK_EXTERNAL_OTEL` in the environment), logs and metrics arrived with
  `session.id`.
- The hook posted an open (`source` was `new`) and a close (`reason` was
  `shutdown`) for the same id the telemetry carried, with the repository
  path as `cwd`.
- A commit Grok made from its shell tool with the git hooks installed
  carried `SDF-Session: <session.id>` and linked as `hook`. With the git
  hooks removed, the next session's commit linked as `heuristic` from the
  hook's `cwd`, with no `--repo`.
- Grok ran the `SessionStart` hooks in `~/.claude/settings.json` with
  `GROK_HOOK_EVENT=session_start`, and the sdf Claude Code hook there posted
  nothing.

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
`${ENV_VAR}` is not expanded. If the receiver has a token set, add
`headers = { Authorization = "Bearer TOKEN" }` to each `otlp-http` table,
with the token written out (see [Optional receiver token](#optional-receiver-token)).

Quit Codex so the exporter flushes. Then:

```bash
sqlite3 sdf-telemetry.sqlite \
  "select name, session_id from records where name like 'codex.%'"
```

`session_id` in that table is the first present key among `session.id`,
`conversation.id`, `pi.session.id`, and `gen_ai.conversation.id`, taken from
the datapoint attributes or the resource. Codex uses `conversation.id`.
Claude Code and OpenCode use `session.id`. Pi logs set only `pi.session.id`.

## Qwen Code

> Mapping below is from QwenLM/qwen-code v0.28.4 (e8424c88, released
> 2026-11-22): [docs/developers/development/telemetry.md](https://github.com/QwenLM/qwen-code/blob/v0.28.4/docs/developers/development/telemetry.md)
> and `packages/core/src/telemetry`. It was not checked against a live
> session. `tests/samples/qwen_*.json` are built from those sources with
> `logPrompts: false`.

Qwen Code forked Gemini CLI's telemetry and renamed it: events, metrics,
and spans use `qwen-code.*` and `service.name` is `qwen-code`. It is off
until `telemetry.enabled` is true. The default protocol is gRPC on port
4317; `otlpProtocol: "http"` sends OTLP/JSON and appends `/v1/traces`,
`/v1/logs`, and `/v1/metrics` to `otlpEndpoint`. `telemetry.outfile` turns
OTLP export off.

```json
{
  "telemetry": {
    "enabled": true,
    "otlpEndpoint": "http://127.0.0.1:4318",
    "otlpProtocol": "http",
    "logPrompts": false
  }
}
```

`logPrompts` defaults to **true**: prompts, API request text (which includes
files Qwen read and its memory extractor's reads of `~/.qwen/memories/`),
and response text go into the logs. `sdf init qwen` sets it to `false`,
also when the file already says `true`. `includeSensitiveSpanAttributes`
(default false) copies messages and tool data into spans; `sdf init qwen`
sets it back to false when it finds it on.

No setting turns off tool arguments: every `qwen-code.tool_call` log carries
the full JSON arguments of the call in `function_args` (the shell command,
the whole content passed to `write_file` or `edit`), and a failed call
carries the tool's error text in `error` and `error.message`
(`logToolCall`, `packages/core/src/telemetry/loggers.ts`, v0.28.4). Its
tool spans repeat the error text. sdf discards these before storing
anything: it keeps the tool name, call id, duration, success, and error
type, and records `function_args_bytes`, `error_bytes`, and `sdf.redacted`.

- Session: logs and spans carry `session.id` (LLM and interaction spans also
  `gen_ai.conversation.id`). The auto-instrumented HTTP `POST` child spans
  have no session id and are attributed through the trace id. Metrics carry
  no `session.id` unless `telemetry.metrics.includeSessionId` is true, so
  they reach a session only by time window.
- Duration is the sum of `qwen-code.interaction` spans (one trace root per
  prompt, including tool approval waits). Without spans it is the
  min-to-max timestamp of Qwen logs (`logs qwen-code window`).
- Tokens are the sum of `input_token_count`, `output_token_count`,
  `cached_content_token_count`, and `thoughts_token_count` on
  `qwen-code.api_response` logs (`total_token_count` is not added), else
  `gen_ai.usage.*` and `thoughts_token_count` on `qwen-code.llm_request`
  spans, else `qwen-code.token.usage` by `type` (`input`, `output`,
  `thought`, `cache`). That counter belongs to the process, not the session:
  sdf places its points by their own time, adds up process runs, and
  subtracts what an earlier session in the same process had counted (see
  [docs/gaps.md](gaps.md)). Input includes cached tokens. With OpenAI-compatible
  providers (the default) output includes thoughts, which Qwen estimates
  from text when the provider does not report them; with Gemini-native
  responses it does not. The totals include subagents and the background
  memory extractor (its spans are separate single-span traces with
  `llm_request.context=standalone`); Qwen does not split them out.
- Tool calls come from `qwen-code.tool` spans: wait from the
  `qwen-code.tool.blocked_on_user` child, execution from
  `qwen-code.tool.execution`, joined to the `qwen-code.tool_call` log by call
  id for `success`. Without spans the logs are used, then
  `qwen-code.tool.call.count`.
- Cost: Qwen Code exports none. Computed cost is unknown until
  `internal/pricing` has a rule for the Qwen token sources.
- Differences from Gemini CLI: `qwen-code.*` instead of `gemini_cli.*`; no
  `tool_token_count` and no `tool` token type; metrics drop `session.id` by
  default; span names follow Claude Code's shape (`interaction`,
  `llm_request`, `tool`, `blocked_on_user`, `execution`, `hook`) with
  `gen_ai.operation.name`; `QWEN_TELEMETRY_*` instead of
  `GEMINI_TELEMETRY_*`, and `OTEL_EXPORTER_OTLP_ENDPOINT` also sets the
  endpoint.
- A published run of an earlier Qwen Code release (spencerclark.dev,
  2026-10-10; it predates v0.28.4 and was not repeated here) had every
  `qwen-code.hook` span marked ERROR with "hook runner returned no output without error
  detail" even with no hooks configured. The summary shows them as they
  arrive.

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
opencode models                      # the ids your configured providers offer
opencode run --model "<provider/model>" "..."
```

`OTEL_EXPORTER_OTLP_ENDPOINT` is the only variable this receiver needs when
no token is set. If the receiver has a token set, also export
`OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer%20$SDF_OTLP_TOKEN"`.

Pick the model id from `opencode models`. On `@opencode/cli` 2.3.2 (checked
2026-11-23) the OpenRouter id this page used to show,
`openrouter/recursive/flint-v1.0:free`, is reported as `Model unavailable`
unless OpenRouter is configured as an OpenCode provider. The free ids 2.3.2
lists without that are OpenCode-hosted, such as `opencode/flint-40b-free`,
and they need an OpenCode account and its free quota. The export is the same
either way; a run that fails at the provider still sends its process traces
and logs.

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

If the receiver has a token set, also export
`OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer%20$SDF_OTLP_TOKEN"`.

HTTP JSON still works if you would rather not use gRPC. Point the endpoint at
port 4318 and set the protocol:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
export OTEL_EXPORTER_OTLP_PROTOCOL=http/json
```


## GitHub Copilot CLI

> Mapping below is from the
> [Copilot CLI OpenTelemetry reference](https://docs.github.com/en/copilot/reference/enterprise-administrators/opentelemetry-reference),
> `copilot help monitoring` in `@github/copilot` 1.0.114, and the
> [copilot-cli changelog](https://github.com/github/copilot-cli/blob/main/changelog.md),
> checked 2026-11-23. It was not checked against a live session: a live run
> needs a Copilot subscription. `tests/samples/copilot_*.json` are built
> from those documents.

Copilot CLI exports **traces and metrics, no logs**. Export turns on when
`OTEL_EXPORTER_OTLP_ENDPOINT` is set (or `COPILOT_OTEL_ENABLED=true`, or a
file exporter path). It speaks OTLP/HTTP only: `OTEL_EXPORTER_OTLP_PROTOCOL`
is `http/json` (default) or `http/protobuf` (1.0.61 and later). `grpc` is not
supported and falls back with a warning, so use port 4318.

```bash
npm install -g @github/copilot
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
export OTEL_EXPORTER_OTLP_PROTOCOL=http/json
export OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT=NO_CONTENT
copilot
```

These `export` lines stay in that shell after `copilot` exits, so other
OTel programs started there will send to sdf too. To scope them to one run,
run copilot through `sh`, which works from bash, zsh, and fish:
`sh -c '. "$1" && exec copilot' sh ~/.config/sdf/copilot.env`.

`sdf init copilot --write` merges these lines into
`~/.config/sdf/copilot.env` for you to source (details and precedence in
[docs/receiver.md](receiver.md#one-step-setup)). Copilot CLI has no
settings-file key for the endpoint; the process environment wins, and
per-signal `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` /
`OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` override the generic endpoint.
Organization or device managed settings can also configure export.
A plain `http://` endpoint such as this receiver exports normally. The
warning in the docs that an `http://` endpoint disables export sits in the
Transport security section and applies when one of the certificate or
client-key variables (`OTEL_EXPORTER_OTLP_*CERTIFICATE`, `*CLIENT_KEY`) is
set; changelog 1.0.107 says the same for `copilot help monitoring`.
`sdf init copilot` warns when one is set. Content capture
(`OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT`) defaults to `NO_CONTENT`; only
`SPAN_ONLY` and `SPAN_AND_EVENT` put prompts and responses on spans
(`EVENT_ONLY` puts them on span events, which sdf does not store). The legacy
`OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` counts only when the
standard variable is empty or unset, though it alone still adds tool schemas
(`gen_ai.tool.definitions`). The user id is the pseudonymous
`enduser.pseudo.id`.

`service.name` is `github-copilot` (override with `OTEL_SERVICE_NAME`; the
summary also recognizes `gen_ai.agent.id` = `github.copilot.*`). Each user
message is one `invoke_agent` span carrying `gen_ai.conversation.id`, with a
`chat {model}` CLIENT span per model request and an `execute_tool {tool}`
span per tool call. Subagents (`explore`, `task`) are nested `invoke_agent`
spans in the same trace. Tool and subagent spans are attributed through the
trace id.

- Top-level `invoke_agent` spans are the ones with `server.address` (the
  docs say subagents have none), else `gen_ai.agent.id` =
  `github.copilot.default`, else those with no `invoke_agent` above them.
  In fleet mode (`invoke_workflow`), the subagents directly under a workflow
  that no top-level `invoke_agent` contains are counted like top-level ones.
- Duration is the union of the time covered by top-level `invoke_agent`
  and `invoke_workflow` spans, so parallel runs count once (0-10 s and
  5-15 s is 15 s). Nested subagent spans are inside them.
- Tokens are the sum of `gen_ai.usage.input_tokens`, `output_tokens`,
  `cache_read.input_tokens`, `cache_write.input_tokens`, and
  `reasoning.output_tokens` on `chat` spans (compaction requests included).
  Input includes cache read and write; output includes reasoning. Without
  chat usage the top-level `invoke_agent` totals are used, then the
  `gen_ai.client.inference.usage.*` counters. 1.0.64 named the cache write
  count `cache_creation.input_tokens`, and earlier builds used underscore
  names (`cache_read_input_tokens`, `cache_creation_input_tokens`,
  `reasoning_output_tokens`); they are read too, and only the first name
  present on a span counts.
- Tool calls come from `execute_tool` spans. There is no success flag:
  `error.type` or an ERROR status is a failure, anything else a success.
- Cost: Copilot reports no USD. `github.copilot.cost` is a billing
  multiplier, not money. `github.copilot.nano_aiu` (AI units / 1e9) is read
  from top-level `invoke_agent` spans only and shown on the `agent cost`
  missing line. Computed cost is always unknown: Copilot is billed in
  premium requests and AI units, not per token, so sdf never prices its
  token counts (a test in `internal/summary` keeps it that way).
- `github.copilot.session.shutdown` (`total_premium_requests`, lines
  added/removed), `gen_ai.client.operation.exception`, hook and compaction
  events are span events. sdf does not store span events yet.
- Metrics carry no session id. A point is attached to a session only when it
  falls inside exactly one Copilot session window.

## Grok CLI

> Mapping below was first sketched from the public xAI guide
> [Monitoring Usage (External OpenTelemetry)](https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-pager/docs/user-guide/24-monitoring-usage.md)
> (`grok_code.schema.version = v1`, status alpha) and then corrected against
> an authenticated `grok` 1.0.92 session on 2026-11-23 (Asia/Bangkok).

Grok CLI's external stream is **metrics and logs only** when both the master
switch and an exporter are set (spans: see **Traces** below):

```bash
export GROK_EXTERNAL_OTEL=1
export OTEL_METRICS_EXPORTER=otlp
export OTEL_LOGS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
grok
```

If the receiver has a token set, also export
`OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer $SDF_OTLP_TOKEN"`. The
literal space always works; 1.0.92 also accepted `%20` (see
[Optional receiver token](#optional-receiver-token)).

`service.name` is `grok-cli`. Events always carry `session.id`; metrics do
too unless `OTEL_METRICS_INCLUDE_SESSION_ID=0`. Startup metrics
(`grok_code.startup.*`) in the live capture had no `session.id` and stay
unattributed even when they overlap a session window.

The published guide said there was no cost metric. The live session exported
`grok_code.cost.usage` in USD (sum of delta points). `grok_code.api_request`
also carried `cost_usd_micros` and, on some events, `cache_creation_tokens`
(0 in that session), which the public field list omitted. Agent-reported
cost prefers the metric, then the sum of `cost_usd_micros / 1_000_000`.

Token counts come from `grok_code.token.usage` (`type` = `input` \| `output`
\| `reasoning` \| `cache_read`) or, if those points are missing, from
`grok_code.api_request` token fields. Live rates matched OpenRouter only
when `cache_read` is treated as a subset of `input` and `reasoning` as a
subset of `output`. A model id such as `grok-4.7` may suffix-match
`x-ai/grok-4.7`. That catalog row has a `min_prompt_tokens` 200000
override: below the threshold base rates apply, an `api_request` at or
above it uses the override rates, and a metric-only aggregate at or above
it stays unknown (see `docs/summary.md`).

Tool calls come from `grok_code.tool_result` (`tool_name`, `success`,
`duration_ms`, `tool_use_id`). Duration prefers
`grok_code.session_end.duration_secs`, then the sum of
`grok_code.turn_completed.duration_ms`.

Live logs arrived with an empty body and no `event.name` attribute. sdf
reads the OTEL `LogRecord.event_name` field at ingest, and the summary
also infers `grok_code.*` event names from attribute shapes so already
stored unnamed records still summarize. The live model was `grok-4.7`.
`tool_decision` used the name `search_replace` for two calls whose later
`tool_result` used `write`.

The official protocol list includes `grpc`. A cheap gRPC check is recorded
in the 2026-11-23 notes when it was run; http/protobuf is the documented
default and was used for the live capture.

**Working directory.** Grok CLI's telemetry carries none. The session hook
that `sdf init grok --write` installs supplies it instead: Grok's hook input
has `workspaceRoot` (the git toplevel of the session directory) and `cwd`,
and the hook posts that path to `/v1/sessions`, where it is stored as
`sdf.session.cwd` and used like any exported working directory (details and
sources in [docs/receiver.md](receiver.md#one-step-setup)). Without the hook
there is no checkout for a Grok session. Why the telemetry cannot carry one,
checked against the public source at `xai-org/grok-build` commit `2bdd1d6`
(synced 2026-09-29):

- The resource is a fixed set: `service.name`, `service.version`,
  `client.version`, `app.entrypoint`, `terminal.type`,
  `grok_code.schema.version`
  ([guide L207-L215](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-pager/docs/user-guide/24-monitoring-usage.md#L207-L215),
  [`build_resource`](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-telemetry/src/external/providers.rs#L131-L154)).
- `OTEL_RESOURCE_ATTRIBUTES` is ignored on purpose. The resource is built with
  `Resource::builder_empty()` so the env detector never runs
  ([guide L119-L120](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-pager/docs/user-guide/24-monitoring-usage.md#L119-L120),
  [providers.rs L132](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-telemetry/src/external/providers.rs#L132)).
  `sdf init` cannot add a cwd attribute that way.
- Log attributes are a closed enum with no cwd, workspace, or repository key
  ([schema.rs L74-L196](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-telemetry/src/external/schema.rs#L74-L196)).
  `grok_code.session_start` has only the boolean `is_git_repo`
  ([guide L287](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-pager/docs/user-guide/24-monitoring-usage.md#L287)).
- `grok_code.tool_result` `file_path` is a per-tool file, sent only with
  `OTEL_LOG_TOOL_DETAILS=1` ([guide L99](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-pager/docs/user-guide/24-monitoring-usage.md#L99)), and `$HOME` is rewritten
  to `~` ([guide L312-L313](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-pager/docs/user-guide/24-monitoring-usage.md#L312-L313),
  [`redact_user_paths`](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-secrets/src/sanitizer.rs#L182-L185)).
  sdf does not request that gate and does not guess a checkout from it.

With the hook installed, a Grok session links by the heuristic (one commit
in the session window), by the git hook, or by a trailer, with no `--repo`,
and a `--repo` that names a different checkout leaves it unlinked as for
other agents. Without the hook, use `sdf summary --repo PATH`: with one
commit in the session window the link is `heuristic`. In that case the
outcome reason says Grok's telemetry has no working directory and names both
`sdf init grok --write` and `--repo`.

**Traces.** Grok CLI's external OTEL stream has no traces exporter
([guide L175-L176](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-pager/docs/user-guide/24-monitoring-usage.md#L175-L176)). Grok also has an internal span pipeline
that normally goes to xAI. It can be pointed at sdf; this was checked live
with `grok` 1.0.92 headless (`grok -p`) on 2026-11-23:

```bash
export GROK_INTERNAL_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces
# Replaces the xAI bearer token on these exports. Use the sdf token if the
# receiver has one, or any value if it does not. A literal space always works.
export GROK_INTERNAL_OTLP_HEADERS="Authorization=Bearer $SDF_OTLP_TOKEN"
```

- The endpoint is used verbatim and wins over the legacy `OTEL_EXPORTER_OTLP_*`
  fallback, which applies only while `GROK_EXTERNAL_OTEL` is off
  ([endpoints.rs L68-L71](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-config/src/endpoints.rs#L68-L71),
  [L159-L177](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-config/src/endpoints.rs#L159-L177)).
- Without the header override every export carries `Authorization: Bearer`
  with the user's xAI token. `GROK_INTERNAL_OTLP_HEADERS` is applied after
  it and replaces it ([xai-grok-otel config.rs L87-L93](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-otel/src/config.rs#L87-L93)).
  In the live run the receiver had a token set and every `/v1/traces` POST
  returned 200, so the override reached it. `x-userid`, `x-teamid`,
  `x-grok-client-version`, and `X-XAI-Token-Auth: xai-grok-cli` are still
  sent ([config.rs L53-L95](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-otel/src/config.rs#L53-L95)).
  sdf reads headers only to check the token and never stores them; no
  stored row in the live database contained the token or `Authorization`.
- Spans are exported only when Grok has a usable login and its telemetry
  mode has session metrics on ([provider.rs L164](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-otel/src/provider.rs#L164)).
  Unauthenticated, no spans arrived. Signed in, spans arrived with and
  without `GROK_TELEMETRY_ENABLED=true`. Turning Grok telemetry on also sends
  Grok's own analytics to xAI. `OTEL_TRACES_EXPORTER=none` turns these spans
  off ([endpoints.rs L212-L219](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-config/src/endpoints.rs#L212-L219)).
- This schema is internal to Grok and undocumented. The names below are what
  1.0.92 sent; a newer release can change them.

Resource: `service.name=grok-cli`, `service.version` (`1.0.92 (b06756fd6256)`),
`client.name` (`grok-pager`), `client.version`, `app.entrypoint`
(`headless`), `terminal.type`, `host.arch`, `os.type`, `os.version`, and
`user.id` / `team.id` (also `organization.id`, `api_key.id`, `deployment.id`
when known, [config.rs L96-L106](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-otel/src/config.rs#L96-L106)).
No working directory, on the resource or on any span. The only path seen was
`file_path` on `tool.search_replace`.

The session id is the snake_case span attribute `session_id`, not
`session.id`. It is on `agent.prompt`, `session`, `session.spawn`,
`session.process_conversation_turn`, `tools.execute`, `tool.execution`, and
a few `session.*` spans. sdf reads `session_id` only on `grok-cli` spans; the
rest of a trace joins by trace id, and parentless spans (for example
`sampling.stream_decode`) by the session's time window. One prompt produced
about 230 spans in some 40 traces. The prompt trace:

```text
agent.prompt                                session_id
  session.handle_prompt
    session.process_conversation_turn       session_id, model_id, input_tokens, output_tokens, cache_read_tokens, stop_reason, request_id
      turn.sampling                         one model request
        http.create_response_stream         model_id, success, status_code, ttft_ms, endpoint
      tools.execute                         one tool batch; session_id, model_id
        permission.wait
        tool.decision                       tool_name, tool_use_id, decision
        tool.execution                      tool_name, tool_use_id (= tool_call_id), success, outcome, tool_id, tool_input_size_bytes, tool_result_size_bytes
          tool.search_replace | tool.run_terminal_cmd | ...
```

Startup (`startup.*`, `session.new_session`, `session.spawn`) is a separate
trace, and teardown (`session_end.*`) hangs off a `session` span.

sdf's default tree keeps `agent.prompt`, `session.process_conversation_turn`,
`turn.sampling`, `http.create_response_stream`, `tools.execute`,
`permission.wait`, and `tool.execution`; `--all-spans` shows the rest. Tool
calls come from `tool.execution` joined to `grok_code.tool_result` on
`tool_use_id` (source `traces tool.execution; logs grok_code.tool_result`).
When there is no `session_end` or `turn_completed` log, duration is the sum
of `agent.prompt` spans. Tokens and cost still come from the external metrics;
the span token attributes are not added. Model requests outside the prompt
trace (a `grok-4.6` request, a failed request) show as extra
`http.create_response_stream` roots. The hand-written sample
`tests/samples/grok_traces.json` (`sdf.fixture=sample`) follows these shapes;
it is not the capture.

Also seen in that run: the external stream accepted
`OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer%20$SDF_OTLP_TOKEN"` (logs
and metrics POSTs returned 200 with a receiver token set), so 1.0.92 decodes
`%20` there. A literal space always works (see
[Optional receiver token](#optional-receiver-token)).

## Gemini CLI

> Mapping below is from Gemini CLI's published
> [telemetry doc](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/telemetry.md)
> and its telemetry source (`packages/core/src/telemetry`) at
> google-gemini/gemini-cli `eecccfcd5aff25de5e78135ca20f7abe3f566955`
> (0.71.0-nightly, 2026-11-20 UTC). It has not been checked against a live,
> signed-in session.

Gemini CLI exports traces, logs, and metrics once `telemetry.enabled` is
true. Settings live in the `telemetry` object of `~/.gemini/settings.json`. A
workspace `.gemini/settings.json` overrides it, but only when Gemini CLI
trusts the folder; in an untrusted folder the workspace file is not loaded.
The system settings file (`/etc/gemini-cli/settings.json` on Linux,
`/Library/Application Support/GeminiCli/settings.json` on macOS,
`C:\ProgramData\gemini-cli\settings.json` on Windows, or
`GEMINI_CLI_SYSTEM_SETTINGS_PATH`) is applied after both and wins over them.
`GEMINI_TELEMETRY_*` variables override every file, and
`OTEL_EXPORTER_OTLP_ENDPOINT` overrides `telemetry.otlpEndpoint` when
`GEMINI_TELEMETRY_OTLP_ENDPOINT` is unset
([configuration layers](https://github.com/google-gemini/gemini-cli/blob/main/docs/reference/configuration.md)).
The default exporter is gRPC at
`http://localhost:4317`, which reaches `sdf serve` as is. With
`otlpProtocol: "http"` it posts OTLP/HTTP JSON to `<otlpEndpoint>/v1/traces`,
`/v1/logs`, and `/v1/metrics`. `telemetry.outfile` replaces the endpoint, so
nothing reaches the receiver while it is set. `target: "gcp"` without
`useCollector` sends to Google Cloud instead.

`service.name` is `gemini-cli` (`service.version` is the Node.js version).
Every log and metric data point carries `session.id`, `installation.id`,
`interactive`, and, when known, `auth_type` and `user.email` (when you are
signed in with Google). Logs name themselves in the `event.name` attribute
(`gemini_cli.config`, `gemini_cli.user_prompt`, `gemini_cli.api_request`,
`gemini_cli.api_response`, `gemini_cli.api_error`, `gemini_cli.tool_call`,
`gemini_cli.file_operation`, `gemini_cli.conversation_finished`, and more).
The body is a sentence such as `Tool call: read_file. Decision: auto_accept.
Success: true. Duration: 14ms.` Each `api_request`, `api_response`, and
`api_error` is followed by a `gen_ai.client.inference.operation.details` log
with the same counts under `gen_ai.usage.*`.

Spans are named for the operation (`llm_call`, `tool_call`, `agent_call`,
...). They carry `gen_ai.conversation.id` (the session id),
`gen_ai.operation.name`, and `gen_ai.agent.name=gemini-cli`, and `llm_call`
adds `gen_ai.request.model`. Prompts, outputs, and tool definitions are on
spans only with `telemetry.traces=true`.

Metrics are cumulative and exported every 10 seconds:
`gemini_cli.session.count`, `gemini_cli.token.usage` (`model`, `type` =
`input` | `output` | `thought` | `cache` | `tool`), `gemini_cli.api.request.count`,
`gemini_cli.tool.call.count` (`function_name`, `success`, `decision`,
`tool_type`), latency histograms, and the GenAI
`gen_ai.client.token.usage` and `gen_ai.client.operation.duration`.

What the summary takes from it:

| Summary field | Gemini CLI source |
| --- | --- |
| session id | `session.id` on logs and metrics; `gen_ai.conversation.id` on spans |
| tool | `gemini-cli` (from `service.name`, or a `gemini_cli.*` name) |
| model | `model` on `config`, `api_request`, `api_response`, `api_error`, and metric points; `gen_ai.request.model` on spans |
| tokens | `gemini_cli.token.usage` by `type`, else the sum of `api_response` `*_token_count` fields |
| duration | min-to-max of the session's logs and spans (no session or turn duration is exported) |
| tool calls | `gemini_cli.tool_call` logs: `function_name`, `success`, `duration_ms` (no call id) |
| errors | `gemini_cli.api_error` in the log events; failed tools as `success=false`; `ERROR` status on spans |
| agent-reported cost | not exported |
| computed cost | unknown: there is no pricing rule for the Gemini token sources yet |

`input` (`input_token_count`) is Gemini's `promptTokenCount`, which includes
the `cache` count. `output` excludes `thought`. `tool` is the tool-use prompt
count. Prompts are in the logs by default: `telemetry.logPrompts` defaults to
`true`, and it adds `prompt`, `request_text`, `response_text`, and
`function_args`. Set it to `false` unless you want that text in the local
SQLite file. `sdf init gemini --write` does that when the file has no
`logPrompts` yet.

## Point Gemini CLI at it

```bash
sdf init gemini --write
gemini
```

Or without touching the settings file:

```bash
export GEMINI_TELEMETRY_ENABLED=true
export GEMINI_TELEMETRY_TARGET=local
export GEMINI_TELEMETRY_OTLP_ENDPOINT=http://127.0.0.1:4318
export GEMINI_TELEMETRY_OTLP_PROTOCOL=http
export GEMINI_TELEMETRY_LOG_PROMPTS=false
gemini
```

gRPC works too: leave the protocol at its default and set
`GEMINI_TELEMETRY_OTLP_ENDPOINT=http://127.0.0.1:4317`.
