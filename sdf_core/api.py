from __future__ import annotations

import os
from collections import deque
from datetime import datetime, timezone
from pathlib import Path
from typing import Annotated, Any, Generator, Literal
from uuid import uuid4

from contextlib import asynccontextmanager

from fastapi import Depends, FastAPI, HTTPException, Request, Response, status
from pydantic import BaseModel, ConfigDict, Field, StringConstraints
from sqlalchemy import select
from sqlalchemy.orm import Session

from .db import (
    AttemptRow,
    Base,
    DecisionEdgeRow,
    EvidenceRow,
    GraphNodeRow,
    ObjectiveMetricRow,
    OutcomeObservationRow,
    RuntimeEventRow,
    TaskRow,
    make_engine,
    make_session_factory,
)
from .escalation import ModelTier
from .adapter import FakeNativeAdapter
from .execution import ExecutionService
from .credentials import CredentialConfigError
from .measurement import build_scorecard_from_db
from .run_adapter import RunBusyError, configured_run_adapter, run_slot
from .observability import configure_logging, get_logger, log_context
from . import tracing
from .timeline import build_timeline
from .policy import ActionRequest, AllowlistPolicy
from .containment import MacOSSandboxBackend
from .checks import CheckConfigError, CheckRegistry, UnknownCheckError
from .evaluator import DeterministicEvaluator
from .e2b_containment import E2BContainmentBackend
from .impact import MetricDeclaration, build_scorecard, measure_objective
from .impact_repository import load_metric_declaration, load_observations, load_scorecard_facts
from . import auth as _auth
from .db import AuthAuditRow


DATABASE_URL = os.getenv("SDF_DATABASE_URL", "sqlite+pysqlite:///:memory:")
engine = make_engine(DATABASE_URL)
Base.metadata.create_all(engine)
SessionLocal = make_session_factory(DATABASE_URL)



def get_db() -> Generator[Session, None, None]:
    db = SessionLocal()
    try:
        yield db
    finally:
        db.close()


# --- ADR 0015: authentication and authorization ---------------------------------
#
# Every route requires a bearer token except ``GET /healthz``.  ``authorize`` is
# an app-level dependency, so every route registered on ``app`` (including
# future ones) passes through it; a route missing from ``auth.ROUTE_ROLES``
# needs viewer for safe methods and admin otherwise (fail closed).

auth_failures = _auth.limiter_from_env()


def _client_host(request: Request) -> str:
    return (request.client.host if request.client else "") or "unknown"


def _audit(db: Session, request: Request, *, event: str, principal: "_auth.Principal | None",
           status_code: int | None, route: str | None, detail: dict[str, Any] | None = None) -> None:
    db.add(AuthAuditRow(
        at=datetime.now(timezone.utc), event=event, principal=principal.name if principal else None,
        token_id=principal.token_id if principal else None, method=request.method,
        path=request.url.path[:500], route=(route or "")[:200] or None, status_code=status_code,
        client=_client_host(request)[:64], detail=detail,
    ))
    db.commit()


def _deny(db: Session, request: Request, route: str, status_code: int, reason: str, detail: str,
          principal: "_auth.Principal | None" = None, headers: dict[str, str] | None = None) -> HTTPException:
    try:
        db.rollback()
        _audit(db, request, event="auth_failed" if status_code == 401 else "denied", principal=principal,
               status_code=status_code, route=route, detail={"reason": reason})
    except Exception:  # noqa: BLE001 - auditing must not turn a denial into a 500
        db.rollback()
    return HTTPException(status_code=status_code, detail=detail, headers=headers)


