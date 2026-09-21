"""Translates DB rows into the pure fact/value objects `impact.py` computes on.

No metric math lives here. This module only reshapes rows.
"""

from __future__ import annotations

from sqlalchemy import select
from sqlalchemy.orm import Session

from .db import (
    ArtifactRow,
    AttemptRow,
    DecisionEdgeRow,
    EvidenceRow,
    ObjectiveMetricRow,
    OutcomeObservationRow,
    TaskRow,
)
from .impact import (
    AttemptFact,
    EvidenceFact,
    EvidenceMode,
    MetricDeclaration,
    OutcomeObservation,
    ScorecardFacts,
    TaskFact,
)


def load_metric_declaration(db: Session, objective_id: str) -> MetricDeclaration | None:
    row = db.get(ObjectiveMetricRow, objective_id)
    if row is None:
        return None
    return MetricDeclaration(
        objective_id=row.objective_id,
        metric_name=row.metric_name,
        baseline=row.baseline,
        target=row.target,
        unit=row.unit,
        source=row.source,
        owner=row.owner,
        measurement_window_days=row.measurement_window_days,
        declared_at=row.declared_at,
    )


def load_observations(db: Session, objective_id: str) -> tuple[OutcomeObservation, ...]:
    rows = db.scalars(select(OutcomeObservationRow).where(OutcomeObservationRow.objective_id == objective_id))
    return tuple(
        OutcomeObservation(
            objective_id=row.objective_id,
            metric_name=row.metric_name,
            value=row.value,
            observed_at=row.observed_at,
            source=row.source,
            mode=EvidenceMode(row.mode),
        )
        for row in rows
    )


def _task_objective_ids(db: Session) -> dict[str, str]:
    edges = db.scalars(
        select(DecisionEdgeRow).where(
            DecisionEdgeRow.target_kind == "task",
            DecisionEdgeRow.source_kind == "objective",
            DecisionEdgeRow.relation == "implements",
        )
    )
    return {edge.target_id: edge.source_id for edge in edges}


def _tasks_with_artifacts(db: Session) -> set[str]:
    attempt_task = dict(db.execute(select(AttemptRow.id, AttemptRow.task_id)).all())
    attempts_with_artifacts = set(db.scalars(select(ArtifactRow.attempt_id).distinct()))
    return {attempt_task[attempt_id] for attempt_id in attempts_with_artifacts if attempt_id in attempt_task}


def load_scorecard_facts(db: Session) -> ScorecardFacts:
    task_objective = _task_objective_ids(db)
    tasks_with_artifacts = _tasks_with_artifacts(db)

    tasks = tuple(
        TaskFact(
            task_id=row.id,
            created_at=row.created_at,
            status=row.status,
            acceptance_criteria=tuple(row.acceptance_criteria or ()),
            objective_id=task_objective.get(row.id),
            has_artifact=row.id in tasks_with_artifacts,
        )
        for row in db.scalars(select(TaskRow))
    )

    attempts = tuple(
        AttemptFact(
            attempt_id=row.id,
            task_id=row.task_id,
            created_at=row.created_at,
            parent_attempt_id=row.parent_attempt_id,
            model_tier=row.model_tier,
            cost_usd=row.cost_usd,
            escalation_reason=row.escalation_reason,
            outcome=row.outcome,
            mode=EvidenceMode(row.evidence_mode),
        )
        for row in db.scalars(select(AttemptRow))
    )

    evidence = tuple(
        EvidenceFact(
            evidence_id=row.id,
            attempt_id=row.attempt_id,
            status=row.status,
            criterion=row.criterion,
            measured_at=row.created_at,
            mode=EvidenceMode(row.evidence_mode),
        )
        for row in db.scalars(select(EvidenceRow))
    )

    return ScorecardFacts(tasks=tasks, attempts=attempts, evidence=evidence)
