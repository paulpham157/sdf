"""Human read model and approver inbox (ADR 0044).

Lists the rows SDF already stores. Nothing here inserts, updates, or deletes
a domain row. String fields are redacted with the same function the timeline
uses. A message body, if a payload ever carries one, is cut to the timeline's
400 characters and then redacted. Lease columns, dispatch keys, and held-out
oracle output are not fields of these payloads.
"""

from __future__ import annotations

import base64
import json
from datetime import datetime
from typing import Any

from sqlalchemy import and_, or_, select
from sqlalchemy.orm import Session

from .auth import Principal, single_user_mode
from .db import AttemptRow, DecisionRequestRow, GraphNodeRow, PlanRow, TaskRow
from .decision_gate import material_payload
from .observability import redact_value
from .plans import decision_request_payload
from .timeline import _BODY_CHARS

# Opaque cursor: the last row's (created_at, id), base64url JSON. The timestamp
# is the value the database returned, unchanged, so the next page's comparison
# matches that dialect's datetime (SQLite hands back naive UTC).
_CURSOR_KEYS = ("t", "id")


class CursorError(ValueError):
    """The client sent a cursor this server cannot decode."""


def encode_cursor(created_at: datetime, row_id: str) -> str:
    raw = json.dumps({"t": created_at.isoformat(), "id": row_id}, separators=(",", ":")).encode()
    return base64.urlsafe_b64encode(raw).decode().rstrip("=")


def decode_cursor(cursor: str) -> tuple[datetime, str]:
    if not isinstance(cursor, str) or cursor == "" or cursor.strip() != cursor:
        raise CursorError("cursor is not valid")
    try:
        padded = cursor + "=" * (-len(cursor) % 4)
        data = json.loads(base64.urlsafe_b64decode(padded.encode()))
    except (ValueError, json.JSONDecodeError, UnicodeError) as exc:
        raise CursorError("cursor is not valid") from exc
    if not isinstance(data, dict) or set(data) != set(_CURSOR_KEYS):
        raise CursorError("cursor is not valid")
    stamp, row_id = data["t"], data["id"]
    if not isinstance(stamp, str) or not isinstance(row_id, str) or row_id == "":
        raise CursorError("cursor is not valid")
    try:
        created_at = datetime.fromisoformat(stamp)
    except ValueError as exc:
        raise CursorError("cursor is not valid") from exc
    return created_at, row_id


def redact_payload(value: Any) -> Any:
    """Redact every string. A ``body`` string is truncated first, like the timeline."""

    if isinstance(value, str):
        return redact_value(value)
    if isinstance(value, dict):
        redacted: dict[str, Any] = {}
        for key, item in value.items():
            name = str(key)
            if name == "body" and isinstance(item, str):
                redacted[name] = redact_value(item[:_BODY_CHARS])
            else:
                redacted[name] = redact_payload(item)
        return redacted
    if isinstance(value, list):
        return [redact_payload(item) for item in value]
    return value


def _iso(value: datetime | None) -> str | None:
    return value.isoformat() if value else None


def _after(model: Any, created_at: datetime, row_id: str):
    return or_(model.created_at > created_at, and_(model.created_at == created_at, model.id > row_id))


def keyset_page(db: Session, model: Any, limit: int, cursor: str | None, *criteria: Any) -> tuple[list[Any], str | None]:
    """Rows in ``(created_at, id)`` order, strictly after ``cursor``, at most ``limit``."""

    query = select(model)
    for criterion in criteria:
        query = query.where(criterion)
    if cursor:
        created_at, row_id = decode_cursor(cursor)
        query = query.where(_after(model, created_at, row_id))
    rows = list(db.scalars(query.order_by(model.created_at, model.id).limit(limit + 1)))
    next_cursor = None
    if len(rows) > limit:
        last = rows[limit - 1]
        next_cursor = encode_cursor(last.created_at, last.id)
        rows = rows[:limit]
    return rows, next_cursor


def task_fields(row: TaskRow) -> dict[str, Any]:
    return {
        "id": row.id,
        "title": row.title,
        "status": row.status,
        "acceptance_criteria": list(row.acceptance_criteria or []),
        "created_at": _iso(row.created_at),
    }


def task_item(row: TaskRow) -> dict[str, Any]:
    return redact_payload(task_fields(row))


def task_detail(db: Session, row: TaskRow) -> dict[str, Any]:
    attempt_ids = list(db.scalars(
        select(AttemptRow.id).where(AttemptRow.task_id == row.id).order_by(AttemptRow.created_at, AttemptRow.id)
    ))
    return redact_payload({**task_fields(row), "attempt_ids": attempt_ids})


