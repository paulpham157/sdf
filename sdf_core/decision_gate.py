"""Material decision gate (#60, ADR 0033).

An agent that faces materially different choices it may not make itself
sends a ``decision_request`` marked ``material`` with the alternatives and
the consequence.  Until a human resolves it, SDF does not dispatch or record
consequential work for the affected scope:

* a **worker**'s request blocks its own subtask (Task): no agent turn for
  that worker, no host verification (no Evidence, no retry Attempt), and
  ``ExecutionService`` refuses to begin or finish an Attempt of that Task;
* the **lead**'s request blocks the whole run (every subtask, every turn);
* a run cannot finish ``succeeded`` while any material request is pending.

A request without ``material: true`` is ordinary uncertainty: it is recorded
and answered as before and blocks nothing.  Materiality is declared by the
agent in the envelope and stored in the append-only ``agent_messages.meta``
(never inferred from the question text), so the gate's input is immutable
and auditable.  Only a human may resolve a material request; any human
outcome (approved, answered, rejected) lifts the block, and the outcome is
delivered to the agent as before.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

from sqlalchemy import select
from sqlalchemy.orm import Session

from .db import AgentMessageRow, AgentRunMemberRow, AgentRunRow, DecisionRequestRow, PlanGateError, PlanTaskRow


class DecisionGateError(PlanGateError):
    """Work was dispatched or recorded while a material decision is pending."""


RUN_SCOPE = "run"


@dataclass(frozen=True)
class MaterialRequest:
    request_id: str
    run_id: str
    requested_by: str
    # ``run`` (the lead asked) or the plan subtask key the worker is bound to.
    scope: str
    consequence: str
    options: tuple[str, ...]


def material_meta(db: Session, request: DecisionRequestRow) -> dict[str, Any] | None:
    """The material declaration of an agent decision request, or ``None`` when it is not material."""

    if request.subject_kind != "agent_request" or not request.message_id:
        return None
    message = db.get(AgentMessageRow, request.message_id)
    meta = (message.meta or {}) if message is not None else {}
    return meta if meta.get("material") is True else None


def is_material(db: Session, request: DecisionRequestRow) -> bool:
    return material_meta(db, request) is not None


def _scope(db: Session, request: DecisionRequestRow) -> str:
    member = db.scalar(select(AgentRunMemberRow).where(
        AgentRunMemberRow.run_id == request.run_id, AgentRunMemberRow.agent_id == request.requested_by))
    if member is None or member.role != "worker" or not member.subtask_key:
        return RUN_SCOPE  # the lead, or an unknown sender: block the whole run
    return member.subtask_key


def pending_material(db: Session, *, run_id: str | None = None, run_ids: list[str] | None = None) -> list[MaterialRequest]:
    """Pending material requests of one run (or several), oldest first."""

    query = select(DecisionRequestRow).where(
        DecisionRequestRow.status == "pending", DecisionRequestRow.subject_kind == "agent_request")
    if run_id is not None:
        query = query.where(DecisionRequestRow.run_id == run_id)
    elif run_ids is not None:
        if not run_ids:
            return []
        query = query.where(DecisionRequestRow.run_id.in_(run_ids))
    # Resolved by another session (the API): never trust the identity map.
    query = query.order_by(DecisionRequestRow.created_at).execution_options(populate_existing=True)
    found: list[MaterialRequest] = []
    for request in db.scalars(query).all():
        meta = material_meta(db, request)
        if meta is None:
            continue
        found.append(MaterialRequest(
            request.id, request.run_id or "", request.requested_by, _scope(db, request),
            str(meta.get("consequence") or ""), tuple(meta.get("options") or ()),
        ))
    return found


def check_decision_gate(db: Session, task_id: str) -> None:
    """Refuse to dispatch or record work on a Task blocked by a pending material decision.

    A Task outside any Plan is never blocked (agent decision requests exist
    only in plan runs).
    """

    link = db.get(PlanTaskRow, task_id)
    if link is None:
        return
    run_ids = list(db.scalars(select(AgentRunRow.id).where(AgentRunRow.plan_id == link.plan_id)))
    for request in pending_material(db, run_ids=run_ids):
        if request.scope in (RUN_SCOPE, link.subtask_key):
            raise DecisionGateError(
                f"material decision {request.request_id} from {request.requested_by} is pending; "
                f"task {task_id} is blocked until a human resolves it"
            )


def material_payload(db: Session, request: DecisionRequestRow) -> dict[str, Any]:
    """Fields added to a decision request's API payload (ADR 0033)."""

    meta = material_meta(db, request)
    if meta is None:
        return {"material": False}
    return {
        "material": True,
        "consequence": meta.get("consequence"),
        "authority": "human",
        "blocks": _scope(db, request) if request.status == "pending" else None,
    }
