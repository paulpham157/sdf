# Gaps

- Content encodings other than `identity` and `gzip` are rejected with
  HTTP 415. Gzip is inflated only up to 8,000,000 decompressed bytes.
  Chunked HTTP and `Content-Length` are both accepted, and gRPC is a
  separate listener on port 4317. gRPC gzip uses the standard compressor.
- Codex's published config reference does not catalog events or metrics. The
  Codex lists in [docs/agents.md](agents.md) can drift from a newer CLI. The Claude list follows
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
  described in [docs/summary.md](summary.md). Pi per-turn input and output are summed, and that sum is
  not added to `gen_ai.client.token.usage`.
- The agent-reported cost is whatever the tool put on the wire (Claude USD,
  Codex micro-USD or `usage.estimated_usd`, Pi `pi.cost.usd`). It is not an
  invoice. OpenCode does not emit one. `computed_cost_usd` is the bundled
  or refreshed OpenRouter token price for the same de-duplicated counts.
  It is not an invoice either: conditional overrides are refused, a missing
  component price is unknown, and per-request or web-search fees are not
  added.
- Prompt and tool content are not requested by the setup in
  [docs/agents.md](agents.md). If you turn those flags on, the text is stored in this local
  SQLite file.
- The git hook sends the commit SHA, checkout path, and branch to the local
  receiver. It does not send the diff or the message. The `SDF-Session`
  trailer stays in the git object. A GitHub squash merge keeps that trailer
  only when the squash commit message includes it. Husky, lefthook, and
  pre-commit are not edited; `core.hooksPath` is left as it was.
- No live capture is committed in this tree. The tests use the sample
  payloads only. OpenCode and Pi behavior above is from the 2026-11-10
  checks of the named package versions, not from a fixture that was copied
  out of a live SQLite file.
