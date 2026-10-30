from __future__ import annotations

from datetime import datetime
from typing import Any

from sqlalchemy import CheckConstraint, DateTime, Float, Index, Integer, JSON, String, Text, UniqueConstraint, create_engine, select, text
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
    model_profile: Mapped[str | None] = mapped_column(String(120), nullable=True)
    outcome: Mapped[str | None] = mapped_column(String(40), nullable=True)
    evidence_mode: Mapped[str] = mapped_column(String(16), default="synthetic")
    # ADR 0023: the turn error taxonomy class of a failed Attempt (or
    # ``cleanup_failed`` when the work finished but the sandbox would not die).
    error_class: Mapped[str | None] = mapped_column(String(32), nullable=True)
    # ADR 0024: OpenTelemetry trace id (32 hex) when tracing was on.
    trace_id: Mapped[str | None] = mapped_column(String(32), nullable=True)
    # ADR 0026: the authenticated principal that started the Attempt. Only that
    # principal (or an admin) may drive its Tool Proxy actions; NULL (internal
    # or pre-0018 Attempts) means admin-only over HTTP.
    principal: Mapped[str | None] = mapped_column(String(120), nullable=True, index=True)


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
    measured_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    evidence_mode: Mapped[str] = mapped_column(String(16), default="synthetic")
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))


RUNTIME_EVENT_PROVENANCE_SQL = (
    "(substr(kind, 1, 5) = 'tool_' AND source = 'tool-proxy') "
    "OR (substr(kind, 1, 5) <> 'tool_' AND source <> 'tool-proxy')"
)


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
        # Provenance (ADR 0029): tool_* kinds are action-boundary events and
        # only the Tool Proxy emits them; the Tool Proxy emits nothing else.
        CheckConstraint(RUNTIME_EVENT_PROVENANCE_SQL, name="ck_runtime_events_provenance"),
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


USAGE_KINDS = ("turn", "sandbox")
USAGE_SOURCES = ("reported", "estimated", "missing")


class UsageLedgerRow(Base):
    """Append-only usage/cost ledger (ADR 0023): one row per agent turn and
    one per sandbox lifetime.  ``source`` says where the numbers came from:
    ``reported`` by the agent/provider, ``estimated`` by SDF (price table,
    wall clock) or ``missing`` (the agent reported nothing usable)."""

    __tablename__ = "usage_ledger"
    __table_args__ = (
        CheckConstraint("kind IN ('turn','sandbox')", name="ck_usage_ledger_kind"),
        CheckConstraint("source IN ('reported','estimated','missing')", name="ck_usage_ledger_source"),
    )
    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    attempt_id: Mapped[str] = mapped_column(String(120), index=True)
    run_id: Mapped[str | None] = mapped_column(String(120), nullable=True, index=True)
    kind: Mapped[str] = mapped_column(String(16))
    agent: Mapped[str | None] = mapped_column(String(32), nullable=True)
    turn: Mapped[int | None] = mapped_column(Integer, nullable=True)
    session_id: Mapped[str | None] = mapped_column(String(120), nullable=True)
    sandbox_id: Mapped[str | None] = mapped_column(String(120), nullable=True)
    sandbox_provider: Mapped[str | None] = mapped_column(String(32), nullable=True)
    model: Mapped[str | None] = mapped_column(String(120), nullable=True)
    input_tokens: Mapped[int | None] = mapped_column(Integer, nullable=True)
    output_tokens: Mapped[int | None] = mapped_column(Integer, nullable=True)
    cache_read_tokens: Mapped[int | None] = mapped_column(Integer, nullable=True)
    seconds: Mapped[float | None] = mapped_column(Float, nullable=True)
    cost_usd: Mapped[float | None] = mapped_column(Float, nullable=True)
    source: Mapped[str] = mapped_column(String(16))
    cost_source: Mapped[str] = mapped_column(String(16))
    status: Mapped[str] = mapped_column(String(32))
    error_class: Mapped[str | None] = mapped_column(String(32), nullable=True)
    detail: Mapped[dict[str, Any] | None] = mapped_column(JSON, nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))


