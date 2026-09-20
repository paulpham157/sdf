"""Impact measurement for the decision graph (ADR-0006).

Two separate questions live here, and the separation is the point. The
scorecard answers "is delivery working", which local Evidence can settle. A
business claim answers "did the Objective move", which only an observed
outcome against its declared baseline can settle. Nothing in the scorecard is
ever allowed to stand in for the second.
"""

from __future__ import annotations

from collections.abc import Iterable, Sequence
from dataclasses import dataclass
from datetime import datetime, timedelta
from enum import StrEnum
from typing import Any

V0_METRICS = (
    "first_attempt_success_rate",
    "accepted_task_rate",
    "evidence_coverage",
    "trace_completeness",
    "time_to_accepted_evidence",
    "escalation_rate",
    "cost_per_accepted_task",
    "escalation_exhaustion_rate",
)

EXHAUSTED = "escalation_exhausted"


class EvidenceMode(StrEnum):
    SYNTHETIC = "synthetic"
    LIVE = "live"


@dataclass(frozen=True)
class MetricDeclaration:
    objective_id: str
    metric_name: str
    baseline: float
    target: float
    unit: str
    source: str
    owner: str
    measurement_window_days: int
    declared_at: datetime

    def __post_init__(self) -> None:
        if self.target == self.baseline:
            raise ValueError("target must differ from baseline")
        if self.measurement_window_days <= 0:
            raise ValueError("measurement window must be a positive number of days")

    @property
    def direction(self) -> str:
        return "increase" if self.target > self.baseline else "decrease"

    @property
    def window_end(self) -> datetime:
        return self.declared_at + timedelta(days=self.measurement_window_days)

    def covers(self, observation: OutcomeObservation) -> bool:
        return (
            observation.objective_id == self.objective_id
            and observation.metric_name == self.metric_name
            and self.declared_at <= observation.observed_at <= self.window_end
        )

    def improvement(self, observed: float) -> float:
        if self.direction == "increase":
            return observed - self.baseline
        return self.baseline - observed


@dataclass(frozen=True)
class OutcomeObservation:
    objective_id: str
    metric_name: str
    value: float
    observed_at: datetime
    source: str
    mode: EvidenceMode


@dataclass(frozen=True)
class BusinessImpact:
    objective_id: str
    status: str
    claim_allowed: bool
    metric_name: str | None = None
    baseline: float | None = None
    target: float | None = None
    observed: float | None = None
    delta: float | None = None
    target_attainment: float | None = None
    observed_at: datetime | None = None
    window_end: datetime | None = None

    def as_dict(self) -> dict[str, Any]:
        return {
            "objective_id": self.objective_id,
            "status": self.status,
            "claim_allowed": self.claim_allowed,
            "metric_name": self.metric_name,
            "baseline": self.baseline,
            "target": self.target,
            "observed": self.observed,
            "delta": self.delta,
            "target_attainment": self.target_attainment,
            "observed_at": self.observed_at.isoformat() if self.observed_at else None,
            "window_end": self.window_end.isoformat() if self.window_end else None,
        }


def measure_objective(
    objective_id: str,
    *,
    declaration: MetricDeclaration | None,
    observations: Sequence[OutcomeObservation],
) -> BusinessImpact:
    if declaration is None:
        return BusinessImpact(objective_id=objective_id, status="undeclared", claim_allowed=False)
    base = {
        "objective_id": objective_id,
        "metric_name": declaration.metric_name,
        "baseline": declaration.baseline,
        "target": declaration.target,
        "window_end": declaration.window_end,
    }
    in_window = [item for item in observations if declaration.covers(item)]
    live = [item for item in in_window if item.mode is EvidenceMode.LIVE]
    if not live:
        # Synthetic runs prove the pipeline works, never that the business moved.
        status = "synthetic_only" if in_window else "unmeasured"
        return BusinessImpact(status=status, claim_allowed=False, **base)
    latest = max(live, key=lambda item: item.observed_at)
    delta = declaration.improvement(latest.value)
    gap = abs(declaration.target - declaration.baseline)
    return BusinessImpact(
        status="measured",
        claim_allowed=True,
        observed=latest.value,
        delta=delta,
        target_attainment=delta / gap,
        observed_at=latest.observed_at,
        **base,
    )


