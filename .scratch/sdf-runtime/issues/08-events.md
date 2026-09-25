# 08 Normalized lifecycle events

Status: resolved
Blocked by: none (live boundary closed 2026-09-25 by agent-credentials #15)

## What to build and acceptance

Source-labelled correlated events replay idempotently; trusted Tool Proxy alone emits tool/policy events; agent completion differs from evaluator-confirmed success.

## Verification

Record behavior checks and exact local/live boundary before resolution. Follow ADR-0004 and the parent spec. Review before commit.

## Resolution

Runtime events now carry a trusted source label, attempt/session correlation,
monotonic sequence, and a durable unique identity. The SQLAlchemy sink handles
redelivery idempotently and exposes replay queries; duplicate source/attempt/
session/sequence observations are ignored. Local lifecycle, persistence, and
migration tests pass. Live Herdr event delivery remains unverified.

Tool Proxy audit records now also emit trusted normalized events with
`source=tool-proxy` for policy decisions, executions and failures. The event
sink correlates Attempt/action identity and ignores redelivery of the same
structured action; terminal text still cannot create these events.

Runtime replay now rejects session rebinding across Attempts and Attempt
rebinding to a different session, preventing durable recovery from silently
crossing execution identities.

## 2026-09-25 Live verification attempt

**Status (corrected by coordinator)**: the Codex auth blocker is resolved for the headless `codex` template (see ticket 07 live resolution). Live *runtime lifecycle events* are still unverified: the headless E2B adapter returns one terminal result and emits no per-step runtime events, and the persistent `sdf-herdr-codex` HerdrRuntime path still lacks Codex credential injection.

**Locally verified** (all 178 tests pass):
- `SqlAlchemyRuntimeEventSink` persists runtime_started, runtime_input_sent, runtime_output_observed with unique identity and correlation
- `RuntimeEventRow` rejects redelivery with identical source/attempt_id/session_id/sequence
- Tool Proxy events created with source=tool-proxy, kind=tool_policy_decided and tool_action_executed
- Event replay forbids session rebinding across attempts

**Live event verification blocked:**
- Codex halts at authentication prompt before sending any task input
- No runtime_input_sent events emitted (input never reached agent)
- No runtime_output_observed events created (agent did not run)
- Herdr event stream never activated (session did not proceed)
- Sandbox ID: im41p6h0cq8rr3joitci6 (captured and killed)

## 2026-09-25 Live boundary resolved (agent-credentials #15)

The persistent `HerdrRuntime` path now has create-time Agent Credentials
(ADR 0007) on the `sdf-herdr-agents` template, so the two live Attempts from
#11 and #14 were re-run with a `RuntimeController` +
`SqlAlchemyRuntimeEventSink` (`source="herdr-e2b"`) around the live runtime.
Both tests now assert the persisted lifecycle, not just the outcome. Printed
evidence is kinds, sources, statuses and sequences only (payloads carry pane
text); every sandbox was killed and `e2b sandbox list` ended empty.

**Claude, `api-key` mode** — `tests/test_claude_api_key_live.py`, 2 passed
(`AgentFixtureLoop`, which terminates nothing by itself):

| seq | source | kind | status |
| --- | --- | --- | --- |
| 1 | herdr-e2b | runtime_started | running |
| 2 | herdr-e2b | runtime_input_sent | running |
| 3 | herdr-e2b | runtime_output_observed | running |

`runtime_started` payload `{"credential_mode": "api-key", "connection_id": null}`;
evaluation PASS, Evidence `("add returns the sum", PASS, live)`; one
session id for all events.

**Codex, `subscription` mode (`codex-personal`)** —
`tests/test_subscription_codex_live.py` through `ExecutionService` +
`RuntimeAgentAdapter`, 1 passed (flaky, see below):

| seq | source | kind | status |
| --- | --- | --- | --- |
| 1 | herdr-e2b | runtime_started | running |
| 2 | herdr-e2b | runtime_input_sent | running |
| 3 | herdr-e2b | runtime_output_observed | completed |
| 4 | herdr-e2b | runtime_terminated | terminated |

`runtime_started` payload
`{"credential_mode": "subscription", "connection_id": "codex-personal"}`;
Task `succeeded`, Evidence `("add returns the sum", PASS)`, no secret in any
pane (`leaked: []`); one session id for all events.

**Remaining boundaries (not blockers for 08):**

- `completed` on the Codex session comes from the test driver, not from Herdr:
  Herdr reports `idle`, never `done`, after a turn
  (`docs/research/herdr-idle-not-done.md`). Follow-up:
  `.scratch/agent-credentials/issues/01-herdr-turn-completion.md`.
- The Codex live run is flaky at prompt submission. On 2026-09-25, 2 of 10
  runs passed (unchanged #14 test: 1 of 2; with the event sink: 1 of 8; the
  table above is from the passing run). Every failure was the same: Codex
  stayed `idle` after `agent prompt` and 3 extra Enter presses ("codex never
  started working on the prompt: idle"), before any lifecycle assertion.
  Every sandbox was killed. The follow-up ticket covers it.
- `runtime_cancelled` / `runtime_reconnected` were proven live on the
  Codex-only transport in tickets 05/06/06b, not in these credentialed runs.
- The headless `e2b-box run` path still emits no per-step events; it keeps
  the plugin's own credential selection (`docs/research/e2b-exec-reliability.md`).
