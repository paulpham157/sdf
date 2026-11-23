# SDF

SDF collects traces of coding-agent work from OpenTelemetry the agents export,
and summarizes a session from that telemetry.

It does not launch, route, or sandbox agents. Codex CLI, Claude Code,
OpenCode v2, Qwen Code, Grok CLI, Gemini CLI, and GitHub Copilot CLI
export their own telemetry. Pi does not; its section uses the community
`pi-otel` extension. SDF listens.

The tools do not emit the same signals. A field the summary cannot find is
reported as not present. SDF does not estimate tokens or duration. It does
compute a cost from the token counts it already derived and a price table
copied from OpenRouter. That figure is separate from the cost the agent
reported. A model with no published price is `unknown`, not zero.

| Tool | Traces | Logs | Metrics | Limitation |
| --- | --- | --- | --- | --- |
| Claude Code | Beta spans when `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1` and `OTEL_TRACES_EXPORTER=otlp` | Event catalog when `OTEL_LOGS_EXPORTER=otlp` | Counters, including tokens and cost, with `session.id` by default, when `OTEL_METRICS_EXPORTER=otlp` | Nothing is exported until `CLAUDE_CODE_ENABLE_TELEMETRY=1`. Traces also need both trace flags. HTTP is port 4318. gRPC is port 4317. |
| Codex CLI | Process spans when `otel.trace_exporter` is an OTLP/HTTP URL | Session record (`codex.sse_event`, `codex.tool_result`, and the other `codex.*` events) | Large catalog. Turn metrics carry `conversation.id` only when every tag is valid | Does not read `OTEL_*`. `metrics_exporter` defaults to `statsig`. A model id containing `:` drops the turn metrics. |
| Qwen Code (`@qwen-code/qwen-code`) | Built in. `qwen-code.interaction`, `qwen-code.llm_request`, `qwen-code.tool` (with `blocked_on_user` and `execution` children), `qwen-code.subagent`, and `qwen-code.hook` spans with `session.id` and `gen_ai.*` | `qwen-code.*` events (`api_response`, `tool_call`, `user_prompt`, ...) with `session.id`. Prompts and request/response text when `logPrompts` is on, which is the default. Every `tool_call` carries the full tool arguments and error text whatever `logPrompts` says; sdf discards them before storing | `qwen-code.*` cumulative counters, including `qwen-code.token.usage`. No session id unless `telemetry.metrics.includeSessionId` | Off until `telemetry.enabled`. Default protocol is gRPC on port 4317. No cost metric. `sdf init qwen` sets `logPrompts: false`. Mapping is from the docs and source (v0.28.4), not a live capture. |
| Grok CLI (`grok`) | Internal spans only, when `GROK_INTERNAL_OTLP_TRACES_ENDPOINT` points here and `GROK_INTERNAL_OTLP_HEADERS` replaces the xAI token. Undocumented schema, mapped from a live 1.0.92 run (`agent.prompt`, `tool.execution`, ...). The external OTEL stream is logs and metrics only | `grok_code.*` events (`user_prompt`, `api_request`, `tool_result`, `session_end`, ...) with `session.id`, when `GROK_EXTERNAL_OTEL=1` and `OTEL_LOGS_EXPORTER=otlp`. Prompt text and tool bodies are off by default | `grok_code.*` delta counters, including `grok_code.token.usage` and `grok_code.cost.usage` (USD), with `session.id` by default. Startup metrics have none | Off until `GROK_EXTERNAL_OTEL=1` and an exporter are both set. HTTP protobuf uses port 4318; gRPC uses port 4317. No working directory on the events; the session hook from `sdf init grok --write` supplies it, and `--repo` is the fallback. A literal space in the token header always works; 1.0.92 was also seen live to accept `%20`. Mapped from a live 1.0.92 session; `sdf init grok --write`, its session hook (with the working directory), and the git-hook session id were also checked live with 1.0.92. |
| OpenCode v2 (`@opencode/cli`) | Built in. Chunked OTLP/HTTP JSON when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. Runner spans set `session.id` | Process logs only (`cli starting`, watcher, shutdown). No `session.id` | Not exported | No per-session log catalog and no metrics. npm `opencode-ai` is v1, not this CLI. |
| Gemini CLI (`@google/gemini-cli`) | Built in. `llm_call` and `tool_call` spans with `gen_ai.conversation.id`. Prompt and tool payloads only with `telemetry.traces=true` | `gemini_cli.*` events (`config`, `user_prompt`, `api_request`, `api_response`, `api_error`, `tool_call`, ...) with `session.id` | Cumulative counters, including `gemini_cli.token.usage`, with `session.id` | Off until `telemetry.enabled`. Default protocol is gRPC on port 4317. No cost metric. `logPrompts` defaults to true. Mapping is from the docs and source, not a live capture. |
| Pi (`@earendil-works/pi-coding-agent`) plus `pi-otel` | Extension spans (`invoke_agent`, `pi.turn`, `chat`, `execute_tool`) with `session.id` | `pi.session.start` and `pi.session.end` only, and only with `PI_OTEL_LOGS=1`. The id is `pi.session.id` | Counters and one duration histogram, only with `PI_OTEL_METRICS=1`. No session id | Not built in. Metrics and logs default off. Default protocol is gRPC on port 4317, which this receiver accepts. |
| GitHub Copilot CLI (`@github/copilot`) | Built in. `invoke_agent`, `chat {model}`, and `execute_tool {tool}` spans with `gen_ai.conversation.id`. Lifecycle events are span events | None. Copilot CLI has no OTLP log signal | GenAI histograms and counters (`gen_ai.client.inference.usage.*`) plus `github.copilot.*`. No session id | On when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. OTLP/HTTP only (`http/json` default). No USD cost; AI units and premium requests instead. Content capture is off by default. Mapping is from the docs and `copilot help monitoring` (1.0.114), not a live capture. |

