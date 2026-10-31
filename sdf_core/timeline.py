"""One ordered timeline for an Attempt or an agent run (ADR 0023).

``build_timeline(db, run_or_attempt_id)`` merges what SDF already records -
runtime events, agent messages, the coordinator's turn log, tool-proxy and
auth audits, decision requests, Evidence, and the usage ledger - into one
list sorted by time.  It is a read model only: nothing is written.

Bodies and payloads are redacted (:func:`sdf_core.observability.redact_value`)
and message bodies are truncated; the full bodies stay behind the existing
``/runs/{id}/messages`` endpoint.
"""

from __future__ import annotations

from datetime import datetime, timezone
from typing import Any

from sqlalchemy import or_, select
from sqlalchemy.orm import Session

from .db import (
    AgentMessageRow,
    AgentRunMemberRow,
    AgentRunRow,
    AttemptRow,
    AuthAuditRow,
    DecisionRequestRow,
    EvidenceRow,
    ToolAuditRow,
    UsageLedgerRow,
)
from .events import category_of, events_for_attempts
from .observability import redact, redact_value
from .usage import ledger_payload

_BODY_CHARS = 400
_EPOCH = datetime(1970, 1, 1, tzinfo=timezone.utc)


def _ts(value: datetime | None) -> datetime:
    if value is None:
        return _EPOCH
    return value if value.tzinfo else value.replace(tzinfo=timezone.utc)


def _entry(at: datetime | None, source: str, kind: str, **fields: Any) -> dict[str, Any]:
    return {"at": _ts(at).isoformat(), "source": source, "kind": kind, **redact_value(fields)}


def _usage_entry(row: UsageLedgerRow) -> dict[str, Any]:
    fields = ledger_payload(row)
    fields.pop("kind"), fields.pop("created_at")
    fields["usage_source"] = fields.pop("source")
    return _entry(row.created_at, "usage", f"usage_{row.kind}", **fields)


def _attempt_entries(db: Session, attempt_ids: list[str]) -> list[dict[str, Any]]:
    out: list[dict[str, Any]] = []
    if not attempt_ids:
        return out
    for a in db.scalars(select(AttemptRow).where(AttemptRow.id.in_(attempt_ids))):
        out.append(_entry(a.created_at, "attempt", "attempt_created", attempt_id=a.id, task_id=a.task_id,
                          agent=a.agent, status=a.status, provider=a.provider, cost_usd=a.cost_usd,
                          input_tokens=a.input_tokens, output_tokens=a.output_tokens, latency_ms=a.latency_ms,
                          error_class=a.error_class, trace_id=a.trace_id, evidence_mode=a.evidence_mode))
    # Normalized events (ADR 0029): provenance-labelled telemetry, never Evidence.
    for e in events_for_attempts(db, attempt_ids):
        out.append(_entry(e.created_at, "runtime", e.kind, attempt_id=e.attempt_id, session_id=e.session_id,
                          sequence=e.sequence, status=e.status, event_source=e.source,
                          category=category_of(e.kind).value, acceptance_evidence=False, payload=e.payload))
    for t in db.scalars(select(ToolAuditRow).where(ToolAuditRow.attempt_id.in_(attempt_ids))):
        out.append(_entry(t.created_at, "tool_audit", t.event, attempt_id=t.attempt_id, tool=t.tool,
                          action=t.action, decision=t.decision, outcome=t.outcome, detail=t.detail,
                          evidence_mode=t.evidence_mode))
    for ev in db.scalars(select(EvidenceRow).where(EvidenceRow.attempt_id.in_(attempt_ids))):
        out.append(_entry(ev.created_at, "evidence", ev.kind, attempt_id=ev.attempt_id, status=ev.status,
                          criterion=ev.criterion, exit_code=ev.exit_code, evidence_mode=ev.evidence_mode,
                          evidence_class=ev.evidence_class, verifier_identity=ev.verifier_identity,
                          subject_artifact_sha256=ev.subject_artifact_sha256))
    for u in db.scalars(select(UsageLedgerRow).where(UsageLedgerRow.attempt_id.in_(attempt_ids))):
        out.append(_usage_entry(u))
    for audit in db.scalars(select(AuthAuditRow).where(or_(*[AuthAuditRow.path.contains(i) for i in attempt_ids]))):
        out.append(_entry(audit.at, "auth_audit", audit.event, principal=audit.principal, route=audit.route,
                          path=audit.path, status_code=audit.status_code))
    return out


