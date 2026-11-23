# SDF

SDF collects traces of coding-agent work from OpenTelemetry the agents export,
and summarizes a session from that telemetry.

It does not launch, route, or sandbox agents. Codex CLI, Claude Code,
OpenCode v2, and Grok CLI export their own telemetry. Pi does not; its
section uses the community `pi-otel` extension. SDF listens.

The tools do not emit the same signals. A field the summary cannot find is
reported as not present. SDF does not estimate tokens or duration. It does
compute a cost from the token counts it already derived and a price table
copied from OpenRouter. That figure is separate from the cost the agent
reported. A model with no published price is `unknown`, not zero.

| Tool | Traces | Logs | Metrics | Limitation |
| --- | --- | --- | --- | --- |
| Claude Code | Beta spans when `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1` and `OTEL_TRACES_EXPORTER=otlp` | Event catalog when `OTEL_LOGS_EXPORTER=otlp` | Counters, including tokens and cost, with `session.id` by default, when `OTEL_METRICS_EXPORTER=otlp` | Nothing is exported until `CLAUDE_CODE_ENABLE_TELEMETRY=1`. Traces also need both trace flags. HTTP is port 4318. gRPC is port 4317. |
| Codex CLI | Process spans when `otel.trace_exporter` is an OTLP/HTTP URL | Session record (`codex.sse_event`, `codex.tool_result`, and the other `codex.*` events) | Large catalog. Turn metrics carry `conversation.id` only when every tag is valid | Does not read `OTEL_*`. `metrics_exporter` defaults to `statsig`. A model id containing `:` drops the turn metrics. |
| OpenCode v2 (`@opencode/cli`) | Built in. Chunked OTLP/HTTP JSON when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. Runner spans set `session.id` | Process logs only (`cli starting`, watcher, shutdown). No `session.id` | Not exported | No per-session log catalog and no metrics. npm `opencode-ai` is v1, not this CLI. |
| Pi (`@earendil-works/pi-coding-agent`) plus `pi-otel` | Extension spans (`invoke_agent`, `pi.turn`, `chat`, `execute_tool`) with `session.id` | `pi.session.start` and `pi.session.end` only, and only with `PI_OTEL_LOGS=1`. The id is `pi.session.id` | Counters and one duration histogram, only with `PI_OTEL_METRICS=1`. No session id | Not built in. Metrics and logs default off. Default protocol is gRPC on port 4317, which this receiver accepts. |
| Grok CLI | Not exported. The external stream is metrics and logs only | `grok_code.*` events when `GROK_EXTERNAL_OTEL=1` and `OTEL_LOGS_EXPORTER=otlp`. Events carry `session.id` | Counters, including tokens and cost. `session.id` is on unless `OTEL_METRICS_INCLUDE_SESSION_ID=0` | Off until the master switch and an exporter are both set. HTTP protobuf uses port 4318. gRPC uses port 4317. |

What each tool exports, and how to point it at the receiver, is in
[docs/agents.md](docs/agents.md).

Repository history from before this collector is preserved at the git tag
`legacy-v1` (commit `38f45e80ed05fd72e9387c2327d7fd7286bcfac3`).

## Quickstart (5 minutes)

No agent, API key, or network connection is needed for these four steps.

**1. Install.** With Go 1.26 or newer:

```bash
go install github.com/paulpham157/sdf/cmd/sdf@latest   # or @v0.1.0
```

That puts `sdf` in `$(go env GOPATH)/bin` (usually `~/go/bin`); make sure that
directory is on your `PATH`. Or download a prebuilt binary for Linux, macOS, or
Windows on amd64 or arm64 from
[GitHub Releases](https://github.com/paulpham157/sdf/releases) (published from
v0.1.0 on), check it against `checksums.txt`, and unpack it:

```bash
VERSION=0.1.0 OS=linux ARCH=amd64   # OS: linux|darwin; ARCH: amd64|arm64
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
--version` and `sdf -v` are the same. A `go install ...@v0.1.0` build prints
`v0.1.0`; a local build prints the pseudo-version the go tool recorded.

**3. See it work.**

```bash
sdf demo
```

starts a receiver on free localhost ports, sends one synthetic Claude
Code-shaped session (labeled `sdf.fixture=demo`) over OTLP/HTTP and OTLP/gRPC,
and prints the session summary: duration, tokens, tool calls, the cost the
agent reported, and the cost computed from the bundled price table. It writes
to a temporary database that is removed afterwards; `--db PATH` keeps it and
`--json` prints the report as JSON. It does not touch `sdf-telemetry.sqlite`
and does not collide with a running `sdf serve`.

**4. Run the built-in checks.**

```bash
sdf check
```

runs the case set embedded in the binary against the summary and prints one
line per case; `not_checked` is reported and is not a failure. See
[docs/check.md](docs/check.md).

Then point a real agent at it: `sdf serve` in one terminal, and
`sdf init claude-code` (or `codex`, `opencode`, `pi`, `grok`) for the settings.

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
serves a read-only UI at `http://127.0.0.1:4320/`. There is no authentication
on either receiver port; keep the bind address on localhost.
`GET http://127.0.0.1:4318/health` (the OTLP/HTTP port, not the UI port)
returns `{"status":"ok"}`. `sdf init --check` probes it for you.

```bash
sdf version            # version, commit, build date, Go version
sdf demo               # synthetic session through the real receiver; no API key
sdf summary            # one block per session, then unattributed records
sdf summary --json
sdf summary --session ID --all-spans
sdf decision add --id ID --statement TEXT --status hypothesis
sdf decision link --relation implements --from decision:ID --to session:ID
sdf prices refresh     # the only command that calls the network for prices
sdf init claude-code   # print exporter settings; --write changes a file
sdf init grok          # Grok CLI external OTEL; --write updates ~/.grok/config.toml
sdf init --check       # is the receiver reachable on 4318 and 4317?
sdf init --git-hooks   # record the commit SHA the agent does not export
sdf check              # embedded local cases against the summary; no network
```

`--host`, `--port`, `--grpc-port`, `--db`, `--ui-addr`, and `--budget`
override the `sdf serve` defaults. `--no-ui` leaves the OTLP ports only.
`--prices`, `--repo`, `--github`, and `--budget` match `sdf summary` and
apply to the page. Every command prints its flags with `--help`.

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