What each tool exports, and how to point it at the receiver, is in
[docs/agents.md](docs/agents.md).

Repository history from before this collector is preserved at the git tag
`legacy-v1` (commit `38f45e80ed05fd72e9387c2327d7fd7286bcfac3`).

## Quickstart (5 minutes)

You need one supported coding agent already installed. The receiver itself
needs no API key or network connection.

**1. Install.** With Go 1.26 or newer:

```bash
go install github.com/paulpham157/sdf/cmd/sdf@latest
```

This is the primary install path. It puts `sdf` in `$(go env GOPATH)/bin`
(usually `~/go/bin`); make sure that directory is on your `PATH`. Until the
first tagged release, `@latest` resolves to the newest commit on `main`.

Prebuilt binaries for Linux, macOS, and Windows on amd64 and arm64 will be on
[GitHub Releases](https://github.com/paulpham157/sdf/releases) after the first
release is published; there is no release yet. Once there is, download the
archive for your platform with `checksums.txt`, verify it, and unpack it:

```bash
VERSION=X.Y.Z OS=linux ARCH=amd64   # a published version; OS: linux|darwin; ARCH: amd64|arm64
curl -LO https://github.com/paulpham157/sdf/releases/download/v${VERSION}/sdf_${VERSION}_${OS}_${ARCH}.tar.gz
curl -LO https://github.com/paulpham157/sdf/releases/download/v${VERSION}/checksums.txt
sha256sum --ignore-missing -c checksums.txt   # macOS: shasum -a 256 --ignore-missing -c checksums.txt
tar -xzf sdf_${VERSION}_${OS}_${ARCH}.tar.gz sdf && sudo mv sdf /usr/local/bin/
```

Windows archives are `.zip` files with `sdf.exe` inside.

**2. Check the build.**

```bash
sdf version
```

prints the version, commit, build date, Go version, and platform. `sdf
--version` and `sdf -v` are the same. A `go install` of a tagged version
prints that version; an untagged or local build prints the pseudo-version the
go tool recorded.

**3. Start the receiver.**

```bash
sdf serve
```

listens for OTLP/HTTP on `127.0.0.1:4318` and OTLP/gRPC on `127.0.0.1:4317`,
writes `sdf-telemetry.sqlite` in the current directory, and serves the UI at
`http://127.0.0.1:4320/`. Leave it running. [Run](#run) has the flags.

**4. Point an agent at it.** In a second terminal:

```bash
sdf init claude-code --write   # or codex, opencode, pi, qwen, grok, gemini, copilot
sdf init --check               # is the receiver reachable on 4318 and 4317?
```

Without `--write`, `sdf init` only prints the exporter settings. With it, the
agent's settings file is updated; follow anything it prints that it cannot
set for you, such as an environment variable to export. What each agent
needs is in [docs/agents.md](docs/agents.md).

**5. Restart the agent** so it reads the new settings, and use it for a
session as usual.

**6. See the session.** Open `http://127.0.0.1:4320/`, or run:

```bash
sdf summary
```

which prints one block per session: duration, tokens, tool calls, the cost
the agent reported, and the cost computed from the bundled price table.
`sdf check` runs the case set embedded in the binary against the summary;
see [docs/check.md](docs/check.md).

## Build from source

The SQLite driver is pure Go (`modernc.org/sqlite`), so the build does not need
cgo or a C compiler. A database written by the previous Python receiver,
including one from before the `trace_id` columns existed, still opens.

```bash
git clone https://github.com/paulpham157/sdf && cd sdf
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o sdf ./cmd/sdf
./sdf version
```

`make build` runs the same command, if you have `make`. Release archives are
built by GoReleaser from `.goreleaser.yaml`; `goreleaser release --snapshot
--clean` builds them locally into `dist/` without publishing anything.

## Run

```bash
sdf serve
```

That listens for OTLP/HTTP on `http://127.0.0.1:4318` and OTLP/gRPC on
`127.0.0.1:4317`, writes `sdf-telemetry.sqlite` in the current directory, and
serves a read-only UI at `http://127.0.0.1:4320/`. Authentication is off by
default; keep both binds on localhost. `--otlp-token`, `--otlp-token-file`,
`SDF_OTLP_TOKEN`, or `SDF_OTLP_TOKEN_FILE` is optional: when a token is set,
OTLP writes require `Authorization: Bearer <token>`. The HTTP receiver stores
a body only when `Content-Type` is `application/json` or `application/x-protobuf`,
and it refuses a request that carries `Origin`. On a loopback bind it also
refuses a `Host` other than `localhost`, `127.0.0.1`, or `[::1]`. The UI
refuses that same foreign `Host` on every route. A token does not bypass
those checks. Details are in [docs/receiver.md](docs/receiver.md).
Before anything is stored, the receiver drops tool arguments
(`function_args`) and the GenAI content attributes (`gen_ai.tool.call.arguments`,
`gen_ai.tool.call.result`, `gen_ai.input.messages`, `gen_ai.output.messages`,
`gen_ai.system_instructions`) from every log, span, and metric, whatever agent
sent them and whatever its settings say, and the tool and hook error text
from Qwen Code and Gemini CLI tool calls and Qwen Code hook spans. It keeps
the tool name, call id, duration, success, and error type, the dropped sizes
(`function_args_bytes`, `error_bytes`, ...), and the dropped keys in
`sdf.redacted`; see [docs/gaps.md](docs/gaps.md).
`GET http://127.0.0.1:4318/health` (the OTLP/HTTP port, not the UI port)
stays open and returns `{"status":"ok"}`. `sdf init --check` probes it for you.

```bash
sdf version            # version, commit, build date, Go version
sdf summary            # one block per session, then unattributed records
sdf summary --json
sdf summary --session ID --all-spans
sdf decision add --id ID --statement TEXT --status hypothesis
sdf decision link --relation implements --from decision:ID --to session:ID
sdf audit verify       # hash-chained ledger; see docs/ledger.md
sdf task declare --ledger PATH ...  # opt-in: also chain the task (or set SDF_LEDGER)
sdf verify SHA --task ID --ledger PATH  # opt-in: chain the evidence and verdict
sdf prices refresh     # the only command that calls the network for prices
sdf init claude-code   # print exporter settings; --write changes a file
sdf init qwen          # Qwen Code telemetry settings
sdf init grok          # Grok CLI external OTEL; --write adds [telemetry] and a session hook
sdf init --check       # is the receiver reachable on 4318 and 4317?
sdf init --git-hooks   # record the commit SHA the agent does not export
sdf check              # embedded local cases against the summary; no network
```

`--host`, `--port`, `--grpc-port`, `--db`, `--ui-addr`, and `--budget`
override the `sdf serve` defaults. `--no-ui` leaves the OTLP ports only.
`--otlp-token` or `--otlp-token-file` (or `SDF_OTLP_TOKEN` /
`SDF_OTLP_TOKEN_FILE`) turns on the optional bearer token; see
[docs/receiver.md](docs/receiver.md) and [docs/agents.md](docs/agents.md)
for how to send it from an agent. The session hooks and git hooks send it
when `SDF_OTLP_TOKEN` or `SDF_OTLP_TOKEN_FILE` is set where they run; it is
never written into a hook file. `--prices`, `--repo`, `--github`, and
`--budget` match `sdf summary` and apply to the page. Every command prints
its flags with `--help`.

The UI page has three views: an overview of totals and cost per accepted
outcome, a session list, and one session. `GET /api/summary`,
`GET /api/snapshot`, and `GET /api/decisions` are read-only. There is no route
that writes or deletes. The page loads nothing from the network.

## Read more

| Document | What it covers |
| --- | --- |
| [docs/agents.md](docs/agents.md) | What each tool exports, and the settings that point it at the receiver |
| [docs/receiver.md](docs/receiver.md) | The receiver, the UI, `sdf init`, and the git hooks |
| [docs/summary.md](docs/summary.md) | How a session is summarized, and how cost is computed |
| [docs/outcomes.md](docs/outcomes.md) | Linking a session to a commit or pull request, decision records, and the local budget |
| [docs/check.md](docs/check.md) | `sdf check` and its cases |
| [docs/ledger.md](docs/ledger.md) | The hash-chained ledger, its encoding, `sdf audit verify` and `sdf audit head`, and what it does not protect against |
| [docs/tasks.md](docs/tasks.md) | `sdf task declare` and `sdf task show`: a task's acceptance criteria, hashed before an agent runs |
| [docs/verify.md](docs/verify.md) | `sdf verify`: run a declared task's checks against a commit, with evidence and a verdict |
| [docs/action.md](docs/action.md) | The `sdf verify` GitHub Action, and its threat model |
| [docs/gaps.md](docs/gaps.md) | Known limits of the collector and the summary |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Tests and continuous integration |
