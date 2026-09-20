"""Provider-neutral impact measurement and deterministic v0 scorecard."""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from math import isfinite
from typing import Sequence

from sqlalchemy import select
from sqlalchemy.orm import Session

from .db import AttemptRow, DecisionEdgeRow, EvidenceRow, TaskRow


def _validate_finite(value: float | None, field_name: str) -> None:
    """Reject non-finite measurements before they enter a scorecard."""

    if value is not None and not isfinite(value):
        raise ValueError(f"{field_name} must be finite")


@dataclass(frozen=True, slots=True)
class AttemptObservation:
    attempt_id: str
    task_id: str
    status: str
    attempt_number: int
    evidence_count: int
    trace_complete: bool
    cost_usd: float
    accepted_evidence_seconds: float | None = None
    escalated: bool = False
    escalation_exhausted: bool = False

    def __post_init__(self) -> None:
        if not self.attempt_id.strip() or not self.task_id.strip() or not self.status.strip():
            raise ValueError("attempt and task identity fields must be non-empty")
        if self.attempt_number <= 0 or self.evidence_count < 0 or self.cost_usd < 0:
            raise ValueError("attempt number, evidence count and cost must be valid")
        _validate_finite(self.cost_usd, "cost_usd")
        _validate_finite(self.accepted_evidence_seconds, "accepted_evidence_seconds")
        if self.accepted_evidence_seconds is not None and self.accepted_evidence_seconds < 0:
            raise ValueError("accepted_evidence_seconds must not be negative")


@dataclass(frozen=True, slots=True)
class Scorecard:
    first_attempt_success_rate: float
    accepted_task_rate: float
    evidence_coverage: float
    trace_completeness: float
    time_to_accepted_evidence_seconds: float | None
    escalation_rate: float
    cost_per_accepted_task: float | None
    escalation_exhaustion_rate: float

    def as_dict(self) -> dict[str, float | None]:
        return {
            "first_attempt_success_rate": self.first_attempt_success_rate,
            "accepted_task_rate": self.accepted_task_rate,
            "evidence_coverage": self.evidence_coverage,
            "trace_completeness": self.trace_completeness,
            "time_to_accepted_evidence_seconds": self.time_to_accepted_evidence_seconds,
            "escalation_rate": self.escalation_rate,
            "cost_per_accepted_task": self.cost_per_accepted_task,
            "escalation_exhaustion_rate": self.escalation_exhaustion_rate,
        }


def build_scorecard(observations: Sequence[AttemptObservation]) -> Scorecard:
    items = tuple(observations)
    task_ids = {item.task_id for item in items}
    task_count = len(task_ids)
    accepted_tasks = {item.task_id for item in items if item.status.lower() == "succeeded"}
    first_attempts = tuple(item for item in items if item.attempt_number == 1)
    accepted_times = tuple(item.accepted_evidence_seconds for item in items if item.accepted_evidence_seconds is not None)
    escalated_tasks = {item.task_id for item in items if item.escalated}
    exhausted_tasks = {item.task_id for item in items if item.escalation_exhausted}

    def rate(numerator: int, denominator: int) -> float:
        return numerator / denominator if denominator else 0.0

    total_cost = sum(item.cost_usd for item in items)
    return Scorecard(
        first_attempt_success_rate=rate(sum(item.status.lower() == "succeeded" for item in first_attempts), len(first_attempts)),
        accepted_task_rate=rate(len(accepted_tasks), task_count),
        evidence_coverage=rate(sum(item.evidence_count > 0 for item in items), len(items)),
        trace_completeness=rate(sum(item.trace_complete for item in items), len(items)),
        time_to_accepted_evidence_seconds=(sum(accepted_times) / len(accepted_times)) if accepted_times else None,
        escalation_rate=rate(len(escalated_tasks), task_count),
        cost_per_accepted_task=(total_cost / len(accepted_tasks)) if accepted_tasks else None,
        escalation_exhaustion_rate=rate(len(exhausted_tasks), task_count),
    )


