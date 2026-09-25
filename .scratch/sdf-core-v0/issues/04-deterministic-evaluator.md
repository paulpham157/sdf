# Deterministic evaluator

Type: task
Status: resolved
Blocked by: 03

Implement build/lint/test and acceptance-criteria evaluation with `PASS`, `FAIL` and `INCONCLUSIVE` outcomes. Every run creates append-only Evidence linked to the Attempt and validates or contradicts the relevant Assumption/Decision.

Acceptance: evaluator failures and timeouts become Evidence; an agent cannot self-certify success.

## Answer

Implemented deterministic allowlisted command evaluation with PASS, FAIL and INCONCLUSIVE outcomes. Every run creates at least one Evidence record, including an explicit INCONCLUSIVE record when no commands are supplied. Evidence includes exit code, stdout/stderr and confidence; unallowlisted commands are never executed.

## Resolution audit (2026-09-25)

**Acceptance criterion 1: evaluator failures and timeouts become Evidence**
- ✅ DeterministicEvaluator.evaluate() returns EvaluationResult with Evidence objects for all outcomes
- ✅ Status ∈ {PASS, FAIL, INCONCLUSIVE} mapped to Evidence.status
- ✅ Evidence includes exit_code, stdout/stderr via ArtifactRow with JSON payload
- ✅ Tests: test_evaluator.py:16-23 (FAIL outcome), test_evaluator.py:26-30 (INCONCLUSIVE)
- Evidence: sdf_core/evaluator.py, tests/test_evaluator.py:6-31

**Acceptance criterion 2: an agent cannot self-certify success**
- ✅ Unmapped passing commands cannot validate criteria: test_evaluator.py:83-92 proves arbitrary success → INCONCLUSIVE
- ✅ Missing criterion_checks for declared criteria → INCONCLUSIVE for missing check (test_evaluator.py:42-58)
- ✅ Only explicitly mapped criterion checks contribute to Evidence
- Evidence: sdf_core/evaluator.py, tests/test_evaluator.py:42-58 (missing coverage), 83-92 (unmapped pass)

**Evaluator artifact provenance (from sdf-runtime ticket 01)**
- ✅ Each Evidence record references an ArtifactRow via artifact_ref
- ✅ Artifact contains evaluator output (criterion, status, command, exit_code) as immutable JSON
- ✅ Full integration loop: test_execution.py:20-56 proves Evidence → Artifact → JSON output
- Evidence: sdf_core/execution.py, tests/test_execution.py:20-56 (full chain including artifact)

**Test results:** Full suite: 178 passed, 5 skipped. Evaluator tests: 7 passed. Integration tests confirm artifact linkage and outcome mapping.
