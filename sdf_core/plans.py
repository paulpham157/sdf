"""Plan-approval gate and decision requests (ADR 0014).

A Plan lists implementation subtasks, each a Task with its own acceptance
criteria and host-owned criterion checks.  Proposing a Plan opens a pending
plan-approval decision request.  No Attempt on its Tasks and no agent run may
start until a *human* resolves that request as approved; the check is made
here, in ``ExecutionService.begin``, by the ORM ``before_flush`` fence and by
PostgreSQL triggers (migration 0014), so there is no bypass through the API.

Agent ``request_decision`` messages also become pending decision requests.  A
human may approve, reject or answer them; a configured policy may only reject
or answer (never approve), and the default policy resolves nothing.
"""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass
from pathlib import PurePosixPath
from typing import Any, Literal, Protocol
from uuid import uuid4

from pydantic import BaseModel, Field, field_validator, model_validator
from sqlalchemy import select
from sqlalchemy.orm import Session

from .db import (
    DecisionEdgeRow,
    DecisionRequestRow,
    GraphNodeRow,
    PlanGateError,
    PlanRow,
    PlanTaskRow,
    TaskRow,
)
from .model import utcnow

CHECK_EXECUTABLES = frozenset({"python", "python3", "pytest", "ruff"})
_KEY = re.compile(r"[a-z0-9][a-z0-9-]{0,47}")
_ACTOR = re.compile(r"[A-Za-z0-9._@:+-]{1,120}")
_RESERVED_ACTORS = re.compile(r"(?i)(sdf|host|lead(-\d+)?|manager|workers?(-\d+)?|agent:.*|policy:.*)")


class SubtaskSpec(BaseModel):
    key: str
    title: str = Field(min_length=1, max_length=500)
    instructions: str = Field(min_length=1, max_length=8000)
    acceptance_criteria: list[str] = Field(min_length=1, max_length=20)
    criterion_checks: dict[str, list[list[str]]]
    # Only these workspace paths are taken from the worker; everything else
    # is reset to the plan fixture before the Evaluator runs.  Empty = all.
    allowed_paths: list[str] = Field(default_factory=list, max_length=50)
    # Host-owned files (e.g. acceptance tests) always restored from the fixture.
    protected_paths: list[str] = Field(default_factory=list, max_length=50)

    @field_validator("key")
    @classmethod
    def _key(cls, value: str) -> str:
        if not _KEY.fullmatch(value):
            raise ValueError("subtask key must be lowercase letters, digits or '-', at most 48 chars")
        return value

    @field_validator("allowed_paths", "protected_paths")
    @classmethod
    def _paths(cls, values: list[str]) -> list[str]:
        for value in values:
            path = PurePosixPath(value)
            if not value or path.is_absolute() or ".." in path.parts or "\\" in value:
                raise ValueError(f"path must be relative inside the workspace: {value!r}")
        return values

    @model_validator(mode="after")
    def _coverage(self) -> "SubtaskSpec":
        missing = [c for c in self.acceptance_criteria if not self.criterion_checks.get(c)]
        if missing:
            raise ValueError(f"every acceptance criterion needs a host check; missing: {missing}")
        extra = set(self.criterion_checks) - set(self.acceptance_criteria)
        if extra:
            raise ValueError(f"checks for unknown criteria: {sorted(extra)}")
        for checks in self.criterion_checks.values():
            for argv in checks:
                if not argv or PurePosixPath(argv[0]).name not in CHECK_EXECUTABLES:
                    raise ValueError(f"check executable must be one of {sorted(CHECK_EXECUTABLES)}")
        if set(self.allowed_paths) & set(self.protected_paths):
            raise ValueError("a path cannot be both allowed and protected")
        return self


class PlanSpec(BaseModel):
    id: str | None = Field(default=None, pattern=r"^[A-Za-z0-9_-]{1,64}$")
    title: str = Field(min_length=1, max_length=500)
    objective_id: str | None = None
    # Over HTTP this is always replaced by the authenticated principal (ADR 0015).
    proposed_by: str = Field(default="", max_length=120)
    fixture_dir: str = Field(min_length=1, max_length=1000)
    subtasks: list[SubtaskSpec] = Field(min_length=1, max_length=8)

    @model_validator(mode="after")
    def _unique(self) -> "PlanSpec":
        keys = [s.key for s in self.subtasks]
        if len(keys) != len(set(keys)):
            raise ValueError("subtask keys must be unique")
        return self


