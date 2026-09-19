from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timezone
from enum import StrEnum
from typing import Any


def utcnow() -> datetime:
    return datetime.now(timezone.utc)


class TaskState(StrEnum):
    CREATED = "created"
    READY = "ready"
    RUNNING = "running"
    EVALUATING = "evaluating"
    SUCCEEDED = "succeeded"
    FAILED = "failed"
    INCONCLUSIVE = "inconclusive"


class AttemptState(StrEnum):
    CREATED = "created"
    DISPATCHED = "dispatched"
    RUNNING = "running"
    COMPLETED = "completed"
    FAILED = "failed"
    CANCELLED = "cancelled"


@dataclass(frozen=True)
class GraphNode:
    id: str
    kind: str
    title: str
    source: str
    owner: str
    confidence: float = 1.0
    created_at: datetime = field(default_factory=utcnow)
    metadata: dict[str, Any] | None = None


@dataclass(frozen=True)
class DecisionEdge:
    source_kind: str
    source_id: str
    target_kind: str
    target_id: str
    relation: str
    source: str
    owner: str
    confidence: float
    created_at: datetime
    evidence_ref: str | None = None
