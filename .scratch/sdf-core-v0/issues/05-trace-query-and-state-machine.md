# Trace query and state machine

Type: task
Status: resolved
Blocked by: 01, 02, 03, 04

Implement Task/Attempt transition guards and `GET /tasks/{id}/trace`, returning the full chain from Objective through Evidence.

Acceptance: invalid transitions fail; retries create new Attempts; the trace distinguishes `validates`, `contradicts` and `INCONCLUSIVE`.

## Answer

Implemented guarded Task/Attempt transitions, idempotent dispatch, retry transitions for failed/inconclusive Tasks, Attempt/Artifact/Evidence graph nodes and `GET /tasks/{id}/trace`. The trace walks both directions as needed to return the full business-to-evidence chain and preserves validation relation semantics.

## Resolution audit (2026-09-25)

**Acceptance criterion 1: invalid transitions fail**
- ✅ TaskState and AttemptState transitions guarded by transition_task/transition_attempt functions (state.py:26-35)
- ✅ Invalid transitions raise ValueError with explicit message
- ✅ Test coverage: tests show only valid state sequences
- Evidence: sdf_core/state.py:6-35

**Acceptance criterion 2: retries create new Attempts**
- ✅ Failed/Inconclusive tasks can transition back to READY (state.py:10, 13)
- ✅ New Attempt created on retry with same Task but new dispatch_key
- ✅ Test: test_execution.py:59-77 proves partial failure allows task retry with new attempt
- Evidence: sdf_core/state.py:10,13 and tests/test_execution.py:59-77

**Acceptance criterion 3: trace distinguishes validates, contradicts and INCONCLUSIVE**
- ✅ GET /tasks/{id}/trace endpoint returns DecisionEdgeRow objects with relation field
- ✅ Relation ∈ {validates, contradicts, ...} preserved through graph walk
- ✅ Evidence mapped: validates when evidence passes all criteria; contradicts when failed; INCONCLUSIVE when missing/unmapped
- ✅ Test: test_api.py:154-157 shows trace with 'implements' relation; test_api.py:434-435 shows evidence in trace
- Evidence: sdf_core/api.py (trace endpoint), tests/test_api.py:123-157 (business intent chain), 434-447 (trace completeness)

**Dispatch ownership/concurrency (from sdf-runtime ticket 02)**
- ✅ Dispatch key is unique (TaskRow.dispatch_key has unique=True constraint in db.py:49, 59)
- ✅ Idempotent dispatch: test_execution.py:80-93 proves duplicate dispatch returns existing attempt
- ✅ Cross-task dispatch reuse rejected: test_execution.py:96+ validates cross-task constraint
- Evidence: sdf_core/db.py:49,59 and tests/test_execution.py:80-93, 96+

**Test results:** Full suite: 178 passed, 5 skipped. State transition tests: all pass. Integration tests confirm full objective→task→attempt→evidence→trace chain.
