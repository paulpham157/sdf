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