@dataclass(frozen=True)
class TaskFact:
    task_id: str
    created_at: datetime
    status: str
    acceptance_criteria: tuple[str, ...]
    objective_id: str | None
    has_artifact: bool


@dataclass(frozen=True)
class AttemptFact:
    attempt_id: str
    task_id: str
    created_at: datetime
    parent_attempt_id: str | None
    model_tier: str | None
    cost_usd: float | None
    escalation_reason: str | None
    outcome: str | None
    mode: EvidenceMode


@dataclass(frozen=True)
class EvidenceFact:
    evidence_id: str
    attempt_id: str
    status: str
    criterion: str | None
    measured_at: datetime
    mode: EvidenceMode


@dataclass(frozen=True)
class ScorecardFacts:
    tasks: tuple[TaskFact, ...] = ()
    attempts: tuple[AttemptFact, ...] = ()
    evidence: tuple[EvidenceFact, ...] = ()


@dataclass(frozen=True)
class Metric:
    name: str
    value: float | None
    numerator: float | None
    denominator: float
    basis: str
    note: str | None = None

    def as_dict(self) -> dict[str, Any]:
        return {
            "name": self.name,
            "value": self.value,
            "numerator": self.numerator,
            "denominator": self.denominator,
            "basis": self.basis,
            "note": self.note,
        }


@dataclass(frozen=True)
class Scorecard:
    metrics: tuple[Metric, ...]

    def metric(self, name: str) -> Metric:
        for item in self.metrics:
            if item.name == name:
                return item
        raise KeyError(name)

    def as_dict(self) -> dict[str, Any]:
        return {"metrics": [item.as_dict() for item in self.metrics]}


def build_scorecard(facts: ScorecardFacts) -> Scorecard:
    return _Computation(facts).run()


def _basis(modes: Iterable[EvidenceMode]) -> str:
    seen = set(modes)
    if not seen:
        return "none"
    if seen == {EvidenceMode.LIVE}:
        return "live"
    if seen == {EvidenceMode.SYNTHETIC}:
        return "synthetic"
    return "mixed"


