from __future__ import annotations

import os
from collections import deque
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Generator
from uuid import uuid4

from fastapi import Depends, FastAPI, HTTPException, Response, status
from pydantic import BaseModel, Field
from sqlalchemy import select
from sqlalchemy.orm import Session

from .db import AttemptRow, Base, DecisionEdgeRow, EvidenceRow, GraphNodeRow, RuntimeEventRow, TaskRow, make_engine, make_session_factory
from .escalation import ModelTier
from .execution import ExecutionService
from .adapter import FakeNativeAdapter
from .measurement import build_scorecard_from_db
from .policy import ActionRequest, AllowlistPolicy
from .containment import MacOSSandboxBackend
from .e2b_containment import E2BContainmentBackend


DATABASE_URL = os.getenv("SDF_DATABASE_URL", "sqlite+pysqlite:///:memory:")
engine = make_engine(DATABASE_URL)
Base.metadata.create_all(engine)
SessionLocal = make_session_factory(DATABASE_URL)

app = FastAPI(title="SDF Core", version="0.1.0")


class NodeRequest(BaseModel):
    id: str = Field(min_length=1, max_length=120)
    title: str = Field(min_length=1, max_length=500)
    owner: str = Field(min_length=1, max_length=120)
    source: str = Field(min_length=1, max_length=500)


class ObjectiveRequest(NodeRequest):
    business_context_id: str
    success_metric: str | None = Field(default=None, max_length=500)
    baseline: float | None = None
    target: float | None = None
    measurement_source: str | None = Field(default=None, max_length=500)
    measurement_window: str | None = Field(default=None, max_length=120)


class TaskRequest(BaseModel):
    id: str = Field(min_length=1, max_length=120)
    title: str = Field(min_length=1, max_length=500)
    objective_id: str | None = None
    idempotency_key: str = Field(min_length=1, max_length=200)
    acceptance_criteria: list[str] = Field(default_factory=list)


class RunTaskRequest(BaseModel):
    dispatch_key: str | None = Field(default=None, min_length=1, max_length=200)
    fixture_dir: str
    instructions: str = "complete the task"
    commands: list[list[str]] = Field(default_factory=list)
    criterion_checks: dict[str, list[list[str]]] | None = Field(
        default=None, description="Explicit criterion checks take precedence over legacy commands, including an empty mapping."
    )
    validation_target_kind: str | None = None
    validation_target_id: str | None = None
    parent_attempt_id: str | None = None
    model_tier: ModelTier = ModelTier.BASIC
    cost_usd: float = Field(default=0.0, ge=0.0)
    provider: str | None = Field(default=None, max_length=120)
    latency_ms: float | None = Field(default=None, ge=0.0)
    input_tokens: int | None = Field(default=None, ge=0)
    output_tokens: int | None = Field(default=None, ge=0)
    escalation_reason: str | None = Field(default=None, max_length=500)


class ToolActionRequest(BaseModel):
    """Structured Tool Proxy input; terminal text is intentionally absent."""

    actor: str = Field(min_length=1, max_length=120)
    tool: str = Field(min_length=1, max_length=120)
    action: str = Field(min_length=1, max_length=120)
    resource: str = Field(min_length=1, max_length=500)
    context: dict[str, Any] = Field(default_factory=dict)


def configured_tool_policy() -> AllowlistPolicy:
    """Build the server-side allowlist; an unset list fails closed.

    ``SDF_TOOL_ALLOWLIST`` is a deployment configuration seam containing
    comma-separated ``tool:action`` pairs, for example
    ``filesystem:read,filesystem:write``.  The HTTP caller cannot expand it.
    """

    configured = os.getenv("SDF_TOOL_ALLOWLIST", "")
    allowed: set[tuple[str, str]] = set()
    for item in configured.split(","):
        tool, separator, action = item.strip().partition(":")
        if separator and tool and action:
            allowed.add((tool, action))
    # Agent egress inside E2B remains available. This is the separate
    # structured SDF network action, which is not yet routed through the box;
    # keep it disabled even if an operator lists it above.
    if os.getenv("SDF_CONTAINMENT_BACKEND", "").strip().lower() == "e2b":
        allowed.discard(("network", "request"))
    return AllowlistPolicy(allowed, name="server-configured-allowlist")


