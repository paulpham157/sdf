from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import Any, Protocol

from sqlalchemy.orm import Session
from sqlalchemy import func, select
from sqlalchemy.dialects.postgresql import insert as postgres_insert
from sqlalchemy.dialects.sqlite import insert as sqlite_insert

from .db import AttemptRow, RuntimeEventRow, ToolAuditRow
from .model import AttemptState
from .runtime import RuntimeEvent, RuntimeEventKind, RuntimeStatus, SqlAlchemyRuntimeEventSink
from .policy import ActionRequest, AuditEvent, AuditRecord, Policy, PolicyDecision, PolicyEffect
from uuid import uuid4
from .containment import ContainmentUnavailable
from .sandbox import FixtureSandbox


class ToolExecutor(Protocol):
    def execute(self, request: ActionRequest) -> Any:
        """Perform one already-authorized structured action."""


class AuditSink(Protocol):
    def append(self, record: AuditRecord) -> None:
        """Persist or forward one trusted SDF audit record."""


class ReplayAwareAuditSink(AuditSink, Protocol):
    def has_event(self, *, attempt_id: str, action_id: str, event: AuditEvent) -> bool:
        """Return whether a durable outcome already exists for an action."""


class ToolEventSink(Protocol):
    def append(self, record: AuditRecord) -> None:
        """Emit one trusted Tool Proxy event."""


class AttemptGuard(Protocol):
    def is_active(self, attempt_id: str) -> bool:
        """Return whether an Attempt exists and may perform tool actions."""


class SqlAlchemyAttemptGuard:
    """Bind Tool Proxy execution to an existing, non-terminal Attempt."""

    _ACTIVE = frozenset({AttemptState.CREATED.value, AttemptState.DISPATCHED.value, AttemptState.RUNNING.value})

    def __init__(self, db: Session):
        self.db = db

    def is_active(self, attempt_id: str) -> bool:
        attempt = self.db.get(AttemptRow, attempt_id)
        return attempt is not None and attempt.status in self._ACTIVE


class ToolExecutionStatus(StrEnum):
    EXECUTED = "executed"
    DENIED = "denied"
    FAILED = "failed"


@dataclass(frozen=True, slots=True)
class ToolExecution:
    action: ActionRequest
    decision: PolicyDecision
    status: ToolExecutionStatus
    value: Any = None
    error: str | None = None
    audit_records: tuple[AuditRecord, ...] = ()


class InMemoryAuditSink:
    """Deterministic sink for tests and local development.

    A production adapter can implement ``AuditSink`` and persist the same
    immutable records alongside the Attempt.  This slice deliberately does
    not claim database-level append-only enforcement.
    """

    def __init__(self) -> None:
        self._records: list[AuditRecord] = []

    def append(self, record: AuditRecord) -> None:
        self._records.append(record)

    @property
    def records(self) -> tuple[AuditRecord, ...]:
        return tuple(self._records)

    def for_attempt(self, attempt_id: str) -> tuple[AuditRecord, ...]:
        return tuple(record for record in self._records if record.attempt_id == attempt_id)


class SqlAlchemyAuditSink:
    """Persist immutable Tool Proxy audit observations alongside SDF state."""

    def __init__(self, db: Session):
        self.db = db

    def has_event(self, *, attempt_id: str, action_id: str, event: AuditEvent) -> bool:
        return self.db.scalar(select(ToolAuditRow.audit_id).where(
            (ToolAuditRow.attempt_id == attempt_id)
            & (ToolAuditRow.action_id == action_id)
            & (ToolAuditRow.event == event.value)
        )) is not None

    def claim_action(self, request: ActionRequest, decision: PolicyDecision) -> bool:
        """Atomically claim an action before invoking an external executor.

        The unique audit identity turns concurrent PostgreSQL/SQLite delivery
        into one winner. The claim is intentionally not emitted as a runtime
        event: it is an internal idempotency fence, not an observed outcome.
        """

        claim_id = f"AUDIT-CLAIM-{uuid4().hex[:16]}"
        values = {
            "audit_id": claim_id,
            "attempt_id": request.attempt_id,
            "action_id": request.action_id,
            "actor": request.actor,
            "tool": request.tool,
            "action": request.action,
            "resource": request.resource,
            "context": dict(request.context),
            "event": "action_claimed",
            "decision": decision.effect.value,
            "executed": False,
            "outcome": "claimed",
            "detail": "executor claim acquired",
            "created_at": decision.created_at,
        }
        bind = self.db.get_bind()
        dialect = bind.dialect.name
        if dialect == "sqlite":
            self.db.execute(sqlite_insert(ToolAuditRow).values(**values).on_conflict_do_nothing(
                index_elements=["attempt_id", "action_id", "event"]
            ))
        elif dialect == "postgresql":
            self.db.execute(postgres_insert(ToolAuditRow).values(**values).on_conflict_do_nothing(
                index_elements=["attempt_id", "action_id", "event"]
            ))
        else:
            existing = self.db.scalar(select(ToolAuditRow.audit_id).where(
                (ToolAuditRow.attempt_id == request.attempt_id)
                & (ToolAuditRow.action_id == request.action_id)
                & (ToolAuditRow.event == "action_claimed")
            ))
            if existing is None:
                self.db.add(ToolAuditRow(**values))
                self.db.flush()
                return True
        owner = self.db.scalar(select(ToolAuditRow.audit_id).where(
            (ToolAuditRow.attempt_id == request.attempt_id)
            & (ToolAuditRow.action_id == request.action_id)
            & (ToolAuditRow.event == "action_claimed")
        ))
        return owner == claim_id

    def append(self, record: AuditRecord) -> None:
        values = {
            "audit_id": record.audit_id,
            "attempt_id": record.attempt_id,
            "action_id": record.action_id,
            "actor": record.actor,
            "tool": record.tool,
            "action": record.action,
            "resource": record.resource,
            "context": dict(record.context),
            "event": record.event.value,
            "decision": record.decision.value,
            "executed": record.executed,
            "outcome": record.outcome,
            "detail": record.detail,
            "created_at": record.created_at,
        }
        bind = self.db.get_bind()
        dialect = bind.dialect.name
        if dialect == "sqlite":
            self.db.execute(sqlite_insert(ToolAuditRow).values(**values).on_conflict_do_nothing(
                index_elements=["attempt_id", "action_id", "event"]
            ))
        elif dialect == "postgresql":
            self.db.execute(postgres_insert(ToolAuditRow).values(**values).on_conflict_do_nothing(
                index_elements=["attempt_id", "action_id", "event"]
            ))
        else:
            existing = self.db.scalar(select(ToolAuditRow).where(
                (ToolAuditRow.attempt_id == record.attempt_id)
                & (ToolAuditRow.action_id == record.action_id)
                & (ToolAuditRow.event == record.event.value)
            ))
            if existing is None:
                self.db.add(ToolAuditRow(**values))


