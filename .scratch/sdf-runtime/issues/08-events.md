# 08 Normalized lifecycle events

Status: resolved
Blocked by: none for local event contract; live runtime verification remains open

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
