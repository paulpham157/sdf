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


def test_each_acceptance_criterion_requires_an_explicit_check(tmp_path: Path):
    result = DeterministicEvaluator().evaluate(
        attempt_id="ATTEMPT-COVERAGE",
        workspace=tmp_path,
        criteria=["health endpoint responds", "migration is reversible"],
        criterion_checks={
            "health endpoint responds": [["python", "-c", "print('ok')"]],
        },
    )

    assert result.status == "INCONCLUSIVE"
    assert {e.criterion for e in result.evidence} == {
        "health endpoint responds",
        "migration is reversible",
    }
    missing = next(e for e in result.evidence if e.criterion == "migration is reversible")
    assert missing.status == "INCONCLUSIVE"


def test_mixed_fail_and_inconclusive_is_order_independent(tmp_path: Path):
    checks = {
        "fails": [["python", "-c", "raise SystemExit(2)"]],
        "unknown": [["rm", "-rf", "."]],
    }
    first = DeterministicEvaluator().evaluate(
        attempt_id="ATTEMPT-MIXED-A",
        workspace=tmp_path,
        criteria=["fails", "unknown"],
        criterion_checks=checks,
    )
    second = DeterministicEvaluator().evaluate(
        attempt_id="ATTEMPT-MIXED-B",
        workspace=tmp_path,
        criteria=["unknown", "fails"],
        criterion_checks={"unknown": checks["unknown"], "fails": checks["fails"]},
    )

    assert first.status == "FAIL"
    assert second.status == "FAIL"


def test_unmapped_passing_command_cannot_certify_a_task(tmp_path: Path):
    result = DeterministicEvaluator().evaluate(
        attempt_id="ATTEMPT-UNMAPPED",
        workspace=tmp_path,
        criteria=["business hypothesis holds"],
        commands=[["python", "-c", "print('arbitrary pass')"]],
    )

    assert result.status == "INCONCLUSIVE"
    assert all(e.criterion is None for e in result.evidence if e.command != "<missing check>")