def configured_containment() -> tuple[object | None, bool]:
    """Return explicit containment configuration for public tool actions."""

    backend_name = os.getenv("SDF_CONTAINMENT_BACKEND", "").strip().lower()
    smoke_passed = os.getenv("SDF_CONTAINMENT_SMOKE", "").strip().lower() == "passed"
    # A backend binary is not evidence of enforcement.  Operators must run
    # the disposable smoke harness and explicitly promote its result before
    # public process actions can use the backend.
    if backend_name == "macos" and smoke_passed:
        backend = MacOSSandboxBackend()
    elif backend_name == "e2b" and smoke_passed:
        backend = E2BContainmentBackend(template=os.getenv("SDF_E2B_TEMPLATE", "base"))
    else:
        backend = None
    required = os.getenv("SDF_REQUIRE_CONTAINMENT", "1").strip().lower() not in {"0", "false", "no"}
    return backend, required


class EvidenceResponse(BaseModel):
    id: str
    attempt_id: str
    kind: str
    status: str
    command: str
    exit_code: int | None
    artifact_ref: str | None
    confidence: float
    criterion: str | None = None


def get_db() -> Generator[Session, None, None]:
    db = SessionLocal()
    try:
        yield db
    finally:
        db.close()


def node_payload(node: GraphNodeRow) -> dict[str, Any]:
    payload = {"id": node.id, "kind": node.kind, "title": node.title, "owner": node.owner, "source": node.source}
    if node.metadata_json is not None:
        payload["metadata"] = node.metadata_json
    return payload


def edge_payload(edge: DecisionEdgeRow) -> dict[str, Any]:
    return {
        "source_kind": edge.source_kind,
        "source_id": edge.source_id,
        "target_kind": edge.target_kind,
        "target_id": edge.target_id,
        "relation": edge.relation,
        "confidence": edge.confidence,
    }


def add_node(db: Session, *, node_id: str, kind: str, title: str, owner: str, source: str) -> GraphNodeRow:
    existing = db.get(GraphNodeRow, node_id)
    if existing:
        raise HTTPException(status_code=409, detail=f"node already exists: {node_id}")
    node = GraphNodeRow(
        id=node_id,
        kind=kind,
        title=title,
        source=source,
        owner=owner,
        confidence=1.0,
        created_at=datetime.now(timezone.utc),
    )
    db.add(node)
    return node


@app.post("/business-contexts", status_code=status.HTTP_201_CREATED)
def create_business_context(payload: NodeRequest, db: Session = Depends(get_db)):
    node = add_node(db, node_id=payload.id, kind="business_context", title=payload.title, owner=payload.owner, source=payload.source)
    db.commit()
    return node_payload(node)


@app.post("/objectives", status_code=status.HTTP_201_CREATED)
def create_objective(payload: ObjectiveRequest, db: Session = Depends(get_db)):
    if not db.get(GraphNodeRow, payload.business_context_id):
        raise HTTPException(status_code=404, detail="business context not found")
    metric_fields = {
        "success_metric": payload.success_metric,
        "baseline": payload.baseline,
        "target": payload.target,
        "source": payload.measurement_source or payload.source,
        "owner": payload.owner,
        "measurement_window": payload.measurement_window,
    }
    metric_metadata = {key: value for key, value in metric_fields.items() if value is not None}
    node = add_node(db, node_id=payload.id, kind="objective", title=payload.title, owner=payload.owner, source=payload.source)
    node.metadata_json = {"metric": metric_metadata} if metric_metadata else None
    db.add(DecisionEdgeRow(
        source_kind="business_context", source_id=payload.business_context_id,
        target_kind="objective", target_id=payload.id, relation="motivates",
        source=payload.source, owner=payload.owner, confidence=1.0,
        created_at=datetime.now(timezone.utc),
    ))
    db.commit()
    return node_payload(node)


def create_domain_node(kind: str, payload: NodeRequest, db: Session) -> dict[str, Any]:
    node = add_node(db, node_id=payload.id, kind=kind, title=payload.title, owner=payload.owner, source=payload.source)
    db.commit()
    return node_payload(node)


@app.post("/assumptions", status_code=status.HTTP_201_CREATED)
def create_assumption(payload: NodeRequest, db: Session = Depends(get_db)):
    return create_domain_node("assumption", payload, db)


@app.post("/constraints", status_code=status.HTTP_201_CREATED)
def create_constraint(payload: NodeRequest, db: Session = Depends(get_db)):
    return create_domain_node("constraint", payload, db)


@app.post("/decisions", status_code=status.HTTP_201_CREATED)
@app.post("/adrs", status_code=status.HTTP_201_CREATED)
def create_decision(payload: NodeRequest, db: Session = Depends(get_db)):
    return create_domain_node("decision", payload, db)


@app.post("/requirements", status_code=status.HTTP_201_CREATED)
def create_requirement(payload: NodeRequest, db: Session = Depends(get_db)):
    return create_domain_node("requirement", payload, db)


