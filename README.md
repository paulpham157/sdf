# SDF

SDF collects traces of coding-agent work from OpenTelemetry the agents export,
and summarizes a session from that telemetry.

It does not launch, route, or sandbox agents. Codex CLI, Claude Code, and
OpenCode v2 export their own telemetry. Pi does not; its section uses the
community `pi-otel` extension. SDF listens.

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

What each tool exports, and how to point it at the receiver, is in
[docs/agents.md](docs/agents.md).

Repository history from before this collector is preserved at the git tag
`legacy-v1` (commit `38f45e80ed05fd72e9387c2327d7fd7286bcfac3`).

## Install

Go 1.26 or newer. The SQLite driver is pure Go (`modernc.org/sqlite`), so the
build does not need cgo or a C compiler. A database written by the previous
Python receiver, including one from before the `trace_id` columns existed,
still opens.

```bash
make build
./sdf serve
```

`make build` is `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o sdf ./cmd/sdf`.

## Run

```bash
./sdf serve
```

That listens for OTLP/HTTP on `http://127.0.0.1:4318` and OTLP/gRPC on
`127.0.0.1:4317`, writes `sdf-telemetry.sqlite` in the current directory, and
serves a read-only UI at `http://127.0.0.1:4320/`. There is no authentication
on either receiver port; keep the bind address on localhost. `GET /health`
returns `{"status":"ok"}`.

```bash
./sdf summary          # one block per session, then unattributed records
./sdf summary --json
./sdf prices refresh   # the only command that calls the network for prices
./sdf init claude-code # print exporter settings; --write changes a file
./sdf init --git-hooks # record the commit SHA the agent does not export
./sdf check            # local cases against the summary; no network
```

`--host`, `--port`, `--grpc-port`, and `--db` override the defaults. `--no-ui`
leaves the OTLP ports only. `--prices`, `--repo`, and `--github` match
`sdf summary` and apply to the page.

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
| [docs/gaps.md](docs/gaps.md) | Known limits of the collector and the summary |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Tests and continuous integration |
