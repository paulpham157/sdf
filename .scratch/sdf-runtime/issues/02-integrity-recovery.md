# 02 Integrity dispatch and recovery

Status: claimed
Blocked by: 01

## What to build and acceptance

Reject dangling graph references; enforce append-only Evidence; dispatch ownership and concurrency are checked; workspace/evaluation failures terminate attempts without losing audit; retries never duplicate execution.

## Verification

Review carry-forward: criterion identity currently lives in graph metadata plus immutable evaluator artifact. Enforce consistency or migrate it into Evidence; evidence lookup must not silently lose it when graph metadata is absent/modified.

Record behavior checks and exact local/live boundary before resolution. Follow ADR-0004 and the parent spec. Review before commit.
