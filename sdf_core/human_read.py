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

from sqlalchemy import and_, collate, or_, select
from sqlalchemy.orm import Session

from .auth import Principal, single_user_mode
from .casefold import casefold
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


class InboxNormalizationError(RuntimeError):
    """The inbox cannot be listed because a plan fold is missing or wrong.

    The handler must not return a page. A partial list would look like a
    complete inbox while omitting or mis-classifying plans.
    """


def _identity_collation(db: Session) -> str:
    """Bytewise equality. A locale collation must not merge two folds."""

    name = db.get_bind().dialect.name
    if name == "postgresql":
        return "C"
    if name == "sqlite":
        return "BINARY"
    raise InboxNormalizationError(f"unsupported database {name!r} for casefold identity")


def _require_casefold_backfill(db: Session) -> None:
    """One-row probe. A missing fold is a failed inbox, not a skipped plan."""

    missing = db.scalar(select(PlanRow.id).where(PlanRow.proposed_by_casefold.is_(None)).limit(1))
    if missing is not None:
        raise InboxNormalizationError(
            f"plan {missing} is missing proposed_by_casefold; inbox listing is unavailable "
            "until migration 0032_plan_casefold has backfilled every plan"
        )


def _require_returned_fold(plan: PlanRow) -> None:
    try:
        expected = casefold(plan.proposed_by or "")
    except ValueError as exc:
        raise InboxNormalizationError(
            f"plan {plan.id} proposer cannot be casefolded; inbox listing is unavailable"
        ) from exc
    if plan.proposed_by_casefold != expected:
        raise InboxNormalizationError(
            f"plan {plan.id} proposed_by_casefold does not match the pinned casefold; "
            "inbox listing is unavailable"
        )


def inbox_page(
    db: Session, principal: Principal, limit: int, cursor: str | None,
) -> tuple[list[tuple[DecisionRequestRow, PlanRow | None]], str | None]:
    """Pending requests this caller may approve, in ``(created_at, id)`` order.

    ``next_cursor`` is the last returned row, or null when no eligible row
    remains. The database may read ineligible pending rows while it fills
    ``LIMIT``. The application receives at most ``limit + 1`` of them and one
    bound fold of the caller, not a list of proposers.

    A missing plan row is not actionable. A missing or incorrect
    ``proposed_by_casefold`` aborts the listing. Single-user mode does not
    hide the caller's plans. A caller who cannot approve gets an empty page
    after the missing-fold probe and does not run the page query.
    """

    if cursor:
        decode_cursor(cursor)  # 422 for everyone, including a caller who sees nothing
    _require_casefold_backfill(db)
    if not principal.can_approve:
        return [], None
    try:
        caller_fold = casefold(principal.name)
    except ValueError as exc:
        raise InboxNormalizationError(
            f"caller identity cannot be casefolded; inbox listing is unavailable: {exc}"
        ) from exc
    collation = _identity_collation(db)
    has_plan = PlanRow.id.is_not(None)
    if single_user_mode():
        visible_plan = has_plan
    else:
        visible_plan = and_(
            has_plan,
            collate(PlanRow.proposed_by_casefold, collation) != caller_fold,
        )
    query = (
        select(DecisionRequestRow, PlanRow)
        .outerjoin(
            PlanRow,
            and_(
                DecisionRequestRow.subject_kind == "plan",
                DecisionRequestRow.subject_id == PlanRow.id,
            ),
        )
        .where(DecisionRequestRow.status == "pending")
        .where(or_(DecisionRequestRow.subject_kind != "plan", visible_plan))
    )
    if cursor:
        created_at, row_id = decode_cursor(cursor)
        query = query.where(_after(DecisionRequestRow, created_at, row_id))
    rows = list(db.execute(
        query.order_by(DecisionRequestRow.created_at, DecisionRequestRow.id).limit(limit + 1)
    ).all())
    for _request, plan in rows:
        if plan is not None:
            _require_returned_fold(plan)
    next_cursor = None
    if len(rows) > limit:
        last = rows[limit - 1][0]
        next_cursor = encode_cursor(last.created_at, last.id)
        rows = rows[:limit]
    return [(request, plan) for request, plan in rows], next_cursor


def inbox_item(db: Session, request: DecisionRequestRow, plan: PlanRow | None) -> dict[str, Any]:
    payload = {**decision_request_payload(request), **material_payload(db, request), "actionable": True}
    if request.subject_kind == "plan" and plan is not None:
        payload["proposed_by"] = plan.proposed_by
        payload["content_sha256"] = plan.content_sha256
    return redact_payload(payload)
