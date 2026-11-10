# SDF

SDF collects traces of coding-agent work from OpenTelemetry exported by the
agents, and summarizes a session from that telemetry.

It does not launch, route, or sandbox agents. Codex CLI, Claude Code, and
OpenCode v2 export their own telemetry. Pi does not; its section uses the
community `pi-otel` extension. SDF listens.

Repository history from before this collector is preserved at the git tag
`legacy-v1` (commit `38f45e80ed05fd72e9387c2327d7fd7286bcfac3`).

Checked against the docs and the Codex sources below on 2026-11-09. Codex
source citations are from `openai/codex` commit `8703d32`. OpenCode and Pi
attribute names were checked on 2026-11-10 against `@opencode/cli` 2.2.6 and
`pi-otel` 0.3.0 on `@earendil-works/pi-coding-agent` 1.5.1.

## What each tool actually exports

The tools do not emit the same signals. A field the summary cannot find is
reported as not present. SDF does not estimate tokens or duration. It does
compute a cost from the token counts it already derived and a price table
copied from OpenRouter. That figure is separate from the cost the agent
reported. A model with no published price is `unknown`, not zero.

| Tool | Traces | Logs | Metrics | Limitation |
| --- | --- | --- | --- | --- |
| Claude Code | Beta spans when `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1` and `OTEL_TRACES_EXPORTER=otlp` | Event catalog when `OTEL_LOGS_EXPORTER=otlp` | Counters, including tokens and cost, with `session.id` by default | Traces are off unless both flags are set. HTTP is port 4318. gRPC is port 4317. |
| Codex CLI | Process spans when `otel.trace_exporter` is an OTLP/HTTP URL | Session record (`codex.sse_event`, `codex.tool_result`, and the other `codex.*` events) | Large catalog. Turn metrics carry `conversation.id` only when every tag is valid | Does not read `OTEL_*`. `metrics_exporter` defaults to `statsig`. A model id containing `:` drops the turn metrics. |
| OpenCode v2 (`@opencode/cli`) | Built in. Chunked OTLP/HTTP JSON when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. Runner spans set `session.id` | Process logs only (`cli starting`, watcher, shutdown). No `session.id` | Not exported | No per-session log catalog and no metrics. npm `opencode-ai` is v1, not this CLI. |
| Pi (`@earendil-works/pi-coding-agent`) plus `pi-otel` | Extension spans (`invoke_agent`, `pi.turn`, `chat`, `execute_tool`) with `session.id` | `pi.session.start` and `pi.session.end` only, and only with `PI_OTEL_LOGS=1`. The id is `pi.session.id` | Counters and one duration histogram, only with `PI_OTEL_METRICS=1`. No session id | Not built in. Metrics and logs default off. Default protocol is gRPC on port 4317, which this receiver accepts. |

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
`protocol` is `binary` (protobuf) or `json`. gRPC (`otlp-grpc`) is accepted on
port 4317. The HTTP setup below is the one checked against Codex; point an
`otlp-grpc` endpoint at `http://127.0.0.1:4317` if you use that exporter.

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

## Install

Go 1.26 or newer. The SQLite driver is pure Go (`modernc.org/sqlite`), so the
build does not need cgo or a C compiler. A database written by the previous
Python receiver, including one from before the `trace_id` columns existed,
still opens. Attribute JSON is ordinary JSON either way.

From this repository:

```bash
make build
./sdf serve
```

`make build` is `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o sdf ./cmd/sdf`.
The same command with `GOOS` and `GOARCH` set to `linux`, `darwin`, or
`windows` and `amd64` or `arm64` cross-compiles a static binary. `.goreleaser.yaml`
lists those six targets. `release.disable` is set, so Goreleaser does not
publish a release. `goreleaser build --snapshot --clean` only writes local
archives under `dist/`.

## Run the receiver

```bash
./sdf serve
```

