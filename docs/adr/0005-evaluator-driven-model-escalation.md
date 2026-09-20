# Evaluator-driven model escalation

Status: accepted

SDF selects models by ordered capability tiers (`basic` → `medium` → `high`),
starting with the lowest suitable tier and escalating only from verified
Evaluator outcomes such as `FAIL`, `INCONCLUSIVE`, timeout or policy/tool
failure. Each escalation creates a new Attempt linked by `parent_attempt_id`,
preserves prior Evidence, and is bounded by `max_attempts = 3`, a configured
maximum tier and an explicit hard cost ceiling. When a bound is reached the
Task becomes `escalation_exhausted`; agents cannot self-certify escalation.
Provider-specific model names remain Model Profiles rather than domain state.
