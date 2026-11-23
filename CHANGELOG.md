# Changelog

All notable changes to SDF will be documented in this file.

This file covers the first public release of the current product: a vendor-neutral OpenTelemetry (OTLP) collector for AI coding-agent telemetry, written in Go and stored in SQLite. It does not list the pre-rebuild Python/Herdr history. That tree is snapshotted at tag `legacy-v1` (commit `38f45e80ed05fd72e9387c2327d7fd7286bcfac3`, 2026-11-08 13:23 America/New_York).

## [0.1.0] — UNRELEASED

First tagged release. Requires Go 1.26 or newer to build from source. Install with `go install github.com/paulpham157/sdf/cmd/sdf@v0.1.0` (after the tag exists) or with the prebuilt archives on GitHub Releases.

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

### Security

- Validate `sdf init --host` / `--port` and quote shell snippets ([#195](https://github.com/paulpham157/sdf/pull/195)).
- Reject forged OTLP posts and rebound UI hosts ([#199](https://github.com/paulpham157/sdf/pull/199)).

### Docs

- Document live Codex sandbox requirements for git-hook linking ([#166](https://github.com/paulpham157/sdf/pull/166)).
- Split the README into a short overview and `docs/` ([449212e](https://github.com/paulpham157/sdf/commit/449212edd0bb7ba18347fb23fa96bb0abdc429ca)).
- Add 5-minute install docs for `go install` and (once published) prebuilt archives ([#187](https://github.com/paulpham157/sdf/pull/187); quickstart later aligned in [#233](https://github.com/paulpham157/sdf/pull/233)).
- Drop the stale “planned” text for `sdf verify`; document `sdf task declare --timeout` / `--no-secret-scan` and `sdf task show --json`; mention GitHub Copilot CLI in the README opening sentence ([#237](https://github.com/paulpham157/sdf/pull/237)).

### CI/Build

- Split CI into pull-request, main, and scheduled workflows ([#162](https://github.com/paulpham157/sdf/pull/162)).
- Add GoReleaser config and `sdf version` link-time metadata (`internal/version` ldflags) ([#187](https://github.com/paulpham157/sdf/pull/187)).
- Bump `actions/setup-go` from 7.0.0 to 7.1.0 ([#180](https://github.com/paulpham157/sdf/pull/180)).
- Centralize `gen_ai.*` semconv names in `internal/genai`, pinned to v0.1.0 ([#202](https://github.com/paulpham157/sdf/pull/202)).

### Cleanup

- Remove leftover Python ignores and historical fake-key findings ([#160](https://github.com/paulpham157/sdf/pull/160)).
- Non-blocking review follow-ups: hooks token, shared setup helpers, and docs ([#208](https://github.com/paulpham157/sdf/pull/208)).

### Removed

- Remove the `sdf demo` command and non-test mock data. `sdf demo` now exits 2 with `unrecognized command "demo"`. The supported first-run path is `sdf version`, `sdf serve`, and `sdf init <agent> --write` ([#233](https://github.com/paulpham157/sdf/pull/233), fixes [#228](https://github.com/paulpham157/sdf/issues/228)).

[0.1.0]: https://github.com/paulpham157/sdf/releases/tag/v0.1.0
