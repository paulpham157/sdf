# 07a Model policy and escalation

Status: resolved
Blocked by: 01, 02

## What to build and acceptance

Define a provider-neutral model policy for `basic`, `medium` and `high` tiers.
Select the lowest suitable tier for a Task, then create a new Attempt with
`parent_attempt_id` when verified `FAIL`, `INCONCLUSIVE`, timeout or
policy/tool failure warrants escalation. Preserve prior Evidence and enforce
`max_attempts = 3`, maximum tier and an explicit hard cost ceiling. Reaching a
bound yields `escalation_exhausted`, never an unbounded retry.

The first slice is deterministic and local: Model Profile, Escalation Policy,
Attempt lineage and budget decisions. Do not bind a real provider or treat an
agent's self-report as an escalation signal.

## Verification

Cover PASS, FAIL, INCONCLUSIVE, timeout, policy failure, budget exhaustion,
maximum-tier exhaustion and stable parent-child Attempt lineage. Record local
versus live evidence before resolution.

## Answer

Implemented provider-neutral `ModelTier`, bounded `EscalationPolicy`,
`create_escalated_attempt`, Attempt `parent_attempt_id`/tier/cost metadata and
ExecutionService lineage wiring. PASS never escalates; verified failure,
inconclusive, timeout and policy/tool failure can escalate until attempt,
tier or cost bounds are reached. Covered by deterministic local tests.
