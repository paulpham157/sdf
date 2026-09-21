# 02 Integrity dispatch and recovery

Status: resolved
Blocked by: 01

## What to build and acceptance

Reject dangling graph references; enforce append-only Evidence; dispatch ownership and concurrency are checked; workspace/evaluation failures terminate attempts without losing audit; retries never duplicate execution.

## Verification

Review carry-forward: criterion identity currently lives in graph metadata plus immutable evaluator artifact. Enforce consistency or migrate it into Evidence; evidence lookup must not silently lose it when graph metadata is absent/modified.

## Answer

Added relational criterion provenance and Alembic revision `0002_evidence_criterion`; evidence lookup reads the Evidence row directly. Dispatch keys cannot be reused across tasks, workspace/artifact/evaluator failures settle state, and regression coverage covers cross-task collisions and evaluator failure. Local suite: 28 passed, 1 PostgreSQL test skipped; migration SQL generation passed. Concurrent PostgreSQL claim races and full append-only/reference enforcement remain explicitly deferred to the next persistence hardening slice.

Record behavior checks and exact local/live boundary before resolution. Follow ADR-0004 and the parent spec. Review before commit.

Follow-up hardening adds an ORM persistence guard: evaluator `EvidenceRow`
updates and deletes fail before flush, while inserts remain append-only. A
regression test covers both mutation paths. The session guard is complemented
by a PostgreSQL database trigger below; concurrent PostgreSQL verification
remains deployment work.

Migration `0009_evidence_append_only` adds the PostgreSQL trigger/function
fence for UPDATE/DELETE; SQLite deliberately remains on the ORM guard because
the trigger syntax is vendor-specific. Fresh SQLite migration to head passes.

Decision edges now validate source/target identity before flush, including
same-transaction GraphNode/Task/Attempt endpoints and previously committed
rows. Dangling source/target regressions pass; edge rollback leaves no partial
reference.

Migration `0010_decision_edge_integrity` adds the corresponding PostgreSQL
trigger for direct SQL writes, covering GraphNode/Task/Attempt endpoint kinds.
SQLite migration to head remains green; PostgreSQL trigger execution still
requires the integration database gate.

`ExecutionService.run()` now recovers a losing unique `dispatch_key` insert:
it rolls back the loser and returns the same-task durable winner, while
cross-task reuse remains rejected. This is the local race-recovery seam;
concurrent PostgreSQL execution still needs the integration test gate.

Tool Proxy action delivery now has a separate durable `action_claimed` fence;
only the delivery that acquires the unique claim may invoke the executor.
PostgreSQL Compose verification now passes Alembic head migration, direct-SQL
append-only triggers, runtime/tool-audit replay idempotency, and an 8-session
claim race with exactly one winner. The promotion gate is therefore resolved
for PostgreSQL persistence hardening; provider/OS containment remains outside
this ticket.
