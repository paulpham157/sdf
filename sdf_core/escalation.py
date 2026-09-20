"""Evaluator-driven model-tier escalation policy."""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum

from sqlalchemy.orm import Session

from .db import AttemptRow, DecisionEdgeRow, GraphNodeRow
from .model import AttemptState, utcnow


class ModelTier(StrEnum):
    BASIC = "basic"
    MEDIUM = "medium"
    HIGH = "high"

    def next(self) -> "ModelTier | None":
        ordered = (ModelTier.BASIC, ModelTier.MEDIUM, ModelTier.HIGH)
        index = ordered.index(self)
        return ordered[index + 1] if index + 1 < len(ordered) else None


@dataclass(frozen=True, slots=True)
class EscalationDecision:
    should_escalate: bool
    next_tier: ModelTier | None
    exhausted: bool
    reason: str


class EscalationPolicy:
    """Choose the next model tier only from verified evaluator outcomes."""

    ESCALATABLE_STATUSES = frozenset({"FAIL", "INCONCLUSIVE", "TIMEOUT", "POLICY_FAILURE", "TOOL_FAILURE"})
    _TIER_ORDER = {ModelTier.BASIC: 0, ModelTier.MEDIUM: 1, ModelTier.HIGH: 2}

    def __init__(self, *, max_attempts: int = 3, max_tier: ModelTier = ModelTier.HIGH, max_cost: float = 10.0):
        if max_attempts <= 0:
            raise ValueError("max_attempts must be positive")
        if max_cost < 0:
            raise ValueError("max_cost must not be negative")
        self.max_attempts = max_attempts
        self.max_tier = ModelTier(max_tier)
        self.max_cost = max_cost

    def decide(
        self,
        *,
        status: str,
        attempt_number: int,
        current_tier: ModelTier,
        spent_cost: float,
        next_attempt_cost: float,
    ) -> EscalationDecision:
        if attempt_number <= 0:
            raise ValueError("attempt_number must be positive")
        if spent_cost < 0 or next_attempt_cost < 0:
            raise ValueError("costs must not be negative")
        tier = ModelTier(current_tier)
        normalized = status.upper()
        if normalized == "PASS":
            return EscalationDecision(False, None, False, "verified pass does not escalate")
        if normalized not in self.ESCALATABLE_STATUSES:
            raise ValueError(f"unverified escalation status: {status}")
        if attempt_number >= self.max_attempts:
            return EscalationDecision(False, None, True, "maximum attempts reached")
        next_tier = tier.next()
        if next_tier is None or self._TIER_ORDER[next_tier] > self._TIER_ORDER[self.max_tier]:
            return EscalationDecision(False, None, True, "maximum model tier reached")
        if spent_cost + next_attempt_cost > self.max_cost:
            return EscalationDecision(False, None, True, "hard cost ceiling reached")
        return EscalationDecision(True, next_tier, False, f"verified {normalized.lower()} requires escalation")


def create_escalated_attempt(
    db: Session,
    *,
    prior: AttemptRow,
    dispatch_key: str,
    model_tier: ModelTier,
    agent: str,
    cost_usd: float = 0.0,
) -> AttemptRow:
    """Create a new Attempt linked to the prior terminal Attempt."""

    if prior.status not in {AttemptState.COMPLETED.value, AttemptState.FAILED.value, AttemptState.CANCELLED.value}:
        raise ValueError("only a terminal attempt can be escalated")
    if cost_usd < 0:
        raise ValueError("cost_usd must not be negative")
    existing = db.query(AttemptRow).filter_by(dispatch_key=dispatch_key).one_or_none()
    if existing is not None:
        if existing.parent_attempt_id != prior.id or existing.model_tier != ModelTier(model_tier).value:
            raise ValueError("dispatch key already belongs to another escalation")
        return existing
    child = AttemptRow(
        id=f"ATTEMPT-ESC-{prior.id}-{ModelTier(model_tier).value}",
        task_id=prior.task_id,
        status=AttemptState.CREATED.value,
        dispatch_key=dispatch_key,
        agent=agent,
        parent_attempt_id=prior.id,
        model_tier=ModelTier(model_tier).value,
        cost_usd=cost_usd,
        created_at=utcnow(),
    )
    db.add(child)
    db.add(GraphNodeRow(id=child.id, kind="attempt", title="Escalated execution attempt", source="escalation", owner="sdf", confidence=1.0, created_at=utcnow()))
    db.add(DecisionEdgeRow(
        source_kind="attempt", source_id=child.id,
        target_kind="attempt", target_id=prior.id,
        relation="depends_on", source="escalation", owner="sdf", confidence=1.0, created_at=utcnow(),
    ))
    db.flush()
    return child
