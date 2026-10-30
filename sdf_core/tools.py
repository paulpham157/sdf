from __future__ import annotations

import hashlib
from dataclasses import dataclass
from enum import StrEnum
from typing import Any, Protocol

from sqlalchemy.orm import Session
from sqlalchemy import insert, select
from sqlalchemy.dialects.postgresql import insert as postgres_insert
from sqlalchemy.dialects.sqlite import insert as sqlite_insert

from .db import AttemptRow, RuntimeEventRow, ToolAuditRow
from .model import AttemptState
from .events import TOOL_PROXY_SOURCE, TRUSTED_ACTION_SOURCES, UntrustedEventError
from .runtime import RuntimeEvent, RuntimeEventKind, RuntimeStatus, SqlAlchemyRuntimeEventSink
from .policy import ActionRequest, AuditEvent, AuditRecord, Policy, PolicyDecision, PolicyEffect
from uuid import uuid4
from .containment import ContainmentUnavailable
from .sandbox import FixtureSandbox


def _durable_context(context) -> dict[str, Any]:
    """Return an action context that the JSON audit/event columns can store.

    ``network.request`` carries a ``bytes`` body. Durable records keep only its
    length and digest: the body is request data, not audit evidence.
    """

    return {
        key: f"<bytes len={len(value)} sha256={hashlib.sha256(value).hexdigest()}>"
        if isinstance(value, (bytes, bytearray)) else value
        for key, value in dict(context).items()
    }


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

    # Optional: ``requested(request: ActionRequest) -> None`` emits
    # ``tool_requested`` when the boundary receives a structured request.


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


_EXECUTION_KEY = ["attempt_id", "action_id", "event"]
_EXECUTION_WHERE = ToolAuditRow.event != AuditEvent.POLICY_DECIDED.value


