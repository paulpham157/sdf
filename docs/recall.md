# Decision and link recall

SDF does not infer a decision from telemetry. A decision exists only when
something calls `sdf decision add`, and a commit is linked only when
something calls `sdf decision link` (or a later recorder writes the same
rows). This page is how that behavior is measured.

Two measurements share the names and stay separate:

- **Labeled recall** runs the harness in `internal/decision/recall` on
  synthetic fixtures. It needs ground truth.
- **Link recall by proxy** is `sdf decision recall`. It needs no labels.
  A commit counts as a miss when its session has a decision and the commit
  itself has no `implements` edge. An `implements` edge from the session
  only does not count that commit as linked. The ratio is
  `commits_linked / commits_in_decision_sessions`, the same totals
  `sdf decision audit` already computes. The overview chart's recall is a
  different share: every recorded commit that has an `implements` edge,
  over all recorded commits.

## Labeled metrics

Each fixture is ingested on its own, then compared with its labels.

| Metric | Numerator | Denominator | When the denominator is 0 |
| --- | --- | --- | --- |
| Decision recall | Labeled decisions that match a stored decision | Labeled decisions | Undefined |
| Link recall | Labeled `implements` commit links that were stored | Labeled commit links | Undefined |
| Decision precision | Those same hits | Stored decisions | Undefined |
| Link precision | Those same hits | Stored `implements` edges from a commit | Undefined |

A labeled decision matches a stored decision when the ids are equal, or,
if no id matched, when the statements are equal after trimming, case
folding, and collapsing whitespace. Each stored decision matches at most
one label. A link matches when an `implements` edge runs from that commit
to the stored decision that matched the label. Session edges are not
links in this metric. The sums are taken over fixtures, and the ratios
are computed from the sums.

The 95% Wilson score interval is reported for recall when the label count
is non-zero. Zero successes pin the low end at 0. A full set of successes
pins the high end at 1.

## Fixture format

Fixtures live in `internal/decision/recall/fixtures/*.json`. They are
hand-written, marked `sdf.fixture=recall`, and contain no secrets. Each
file is one scenario:

- `name`: lowercase words separated by hyphens.
- `notes`: one short description of what the scenario holds.
- `logs`: OTLP/JSON log export bodies. The harness decodes them with
  `otlp.Decode` and stores them with `store.Insert`.
- `labels.decisions`: `id`, `statement`, `status` (`hypothesis` or
  `decision`), and optional `decided_by` (a GitHub login, or the legacy
  values `user` or `agent`). The
  statement must appear in a log body.
- `labels.links`: `commit` (lowercase 40-character SHA) and `decision`
  (a labeled id). The SHA must be an `sdf.git.commit` event in `logs`.
  The edge means that commit implements that decision. An unlabeled
  commit is a commit that must stay unlinked.

Turns use a log named `recall.turn` with `session.id`, `recall.role`, and
the utterance in the body. Commits use the `sdf.git.commit` shape the git
hook posts, so `sdf decision audit` can see them.

## Scenarios

| Fixture | Labels |
| --- | --- |
| `approve` | User approves WAL mode. One decision, no commit. |
| `reject` | User rejects PostgreSQL and stays on SQLite. One decision, no commit. |
| `decide-commit` | One decision, then one commit that implements it. |
| `unrelated-commit` | One decision, then a typo-fix commit that does not implement it. |
| `plain-chat` | A question and an answer. No decision and no commit. |
| `long-session` | The decision is the first turn. Eight turns and twelve hours later, one commit implements it. |
| `many-to-many` | Two decisions and three commits. The middle commit implements both. |

## Adding a scenario

Add one JSON file in that directory. Keep it small. Use a new session id
and a new SHA. The loader rejects a statement that is not in a log body, a
link whose commit was not ingested, a duplicate id, and a duplicate
statement inside the file. `go test ./internal/decision/recall` runs every
file. The CI gate compares the skill-only run with the snapshot below.
A new label changes the denominators, so update the snapshot and this
page in the same change.