def attempt_item(row: AttemptRow) -> dict[str, Any]:
    # dispatch_key, executor_id, and executor_heartbeat_at are operational and stay off.
    return redact_payload({
        "id": row.id,
        "task_id": row.task_id,
        "status": row.status,
        "agent": row.agent,
        "parent_attempt_id": row.parent_attempt_id,
        "model_tier": row.model_tier,
        "cost_usd": row.cost_usd,
        "provider": row.provider,
        "latency_ms": row.latency_ms,
        "input_tokens": row.input_tokens,
        "output_tokens": row.output_tokens,
        "escalation_reason": row.escalation_reason,
        "created_at": _iso(row.created_at),
        "model_profile": row.model_profile,
        "outcome": row.outcome,
        "evidence_mode": row.evidence_mode,
        "error_class": row.error_class,
        "trace_id": row.trace_id,
        "principal": row.principal,
    })


def plan_item(row: PlanRow) -> dict[str, Any]:
    return redact_payload({
        "id": row.id,
        "title": row.title,
        "status": row.status,
        "objective_id": row.objective_id,
        "proposed_by": row.proposed_by,
        "content_sha256": row.content_sha256,
        "created_at": _iso(row.created_at),
        "decision_request_id": row.decision_request_id,
    })


def objective_item(row: GraphNodeRow) -> dict[str, Any]:
    # ``owner`` is the domain owner on the graph node, not the caller (ADR 0015).
    return redact_payload({
        "id": row.id,
        "title": row.title,
        "owner": row.owner,
        "source": row.source,
        "created_at": _iso(row.created_at),
    })


def _plans_for(db: Session, rows: list[DecisionRequestRow]) -> dict[str, PlanRow]:
    ids = [row.subject_id for row in rows if row.subject_kind == "plan"]
    if not ids:
        return {}
    return {plan.id: plan for plan in db.scalars(select(PlanRow).where(PlanRow.id.in_(ids)))}


def _proposer_may_not_see(plan: PlanRow | None, principal: Principal) -> bool:
    """Same casefold comparison ``decide_plan`` uses to refuse self-approval."""

    if plan is None or single_user_mode():
        return False
    return principal.name.casefold() == (plan.proposed_by or "").casefold()


def inbox_page(
    db: Session, principal: Principal, limit: int, cursor: str | None,
) -> tuple[list[tuple[DecisionRequestRow, PlanRow | None]], str | None]:
    """Pending requests this caller may approve or resolve.

    A caller who cannot approve gets an empty page. A plan the caller proposed
    is omitted unless single-user mode is on. The cursor is the last included
    row, so a filtered plan does not consume a slot or stall the next page.
    """

    if cursor:
        decode_cursor(cursor)  # 422 for everyone, including a caller who sees nothing
    if not principal.can_approve:
        return [], None
    found: list[tuple[DecisionRequestRow, PlanRow | None]] = []
    scanned: tuple[datetime, str] | None = decode_cursor(cursor) if cursor else None
    seen: set[str] = set()
    while len(found) <= limit:
        query = select(DecisionRequestRow).where(DecisionRequestRow.status == "pending")
        if scanned is not None:
            query = query.where(_after(DecisionRequestRow, scanned[0], scanned[1]))
        batch = list(db.scalars(query.order_by(DecisionRequestRow.created_at, DecisionRequestRow.id).limit(100)))
        if not batch:
            break
        plans = _plans_for(db, batch)
        advanced = False
        for row in batch:
            if row.id in seen:
                batch = []
                break
            seen.add(row.id)
            advanced = True
            scanned = (row.created_at, row.id)
            plan = plans.get(row.subject_id) if row.subject_kind == "plan" else None
            if row.subject_kind == "plan" and _proposer_may_not_see(plan, principal):
                continue
            found.append((row, plan))
            if len(found) > limit:
                break
        if not advanced or len(batch) < 100:
            break
    next_cursor = None
    if len(found) > limit:
        last = found[limit - 1][0]
        next_cursor = encode_cursor(last.created_at, last.id)
        found = found[:limit]
    return found, next_cursor


def inbox_item(db: Session, request: DecisionRequestRow, plan: PlanRow | None) -> dict[str, Any]:
    payload = {**decision_request_payload(request), **material_payload(db, request), "actionable": True}
    if request.subject_kind == "plan":
        payload["proposed_by"] = plan.proposed_by if plan is not None else None
        payload["content_sha256"] = plan.content_sha256 if plan is not None else None
    return redact_payload(payload)
