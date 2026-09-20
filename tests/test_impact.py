from datetime import timedelta

import pytest

from sdf_core.impact import (
    AttemptFact,
    EvidenceFact,
    EvidenceMode,
    MetricDeclaration,
    OutcomeObservation,
    ScorecardFacts,
    TaskFact,
    build_scorecard,
    measure_objective,
)
from sdf_core.model import utcnow


T0 = utcnow()


def declaration(**overrides) -> MetricDeclaration:
    kwargs = dict(
        objective_id="OBJ-1",
        metric_name="incident_minutes",
        baseline=120.0,
        target=60.0,
        unit="minutes",
        source="pagerduty",
        owner="sre",
        measurement_window_days=30,
        declared_at=T0,
    )
    kwargs.update(overrides)
    return MetricDeclaration(**kwargs)


def observation(**overrides) -> OutcomeObservation:
    kwargs = dict(
        objective_id="OBJ-1",
        metric_name="incident_minutes",
        value=90.0,
        observed_at=T0 + timedelta(days=10),
        source="pagerduty",
        mode=EvidenceMode.LIVE,
    )
    kwargs.update(overrides)
    return OutcomeObservation(**kwargs)


def task(**overrides) -> TaskFact:
    kwargs = dict(
        task_id="TASK-1",
        created_at=T0,
        status="succeeded",
        acceptance_criteria=("tests pass",),
        objective_id="OBJ-1",
        has_artifact=True,
    )
    kwargs.update(overrides)
    return TaskFact(**kwargs)


def attempt(**overrides) -> AttemptFact:
    kwargs = dict(
        attempt_id="ATTEMPT-1",
        task_id="TASK-1",
        created_at=T0,
        parent_attempt_id=None,
        model_tier="basic",
        cost_usd=2.0,
        escalation_reason=None,
        outcome=None,
        mode=EvidenceMode.SYNTHETIC,
    )
    kwargs.update(overrides)
    return AttemptFact(**kwargs)


def evidence(**overrides) -> EvidenceFact:
    kwargs = dict(
        evidence_id="EVIDENCE-1",
        attempt_id="ATTEMPT-1",
        status="PASS",
        criterion="tests pass",
        measured_at=T0 + timedelta(seconds=30),
        mode=EvidenceMode.SYNTHETIC,
    )
    kwargs.update(overrides)
    return EvidenceFact(**kwargs)


def scorecard(tasks=None, attempts=None, evidences=None):
    return build_scorecard(
        ScorecardFacts(
            tasks=tuple(tasks if tasks is not None else [task()]),
            attempts=tuple(attempts if attempts is not None else [attempt()]),
            evidence=tuple(evidences if evidences is not None else [evidence()]),
        )
    )


def metric(card, name):
    return card.metric(name)


# --- Declaration validation -------------------------------------------------


def test_declaration_derives_direction_from_baseline_and_target():
    assert declaration().direction == "decrease"
    assert declaration(baseline=10.0, target=40.0).direction == "increase"


def test_declaration_rejects_target_equal_to_baseline():
    with pytest.raises(ValueError, match="target must differ from baseline"):
        declaration(target=120.0)


def test_declaration_rejects_non_positive_window():
    with pytest.raises(ValueError, match="measurement window"):
        declaration(measurement_window_days=0)


# --- Business impact attribution -------------------------------------------


def test_objective_without_declaration_makes_no_impact_claim():
    impact = measure_objective("OBJ-1", declaration=None, observations=())
    assert impact.status == "undeclared"
    assert impact.claim_allowed is False
    assert impact.delta is None


def test_declared_objective_without_observation_is_unmeasured():
    impact = measure_objective("OBJ-1", declaration=declaration(), observations=())
    assert impact.status == "unmeasured"
    assert impact.claim_allowed is False
    assert impact.baseline == 120.0


