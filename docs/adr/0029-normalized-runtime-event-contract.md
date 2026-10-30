# ADR 0029: the normalized runtime event contract

Status: proposed (issue #57, P3 in `docs/roadmap.md`)

## Context

`runtime_events` already stored Attempt/session-correlated observations with a
unique `(source, attempt_id, session_id, sequence)` identity (migration 0007),
written by the `RuntimeController` (lifecycle) and the Tool Proxy (policy and
execution). P3 asks for a stated contract: correlation and provenance on every
event, idempotent replay, defined behaviour for out-of-order delivery and
reconnects, `TOOL_REQUESTED`/`POLICY_DECIDED` only from trusted SDF action
boundaries, and no path by which telemetry becomes acceptance evidence.

Gaps found:

- Any code could build a `RuntimeEvent` with a `tool_*` kind and any source,
  and a runtime controller could replay one, so a forwarded line of agent
  output could have been stored as a trusted tool event.
- A controller created after a restart allocated sequences from 1 again, so
  its first event collided with the persisted stream (a conflict error, or a
  silent drop if the payloads happened to match).
- Read endpoints ordered by `sequence` alone, interleaving independent streams
  arbitrarily.
- Rows were mutable, and there was no `tool_requested` event.

## Decision

The contract lives in `sdf_core/events.py`.

1. **Correlation.** Every event has `attempt_id`, `session_id`, `source` and a
   per-stream `sequence`. A stream is `(source, attempt_id, session_id)`.
2. **Provenance by kind.** Kinds beginning with `tool_` are action-boundary
   events (`tool_requested`, `tool_policy_decided`, `tool_action_executed`,
   `tool_action_failed`). Only the Tool Proxy source (`tool-proxy`) may emit
   them, and it emits nothing else. Every other kind is a runtime observation.
   The rule is enforced at three points: building a `RuntimeEvent`
   (`UntrustedEventError`), constructing a `RuntimeController` or
   `SqlAlchemyToolEventSink` with the wrong source, and the database
   (`ck_runtime_events_provenance`, migration 0019). A runtime controller
   replays only its own source.
3. **`tool_requested`.** The Tool Proxy records it when it receives a
   structured `ActionRequest` (once per action identity), before the binding,
   guard and policy decide. Terminal text is refused with `TypeError` and
   produces no event.
4. **Replay is idempotent.** Re-appending an event with the same identity and
   data is a no-op, and the same identity with different data raises
   `RuntimeEventConflictError` (unchanged). A controller replays a batch in
   `sequence` order, whatever order it arrived in.
5. **Out-of-order delivery.** An event is stored as delivered. Missing lower
   sequences are reported as `gaps` in the stream summary until they arrive.
   Nothing is synthesised to fill them and nothing is reordered in storage.
6. **Reconnect and restart.** A new controller continues a stream from the
   highest persisted sequence for its source, attempt and session, so it never
   reuses a sequence. Calling `RuntimeController.recover(attempt_id)` first
   restores the session binding from the durable stream without calling the
   runtime. After that, `start` records no second start and `reconnect`
   appends `runtime_reconnected` at the next sequence. Without `recover`, a
   repeated `start` is recorded as a new observation at the next sequence.
7. **Deterministic order.** Within a stream, `sequence` is authoritative.
   Across streams, `GET /attempts/{id}/runtime-events` and `/tasks/{id}/trace`
   merge by the head event's persisted time, then stream key, then sequence.
   The merge never breaks a stream's own order. The `/runs/{id}/timeline`
   view stays a persisted-time view across all record types (ADR 0023).
8. **Append-only.** Runtime events cannot be updated or deleted: an ORM guard
   plus, on PostgreSQL, the trigger `trg_runtime_events_append_only`.
9. **Not acceptance evidence.** No event changes Attempt status, Task status or
   Evidence. The evaluator and the scorecard do not read events. Normalized
   views carry `category`, `trusted_boundary` and `acceptance_evidence: false`.
   `GET /attempts/{id}/event-streams` summarises each stream (count, sequence
   range, gaps, last kind/status) for consumers such as routing (P4), and it is
   telemetry, not Evidence.

## Consequences

- Migration 0019 fails if stored rows already break the provenance rule. It
  names the count and does not rewrite them. On a database written only by
  SDF, no such rows exist.
- Tool Proxy streams gain a leading `tool_requested` event, so code that
  expected exactly `[tool_policy_decided, tool_action_executed]` sees three
  kinds.
- **Remaining live boundary.** These rules are proven with local tests (SQLite
  and PostgreSQL). The live `/run` path writes `run-<provider>` lifecycle
  events through the same controller. Live reconnect after a real process
  restart, and redelivery from a remote Herdr bridge, have not been exercised
  against a deployed endpoint (the P2 HTTPS bridge proof remains the gate for
  that).