class _Computation:
    def __init__(self, facts: ScorecardFacts):
        self.tasks = facts.tasks
        self.attempts_by_task: dict[str, list[AttemptFact]] = {}
        for attempt in facts.attempts:
            self.attempts_by_task.setdefault(attempt.task_id, []).append(attempt)
        for items in self.attempts_by_task.values():
            items.sort(key=lambda item: (item.created_at, item.attempt_id))
        self.evidence_by_attempt: dict[str, list[EvidenceFact]] = {}
        for item in facts.evidence:
            self.evidence_by_attempt.setdefault(item.attempt_id, []).append(item)
        self.accepting: dict[str, AttemptFact] = {}
        for task in self.tasks:
            for attempt in self.attempts_by_task.get(task.task_id, []):
                if self._accepts(task, attempt):
                    self.accepting[task.task_id] = attempt
                    break

    def _accepts(self, task: TaskFact, attempt: AttemptFact) -> bool:
        evidence = self.evidence_by_attempt.get(attempt.attempt_id, [])
        if not evidence or not task.acceptance_criteria:
            return False
        if any(item.status != "PASS" for item in evidence):
            return False
        passed = {item.criterion for item in evidence if item.status == "PASS"}
        return set(task.acceptance_criteria) <= passed

    def _modes(self, tasks: Sequence[TaskFact]) -> list[EvidenceMode]:
        modes: list[EvidenceMode] = []
        for task in tasks:
            for attempt in self.attempts_by_task.get(task.task_id, []):
                modes.append(attempt.mode)
                modes.extend(item.mode for item in self.evidence_by_attempt.get(attempt.attempt_id, []))
        return modes

    def _ratio(self, name: str, numerator: float, population: Sequence[TaskFact], *, note: str | None = None) -> Metric:
        denominator = len(population)
        return Metric(
            name=name,
            value=numerator / denominator if denominator else None,
            numerator=numerator if denominator else None,
            denominator=denominator,
            basis=_basis(self._modes(population)) if denominator else "none",
            note=note,
        )

    def _escalated(self, task: TaskFact) -> bool:
        return any(a.parent_attempt_id for a in self.attempts_by_task.get(task.task_id, []))

    def run(self) -> Scorecard:
        attempted = [t for t in self.tasks if self.attempts_by_task.get(t.task_id)]
        accepted = [t for t in self.tasks if t.task_id in self.accepting]
        escalated = [t for t in self.tasks if self._escalated(t)]

        first_success = sum(
            1
            for task in attempted
            if self.accepting.get(task.task_id) is self.attempts_by_task[task.task_id][0]
        )
        traced = sum(
            1
            for task in self.tasks
            if task.objective_id
            and task.has_artifact
            and any(
                self.evidence_by_attempt.get(a.attempt_id)
                for a in self.attempts_by_task.get(task.task_id, [])
            )
        )
        return Scorecard(
            metrics=(
                self._ratio("first_attempt_success_rate", first_success, attempted),
                self._ratio("accepted_task_rate", len(accepted), self.tasks),
                self._coverage(),
                self._ratio("trace_completeness", traced, self.tasks),
                self._time_to_accepted(accepted),
                self._ratio("escalation_rate", len(escalated), attempted),
                self._cost_per_accepted(accepted),
                self._ratio(
                    "escalation_exhaustion_rate",
                    sum(
                        1
                        for task in escalated
                        if any(
                            a.outcome == EXHAUSTED for a in self.attempts_by_task[task.task_id]
                        )
                    ),
                    escalated,
                ),
            )
        )

    def _coverage(self) -> Metric:
        declared = 0
        covered = 0
        for task in self.tasks:
            conclusive = {
                item.criterion
                for attempt in self.attempts_by_task.get(task.task_id, [])
                for item in self.evidence_by_attempt.get(attempt.attempt_id, [])
                if item.status in {"PASS", "FAIL"}
            }
            declared += len(task.acceptance_criteria)
            covered += sum(1 for criterion in task.acceptance_criteria if criterion in conclusive)
        return Metric(
            name="evidence_coverage",
            value=covered / declared if declared else None,
            numerator=covered if declared else None,
            denominator=declared,
            basis=_basis(self._modes(self.tasks)) if declared else "none",
        )

    def _time_to_accepted(self, accepted: Sequence[TaskFact]) -> Metric:
        seconds = []
        for task in accepted:
            evidence = self.evidence_by_attempt[self.accepting[task.task_id].attempt_id]
            seconds.append((max(item.measured_at for item in evidence) - task.created_at).total_seconds())
        total = sum(seconds)
        return Metric(
            name="time_to_accepted_evidence",
            value=total / len(seconds) if seconds else None,
            numerator=total if seconds else None,
            denominator=len(accepted),
            basis=_basis(self._modes(accepted)) if accepted else "none",
        )

    def _cost_per_accepted(self, accepted: Sequence[TaskFact]) -> Metric:
        costs = [a.cost_usd for items in self.attempts_by_task.values() for a in items]
        # A partially recorded bill understates cost, so report nothing rather
        # than a number that looks cheaper than the work really was.
        if any(cost is None for cost in costs):
            return Metric(
                name="cost_per_accepted_task",
                value=None,
                numerator=None,
                denominator=len(accepted),
                basis=_basis(self._modes(accepted)) if accepted else "none",
                note="incomplete cost attribution",
            )
        total = sum(costs)
        return Metric(
            name="cost_per_accepted_task",
            value=total / len(accepted) if accepted else None,
            numerator=total if accepted else None,
            denominator=len(accepted),
            basis=_basis(self._modes(accepted)) if accepted else "none",
        )
