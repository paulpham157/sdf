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
- Grok CLI's external stream exports no traces. Startup metrics
  (`grok_code.startup.*`) in the 2026-11-23 capture had no `session.id` and
  stay unattributed. The working directory and the internal spans are
  covered below.
- GitHub Copilot CLI has no log signal; its lifecycle events
  (`github.copilot.session.shutdown` with `total_premium_requests`,
  exceptions, hooks) are span events, which sdf does not store. Its metrics
  carry no session id. It reports AI units and a billing multiplier, not
  USD, and sdf deliberately does not price its tokens, so both cost fields
  are unknown. The mapping and the `copilot_*.json` samples come from
  the docs and `copilot help monitoring` (1.0.114), not a live session.
- Retried delta exports can be double-counted. Cumulative series use the
  latest point of each process run and add the runs up (see
  [docs/summary.md](summary.md)). That is still wrong in these cases:
  - Two processes exporting the same series at once with the same start
    time are read as one run, so the total is whichever reported last
    instead of the sum. The resource does not separate them, because runs
    with a start time are keyed by start time alone.
  - Without a start time, two processes with the same resource interleave
    and their drops read as resets, which over-counts.
  - Without a start time, a new run whose first value is not lower than the
    last value of the run before is not seen as a reset, which
    under-counts (100, then a restart that first reports 150, is 150, not
    250).
  - A cumulative UpDownCounter without a start time that goes down reads as
    a reset.
- Codex `response.completed` and OpenCode `SessionStep.attempt` counts are
  per request and are summed (issues #204 and #200), as described in
  [docs/summary.md](summary.md).
  Pi per-turn input and output are summed, and that sum is not added to
  `gen_ai.client.token.usage`.
- The agent-reported cost is whatever the tool put on the wire (Claude USD,
  Codex micro-USD or `usage.estimated_usd`, Pi `pi.cost.usd`). It is not an
  invoice. OpenCode does not emit one. `computed_cost_usd` is the bundled
  or refreshed OpenRouter token price for the same de-duplicated counts.
  It is not an invoice either: conditional overrides are refused, a missing
  component price is unknown, and per-request or web-search fees are not
  added.
- Gemini CLI reports no cost, and `internal/pricing` has no rule for its
  token sources yet, so both cost fields are unknown for a Gemini CLI session.
  Its tool-call logs carry no call id, so a `tool_call` span and its log are
  not joined; tool calls come from the logs. The Gemini CLI mapping and the
  `gemini_*.json` samples come from the published docs and source, not from a
  live session.
- `gemini --resume` reuses the `session.id`, but the new process restarts its
  cumulative counters at 0, and cumulative series keep only their latest
  point, so a resumed session undercounts cumulative tokens (1000 then 500
  reads as 500) until [#191](https://github.com/paulpham157/sdf/pull/191)
  merges.
- Prompt and tool content are not requested by the setup in
  [docs/agents.md](agents.md). If you turn those flags on, the text is stored in this local
  SQLite file. The exception is tool arguments: Qwen Code sends the full
  arguments of every tool call (`function_args`: shell commands, the content
  of `write_file` and `edit`) and the tool's error text on
  `qwen-code.tool_call` whatever `logPrompts` says, and Gemini CLI sends
  `function_args` when its `logPrompts` is on. sdf drops `function_args`
  from every record, and `error` / `error.message` from Qwen and Gemini
  tool-call records and Qwen tool spans (whose status message becomes
  "tool error"), before anything is stored. It keeps the tool name, call
  id, duration, success, and error type, the dropped sizes
  (`function_args_bytes`, `error_bytes`), and the dropped keys in
  `sdf.redacted`.
- Qwen Code reports no cost, and `internal/pricing` has no rule for its
  token sources, so both cost fields are unknown. Its token totals include
  subagents and the background memory extractor. Its metrics carry no
  session id by default. Span events and the log-to-span bridge (used with
  HTTP when only a traces endpoint is set) are not modelled. Tokens come from
  per-response logs, which a resumed session (`qwen --resume`) does not
  reset. The `qwen-code.token.usage` fallback is a per-process cumulative
  counter: its points are placed by their own time, each process run is
  added up (the same rule as
  [#191](https://github.com/paulpham157/sdf/pull/191), done in the Qwen code
  so it does not wait for #191), and when the process started more than
  10 s before the session (an earlier session in that process, after
  `/clear`) the run's first point in the session is subtracted. Tokens used
  before that first export are lost, and a `/clear` within 10 s of launch
  is missed. With `telemetry.metrics.includeSessionId` the points are the
  session's own and nothing is subtracted. The mapping and
  the `qwen_*.json` samples come from the v0.28.4 docs and source, not a
  live session.
- The git hook sends the commit SHA, checkout path, and branch to the local
  receiver. It does not send the diff or the message. The `SDF-Session`
  trailer stays in the git object. A GitHub squash merge keeps that trailer
  only when the squash commit message includes it. Husky, lefthook, and
  pre-commit are not edited; `core.hooksPath` is left as it was.
- No live capture is committed in this tree; the tests use the hand-written
  sample payloads (`tests/samples/grok_traces.json` follows the Grok CLI
  1.0.92 span shapes but is not a capture). OpenCode and Pi behavior above is from the 2026-11-10
  checks of the named package versions, not from a fixture that was copied
  out of a live SQLite file.
- Grok CLI's telemetry has no working directory. Its resource is a fixed
  set and `OTEL_RESOURCE_ATTRIBUTES` is ignored, so there is no env var to
  add one. The session hook from `sdf init grok --write` posts the hook's
  `workspaceRoot` instead, and the heuristic uses it. Without that hook a
  Grok session links to a commit only with `--repo PATH` and one commit in
  the time window (or a git hook or trailer). The hook path was checked
  against a live `grok` 1.0.92 on 2026-11-23. Grok CLI spans come only
  from its internal, undocumented pipeline (`GROK_INTERNAL_OTLP_TRACES_ENDPOINT`
  with `GROK_INTERNAL_OTLP_HEADERS` replacing the xAI token). The span
  mapping follows grok 1.0.92 and can break on a newer release. Spans need a
  signed-in Grok with telemetry not disabled, still send `x-userid` and
  `x-teamid` headers, and put `user.id` / `team.id` on the resource, which
  sdf stores. Sources are in [docs/agents.md](agents.md#grok-cli).