class SqlAlchemyToolEventSink:
    """Translate trusted Tool Proxy audit records to normalized runtime events."""

    _KINDS = {
        AuditEvent.POLICY_DECIDED: RuntimeEventKind.TOOL_POLICY_DECIDED,
        AuditEvent.ACTION_EXECUTED: RuntimeEventKind.TOOL_ACTION_EXECUTED,
        AuditEvent.ACTION_FAILED: RuntimeEventKind.TOOL_ACTION_FAILED,
    }

    def __init__(self, db: Session, *, source: str = "tool-proxy"):
        self.db = db
        self.source = source
        self.events = SqlAlchemyRuntimeEventSink(db)

    def append(self, record: AuditRecord) -> None:
        session_id = f"tool:{record.attempt_id}"
        kind = self._KINDS[record.event]
        existing = self.db.scalars(
            select(RuntimeEventRow).where(
                (RuntimeEventRow.source == self.source)
                & (RuntimeEventRow.attempt_id == record.attempt_id)
                & (RuntimeEventRow.session_id == session_id)
            )
        ).all()
        if any(
            row.kind == kind.value
            and isinstance(row.payload, dict)
            and row.payload.get("action_id") == record.action_id
            for row in existing
        ):
            return
        current = self.db.scalar(
            select(func.max(RuntimeEventRow.sequence)).where(
                (RuntimeEventRow.source == self.source)
                & (RuntimeEventRow.attempt_id == record.attempt_id)
                & (RuntimeEventRow.session_id == session_id)
            )
        ) or 0
        event = RuntimeEvent(
            kind=kind,
            session_id=session_id,
            attempt_id=record.attempt_id,
            sequence=int(current) + 1,
            status=RuntimeStatus.RUNNING,
            payload={
                "audit_id": record.audit_id,
                "action_id": record.action_id,
                "actor": record.actor,
                "tool": record.tool,
                "action": record.action,
                "resource": record.resource,
                "context": dict(record.context),
                "decision": record.decision.value,
                "executed": record.executed,
                "outcome": record.outcome,
                "detail": record.detail,
            },
            source=self.source,
        )
        self.events.append(event)


