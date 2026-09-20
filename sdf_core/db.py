from __future__ import annotations

from datetime import datetime
from typing import Any

from sqlalchemy import CheckConstraint, DateTime, Float, Integer, JSON, String, Text, UniqueConstraint, create_engine, select
from sqlalchemy.pool import StaticPool
from sqlalchemy import event
from sqlalchemy.orm import DeclarativeBase, Mapped, Session, mapped_column, sessionmaker


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
    parent_attempt_id: Mapped[str | None] = mapped_column(String(120), nullable=True, index=True)
    model_tier: Mapped[str] = mapped_column(String(32), default="basic", server_default="basic")
    cost_usd: Mapped[float] = mapped_column(Float, default=0.0, server_default="0")
    provider: Mapped[str | None] = mapped_column(String(120), nullable=True)
    latency_ms: Mapped[float | None] = mapped_column(Float, nullable=True)
    input_tokens: Mapped[int | None] = mapped_column(Integer, nullable=True)
    output_tokens: Mapped[int | None] = mapped_column(Integer, nullable=True)
    escalation_reason: Mapped[str | None] = mapped_column(String(500), nullable=True)
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
    measured_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))


class RuntimeEventRow(Base):
    __tablename__ = "runtime_events"
    __table_args__ = (
        # A provider may redeliver an observation after a reconnect.  The
        # source is part of the identity because two trusted producers can
        # legitimately use the same Attempt/session sequence namespace.
        UniqueConstraint(
            "source",
            "attempt_id",
            "session_id",
            "sequence",
            name="uq_runtime_events_source_attempt_session_sequence",
        ),
    )
    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    source: Mapped[str] = mapped_column(String(120), default="runtime", server_default="runtime")
    attempt_id: Mapped[str] = mapped_column(String(120), index=True)
    session_id: Mapped[str] = mapped_column(String(120), index=True)
    sequence: Mapped[int] = mapped_column(Integer)
    kind: Mapped[str] = mapped_column(String(64))
    status: Mapped[str] = mapped_column(String(32))
    payload: Mapped[dict[str, Any]] = mapped_column(JSON)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))


class ToolAuditRow(Base):
    __tablename__ = "tool_audits"
    __table_args__ = (
        UniqueConstraint(
            "attempt_id",
            "action_id",
            "event",
            name="uq_tool_audits_attempt_action_event",
        ),
    )
    audit_id: Mapped[str] = mapped_column(String(120), primary_key=True)
    attempt_id: Mapped[str] = mapped_column(String(120), index=True)
    action_id: Mapped[str] = mapped_column(String(120), index=True)
    actor: Mapped[str] = mapped_column(String(120))
    tool: Mapped[str] = mapped_column(String(120))
    action: Mapped[str] = mapped_column(String(120))
    resource: Mapped[str] = mapped_column(String(500))
    context: Mapped[dict[str, Any]] = mapped_column(JSON)
    event: Mapped[str] = mapped_column(String(64))
    decision: Mapped[str] = mapped_column(String(32))
    executed: Mapped[bool] = mapped_column(default=False)
    outcome: Mapped[str] = mapped_column(String(64))
    detail: Mapped[str] = mapped_column(String(1000))
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))


def make_engine(url: str = "sqlite+pysqlite:///:memory:"):
    kwargs = {"future": True}
    if url.startswith("sqlite") and ":memory:" in url:
        kwargs.update({"connect_args": {"check_same_thread": False}, "poolclass": StaticPool})
    return create_engine(url, **kwargs)


def make_session_factory(url: str = "sqlite+pysqlite:///:memory:"):
    engine = make_engine(url)
    Base.metadata.create_all(engine)
    return sessionmaker(engine, expire_on_commit=False)


@event.listens_for(Session, "before_flush")
def _reject_evidence_mutation(session: Session, flush_context: Any, instances: Any) -> None:
    """Keep evaluator Evidence append-only at the ORM persistence boundary."""

    for item in session.deleted:
        if isinstance(item, EvidenceRow):
            raise ValueError("Evidence rows are append-only and cannot be deleted")
    for item in session.dirty:
        if isinstance(item, EvidenceRow) and session.is_modified(item, include_collections=False):
            raise ValueError("Evidence rows are append-only and cannot be updated")


@event.listens_for(Session, "before_flush")
def _reject_tool_audit_mutation(session: Session, flush_context: Any, instances: Any) -> None:
    """Keep Tool Proxy audit observations append-only at the ORM boundary."""

    for item in session.deleted:
        if isinstance(item, ToolAuditRow):
            raise ValueError("Tool audit rows are append-only and cannot be deleted")
    for item in session.dirty:
        if isinstance(item, ToolAuditRow) and session.is_modified(item, include_collections=False):
            raise ValueError("Tool audit rows are append-only and cannot be updated")


@event.listens_for(Session, "before_flush")
def _reject_dangling_decision_edges(session: Session, flush_context: Any, instances: Any) -> None:
    """Reject graph edges whose endpoints are not graph nodes."""

    edges = [item for item in session.new if isinstance(item, DecisionEdgeRow)]
    if not edges:
        return
    new_node_ids = {item.id for item in session.new if isinstance(item, GraphNodeRow)}
    new_task_ids = {item.id for item in session.new if isinstance(item, TaskRow)}
    new_attempt_ids = {item.id for item in session.new if isinstance(item, AttemptRow)}
    missing: set[str] = set()
    graph_ids = {
        endpoint
        for edge in edges
        for endpoint, kind in ((edge.source_id, edge.source_kind), (edge.target_id, edge.target_kind))
        if kind not in {"task", "attempt"}
    } - new_node_ids
    task_ids = {
        endpoint
        for edge in edges
        for endpoint, kind in ((edge.source_id, edge.source_kind), (edge.target_id, edge.target_kind))
        if kind == "task"
    } - new_task_ids
    attempt_ids = {
        endpoint
        for edge in edges
        for endpoint, kind in ((edge.source_id, edge.source_kind), (edge.target_id, edge.target_kind))
        if kind == "attempt"
    } - new_attempt_ids
    if graph_ids:
        missing.update(graph_ids - set(session.scalars(select(GraphNodeRow.id).where(GraphNodeRow.id.in_(graph_ids)))))
    if task_ids:
        missing.update(task_ids - set(session.scalars(select(TaskRow.id).where(TaskRow.id.in_(task_ids)))))
    if attempt_ids:
        missing.update(attempt_ids - set(session.scalars(select(AttemptRow.id).where(AttemptRow.id.in_(attempt_ids)))))
    if missing:
        raise ValueError(f"decision edge references missing graph node(s): {sorted(missing)}")