class ToolAuditRow(Base):
    __tablename__ = "tool_audits"
    # ADR 0026: execution events (claim, executed, failed) stay unique per
    # action, which is the idempotency fence; ``policy_decided`` rows are one
    # per delivery so every caller's decision is recorded with its principal.
    __table_args__ = (
        Index(
            "uq_tool_audits_execution_event",
            "attempt_id",
            "action_id",
            "event",
            unique=True,
            sqlite_where=text("event <> 'policy_decided'"),
            postgresql_where=text("event <> 'policy_decided'"),
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
    evidence_mode: Mapped[str] = mapped_column(String(16), default="synthetic")
    # Authenticated API principal that requested the action (#51); NULL for
    # in-process callers and rows written before migration 0018.
    principal: Mapped[str | None] = mapped_column(String(120), nullable=True, index=True)


# --- P4: plan-approval gate, decision requests and agent messaging (ADR 0014) ---

PLAN_STATUSES = ("proposed", "approved", "rejected")
DECISION_REQUEST_STATUSES = ("pending", "approved", "rejected", "answered")
AGENT_MESSAGE_KINDS = ("message", "report", "question", "decision_request", "decision", "system")
AGENT_ROLES = ("lead", "worker", "manager", "sdf")
RUN_STATUSES = ("running", "succeeded", "failed", "cancelled")


class PlanRow(Base):
    """An implementation plan; its Tasks cannot start until a human approves it."""

    __tablename__ = "plans"
    __table_args__ = (CheckConstraint("status IN ('proposed','approved','rejected')", name="ck_plans_status"),)
    id: Mapped[str] = mapped_column(String(120), primary_key=True)
    title: Mapped[str] = mapped_column(String(500))
    objective_id: Mapped[str | None] = mapped_column(String(120), nullable=True)
    status: Mapped[str] = mapped_column(String(16), index=True)
    # Immutable once proposed; approval binds to this exact content hash.
    content: Mapped[dict[str, Any]] = mapped_column(JSON)
    content_sha256: Mapped[str] = mapped_column(String(64))
    proposed_by: Mapped[str] = mapped_column(String(120))
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    decision_request_id: Mapped[str] = mapped_column(String(120))
    decided_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)


class PlanTaskRow(Base):
    """Membership of a Task in a Plan; a gated Task belongs to exactly one Plan."""

    __tablename__ = "plan_tasks"
    task_id: Mapped[str] = mapped_column(String(120), primary_key=True)
    plan_id: Mapped[str] = mapped_column(String(120), index=True)
    subtask_key: Mapped[str] = mapped_column(String(64))


class DecisionRequestRow(Base):
    """A Decision awaiting resolution (plan approval or an agent's request).

    Only a human resolves a plan approval; an agent request may be resolved by
    a human or a configured policy.  Nothing is auto-approved.
    """

    __tablename__ = "decision_requests"
    __table_args__ = (
        CheckConstraint("status IN ('pending','approved','rejected','answered')", name="ck_decision_requests_status"),
        CheckConstraint("subject_kind IN ('plan','agent_request')", name="ck_decision_requests_subject"),
        CheckConstraint("resolver_kind IS NULL OR resolver_kind IN ('human','policy')", name="ck_decision_requests_resolver"),
    )
    id: Mapped[str] = mapped_column(String(120), primary_key=True)
    subject_kind: Mapped[str] = mapped_column(String(32))
    subject_id: Mapped[str] = mapped_column(String(120), index=True)
    run_id: Mapped[str | None] = mapped_column(String(120), nullable=True, index=True)
    question: Mapped[str] = mapped_column(Text)
    options: Mapped[list[str]] = mapped_column(JSON, default=list)
    status: Mapped[str] = mapped_column(String(16), index=True)
    requested_by: Mapped[str] = mapped_column(String(120))
    message_id: Mapped[str | None] = mapped_column(String(120), nullable=True)
    resolved_by: Mapped[str | None] = mapped_column(String(120), nullable=True)
    resolver_kind: Mapped[str | None] = mapped_column(String(16), nullable=True)
    resolution: Mapped[str | None] = mapped_column(Text, nullable=True)
    decision_node_id: Mapped[str | None] = mapped_column(String(120), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    resolved_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)


class AgentRunRow(Base):
    """One lead/worker coordination run over an approved Plan."""

    __tablename__ = "agent_runs"
    __table_args__ = (CheckConstraint("status IN ('running','succeeded','failed','cancelled')", name="ck_agent_runs_status"),)
    id: Mapped[str] = mapped_column(String(120), primary_key=True)
    plan_id: Mapped[str] = mapped_column(String(120), index=True)
    status: Mapped[str] = mapped_column(String(16), index=True)
    config: Mapped[dict[str, Any]] = mapped_column(JSON)
    started_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    finished_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    cancel_requested_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    cost_usd: Mapped[float] = mapped_column(Float, default=0.0)
    # ADR 0015: authenticated principals, never request fields.
    started_by: Mapped[str | None] = mapped_column(String(120), nullable=True)
    cancel_requested_by: Mapped[str | None] = mapped_column(String(120), nullable=True)
    summary: Mapped[dict[str, Any] | None] = mapped_column(JSON, nullable=True)
    error: Mapped[str | None] = mapped_column(Text, nullable=True)
    trace_id: Mapped[str | None] = mapped_column(String(32), nullable=True)  # ADR 0024


class AgentRunMemberRow(Base):
    """A lead or worker in a run, bound by the host to exactly one sandbox."""

    __tablename__ = "agent_run_members"
    __table_args__ = (UniqueConstraint("run_id", "agent_id", name="uq_agent_run_members_run_agent"),)
    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    run_id: Mapped[str] = mapped_column(String(120), index=True)
    agent_id: Mapped[str] = mapped_column(String(64))
    role: Mapped[str] = mapped_column(String(16))
    agent_kind: Mapped[str] = mapped_column(String(32))
    subtask_key: Mapped[str | None] = mapped_column(String(64), nullable=True)
    sandbox_id: Mapped[str | None] = mapped_column(String(120), nullable=True)
    status: Mapped[str] = mapped_column(String(32))
    turns: Mapped[int] = mapped_column(Integer, default=0)
    input_tokens: Mapped[int] = mapped_column(Integer, default=0)
    output_tokens: Mapped[int] = mapped_column(Integer, default=0)


class AgentMessageRow(Base):
    """Append-only agent message.  ``from_agent`` is assigned by the host from
    the sandbox the message was collected from, never from the envelope."""

    __tablename__ = "agent_messages"
    __table_args__ = (
        UniqueConstraint("run_id", "seq", name="uq_agent_messages_run_seq"),
        UniqueConstraint("run_id", "from_agent", "envelope_id", name="uq_agent_messages_envelope"),
        CheckConstraint(
            "kind IN ('message','report','question','decision_request','decision','system')", name="ck_agent_messages_kind"
        ),
    )
    id: Mapped[str] = mapped_column(String(120), primary_key=True)
    run_id: Mapped[str] = mapped_column(String(120), index=True)
    seq: Mapped[int] = mapped_column(Integer)
    envelope_id: Mapped[str] = mapped_column(String(64))
    from_agent: Mapped[str] = mapped_column(String(64))
    from_role: Mapped[str] = mapped_column(String(16))
    to_agent: Mapped[str | None] = mapped_column(String(64), nullable=True)
    to_role: Mapped[str | None] = mapped_column(String(16), nullable=True)
    kind: Mapped[str] = mapped_column(String(24))
    body: Mapped[str] = mapped_column(Text)
    body_sha256: Mapped[str] = mapped_column(String(64))
    truncated: Mapped[bool] = mapped_column(default=False)
    attempt_id: Mapped[str | None] = mapped_column(String(120), nullable=True)
    source_sandbox_id: Mapped[str | None] = mapped_column(String(120), nullable=True)
    meta: Mapped[dict[str, Any] | None] = mapped_column(JSON, nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))


