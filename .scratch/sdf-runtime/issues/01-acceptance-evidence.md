# 01 Acceptance criteria and evaluator provenance

Status: resolved
Blocked by: None

## What to build and acceptance

Each criterion maps explicitly to a deterministic check; missing coverage is INCONCLUSIVE. Evidence references evaluator output artifacts, never agent logs. Mixed FAIL/INCONCLUSIVE is order independent; arbitrary command success must not auto-validate business assumptions.

## Verification

Record behavior checks and exact local/live boundary before resolution. Follow ADR-0004 and the parent spec. Review before commit.

## Answer

Explicit criterion checks, missing coverage INCONCLUSIVE, order-independent outcome aggregation and separate evaluator output artifacts implemented. Coordinator reproduced and fixed omitted-mapping HTTP compatibility and partial-success validation bugs with red/green regressions. Artifact payload and validation reference coverage added. Final local suite: 26 passed, 1 PostgreSQL test skipped; no live provider or current PostgreSQL verification claimed. Standards review: no mandatory breaches; Spec blocker fixed. Metadata integrity/recovery follow-up is tracked in ticket 02.
