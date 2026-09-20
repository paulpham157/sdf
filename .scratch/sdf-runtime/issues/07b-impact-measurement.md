# 07b Impact measurement and attribution

Status: resolved
Blocked by: 01, 02 (07a no longer a blocker: escalation *policy* is out of
scope here; this ticket only records the attribution fields escalation will
populate)

## What to build and acceptance

Implement the provider-neutral measurement model from ADR-0006. Objectives
can declare a success metric, baseline, target, source, owner and measurement
window. Attempts record model/provider, latency, token/cost observations,
escalation reason and parent lineage. Evidence records criterion, confidence,
artifact reference and measurement time.

Expose a deterministic v0 scorecard for first-attempt success, accepted-task
rate, evidence coverage, trace completeness, time to accepted Evidence,
escalation rate, cost per accepted Task and escalation exhaustion. Keep local
Evidence separate from production business impact; do not invent causal
impact without an observed outcome against its baseline.

## Verification

Cover missing baseline/target, local-only Evidence, production outcome
attribution, cost aggregation and attempt lineage. Record which metrics are
synthetic versus live.

## Implemented

- `sdf_core/impact.py`: pure domain model. `MetricDeclaration` (rejects
  target==baseline, non-positive window). `measure_objective` returns
  `undeclared` / `unmeasured` / `synthetic_only` / `measured`; only a live
  observation inside the declared window sets `claim_allowed=True`.
  `build_scorecard` computes the 8 v0 metrics from `ScorecardFacts`, each
  `Metric` carrying a `basis` (`synthetic`/`live`/`mixed`/`none`) and `None`
  rather than a misleading zero when a denominator is empty or a bill is
  only partially recorded.
- `sdf_core/db.py` / `alembic/versions/0003_impact_attribution.py`: Attempt
  lineage/cost/tier/mode fields, Evidence mode, `objective_metrics` and
  `outcome_observations` tables. (Fixed a pre-existing bug found while
  verifying the migration chain: `0001_initial.py` already created
  `evidence.criterion` inline, making `0002_evidence_criterion.py` fail
  `duplicate column` on a from-scratch `alembic upgrade head`; removed the
  inline column from 0001 so the chain runs end to end.)
- `sdf_core/impact_repository.py`: pure DB row -> fact translation, no
  metric math. Recovers Task->Objective linkage via the existing
  `DecisionEdgeRow(relation="implements")` edge (Task has no `objective_id`
  column) and `has_artifact` via `ArtifactRow` existence.
- `sdf_core/api.py`: `POST /objectives/{id}/metric` (409 on re-declare, 422
  on invalid declaration), `POST /objectives/{id}/observations`,
  `GET /objectives/{id}/impact`, `GET /scorecard`.
- Tests: `tests/test_impact.py` (25, pure domain), `tests/test_impact_api.py`
  (15, API + repository wiring). Full suite: 80 passed, 1 skipped
  (Postgres integration, no server running).

## Deliberately out of scope

- `sdf_core/execution.py` does not yet populate `model_tier`/`cost_usd`/
  escalation fields on Attempt — that's 07a's escalation *policy*, which
  still queues separately. The columns exist and default to null/synthetic
  so 07a can fill them in without another migration.
- No production outcome ingestion pipeline; `POST .../observations` is the
  manual attribution entry point until one exists.
