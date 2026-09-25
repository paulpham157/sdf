# 01 Herdr turn completion: `idle` is not `done` (draft)

Status: needs-triage
Follow-up of: agent-credentials #11, #14, #15 (sdf-runtime ticket 08)
Draft only; not filed on GitHub.

## Problem

After an interactive agent turn, Herdr 0.9.1 reports `agent_status: "idle"`
with `interactive_ready: true`, never `done`
(`docs/research/herdr-idle-not-done.md`). `HerdrRuntime._status` maps only
`done|completed|complete` to `RuntimeStatus.COMPLETED`, so a finished turn
stays `RUNNING`. `RuntimeAgentAdapter` turns that into a failed run
(`exit_code=1`), and `ExecutionService.run` never calls the Evaluator for an
interactive Herdr agent. The live tests work around this in their own
drivers (the Codex `_Recorder` flips the status to `completed` once the turn
returns to `idle`; the Claude test uses `AgentFixtureLoop`, which evaluates
whatever the status is).

A related symptom: Codex 0.157 can swallow the submitting Enter, so the pane
stays `idle` after `agent prompt`. The #14 test presses Enter up to 3 times,
and the live run still fails sometimes before the turn starts
("codex never started working on the prompt: idle"), with or without the
lifecycle event sink.

## What to build

Option B from the research note: treat a normal return of
`herdr agent prompt --wait --until idle` (after Herdr saw `working` or
`blocked`) as the end of the turn. `HerdrRuntime.send` returns
`RuntimeStatus.COMPLETED`, and `status()` keeps it while Herdr reports `idle`
for the same session. The `agent_prompt_stalled` path stays `RUNNING`.
"Completed" means "the agent finished its turn" and stays separate from
Evaluator acceptance (Accepted Outcome). Move the resubmit-Enter handling for
Codex into the runtime so the live tests stop carrying their own drivers.

## Acceptance

- [ ] Unit tests for Codex and Claude: prompt returns normally → `COMPLETED`;
      `agent_prompt_stalled` with a foreground child → `RUNNING`; a later
      `idle` read keeps `COMPLETED`.
- [ ] `ExecutionService.run` evaluates an interactive Herdr Attempt without a
      test-side status override; `tests/test_subscription_codex_live.py`
      drops `_Recorder.status`'s `answered` override.
- [ ] Codex prompt submission is reliable across 5 consecutive live runs.
- [ ] The live lifecycle events still read started → input_sent →
      output_observed(completed) → terminated.