class SqlAlchemyAuditSink:
    """Persist immutable Tool Proxy audit observations alongside SDF state."""

    def __init__(self, db: Session, *, principal: str | None = None):
        self.db = db
        # The authenticated principal behind every row this sink writes (#51).
        self.principal = principal

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
            "context": _durable_context(request.context),
            "event": "action_claimed",
            "decision": decision.effect.value,
            "executed": False,
            "outcome": "claimed",
            "detail": "executor claim acquired",
            "created_at": decision.created_at,
            "principal": self.principal,
        }
        bind = self.db.get_bind()
        dialect = bind.dialect.name
        if dialect == "sqlite":
            self.db.execute(sqlite_insert(ToolAuditRow).values(**values).on_conflict_do_nothing(
                index_elements=_EXECUTION_KEY, index_where=_EXECUTION_WHERE,
            ))
        elif dialect == "postgresql":
            self.db.execute(postgres_insert(ToolAuditRow).values(**values).on_conflict_do_nothing(
                index_elements=_EXECUTION_KEY, index_where=_EXECUTION_WHERE,
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
            "context": _durable_context(record.context),
            "event": record.event.value,
            "decision": record.decision.value,
            "executed": record.executed,
            "outcome": record.outcome,
            "detail": record.detail,
            "created_at": record.created_at,
            "principal": self.principal,
        }
        bind = self.db.get_bind()
        dialect = bind.dialect.name
        if record.event is AuditEvent.POLICY_DECIDED:
            # One decision row per decision (ADR 0026): a repeated request or
            # another principal's identical request is still recorded; only a
            # redelivery of the very same record (same audit_id) is a no-op.
            if dialect == "sqlite":
                self.db.execute(sqlite_insert(ToolAuditRow).values(**values).on_conflict_do_nothing(
                    index_elements=["audit_id"]))
            elif dialect == "postgresql":
                self.db.execute(postgres_insert(ToolAuditRow).values(**values).on_conflict_do_nothing(
                    index_elements=["audit_id"]))
            elif self.db.get(ToolAuditRow, record.audit_id) is None:
                self.db.execute(insert(ToolAuditRow).values(**values))
            return
        if dialect == "sqlite":
            self.db.execute(sqlite_insert(ToolAuditRow).values(**values).on_conflict_do_nothing(
                index_elements=_EXECUTION_KEY, index_where=_EXECUTION_WHERE,
            ))
        elif dialect == "postgresql":
            self.db.execute(postgres_insert(ToolAuditRow).values(**values).on_conflict_do_nothing(
                index_elements=_EXECUTION_KEY, index_where=_EXECUTION_WHERE,
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

    def __init__(self, db: Session, *, source: str = TOOL_PROXY_SOURCE, principal: str | None = None):
        if source not in TRUSTED_ACTION_SOURCES:
            raise UntrustedEventError(f"tool events must use a trusted action-boundary source, not {source!r}")
        self.db = db
        self.source = source
        self.principal = principal
        self.events = SqlAlchemyRuntimeEventSink(db)

    def _emit(self, kind: RuntimeEventKind, attempt_id: str, payload: dict[str, Any], dedupe: tuple[str, str]) -> None:
        session_id = f"tool:{attempt_id}"
        existing = self.db.scalars(
            select(RuntimeEventRow).where(
                (RuntimeEventRow.source == self.source)
                & (RuntimeEventRow.attempt_id == attempt_id)
                & (RuntimeEventRow.session_id == session_id)
            )
        ).all()
        field, value = dedupe
        if any(
            row.kind == kind.value and isinstance(row.payload, dict) and row.payload.get(field) == value
            for row in existing
        ):
            return
        current = max((row.sequence for row in existing), default=0)
        self.events.append(RuntimeEvent(
            kind=kind,
            session_id=session_id,
            attempt_id=attempt_id,
            sequence=int(current) + 1,
            status=RuntimeStatus.RUNNING,
            payload=payload,
            source=self.source,
        ))

    def requested(self, request: ActionRequest) -> None:
        """``tool_requested``: the boundary received this structured request.

        Recorded once per action identity; each delivery's principal and
        decision are on the ``tool_policy_decided`` events that follow.
        """

        self._emit(RuntimeEventKind.TOOL_REQUESTED, request.attempt_id, {
            "action_id": request.action_id,
            "actor": request.actor,
            "tool": request.tool,
            "action": request.action,
            "resource": request.resource,
            "context": _durable_context(request.context),
            **({"principal": self.principal} if self.principal is not None else {}),
        }, ("action_id", request.action_id))

    def append(self, record: AuditRecord) -> None:
        kind = self._KINDS[record.event]
        # Execution outcomes are deduplicated per action; decisions per audit
        # row, so each delivery's decision (and principal) is observable.
        per_delivery = record.event is AuditEvent.POLICY_DECIDED
        dedupe = ("audit_id", record.audit_id) if per_delivery else ("action_id", record.action_id)
        self._emit(kind, record.attempt_id, {
            "audit_id": record.audit_id,
            "action_id": record.action_id,
            "actor": record.actor,
            "tool": record.tool,
            "action": record.action,
            "resource": record.resource,
            "context": _durable_context(record.context),
            "decision": record.decision.value,
            "executed": record.executed,
            "outcome": record.outcome,
            "detail": record.detail,
            **({"principal": self.principal} if self.principal is not None else {}),
        }, dedupe)


class AttemptBinding:
    """Bind an HTTP Tool Proxy call to the Attempt it targets (ADR 0026).

    Checked before the attempt guard and the configured policy, so a forged
    identity is always reported as such (403), even on a terminal Attempt:

    * ``claimed_actor``: a caller-sent actor must equal the server-derived
      ``agent:<attempt.agent>``;
    * ``principal``/``owner``: only the principal that started the Attempt,
      or an admin, may drive its tools. An Attempt without an owner (internal
      or created before migration 0018) is admin-only.
    """

    ACTOR = "actor-binding"
    OWNER = "attempt-binding"

    def __init__(self, *, claimed_actor: str | None, principal: str, owner: str | None, is_admin: bool):
        self.claimed_actor = claimed_actor
        self.principal = principal
        self.owner = owner
        self.is_admin = is_admin

    def check(self, request: ActionRequest) -> PolicyDecision | None:
        if self.claimed_actor is not None and self.claimed_actor != request.actor:
            return PolicyDecision.deny(
                request,
                reason=f"claimed actor {self.claimed_actor[:120]!r} does not match the attempt's agent {request.actor!r}",
                policy=self.ACTOR,
            )
        if not self.is_admin and (self.owner is None or self.owner != self.principal):
            return PolicyDecision.deny(
                request,
                reason=f"principal {self.principal!r} did not start this attempt"
                + ("" if self.owner is not None else " (attempt has no owner; admin only)"),
                policy=self.OWNER,
            )
        return None


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
        binding: AttemptBinding | None = None,
    ):
        self.binding = binding
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
        requested = getattr(self.event_sink, "requested", None)
        if callable(requested):
            requested(request)

        binding_denial = self.binding.check(request) if self.binding is not None else None
        if binding_denial is not None:
            decision = binding_denial
        elif self.attempt_guard is not None and not self.attempt_guard.is_active(request.attempt_id):
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