def _authenticate(request: Request, db: Session, route: str) -> "_auth.Principal":
    client = _client_host(request)
    retry = auth_failures.retry_after(client)
    if retry:
        raise _deny(db, request, route, 429, "locked_out", "too many failed authentication attempts",
                    headers={"Retry-After": str(retry)})
    header = request.headers.get("authorization", "")
    scheme, _, presented = header.partition(" ")
    if not header and _auth.dev_insecure():
        server = (request.scope.get("server") or ("",))[0]
        forwarded = any(h in request.headers for h in ("x-forwarded-for", "forwarded", "x-real-ip"))
        if _auth.is_loopback(client) and _auth.is_loopback(server) and not forwarded:
            return _auth.DEV_PRINCIPAL
        raise _deny(db, request, route, 403, "dev_not_loopback",
                    "SDF_AUTH_DEV_INSECURE only serves direct requests to 127.0.0.1")
    key = _auth.pepper()
    if key is None:
        raise HTTPException(status_code=503, detail=(
            "authentication is not configured: set SDF_AUTH_PEPPER and create a token "
            "(python -m sdf_core.auth_cli create), or SDF_AUTH_DEV_INSECURE=1 on 127.0.0.1"))
    challenge = {"WWW-Authenticate": "Bearer"}
    if scheme.lower() != "bearer" or not presented.strip():
        auth_failures.failure(client)
        raise _deny(db, request, route, 401, "missing", "a bearer token is required", headers=challenge)
    try:
        principal = _auth.verify_token(db, presented.strip(), key=key)
    except _auth.AuthError as exc:
        auth_failures.failure(client)
        raise _deny(db, request, route, 401, str(exc), "invalid, expired or revoked token",
                    headers=challenge) from None
    db.commit()  # last_used_at
    return principal


def authorize(request: Request, db: Session = Depends(get_db)):
    route_obj = request.scope.get("route")
    route = getattr(route_obj, "path", request.url.path)
    method = request.method.upper()
    if (method, route) in _auth.PUBLIC_ROUTES:
        yield None
        return
    principal = _authenticate(request, db, route)
    needed = _auth.required_role(method, route)
    if not principal.has(needed):
        raise _deny(db, request, route, 403, f"needs_{needed}", f"this action needs the {needed} role",
                    principal=principal)
    if needed == "approver" and not principal.can_approve:
        raise _deny(db, request, route, 403, "not_human_approver",
                    "only a human, non-agent identity may approve or decide", principal=principal)
    request.state.principal = principal
    if method in _auth.SAFE_METHODS:
        yield principal
        return
    status_code = getattr(route_obj, "status_code", None) or 200
    try:
        yield principal
    except HTTPException as exc:
        status_code = exc.status_code
        raise
    except Exception:
        status_code = 500
        raise
    finally:
        try:
            db.rollback()
            _audit(db, request, event="mutation", principal=principal, status_code=status_code, route=route,
                   detail={"roles": sorted(principal.roles), "method": principal.method,
                           "path_params": dict(request.path_params)})
        except Exception:  # noqa: BLE001
            db.rollback()


def current_principal(request: Request) -> "_auth.Principal":
    principal = getattr(request.state, "principal", None)
    if principal is None:  # unreachable for non-public routes; fail closed anyway
        raise HTTPException(status_code=401, detail="not authenticated")
    return principal


@asynccontextmanager
async def _lifespan(_app: FastAPI):
    if _auth.pepper() is None and not _auth.dev_insecure():
        raise RuntimeError(
            "SDF Core refuses to start without authentication: set SDF_AUTH_PEPPER (>= 32 chars) and create "
            "a token with `python -m sdf_core.auth_cli create`, or set SDF_AUTH_DEV_INSECURE=1 and bind 127.0.0.1")
    configure_logging()
    # ADR 0024: off unless SDF_OTEL_ENABLED; spans never leave SDF Core for a sandbox.
    # SDF_OTEL_* / OTEL_* may live in the repo-root .env like every other setting.
    otel_env = _operator_env()
    owned = not tracing.enabled() and tracing.configure_tracing(otel_env)
    if owned:
        tracing.instrument_fastapi(_app)
        # SessionLocal owns its own engine; the instrumentor is a singleton, so pass both at once.
        tracing.instrument_sqlalchemy([engine, SessionLocal.kw.get("bind")], otel_env)
    try:
        yield
    finally:
        if owned:
            tracing.shutdown()  # uninstrument app and DB, flush queued spans


app = FastAPI(title="SDF Core", version="0.1.0", lifespan=_lifespan, dependencies=[Depends(authorize)],
              docs_url=None, redoc_url=None, openapi_url=None)


_log = get_logger("api")


