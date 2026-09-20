# Impact measurement follows the decision graph

Status: accepted

SDF measures impact at four linked levels rather than treating agent activity
as value: business outcome, decision quality, engineering delivery and
execution economics/safety. Every Objective may declare a metric, baseline,
target, source, owner and measurement window; Tasks and Attempts record the
trace and cost needed to attribute change. The v0 scorecard starts with
first-attempt success, accepted-task rate, evidence coverage, trace
completeness, time to accepted Evidence, escalation rate, cost per accepted
Task and escalation exhaustion rate. A local evaluator PASS is local Evidence,
not proof of production business impact; production impact requires a measured
outcome against the declared baseline.

## Measurement layers

- Business: outcome delta against baseline, target attainment and value/cost.
- Decision: Evidence coverage, confidence, contradiction and decision reversal.
- Engineering: first-attempt success, cycle time, rework and escaped defects.
- Runtime/economics: tier success uplift, latency, cost per accepted Task,
  policy denies, timeout and cleanup failures.

## Required attribution fields

Objectives should carry `success_metric`, `baseline`, `target`, `source`,
`owner` and `measurement_window`. Attempts should carry model/provider,
latency, token/cost observations, escalation reason and parent lineage.
Evidence retains criterion, confidence, artifact reference and measurement
time. No metric may claim production impact when only static, unit, mock or
synthetic evidence exists.