class AgentClaimRow(Base):
    """What an agent *says* it did (a ``report``).  Never Evidence."""

    __tablename__ = "agent_claims"
    id: Mapped[str] = mapped_column(String(120), primary_key=True)
    run_id: Mapped[str] = mapped_column(String(120), index=True)
    message_id: Mapped[str] = mapped_column(String(120), unique=True)
    agent_id: Mapped[str] = mapped_column(String(64))
    attempt_id: Mapped[str | None] = mapped_column(String(120), nullable=True)
    subtask_key: Mapped[str | None] = mapped_column(String(64), nullable=True)
    claimed_status: Mapped[str] = mapped_column(String(16))
    body: Mapped[str] = mapped_column(Text)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))


class PlanGateError(ValueError):
    """An implementation Task/Attempt/run was started without an approved Plan."""


def make_engine(url: str = "sqlite+pysqlite:///:memory:"):
    kwargs = {"future": True}
    if url.startswith("sqlite") and ":memory:" in url:
        kwargs.update({"connect_args": {"check_same_thread": False}, "poolclass": StaticPool})
    return create_engine(url, **kwargs)


class ApiTokenRow(Base):
    """A bearer API token (ADR 0015).  Only an HMAC-SHA256 of the secret, keyed
    with a server-side pepper, is stored; the plaintext is shown once."""

    __tablename__ = "api_tokens"
    __table_args__ = (CheckConstraint("kind IN ('human','service')", name="ck_api_tokens_kind"),)
    id: Mapped[str] = mapped_column(String(32), primary_key=True)
    principal: Mapped[str] = mapped_column(String(120), index=True)
    kind: Mapped[str] = mapped_column(String(16))
    roles: Mapped[list[str]] = mapped_column(JSON)
    secret_hash: Mapped[str] = mapped_column(String(64))
    description: Mapped[str | None] = mapped_column(String(500), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    created_by: Mapped[str] = mapped_column(String(120))
    expires_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    revoked_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    revoked_by: Mapped[str | None] = mapped_column(String(120), nullable=True)
    last_used_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)