@app.middleware("http")
async def _request_context(request: Request, call_next):
    # ADR 0023: every log line of a request carries its request id.
    supplied = request.headers.get("x-request-id", "")
    request_id = supplied if supplied.isascii() and 0 < len(supplied) <= 64 and supplied.replace("-", "").isalnum() \
        else uuid4().hex
    with log_context(request_id=request_id):
        tracing.set_attributes(request_id=request_id)
        response = await call_next(request)
    response.headers["X-Request-ID"] = request_id
    return response


def fixtures_root() -> Path:
    """The only directory tree ``fixture_dir`` may point into (``SDF_FIXTURES_ROOT``)."""

    return Path(os.environ.get("SDF_FIXTURES_ROOT") or Path.cwd() / "fixtures").resolve()


def resolve_fixture(raw: str) -> Path:
    """Resolve ``fixture_dir`` (symlinks included) and keep it inside the fixtures root."""

    root = fixtures_root()
    try:
        candidate = Path(raw)
        resolved = (candidate if candidate.is_absolute() else root / candidate).resolve(strict=True)
    except (OSError, RuntimeError, ValueError):
        raise HTTPException(status_code=400, detail="fixture_dir must be an existing directory under SDF_FIXTURES_ROOT")
    if not resolved.is_dir() or not resolved.is_relative_to(root):
        raise HTTPException(status_code=400, detail="fixture_dir must be an existing directory under SDF_FIXTURES_ROOT")
    # A fixture inside a workspace or artifact tree is agent-writable, and its
    # sdf-checks.json would be too (ADR 0025).
    if any(resolved == write_root or resolved.is_relative_to(write_root) for write_root in server_write_roots()):
        raise HTTPException(status_code=400, detail="fixture_dir must not be under a workspace or artifact root")
    return resolved


@app.get("/healthz")
def healthz():
    return {"status": "ok"}


@app.get("/auth/whoami")
def whoami(principal: "_auth.Principal" = Depends(current_principal)):
    return {**principal.as_record(), "can_approve": principal.can_approve,
            "single_user_mode": _auth.single_user_mode()}


@app.get("/openapi.json", include_in_schema=False)
def openapi_schema():
    return app.openapi()


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