def test_synthetic_observation_never_supports_a_business_claim():
    impact = measure_objective(
        "OBJ-1",
        declaration=declaration(),
        observations=(observation(mode=EvidenceMode.SYNTHETIC),),
    )
    assert impact.status == "synthetic_only"
    assert impact.claim_allowed is False
    assert impact.delta is None


def test_observation_outside_the_window_does_not_count():
    impact = measure_objective(
        "OBJ-1",
        declaration=declaration(),
        observations=(observation(observed_at=T0 + timedelta(days=31)),),
    )
    assert impact.status == "unmeasured"
    assert impact.claim_allowed is False


def test_live_observation_in_window_attributes_improvement():
    impact = measure_objective("OBJ-1", declaration=declaration(), observations=(observation(),))
    assert impact.status == "measured"
    assert impact.claim_allowed is True
    assert impact.observed == 90.0
    # A decrease metric improves by 30 minutes against a 60-minute gap.
    assert impact.delta == 30.0
    assert impact.target_attainment == pytest.approx(0.5)


def test_regression_against_baseline_is_reported_as_negative_delta():
    impact = measure_objective(
        "OBJ-1", declaration=declaration(), observations=(observation(value=150.0),)
    )
    assert impact.delta == -30.0
    assert impact.target_attainment == pytest.approx(-0.5)


def test_increase_metric_uses_the_opposite_direction():
    impact = measure_objective(
        "OBJ-1",
        declaration=declaration(baseline=100.0, target=200.0),
        observations=(observation(value=150.0),),
    )
    assert impact.delta == 50.0
    assert impact.target_attainment == pytest.approx(0.5)


def test_latest_live_observation_in_window_wins():
    impact = measure_objective(
        "OBJ-1",
        declaration=declaration(),
        observations=(
            observation(value=110.0, observed_at=T0 + timedelta(days=2)),
            observation(value=70.0, observed_at=T0 + timedelta(days=20)),
        ),
    )
    assert impact.observed == 70.0


def test_observations_for_another_metric_are_ignored():
    impact = measure_objective(
        "OBJ-1",
        declaration=declaration(),
        observations=(observation(metric_name="deploy_count"),),
    )
    assert impact.status == "unmeasured"


# --- Scorecard --------------------------------------------------------------


def test_scorecard_reports_a_clean_single_attempt_task():
    card = scorecard()
    assert metric(card, "first_attempt_success_rate").value == 1.0
    assert metric(card, "accepted_task_rate").value == 1.0
    assert metric(card, "evidence_coverage").value == 1.0
    assert metric(card, "trace_completeness").value == 1.0
    assert metric(card, "escalation_rate").value == 0.0
    assert metric(card, "cost_per_accepted_task").value == 2.0
    assert metric(card, "time_to_accepted_evidence").value == pytest.approx(30.0)


def test_empty_scorecard_reports_none_rather_than_zero():
    card = scorecard(tasks=[], attempts=[], evidences=[])
    for item in card.metrics:
        assert item.value is None, item.name
        assert item.basis == "none"


def test_uncovered_criterion_is_not_counted_as_coverage():
    card = scorecard(
        tasks=[task(acceptance_criteria=("tests pass", "lint clean"))],
        evidences=[evidence()],
    )
    assert metric(card, "evidence_coverage").value == pytest.approx(0.5)
    assert metric(card, "accepted_task_rate").value == 0.0


def test_inconclusive_evidence_is_not_coverage_and_not_acceptance():
    card = scorecard(evidences=[evidence(status="INCONCLUSIVE")])
    assert metric(card, "evidence_coverage").value == 0.0
    assert metric(card, "accepted_task_rate").value == 0.0


def test_failing_evidence_counts_as_coverage_but_not_acceptance():
    card = scorecard(evidences=[evidence(status="FAIL")])
    assert metric(card, "evidence_coverage").value == 1.0
    assert metric(card, "accepted_task_rate").value == 0.0


def test_task_without_objective_breaks_trace_completeness():
    card = scorecard(tasks=[task(objective_id=None)])
    assert metric(card, "trace_completeness").value == 0.0


