# 03 Tool Proxy and Policy

Status: resolved
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