@app.post("/tasks", status_code=status.HTTP_201_CREATED)
def create_task(payload: TaskRequest, response: Response, db: Session = Depends(get_db)):
    existing = db.scalar(select(TaskRow).where(TaskRow.idempotency_key == payload.idempotency_key))
    if existing:
        response.status_code = status.HTTP_200_OK
        return {"id": existing.id, "title": existing.title, "status": existing.status}
    if payload.objective_id and not db.get(GraphNodeRow, payload.objective_id):
        raise HTTPException(status_code=404, detail="objective not found")
    add_node(db, node_id=payload.id, kind="task", title=payload.title, owner="sdf", source="api")
    task = TaskRow(
        id=payload.id, title=payload.title, status="created", idempotency_key=payload.idempotency_key,
        acceptance_criteria=payload.acceptance_criteria, created_at=datetime.now(timezone.utc),
    )
    db.add(task)
    if payload.objective_id:
        db.add(DecisionEdgeRow(
            source_kind="objective", source_id=payload.objective_id,
            target_kind="task", target_id=payload.id, relation="implements",
            source="api", owner="sdf", confidence=1.0, created_at=datetime.now(timezone.utc),
        ))
    db.commit()
    return {"id": task.id, "title": task.title, "status": task.status}


@app.get("/tasks/{task_id}/trace")
def get_task_trace(task_id: str, db: Session = Depends(get_db)):
    if not db.get(TaskRow, task_id):
        raise HTTPException(status_code=404, detail="task not found")
    nodes: dict[str, GraphNodeRow] = {}
    edges: list[DecisionEdgeRow] = []
    seen_edges: set[int] = set()
    queue = deque([("task", task_id)])
    seen = set(queue)
    while queue:
        kind, node_id = queue.popleft()
        node = db.get(GraphNodeRow, node_id)
        if node:
            nodes[node.id] = node
        related = db.scalars(select(DecisionEdgeRow).where(
            ((DecisionEdgeRow.target_id == node_id) & (DecisionEdgeRow.target_kind == kind))
            | ((DecisionEdgeRow.source_id == node_id) & (DecisionEdgeRow.source_kind == kind))
        ))
        for edge in related:
            if edge.id in seen_edges:
                continue
            seen_edges.add(edge.id)
            edges.append(edge)
            related_node = (edge.source_kind, edge.source_id) if edge.target_id == node_id and edge.target_kind == kind else (edge.target_kind, edge.target_id)
            if related_node not in seen:
                seen.add(related_node)
                queue.append(related_node)
    ordered_nodes = list(nodes.values())
    attempt_ids = db.scalars(select(AttemptRow.id).where(AttemptRow.task_id == task_id)).all()
    runtime_events = db.scalars(
        select(RuntimeEventRow)
        .where(RuntimeEventRow.attempt_id.in_(attempt_ids))
        .order_by(RuntimeEventRow.attempt_id, RuntimeEventRow.sequence)
    ).all()
    return {
        "nodes": [node_payload(n) for n in ordered_nodes],
        "edges": [edge_payload(e) for e in edges],
        "runtime_events": [
            {
                "source": event.source,
                "attempt_id": event.attempt_id,
                "session_id": event.session_id,
                "sequence": event.sequence,
                "kind": event.kind,
                "status": event.status,
                "payload": event.payload,
                "created_at": event.created_at,
            }
            for event in runtime_events
        ],
    }


@app.get("/evidence/{evidence_id}", response_model=EvidenceResponse)
def get_evidence(evidence_id: str, db: Session = Depends(get_db)):
    evidence = db.get(EvidenceRow, evidence_id)
    if evidence is None:
        raise HTTPException(status_code=404, detail="evidence not found")
    node = db.get(GraphNodeRow, evidence_id)
    criterion = evidence.criterion
    return {
        "id": evidence.id,
        "attempt_id": evidence.attempt_id,
        "kind": evidence.kind,
        "status": evidence.status,
        "command": evidence.command,
        "exit_code": evidence.exit_code,
        "artifact_ref": evidence.artifact_ref,
        "confidence": evidence.confidence,
        "criterion": criterion,
    }