CheckId = Annotated[str, StringConstraints(pattern=r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,119}$")]


class RunTaskRequest(BaseModel):
    # Unknown fields are rejected, so the removed ``commands`` field and the
    # old argv form of ``criterion_checks`` fail loudly instead of being ignored.
    model_config = ConfigDict(extra="forbid")

    dispatch_key: str | None = Field(default=None, min_length=1, max_length=200)
    fixture_dir: str
    instructions: str = "complete the task"
    criterion_checks: dict[str, list[CheckId]] | None = Field(
        default=None,
        description=(
            "Map each acceptance criterion to server-registered check ids (ADR 0025): ids come from the "
            "fixture's sdf-checks.json or SDF_EVALUATOR_CHECKS_FILE. Callers cannot supply commands."
        ),
    )
    validation_target_kind: str | None = None
    validation_target_id: str | None = None
    parent_attempt_id: str | None = Field(default=None, description=(
        "Retry/escalate a finished Attempt of the same Task that did not pass every check."))
    model_tier: ModelTier | None = Field(default=None, description=(
        "Declared routing tier (not measured). Default basic; a retry defaults to the parent's tier "
        "and may not go below it."))
    # cost_usd, provider, latency_ms, input_tokens, output_tokens and
    # escalation_reason were removed (#50, ADR 0027): they are measured by the
    # server, and extra="forbid" rejects a request that still sends them (or
    # any other telemetry name such as error_class, which was never accepted).


class ToolActionRequest(BaseModel):
    """Structured Tool Proxy input; terminal text is intentionally absent."""

    actor: str = Field(min_length=1, max_length=120)
    tool: str = Field(min_length=1, max_length=120)
    action: str = Field(min_length=1, max_length=120)
    resource: str = Field(min_length=1, max_length=500)
    context: dict[str, Any] = Field(default_factory=dict)


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


EVALUATOR_MODES = ("off", "prefer", "require")


def evaluator_mode(*, live: bool) -> str:
    """``SDF_EVALUATOR_CONTAINMENT``; unset means ``require`` for a live agent, ``off`` otherwise."""

    raw = os.getenv("SDF_EVALUATOR_CONTAINMENT")
    mode = (raw or "").strip().lower() or ("require" if live else "off")
    if mode not in EVALUATOR_MODES:
        raise HTTPException(status_code=400, detail="SDF_EVALUATOR_CONTAINMENT must be off, prefer or require")
    return mode


def configured_evaluator(registry: CheckRegistry | None = None, *, live: bool = False) -> DeterministicEvaluator:
    """Evaluator for public runs and plan runs (ADR 0025).

    ``SDF_EVALUATOR_CONTAINMENT``: ``off`` runs checks on the host with a
    reduced environment and hardened argv; ``prefer`` uses the promoted
    containment backend (``SDF_CONTAINMENT_BACKEND`` +
    ``SDF_CONTAINMENT_SMOKE=passed``) when there is one and otherwise runs on
    the host with a warning; ``require`` never runs a check outside it
    (INCONCLUSIVE).  Unset, the mode is ``require`` when the workspace was
    written by a live agent (``live=True``: ``SDF_RUN_ADAPTER=live`` and every
    plan run) and ``off`` for the deterministic fake adapter.  Every Evidence
    records where its check ran (``runner``).
    """

    mode = evaluator_mode(live=live)
    backend = configured_containment()[0] if mode in {"prefer", "require"} else None
    if backend is None and mode != "require" and live:
        _log.warning("evaluator_runs_on_host", extra={
            "mode": mode, "reason": "no promoted containment backend" if mode == "prefer" else "mode off",
        })
    return DeterministicEvaluator(
        containment_backend=backend,
        require_containment=mode == "require",
        timeout_for=registry.slot_timeout if registry is not None else None,
    )


def server_write_roots() -> tuple[Path, ...]:
    """Trees an agent run writes into; a check source there is not server-controlled."""

    raw = [os.getenv("SDF_WORKSPACE_ROOT") or ".sdf-workspaces", ".sdf-workspaces",
           os.getenv("SDF_ARTIFACT_ROOT") or ".sdf-artifacts", ".sdf-artifacts"]
    return tuple(dict.fromkeys(Path(item).resolve() for item in raw))


def resolve_registered_checks(
    fixture: Path, criterion_checks: dict[str, list[str]] | None
) -> tuple[CheckRegistry | None, dict[str, list[list[str]]] | None]:
    """Turn requested check ids into server-owned argv, before any sandbox starts."""

    if criterion_checks is None:
        return None, None
    try:
        registry = CheckRegistry.load(fixture, forbidden_roots=server_write_roots())
        return registry, registry.resolve(criterion_checks)
    except CheckConfigError as exc:
        raise HTTPException(status_code=400, detail=f"check registry error: {exc}") from exc
    except UnknownCheckError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc


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


@app.get("/runs/{run_id}/timeline")
def run_timeline(run_id: str, db: Session = Depends(get_db)):
    """Merged, redacted timeline for an agent run id or an Attempt id (ADR 0023)."""

    timeline = build_timeline(db, run_id)
    if timeline is None:
        raise HTTPException(status_code=404, detail="no agent run or attempt with that id")
    return timeline


@app.post("/tasks/{task_id}/run")
def run_task(task_id: str, payload: RunTaskRequest, request: Request, db: Session = Depends(get_db)):
    if not db.get(TaskRow, task_id):
        raise HTTPException(status_code=404, detail="task not found")
    fixture = resolve_fixture(payload.fixture_dir)
    target = None
    if payload.validation_target_kind and payload.validation_target_id:
        target = (payload.validation_target_kind, payload.validation_target_id)
        target_node = db.get(GraphNodeRow, payload.validation_target_id)
        if target_node is None or target_node.kind != payload.validation_target_kind:
            raise HTTPException(status_code=404, detail="validation target not found")
    registry, checks = resolve_registered_checks(fixture, payload.criterion_checks)
    principal = getattr(getattr(request.state, "principal", None), "name", None)
    adapter = None
    with log_context(task_id=task_id, principal=principal), \
            tracing.span("sdf.task.run", task_id=task_id, principal=principal) as task_span:
        try:
            # One handler for every configuration error (adapter, provider,
            # credentials): it is the operator's to fix, so 400, before any sandbox.
            adapter = configured_run_adapter(db)
            with run_slot(adapter):
                service = ExecutionService(
                    db,
                    workspace_root=Path(".sdf-workspaces"),
                    artifact_root=Path(".sdf-artifacts"),
                    adapter=adapter,
                    evaluator=configured_evaluator(registry, live=not isinstance(adapter, FakeNativeAdapter)),
                )
                attempt = service.run(
                    task_id=task_id,
                    dispatch_key=payload.dispatch_key or f"http-{uuid4().hex}",
                    fixture=fixture,
                    instructions=payload.instructions,
                    commands=[],
                    criterion_checks=checks,
                    validation_target=target,
                    parent_attempt_id=payload.parent_attempt_id,
                    model_tier=payload.model_tier,
                    # ADR 0014: a Task that belongs to a Plan never starts before a
                    # human approved the Plan; SDF_REQUIRE_PLAN_APPROVAL=1 also
                    # refuses Tasks that have no Plan at all.
                    require_plan_approval=require_plan_approval(),
                )
        except CredentialConfigError as exc:
            raise HTTPException(status_code=400, detail=str(exc)) from exc
        except RunBusyError as exc:
            raise HTTPException(status_code=429, detail=str(exc), headers={"Retry-After": "30"}) from exc
        except (ValueError, FileExistsError) as exc:
            raise HTTPException(status_code=409, detail=str(exc)) from exc
        except Exception as exc:
            last = getattr(adapter, "last_run", None) or {}
            tracing.mark_error(last.get("error_class") or "unknown", task_span)
            _log.error("run_failed", extra={"error_type": type(exc).__name__, "error_class": last.get("error_class")})
            if not last:
                raise
            # The Attempt is already recorded FAILED with its ledger rows.
            raise HTTPException(status_code=502, detail={
                "error": "agent run failed", "error_class": last.get("error_class"),
                "attempt_id": last.get("attempt_id"), "sandbox_id": last.get("sandbox_id"),
            }) from exc
        tracing.set_attributes(task_span, attempt_id=attempt.id, status=attempt.status)
    task = db.get(TaskRow, task_id)
    return {
        "task_id": task_id,
        "task_status": task.status,
        "attempt_id": attempt.id,
        "attempt_status": attempt.status,
        "trace_id": attempt.trace_id,
        "parent_attempt_id": attempt.parent_attempt_id,
        "model_tier": attempt.model_tier,
        "cost_usd": attempt.cost_usd,
        "provider": attempt.provider,
        "latency_ms": attempt.latency_ms,
        "input_tokens": attempt.input_tokens,
        "output_tokens": attempt.output_tokens,
        "escalation_reason": attempt.escalation_reason,
        "error_class": attempt.error_class,
    }


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
def record_outcome_observation(objective_id: str, payload: ObservationRequest, request: Request,
                               db: Session = Depends(get_db)):
    get_objective_node(db, objective_id)
    # ADR 0027 §6: observations are caller-reported, not measured.  The row has
    # no principal column (out of scope for #50); the audit log line says who.
    principal = getattr(getattr(request.state, "principal", None), "name", None)
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
    _log.info("observation_recorded", extra={"observation_id": observation_id, "objective_id": objective_id,
                                             "principal": principal, "mode": payload.mode})
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


@app.get("/impact-scorecard")
def get_impact_scorecard(db: Session = Depends(get_db)):
    facts = load_scorecard_facts(db)
    return build_scorecard(facts).as_dict()


# --- ADR 0014: plans, plan-approval gate, agent runs, messages, decisions ---------
#
# The lead/worker coordinator is exposed on its own endpoints rather than on
# ``POST /tasks/{id}/run``: that endpoint runs one Task synchronously through a
# single NativeAdapter (the fake adapter here), while an agent run spans every
# Task of a Plan, several sandboxes and minutes of wall time.  The gate itself
# is shared: ``/tasks/{id}/run`` refuses a Task whose Plan is not approved.

import threading as _threading  # noqa: E402

from .agent_sandbox import agent_environ, e2b_factory, running_sandbox_ids  # noqa: E402
from .credentials import load_dotenv  # noqa: E402
from .coordinator import Coordinator, RunConfig, ServerConfigError, request_cancel  # noqa: E402
from .db import (  # noqa: E402
    AgentClaimRow,
    AgentMessageRow,
    AgentRunMemberRow,
    AgentRunRow,
    DecisionRequestRow,
    PlanRow,
)
from .plans import (  # noqa: E402
    PlanSpec,
    SeparationOfDutiesError,
    decide_plan,
    decision_request_payload,
    plan_payload,
    propose_plan,
    resolve_request,
)


def require_plan_approval() -> bool:
    return os.getenv("SDF_REQUIRE_PLAN_APPROVAL", "").strip().lower() in {"1", "true", "yes"}


class PlanDecisionRequest(BaseModel):
    # The decider is the authenticated principal (ADR 0015); a client-supplied
    # ``decided_by`` is not a field and is ignored like any unknown key.
    rationale: str = Field(default="", max_length=4000)
    expected_sha256: str | None = Field(default=None, min_length=64, max_length=64)


class StartRunRequest(BaseModel):
    config: dict[str, Any] = Field(default_factory=dict)
    wait: bool = False


class ResolveDecisionRequest(BaseModel):
    outcome: Literal["approved", "rejected", "answered"]
    # The resolver is the authenticated principal; ``resolved_by`` is ignored.
    resolution: str = Field(default="", max_length=4000)


def _gate(exc: Exception) -> HTTPException:
    return HTTPException(status_code=409, detail=str(exc))


@app.post("/plans", status_code=status.HTTP_201_CREATED)
def create_plan(payload: PlanSpec, db: Session = Depends(get_db),
                principal: _auth.Principal = Depends(current_principal)):
    resolve_fixture(payload.fixture_dir)
    # The proposer is the authenticated principal, whatever the body says.
    payload = payload.model_copy(update={"proposed_by": principal.name})
    try:
        plan = propose_plan(db, payload)
        db.commit()
    except LookupError as exc:
        db.rollback()
        raise HTTPException(status_code=404, detail=str(exc)) from exc
    except ValueError as exc:
        db.rollback()
        raise HTTPException(status_code=409, detail=str(exc)) from exc
    return plan_payload(plan, db)


@app.get("/plans/{plan_id}")
def get_plan(plan_id: str, db: Session = Depends(get_db)):
    plan = db.get(PlanRow, plan_id)
    if plan is None:
        raise HTTPException(status_code=404, detail="plan not found")
    return plan_payload(plan, db)


def _decide(plan_id: str, payload: PlanDecisionRequest, approve: bool, db: Session,
            principal: _auth.Principal) -> dict[str, Any]:
    try:
        plan = decide_plan(db, plan_id, approve=approve, decided_by=principal.name,
                           rationale=payload.rationale, expected_sha256=payload.expected_sha256,
                           allow_self_approval=_auth.single_user_mode(), principal=principal.as_record())
        db.commit()
    except LookupError as exc:
        db.rollback()
        raise HTTPException(status_code=404, detail=str(exc)) from exc
    except SeparationOfDutiesError as exc:
        db.rollback()
        raise HTTPException(status_code=403, detail=str(exc)) from exc
    except ValueError as exc:  # PlanGateError included
        db.rollback()
        raise _gate(exc) from exc
    return plan_payload(plan, db)


@app.post("/plans/{plan_id}/approve")
def approve_plan(plan_id: str, payload: PlanDecisionRequest, db: Session = Depends(get_db),
                 principal: _auth.Principal = Depends(current_principal)):
    return _decide(plan_id, payload, True, db, principal)


@app.post("/plans/{plan_id}/reject")
def reject_plan(plan_id: str, payload: PlanDecisionRequest, db: Session = Depends(get_db),
                principal: _auth.Principal = Depends(current_principal)):
    return _decide(plan_id, payload, False, db, principal)


def _agent_factory():
    factory = getattr(app.state, "agent_sandbox_factory", None)
    if factory is not None:
        return factory
    if os.getenv("SDF_AGENT_SANDBOX", "").strip().lower() == "e2b":
        return e2b_factory(agent_environ(_operator_env(), {"opencode", "pi"}))
    return None


def _operator_env() -> dict[str, str]:
    """os.environ plus the repo-root .env, in a private mapping (values never logged)."""
    env = dict(os.environ)
    load_dotenv(environ=env)
    return env


def _cleanup_check():
    check = getattr(app.state, "agent_cleanup_check", None)
    if check is not None:
        return check
    key = _operator_env().get("E2B_API_KEY")
    if os.getenv("SDF_AGENT_SANDBOX", "").strip().lower() == "e2b" and key:
        return lambda ids: ids & running_sandbox_ids(key)
    return None


def factory_evidence_mode(factory: Any) -> str:
    """``live`` only for a factory that builds real agents (``e2b_factory``)."""
    return "live" if getattr(factory, "evidence_mode", None) == "live" else "synthetic"


def _coordinator(db: Session) -> Coordinator:
    factory = _agent_factory()
    if factory is None:
        raise HTTPException(status_code=503, detail="no agent sandbox factory configured (set SDF_AGENT_SANDBOX=e2b)")
    return Coordinator(
        db, factory=factory,
        workspace_root=Path(os.getenv("SDF_WORKSPACE_ROOT", ".sdf-workspaces")),
        artifact_root=Path(os.getenv("SDF_ARTIFACT_ROOT", ".sdf-artifacts")),
        cleanup_check=_cleanup_check(),
        # Plan workspaces are always written by live agents (ADR 0025).
        evaluator=configured_evaluator(live=True),
    )


def run_payload(run: AgentRunRow, db: Session) -> dict[str, Any]:
    members = db.scalars(select(AgentRunMemberRow).where(AgentRunMemberRow.run_id == run.id)).all()
    return {
        "id": run.id, "plan_id": run.plan_id, "status": run.status, "error": run.error, "cost_usd": run.cost_usd,
        "config": run.config, "summary": run.summary, "started_by": run.started_by,
        "cancel_requested_by": run.cancel_requested_by, "trace_id": run.trace_id,
        "started_at": run.started_at.isoformat() if run.started_at else None,
        "finished_at": run.finished_at.isoformat() if run.finished_at else None,
        "members": [{"agent_id": m.agent_id, "role": m.role, "agent_kind": m.agent_kind, "subtask_key": m.subtask_key,
                     "sandbox_id": m.sandbox_id, "status": m.status, "turns": m.turns} for m in members],
    }


@app.post("/plans/{plan_id}/runs", status_code=status.HTTP_202_ACCEPTED)
def start_agent_run(plan_id: str, payload: StartRunRequest, db: Session = Depends(get_db),
                    principal: _auth.Principal = Depends(current_principal)):
    plan = db.get(PlanRow, plan_id)
    if plan is None:
        raise HTTPException(status_code=404, detail="plan not found")
    if plan.status != "approved":
        raise HTTPException(status_code=409, detail=f"plan {plan_id} is {plan.status}; a human must approve it first")
    try:
        # ADR 0027 §4: prices from the server, Evidence mode from the agent factory.
        config = RunConfig.from_request(payload.config, evidence_mode=factory_evidence_mode(_agent_factory()))
    except ServerConfigError as exc:
        raise HTTPException(status_code=503, detail=f"server run configuration is invalid: {exc}") from exc
    except (TypeError, ValueError) as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from exc
    coordinator = _coordinator(db)
    try:
        run = coordinator.create_run(plan_id, config, started_by=principal.name)
    except ValueError as exc:
        db.rollback()
        raise _gate(exc) from exc
    if payload.wait:
        run = coordinator.execute(run.id)
        return run_payload(run, db)
    run_id = run.id
    factory, check, evaluator = coordinator.factory, coordinator.cleanup_check, coordinator.evaluator

    # ADR 0024: the request span ends before the run does, so the run gets its own
    # root span (new trace, linked to the request).  Its trace id is stored on the
    # run now, so the 202 response and `runs show` report the same trace.
    root = tracing.detached_root("sdf.run.background", run_id=run_id, plan_id=plan_id)
    root_trace = tracing.span_trace_id(root)
    if root_trace:
        run.trace_id = root_trace
        db.commit()

    def background() -> None:
        session = SessionLocal()
        try:
            with tracing.activate(root):
                Coordinator(session, factory=factory, workspace_root=coordinator.workspace_root,
                            artifact_root=coordinator.artifact_root, cleanup_check=check,
                            evaluator=evaluator).execute(run_id)
        finally:
            session.close()

    # in_current_context carries the log context; activate() replaces the current span.
    _threading.Thread(target=tracing.in_current_context(background), name=f"sdf-{run_id}", daemon=True).start()
    return run_payload(run, db)


@app.get("/runs/{run_id}")
def get_agent_run(run_id: str, db: Session = Depends(get_db)):
    run = db.get(AgentRunRow, run_id)
    if run is None:
        raise HTTPException(status_code=404, detail="run not found")
    return run_payload(run, db)


@app.post("/runs/{run_id}/cancel")
def cancel_agent_run(run_id: str, db: Session = Depends(get_db),
                     principal: _auth.Principal = Depends(current_principal)):
    run = db.get(AgentRunRow, run_id)
    if run is None:
        raise HTTPException(status_code=404, detail="run not found")
    if run.status == "running" and run.cancel_requested_at is None:
        run.cancel_requested_at = datetime.now(timezone.utc)
        run.cancel_requested_by = principal.name
        db.commit()
    request_cancel(run_id)
    return run_payload(run, db)


@app.get("/runs/{run_id}/messages")
def get_agent_messages(run_id: str, db: Session = Depends(get_db)):
    if db.get(AgentRunRow, run_id) is None:
        raise HTTPException(status_code=404, detail="run not found")
    rows = db.scalars(select(AgentMessageRow).where(AgentMessageRow.run_id == run_id).order_by(AgentMessageRow.seq)).all()
    claims = {c.message_id: c.id for c in db.scalars(select(AgentClaimRow).where(AgentClaimRow.run_id == run_id)).all()}
    return [{"id": m.id, "seq": m.seq, "from": m.from_agent, "from_role": m.from_role, "to": m.to_agent,
             "to_role": m.to_role, "kind": m.kind, "body": m.body, "truncated": m.truncated,
             "attempt_id": m.attempt_id, "source_sandbox_id": m.source_sandbox_id, "meta": m.meta,
             "claim_id": claims.get(m.id), "evidence": False,
             "created_at": m.created_at.isoformat()} for m in rows]


@app.get("/decision-requests")
def list_decision_requests(status_filter: str | None = None, run_id: str | None = None,
                           db: Session = Depends(get_db)):
    query = select(DecisionRequestRow).order_by(DecisionRequestRow.created_at)
    if status_filter:
        query = query.where(DecisionRequestRow.status == status_filter)
    if run_id:
        query = query.where(DecisionRequestRow.run_id == run_id)
    return [decision_request_payload(r) for r in db.scalars(query).all()]


@app.post("/decision-requests/{request_id}/resolve")
def resolve_decision_request(request_id: str, payload: ResolveDecisionRequest, db: Session = Depends(get_db),
                             principal: _auth.Principal = Depends(current_principal)):
    # The HTTP caller is the authenticated human principal (approver role,
    # human token); policies run in-process only.
    try:
        request = resolve_request(db, request_id, outcome=payload.outcome, resolved_by=principal.name,
                                  resolver_kind="human", resolution=payload.resolution,
                                  allow_self_approval=_auth.single_user_mode(), principal=principal.as_record())
        db.commit()
    except LookupError as exc:
        db.rollback()
        raise HTTPException(status_code=404, detail=str(exc)) from exc
    except SeparationOfDutiesError as exc:
        db.rollback()
        raise HTTPException(status_code=403, detail=str(exc)) from exc
    except ValueError as exc:
        db.rollback()
        raise _gate(exc) from exc
    return decision_request_payload(request)
