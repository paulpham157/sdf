# 07b Impact measurement and attribution

Status: ready-for-agent
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
