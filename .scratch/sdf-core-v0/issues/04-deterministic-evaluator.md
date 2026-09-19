# Deterministic evaluator

Type: task
Status: ready-for-agent
Blocked by: 03

Implement build/lint/test and acceptance-criteria evaluation with `PASS`, `FAIL` and `INCONCLUSIVE` outcomes. Every run creates append-only Evidence linked to the Attempt and validates or contradicts the relevant Assumption/Decision.

Acceptance: evaluator failures and timeouts become Evidence; an agent cannot self-certify success.

## Answer

Current audit: Reopened: acceptance coverage and evaluator artifact provenance remain incomplete; follow sdf-runtime ticket 01. Prior implementation notes below are historical partial evidence.

Implemented deterministic allowlisted command evaluation with PASS, FAIL and INCONCLUSIVE outcomes. Every run creates at least one Evidence record, including an explicit INCONCLUSIVE record when no commands are supplied. Evidence includes exit code, stdout/stderr and confidence; unallowlisted commands are never executed.
