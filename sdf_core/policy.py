from __future__ import annotations

import hashlib
import json
from collections.abc import Iterable, Mapping
from dataclasses import dataclass, field
from datetime import datetime, timezone
from enum import StrEnum
from types import MappingProxyType
from typing import Any, Protocol
from uuid import uuid4


def _utcnow() -> datetime:
    return datetime.now(timezone.utc)


class PolicyEffect(StrEnum):
    """The two possible authorization outcomes at the Tool Proxy boundary."""

    ALLOW = "allow"
    DENY = "deny"


class AuditEvent(StrEnum):
    """Events emitted by the trusted action boundary."""

    POLICY_DECIDED = "policy_decided"
    ACTION_EXECUTED = "action_executed"
    ACTION_FAILED = "action_failed"


@dataclass(frozen=True, slots=True)
class ActionRequest:
    """A structured, Attempt-bound request for one tool action.

    The Tool Proxy accepts this value object rather than terminal text.  Its
    stable ``action_id`` includes every identity field, including context, so
    a policy decision cannot silently be reused for another Attempt or
    resource.
    """

    attempt_id: str
    actor: str
    tool: str
    action: str
    resource: str
    context: Mapping[str, Any] = field(default_factory=dict)

    def __post_init__(self) -> None:
        for field_name in ("attempt_id", "actor", "tool", "action", "resource"):
            value = getattr(self, field_name)
            if not isinstance(value, str) or not value.strip():
                raise ValueError(f"{field_name} must be a non-empty string")
        if not isinstance(self.context, Mapping):
            raise TypeError("context must be a mapping")
        # Keep callers from mutating the identity after policy evaluation.
        object.__setattr__(self, "context", MappingProxyType(dict(self.context)))

    @property
    def action_id(self) -> str:
        payload = {
            "attempt_id": self.attempt_id,
            "actor": self.actor,
            "tool": self.tool,
            "action": self.action,
            "resource": self.resource,
            "context": dict(self.context),
        }
        encoded = json.dumps(payload, sort_keys=True, separators=(",", ":"), default=str)
        return f"ACTION-{hashlib.sha256(encoded.encode("utf-8")).hexdigest()}"


@dataclass(frozen=True, slots=True)
class PolicyDecision:
    """An explicit authorization result for one ActionRequest."""

    attempt_id: str
    action_id: str
    effect: PolicyEffect
    reason: str
    policy: str
    created_at: datetime = field(default_factory=_utcnow)

    def __post_init__(self) -> None:
        if not self.attempt_id:
            raise ValueError("attempt_id must be non-empty")
        if not self.action_id:
            raise ValueError("action_id must be non-empty")
        if not self.reason.strip():
            raise ValueError("reason must be non-empty")
        if not self.policy.strip():
            raise ValueError("policy must be non-empty")
        object.__setattr__(self, "effect", PolicyEffect(self.effect))

    @property
    def allowed(self) -> bool:
        return self.effect is PolicyEffect.ALLOW

    @classmethod
    def allow(cls, request: ActionRequest, *, reason: str, policy: str) -> "PolicyDecision":
        return cls(
            attempt_id=request.attempt_id,
            action_id=request.action_id,
            effect=PolicyEffect.ALLOW,
            reason=reason,
            policy=policy,
        )

    @classmethod
    def deny(cls, request: ActionRequest, *, reason: str, policy: str) -> "PolicyDecision":
        return cls(
            attempt_id=request.attempt_id,
            action_id=request.action_id,
            effect=PolicyEffect.DENY,
            reason=reason,
            policy=policy,
        )


class Policy(Protocol):
    def decide(self, request: ActionRequest) -> PolicyDecision:
        """Return an explicit decision without executing the action."""


@dataclass(frozen=True, slots=True)
class AuditRecord:
    """Immutable audit observation correlated to an Attempt and action."""

    audit_id: str
    attempt_id: str
    action_id: str
    actor: str
    tool: str
    action: str
    resource: str
    context: Mapping[str, Any]
    event: AuditEvent
    decision: PolicyEffect
    executed: bool
    outcome: str
    detail: str
    created_at: datetime = field(default_factory=_utcnow)

    @classmethod
    def for_request(
        cls,
        request: ActionRequest,
        *,
        event: AuditEvent,
        decision: PolicyDecision,
        executed: bool,
        outcome: str,
        detail: str,
    ) -> "AuditRecord":
        return cls(
            audit_id=f"AUDIT-{uuid4().hex[:16]}",
            attempt_id=request.attempt_id,
            action_id=request.action_id,
            actor=request.actor,
            tool=request.tool,
            action=request.action,
            resource=request.resource,
            context=request.context,
            event=event,
            decision=decision.effect,
            executed=executed,
            outcome=outcome,
            detail=detail,
        )


class AllowlistPolicy:
    """Small deterministic policy for the pre-sandbox Tool Proxy slice.

    This checks tool/action identity only.  Filesystem, process and network
    enforcement belongs to the enforced sandbox ticket; an allow decision
    here never implies that the underlying resource is safe to access.
    """

    def __init__(
        self,
        allowed_actions: Iterable[tuple[str, str]] = (),
        *,
        allowed_actors: Iterable[str] | None = None,
        allowed_resources: Iterable[str] | None = None,
        required_context: Mapping[str, Any] | None = None,
        name: str = "allowlist",
    ):
        self._allowed_actions = frozenset(allowed_actions)
        self._allowed_actors = frozenset(allowed_actors) if allowed_actors is not None else None
        self._allowed_resources = frozenset(allowed_resources) if allowed_resources is not None else None
        self._required_context = dict(required_context or {})
        self.name = name

    def decide(self, request: ActionRequest) -> PolicyDecision:
        key = (request.tool, request.action)
        actor_allowed = self._allowed_actors is None or request.actor in self._allowed_actors
        resource_allowed = self._allowed_resources is None or request.resource in self._allowed_resources
        context_allowed = all(request.context.get(k) == v for k, v in self._required_context.items())
        if key in self._allowed_actions and actor_allowed and resource_allowed and context_allowed:
            return PolicyDecision.allow(
                request,
                reason=f"{request.tool}.{request.action} is allowlisted",
                policy=self.name,
            )
        return PolicyDecision.deny(
            request,
            reason=f"{request.tool}.{request.action} is not allowlisted",
            policy=self.name,
        )
