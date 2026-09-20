from __future__ import annotations

import os
from collections import deque
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Generator, Literal
from uuid import uuid4

from fastapi import Depends, FastAPI, HTTPException, Response, status
from pydantic import BaseModel, Field
from sqlalchemy import select
from sqlalchemy.orm import Session

from .db import Base, DecisionEdgeRow, EvidenceRow, GraphNodeRow, ObjectiveMetricRow, OutcomeObservationRow, TaskRow, make_engine, make_session_factory
from .execution import ExecutionService
from .adapter import FakeNativeAdapter
from .impact import MetricDeclaration, build_scorecard, measure_objective
from .impact_repository import load_metric_declaration, load_observations, load_scorecard_facts


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


class TaskRequest(BaseModel):
    id: str = Field(min_length=1, max_length=120)
    title: str = Field(min_length=1, max_length=500)
    objective_id: str | None = None
    idempotency_key: str = Field(min_length=1, max_length=200)
    acceptance_criteria: list[str] = Field(default_factory=list)


class RunTaskRequest(BaseModel):
    fixture_dir: str
    instructions: str = "complete the task"
    commands: list[list[str]] = Field(default_factory=list)
    criterion_checks: dict[str, list[list[str]]] | None = Field(
        default=None, description="Explicit criterion checks take precedence over legacy commands, including an empty mapping."
    )
    validation_target_kind: str | None = None
    validation_target_id: str | None = None


class MetricDeclarationRequest(BaseModel):
    metric_name: str = Field(min_length=1, max_length=120)
    baseline: float
    target: float
    unit: str = Field(min_length=1, max_length=40)
    source: str = Field(min_length=1, max_length=500)
    owner: str = Field(min_length=1, max_length=120)
    measurement_window_days: int = Field(gt=0)


class ObservationRequest(BaseModel):
    metric_name: str = Field(min_length=1, max_length=120)
    value: float
    observed_at: datetime
    source: str = Field(min_length=1, max_length=500)
    mode: Literal["synthetic", "live"]


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


def declaration_payload(declaration: MetricDeclaration) -> dict[str, Any]:
    return {
        "objective_id": declaration.objective_id,
        "metric_name": declaration.metric_name,
        "baseline": declaration.baseline,
        "target": declaration.target,
        "unit": declaration.unit,
        "source": declaration.source,
        "owner": declaration.owner,
        "measurement_window_days": declaration.measurement_window_days,
        "declared_at": declaration.declared_at.isoformat(),
    }


def get_objective_node(db: Session, objective_id: str) -> GraphNodeRow:
    node = db.get(GraphNodeRow, objective_id)
    if node is None or node.kind != "objective":
        raise HTTPException(status_code=404, detail="objective not found")
    return node


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
    node = add_node(db, node_id=payload.id, kind="objective", title=payload.title, owner=payload.owner, source=payload.source)
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
    return {"nodes": [node_payload(n) for n in ordered_nodes], "edges": [edge_payload(e) for e in edges]}


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
            dispatch_key=f"http-{task_id}-{payload.instructions}",
            fixture=fixture,
            instructions=payload.instructions,
            commands=payload.commands,
            criterion_checks=payload.criterion_checks,
            validation_target=target,
        )
    except (ValueError, FileExistsError) as exc:
        raise HTTPException(status_code=409, detail=str(exc)) from exc
    task = db.get(TaskRow, task_id)
    return {"task_id": task_id, "task_status": task.status, "attempt_id": attempt.id, "attempt_status": attempt.status}


@app.post("/objectives/{objective_id}/metric", status_code=status.HTTP_201_CREATED)
def declare_objective_metric(objective_id: str, payload: MetricDeclarationRequest, db: Session = Depends(get_db)):
    get_objective_node(db, objective_id)
    if db.get(ObjectiveMetricRow, objective_id):
        raise HTTPException(status_code=409, detail=f"metric already declared for objective: {objective_id}")
    try:
        declaration = MetricDeclaration(
            objective_id=objective_id,
            metric_name=payload.metric_name,
            baseline=payload.baseline,
            target=payload.target,
            unit=payload.unit,
            source=payload.source,
            owner=payload.owner,
            measurement_window_days=payload.measurement_window_days,
            declared_at=datetime.now(timezone.utc),
        )
    except ValueError as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from exc
    db.add(ObjectiveMetricRow(
        objective_id=declaration.objective_id,
        metric_name=declaration.metric_name,
        baseline=declaration.baseline,
        target=declaration.target,
        unit=declaration.unit,
        source=declaration.source,
        owner=declaration.owner,
        measurement_window_days=declaration.measurement_window_days,
        declared_at=declaration.declared_at,
    ))
    db.commit()
    return declaration_payload(declaration)


@app.post("/objectives/{objective_id}/observations", status_code=status.HTTP_201_CREATED)
def record_outcome_observation(objective_id: str, payload: ObservationRequest, db: Session = Depends(get_db)):
    get_objective_node(db, objective_id)
    observation_id = f"OBSERVATION-{uuid4().hex[:12]}"
    db.add(OutcomeObservationRow(
        id=observation_id,
        objective_id=objective_id,
        metric_name=payload.metric_name,
        value=payload.value,
        observed_at=payload.observed_at,
        source=payload.source,
        mode=payload.mode,
        created_at=datetime.now(timezone.utc),
    ))
    db.commit()
    return {
        "id": observation_id,
        "objective_id": objective_id,
        "metric_name": payload.metric_name,
        "value": payload.value,
        "observed_at": payload.observed_at.isoformat(),
        "source": payload.source,
        "mode": payload.mode,
    }


@app.get("/objectives/{objective_id}/impact")
def get_objective_impact(objective_id: str, db: Session = Depends(get_db)):
    get_objective_node(db, objective_id)
    declaration = load_metric_declaration(db, objective_id)
    observations = load_observations(db, objective_id)
    impact = measure_objective(objective_id, declaration=declaration, observations=observations)
    return impact.as_dict()


@app.get("/scorecard")
def get_scorecard(db: Session = Depends(get_db)):
    facts = load_scorecard_facts(db)
    return build_scorecard(facts).as_dict()