def build_timeline(db: Session, run_or_attempt_id: str) -> dict[str, Any] | None:
    """Return ``{"id", "kind", "entries"}`` or ``None`` when the id is unknown."""

    run = db.get(AgentRunRow, run_or_attempt_id)
    if run is None:
        attempt = db.get(AttemptRow, run_or_attempt_id)
        if attempt is None:
            return None
        entries = _attempt_entries(db, [attempt.id])
        entries += [
            _entry(audit.at, "auth_audit", audit.event, principal=audit.principal, route=audit.route,
                   path=audit.path, status_code=audit.status_code)
            for audit in db.scalars(select(AuthAuditRow).where(AuthAuditRow.path.contains(attempt.task_id)))
            if audit.route == "/tasks/{task_id}/run"
        ]
        return {"id": attempt.id, "kind": "attempt", "task_id": attempt.task_id, "status": attempt.status,
                "error_class": attempt.error_class, "trace_id": attempt.trace_id, "entries": _sorted(entries)}

    entries: list[dict[str, Any]] = [
        _entry(run.started_at, "run", "run_started", run_id=run.id, plan_id=run.plan_id, started_by=run.started_by),
    ]
    if run.finished_at:
        entries.append(_entry(run.finished_at, "run", "run_finished", run_id=run.id, status=run.status,
                              cost_usd=run.cost_usd, error=redact(run.error or "") or None))
    members = list(db.scalars(select(AgentRunMemberRow).where(AgentRunMemberRow.run_id == run.id)))
    attempt_ids: set[str] = set()
    for m in db.scalars(select(AgentMessageRow).where(AgentMessageRow.run_id == run.id)):
        if m.attempt_id:
            attempt_ids.add(m.attempt_id)
        entries.append(_entry(m.created_at, "message", m.kind, seq=m.seq, from_agent=m.from_agent,
                              to_agent=m.to_agent, attempt_id=m.attempt_id, truncated=m.truncated,
                              body=redact(m.body[:_BODY_CHARS])))
    for d in db.scalars(select(DecisionRequestRow).where(DecisionRequestRow.run_id == run.id)):
        entries.append(_entry(d.created_at, "decision", "decision_requested", request_id=d.id,
                              subject_id=d.subject_id, requested_by=d.requested_by, question=d.question[:_BODY_CHARS]))
        if d.resolved_at:
            entries.append(_entry(d.resolved_at, "decision", f"decision_{d.status}", request_id=d.id,
                                  resolved_by=d.resolved_by))
    summary = run.summary or {}
    for agent_id, cost in (summary.get("cost") or {}).items():
        for turn in cost.get("turns") or []:
            # The coordinator's turn log carries durations, not timestamps.
            entries.append(_entry(run.finished_at or run.started_at, "turn_log", "turn", agent=agent_id, **turn))
    for subtask in (summary.get("subtasks") or {}).values():
        attempt_ids.update(a for a in subtask.get("attempts") or [] if isinstance(a, str))
    entries += _attempt_entries(db, sorted(attempt_ids))
    entries += [_usage_entry(u)
                for u in db.scalars(select(UsageLedgerRow).where(UsageLedgerRow.run_id == run.id,
                                                                 UsageLedgerRow.attempt_id.not_in(attempt_ids or {""})))]
    return {"id": run.id, "kind": "agent_run", "plan_id": run.plan_id, "status": run.status, "trace_id": run.trace_id,
            "members": [{"agent_id": m.agent_id, "kind": m.agent_kind, "role": m.role, "sandbox_id": m.sandbox_id,
                         "status": m.status, "turns": m.turns, "input_tokens": m.input_tokens,
                         "output_tokens": m.output_tokens} for m in members],
            "entries": _sorted(entries)}


def _sorted(entries: list[dict[str, Any]]) -> list[dict[str, Any]]:
    order = {"run": 0, "attempt": 1, "message": 2, "runtime": 3, "tool_audit": 4, "turn_log": 5,
             "decision": 6, "evidence": 7, "usage": 8, "auth_audit": 9}
    return sorted(entries, key=lambda e: (e["at"], order.get(e["source"], 99), e.get("sequence") or 0))


def format_timeline(timeline: dict[str, Any]) -> str:
    """Human-readable rendering for ``python -m sdf_core.cli runs show``."""

    head = f"{timeline['kind']} {timeline['id']} status={timeline.get('status')}"
    if timeline.get("error_class"):
        head += f" error_class={timeline['error_class']}"
    if timeline.get("trace_id"):  # ADR 0024: look the trace up in the OTLP backend
        head += f" trace_id={timeline['trace_id']}"
    lines = [head]
    skip = {"at", "source", "kind", "payload", "body"}
    for e in timeline["entries"]:
        extras = " ".join(f"{k}={v}" for k, v in e.items() if k not in skip and v not in (None, "", [], {}))
        lines.append(f"{e['at']}  {e['source']:<10} {e['kind']:<28} {extras}"[:400])
    return "\n".join(lines)
