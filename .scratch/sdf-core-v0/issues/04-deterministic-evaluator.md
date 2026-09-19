# Deterministic evaluator

Type: task
Status: resolved
Blocked by: 03

Implement build/lint/test and acceptance-criteria evaluation with `PASS`, `FAIL` and `INCONCLUSIVE` outcomes. Every run creates append-only Evidence linked to the Attempt and validates or contradicts the relevant Assumption/Decision.

Acceptance: evaluator failures and timeouts become Evidence; an agent cannot self-certify success.

## Answer

Implemented deterministic allowlisted command evaluation with PASS, FAIL and INCONCLUSIVE outcomes. Every run creates at least one Evidence record, including an explicit INCONCLUSIVE record when no commands are supplied. Evidence includes exit code, stdout/stderr and confidence; unallowlisted commands are never executed.
