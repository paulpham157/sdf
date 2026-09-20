from datetime import datetime, timezone
from math import inf, nan

import pytest

from sdf_core.measurement import AttemptObservation, ImpactMeasurement, ObjectiveMetric, build_scorecard


def test_empty_scorecard_is_deterministic_and_does_not_claim_time_or_cost():
    scorecard = build_scorecard(())

    assert scorecard.as_dict() == {
        "first_attempt_success_rate": 0.0,
        "accepted_task_rate": 0.0,
        "evidence_coverage": 0.0,
        "trace_completeness": 0.0,
        "time_to_accepted_evidence_seconds": None,
        "escalation_rate": 0.0,
        "cost_per_accepted_task": None,
        "escalation_exhaustion_rate": 0.0,
    }


@pytest.mark.parametrize(
    ("field_name", "value"),
    [
        ("baseline", nan),
        ("target", inf),
    ],
)
def test_objective_metric_rejects_non_finite_targets(field_name, value):
    values = {
        "success_metric": "lead time",
        "baseline": 60.0,
        "target": 30.0,
        "source": "prod-metrics",
        "owner": "product",
        "measurement_window": "30d",
    }
    values[field_name] = value

    with pytest.raises(ValueError, match=field_name):
        ObjectiveMetric(**values)


def test_measurement_contract_rejects_non_finite_or_negative_values():
    with pytest.raises(ValueError, match="accepted_evidence_seconds"):
        AttemptObservation(
            "A1", "T1", "succeeded", 1, 1, True, 0.1, accepted_evidence_seconds=-1.0,
        )
    with pytest.raises(ValueError, match="cost_usd"):
        AttemptObservation("A2", "T2", "failed", 1, 0, False, nan)
    with pytest.raises(ValueError, match="value"):
        ImpactMeasurement(
            objective_id="OBJ-1", value=inf, source="prod-metrics", environment="production",
            measured_at=datetime.now(timezone.utc), baseline=60.0,
        )


def test_production_attribution_rejects_mismatched_source_and_baseline():
    metric = ObjectiveMetric(
        "deployment lead time", baseline=60.0, target=30.0,
        source="prod-metrics", owner="product", measurement_window="30d",
    )
    source_mismatch = ImpactMeasurement(
        objective_id="OBJ-1", value=28.0, source="other-metrics", environment="production",
        measured_at=datetime.now(timezone.utc), baseline=60.0,
    )
    baseline_mismatch = ImpactMeasurement(
        objective_id="OBJ-1", value=28.0, source="prod-metrics", environment="production",
        measured_at=datetime.now(timezone.utc), baseline=61.0,
    )

    assert metric.attribute(source_mismatch).is_attributable is False
    assert "source" in metric.attribute(source_mismatch).reason
    assert metric.attribute(baseline_mismatch).is_attributable is False
    assert "baseline" in metric.attribute(baseline_mismatch).reason
