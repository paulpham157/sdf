# ADR 0026: Tool Proxy actor binding and principal audit

Status: proposed (branch `sec/51-tool-audit-principal`, issue #51)

## Context

`POST /attempts/{id}/actions` built the Tool Proxy `ActionRequest` with
`actor=payload.actor`. Any operator token could therefore act as any agent
(e.g. `agent:root`), pass an `AllowlistPolicy.allowed_actors` check meant for
another agent, and leave `tool_audits` rows that named the forged actor and
not the token that made the call. Since ADR 0015 the API knows who is
calling (`request.state.principal`), but the tool audit trail did not record it.

## Decision

1. **The actor comes from the server.** The endpoint sets
   `actor = "agent:<attempt.agent>"` from the Attempt row. `payload.actor` is
   optional and deprecated.
2. **A mismatch is denied and audited, not ignored.** If the caller sends an
   `actor` that differs from the derived one, an `ActorBindingPolicy` wraps
   the configured policy and denies through the normal `ToolProxy` path. That
   writes a `policy_decided`/`deny` audit row (plus a `tool_policy_decided`
   runtime event) with the derived actor, the authenticated principal and
   `context.sdf_claimed_actor`. The HTTP answer is `403` with
   `detail.error = "actor_mismatch"`. The claimed actor goes into the context, so
   the denial gets its own `action_id` and cannot take over the audit slot
   (`attempt_id, action_id, event`) of the real action.
3. **Every tool audit row names the principal.** New nullable column
   `tool_audits.principal` (indexed, migration `0018_tool_audit_principal`).
   `SqlAlchemyAuditSink(db, principal=...)` and
   `SqlAlchemyToolEventSink(db, principal=...)` stamp it on decisions (allow
   and deny), claims and outcomes. `ExecutionService.execute_tool(...,
   principal=...)` passes it through. In-process callers that have no HTTP
   principal leave it `NULL`.
4. The endpoint fails closed (`401`) if no authenticated principal is present.

## Consequences

- **API change:** `actor` in the request body is optional. Clients that sent
  the correct `agent:<agent>` value keep working. Clients that sent anything
  else now get `403` where they used to get an execution under the forged
  identity. Responses also return `actor` and `principal`.
- Requests refused before the Tool Proxy runs (`401/403` from auth, `404`
  unknown attempt, `409` missing workspace) are not written to `tool_audits`.
  Auth denials are already recorded in `auth_audit` with the principal.
- Rows written before 0018 have `principal = NULL`. The downgrade drops the
  column and its index.