The harness calls a `Recorder` after ingest. `SkillOnly` writes nothing.
Pass another `Recorder` to `Measure` to score a writer. A recorder that is
a measurement must not read `Fixture.Labels`. This package does not call
`decision.AutoLinkGitCommits`. That writer is on main
([#323](https://github.com/paulpham157/sdf/pull/323), issue
[#281](https://github.com/paulpham157/sdf/issues/281)): a recorder can call
it and the same fixtures will score that path. CI still gates on
`SkillOnly`.

## Baseline

Measured on the skill-only pipeline, which is what CI runs. Nothing is
recorded, so both recalls are 0 and both precisions are undefined.

| | Labeled | Recorded | Hits | Recall | Precision | Wilson 95% (recall) |
| --- | --- | --- | --- | --- | --- | --- |
| Decisions | 7 | 0 | 0 | 0 | undefined | [0, 0.354330] |
| Commit links | 6 | 0 | 0 | 0 | undefined | [0, 0.390334] |

Per scenario the hits are 0. `approve`, `reject`, `decide-commit`,
`unrelated-commit`, and `long-session` each have one labeled decision
(Wilson high 0.793451). `many-to-many` has two decisions (Wilson high
0.657620) and four links (Wilson high 0.489891). `plain-chat` has none,
so its recall is undefined. Labeled links are one each on `decide-commit`
and `long-session` (Wilson high 0.793451).

Issue #284's target is link recall at least 95% and a precision drop of at
most 5 points against a stronger pipeline. These floors are the measured
baseline. Raise them when a recorder is plugged in and the new numbers
have been measured. The test fails if recall drops below the snapshot, if
a precision that was undefined becomes defined, or if the label counts
change.

## `sdf decision recall`

```bash
sdf decision recall [--db PATH] [--json] [--since DURATION|RFC3339] [--until RFC3339]
```

The store is opened read-only and is not created. A missing file in an
existing directory is an empty store and exits 0. JSON uses schema
`sdf.decision.recall/v1`. `link_recall_by_proxy` and `wilson95` are null
when no commit in the window sits in a session that has a decision.
`missed_commits` lists those misses. A session-only `implements` edge
does not count the commit as linked; only `implements` from the commit
itself does. `--since 24h` is the last 24 hours before the command runs.
Exit 0 means the report was written, including when there are misses
(unlike `sdf decision audit`, which exits 1 on findings).

## Live agents

`cmd/sdf-recall` drives the real agent CLIs through the same fixtures,
k times each, and scores the decisions and commit links those CLIs
actually write with `sdf`. It is opt-in. CI does not run it, and
`go test ./...` does not call a model. The CI gate stays the skill-only
baseline above.

```bash
export SDF_RECALL_API_KEY=...   # or TOKENROUTER_API_KEY
go run ./cmd/sdf-recall
go run ./cmd/sdf-recall --agents opencode,claude-code --scenarios decide-commit --k 1
go run ./cmd/sdf-recall --parallel 4
go run ./cmd/sdf-recall --json
go run ./cmd/sdf-recall --merge a.json,b.json
go run ./cmd/sdf-recall --no-install
```

With no key the command prints a skip line and exits 0. Nothing is sent
to a model. A written report also exits 0, including when recall is low.
`--agents` selects ids from `sdf init` (`opencode`, `pi`, `qwen`, `grok`,
`copilot`, `codex`, `claude-code`, `gemini`). `--scenarios` limits the
fixtures (the default is all seven). `--k` is 1 to 100 and defaults to 5.
`--parallel` is how many scenario runs proceed at once (1 to 32, default
1). `--merge` pools JSON reports from separate invocations into one
recall, one precision, and one Wilson interval, and does not start a CLI.
`--json` and `--markdown` choose the encoding; the default is both. JSON
uses schema `sdf.decision.recall.live/v1`.

Each run is a temp git repo. The fixture's OTLP is loaded into
`sdf-telemetry.sqlite` there, and a temp `sdf serve` receives whatever
the CLI exports. The prompt is the fixture's log bodies and commit SHAs.
It points at `skills/sdf-decisions/SKILL.md`. Labels and notes are not in
the prompt. The agent records with the `sdf` binary built from this
module, unless `SDF_BIN` is set.

Recall and precision are pooled over scored runs. A process that exits
non-zero, or that hits the run timeout, is a failed run. It is listed
separately and is not a miss, so it is not in the recall denominator.
A run that exits 0 and records nothing is a miss. The 95% Wilson interval
is the interval of that pooled recall.

The model id defaults to `deepseek/deepseek-v4.1-flash-1017-free`
(`SDF_RECALL_MODEL`). A full pass is 6 CLIs times 7 scenarios times k=5,
210 runs, and each run makes several model calls. That default id is a
free TokenRouter model. The free tier throttles; HTTP 429 is retried.
Another model can cost money. The provider key is read from the
environment and stays in the runner. Each CLI process gets a per-run
local token instead, including Pi's `apiKey` field. `SDF_RECALL_API_KEY`
is not set on the CLI, so a shell inside the agent cannot print the
provider key. The proxy checks that local token and then adds the
provider key on the upstream request. The provider key is not written
into agent config, logs, the report, or git. Stderr and upstream error
bodies are redacted: the key, its base64 and URL-encoded forms, `Bearer`
tokens, and `sk-` tokens. Stderr from each run is written under `--out`
(default `sdf-recall-out`, or `SDF_RECALL_OUT`). The error line in the
report is the tail of that stderr, up to 4 KB, after Node and npm
warnings are dropped. A run that reaches its timeout is reported as a
timeout. A second interrupt exits immediately.

| Agent | Protocol | Base |
| --- | --- | --- |
| [OpenCode](agents.md#opencode-v2), [Pi](agents.md#pi), [Qwen Code](agents.md#qwen-code), [Grok CLI](agents.md#grok-cli), [Copilot CLI](agents.md#github-copilot-cli) | OpenAI Chat Completions, `POST /v1/chat/completions` | `SDF_RECALL_OPENAI_BASE_URL`, else `SDF_RECALL_BASE_URL`, else `https://api.tokenrouter.com/v1` |
| [Codex](agents.md#codex-cli) | OpenAI Responses, `POST /v1/responses` | unsupported with this provider |
| [Claude Code](agents.md#claude-code) | Anthropic Messages, `POST /v1/messages` | `SDF_RECALL_ANTHROPIC_BASE_URL`, else `https://api.tokenrouter.com` (Claude appends `/v1/messages`) |
| [Gemini CLI](agents.md#gemini-cli) | native `generateContent` | unsupported with this provider |

Chat Completions and Messages on TokenRouter were checked live with
`deepseek/deepseek-v4.1-flash-1017-free` and returned HTTP 200. A plain
`POST /v1/responses` also returned HTTP 200. Codex still
sends `tool_choice` and `reasoning` on that path, and TokenRouter answers
HTTP 400. Leaving `model_reasoning_effort` unset does not strip those
fields, and Codex has no provider flag that omits `tool_choice`. The
runner reports Codex unsupported for that reason and does not rewrite the
body. The [TokenRouter Codex guide](https://www.tokenrouter.com/docs/codex-chatgpt-setup/)
describes the provider block; this command does not apply it.
Claude Code setup is the [TokenRouter Claude Code guide](https://www.tokenrouter.com/docs/claude-code-setup/).
The Anthropic base does not fall back to the OpenAI base, because that
URL already ends in `/v1`. Gemini's native `generateContent` paths on
this provider do not succeed (the v1beta path returns HTTP 500 and the v1
path returns HTTP 404). The runner reports Gemini CLI as unsupported
with that reason. It does not point Gemini at Chat Completions.

Copilot selects the model as `tokenrouter/recall`. The proxy rewrites that
alias to `SDF_RECALL_MODEL` on the way upstream and answers `GET /v1/models`
with that id, so Copilot's model lookup is not a 404.

Qwen Code's model comes from `OPENAI_BASE_URL`, `OPENAI_MODEL`, and
`OPENAI_API_KEY`. That variable holds the local token, not the provider
key. Telemetry uses `QWEN_TELEMETRY_*` with protocol `http`
(OTLP/JSON). The generic `OTEL_EXPORTER_OTLP_ENDPOINT` is not set, because
that variable overrides the Qwen endpoint and the stock exporter treats it
as gRPC. `QWEN_HOME` is the temp config directory. System settings are
pointed at empty files so a machine-wide `settings.json` cannot replace
this, and the temp repo is trusted so folder trust does not block tools.

The CLIs talk to a proxy on `127.0.0.1`. Each run has its own proxy, so
`--parallel` does not share the turn or token cap. The proxy checks the
per-run local token, then adds the provider key, retries HTTP 429 and 5xx
with backoff (honoring `Retry-After` up to 30 seconds), and streams an
upstream `text/event-stream` response without
buffering it or replacing its `Content-Type`. A run stops after the turn
cap (default 30) or the token cap (default 1,000,000). One request from
these CLIs is tens of kilobytes, so a 20,000 token cap ended the run after
a few calls. The run timeout defaults to 5 minutes. When the cap is hit on
a streaming request, the proxy sends a terminal event for that protocol
(Chat Completions `finish_reason`, Anthropic `message_stop`, Responses
`response.completed`) instead of a JSON document the client treats as a
broken stream. A capped run that still exits 0 is scored and marked
capped. Other knobs are `SDF_RECALL_MAX_TURNS`, `SDF_RECALL_MAX_TOKENS`,
`SDF_RECALL_RETRIES`, `SDF_RECALL_HTTP_TIMEOUT`, and
`SDF_RECALL_RUN_TIMEOUT`. An `http://` base whose host is not loopback
is rejected. `https://` is unchanged.

`--install` installs the packages this repo already names for OpenCode
(`@opencode/cli@2.4.1`), Pi (`@earendil-works/pi-coding-agent@1.5.1` and
`pi-otel@0.3.0`), Qwen Code (`@qwen-code/qwen-code@0.28.4`), and Copilot
(`@github/copilot@1.0.114`) when the binary is missing. It is off unless
you pass `--install`. npm installs use `--ignore-scripts` and a prefix
this run creates, not the user npm prefix and not the real home
directory. Claude Code and Grok are located on `PATH` and are not
installed. `--no-install` or `SDF_RECALL_NO_INSTALL=1` keeps installation
off. An agent that is supported but has no binary is `unavailable`.
Codex and Gemini are `unsupported` and are not looked up.

## Out of scope

Comparing the shipped skill with a new recorder, and labeling real
sessions with an LLM judge, are still follow-ups. They are not this
command. Gemini stays out until this provider has a working
`generateContent` endpoint, or Gemini CLI can be pointed at an
OpenAI-compatible base this runner can target. Codex stays out until
this provider accepts Responses requests that include `tool_choice` and
`reasoning`. The CI gate is the fixture harness only.
