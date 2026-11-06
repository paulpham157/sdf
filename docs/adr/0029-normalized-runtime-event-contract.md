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
   runtime. That binding is *unconfirmed* until the runtime confirms it:
   - `reconnect(session_id)` succeeds: `runtime_reconnected` is appended at
     the next sequence. If the runtime no longer has the session it raises,
     and the binding stays unconfirmed.
   - `start` first asks the runtime (`status`) whether it still has the
     recovered session; every runtime answers an unknown session with
     `KeyError` before any provider call. If it still has it (e.g. Herdr
     after `restore_binding`), the runtime reattached and no second start is
     recorded. Session-id equality alone is not trusted: Herdr names an agent
     after its kind, so a fresh sandbox returns the same id.
   - The runtime lost the session and `start` returns a **new** id: the
     Attempt is rebound. The new session's stream begins at sequence 1 with
     `runtime_started` carrying `supersedes_session_id`. The old session is
     retired (further calls on it raise) and nothing is synthesised on its
     stream. A later `recover` follows the supersession chain to the newest
     session.
   - The runtime lost the session and `start` returns the **same** id (a
     fresh process reusing the name): `runtime_started` is recorded at the
     next sequence of that stream with `replaces_lost_session: true`.

   A binding created by a live `start` in the same controller, or one the
   runtime still has, is never rebound: a different session id from the
   runtime is then an error.
   Without `recover`, a repeated `start` is recorded as a new observation at
   the next sequence.
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
- **Startup reconciliation is not `RuntimeController.recover`.** The
  amendment below settles a dead Attempt row. `recover()` only restores a
  runtime-event stream, and it is still not called from `LiveRunAdapter`.
  Each `/run`
  creates a fresh sandbox and kills it in `finally`, so there is no live
  session to reattach to, and headless runtimes name sessions
  `<kind>-<attempt>-<n>` per process: a re-run of the same Attempt would
  reuse the old id and be mistaken for a reattach, suppressing a real
  `runtime_started`. Real reattach needs the Herdr binding snapshot
  (workspace and pane ids) and the sandbox id persisted, and a sandbox that
  outlives the SDF process; that belongs with the P2 bridge work.

## Amendment (2026-11-06): startup reconciliation of a dead Attempt

This ADR stays **proposed**. The amendment is part of that proposal. It does
not accept it. It does not change the event contract above. It says what a
restart does with an Attempt left `running`.

`RuntimeController.recover` is not used. FastAPI lifespan startup, lifespan
shutdown, and `POST /tasks/{id}/run` (before a new Attempt is created) call
`recover_orphaned_attempts`. A background refresh does the same on PostgreSQL
and on any engine that is not the single-connection in-memory SQLite used by
hermetic tests.

**Who is live.** `begin` stamps `executor_id` (this process) and
`executor_heartbeat_at` in the same commit that enters `running`, and holds
the claim on the `ExecutionService` until the Attempt is terminal. Before
that, the process holds a lock that the operating system or PostgreSQL
releases when the process dies: an exclusive `flock` beside a SQLite file,
or a session advisory lock on PostgreSQL.

- This process's id stays live while the process is alive. Dropping the
  `ExecutionService` (the request ended, or a material decision left the
  Attempt running, ADR 0033) is not death and is not labeled `killed`.
  A forked child mints its own id; it does not keep the parent's id and
  then treat the parent's Attempt as abandoned. Another process does not
  settle these rows while this process still holds the lock.
- A different `executor_id` is live while its process holds that lock. A
  stale heartbeat does not make it dead. A fresh heartbeat does not make it
  live after the lock is gone. The heartbeat is not the cross-process fence.
- In-memory SQLite has no file and no second OS process. There the foreign
  id is judged by the heartbeat (30 seconds). A held object in this process
  is never reaped because the heartbeat is old.
- A running row with **no** `executor_id` is not swept on startup. That
  includes Attempts left `running` by a process from before migration
  `0027`. Sweeping them would also fail a pre-lease process that is still
  alive. New runs always take a lease. An operator who has stopped every
  pre-lease process may set `SDF_RECOVER_UNLEASED_ATTEMPTS=1` for one
  startup. That fails unleased `running` rows only. It does not fail a row
  whose executor lock or in-process claim is held. Setting it while an old
  process is still running will fail that process's Attempts. That is a
  deployment decision; the default is off.

**What changes.** A dead leased Attempt moves `running` → `failed`, and
`error_class` becomes `killed` only when it was empty. If the Task is
`running` and no Attempt of that Task is still `running`, the Task moves
`running` → `evaluating` → `failed`, which are existing transitions. The
Attempt and the Task are updated in one transaction, each with a
compare-and-swap on the status that was read. A lost swap writes nothing
for that row. A second pass finds no dead lease and writes nothing.
Evidence, revisions, graph rows, and provenance are not inserted, updated,
or deleted.

**Fences.** `finish` and the adapter-failure path update the Attempt only
while it is still `running` for this executor (or unleased, for a row this
process is finishing directly). A loss rolls that transaction back, so a
late writer does not keep artifacts or Evidence and does not move the Task.
`/run` returns 409. Coordinator cancellation moves `running` → `cancelled`
only while the Attempt is still `running`, then the Task toward
`inconclusive` only if that cancel won and nothing is still running. Cancel
and recovery can race: one terminal state wins, the other leaves the rows
alone. A terminal Attempt is not moved again.

**After settlement.** The same `dispatch_key` replays the failed Attempt and
does not call the adapter. A new key can enter `ready` (the Task is
`failed`). That new key is a new adapter call.

**Boundaries.**

- On file SQLite and PostgreSQL, a dead process is visible as soon as its
  lock is released. Recovery does not wait out a heartbeat, and it does not
  reap a process that still holds the lock.
- The same `dispatch_key` is one Attempt across connections that share the
  database, not only inside one process. The unique key is the fence. The
  loser does not call the adapter.
- In-memory SQLite cannot see another OS process. Its foreign-executor rule
  is the heartbeat compared on the database clock, not the host's
  `utcnow`, and only there.
- `finish` does not update the Attempt row until evaluation has returned.
  Execution Evidence and verification Evidence commit in that same short
  transaction. If the compare-and-swap loses, the transaction rolls back
  and the Evidence is not kept. A second key on a Task that already has a
  running Attempt is rejected before the adapter runs.
- `flock` is not a reliable lock on NFS. A deployment that puts the SQLite
  file on NFS does not get this fence. PostgreSQL advisory locks follow the
  database connection, not the filesystem.
- No sweep of every `running` row. Unleased rows stay `running` until the
  operator sets `SDF_RECOVER_UNLEASED_ATTEMPTS=1` after pre-lease processes
  are stopped.
- Not exactly-once for external effects. The adapter for one key runs at
  most once. Process death does not prove a remote sandbox has exited. A
  new key can start another sandbox that overlaps one the dead process
  already created. Recovery does not start that key, and it does not repeat
  the old key. See the ADR 0031 amendment.
