# Trace query and state machine

Type: task
Status: resolved
Blocked by: 01, 02, 03, 04

Implement Task/Attempt transition guards and `GET /tasks/{id}/trace`, returning the full chain from Objective through Evidence.

Acceptance: invalid transitions fail; retries create new Attempts; the trace distinguishes `validates`, `contradicts` and `INCONCLUSIVE`.

## Answer

Implemented guarded Task/Attempt transitions, idempotent dispatch, retry transitions for failed/inconclusive Tasks, Attempt/Artifact/Evidence graph nodes and `GET /tasks/{id}/trace`. The trace walks both directions as needed to return the full business-to-evidence chain and preserves validation relation semantics.
