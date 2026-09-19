# Trace query and state machine

Type: task
Status: ready-for-agent
Blocked by: 01, 02, 03, 04

Implement Task/Attempt transition guards and `GET /tasks/{id}/trace`, returning the full chain from Objective through Evidence.

Acceptance: invalid transitions fail; retries create new Attempts; the trace distinguishes `validates`, `contradicts` and `INCONCLUSIVE`.

## Answer

Current audit: Reopened: dispatch ownership/concurrency and failure recovery are not fully verified; follow sdf-runtime ticket 02. Prior implementation notes below are historical partial evidence.

Implemented guarded Task/Attempt transitions, idempotent dispatch, retry transitions for failed/inconclusive Tasks, Attempt/Artifact/Evidence graph nodes and `GET /tasks/{id}/trace`. The trace walks both directions as needed to return the full business-to-evidence chain and preserves validation relation semantics.