def test_task_without_artifact_breaks_trace_completeness():
    card = scorecard(tasks=[task(has_artifact=False)])
    assert metric(card, "trace_completeness").value == 0.0


def test_escalated_second_attempt_is_not_first_attempt_success():
    card = scorecard(
        attempts=[
            attempt(attempt_id="ATTEMPT-1", cost_usd=2.0),
            attempt(
                attempt_id="ATTEMPT-2",
                parent_attempt_id="ATTEMPT-1",
                model_tier="medium",
                cost_usd=5.0,
                escalation_reason="FAIL",
                created_at=T0 + timedelta(minutes=1),
            ),
        ],
        evidences=[
            evidence(evidence_id="EVIDENCE-1", attempt_id="ATTEMPT-1", status="FAIL"),
            evidence(
                evidence_id="EVIDENCE-2",
                attempt_id="ATTEMPT-2",
                status="PASS",
                measured_at=T0 + timedelta(minutes=2),
            ),
        ],
    )
    assert metric(card, "first_attempt_success_rate").value == 0.0
    assert metric(card, "accepted_task_rate").value == 1.0
    assert metric(card, "escalation_rate").value == 1.0
    # Failed attempts still cost money; acceptance carries the whole bill.
    assert metric(card, "cost_per_accepted_task").value == 7.0
    assert metric(card, "time_to_accepted_evidence").value == pytest.approx(120.0)


def test_exhaustion_rate_is_measured_against_escalated_tasks_only():
    card = scorecard(
        attempts=[
            attempt(attempt_id="ATTEMPT-1"),
            attempt(
                attempt_id="ATTEMPT-2",
                parent_attempt_id="ATTEMPT-1",
                outcome="escalation_exhausted",
            ),
        ],
        evidences=[evidence(status="FAIL")],
    )
    assert metric(card, "escalation_exhaustion_rate").value == 1.0
    assert metric(card, "accepted_task_rate").value == 0.0


def test_task_that_never_escalated_is_excluded_from_exhaustion_denominator():
    card = scorecard()
    assert metric(card, "escalation_exhaustion_rate").value is None


def test_partial_cost_recording_suppresses_the_cost_metric():
    card = scorecard(
        attempts=[
            attempt(attempt_id="ATTEMPT-1", cost_usd=2.0),
            attempt(attempt_id="ATTEMPT-2", cost_usd=None, created_at=T0 + timedelta(minutes=1)),
        ]
    )
    assert metric(card, "cost_per_accepted_task").value is None
    assert metric(card, "cost_per_accepted_task").note == "incomplete cost attribution"


def test_metric_basis_distinguishes_synthetic_from_live():
    card = scorecard()
    assert metric(card, "accepted_task_rate").basis == "synthetic"
    live = scorecard(
        attempts=[attempt(mode=EvidenceMode.LIVE)],
        evidences=[evidence(mode=EvidenceMode.LIVE)],
    )
    assert metric(live, "accepted_task_rate").basis == "live"
    mixed = scorecard(
        tasks=[task(), task(task_id="TASK-2")],
        attempts=[
            attempt(),
            attempt(attempt_id="ATTEMPT-2", task_id="TASK-2", mode=EvidenceMode.LIVE),
        ],
        evidences=[
            evidence(),
            evidence(
                evidence_id="EVIDENCE-2", attempt_id="ATTEMPT-2", mode=EvidenceMode.LIVE
            ),
        ],
    )
    assert metric(mixed, "accepted_task_rate").basis == "mixed"


def test_scorecard_never_reports_business_impact():
    names = {item.name for item in scorecard().metrics}
    assert "business_impact" not in names
    assert names == {
        "first_attempt_success_rate",
        "accepted_task_rate",
        "evidence_coverage",
        "trace_completeness",
        "time_to_accepted_evidence",
        "escalation_rate",
        "cost_per_accepted_task",
        "escalation_exhaustion_rate",
    }
