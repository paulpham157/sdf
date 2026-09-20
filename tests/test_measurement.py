from datetime import datetime, timezone

import pytest

from sdf_core.measurement import AttemptObservation, ImpactMeasurement, ObjectiveMetric, build_scorecard, build_scorecard_from_db
from sdf_core.db import ArtifactRow, AttemptRow, Base, DecisionEdgeRow, EvidenceRow, GraphNodeRow, TaskRow, make_engine
from sdf_core.model import utcnow
from sqlalchemy.orm import sessionmaker


def test_scorecard_reports_first_attempt_success_and_cost_per_accepted_task():
    observations = [
        AttemptObservation("A1", "T1", status="succeeded", attempt_number=1, evidence_count=2, trace_complete=True, cost_usd=0.4, accepted_evidence_seconds=4.0),
        AttemptObservation("A2", "T2", status="failed", attempt_number=1, evidence_count=1, trace_complete=True, cost_usd=0.5, escalated=True),
        AttemptObservation("A3", "T2", status="succeeded", attempt_number=2, evidence_count=2, trace_complete=True, cost_usd=0.8, escalated=True, accepted_evidence_seconds=8.0),
    ]

    scorecard = build_scorecard(observations)

    assert scorecard.first_attempt_success_rate == pytest.approx(0.5)
    assert scorecard.accepted_task_rate == pytest.approx(1.0)
    assert scorecard.evidence_coverage == pytest.approx(1.0)
    assert scorecard.trace_completeness == pytest.approx(1.0)
    assert scorecard.cost_per_accepted_task == pytest.approx(0.85)
    assert scorecard.escalation_rate == pytest.approx(0.5)
    assert scorecard.time_to_accepted_evidence_seconds == pytest.approx(6.0)


def test_metric_requires_baseline_and_production_outcome_for_impact_attribution():
    metric = ObjectiveMetric("deployment lead time", baseline=60.0, target=30.0, source="prod-metrics", owner="product", measurement_window="30d")
    outcome = ImpactMeasurement(
        objective_id="OBJ-1", value=28.0, source="prod-metrics", environment="production",
        measured_at=datetime.now(timezone.utc), baseline=60.0,
    )

    attribution = metric.attribute(outcome)

    assert attribution.is_attributable is True
    assert attribution.delta == pytest.approx(-32.0)
    assert attribution.target_met is True


def test_local_evidence_or_missing_baseline_cannot_claim_impact():
    missing_baseline = ObjectiveMetric("quality", baseline=None, target=1.0, source="local", owner="qa", measurement_window="run")
    local = ImpactMeasurement(
        objective_id="OBJ-2", value=1.0, source="local", environment="local",
        measured_at=datetime.now(timezone.utc), baseline=None,
    )

    result = missing_baseline.attribute(local)

    assert result.is_attributable is False
    assert "baseline" in result.reason


def test_db_trace_completeness_requires_task_artifact_and_evidence_chain():
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        db.add(TaskRow(
            id="TASK-TRACE-METRIC", title="trace", status="succeeded",
            idempotency_key="trace-metric", acceptance_criteria=[], created_at=utcnow(),
        ))
        db.add(AttemptRow(
            id="ATTEMPT-TRACE-METRIC", task_id="TASK-TRACE-METRIC", status="completed",
            dispatch_key="dispatch-trace-metric", agent="test", created_at=utcnow(),
        ))
        db.add(EvidenceRow(
            id="EVIDENCE-TRACE-METRIC", attempt_id="ATTEMPT-TRACE-METRIC", kind="test",
            status="PASS", command="check", exit_code=0, artifact_ref=None,
            confidence=1.0, criterion="check", measured_at=utcnow(), created_at=utcnow(),
        ))
        db.add_all([
            GraphNodeRow(id="ATTEMPT-TRACE-METRIC", kind="attempt", title="attempt", source="test", owner="test", confidence=1.0, created_at=utcnow()),
            GraphNodeRow(id="EVIDENCE-TRACE-METRIC", kind="evidence", title="evidence", source="test", owner="test", confidence=1.0, created_at=utcnow()),
        ])
        db.add_all([
            DecisionEdgeRow(
                source_kind="attempt", source_id="ATTEMPT-TRACE-METRIC", target_kind="task",
                target_id="TASK-TRACE-METRIC", relation="implements", source="test", owner="test",
                confidence=1.0, created_at=utcnow(),
            ),
            DecisionEdgeRow(
                source_kind="evidence", source_id="EVIDENCE-TRACE-METRIC", target_kind="attempt",
                target_id="ATTEMPT-TRACE-METRIC", relation="measures", source="test", owner="test",
                confidence=1.0, created_at=utcnow(),
            ),
        ])
        db.commit()
        assert build_scorecard_from_db(db).trace_completeness == 0.0

        db.add(ArtifactRow(
            id="ARTIFACT-TRACE-METRIC", attempt_id="ATTEMPT-TRACE-METRIC", kind="diff",
            uri="/tmp/diff", sha256="0" * 64, size_bytes=1, created_at=utcnow(),
        ))
        db.add(GraphNodeRow(id="ARTIFACT-TRACE-METRIC", kind="artifact", title="diff", source="test", owner="test", confidence=1.0, created_at=utcnow()))
        db.add(DecisionEdgeRow(
            source_kind="artifact", source_id="ARTIFACT-TRACE-METRIC", target_kind="attempt",
            target_id="ATTEMPT-TRACE-METRIC", relation="measures", source="test", owner="test",
            confidence=1.0, created_at=utcnow(),
        ))
        db.commit()
        assert build_scorecard_from_db(db).trace_completeness == 1.0
