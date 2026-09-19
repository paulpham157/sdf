# End to end SDF Core vertical slice

Type: task
Status: ready-for-agent
Blocked by: 05

Add the single deterministic integration test proving Objective → Task → Attempt → native/fake agent → diff → evaluator → Evidence → graph trace.

Acceptance: the test passes against real PostgreSQL and a fake adapter; one negative case proves a failed evaluation contradicts or leaves an Assumption inconclusive.

## Answer

Current audit: Reopened: existing fixture tests do not establish the complete acceptance-bound loop or real runtime. Follow sdf-runtime tickets 01 and 07. Prior implementation notes below are historical partial evidence.

Added service and HTTP integration coverage for fixture workspace execution, immutable artifacts, evaluator Evidence, task state updates and trace traversal. Added a negative path proving evaluator failure creates a `contradicts` edge and fails the Task. Added a PostgreSQL integration test and verified the full suite against a temporary PostgreSQL 16 container.
