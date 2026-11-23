# Changelog

All notable changes to SDF will be documented in this file.

This file covers the first public release of the current product: a vendor-neutral OpenTelemetry (OTLP) collector for AI coding-agent telemetry, written in Go and stored in SQLite. It does not list the pre-rebuild Python/Herdr history. That tree is snapshotted at tag `legacy-v1` (commit `38f45e80ed05fd72e9387c2327d7fd7286bcfac3`, 2026-11-08 13:23 America/New_York).

## [0.1.0] — UNRELEASED

This will be the first tagged release. Requires Go 1.26 or newer to build from source. Install with `go install github.com/paulpham157/sdf/cmd/sdf@v0.1.0` (after the tag exists) or with the prebuilt archives on GitHub Releases once published.

### Breaking changes

- **BREAKING:** `sdf decision link` now enforces endpoint kinds for relations that used to accept any kind. `implements` must run from a `session` or `commit` to a `decision` (a `decision` source, or a `session`/`commit` target, is refused); `validates` and `contradicts` must point to a `decision` (a `session` or `commit` target is refused); `constrains` must start from a `decision` (a `session` or `commit` source is refused). Refused links exit 1 with `ErrEndpointKind`, for example `implements links a session or commit to a decision: from is decision:a`. Rows an older sdf stored are kept and listed, count for the duplicate check, and drive no read-time field ([#278](https://github.com/paulpham157/sdf/pull/278)).

### Features

- Replace the previous product with an OTLP receiver for Codex and Claude Code; store traces, metrics, and logs in SQLite and summarize sessions ([#157](https://github.com/paulpham157/sdf/pull/157)).
- Accept chunked OTLP and summarize OpenCode and Pi sessions ([#158](https://github.com/paulpham157/sdf/pull/158)).
- Port the collector to a single static Go binary (`CGO_ENABLED=0`) ([#159](https://github.com/paulpham157/sdf/pull/159)).
- Compute session cost, link git outcomes, and add `sdf init` and `sdf prices refresh` ([#161](https://github.com/paulpham157/sdf/pull/161)).
- Add a local read-only UI for session cost and outcomes ([#163](https://github.com/paulpham157/sdf/pull/163)).
- Link Pi, OpenCode, and Codex commits with `sdf init --git-hooks` ([#164](https://github.com/paulpham157/sdf/pull/164)).
- Record session open and close from agent hooks (`POST /v1/sessions`, `sdf.session.open` / `sdf.session.close`); `sdf init --write` installs the hook ([d066acd](https://github.com/paulpham157/sdf/commit/d066acd22d5e59d6bc61a6826159cdcc101304a4)).
- Record decisions separately from telemetry ([#171](https://github.com/paulpham157/sdf/pull/171)).
- Check the summary against a fixed set of local cases (`sdf check`) ([#172](https://github.com/paulpham157/sdf/pull/172)).
- Report a local budget against computed session cost ([#173](https://github.com/paulpham157/sdf/pull/173)).
- Add `--budget` to `sdf summary` and `sdf serve` ([#177](https://github.com/paulpham157/sdf/pull/177)).
- Add `sdf decision add` and `sdf decision link` ([#178](https://github.com/paulpham157/sdf/pull/178)).
- Render decisions and the budget on the session page ([#179](https://github.com/paulpham157/sdf/pull/179)).
- Map Grok CLI `grok_code.*` telemetry into session summaries ([#181](https://github.com/paulpham157/sdf/pull/181)).
- Add `sdf version` (also `--version` / `-v`) and `sdf init grok`; fix `sdf check` ([#183](https://github.com/paulpham157/sdf/pull/183)).
- Show the session span tree and ERROR status in the UI ([#184](https://github.com/paulpham157/sdf/pull/184)).
- Apply OpenRouter `min_prompt_tokens` price overrides ([#185](https://github.com/paulpham157/sdf/pull/185)).
- Map Gemini CLI `gemini_cli.*` telemetry into session summaries ([#186](https://github.com/paulpham157/sdf/pull/186)).
- Add GitHub Copilot CLI support ([#188](https://github.com/paulpham157/sdf/pull/188)).
- Add a hash-chained ledger and `sdf audit verify` / `sdf audit head` ([#189](https://github.com/paulpham157/sdf/pull/189)).
- Add `sdf task declare` and `sdf task show` ([#190](https://github.com/paulpham157/sdf/pull/190)).
- Add optional bearer-token auth to the local OTLP receiver ([#192](https://github.com/paulpham157/sdf/pull/192)).
- Add `sdf verify` to run a declared task against a commit ([#193](https://github.com/paulpham157/sdf/pull/193)).
- Add Qwen Code support (summary mapping and `sdf init qwen`) ([#194](https://github.com/paulpham157/sdf/pull/194)).
- Add a fork-safe GitHub Action for `sdf verify` on pull requests ([#196](https://github.com/paulpham157/sdf/pull/196)).
- Complete Grok CLI support: init, session id, session hooks, cwd via hooks, and internal trace mapping ([#203](https://github.com/paulpham157/sdf/pull/203)).
- Opt-in ledger for `sdf task declare` and `sdf verify` (`--ledger` / `$SDF_LEDGER`) ([#207](https://github.com/paulpham157/sdf/pull/207)).
- Record who decided, alternatives, and a reason on `sdf decision add` (`--decided-by user|agent`, `--alternatives`, `--reason`); ship the `sdf-decisions` skill ([#234](https://github.com/paulpham157/sdf/pull/234)).
- Add `sdf decision list` (and `--json`) to print decision records and edges ([#236](https://github.com/paulpham157/sdf/pull/236)).
- Show each decision's effective status (`effective_status` / `superseded_by`, computed at read time and never stored) in the `EFFECTIVE` column and as "superseded by" in the UI ([#240](https://github.com/paulpham157/sdf/pull/240)).
- Add `--expect-prefix` and `--expect-prefix-file` to `sdf audit verify` ([#220](https://github.com/paulpham157/sdf/pull/220)).
- In `sdf decision link`, `supersedes` links are decision-to-decision only, unknown refs are rejected, and exact duplicates are rejected ([#251](https://github.com/paulpham157/sdf/pull/251), fixes [#249](https://github.com/paulpham157/sdf/issues/249)). `sdf decision list` shows `decided_by`, alternatives, and reason.
- Add `sdf decision retract` (append-only; `--id`, optional `--reason` and `--retracted-by user|agent`) ([#256](https://github.com/paulpham157/sdf/pull/256)).
- Add `sdf redact --db` to scrub stored content; `sdf serve` warns on a database that may still hold unscrubbed text ([#225](https://github.com/paulpham157/sdf/pull/225)).
- Add decision relations `blocks`, `conflicts_with`, and `refines` to `sdf decision link`, with `blocked_by` and `depends_on` as input aliases of `blocks` (ends swapped). `blocks` and `refines` reject cycles; `conflicts_with` is symmetric and stored once in canonical order; `conflicts_with` and `refines` are refused when one side already supersedes the other, while `supersedes` after `conflicts_with` is allowed and resolves it. `sdf decision list`, `GET /api/decisions`, and the UI show the read-time fields `blocked_by`, `open_blockers`, `blocked`, `conflicts_with`, `conflicted`, `refined_by`, and `challenged` (omitted when empty); `effective_status` is unchanged. A blocker is open while any current head is a `hypothesis`, or when its `supersedes` chain only reaches a legacy cycle; a retracted blocker blocks nothing. `challenged` counts `validates` and `contradicts` only from sessions, commits, and active decisions ([#278](https://github.com/paulpham157/sdf/pull/278)).
- Add a Decisions tab to the UI and `GET /api/decisions/graph` (also `sdf decision graph` and `--json`): decision records grouped into chapters by branch, PR, session, or decision, with the chosen path, not-taken alternatives, endings, locked, conflict, and challenged states, needs-attention items, and an explored figure. The page stays read-only; what-ifs are copyable commands ([#279](https://github.com/paulpham157/sdf/pull/279)).

### Fixes

- Follow up the local UI and git hooks ([#165](https://github.com/paulpham157/sdf/pull/165)).
- Fix outcome links for squash trailers, closed PRs, and empty CI, and honor `--repo` ([#167](https://github.com/paulpham157/sdf/pull/167)).
- Match a known commit SHA without regard to case ([#174](https://github.com/paulpham157/sdf/pull/174)).
- Report an unplaceable day window as unknown cost, not as no sessions ([#175](https://github.com/paulpham157/sdf/pull/175)).
- Render a terminating budget total at the width it needs ([#176](https://github.com/paulpham157/sdf/pull/176)).
- Attribute OpenCode camelCase `sessionID` logs to the session ([#182](https://github.com/paulpham157/sdf/pull/182)).
- Sum cumulative counters across process restarts ([#191](https://github.com/paulpham157/sdf/pull/191)).
- Keep user hooks when `sdf init` writes Claude Code and Codex session hooks ([#197](https://github.com/paulpham157/sdf/pull/197)).
- Read OpenCode `cache_creation` and `reasoning.output_tokens`; ignore modality keys in pricing ([#198](https://github.com/paulpham157/sdf/pull/198)).
- Sum OpenCode input and cache tokens per request ([#201](https://github.com/paulpham157/sdf/pull/201), fixes [#200](https://github.com/paulpham157/sdf/issues/200)).
- Sum Codex `response.completed` tokens per request ([#205](https://github.com/paulpham157/sdf/pull/205), fixes [#204](https://github.com/paulpham157/sdf/issues/204)).
- Send the receiver token from `SDF_OTLP_TOKEN` or `SDF_OTLP_TOKEN_FILE` in hooks ([#206](https://github.com/paulpham157/sdf/pull/206)).
- Price Codex `response.completed` sessions per request ([#217](https://github.com/paulpham157/sdf/pull/217), fixes [#213](https://github.com/paulpham157/sdf/issues/213)).
- Count a duplicated Claude/Grok `api_request` log once ([#218](https://github.com/paulpham157/sdf/pull/218), fixes [#214](https://github.com/paulpham157/sdf/issues/214)).
- Price mixed Codex sessions per call when logs match `codex.turn.token_usage` ([#223](https://github.com/paulpham157/sdf/pull/223), fixes [#222](https://github.com/paulpham157/sdf/issues/222)).
- Run Grok and Qwen `sdf init` writes through the shared precheck: refuse unwritable targets and, as root, dangling or escaping symlinks outside the config dir and `$HOME` ([#210](https://github.com/paulpham157/sdf/pull/210)).
- Pin Grok `otel_log_*` gates off in `sdf init grok`, detect existing telemetry in any TOML form, and warn when `OTEL_LOG_TOOL_DETAILS` is set in the environment ([#219](https://github.com/paulpham157/sdf/pull/219)).
- Detect an existing `otel` table in any TOML form in `sdf init codex`, and refuse invalid TOML ([#221](https://github.com/paulpham157/sdf/pull/221)).
- Merge Codex session hooks even when `[otel]` already exists, match only sdf's own hook, and refuse a `null` hook event ([#211](https://github.com/paulpham157/sdf/pull/211)).
- Keep the Qwen exit token flush, report unknown instead of 0 when there is no baseline, and always redact GenAI content for every agent ([#212](https://github.com/paulpham157/sdf/pull/212)).
- Set SQLite `busy_timeout` and WAL on open so concurrent ingest and summary do not fail with `SQLITE_BUSY` ([#238](https://github.com/paulpham157/sdf/pull/238)).
- Validate `sdf init --host` and ports before `--check` probes them, and tighten host parsing ([#215](https://github.com/paulpham157/sdf/pull/215)).
- Never hint the unspecified address for `sdf init --host` ([#226](https://github.com/paulpham157/sdf/pull/226)).
- Price `cache_read` at 0 on bundled OpenRouter `:free` rows whose prompt and completion are 0; a paid row such as `mistralai/mistral-nemo` with no `input_cache_read` stays unknown ([#246](https://github.com/paulpham157/sdf/pull/246), fixes [#243](https://github.com/paulpham157/sdf/issues/243)). Docs and tests for that rule were tightened in [#254](https://github.com/paulpham157/sdf/pull/254).
- Count a re-sent Copilot span or Gemini `api_response` log once ([#250](https://github.com/paulpham157/sdf/pull/250), fixes [#248](https://github.com/paulpham157/sdf/issues/248)).
- `sdf summary` works on a database in a read-only directory. It no longer creates a file for a missing `--db` path, and it does not convert a rollback-journal database to WAL ([#253](https://github.com/paulpham157/sdf/pull/253)).
- Fix a stack overflow when the UI renders a deep span tree ([#232](https://github.com/paulpham157/sdf/pull/232)).
- Keep the first copy of a re-sent Copilot span and use unambiguous dedup keys ([#257](https://github.com/paulpham157/sdf/pull/257)).
- List a tool call from a re-sent export once ([#259](https://github.com/paulpham157/sdf/pull/259), fixes [#258](https://github.com/paulpham157/sdf/issues/258)).
- Count a re-sent Qwen Code log or span once ([#262](https://github.com/paulpham157/sdf/pull/262), fixes [#261](https://github.com/paulpham157/sdf/issues/261)).
- `sdf decision link` errors for the `blocked_by` and `depends_on` aliases name the alias and the `--from`/`--to` flag as typed, not the swapped canonical ends; the UI marks a blocker "(settled)" only when no blocker is open, and its "conflicts with" badge lists only active sides ([#278](https://github.com/paulpham157/sdf/pull/278)).

### Security

- Prompt and response content is now dropped before storing unless `sdf serve --store-content` is set. Tool and error text is dropped too ([#224](https://github.com/paulpham157/sdf/pull/224)).
- Validate `sdf init --host` / `--port` and quote shell snippets ([#195](https://github.com/paulpham157/sdf/pull/195)).
- Reject forged OTLP posts and rebound UI hosts ([#199](https://github.com/paulpham157/sdf/pull/199)).

### Docs

- Document live Codex sandbox requirements for git-hook linking ([#166](https://github.com/paulpham157/sdf/pull/166)).
- Split the README into a short overview and `docs/` ([449212e](https://github.com/paulpham157/sdf/commit/449212edd0bb7ba18347fb23fa96bb0abdc429ca)).
- Add 5-minute install docs for `go install` and (once published) prebuilt archives ([#187](https://github.com/paulpham157/sdf/pull/187); quickstart later aligned in [#233](https://github.com/paulpham157/sdf/pull/233)).
- Drop the stale “planned” text for `sdf verify`; document `sdf task declare --timeout` / `--no-secret-scan` and `sdf task show --json`; mention GitHub Copilot CLI in the README opening sentence ([#237](https://github.com/paulpham157/sdf/pull/237)). Say what `--no-secret-scan` skips, and that `sdf task show --json` is indented ([#241](https://github.com/paulpham157/sdf/pull/241)).
- Shorten the README overview and add a diagram ([#265](https://github.com/paulpham157/sdf/pull/265)).
- Add a Decisions page screenshot to the README ([#266](https://github.com/paulpham157/sdf/pull/266)).

### CI/Build

- Split CI into pull-request, main, and scheduled workflows ([#162](https://github.com/paulpham157/sdf/pull/162)).
- Add GoReleaser config and `sdf version` link-time metadata (`internal/version` ldflags) ([#187](https://github.com/paulpham157/sdf/pull/187)).
- Bump `actions/setup-go` from 7.0.0 to 7.1.0 ([#180](https://github.com/paulpham157/sdf/pull/180)).
- Bump CodeQL to v4.39.4 and govulncheck to v1.10.0, cache setup-go on `go.sum`, and add `workflow_dispatch` to the scheduled workflow ([#244](https://github.com/paulpham157/sdf/pull/244)).
- Pin the release toolchain to `go1.26.11` and bump GoReleaser to v2.20.3 ([#245](https://github.com/paulpham157/sdf/pull/245)).

### Cleanup

- Remove leftover Python ignores and historical fake-key findings ([#160](https://github.com/paulpham157/sdf/pull/160)).
- Centralize `gen_ai.*` semconv names in `internal/genai`, pinned to v0.1.0 ([#202](https://github.com/paulpham157/sdf/pull/202)).
- Non-blocking review follow-ups: hooks token, shared setup helpers, and docs ([#208](https://github.com/paulpham157/sdf/pull/208)).

### Removed

- Remove the `sdf demo` command and non-test mock data. `sdf demo` now exits 2 with `unrecognized command "demo"`. The supported first-run path is `sdf version`, `sdf serve`, and `sdf init <agent> --write` ([#233](https://github.com/paulpham157/sdf/pull/233), fixes [#228](https://github.com/paulpham157/sdf/issues/228)).

[0.1.0]: https://github.com/paulpham157/sdf/releases/tag/v0.1.0
