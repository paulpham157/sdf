# Agent Credentials: orchestration process log

Local log, not committed until the work is finished and pushed (user rule, 2026-09-25).

Spec: GitHub #4. Tickets #5–#15 (01–11), follow-up #16 (12).
Coordinator: Claude Code in the main checkout. Orca Run `run_ceb988fb741f`.
Integration branch: `feat/agent-credentials` (local only, not pushed).

## How the work was run

- Each ticket went to one supervised Orca worker (a Claude Code terminal) in its own top-level worktree under `~/orca/workspaces/SDFA/ac-NN-<slug>`, created with `orca orchestration worker-start --worktree new-top-level`.
- The worker's base branch was chosen from the dependency graph. Tickets that depend only on #5 branched from #5's branch, and only on #8 from #8's branch. Tickets needing several merged tickets branched from `feat/agent-credentials`.
- Each worker was told to do TDD and to fan out its own subagents (explore, write tests in parallel, code-review before `worker_done`).
- Each spec carried the handoff constraints: never print secrets, `stdin=DEVNULL` for e2b, kill every sandbox, the herdr-e2b plugin is read-only, never forward the host `ANTHROPIC_*`, no push, no PR, no issue comments.
- On each `worker_done` the coordinator:
  1. re-ran the full suite in the worker's worktree;
  2. checked `e2b sandbox list`;
  3. spot-checked secret handling (template JSON, plugin helper output, the pane-env seed);
  4. released the worker;
  5. merged the branch into `feat/agent-credentials` with `--no-ff` in a temporary worktree and re-ran the suite;
  6. started the next unblocked wave.

## Waves

| Wave | Tickets (worktree base) |
|---|---|
| 1 | #5 (main), #8 (main) |
| 2 | #9, #13 (from #8); #6, #7 (from #5); #10 (integration after #5 and #8) |
| 3 | #11, #12, #14 (integration after #6, #7, #9, #10, #13) |
| 4 | #15 (integration after all) |
| 5 | #16 follow-up (integration), done and merged (`4e0c219`) |

## Coordinator decisions

- **#11: create-time envs do not reach Herdr panes.** The Herdr server starts before the envs apply. Decision: one agent-agnostic seed writes `~/.config/sdf/agent-env.sh` (0600) with exports by variable name, read from `process.env` inside the box; `.bashrc` sources it. #11 owns it; #14 must not duplicate it.
- **#11: Herdr reports `idle`, never `done`.** Decision: option C. The live Attempt uses `AgentFixtureLoop`, runtime `send`/`status` semantics stay unchanged, and the gap is written up in `docs/research/herdr-idle-not-done.md`. #14 still drove `ExecutionService` through a test-only wrapper.
- **Merging #14.** Conflicts in `sdf_core/credential_injection.py` (imports) and `tests/test_credential_injection.py`. Both sides were kept. Two #14 tests were updated to expect #11's pane-env seed, which exposes the token by name.
- **#15 reported `failed`.** The docs, canvas, and `sdf-herdr-codex` retirement (ADR 0009) are done. The live Codex `subscription` run passed 2 of 10 because Codex swallows the prompt Enter and Herdr never reports `done`. The user approved opening the follow-up as #16. #15 is now blocked by #16 on GitHub.

## User decisions (2026-09-25)

1. Open the follow-up (#16). Live Codex tests use `gpt-6-luna`; no larger model, because model escalation is a separate ticket.
2. Report results only into local files. Do not commit test results until the work is finished and pushed.
3. Commit in parts.
4. Finish the work before pushing.
5. Merge the worktree branches into `feat/agent-credentials`, remove the finished worktrees, keep the unfinished ones, and log the process (this file).

## Housekeeping done

- Docs committed in parts on `feat/agent-credentials`:
  - `8413fab` ADR 0007 and glossary
  - `c9be804` issue tracker switched to GitHub
  - `63f9196` spec and tickets mirror
  - `2b39026` #16 mirror
- All 11 worktree branches were verified as ancestors of `feat/agent-credentials` with no uncommitted changes.
- Removed through `orca worktree rm`: `ac-01` … `ac-10`. Their local branches were deleted with them; the commits live on in the integration branch.
- `ac-11-docs-sync` was kept until #16 closed #15's first criterion, then removed. `ac-12-turn-completion` (#16) was removed after its merge; its uncommitted `results-16.md` was first copied to the main checkout and checked byte-for-byte.
- No worktrees remain; only the main checkout, on `feat/agent-credentials`.
- Left untouched as the handoff asked: `.pi/`, `:memory:.ses`, `graphify-out/cache/last_query_stamp`.

## #16 and the close of #15

- The #16 worker implemented option B: `HerdrRuntime.send` returns `COMPLETED` on a normal prompt return, and Codex Enter resubmission moved into the runtime. `gpt-6-luna` is pinned only in `tests/test_subscription_codex_live.py`; `sdf_core/` has no model reference (checked by the coordinator).
- Live: Codex subscription on `gpt-6-luna` passed 5 of 5 consecutive runs, and Claude api-key passed. Details are in `results-16.md`.
- Coordinator: re-ran the suite in the worktree (430 passed, 20 skipped), merged (`4e0c219`, no conflicts, 430 passed), then updated the #15 docs (`08-events.md`, `e2b-exec-reliability.md`) with the repeatable evidence (`87b2d58`).

## Before push (open)

- Final two-axis code review over `main..feat/agent-credentials` (in progress).
- User go-ahead, then push and open a PR. After the push, commit the results files and post issue comments.