class AuthAuditRow(Base):
    """Append-only audit of authenticated mutations and authentication failures."""

    __tablename__ = "auth_audit"
    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    at: Mapped[datetime] = mapped_column(DateTime(timezone=True), index=True)
    event: Mapped[str] = mapped_column(String(32), index=True)
    principal: Mapped[str | None] = mapped_column(String(120), nullable=True, index=True)
    token_id: Mapped[str | None] = mapped_column(String(32), nullable=True)
    method: Mapped[str | None] = mapped_column(String(8), nullable=True)
    path: Mapped[str | None] = mapped_column(String(500), nullable=True)
    route: Mapped[str | None] = mapped_column(String(200), nullable=True)
    status_code: Mapped[int | None] = mapped_column(Integer, nullable=True)
    client: Mapped[str | None] = mapped_column(String(64), nullable=True)
    detail: Mapped[dict[str, Any] | None] = mapped_column(JSON, nullable=True)


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


@event.listens_for(Session, "before_flush")
def _reject_agent_message_mutation(session: Session, flush_context: Any, instances: Any) -> None:
    """Agent messages, claims, usage-ledger rows and normalized runtime events
    (ADR 0029) are append-only, like Evidence."""

    for item in session.deleted:
        if isinstance(item, (AgentMessageRow, AgentClaimRow, UsageLedgerRow, RuntimeEventRow)):
            raise ValueError(f"{type(item).__name__} rows are append-only and cannot be deleted")
    for item in session.dirty:
        if isinstance(item, (AgentMessageRow, AgentClaimRow, UsageLedgerRow, RuntimeEventRow)) and session.is_modified(item, include_collections=False):
            raise ValueError(f"{type(item).__name__} rows are append-only and cannot be updated")


_PLAN_MUTABLE = {"status", "decided_at"}


