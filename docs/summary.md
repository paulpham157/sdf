# Summary

```bash
./sdf summary
./sdf summary --db PATH
./sdf summary --session sample-claude-1
./sdf summary --json
./sdf summary --all-spans
./sdf summary --prices bundled
./sdf summary --repo PATH
./sdf summary --github
./sdf summary --budget PATH
```

`--budget` reads one local ceiling and compares it with computed session cost.
The report then has a `budget` with status `within`, `over`, or `unknown`.
Without the flag the field is absent. A file that fails to load exits non-zero
and prints no report. The comparison itself is in [docs/outcomes.md](outcomes.md).

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
agent, model, or tool). For Grok CLI internal spans it keeps only
`agent.prompt`, `session.process_conversation_turn`, `turn.sampling`,
`http.create_response_stream`, `tools.execute`, `permission.wait`, and
`tool.execution` (see [docs/agents.md](agents.md#grok-cli)). For other tools,
any other span that carries a call id or tool name is kept too. Internal names such as `append_items`, `LLM.compile`, and
`Plugin.load` stay in the count and out of the tree. `--all-spans` prints
them.

Tool calls join `exec_command` or `claude_code.tool` to `codex.tool_result` /
`claude_code.tool_result` on `call_id` or `tool_use_id`. OpenCode tool calls
come from `Tool.execute` (`gen_ai.tool.name`, `gen_ai.tool.call.id`). Grok CLI tool calls come from `tool.execution` (`tool_name`, `tool_use_id`,
`success`) joined to `grok_code.tool_result` on `tool_use_id`; without spans
they come from the log alone. Pi tool
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

## Precedence

So the same numbers are not added twice:

- GitHub Copilot CLI sessions are read first and on their own: duration is
  the union of the intervals of top-level `invoke_agent` and
  `invoke_workflow` spans (`traces invoke_agent and invoke_workflow
  (top-level, union of intervals, github-copilot)`; parallel runs and nested
  subagent spans count once), and tokens are the
  sum of `gen_ai.usage.*` on `chat` spans (`traces chat github-copilot`),
  else the top-level `invoke_agent` totals, else the
  `gen_ai.client.inference.usage.*` counters. Copilot reports no USD cost.
- Duration: sum of `claude_code.interaction` spans, else `codex.turn.e2e_duration_ms`,
  else sum of `run_turn` spans, else sum of Pi `invoke_agent` spans, else sum
  of OpenCode `SessionRunner.drain` spans, else `claude_code.active_time.total`
  (active seconds, not wall clock), else Grok CLI `grok_code.session_end`
  `duration_secs`, else the sum of `grok_code.turn_completed` `duration_ms`,
  else the sum of Grok `agent.prompt` spans,
  else for Gemini CLI the min-to-max timestamp of its logs and spans (`logs
  and traces gemini_cli window`; its cumulative metrics start at process
  start and export every 10 seconds, so they are left out), else the
  min-to-max timestamp (`observed_window`).
- Tokens: `claude_code.token.usage` by `type`, else `codex.turn.token_usage`
  by `token_type`, else the sum of Claude `api_request` token attributes,
  else Codex `response.completed` with the rule below, else Gemini CLI
  `gemini_cli.token.usage` by `type` (`input`, `output`, `thought`, `cache`,
  `tool`; cumulative, so each series keeps its latest point), else the sum of
  Gemini CLI `gemini_cli.api_response` `input_token_count`,
  `output_token_count`, `cached_content_token_count`, `thoughts_token_count`,
  and `tool_token_count` (`total_token_count` is not added). Gemini CLI's
  `gen_ai.client.token.usage` and `gen_ai.client.inference.operation.details`
  repeat the same input and output and are not added. Else OpenCode
  `SessionStep.attempt` `gen_ai.usage.*` (one span per provider request; input,
  cache, output, and reasoning are summed across requests;
  `cache_creation.input_tokens` is read as
  `cache_write.input_tokens` and `reasoning_tokens` as
  `reasoning.output_tokens`), else the sum of Pi `chat` `gen_ai.usage.input_tokens` and
  `output_tokens`. That Pi sum is not added to `gen_ai.client.token.usage`.
  If the chat spans have no usage attributes, the Pi token metric is the
  fallback. Else Grok CLI `grok_code.token.usage` by `type`, else the sum of
  `grok_code.api_request` token attributes (including `cache_creation_tokens` when present). Live Grok CLI 1.0.92 capture, 2026-11-23.
- Cost the agent reported: `claude_code.cost.usage` (USD), else
  `codex.turn.cost_microusd` divided by 1,000,000, else `cost_usd` on
  `claude_code.api_request`, else `usage.estimated_usd` on `codex.turn_cost`,
  else the sum of `pi.cost.usd` on Pi `chat` spans. Claude's figure is
  labeled as the agent's estimate, not the provider price. OpenCode emits
  no cost. Gemini CLI emits no cost either. Grok CLI reports `grok_code.cost.usage` in USD (live 2026-11-23 capture; the published guide omitted it), else the sum of `grok_code.api_request` `cost_usd_micros` / 1,000,000. The text calls this `agent_reported_cost_usd`. `cost_usd` in
  the text and in `--json` is the same number.
- Cost SDF computed: those same token totals, after the de-dup rule above,
  times the OpenRouter per-token price. See below.
- Qwen Code sessions use their own rules, applied after the ones above:
  duration is the sum of `qwen-code.interaction` spans (else `logs qwen-code
  window`), tokens are the sum of `qwen-code.api_response` token counts
  (else `qwen-code.llm_request` usage, else `qwen-code.token.usage` by
  `type`), and tool calls come from `qwen-code.tool` spans joined to
  `qwen-code.tool_call` logs. See [docs/agents.md](agents.md#qwen-code).

Codex `response.completed` token fields are not all additive. `input_token_count`,
`cached_token_count`, and `cache_write_token_count` already include the
conversation prefix, which the next request sends again. The summary takes
the max of those. `output_token_count` and `reasoning_token_count` are new
tokens and are summed. When `turn.id` is present, only the last
`response.completed` in that turn is used, so a retry does not add the
attempt again. `tool_token_count` is ignored: at the cited Codex commit it is
recorded from `total_tokens`.

What one value is, per source, and how values are combined:

| Source | One value is | Rule |
| --- | --- | --- |
| Claude Code `claude_code.token.usage` | delta or cumulative metric point | delta points summed; a cumulative series keeps the latest point per process run, runs summed |
| Claude Code `claude_code.api_request` logs | one API request | summed |
| Codex `codex.turn.token_usage` | histogram point per turn | summed like other metrics |
| Codex `response.completed` logs | one response, last per `turn.id` | input, cached, cache_write max; output, reasoning summed |
| OpenCode `SessionStep.attempt` spans | one provider request (finish usage of one `llm.stream`) | all counts summed; aliases on one span are one value (max); one value per trace and span id |
| Pi `chat` spans | one model turn | input and output summed; one value per trace and span id |
| Pi `gen_ai.client.token.usage`, Grok CLI `grok_code.token.usage` | metric point | as Claude metrics |
| Grok CLI `grok_code.api_request` logs | one API request | summed |

Delta metric points are summed. Gauges keep the latest point for that
attribute set. A cumulative series (same name and attributes) keeps the
latest value of each process run and adds the runs up. So when an agent
resumes a session in a new process (`gemini --resume` keeps the
`session.id`, but its counters restart at 0), 1000 before the resume and 500
after it is 1500, not 500.

- A point with `StartTimeUnixNano` belongs to the run with that start time.
  The latest point of the run (by time, then by storage order) is its value;
  a lower value inside one start time is not a reset, because a cumulative
  UpDownCounter may go down (5, 3, 4 is 4). The resource is ignored here, so
  a resource that gains an attribute mid-run (`process.pid`) does not split
  it.
- A point without a start time joins the run with the latest start time at
  or before its timestamp among the runs reported by the same resource. A
  point from another resource never joins a run it did not report, so a
  resumed process without a start time is its own run (1000, then 500 from
  the new process, is 1500). If there is no such run, points are grouped by
  resource and ordered by time, and a value lower than the one before starts
  a new run.
- Points of one group with the same timestamp are one point: the one stored
  last wins (1000 then 900 at the same time is 900, not 1900).

The limits are in [docs/gaps.md](gaps.md). A Codex model id that contains `:` leaves the
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
`--url` fetches a different models URL with the same shape.
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
| Qwen Code `qwen-code.api_response`, `llm_request`, and `token.usage` | No pricing rule yet, so computed cost is `unknown` (or unknown earlier when the model is not in the catalog). From `ApiResponseEvent` and the OpenAI converter at v0.28.4: input includes `cached_content_token_count`; with OpenAI-compatible providers output includes `thoughts_token_count` (possibly estimated), with Gemini-native responses it does not. A rule would have to know the provider. |
| Codex `codex.turn.token_usage` and `response.completed` | `cached_input` is part of `input` (`TokenUsage.non_cached_input` in `codex-rs/protocol` at the commit cited above). `cache_write_input` is subtracted from input too. `reasoning_output` is subtracted from output. `total` is not priced. A negative remainder is unknown. |
| OpenCode `SessionStep.attempt` | The summary has already summed input and cache over requests (one span per request; issue #200). `gen_ai.usage.cache_read.input_tokens` is priced as a subset of input. `gen_ai.usage.cache_creation.input_tokens` (what OpenCode sends) and `cache_creation_input_tokens` are the pre-v0.1.0 names for `gen_ai.usage.cache_write.input_tokens`; the summary folds them into `cache_write.input_tokens`, and when a span carries more than one of them the largest is kept, never the sum. Cache write is priced as a subset of input. When old and new names for one bucket arrive together, the largest is priced whatever the key order; two spellings of one bucket with no renamed key among them (for example `cache_write.input_tokens` and `cache_write_input_tokens`) still leave the cost unknown. `gen_ai.usage.reasoning.output_tokens` (the summary folds `reasoning_tokens` into it) is priced as a subset of output. Per-modality keys `gen_ai.usage.{text,image,audio,video}.{input_tokens,output_tokens,cache_read.input_tokens,cache_write.input_tokens}` are breakdowns of the totals: they stay in the token map, are summed over requests like their total, are not priced again, and are named in the cost `method`. If the matching total is missing (for example only `text.input_tokens` and `text.output_tokens`), the cost is unknown, not $0. The v0.1.0 registry defines only text, image, and audio with `input_tokens`, `output_tokens`, and `cache_read.input_tokens`; `video.*` and `<modality>.cache_write.input_tokens` are not in it and are accepted for forward compatibility only. Any other modality or unrecognized key still leaves the computed cost unknown. |
| Gemini CLI `gemini_cli.token.usage` and `api_response` | No pricing rule yet, so computed cost is `unknown` with the reason `no pricing rule for token source`. From `ApiResponseEvent` in `packages/core/src/telemetry/types.ts`: `input` / `input_token_count` is Gemini's `promptTokenCount` and includes `cache` / `cached_content_token_count`; `output` excludes `thought`; `tool` is the tool-use prompt count. `google/gemini-2.5-pro` also has `pricing.overrides` in the bundled table. |
| Pi `chat` and `gen_ai.client.token.usage` | The summary's input and output only. Cache and reasoning attributes are left out of that total, so they are not priced. |
| GitHub Copilot CLI `chat` spans, `invoke_agent`, and `gen_ai.client.inference.usage.*` | Never priced per token, so computed cost is always `unknown`. Copilot bills premium requests (`github.copilot.cost` multiplier) and AI units (`nano_aiu`), not tokens, and a token price would mislead. `TestCopilotIsNeverPricedPerToken` fails if a pricing rule for these sources is added. For reference, per the Copilot docs `input_tokens` includes `cache_read` and `cache_write`, and `output_tokens` includes `reasoning`. |
| Grok CLI `grok_code.token.usage` and `api_request` | Live session (grok 1.0.92, 2026-11-23): `cache_read` is a subset of `input`, `cache_creation` (when present) is also subtracted from input, and `reasoning` is a subset of `output`. OpenRouter base rates matched `cost.usage` / `cost_usd_micros` only under that overlap. Reasoning uses `internal_reasoning` when the row has it, otherwise the completion price. Agent-reported cost is `grok_code.cost.usage` (USD) or `cost_usd_micros`. A catalog row with `pricing.overrides` (for example `x-ai/grok-4.7`) still leaves computed cost unknown. |

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
