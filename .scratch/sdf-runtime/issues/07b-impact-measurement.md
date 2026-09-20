# 07b Impact measurement and attribution

Status: resolved
Blocked by: 01, 02, 07a

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

## Answer

Implemented provider-neutral `ObjectiveMetric`/`ImpactMeasurement` attribution,
local-versus-production guardrails, deterministic v0 `Scorecard`, and
Attempt/Evidence measurement fields (provider, latency, token counts, cost,
escalation reason, measurement time). Local tests cover missing baseline and
target, production attribution, evidence/trace coverage, escalation rate and
cost-per-accepted-task. No production business impact is claimed from local
evaluator evidence.

Follow-up persistence is now wired: `/objectives` stores the declared metric
contract in objective metadata, and `/tasks/{id}/run` accepts and returns
provider, latency, token, cost and escalation observations on Attempt rows.
API regression coverage verifies round-trip fields; production attribution is
still correctly gated on measured production outcomes.

Scorecard `trace_completeness` now requires the full persisted chain for each
Attempt: `implements` Task, at least one artifact `measures` Attempt, and
Evidence `measures` Attempt. Evidence alone or an unrelated edge no longer
counts as a complete trace.