def build_scorecard_from_db(db: Session) -> Scorecard:
    """Build the deterministic v0 scorecard from persisted local observations."""

    attempts = tuple(db.scalars(select(AttemptRow).order_by(AttemptRow.task_id, AttemptRow.created_at)).all())
    tasks = {task.id: task for task in db.scalars(select(TaskRow)).all()}
    evidence = tuple(db.scalars(select(EvidenceRow)).all())
    edges = tuple(db.scalars(select(DecisionEdgeRow)).all())
    evidence_by_attempt: dict[str, list[EvidenceRow]] = {}
    for item in evidence:
        evidence_by_attempt.setdefault(item.attempt_id, []).append(item)
    attempt_numbers: dict[str, int] = {}
    observations: list[AttemptObservation] = []
    for attempt in attempts:
        attempt_numbers[attempt.task_id] = attempt_numbers.get(attempt.task_id, 0) + 1
        rows = evidence_by_attempt.get(attempt.id, [])
        statuses = {row.status for row in rows}
        if "FAIL" in statuses:
            status = "failed"
        elif rows and statuses == {"PASS"}:
            status = "succeeded"
        else:
            status = "inconclusive"
        accepted_seconds = None
        task = tasks.get(attempt.task_id)
        measured = [row.measured_at for row in rows if row.status == "PASS" and row.measured_at is not None]
        if task is not None and task.status == "succeeded" and measured:
            try:
                accepted_seconds = max(0.0, (min(measured) - attempt.created_at).total_seconds())
            except TypeError:
                accepted_seconds = None
        attempt_edges = tuple(
            edge for edge in edges
            if edge.source_id == attempt.id or edge.target_id == attempt.id
        )
        has_task_link = any(
            edge.source_kind == "attempt"
            and edge.source_id == attempt.id
            and edge.target_kind == "task"
            and edge.target_id == attempt.task_id
            and edge.relation == "implements"
            for edge in attempt_edges
        )
        has_artifact = any(
            edge.source_kind == "artifact"
            and edge.target_kind == "attempt"
            and edge.target_id == attempt.id
            and edge.relation == "measures"
            for edge in attempt_edges
        )
        has_evidence = bool(rows) and any(
            edge.source_kind == "evidence"
            and edge.target_kind == "attempt"
            and edge.target_id == attempt.id
            and edge.relation == "measures"
            for edge in attempt_edges
        )
        trace_complete = has_task_link and has_artifact and has_evidence
        observations.append(AttemptObservation(
            attempt_id=attempt.id,
            task_id=attempt.task_id,
            status=status,
            attempt_number=attempt_numbers[attempt.task_id],
            evidence_count=len(rows),
            trace_complete=trace_complete,
            cost_usd=attempt.cost_usd,
            accepted_evidence_seconds=accepted_seconds,
            escalated=attempt.parent_attempt_id is not None,
            escalation_exhausted=task is not None and task.status == "escalation_exhausted",
        ))
    return build_scorecard(observations)


@dataclass(frozen=True, slots=True)
class ObjectiveMetric:
    success_metric: str
    baseline: float | None
    target: float | None
    source: str
    owner: str
    measurement_window: str

    def __post_init__(self) -> None:
        if not self.success_metric.strip() or not self.source.strip() or not self.owner.strip() or not self.measurement_window.strip():
            raise ValueError("metric identity fields must be non-empty")
        _validate_finite(self.baseline, "baseline")
        _validate_finite(self.target, "target")

    def attribute(self, outcome: "ImpactMeasurement") -> "ImpactAttribution":
        if self.baseline is None:
            return ImpactAttribution(False, None, False, "baseline is required")
        if self.target is None:
            return ImpactAttribution(False, None, False, "target is required")
        if outcome.environment != "production":
            return ImpactAttribution(False, None, False, "only production outcomes can claim impact")
        if outcome.source != self.source:
            return ImpactAttribution(False, None, False, "outcome source does not match metric source")
        if outcome.baseline != self.baseline:
            return ImpactAttribution(False, None, False, "outcome baseline does not match declared baseline")
        delta = outcome.value - self.baseline
        target_met = outcome.value <= self.target if self.target < self.baseline else outcome.value >= self.target
        return ImpactAttribution(True, delta, target_met, "production outcome measured against declared baseline")


@dataclass(frozen=True, slots=True)
class ImpactMeasurement:
    objective_id: str
    value: float
    source: str
    environment: str
    measured_at: datetime
    baseline: float | None

    def __post_init__(self) -> None:
        if not self.objective_id.strip() or not self.source.strip() or not self.environment.strip():
            raise ValueError("impact measurement identity fields must be non-empty")
        if not isinstance(self.measured_at, datetime):
            raise ValueError("measured_at must be a datetime")
        _validate_finite(self.value, "value")
        _validate_finite(self.baseline, "baseline")


@dataclass(frozen=True, slots=True)
class ImpactAttribution:
    is_attributable: bool
    delta: float | None
    target_met: bool
    reason: str
