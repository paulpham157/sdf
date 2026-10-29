# ADR 0026: Tool Proxy actor binding and principal audit

Status: proposed (branch `sec/51-tool-audit-principal`, issue #51)

## Context

`POST /attempts/{id}/actions` built the Tool Proxy `ActionRequest` with
`actor=payload.actor`. Any operator token could therefore act as any agent
(e.g. `agent:root`), pass an `AllowlistPolicy.allowed_actors` check meant for
another agent, and leave `tool_audits` rows that named the forged actor and
not the token that made the call. Since ADR 0015 the API knows who is
calling (`request.state.principal`), but the tool audit trail did not record it.
Any operator could also drive the tools of any other principal's Attempt.

## Decision

1. **The actor comes from the server.** The endpoint sets
   `actor = "agent:<attempt.agent>"` from the Attempt row. `payload.actor` is
   optional and deprecated.
2. **Attempt binding is checked first.** `AttemptBinding` runs inside
   `ToolProxy` *before* the attempt guard and the configured policy, so a
   forged identity is always reported as such, even on a terminal Attempt:
   - a caller-sent `actor` that differs from the derived one is denied
     (`policy = "actor-binding"`, HTTP `403`, `detail.error = "actor_mismatch"`);
   - a principal that did not start the Attempt is denied unless it is an
     admin (`policy = "attempt-binding"`, HTTP `403`,
     `detail.error = "attempt_not_owned"`). The owner is the new nullable
     `attempts.principal`, set by `POST /tasks/{id}/run` (the caller) and by
     plan runs (the run's `started_by`). An Attempt without an owner
     (in-process or created before 0018) is admin-only over HTTP.

   Both denials go through the normal `ToolProxy` path and write a
   `policy_decided`/`deny` row (plus a `tool_policy_decided` runtime event)
   with the derived actor and the authenticated principal; the claimed actor
   is in the row's `detail` (reason).
3. **The claimed actor is not part of the action identity.** `action_id`
   hashes only the server-derived fields and the caller's `context`. Context
   keys starting with `sdf_` (any case) are reserved for the server and are
   refused with `422`, so a caller cannot forge server annotations.
4. **One decision row per decision; dedupe only the execution fence.** The
   unique index on `tool_audits (attempt_id, action_id, event)` is replaced by
   the partial unique index `uq_tool_audits_execution_event` with
   `WHERE event <> 'policy_decided'` (SQLite and PostgreSQL). Claims,
   `action_executed` and `action_failed` stay one per action (the idempotency
   fence and replay answer are unchanged). Every `policy_decided` record is
   inserted, keyed only by its own `audit_id` (a redelivery of the very same
   record is a no-op). So a second principal repeating an executed action gets
   the idempotent `executed` replay *and* its own attributed decision row, and
   every denial (actor mismatch, ownership, policy) is recorded separately.
   The `tool-proxy` runtime events follow the same rule.
5. **Every tool audit row names the principal.** New nullable column
   `tool_audits.principal` (indexed). `SqlAlchemyAuditSink(db, principal=...)`
   and `SqlAlchemyToolEventSink(db, principal=...)` stamp it on decisions,
   claims and outcomes. `ExecutionService.execute_tool(..., principal=...,
   binding=...)` passes them through. In-process callers that have no HTTP
   principal leave it `NULL`.
6. **401 is defense in depth.** The auth middleware (ADR 0015) already
   rejects unauthenticated requests before the handler runs. The handler
   still fails closed with `401` if `request.state.principal` is missing, so
   a middleware misconfiguration cannot produce unattributed tool actions.

All of this is in migration `0018_tool_audit_principal`.

## Consequences

- **API change:** `actor` in the request body is optional. Clients that sent
  the correct `agent:<agent>` value keep working. Clients that sent anything
  else get `403`. Operators can only drive Attempts they started; admins can
  drive any. `sdf_*` context keys get `422`. Responses add `actor` and
  `principal`.
- **Audit volume:** repeated identical requests now add one decision row
  each (they used to be folded into the first). Executions are not repeated.
- Requests refused before the Tool Proxy runs (`401/403` from auth, `404`
  unknown attempt, `409` missing workspace, `422` validation) are not written
  to `tool_audits`. Auth denials are already recorded in `auth_audit`.
- Rows written before 0018 have `principal = NULL`; pre-0018 Attempts have no
  owner and are admin-only for HTTP tool actions.
- **Downgrade is lossy:** to restore the old full unique index it keeps only
  the earliest `policy_decided` row per `(attempt_id, action_id)` (on
  PostgreSQL the append-only trigger is disabled for that delete and
  re-enabled), then drops `tool_audits.principal` and `attempts.principal`.
- **Known limitation:** ownership is per principal, not per Attempt token. A
  principal that started an Attempt can drive it from any of its tokens; an
  attempt-scoped credential for in-sandbox agents is future work.