That listens for OTLP/HTTP on `http://127.0.0.1:4318` and OTLP/gRPC on
`127.0.0.1:4317`, and writes `sdf-telemetry.sqlite` in the current directory.
4318 is the OTLP/HTTP default. 4317 is the OTLP/gRPC default, which is where
pi-otel exports unless you change it. There is no authentication on either
port; keep the bind address on localhost. Override with `--host`, `--port`
(HTTP), `--grpc-port`, and `--db`.

The same process serves a read-only UI at `http://127.0.0.1:4320/` unless
you pass `--no-ui`. `--ui-addr` moves the UI. `--host` does not: the page
stays on localhost when the receivers are bound wider. `--prices`, `--repo`,
and `--github` match `sdf summary` and apply to the page. See Open the UI.

`GET /health` returns `{"status":"ok"}`. HTTP exports are `POST /v1/traces`,
`POST /v1/metrics`, and `POST /v1/logs`, `Content-Type: application/json` or
`application/x-protobuf`. The body may use `Content-Length` or
`Transfer-Encoding: chunked`. OpenCode trace batches and pi-otel HTTP posts
are chunked and omit `Content-Length`. When both headers are present, chunked
encoding wins. `Content-Encoding: gzip` is inflated after the body is
framed, and the decompressed size is capped at 8,000,000 bytes. Any other
content encoding gets HTTP 415. gRPC clients call the OTLP `TraceService`,
`MetricsService`, and `LogsService` `Export` RPCs, and may gzip the message.

Rows are one span, one metric point, or one log record. A span also stores
`status_code` (`UNSET`, `OK`, or `ERROR`) and `status_message`. Databases
written before those columns existed still open; the columns are added on
open, and a missing code is `UNSET`. Inspect them with SQLite:

```bash
sqlite3 sdf-telemetry.sqlite \
  "select signal, session_id, name from records order by id desc limit 20"
```

## Open the UI

```bash
./sdf serve
```

The process prints `UI listening on http://127.0.0.1:4320/` (the default).
Open that URL on the same machine.

The page has three views. Overview is the total across sessions and the cost
per accepted outcome, including that figure per agent. Sessions lists every
stored session, newest first, with the agent, start time, duration, models,
input and output tokens, computed cost, the cost the agent reported, the
linked commit or pull request, and whether the link was exact, heuristic, or
none. Filter the list by agent, or click a column to sort it. A session
opens onto the totals, the token buckets, a per-model note, tool-call counts,
the two costs side by side, and the outcome. A field the summary could not
establish says `unknown` and gives the reason (no published price, no working
directory or commit, merge status not requested).

Those numbers are the `sdf summary` report. The page does not price tokens
again. `GET /api/summary` on the UI port is the same document as
`sdf summary --json` for that database, including `--prices` and `--repo`.
`GET /api/snapshot` is that report newest-first, plus the per-agent totals.
Both are read-only. There is no route that writes or deletes.

A known `0` is a published price of zero, such as a `:free` model whose
token buckets are all priced. It is not a stand-in for a missing price. If
any session in a total has an unknown computed cost, the total is unknown
too. The summary does not split one session's tokens across several models,
so the page does not either: several models stay unknown, with the session
total still shown as a session total.

`--github` asks whether a linked commit's pull request merged, on each
refresh, the same way `sdf summary --github` does. Without it, accepted
stays unknown because merge status was not requested. The cost per accepted
outcome uses that answer. It is unknown, not zero, when nothing merged or
when a session has no known outcome.

The UI is on by default so a local demo is `sdf serve` and a browser.
`--no-ui` leaves the OTLP ports only. It is a second listener, embedded in
the sdf binary (HTML, CSS, and script). The page does not load fonts,
scripts, or anything else from the network.

`sdf serve --host 0.0.0.0` exposes the OTLP receivers on every interface.
Those payloads can include prompts and filesystem paths when the agent is
opted in, and the receiver has no authentication. The UI still binds to
`127.0.0.1` in that case. `--ui-addr 0.0.0.0:4320` exposes the same
telemetry in a browser and prints a warning. Do not do that on a network
you do not trust.