@app.get("/attempts/{attempt_id}/runtime-events")
def get_runtime_events(attempt_id: str, db: Session = Depends(get_db)):
    if db.get(AttemptRow, attempt_id) is None:
        raise HTTPException(status_code=404, detail="attempt not found")
    rows = db.scalars(
        select(RuntimeEventRow)
        .where(RuntimeEventRow.attempt_id == attempt_id)
        .order_by(RuntimeEventRow.sequence)
    ).all()
    return [
        {
            "source": row.source,
            "attempt_id": row.attempt_id,
            "session_id": row.session_id,
            "sequence": row.sequence,
            "kind": row.kind,
            "status": row.status,
            "payload": row.payload,
            "created_at": row.created_at,
        }
        for row in rows
    ]


@app.get("/scorecard")
def get_scorecard(db: Session = Depends(get_db)):
    """Return the deterministic local scorecard; it is not production impact proof."""

    return build_scorecard_from_db(db).as_dict()


@app.post("/attempts/{attempt_id}/actions")
def execute_tool_action(attempt_id: str, payload: ToolActionRequest, db: Session = Depends(get_db)):
    """Execute one explicit, Attempt-bound Tool Proxy action.

    The endpoint accepts only structured fields.  Authorization comes from
    server configuration, never from the request body, and a missing
    workspace or inactive Attempt fails closed.
    """

    attempt = db.get(AttemptRow, attempt_id)
    if attempt is None:
        raise HTTPException(status_code=404, detail="attempt not found")
    workspace = Path(".sdf-workspaces") / attempt_id
    if not workspace.is_dir():
        raise HTTPException(status_code=409, detail="attempt workspace not found")
    service = ExecutionService(
        db,
        workspace_root=Path(".sdf-workspaces"),
        artifact_root=Path(".sdf-artifacts"),
    )
    request = ActionRequest(
        attempt_id=attempt_id,
        actor=payload.actor,
        tool=payload.tool,
        action=payload.action,
        resource=payload.resource,
        context=payload.context,
    )
    containment_backend, require_containment = configured_containment()
    result = service.execute_tool(
        attempt_id=attempt_id,
        request=request,
        policy=configured_tool_policy(),
        containment_backend=containment_backend,
        require_containment=require_containment,
    )
    db.commit()
    value = result.value
    if value is not None and not isinstance(value, (str, int, float, bool, list, dict)):
        value = str(value)
    return {
        "attempt_id": attempt_id,
        "action_id": request.action_id,
        "status": result.status.value,
        "effect": result.decision.effect.value,
        "reason": result.decision.reason,
        "value": value,
        "error": result.error,
    }


@app.post("/tasks/{task_id}/run")
def run_task(task_id: str, payload: RunTaskRequest, db: Session = Depends(get_db)):
    if not db.get(TaskRow, task_id):
        raise HTTPException(status_code=404, detail="task not found")
    fixture = Path(payload.fixture_dir)
    if not fixture.is_dir():
        raise HTTPException(status_code=400, detail="fixture_dir must be an existing directory")
    target = None
    if payload.validation_target_kind and payload.validation_target_id:
        target = (payload.validation_target_kind, payload.validation_target_id)
        target_node = db.get(GraphNodeRow, payload.validation_target_id)
        if target_node is None or target_node.kind != payload.validation_target_kind:
            raise HTTPException(status_code=404, detail="validation target not found")
    service = ExecutionService(
        db,
        workspace_root=Path(".sdf-workspaces"),
        artifact_root=Path(".sdf-artifacts"),
        adapter=FakeNativeAdapter("app.py", "print('updated')\n"),
    )
    try:
        attempt = service.run(
            task_id=task_id,
            dispatch_key=payload.dispatch_key or f"http-{uuid4().hex}",
            fixture=fixture,
            instructions=payload.instructions,
            commands=payload.commands,
            criterion_checks=payload.criterion_checks,
            validation_target=target,
            parent_attempt_id=payload.parent_attempt_id,
            model_tier=payload.model_tier,
            cost_usd=payload.cost_usd,
            provider=payload.provider,
            latency_ms=payload.latency_ms,
            input_tokens=payload.input_tokens,
            output_tokens=payload.output_tokens,
            escalation_reason=payload.escalation_reason,
        )
    except (ValueError, FileExistsError) as exc:
        raise HTTPException(status_code=409, detail=str(exc)) from exc
    task = db.get(TaskRow, task_id)
    return {
        "task_id": task_id,
        "task_status": task.status,
        "attempt_id": attempt.id,
        "attempt_status": attempt.status,
        "parent_attempt_id": attempt.parent_attempt_id,
        "model_tier": attempt.model_tier,
        "cost_usd": attempt.cost_usd,
        "provider": attempt.provider,
        "latency_ms": attempt.latency_ms,
        "input_tokens": attempt.input_tokens,
        "output_tokens": attempt.output_tokens,
        "escalation_reason": attempt.escalation_reason,
    }
