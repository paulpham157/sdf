from __future__ import annotations

from pathlib import Path

import pytest

from sdf_core.policy import ActionRequest, AllowlistPolicy, PolicyEffect
from sdf_core.tools import InMemoryAuditSink, ToolExecutionStatus, ToolProxy


class RecordingExecutor:
    def __init__(self, target: Path):
        self.target = target
        self.calls: list[ActionRequest] = []

    def execute(self, request: ActionRequest) -> str:
        self.calls.append(request)
        self.target.write_text(request.resource, encoding="utf-8")
        return "written"


def action(*, attempt_id: str = "ATTEMPT-001", resource: str = "fixture/app.py") -> ActionRequest:
    return ActionRequest(
        attempt_id=attempt_id,
        actor="agent:codex",
        tool="filesystem",
        action="write",
        resource=resource,
        context={"workspace": "fixture", "source": "structured-request"},
    )


def test_action_identity_is_attempt_bound_and_contextual():
    first = action()
    same_action = action()
    other_attempt = action(attempt_id="ATTEMPT-002")
    other_resource = action(resource="fixture/other.py")

    assert first.action_id == same_action.action_id
    assert first.action_id != other_attempt.action_id
    assert first.action_id != other_resource.action_id
    assert first.attempt_id == "ATTEMPT-001"
    assert first.context["source"] == "structured-request"


def test_allowed_action_is_decided_before_executor_and_audited(tmp_path: Path):
    target = tmp_path / "result"
    executor = RecordingExecutor(target)
    audit = InMemoryAuditSink()
    proxy = ToolProxy(
        policy=AllowlistPolicy({("filesystem", "write")} ),
        executor=executor,
        audit=audit,
    )

    result = proxy.execute(action())

    assert result.status is ToolExecutionStatus.EXECUTED
    assert result.decision.effect is PolicyEffect.ALLOW
    assert result.value == "written"
    assert target.read_text(encoding="utf-8") == "fixture/app.py"
    assert executor.calls == [action()]
    assert [record.event for record in result.audit_records] == [
        "policy_decided",
        "action_executed",
    ]
    assert all(record.attempt_id == "ATTEMPT-001" for record in audit.records)
    assert audit.records[0].decision is PolicyEffect.ALLOW
    assert audit.records[1].executed is True


def test_denied_action_has_no_side_effect_and_is_audited(tmp_path: Path):
    target = tmp_path / "result"
    executor = RecordingExecutor(target)
    audit = InMemoryAuditSink()
    proxy = ToolProxy(
        policy=AllowlistPolicy({("filesystem", "read")} ),
        executor=executor,
        audit=audit,
    )

    result = proxy.execute(action())

    assert result.status is ToolExecutionStatus.DENIED
    assert result.decision.effect is PolicyEffect.DENY
    assert result.value is None
    assert executor.calls == []
    assert not target.exists()
    assert len(audit.records) == 1
    record = audit.records[0]
    assert record.attempt_id == "ATTEMPT-001"
    assert record.decision is PolicyEffect.DENY
    assert record.executed is False
    assert record.event == "policy_decided"


def test_terminal_text_is_not_a_trusted_tool_request(tmp_path: Path):
    executor = RecordingExecutor(tmp_path / "result")
    proxy = ToolProxy(
        policy=AllowlistPolicy({("filesystem", "write")} ),
        executor=executor,
    )

    with pytest.raises(TypeError, match="structured ActionRequest"):
        proxy.execute("filesystem.write fixture/app.py")  # type: ignore[arg-type]

    assert executor.calls == []
    assert not (tmp_path / "result").exists()


def test_policy_decision_cannot_cross_attempt_boundary(tmp_path: Path):
    class WrongAttemptPolicy:
        def decide(self, request: ActionRequest):
            from sdf_core.policy import PolicyDecision

            return PolicyDecision(
                attempt_id="ATTEMPT-OTHER",
                action_id=request.action_id,
                effect=PolicyEffect.ALLOW,
                reason="wrong identity",
                policy="test",
            )

    executor = RecordingExecutor(tmp_path / "result")
    proxy = ToolProxy(policy=WrongAttemptPolicy(), executor=executor)

    with pytest.raises(ValueError, match="attempt-bound"):
        proxy.execute(action())

    assert executor.calls == []
    assert not (tmp_path / "result").exists()


def test_allowlist_can_bind_actor_resource_and_context(tmp_path: Path):
    executor = RecordingExecutor(tmp_path / "result")
    proxy = ToolProxy(
        policy=AllowlistPolicy(
            {("filesystem", "write")},
            allowed_actors={"agent:codex"},
            allowed_resources={"fixture/app.py"},
            required_context={"workspace": "fixture"},
        ),
        executor=executor,
    )

    allowed = proxy.execute(action())
    denied = proxy.execute(
        ActionRequest(
            attempt_id="ATTEMPT-001",
            actor="agent:other",
            tool="filesystem",
            action="write",
            resource="fixture/app.py",
            context={"workspace": "fixture"},
        )
    )

    assert allowed.status is ToolExecutionStatus.EXECUTED
    assert denied.status is ToolExecutionStatus.DENIED
    assert len(executor.calls) == 1
