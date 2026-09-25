# Agent Credentials: per-ticket results

Local results file, not committed until the work is finished and pushed (user rule, 2026-09-25).

The coordinator re-ran the full default suite in each worker's worktree before merging. Live results are the workers' reports (`SDF_LIVE_E2B=1`). The coordinator checked `e2b sandbox list` after each wave and it was empty each time.

| Ticket | Branch / commits | Default suite (coordinator re-run) | Live result (worker report) | Outcome |
|---|---|---|---|---|
| #5 (01) SDK transport | ac-01 `24495ac` | 191 passed, 9 skipped | create, two commands, close, and timeout-kill: 2 passed | succeeded |
| #8 (04) credential core | ac-04 `68a4d92`, `199c1dc` | 215 passed, 7 skipped | none (pure) | succeeded |
| #9 (05) base URL rules | ac-05 `6c5d56a` | 298 passed, 7 skipped | none (pure) | succeeded |
| #13 (09) plugin bridge | ac-09 `56ee61b` | 239 passed, 8 skipped | `SDF_LIVE_PLUGIN=1` with `codex-personal`: shape only, green | succeeded |
| #6 (02) SDK staging | ac-02 `f00cce4`, `8dc1281`, `78500aa` | 205 passed, 10 skipped | round trip: 3 passed | succeeded |
| #10 (06) create-time injection | ac-06 `bcf798b`, `8aceee2` | 246 passed, 10 skipped | dummy values compared by sha256: green | succeeded |
| #7 (03) template `sdf-herdr-agents` | ac-03 `3bd6b07`, `ec8ec51` | 198 passed, 12 skipped | Claude and Codex start with no first-run dialogs: 3 passed, 3 runs in a row | succeeded |
| #12 (08) Codex api-key | ac-08 `495a811` | 383 passed, 17 skipped | dummy-key wiring green; real-key Attempt skipped (no `SDF_OPENAI_API_KEY`) | succeeded |
| #11 (07) Claude api-key | ac-07 `925ee78` | 381 passed, 17 skipped | dummy wiring and a real-key Attempt: green, Evidence recorded | succeeded |
| #14 (10) subscription | ac-10 `b343c43`, `8c847ff`, `92edd05`, `2bd99e8` | 395 passed, 16 skipped | Codex `codex-personal` Attempt: green twice | succeeded |
| #15 (11) docs and live boundary | ac-11 `e9d3512`, `77f9a9d`, `1819cbc`, `ecc6514` | 411 passed, 20 skipped | Claude api-key 2 passed; Codex subscription 2 of 10 | failed at first; first criterion closed after #16 (`87b2d58`) |
| #16 (12) turn completion | ac-12 `5394b88`, `98f1846`, `9889faf`, `ee2cb9d`, `59b9068`, `bdde610` | 430 passed, 20 skipped | Codex subscription on `gpt-6-luna`: 5 of 5 in a row; Claude api-key: 2 passed; both through ExecutionService with no override (details in `results-16.md`) | succeeded |

The integration branch after the #15 merge (`bdb1944`) gave 411 passed, 20 skipped. After the #16 merge and the #15 docs update (`87b2d58`): 430 passed, 20 skipped.

## Findings to report on the issues after the push

- **#12:** Codex 0.157.0 (and 0.155.1) takes its base URL from the `config.toml` key `openai_base_url`, not the `OPENAI_BASE_URL` env var. Verified in source and in a live sandbox. See `docs/research/codex-base-url.md`.
- **#11:** Herdr panes do not inherit create-time envs; the agent-agnostic `agent-env.sh` seed covers this. Herdr reports `idle` and never `done` (`docs/research/herdr-idle-not-done.md`), which led to #16.
- **#7:** The first live run was a false green. Claude hit a folder-trust dialog after reporting idle, so trust is now baked into the template for `/home/user`, `/home/user/project`, `/workspace` and `/tmp/sdf`, and the live test requires 12 s of steady idle.
- **#14:** Codex needs `-c projects."<attempt dir>".trust_level="trusted"`. Codex 0.157 swallows the first Enter sometimes, which is part of #16.
- **#15:** ADR 0009 retires the `sdf-herdr-codex` template (the repo directory is removed; the published E2B template is left alone).
- **#16:** live runs found 3 more bugs, all fixed with tests: Codex ignored the `-c` trust override (now pre-trusted in `config.toml`); `idle` flickered mid-turn (a settle window plus a visible-screen fallback); `capture_diff` crashed on `.pyc` files (binary files are now named, not decoded).
