# Herdr reports `idle`, never `done`, after an interactive agent turn

Observed live on 2026-09-25 (ticket #11), template `sdf-herdr-agents`
(Herdr 0.9.1, Claude Code 2.1.282), Claude in `api-key` mode.

## Observation

- `herdr agent prompt <id> <text> --wait --until idle` returns normally once the
  turn ends. Herdr only accepts the wait after it has seen a `working` (or
  `blocked`) state within 5 s of submission; otherwise it fails with
  `agent_prompt_stalled`.
- Afterwards `herdr agent get` keeps reporting `agent_status: "idle"` with
  `interactive_ready: true`. It never reports `done`, whether the workspace was
  created focused or with `--no-focus`, and whether or not Herdr's Claude hook
  integration (`herdr integration install claude`) is installed.
- `HerdrRuntime._status` maps only `done|completed|complete` to
  `RuntimeStatus.COMPLETED`, so `HerdrRuntime.status()` stays `RUNNING` after a
  successful turn.

## Consequence

`RuntimeAgentAdapter` reports a `RUNNING` session as a failed run
(`exit_code=1`), so `ExecutionService.run` marks the Attempt failed and never
calls the evaluator for an interactive Herdr agent. `AgentFixtureLoop`
evaluates whatever the runtime status is, so ticket #11's live Attempt goes
through it (the same path as the live fixture loop) and records Evaluator
Evidence from it.

## Option B (not done in #11)

Treat a normal return of `agent prompt --wait` as the end of the turn:
`HerdrRuntime.send` would return `RuntimeStatus.COMPLETED` when the wait returns
without error, and `status()` would keep that value while Herdr reports `idle`
for the same session. The `agent_prompt_stalled` path that keeps a session
`RUNNING` would stay as it is. This changes shared runtime semantics (Codex and
Claude alike): "completed" would mean "the agent finished its turn", which is
still separate from evaluator acceptance. It needs its own ticket and unit tests
for both agent kinds.
