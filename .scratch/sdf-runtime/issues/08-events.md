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