class ToolProxy:
    """Authorize a structured action before handing it to an executor."""

    def __init__(
        self,
        *,
        policy: Policy,
        executor: ToolExecutor,
        audit: AuditSink | None = None,
        attempt_guard: AttemptGuard | None = None,
        event_sink: ToolEventSink | None = None,
    ):
        self.policy = policy
        self.executor = executor
        self.audit = audit or InMemoryAuditSink()
        self.attempt_guard = attempt_guard
        self.event_sink = event_sink

    def _record(self, record: AuditRecord) -> None:
        self.audit.append(record)
        if self.event_sink is not None:
            self.event_sink.append(record)

    def execute(self, request: ActionRequest) -> ToolExecution:
        if not isinstance(request, ActionRequest):
            raise TypeError("ToolProxy requires a structured ActionRequest; terminal text is not trusted")

        if self.attempt_guard is not None and not self.attempt_guard.is_active(request.attempt_id):
            decision = PolicyDecision.deny(
                request,
                reason="Attempt does not exist or is not active",
                policy="attempt-guard",
            )
        else:
            decision = self.policy.decide(request)
        if not isinstance(decision, PolicyDecision):
            raise TypeError("policy must return a PolicyDecision")
        if decision.attempt_id != request.attempt_id or decision.action_id != request.action_id:
            raise ValueError("policy decision is not attempt-bound to the requested action")

        decision_audit = AuditRecord.for_request(
            request,
            event=AuditEvent.POLICY_DECIDED,
            decision=decision,
            executed=False,
            outcome=decision.effect.value,
            detail=decision.reason,
        )
        self._record(decision_audit)

        if decision.effect is PolicyEffect.DENY:
            return ToolExecution(
                action=request,
                decision=decision,
                status=ToolExecutionStatus.DENIED,
                audit_records=(decision_audit,),
            )

        # A durable execution observation is the idempotency fence.  A
        # redelivered structured request must not invoke the executor again,
        # even if the caller constructed a fresh policy/audit object.
        has_event = getattr(self.audit, "has_event", None)
        if callable(has_event):
            if has_event(
                attempt_id=request.attempt_id,
                action_id=request.action_id,
                event=AuditEvent.ACTION_EXECUTED,
            ):
                return ToolExecution(
                    action=request,
                    decision=decision,
                    status=ToolExecutionStatus.EXECUTED,
                )
            if has_event(
                attempt_id=request.attempt_id,
                action_id=request.action_id,
                event=AuditEvent.ACTION_FAILED,
            ):
                return ToolExecution(
                    action=request,
                    decision=decision,
                    status=ToolExecutionStatus.FAILED,
                    error="durable action outcome is already failed; issue a new action identity to retry",
                )
        claim_action = getattr(self.audit, "claim_action", None)
        if callable(claim_action) and not claim_action(request, decision):
            return ToolExecution(
                action=request,
                decision=decision,
                status=ToolExecutionStatus.FAILED,
                error="action is already claimed by another delivery; issue a new action identity to retry",
            )

        try:
            value = self.executor.execute(request)
        except Exception as exc:  # record the boundary failure before returning it
            failed_audit = AuditRecord.for_request(
                request,
                event=AuditEvent.ACTION_FAILED,
                decision=decision,
                executed=True,
                outcome=ToolExecutionStatus.FAILED.value,
                detail=f"{type(exc).__name__}: {exc}",
            )
            self._record(failed_audit)
            return ToolExecution(
                action=request,
                decision=decision,
                status=ToolExecutionStatus.FAILED,
                error=str(exc),
                audit_records=(decision_audit, failed_audit),
            )

        executed_audit = AuditRecord.for_request(
            request,
            event=AuditEvent.ACTION_EXECUTED,
            decision=decision,
            executed=True,
            outcome=ToolExecutionStatus.EXECUTED.value,
            detail="executor completed",
        )
        self._record(executed_audit)
        return ToolExecution(
            action=request,
            decision=decision,
            status=ToolExecutionStatus.EXECUTED,
            value=value,
            audit_records=(decision_audit, executed_audit),
        )


class SandboxToolExecutor:
    """Execute structured tool actions through one disposable FixtureSandbox.

    The executor deliberately accepts only the small action vocabulary that
    the Tool Proxy can authorize.  Callers cannot smuggle terminal text into
    this seam: filesystem paths are resolved by the sandbox, processes start
    in the sandbox, and network access is delegated to the sandbox's explicit
    capability gate.
    """

    def __init__(self, sandbox: FixtureSandbox, *, network_actions_allowed: bool = True):
        self.sandbox = sandbox
        self.network_actions_allowed = network_actions_allowed

    def execute(self, request: ActionRequest) -> Any:
        if request.tool == "filesystem":
            if request.action == "read":
                return self.sandbox.read_text(request.resource)
            if request.action == "write":
                data = request.context.get("data")
                if not isinstance(data, str):
                    raise ValueError("filesystem.write requires string context[data]")
                return str(self.sandbox.write_text(request.resource, data))
            raise ValueError(f"unsupported filesystem action: {request.action}")

        if request.tool == "process" and request.action == "run":
            command = request.context.get("command")
            if not isinstance(command, (list, tuple)) or not command:
                raise ValueError("process.run requires non-empty context[command]")
            return self.sandbox.run(
                command,
                cwd=request.resource,
                env=request.context.get("env"),
                timeout_seconds=request.context.get("timeout_seconds"),
                max_output_bytes=request.context.get("max_output_bytes"),
            )

        if request.tool == "network" and request.action == "request":
            if not self.network_actions_allowed:
                raise ContainmentUnavailable("network.request is disabled by the selected containment backend")
            method = request.context.get("method", "GET")
            headers = request.context.get("headers")
            body = request.context.get("body", b"")
            if not isinstance(method, str) or not isinstance(body, (bytes, bytearray)):
                raise ValueError("network.request requires string method and bytes body")
            return self.sandbox.request_network(
                request.resource,
                method=method,
                headers=headers,
                body=bytes(body),
            )

        raise ValueError(f"unsupported tool action: {request.tool}.{request.action}")
