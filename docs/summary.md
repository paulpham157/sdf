# Summary

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

## Precedence

So the same numbers are not added twice:

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
  times the OpenRouter per-token price. See below.

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