An empty database shows an empty state that points at `sdf init`.

## One-step setup

`sdf init` prints the exporter settings for one agent. It does not change a
file unless you pass `--write`. An existing file is copied to a sibling
ending in `.sdf-bak` before it is changed. A file that is not valid JSON is
left untouched. An existing Codex `[otel]` table is left untouched.

```bash
sdf init claude-code
sdf init claude-code --write
sdf init codex --write
sdf init opencode --write
sdf init pi --write
sdf init pi --install
sdf init --check
```

`--check` GETs `http://127.0.0.1:4318/health` and opens a TCP connection to
`127.0.0.1:4317`. The TCP connect does not speak gRPC. Either listener
counts as reachable. `--host`, `--port`, and `--grpc-port` match `sdf serve`.

The settings are the ones in the sections below. Claude Code's variables are
from the [monitoring doc](https://code.claude.com/docs/en/monitoring-usage)
and are written to `~/.claude/settings.json`, not to a repository settings
file. Codex's `[otel]` table is from the
[config reference](https://developers.openai.com/codex/config-reference) and
is written to `$CODEX_HOME/config.toml`. OpenCode v2 only needs
`OTEL_EXPORTER_OTLP_ENDPOINT`; `--write` saves a snippet you source, because
OpenCode does not load that file itself. Pi needs `pi install npm:pi-otel`
(the extension checked here is 0.3.0). `--install` is what runs that command.
`--write` updates `.pi/settings.json` in the current directory, which pi-otel
0.3.0 reads before `~/.pi/agent/settings.json`. The protocol in that file is
`http/json`. pi-otel's own default is gRPC on port 4317, which this receiver
also accepts.

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

OpenCode v2 is the `@opencode/cli` package, not npm `opencode-ai` (that package
is still v1). v1's `experimental.openTelemetry` flag is ignored by v2. Export
turns on when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. That is the only variable
this receiver needs:

```bash
npm install -g @opencode/cli
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
opencode run --model "openrouter/recursive/flint-v1.0:free" "..."
```

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

## Point Pi at it

Pi's own CLI does not export OpenTelemetry. `PI_TELEMETRY` only toggles an
install ping and provider attribution headers. The official
`@earendil-works/pi-telemetry` package is an in-process contract with no
exporter. The package to install is `@earendil-works/pi-coding-agent` (the
older `@mariozechner/pi-coding-agent` name is retired).

Traces, logs, and metrics in this section come from the community extension
`pi-otel`, not from Pi itself. `pi-otel` 0.3.0 defaults to traces on, metrics
and logs off, and gRPC at `http://127.0.0.1:4317`. With `sdf serve` already
running, that default reaches the gRPC port. Metrics and logs stay off until
you turn them on:

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

`PI_OTEL_DISABLED=1` turns the extension off. The same switches can be set in
`.pi/agent/settings.json` under `otel` (`protocol`, `signals.traces`,
`signals.metrics`, `signals.logs`, `spanNaming`, `serviceName`). Leave content
capture off; the summary does not need prompts or tool arguments.

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

## Summarize a session

```bash
./sdf summary
./sdf summary --session sample-claude-1
./sdf summary --json
./sdf summary --all-spans
```

One block per real session (`session.id`, `conversation.id`,
`pi.session.id`, or `gen_ai.conversation.id`), then an `unattributed` block
for records that could not be joined. Each block has:

- signal counts (`traces`, `logs`, `metrics`)
- duration, tokens, and cost, each labeled with the signal it came from
- a span tree (turn or interaction, then model requests, then tool calls with
  wait and execution when those child spans exist)
- log events, with `codex.sse_event` collapsed to a count
- the metric points that arrived
- `missing`, naming any signal or Codex session metric that is absent and why

The default span tree keeps Claude's `claude_code.*` spans, the Codex spans
`run_turn`, `codex.sampling`, `codex.turn_input`, `exec_command`,
`unified_exec.exec_command`, and `codex.tool_blocking`, OpenCode's
`SessionRunner.drain`, `SessionRunner.runStep`, `SessionStep.attempt`,
`Tool.execute`, `FileWrite.write`, and `POST`, and Pi's `invoke_agent`,
`pi.turn`, `chat`, and `execute_tool` (the span name may continue with the
agent, model, or tool). Any other span that carries a call id or tool name
is kept too. Internal names such as `append_items`, `LLM.compile`, and
`Plugin.load` stay in the count and out of the tree. `--all-spans` prints
them.

Tool calls join `exec_command` or `claude_code.tool` to `codex.tool_result` /
`claude_code.tool_result` on `call_id` or `tool_use_id`. OpenCode tool calls
come from `Tool.execute` (`gen_ai.tool.name`, `gen_ai.tool.call.id`). Pi tool
calls come from `execute_tool` (`gen_ai.tool.name`, `gen_ai.tool.call.id`,
`pi.tool.is_error`). Success comes from the log when the span has none. An
OpenCode tool span has no success flag, so the call is `unknown`, unless the
span status is `ERROR`. An `ERROR` span is marked in the text tree
(`status=ERROR`, plus the status message when one was exported). `--json`
includes `status_code` and `status_message` on every span; a span with no
status is `UNSET`. A tool-call span whose status is `ERROR` and that has no
`success` attribute, no `pi.tool.is_error`, and no joined tool-result log is
`success=false`. An explicit flag still wins. Duration prefers the execution
child span, then the tool span, then the log.

Precedence, so the same numbers are not added twice:

- Duration: sum of `claude_code.interaction` spans, else `codex.turn.e2e_duration_ms`,
  else sum of `run_turn` spans, else sum of Pi `invoke_agent` spans, else sum
  of OpenCode `SessionRunner.drain` spans, else `claude_code.active_time.total`
  (active seconds, not wall clock), else the min-to-max timestamp
  (`observed_window`).
- Tokens: `claude_code.token.usage` by `type`, else `codex.turn.token_usage`
  by `token_type`, else the sum of Claude `api_request` token attributes,
  else Codex `response.completed` with the rule below, else OpenCode
  `SessionStep.attempt` `gen_ai.usage.*` (input and cache use the max, output
  is summed), else the sum of Pi `chat` `gen_ai.usage.input_tokens` and
  `output_tokens`. That Pi sum is not added to `gen_ai.client.token.usage`.
  If the chat spans have no usage attributes, the Pi token metric is the
  fallback.
- Cost the agent reported: `claude_code.cost.usage` (USD), else
  `codex.turn.cost_microusd` divided by 1,000,000, else `cost_usd` on
  `claude_code.api_request`, else `usage.estimated_usd` on `codex.turn_cost`,
  else the sum of `pi.cost.usd` on Pi `chat` spans. Claude's figure is
  labeled as the agent's estimate, not the provider price. OpenCode emits
  no cost. The text calls this `agent_reported_cost_usd`. `cost_usd` in
  the text and in `--json` is the same number.
- Cost SDF computed: those same token totals, after the de-dup rule above,
  times the OpenRouter per-token price. See Computed cost.

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

## Computed cost

`sdf summary` prices the token totals it already reduced. It does not add
cache or reasoning back in for an agent whose rule dropped them, and it does
not sum a prefix that the summary kept as a max.

Prices are copied from
[OpenRouter's public models list](https://openrouter.ai/api/v1/models).
The field names and the meaning of `"0"` (that component is free) are
[OpenRouter's pricing object](https://openrouter.ai/docs/guides/overview/models).
`internal/pricing/openrouter.json` is that copy. Its `source` and
`retrieved_at` are the URL and the UTC time of the download. Nothing in that
file was typed in by hand.

Offline summary uses the bundled file. A file written by refresh wins when
it is present:

```bash
sdf prices refresh
sdf summary --prices bundled
sdf summary --prices /path/to/openrouter-models.json
```

`sdf prices refresh` is the only command that calls the network for prices.
It GETs the models URL with no API key and writes
`$XDG_CACHE_HOME/sdf/openrouter-models.json` (or the path in `--out`).
`SDF_PRICE_CACHE` changes the cache directory. A model id that is not in the
table, or a token bucket whose price field is absent, is `computed_cost_usd`
status `unknown` with a reason. The `usd` field is omitted in that case.
It is not `0`.

Matching is exact on the catalog id, or on an `openrouter/` prefix stripped
once (OpenCode's `--model openrouter/...` form), or on a suffix that hits
exactly one catalog id. Two hits is unknown. Two models that resolve to
different rows are unknown. A row with `pricing.overrides` is unknown: the
base rate would be a guess about which conditional rate applied.

How a count is turned into a bucket:

| Token source | Rule |
| --- | --- |
| Claude Code `claude_code.token.usage` and `api_request` | `input` excludes cache read and cache creation. Each is priced on its own. [Monitoring](https://code.claude.com/docs/en/monitoring-usage) documents that split. |
| Codex `codex.turn.token_usage` and `response.completed` | `cached_input` is part of `input` (`TokenUsage.non_cached_input` in `codex-rs/protocol` at the commit cited above). `cache_write_input` is subtracted from input too. `reasoning_output` is subtracted from output. `total` is not priced. A negative remainder is unknown. |
| OpenCode `SessionStep.attempt` | The summary has already taken the max of input and of cache. `gen_ai.usage.cache_read.input_tokens` is priced as a subset of input. |
| Pi `chat` and `gen_ai.client.token.usage` | The summary's input and output only. Cache and reasoning attributes are left out of that total, so they are not priced. |

Cache-creation counts mix the five-minute and one-hour lifetimes. If the
catalog publishes both `input_cache_write` and `input_cache_write_1h` and
they differ, the cost is unknown. Per-request, image, audio, and web-search
prices are not added; the note names them when the row lists a non-zero fee.
Reasoning uses `internal_reasoning` when the row has it, and the completion
price when it does not, because those reasoning tokens are already inside
the output count.

On the 2026-11-10 snapshot, `recursive/flint-v1.0:free` has prompt and
completion `"0"` and no cache-read price. A Pi session, whose totals are
input and output only, computes `$0`. A Claude Code session that also reports
cache-read tokens stays `unknown` rather than treating the missing cache
price as zero. The agent's `$0.065` on that model is still shown as
`agent_reported_cost_usd`.

## Git outcomes

Telemetry says a tool call succeeded. It does not say the change was merged.
`sdf summary` attaches an outcome from the local checkout, and stops when the
match would be a guess. Nothing in this step calls the network unless you
pass `--github`.

A link is **exact** when `claude_code.tool_result` carries
`vcs.ref.head.revision` or a full 40-character `git_commit_id` (including one
inside `tool_parameters`). Claude Code adds those only when
`OTEL_LOG_TOOL_DETAILS=1`, and an abbreviated id is ignored. The SHA has to
exist in the checkout. If it does not, the session stays unlinked. SDF does
not substitute a different commit from the same window.

A link is **heuristic** when there is no exported SHA, the session has one
absolute working directory (`pi.cwd` from pi-otel, or a single
`workspace.host_paths` value from Claude Code), that directory is a git
checkout on this machine, and exactly one commit has a committer time inside
the session window widened by two seconds. Zero commits, or more than one,
stays unlinked. The same commit in two sessions' windows unlinks both.
`--repo PATH` supplies a checkout when the telemetry has no directory. If it
disagrees with a directory the agent did export, the session stays unlinked.

Codex and OpenCode do not export a working directory or a commit SHA on the
signals this receiver stores, so those sessions stay unlinked unless you pass
`--repo` and the time window contains one commit.

`--github` asks for the pull request and CI status of a commit that is already
linked. It uses `gh` when `gh` is on `PATH`, and otherwise `GH_TOKEN` or
`GITHUB_TOKEN` against `api.github.com`. The token is a header, not an
argument. The remote has to be `github.com`. No pull request means not
accepted. Several pull requests, or a failed query, means accepted is unknown.
CI is `success`, `pending`, `failure`, or `none`.

`cost_per_accepted_outcome_usd` is the sum of `computed_cost_usd` for sessions
whose pull request is merged, divided by how many such sessions there are.
It is `unknown`, not zero, when any session in the set has no known outcome,
when an accepted session has unknown computed cost, when the same commit is
accepted on two sessions, or when nothing was merged.

## Tests

```bash
go test ./...
```

`tests/samples/` is sample OTLP, marked `sdf.fixture=sample` and described in
`tests/samples/README.md`. Nothing there was captured from a live agent.
The OpenCode and Pi files follow the attribute names from the 2026-11-10
checks; the ids are fake.

## Gaps

- Content encodings other than `identity` and `gzip` are rejected with
  HTTP 415. Gzip is inflated only up to 8,000,000 decompressed bytes.
  Chunked HTTP and `Content-Length` are both accepted, and gRPC is a
  separate listener on port 4317. gRPC gzip uses the standard compressor.
- Codex's published config reference does not catalog events or metrics. The
  Codex lists in this file can drift from a newer CLI. The Claude list follows
  the monitoring doc, which is versioned by Claude Code release in places
  (several attributes require a stated minimum version).
- Codex has no documented product span schema. The default tree is an
  allowlist from the spans observed on Codex 0.167.0 (`run_turn`,
  `exec_command`, `codex.sampling`, `codex.tool_blocking`, and
  `unified_exec.exec_command`), not a promise that those names stay stable.
  The OpenCode and Pi names in the default tree are the ones observed on
  `@opencode/cli` 2.2.6 and `pi-otel` 0.3.0 with `PI_OTEL_SPAN_NAMING=genai`.
  `--all-spans` shows the rest.
- Codex session metrics are not emitted for a model id that contains `:`.
  That includes OpenRouter `:free` slugs. No config alias was found that
  changes only the metric tag. Process metrics can still arrive, and the
  summary falls back to logs and spans for tokens and duration.
- Records with no session id are attached by trace id, span id, or a
  timestamp that sits inside exactly one session of the same tool. The rest
  are unattributed. Two overlapping sessions leave an ambiguous record
  unattributed. OpenCode process logs and startup spans are not attached by
  timestamp; they have no session id and are not a session catalog. Pi
  metrics have no session id and use that time window. Pi logs are attached
  by `pi.session.id`.
- OpenCode v2.2.6 exports no OTLP metrics. Its logs are process events. The
  summary says so on the `missing` lines instead of claiming the session
  exported nothing.
- Pi has no built-in OTLP exporter. `pi-otel` logs only session start and
  end, and only when `PI_OTEL_LOGS=1`. The summary keeps those two events
  and names that limit. Metrics stay unattributed when they do not fall
  inside exactly one Pi session.
- Retried delta exports can be double-counted. Cumulative series use the
  latest point. Codex and OpenCode input tokens use the max, not the sum, as
  described above. Pi per-turn input and output are summed, and that sum is
  not added to `gen_ai.client.token.usage`.
- The agent-reported cost is whatever the tool put on the wire (Claude USD,
  Codex micro-USD or `usage.estimated_usd`, Pi `pi.cost.usd`). It is not an
  invoice. OpenCode does not emit one. `computed_cost_usd` is the bundled
  or refreshed OpenRouter token price for the same de-duplicated counts.
  It is not an invoice either: conditional overrides are refused, a missing
  component price is unknown, and per-request or web-search fees are not
  added.
- Prompt and tool content are not requested by the setup above. If you turn
  those flags on, the text is stored in this local SQLite file.
- No live capture is committed in this tree. The tests use the sample
  payloads only. OpenCode and Pi behavior above is from the 2026-11-10
  checks of the named package versions, not from a fixture that was copied
  out of a live SQLite file.
