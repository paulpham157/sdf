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
`pi.tool.is_error`). Success comes from the log when the span has none.
A tool span stored twice (same trace id and span id, for example after an
OTLP export retry) is one call, and a span without both ids is never merged.
A tool log (`claude_code.tool_result`, `codex.tool_result`,
`grok_code.tool_result`, `gemini_cli.tool_call`) stored twice is one call
only when it is an exact copy (same session, log time, body, and every
attribute); `gemini_cli.tool_call` has no call id, so two calls of the same
tool with the same result are both listed (issue #258). Qwen Code tool
calls follow the same rule: `qwen-code.tool` spans by trace and span id,
`qwen-code.tool_call` logs only as exact copies (issue #261). An
OpenCode tool span has no success flag, so the call is `unknown`, unless the
span status is `ERROR`. An `ERROR` span is marked in the text tree
(`status=ERROR`, plus the status message when one was exported, and
`error_type=` when the span's `error.type`, `error_type`, or
`exception.type` is not already in that message). By default the stored
message is sdf's own, built from the kept error class and code, such as
`tool error: FileNotFoundError` (see
[content storage](receiver.md#content-storage)). `--json` includes
`status_code`, `status_message`, and `error_type` on every span; a span
with no status is `UNSET`. A tool-call span whose status is `ERROR` and that has no
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
  `gen_ai.client.inference.usage.*` counters. A span stored twice (same
  trace id and span id, for example after an OTLP export retry) counts once
  (issue #248). Copilot reports no USD cost.
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
  `tool`; cumulative, so each series adds up the latest point of each process
  run), else the sum of
  Gemini CLI `gemini_cli.api_response` `input_token_count`,
  `output_token_count`, `cached_content_token_count`, `thoughts_token_count`,
  and `tool_token_count` (`total_token_count` is not added). A log stored
  twice counts once (issue #248). The event has no response id, and
  `prompt_id` is shared by every response of one prompt, so only an exact
  copy is merged: same session, log time, body, and every attribute
  (`event.timestamp`, `prompt_id`, `duration_ms`, counts, and the rest). A
  log with neither `event.timestamp` nor a log time is never merged. Gemini CLI's
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
  `qwen-code.tool_call` logs. A log stored twice counts once only when it
  is an exact copy (same session, log time, body, and every attribute),
  and a span stored twice (same trace and span id), interaction spans
  included, counts once (issue #261). See [docs/agents.md](agents.md#qwen-code).

Codex `response.completed` logs are the fallback when `codex.turn.token_usage`
is missing (for example an OpenRouter `:free` model id, whose `:` makes Codex
drop the turn metrics). Each event is the usage of one Responses API call,
and one turn makes one call per tool round trip, so `input_token_count`,
`cached_token_count`, `cache_write_token_count`, `output_token_count`, and
`reasoning_token_count` are all summed, the same way Codex sums
`codex.turn.token_usage`. Every call is billed its full input, prefix
included, and its own cache reads. `turn.id` does not collapse events: Codex
0.167.0 and 0.171.0 do not put it (or a response id) on these logs, and
every call in a turn is billed. An event stored twice (same
`conversation.id`, `event.timestamp`, and counts) counts once.
`tool_token_count` is ignored: at the cited Codex commit it is recorded from
`total_tokens`. Checked against a real Codex 0.171.0 session on OpenRouter
(issue #204): four calls billed 37400 input, 18560 cached, and 109 output
tokens, which matches both the summed logs and `codex.turn.token_usage`.

`codex.turn.token_usage` itself is not per request (issue #222). In Codex
0.171.0 (`codex-rs/core/src/state/turn_token_usage.rs`) it is a histogram
with one sample per turn and model, whose value is that turn's usage summed
over every Responses call of the turn. Its tags are `token_type` (`total`,
`input`, `cached_input`, `cache_write_input`, `output`, `reasoning_output`),
`tmp_mem_enabled`, and the session tags `auth_mode`, `session_source`,
`originator`, `service_name`, `model`, and `app.version`; there is no
`turn.id` and no `conversation.id`. With the default delta temporality one
exported point is every sample of one attribute set in that export
interval, so it can hold several turns, and SDF stores only the histogram
`sum` (not `count`, `min`, `max`, or buckets). Neither the calls nor the
turns can be split back out of a stored point.

Claude Code `claude_code.api_request` and Grok CLI `grok_code.api_request`
logs are one API request each and are summed. A log stored twice (for
example after an OTLP export retry) counts once, in the token totals, in the
per-request counts used for `min_prompt_tokens` override pricing, and in the
agent-reported `cost_usd` / `cost_usd_micros` sum (issue #214). Two records
are the same request only when all of these match: `session.id`,
`prompt.id`, `request_id`, `client_request_id`, `event.sequence`,
`event.timestamp`, the log record time, every token count, and `cost_usd` /
`cost_usd_micros`. A field the event does not carry counts as empty. Claude
Code documents `request_id` (the server request id), `client_request_id`,
`prompt.id`, `event.sequence`, and `event.timestamp` on `api_request`; the
Grok CLI guide documents `event.sequence` and `prompt.id` on every event,
and grok 1.0.92 sent no `event.timestamp`. `event.sequence` is a
per-process counter, so two real requests in one process do not share it.
A record with neither `event.timestamp` nor a log time is never merged.
This was not checked against real duplicated data. Codex `response.completed`
logs keep their own key above (`conversation.id`, `event.timestamp`, and
counts) for both the totals and the per-request prices; the `api_request`
key is not applied to them.

What one value is, per source, and how values are combined:

| Source | One value is | Rule |
| --- | --- | --- |
| Claude Code `claude_code.token.usage` | delta or cumulative metric point | delta points summed; a cumulative series keeps the latest point per process run, runs summed |
| Claude Code `claude_code.api_request` logs | one API request | summed; one value per `session.id`, `prompt.id`, `request_id`, `client_request_id`, `event.sequence`, `event.timestamp`, log time, counts, and cost |
| Codex `codex.turn.token_usage` | delta histogram `sum`: every turn of one model in one export interval, each turn summed over its Responses calls | summed like other metrics; a cumulative series as Claude metrics |
| Codex `response.completed` logs | one Responses API call | all counts summed; one value per `conversation.id`, `event.timestamp`, and counts |
| OpenCode `SessionStep.attempt` spans | one provider request (finish usage of one `llm.stream`) | all counts summed; aliases on one span are one value (max); one value per trace and span id |
| Pi `chat` spans | one model turn | input and output summed; one value per trace and span id |
| Pi `gen_ai.client.token.usage`, Grok CLI `grok_code.token.usage` | metric point | as Claude metrics |
| Grok CLI `grok_code.api_request` logs | one API request | summed; same one-value rule as Claude `api_request` logs |

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

## Deep span trees

The span tree nests at most 32 levels below a root (`MaxSpanDepth`, the
same limit the UI draws to). A span at depth 32 that has descendants keeps
all of them as leaf children, listed once each in depth-first span order,
and `--json` gives it `flattened_descendants` with their count. The key is
absent on every other span, so a tree within the limit is unchanged. The
text tree prints `(N deeper spans listed flat below; depth limit 32)` under
that span. No span is dropped: every `status=ERROR` span and every tool
call is still there. Without the limit each level added two JSON nesting
levels, so past about 4997 levels the JSON encoder stopped (issue #267) and
the indented output grew with depth squared. A cycle formed by two stored
spans sharing a span id is cut where a span would repeat on its own path.

The tree is built and printed without recursion. The UI writes each JSON
response into memory first. An encoding error is logged to stderr and
answered with `500` and `{"error": "..."}`, never `200` with an empty or
partial body.

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
different rows are unknown.

A row with `pricing.overrides` that only uses `min_prompt_tokens` (the shape
on `x-ai/grok-4.6`, `x-ai/grok-4.7`, Claude Sonnet 4.5, Gemini 2.5 Pro, and
other long-context rows in the bundled table) is priced at the base rates
when the prompt token count is below that threshold, and at the matching
override rates when a per-request prompt count is at or above it. Prompt
tokens are the request's input total: for overlap sources (Grok, Codex,
OpenCode) that is the input count, which already includes cache; for
disjoint sources (Claude Code) it is input plus cache read plus cache write.
When only a session aggregate exists and that prompt total is at or above
the lowest threshold, the cost stays `unknown`: a request could have crossed
the threshold, and the base rate is not applied to the total. Per-request
counts are used when the token source is already `grok_code.api_request` or
`claude_code.api_request` logs, or Codex `response.completed` logs: each
de-duplicated `response.completed` event (the same events the displayed
totals sum) is priced on its own, so a Codex session whose summed prompt
tokens reach 272000 is still priced when every call is below the
`min_prompt_tokens` 272000 override on `openai/gpt-5.4` and similar rows,
and a single call at or above it uses the override rates (issue #213). A
Codex session whose token source is `codex.turn.token_usage` has no
per-request counts in the metric (see above), so on its own it is priced
from that aggregate and stays `unknown` when the summed input reaches the
threshold, even if every call was below it; pricing it per turn would be
wrong too, because one turn sums several calls. When the same session also
has `response.completed` logs, and their de-duplicated counts add up to
exactly the metric totals for `input`, `cached_input`, `cache_write_input`,
`output`, and `reasoning_output` (a missing value counts as 0), each log is
priced as one request under the metric's keys (issue #222). The displayed
totals stay the metric's, so nothing is counted twice. If the logs do not
add up (a call's log is missing, or there are logs the metric does not
cover), the aggregate rule applies. An override entry with any other condition
key (for example `utc_start` / `utc_end`) leaves the cost unknown, including
when a `min_prompt_tokens` field is present but not a non-negative integer.

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
| Grok CLI `grok_code.token.usage` and `api_request` | Live session (grok 1.0.92, 2026-11-23): `cache_read` is a subset of `input`, `cache_creation` (when present) is also subtracted from input, and `reasoning` is a subset of `output`. OpenRouter base rates matched `cost.usage` / `cost_usd_micros` only under that overlap. Reasoning uses `internal_reasoning` when the row has it, otherwise the completion price. Agent-reported cost is `grok_code.cost.usage` (USD) or `cost_usd_micros`. `x-ai/grok-4.6` and `x-ai/grok-4.7` have `min_prompt_tokens` 200000 overrides; a live `grok-4.7` api_request of 14454/177/121/10112 input/output/reasoning/cache_read prices at `$0.014802` (base rates), and the session totals 64649/384/167/51072 price at `$0.054994` because that prompt total is below the threshold. A metric-only total at or above 200000 stays unknown unless per-request `api_request` counts are the token source. |

Cache-creation counts mix the five-minute and one-hour lifetimes. If the
catalog publishes both `input_cache_write` and `input_cache_write_1h` and
they differ, the cost is unknown. Per-request, image, audio, and web-search
prices are not added; the note names them when the row lists a non-zero fee.
Reasoning uses `internal_reasoning` when the row has it, and the completion
price when it does not, because those reasoning tokens are already inside
the output count.

A missing component price is not treated as zero, with one exception
(issue #243). On the 2026-11-10 snapshot, and in the live API on
2026-11-23, all 13 OpenRouter `:free` rows, such as
`recursive/flint-v1.0:free`, have prompt and completion `"0"` and no
`input_cache_read` price. The
[Models API reference](https://openrouter.ai/docs/api_reference/models)
("Sentinel prices") says models ending in `:free` set prompt and completion
to `0`. The
[Free Variant guide](https://openrouter.ai/docs/guides/routing/model-variants/free)
says they are served without cost. So for a row whose id ends in `:free`
and whose prompt and completion are both 0, cache-read tokens are priced at
0. The cost `note` says so. On that model a Claude Code session with
10752 cache-read tokens, a Codex session, and an OpenCode session all
compute `$0`, and so does a Pi session, whose totals are input and output
only. The agent's `$0.065` on that model is still shown as
`agent_reported_cost_usd`. The rule covers cache read only. A cache-write
count on a `:free` row with no `input_cache_write` price is still `unknown`.
Prompt and completion must both parse as zero. A `:free` row whose prompt or
completion is `-1` (OpenRouter's dynamic-router sentinel), empty, not a
decimal, or non-zero does not get the rule.

The rule needs the `:free` suffix, so zero-priced rows without it are not
covered. On the 2026-11-10 snapshot, `openrouter/free` (the Free Models
Router), `google/lyria-3-clip-preview`, and `google/lyria-3-pro-preview`
each have only prompt `"0"` and completion `"0"`. A session on one of them
with input and output only computes `$0`. A session that also reports
cache-read tokens stays `unknown` with
`catalog has no input_cache_read price for model <id>`. The router's actual
model and the preview rows' cache billing are not in the catalog. Router rows
with prompt and completion `-1` are never priced: the OpenRouter routers
`openrouter/auto`, `openrouter/auto-beta`, `openrouter/auto-code`,
`openrouter/bodybuilder`, `openrouter/fusion`, and `openrouter/pareto-code`,
and the non-OpenRouter routers `nvidia/switchyard` and `typesafe/jev-router`.

A paid row with no `input_cache_read` stays `unknown` when the session has
cache-read tokens. `mistralai/mistral-nemo` is one example. Its row lists
prompt `0.000000029` and completion `0.00000003`, which are DeepInfra's
rates, and DeepInfra does not cache. In
[its endpoint list](https://openrouter.ai/api/v1/models/mistralai/mistral-nemo/endpoints)
on 2026-11-23, each endpoint that caches has its own prompt and cache-read
rates. Io Net has prompt `0.00000001656` and cache read `0.0000000108`.
Mistral has prompt `0.00000015` and cache read `0.000000015`. OpenRouter
routes cached requests to such an endpoint
([prompt caching, sticky routing](https://openrouter.ai/docs/guides/best-practices/prompt-caching)).
The catalog holds only model-level rates, so it cannot say what those
cached tokens cost, and OpenRouter does not document billing cache reads at
the prompt rate. The real Codex capture in issue #204 (37400 input, 18560
cached, 109 output) therefore stays `unknown` with
`catalog has no input_cache_read price for model mistralai/mistral-nemo`. A
mistral-nemo session with no cached tokens is priced at the row's rates.
