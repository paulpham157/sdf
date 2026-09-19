# Objective to task API

Type: task
Status: resolved
Blocked by: 01

Implement creation endpoints for BusinessContext, Objective, Assumption, Constraint, Decision/ADR, Requirement and Task, preserving graph edges and idempotency.

Acceptance: duplicate request keys return the original resource and the trace contains the complete business-to-task chain.

## Answer

Implemented FastAPI creation endpoints for BusinessContext, Objective, Assumption, Constraint, Decision/ADR, Requirement and Task plus `GET /tasks/{id}/trace` and `GET /evidence/{id}`. Task creation is idempotent by `idempotency_key`; the trace returns the business chain, Attempt, Artifact and Evidence validation edges. Covered by API integration tests.
