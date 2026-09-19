from pathlib import Path

from sdf_core.evaluator import DeterministicEvaluator


def test_evaluator_emits_pass_evidence_for_successful_command(tmp_path: Path):
    result = DeterministicEvaluator().evaluate(
        attempt_id="ATTEMPT-001", workspace=tmp_path,
        commands=[["python", "-c", "print('ok')"]],
    )
    assert result.status == "PASS"
    assert result.evidence[0].status == "PASS"
    assert result.evidence[0].exit_code == 0


def test_evaluator_emits_fail_evidence_for_failed_command(tmp_path: Path):
    result = DeterministicEvaluator().evaluate(
        attempt_id="ATTEMPT-002", workspace=tmp_path,
        commands=[["python", "-c", "raise SystemExit(2)"]],
    )
    assert result.status == "FAIL"
    assert result.evidence[0].status == "FAIL"
    assert result.evidence[0].exit_code == 2


def test_evaluator_without_commands_is_inconclusive(tmp_path: Path):
    result = DeterministicEvaluator().evaluate(attempt_id="ATTEMPT-EMPTY", workspace=tmp_path, commands=[])
    assert result.status == "INCONCLUSIVE"
    assert len(result.evidence) == 1
    assert result.evidence[0].status == "INCONCLUSIVE"


def test_evaluator_does_not_run_unallowlisted_executable(tmp_path: Path):
    result = DeterministicEvaluator().evaluate(
        attempt_id="ATTEMPT-003", workspace=tmp_path,
        commands=[["rm", "-rf", "."]],
    )
    assert result.status == "INCONCLUSIVE"
    assert result.evidence[0].status == "INCONCLUSIVE"