def content_hash(content: dict[str, Any]) -> str:
    return hashlib.sha256(json.dumps(content, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def task_id_for(plan_id: str, key: str) -> str:
    return f"{plan_id}--{key}"


class SeparationOfDutiesError(PlanGateError):
    """The principal who proposed a plan tried to approve it (ADR 0015)."""


def validate_human(actor: str) -> str:
    actor = (actor or "").strip()
    if not _ACTOR.fullmatch(actor) or _RESERVED_ACTORS.fullmatch(actor):
        raise PlanGateError("a decision must name the human who made it (agent and host identities are refused)")
    return actor


def _edge(db: Session, src: tuple[str, str], dst: tuple[str, str], relation: str, source: str) -> None:
    db.add(DecisionEdgeRow(
        source_kind=src[0], source_id=src[1], target_kind=dst[0], target_id=dst[1], relation=relation,
        source=source, owner="sdf", confidence=1.0, created_at=utcnow(),
    ))


def propose_plan(db: Session, spec: PlanSpec) -> PlanRow:
    if not _ACTOR.fullmatch(spec.proposed_by or ""):
        raise ValueError("a plan needs a proposer (proposed_by)")
    plan_id = spec.id or f"PLAN-{uuid4().hex[:12]}"
    if db.get(PlanRow, plan_id) or db.get(GraphNodeRow, plan_id):
        raise ValueError(f"plan already exists: {plan_id}")
    if spec.objective_id:
        objective = db.get(GraphNodeRow, spec.objective_id)
        if objective is None or objective.kind != "objective":
            raise LookupError("objective not found")
    content = spec.model_dump(exclude={"id"})
    for sub in spec.subtasks:
        task_id = task_id_for(plan_id, sub.key)
        if db.get(TaskRow, task_id) or db.get(GraphNodeRow, task_id):
            raise ValueError(f"task already exists: {task_id}")
    now = utcnow()
    request_id = f"DREQ-{uuid4().hex[:12]}"
    plan = PlanRow(
        id=plan_id, title=spec.title, objective_id=spec.objective_id, status="proposed", content=content,
        content_sha256=content_hash(content), proposed_by=spec.proposed_by, created_at=now,
        decision_request_id=request_id,
    )
    db.add(plan)
    db.add(GraphNodeRow(
        id=plan_id, kind="plan", title=spec.title, source="plans", owner=spec.proposed_by, confidence=1.0,
        created_at=now, metadata_json={"content_sha256": plan.content_sha256, "status": "proposed"},
    ))
    db.add(DecisionRequestRow(
        id=request_id, subject_kind="plan", subject_id=plan_id, question=f"Approve plan {plan_id}: {spec.title}?",
        options=["approve", "reject"], status="pending", requested_by=spec.proposed_by, created_at=now,
    ))
    for sub in spec.subtasks:
        task_id = task_id_for(plan_id, sub.key)
        db.add(TaskRow(
            id=task_id, title=sub.title, status="created", idempotency_key=f"plan:{plan_id}:{sub.key}",
            acceptance_criteria=list(sub.acceptance_criteria), created_at=now,
        ))
        db.add(GraphNodeRow(id=task_id, kind="task", title=sub.title, owner="sdf", source="plans",
                            confidence=1.0, created_at=now))
        db.add(PlanTaskRow(task_id=task_id, plan_id=plan_id, subtask_key=sub.key))
    db.flush()
    for sub in spec.subtasks:
        _edge(db, ("task", task_id_for(plan_id, sub.key)), ("plan", plan_id), "depends_on", "plans")
    if spec.objective_id:
        _edge(db, ("plan", plan_id), ("objective", spec.objective_id), "implements", "plans")
    db.flush()
    return plan


def _decision_node(db: Session, request: DecisionRequestRow, title: str, metadata: dict[str, Any]) -> str:
    # The node id is bound on the request before the single flush that
    # resolves it: a resolved request is final (ORM guard + PG trigger).
    node_id = f"DECISION-{uuid4().hex[:12]}"
    request.decision_node_id = node_id
    db.add(GraphNodeRow(
        id=node_id, kind="decision", title=title[:500], source=f"decision_request:{request.id}",
        owner=request.resolved_by or "sdf", confidence=1.0, created_at=utcnow(), metadata_json=metadata,
    ))
    db.flush()
    return node_id


def decide_plan(
    db: Session, plan_id: str, *, approve: bool, decided_by: str, rationale: str = "",
    expected_sha256: str | None = None, allow_self_approval: bool = False,
    principal: dict[str, Any] | None = None,
) -> PlanRow:
    """Resolve a plan-approval request.  Only a named human can do this, and
    not the one who proposed the plan unless single-user mode allows it
    (``allow_self_approval``; rejecting your own plan is always allowed).
    ``principal`` is the authenticated identity recorded on the Decision."""

    actor = validate_human(decided_by)
    plan = db.get(PlanRow, plan_id)
    if plan is None:
        raise LookupError("plan not found")
    if plan.status != "proposed":
        raise PlanGateError(f"plan {plan_id} is already {plan.status}")
    self_decision = actor.casefold() == (plan.proposed_by or "").casefold()
    if approve and self_decision and not allow_self_approval:
        raise SeparationOfDutiesError(
            f"separation of duties: {actor} proposed plan {plan_id} and cannot approve it "
            "(another approver must, or enable SDF_AUTH_SINGLE_USER for a solo deployment)")
    if expected_sha256 is not None and expected_sha256 != plan.content_sha256:
        raise PlanGateError("plan content changed since it was reviewed (content_sha256 mismatch)")
    if content_hash(plan.content) != plan.content_sha256:
        raise PlanGateError("plan content does not match its recorded hash")
    request = db.get(DecisionRequestRow, plan.decision_request_id)
    if request is None or request.status != "pending":
        raise PlanGateError("plan approval request is not pending")
    now = utcnow()
    request.status = "approved" if approve else "rejected"
    request.resolver_kind = "human"
    request.resolved_by = actor
    request.resolution = rationale[:4000] or None
    request.resolved_at = now
    node_id = _decision_node(
        db, request, f"{'Approve' if approve else 'Reject'} plan {plan_id}",
        {"decision_request_id": request.id, "plan_id": plan_id, "content_sha256": plan.content_sha256,
         "outcome": request.status, "rationale": rationale[:2000], "proposed_by": plan.proposed_by,
         "decided_by": actor, "principal": principal or {"name": actor, "method": "in_process"},
         "self_approval": bool(approve and self_decision)},
    )
    _edge(db, ("decision", node_id), ("plan", plan_id), "constrains" if approve else "contradicts", "plans")
    plan.status = "approved" if approve else "rejected"
    plan.decided_at = now
    db.flush()
    return plan


def plan_payload(plan: PlanRow, db: Session) -> dict[str, Any]:
    request = db.get(DecisionRequestRow, plan.decision_request_id)
    tasks = db.scalars(select(PlanTaskRow).where(PlanTaskRow.plan_id == plan.id)).all()
    return {
        "id": plan.id, "title": plan.title, "status": plan.status, "objective_id": plan.objective_id,
        "content_sha256": plan.content_sha256, "proposed_by": plan.proposed_by,
        "decision_request": decision_request_payload(request) if request else None,
        "tasks": [{"task_id": t.task_id, "subtask_key": t.subtask_key,
                   "status": (db.get(TaskRow, t.task_id).status if db.get(TaskRow, t.task_id) else None)} for t in tasks],
        "subtasks": plan.content.get("subtasks", []),
    }


# --- agent decision requests ------------------------------------------------

def open_agent_request(
    db: Session, *, run_id: str, requested_by: str, question: str, options: list[str], message_id: str,
) -> DecisionRequestRow:
    request = DecisionRequestRow(
        id=f"DREQ-{uuid4().hex[:12]}", subject_kind="agent_request", subject_id=message_id, run_id=run_id,
        question=question, options=list(options), status="pending", requested_by=requested_by,
        message_id=message_id, created_at=utcnow(),
    )
    db.add(request)
    db.flush()
    return request


Outcome = Literal["approved", "rejected", "answered"]


@dataclass(frozen=True)
class PolicyDecision:
    outcome: Outcome
    resolution: str
    policy_name: str


class DecisionPolicy(Protocol):
    def decide(self, request: DecisionRequestRow) -> PolicyDecision | None: ...


class NoAutoResolvePolicy:
    """Default: every agent decision request waits for a human."""

    def decide(self, request: DecisionRequestRow) -> PolicyDecision | None:  # noqa: ARG002
        return None


class RuleDecisionPolicy:
    """Operator-configured rules that may *reject* or *answer* a request whose
    question matches a regex.  A policy can never approve."""

    def __init__(self, rules: list[tuple[str, Outcome, str]], name: str = "rules") -> None:
        for _, outcome, _ in rules:
            if outcome == "approved":
                raise ValueError("a decision policy cannot approve; only a human can")
        self._rules = [(re.compile(p), o, r) for p, o, r in rules]
        self.name = name

    def decide(self, request: DecisionRequestRow) -> PolicyDecision | None:
        for pattern, outcome, resolution in self._rules:
            if pattern.search(request.question):
                return PolicyDecision(outcome, resolution, self.name)
        return None


def resolve_request(
    db: Session, request_id: str, *, outcome: Outcome, resolved_by: str, resolver_kind: Literal["human", "policy"],
    resolution: str = "", allow_self_approval: bool = False, principal: dict[str, Any] | None = None,
) -> DecisionRequestRow:
    request = db.get(DecisionRequestRow, request_id)
    if request is None:
        raise LookupError("decision request not found")
    if request.subject_kind == "plan":
        if resolver_kind != "human" or outcome == "answered":
            raise PlanGateError("only a human approves or rejects a plan")
        decide_plan(db, request.subject_id, approve=outcome == "approved", decided_by=resolved_by,
                    rationale=resolution, allow_self_approval=allow_self_approval, principal=principal)
        return request
    if request.status != "pending":
        raise PlanGateError("decision request is already resolved")
    if outcome not in ("approved", "rejected", "answered"):
        raise ValueError("outcome must be approved, rejected or answered")
    if resolver_kind == "human":
        actor = validate_human(resolved_by)
    elif resolver_kind == "policy":
        if outcome == "approved":
            raise PlanGateError("a policy cannot approve a decision request; only a human can")
        actor = f"policy:{resolved_by}"
    else:
        raise ValueError("resolver_kind must be human or policy")
    if outcome == "answered" and not resolution.strip():
        raise ValueError("an answer needs a resolution text")
    request.status = outcome
    request.resolver_kind = resolver_kind
    request.resolved_by = actor
    request.resolution = resolution[:4000] or None
    request.resolved_at = utcnow()
    _decision_node(
        db, request, f"{outcome}: {request.question[:400]}",
        {"decision_request_id": request.id, "run_id": request.run_id, "requested_by": request.requested_by,
         "outcome": outcome, "resolver_kind": resolver_kind, "resolved_by": actor,
         "principal": principal or {"name": actor, "method": "in_process" if resolver_kind == "human" else "policy"}},
    )
    db.flush()
    return request


def decision_request_payload(request: DecisionRequestRow) -> dict[str, Any]:
    return {
        "id": request.id, "subject_kind": request.subject_kind, "subject_id": request.subject_id,
        "run_id": request.run_id, "question": request.question, "options": request.options,
        "status": request.status, "requested_by": request.requested_by, "resolved_by": request.resolved_by,
        "resolver_kind": request.resolver_kind, "resolution": request.resolution,
        "decision_node_id": request.decision_node_id,
        "created_at": request.created_at.isoformat() if request.created_at else None,
        "resolved_at": request.resolved_at.isoformat() if request.resolved_at else None,
    }