@event.listens_for(Session, "before_flush")
def _guard_plans_and_decisions(session: Session, flush_context: Any, instances: Any) -> None:
    """Plan content is immutable; status moves once, proposed -> approved|rejected,
    and approval needs a human-resolved, approved decision request.  A resolved
    decision request is final.  Plans and decision requests are never deleted."""

    from sqlalchemy import inspect as sa_inspect

    for item in session.deleted:
        if isinstance(item, (PlanRow, DecisionRequestRow, PlanTaskRow)):
            raise ValueError(f"{type(item).__name__} rows cannot be deleted")
    with session.no_autoflush:
        for item in list(session.dirty):
            if isinstance(item, PlanTaskRow) and session.is_modified(item, include_collections=False):
                raise ValueError("plan task membership is immutable")
            if isinstance(item, DecisionRequestRow) and session.is_modified(item, include_collections=False):
                history = sa_inspect(item).attrs.status.history
                before = (history.deleted or history.unchanged or [None])[0]
                if before is not None and before != "pending":
                    raise ValueError("a resolved decision request is final")
            if not isinstance(item, PlanRow) or not session.is_modified(item, include_collections=False):
                continue
            state = sa_inspect(item)
            changed = {attr.key for attr in state.attrs if attr.history.has_changes()}
            if changed - _PLAN_MUTABLE:
                raise ValueError("plan content is immutable once proposed; propose a new plan")
            if "status" in changed:
                before = state.attrs.status.history.deleted
                if not before or before[0] != "proposed" or item.status not in ("approved", "rejected"):
                    raise ValueError("plan status may only move once from proposed to approved or rejected")
                if item.status == "approved":
                    request = session.get(DecisionRequestRow, item.decision_request_id)
                    if (
                        request is None or request.subject_id != item.id or request.status != "approved"
                        or request.resolver_kind != "human"
                    ):
                        raise PlanGateError("plan approval requires a human-approved decision request")


def _unapproved_plan(session: Session, task_id: str) -> str | None:
    link = session.get(PlanTaskRow, task_id)
    if link is None:
        for item in session.new:
            if isinstance(item, PlanTaskRow) and item.task_id == task_id:
                link = item
    if link is None:
        return None
    plan = session.get(PlanRow, link.plan_id)
    if plan is None or plan.status != "approved":
        return link.plan_id
    return None


@event.listens_for(Session, "before_flush")
def _enforce_plan_gate(session: Session, flush_context: Any, instances: Any) -> None:
    """No Attempt on a planned Task, and no agent run, before human approval."""

    with session.no_autoflush:
        for item in list(session.new):
            if isinstance(item, AttemptRow):
                blocked = _unapproved_plan(session, item.task_id)
                if blocked is not None:
                    raise PlanGateError(f"plan {blocked} is not approved; task {item.task_id} cannot start")
            elif isinstance(item, AgentRunRow):
                plan = session.get(PlanRow, item.plan_id)
                if plan is None or plan.status != "approved":
                    raise PlanGateError(f"plan {item.plan_id} is not approved; no agent run may start")


_TOKEN_MUTABLE = {"last_used_at", "revoked_at", "revoked_by"}


@event.listens_for(Session, "before_flush")
def _guard_auth_rows(session: Session, flush_context: Any, instances: Any) -> None:
    """Audit rows are append-only; a token's identity, roles, secret hash and
    expiry are immutable, it is never deleted, and revocation is final."""

    from sqlalchemy import inspect as sa_inspect

    for item in session.deleted:
        if isinstance(item, AuthAuditRow):
            raise ValueError("auth audit rows are append-only and cannot be deleted")
        if isinstance(item, ApiTokenRow):
            raise ValueError("API tokens are revoked, never deleted")
    for item in session.dirty:
        if isinstance(item, AuthAuditRow) and session.is_modified(item, include_collections=False):
            raise ValueError("auth audit rows are append-only and cannot be updated")
        if isinstance(item, ApiTokenRow) and session.is_modified(item, include_collections=False):
            state = sa_inspect(item)
            changed = {attr.key for attr in state.attrs if attr.history.has_changes()}
            if changed - _TOKEN_MUTABLE:
                raise ValueError("an API token's identity, roles, secret and expiry are immutable")
            history = state.attrs.revoked_at.history
            before = (history.deleted or history.unchanged or [None])[0]
            if before is not None and changed & {"revoked_at", "revoked_by"}:
                raise ValueError("a revoked API token stays revoked")
