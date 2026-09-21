# 03 Tool Proxy and Policy

Status: needs-info
Blocked by: 02

## What to build and acceptance

Attempt-bound allow/deny decisions precede action execution; denied actions produce no side effects; approval and audit boundaries are explicit.

## Verification

Record behavior checks and exact local/live boundary before resolution. Follow ADR-0004 and the parent spec. Review before commit.

## Answer

Implemented the deterministic Tool Proxy boundary in `sdf_core/policy.py` and
`sdf_core/tools.py`. Structured `ActionRequest` values bind actor, tool,
action, resource, context and `attempt_id` into a stable action identity.
`AllowlistPolicy` returns explicit allow/deny decisions and can constrain
actor, resource and context. `ToolProxy` records an Attempt-linked audit
decision before execution, never invokes the executor for a denial, rejects
untrusted terminal text, and records success/failure outcomes.

Verification: focused `6 passed`; full `.venv/bin/pytest -q` `34 passed, 1
skipped`; compile check passed. This is local deterministic evidence only.
Audit persistence and OS filesystem/process/network containment remain out of
scope for this ticket and are tracked by later persistence/sandbox work.

Follow-up implementation added `SandboxToolExecutor` plus a durable
`SqlAlchemyAuditSink` (migration `0006_tool_audits`) and regression coverage for
Attempt-bound persisted decisions. The ticket remains `needs-info` because
the live OS-level containment and full `POST /tasks/{id}/run` integration are
not yet proven.

Review boundary: the seam is not yet wired into `POST /tasks/{id}/run`, does
not verify Attempt existence/active state, and has no durable replay/idempotency
guard. Keep this ticket `needs-info` until runtime integration and durable
action/audit semantics are implemented.

Additional hardening is now in place: `SqlAlchemyAttemptGuard` rejects missing
or terminal Attempts before the executor runs, and `SqlAlchemyAuditSink` uses
the durable `(attempt_id, action_id, event)` identity from migration `0008` to
ignore audit redelivery. The ticket remains `needs-info` only for full run
endpoint wiring and live OS-level containment proof.

`ExecutionService.execute_tool()` now wires a structured ActionRequest through
the Attempt workspace, FixtureSandbox, ToolProxy, AttemptGuard and durable audit
seam. The task-run endpoint remains separate from tool messages; callers use
the explicit `/attempts/{attempt_id}/actions` boundary rather than trusting
terminal text or arbitrary HTTP commands.

`ExecutionService.execute_tool()` now defaults to durable SQLAlchemy audit plus
normalized `tool-proxy` runtime events, even when a caller supplies an
additional in-memory audit consumer. Integration coverage verifies both event
records for an allowed action.

The public `POST /attempts/{attempt_id}/actions` boundary now accepts only a
structured action payload. It uses the server-side `SDF_TOOL_ALLOWLIST`
configuration (unset means deny-all), requires an active Attempt workspace,
and returns the durable policy/execution outcome. API tests cover default
deny/no side effect and configured allow with persisted tool-proxy events.
Live OS-level containment remains a separate unresolved gate.

Live E2B containment evidence (2026-09-21) now covers the structured process
path: one Attempt-bound action executed in a disposable E2B box, its process
artifact was pulled and hashed, the three durable audit events shared the
Attempt identity, and the box was cleaned up (`tracked:false`). E2B network
deny remains unproven and is intentionally fail-closed by the backend.

The structured SDF network policy is now explicit: `network.request` is denied
at both the server allowlist and executor layers whenever
`SDF_CONTAINMENT_BACKEND=e2b`, even if an operator lists the action in
`SDF_TOOL_ALLOWLIST`. This does not restrict the coding agent's own E2B
network egress.

An Attempt-bound E2B process live smoke returned HTTP 200 from `e2b.dev` on
2026-09-21, confirming that agent egress remains functional while the separate
structured action stays denied.

Sequential redelivery of an already executed structured action is now fenced
by the durable `ACTION_EXECUTED` audit identity, so the executor is not called
again for the same `(attempt_id, action_id)`. This is local replay evidence;
cross-process claim races still require the PostgreSQL/concurrency gate.

The replay fence also treats a durable `ACTION_FAILED` outcome as terminal:
redelivery returns the recorded failure without re-invoking an executor. A
caller must issue a new action identity to request an intentional retry.

Tool audit rows are now append-only at the ORM boundary, with PostgreSQL
migration `0011_tool_audit_append_only` adding the direct-SQL trigger fence.

Before executor invocation, durable `action_claimed` insertion now gives one
delivery the action identity; concurrent redelivery loses the claim and does
not invoke the executor. The PostgreSQL Compose multi-session gate now passes
with exactly one winner; live OS/provider containment remains the outstanding
reason for this ticket's `needs-info` status.

Live E2B network-policy smoke (2026-09-21): an agent process inside the
disposable `base` box fetched `https://e2b.dev` and returned HTTP 200. With
`SDF_CONTAINMENT_BACKEND=e2b`, `SDF_CONTAINMENT_SMOKE=passed`, and an operator
allowlist that included `network:request`, the structured Tool Proxy action
still returned `DENIED` before any request was sent. `e2b-box status --json`
reported `tracked:false` and `e2b-box list --json` was empty after cleanup.
Active-work cancellation and detached-descendant cleanup remain unverified.

Policy tradeoff: the structured `network.request` capability remains denied
because it is not routed through E2B, while agent-owned egress remains enabled
for full agent functionality. Full suite and regression tests pass. The
remaining security hardening is destination-aware E2B egress proxy/allowlist
control to reduce exfiltration and unintended endpoint calls.
