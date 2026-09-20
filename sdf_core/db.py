from __future__ import annotations

from datetime import datetime
from typing import Any

from sqlalchemy import CheckConstraint, DateTime, Float, Integer, JSON, String, Text, create_engine
from sqlalchemy.pool import StaticPool
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column, sessionmaker


class Base(DeclarativeBase):
    pass


class GraphNodeRow(Base):
    __tablename__ = "graph_nodes"
    id: Mapped[str] = mapped_column(String(120), primary_key=True)
    kind: Mapped[str] = mapped_column(String(40), index=True)
    title: Mapped[str] = mapped_column(String(500))
    source: Mapped[str] = mapped_column(String(500))
    owner: Mapped[str] = mapped_column(String(120))
    confidence: Mapped[float] = mapped_column(Float, default=1.0)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    metadata_json: Mapped[dict[str, Any] | None] = mapped_column(JSON, nullable=True)


class DecisionEdgeRow(Base):
    __tablename__ = "decision_edges"
    __table_args__ = (CheckConstraint("relation IN ('motivates','constrains','implements','depends_on','measures','validates','contradicts')", name="ck_decision_edges_relation"),)
    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    source_kind: Mapped[str] = mapped_column(String(40))
    source_id: Mapped[str] = mapped_column(String(120))
    target_kind: Mapped[str] = mapped_column(String(40))
    target_id: Mapped[str] = mapped_column(String(120))
    relation: Mapped[str] = mapped_column(String(40))
    source: Mapped[str] = mapped_column(String(500))
    owner: Mapped[str] = mapped_column(String(120))
    confidence: Mapped[float] = mapped_column(Float)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    evidence_ref: Mapped[str | None] = mapped_column(String(500), nullable=True)


class TaskRow(Base):
    __tablename__ = "tasks"
    id: Mapped[str] = mapped_column(String(120), primary_key=True)
    title: Mapped[str] = mapped_column(String(500))
    status: Mapped[str] = mapped_column(String(32), index=True)
    idempotency_key: Mapped[str] = mapped_column(String(200), unique=True)
    acceptance_criteria: Mapped[list[str]] = mapped_column(JSON)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))


class AttemptRow(Base):
    __tablename__ = "attempts"
    id: Mapped[str] = mapped_column(String(120), primary_key=True)
    task_id: Mapped[str] = mapped_column(String(120), index=True)
    status: Mapped[str] = mapped_column(String(32), index=True)
    dispatch_key: Mapped[str] = mapped_column(String(200), unique=True)
    agent: Mapped[str] = mapped_column(String(120))
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    parent_attempt_id: Mapped[str | None] = mapped_column(String(120), nullable=True, index=True)
    model_tier: Mapped[str | None] = mapped_column(String(16), nullable=True)
    model_profile: Mapped[str | None] = mapped_column(String(120), nullable=True)
    provider: Mapped[str | None] = mapped_column(String(120), nullable=True)
    input_tokens: Mapped[int | None] = mapped_column(Integer, nullable=True)
    output_tokens: Mapped[int | None] = mapped_column(Integer, nullable=True)
    cost_usd: Mapped[float | None] = mapped_column(Float, nullable=True)
    latency_ms: Mapped[int | None] = mapped_column(Integer, nullable=True)
    escalation_reason: Mapped[str | None] = mapped_column(String(120), nullable=True)
    outcome: Mapped[str | None] = mapped_column(String(40), nullable=True)
    evidence_mode: Mapped[str] = mapped_column(String(16), default="synthetic")


class ObjectiveMetricRow(Base):
    """One declared outcome metric per Objective. Without it, no business claim."""

    __tablename__ = "objective_metrics"
    __table_args__ = (CheckConstraint("measurement_window_days > 0", name="ck_objective_metrics_window"),)
    objective_id: Mapped[str] = mapped_column(String(120), primary_key=True)
    metric_name: Mapped[str] = mapped_column(String(120))
    baseline: Mapped[float] = mapped_column(Float)
    target: Mapped[float] = mapped_column(Float)
    unit: Mapped[str] = mapped_column(String(40))
    source: Mapped[str] = mapped_column(String(500))
    owner: Mapped[str] = mapped_column(String(120))
    measurement_window_days: Mapped[int] = mapped_column(Integer)
    declared_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))


class OutcomeObservationRow(Base):
    __tablename__ = "outcome_observations"
    __table_args__ = (CheckConstraint("mode IN ('synthetic','live')", name="ck_outcome_observations_mode"),)
    id: Mapped[str] = mapped_column(String(120), primary_key=True)
    objective_id: Mapped[str] = mapped_column(String(120), index=True)
    metric_name: Mapped[str] = mapped_column(String(120))
    value: Mapped[float] = mapped_column(Float)
    observed_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    source: Mapped[str] = mapped_column(String(500))
    mode: Mapped[str] = mapped_column(String(16))
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))


class ArtifactRow(Base):
    __tablename__ = "artifacts"
    id: Mapped[str] = mapped_column(String(120), primary_key=True)
    attempt_id: Mapped[str] = mapped_column(String(120), index=True)
    kind: Mapped[str] = mapped_column(String(40))
    uri: Mapped[str] = mapped_column(String(500))
    sha256: Mapped[str] = mapped_column(String(64))
    size_bytes: Mapped[int] = mapped_column(Integer)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))


class EvidenceRow(Base):
    __tablename__ = "evidence"
    id: Mapped[str] = mapped_column(String(120), primary_key=True)
    attempt_id: Mapped[str] = mapped_column(String(120), index=True)
    kind: Mapped[str] = mapped_column(String(40))
    status: Mapped[str] = mapped_column(String(32))
    command: Mapped[str] = mapped_column(Text)
    exit_code: Mapped[int | None] = mapped_column(Integer, nullable=True)
    artifact_ref: Mapped[str | None] = mapped_column(String(120), nullable=True)
    confidence: Mapped[float] = mapped_column(Float)
    criterion: Mapped[str | None] = mapped_column(String(500), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    evidence_mode: Mapped[str] = mapped_column(String(16), default="synthetic")


def make_engine(url: str = "sqlite+pysqlite:///:memory:"):
    kwargs = {"future": True}
    if url.startswith("sqlite") and ":memory:" in url:
        kwargs.update({"connect_args": {"check_same_thread": False}, "poolclass": StaticPool})
    return create_engine(url, **kwargs)


def make_session_factory(url: str = "sqlite+pysqlite:///:memory:"):
    engine = make_engine(url)
    Base.metadata.create_all(engine)
    return sessionmaker(engine, expire_on_commit=False)
