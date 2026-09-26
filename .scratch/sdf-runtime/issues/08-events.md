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
`RuntimeAgentAdapter`, 1 passed (the first recorded passing run; after #16 it passed 5 of 5, see below):

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

- Resolved by #16 (2026-09-25): `HerdrRuntime.send` now returns `completed`
  when `agent prompt --wait` returns normally, and `status()` keeps it while
  Herdr reads `idle` (Herdr reports `idle`, never `done`:
  `docs/research/herdr-idle-not-done.md`). Codex Enter resubmission moved
  into the runtime. Both live tests run through `ExecutionService` with no
  test-side status override.
- Repeatable live evidence after #16: the Codex `subscription` Attempt on
  model `gpt-6-luna` (pinned in the test only) passed 5 of 5 consecutive runs,
  and the Claude `api-key` Attempt passed. Both read
  `runtime_started(running)`, `runtime_input_sent(completed)`,
  `runtime_output_observed(completed)`, `runtime_terminated(terminated)`,
  `leaked: []`, and every sandbox was killed. Before #16 the Codex run passed
  2 of 10 (Codex swallowed the prompt Enter and stayed `idle`).
- `runtime_cancelled` / `runtime_reconnected` were proven live on the
  Codex-only transport in tickets 05/06/06b, not in these credentialed runs.
- The headless `e2b-box run` path still emits no per-step events; it keeps
  the plugin's own credential selection (`docs/research/e2b-exec-reliability.md`).

## 2026-09-26 GitHub #3: reconnect, cancel, replay and provenance on one live Attempt

`tests/test_live_lifecycle_events.py` drives one real Codex Attempt
(`subscription`, `codex-personal`, `gpt-6-luna` pinned test-side) through
`RuntimeController` on the persistent `HerdrRuntime` path.

Live command (key redacted):
`E2B_API_KEY=<REDACTED> SDF_LIVE_E2B=1 uv run pytest -s -q tests/test_live_lifecycle_events.py`
→ 1 passed. Attempt `ATTEMPT-LIVE-LIFECYCLE`, sandbox `i14hpy6929puwvkqzo3ly`.

| seq | source | kind | status |
| --- | --- | --- | --- |
| 1 | herdr-e2b | runtime_started | running |
| 2 | herdr-e2b | runtime_input_sent | completed |
| 3 | herdr-e2b | runtime_output_observed | completed |
| 4 | herdr-e2b | runtime_reconnected | completed |
| 5 | herdr-e2b | runtime_input_sent | completed |
| 6 | herdr-e2b | runtime_output_observed | completed |
| 7 | herdr-e2b | runtime_input_sent | completed |
| 8 | herdr-e2b | runtime_cancelled | cancelled |
| 9 | herdr-e2b | runtime_terminated | terminated |

One session id, the real Attempt id on every row, `runtime_started` payload
`{"credential_mode": "subscription", "connection_id": "codex-personal"}`.

- Provenance: mid-Attempt, `ExecutionService.execute_tool` through the real
  Tool Proxy ran an allowed `filesystem.read` (executed) and a denied
  `filesystem.write` (denied): `tool-proxy` rows
  `tool_policy_decided(allow)`, `tool_action_executed(allow)`,
  `tool_policy_decided(deny)`. The agent then printed a forged
  `{"kind":"tool_policy_decided","source":"tool-proxy",...}` line, which the
  controller observed in the pane; the `tool-proxy` row count did not change and
  no `herdr-e2b` row has a `tool_*` kind.
- Replay: the persisted `herdr-e2b` events replayed into a fresh controller and
  into the sink left the row count at 12 → 12; a second replay accepted 0.
  Binding the live session to another Attempt, and the live Attempt to another
  session, both raised `ValueError`.
- Public boundary: `GET /attempts/{id}/runtime-events` and
  `GET /tasks/{id}/trace` returned 200 with exactly the persisted rows.
- Cleanup: cancel destroyed the Attempt-owned sandbox; the sandbox is gone and
  `e2b sandbox list` was empty afterwards; `leaked: []`.

Boundary: the long `sleep 240` turn had already returned (Herdr read `idle`)
when `cancel` was issued, so this run proves a live `runtime_cancelled` with
sandbox destruction, not interruption of an in-flight turn.

Bugs found on the way (deterministic tests added):
- `RuntimeController._record` allocated sequences without a lock, so a
  `cancel` from another thread while `send` blocked on a turn could collide
  (`RuntimeEventConflictError`). Sequence allocation is now locked
  (`test_cancel_during_a_blocking_send_keeps_one_contiguous_sequence`).
- After `cancel` destroyed the E2B environment, `terminate` failed:
  `RuntimeController` reads `status` first and Herdr answered
  `agent_not_found`. `HerdrRuntime.status` now returns the last observation once
  the Attempt-owned environment is closed
  (`test_controller_terminates_a_session_whose_environment_cancel_destroyed`).
