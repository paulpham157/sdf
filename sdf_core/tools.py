from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import Any, Protocol

from .policy import ActionRequest, AuditEvent, AuditRecord, Policy, PolicyDecision, PolicyEffect


class ToolExecutor(Protocol):
    def execute(self, request: ActionRequest) -> Any:
        """Perform one already-authorized structured action."""


class AuditSink(Protocol):
    def append(self, record: AuditRecord) -> None:
        """Persist or forward one trusted SDF audit record."""


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


class ToolProxy:
    """Authorize a structured action before handing it to an executor."""

    def __init__(self, *, policy: Policy, executor: ToolExecutor, audit: AuditSink | None = None):
        self.policy = policy
        self.executor = executor
        self.audit = audit or InMemoryAuditSink()

    def execute(self, request: ActionRequest) -> ToolExecution:
        if not isinstance(request, ActionRequest):
            raise TypeError("ToolProxy requires a structured ActionRequest; terminal text is not trusted")

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
        self.audit.append(decision_audit)

        if decision.effect is PolicyEffect.DENY:
            return ToolExecution(
                action=request,
                decision=decision,
                status=ToolExecutionStatus.DENIED,
                audit_records=(decision_audit,),
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
            self.audit.append(failed_audit)
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
        self.audit.append(executed_audit)
        return ToolExecution(
            action=request,
            decision=decision,
            status=ToolExecutionStatus.EXECUTED,
            value=value,
            audit_records=(decision_audit, executed_audit),
        )
