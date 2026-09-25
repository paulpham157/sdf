from __future__ import annotations

from pathlib import Path
import sys

import pytest

from sdf_core.policy import ActionRequest, AllowlistPolicy, AuditEvent, AuditRecord, PolicyEffect
from sdf_core.db import AttemptRow, Base, RuntimeEventRow, ToolAuditRow, make_engine
from sdf_core.model import utcnow
from sdf_core.sandbox import FixtureSandbox, NetworkAccessDenied
from sdf_core.tools import InMemoryAuditSink, SandboxToolExecutor, SqlAlchemyAuditSink, SqlAlchemyAttemptGuard, SqlAlchemyToolEventSink, ToolExecutionStatus, ToolProxy
from sqlalchemy.orm import sessionmaker


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


def test_sandbox_executor_keeps_structured_actions_inside_proxy_boundary(tmp_path: Path):
    sandbox = FixtureSandbox(tmp_path / "fixture")
    audit = InMemoryAuditSink()
    proxy = ToolProxy(
        policy=AllowlistPolicy({("filesystem", "write")}),
        executor=SandboxToolExecutor(sandbox),
        audit=audit,
    )

    result = proxy.execute(ActionRequest(
        attempt_id="ATTEMPT-SANDBOX",
        actor="agent:codex",
        tool="filesystem",
        action="write",
        resource="notes/result.txt",
        context={"data": "approved"},
    ))

    assert result.status is ToolExecutionStatus.EXECUTED
    assert (tmp_path / "fixture" / "notes" / "result.txt").read_text() == "approved"
    assert [record.event for record in audit.for_attempt("ATTEMPT-SANDBOX")] == [
        "policy_decided", "action_executed"
    ]


def test_sandbox_executor_rejects_network_without_policy_or_capability(tmp_path: Path):
    sandbox = FixtureSandbox(tmp_path / "fixture")
    proxy = ToolProxy(
        policy=AllowlistPolicy({("network", "request")}),
        executor=SandboxToolExecutor(sandbox),
    )

    result = proxy.execute(ActionRequest(
        attempt_id="ATTEMPT-NETWORK",
        actor="agent:codex",
        tool="network",
        action="request",
        resource="https://example.test",
        context={},
    ))

    assert result.status is ToolExecutionStatus.FAILED
    assert isinstance(result.error, str)
    assert "network is disabled" in result.error


def test_sandbox_executor_process_uses_sandbox_cwd_and_limits(tmp_path: Path):
    sandbox = FixtureSandbox(tmp_path / "fixture", max_output_bytes=32)
    proxy = ToolProxy(
        policy=AllowlistPolicy({("process", "run")}),
        executor=SandboxToolExecutor(sandbox),
    )

    result = proxy.execute(ActionRequest(
        attempt_id="ATTEMPT-PROCESS",
        actor="agent:codex",
        tool="process",
        action="run",
        resource=".",
        context={"command": [sys.executable, "-c", "print('ok')"]},
    ))

    assert result.status is ToolExecutionStatus.EXECUTED
    assert result.value.stdout.strip() == "ok"
    assert result.value.cwd == (tmp_path / "fixture").resolve()


def test_sqlalchemy_audit_sink_persists_attempt_bound_decisions(tmp_path: Path):
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        db.add(AttemptRow(
            id="ATTEMPT-AUDIT", task_id="TASK-AUDIT", status="running", dispatch_key="audit-dispatch",
            agent="codex", created_at=utcnow(),
        ))
        db.commit()
        proxy = ToolProxy(
            policy=AllowlistPolicy({("filesystem", "read")}),
            executor=SandboxToolExecutor(FixtureSandbox(tmp_path / "fixture")),
            audit=SqlAlchemyAuditSink(db),
        )
        proxy.execute(ActionRequest(
            attempt_id="ATTEMPT-AUDIT", actor="agent:codex", tool="filesystem",
            action="read", resource="missing.txt", context={},
        ))
        db.commit()

        rows = db.query(ToolAuditRow).filter_by(attempt_id="ATTEMPT-AUDIT").all()
        assert len(rows) == 3
        assert {row.event for row in rows} == {"policy_decided", "action_claimed", "action_failed"}
        assert all(row.action_id.startswith("ACTION-") for row in rows)


def test_e2b_containment_disables_network_request_even_if_executor_is_direct(tmp_path: Path):
    proxy = ToolProxy(
        policy=AllowlistPolicy({("network", "request")}),
        executor=SandboxToolExecutor(
            FixtureSandbox(tmp_path / "fixture"),
            network_actions_allowed=False,
        ),
        audit=InMemoryAuditSink(),
    )
    result = proxy.execute(ActionRequest(
        attempt_id="ATTEMPT-E2B-NETWORK-DIRECT",
        actor="agent:test",
        tool="network",
        action="request",
        resource="https://example.invalid",
        context={"method": "GET", "body": b""},
    ))
    assert result.status is ToolExecutionStatus.FAILED
    assert "network.request is disabled" in (result.error or "")


def test_sqlalchemy_audit_sink_replay_is_idempotent_by_attempt_action_event():
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        request = action(attempt_id="ATTEMPT-REPLAY")
        decision = AllowlistPolicy({("filesystem", "write")}).decide(request)
        sink = SqlAlchemyAuditSink(db)
        sink.append(AuditRecord.for_request(
            request, event=AuditEvent.POLICY_DECIDED, decision=decision,
            executed=False, outcome="allow", detail="first",
        ))
        db.commit()
        sink.append(AuditRecord.for_request(
            request, event=AuditEvent.POLICY_DECIDED, decision=decision,
            executed=False, outcome="allow", detail="redelivery",
        ))
        db.commit()
        assert db.query(ToolAuditRow).filter_by(attempt_id="ATTEMPT-REPLAY").count() == 1


def test_sqlalchemy_audit_sink_claim_has_one_winner():
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        request = action(attempt_id="ATTEMPT-CLAIM")
        decision = AllowlistPolicy({("filesystem", "write")}).decide(request)
        sink = SqlAlchemyAuditSink(db)
        assert sink.claim_action(request, decision) is True
        assert sink.claim_action(request, decision) is False
        db.commit()
        assert db.query(ToolAuditRow).filter_by(
            attempt_id="ATTEMPT-CLAIM", event="action_claimed",
        ).count() == 1


def test_attempt_guard_denies_missing_or_terminal_attempt_before_executor(tmp_path: Path):
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        db.add(AttemptRow(
            id="ATTEMPT-DONE", task_id="TASK-DONE", status="completed",
            dispatch_key="dispatch-done", agent="codex", created_at=utcnow(),
        ))
        db.add(AttemptRow(
            id="ATTEMPT-ACTIVE", task_id="TASK-ACTIVE", status="running",
            dispatch_key="dispatch-active", agent="codex", created_at=utcnow(),
        ))
        db.commit()
        executor = RecordingExecutor(tmp_path / "result")
        audit = InMemoryAuditSink()
        proxy = ToolProxy(
            policy=AllowlistPolicy({("filesystem", "write")}),
            executor=executor,
            audit=audit,
            attempt_guard=SqlAlchemyAttemptGuard(db),
        )

        missing = proxy.execute(action(attempt_id="ATTEMPT-MISSING"))
        terminal = proxy.execute(action(attempt_id="ATTEMPT-DONE"))
        active = proxy.execute(action(attempt_id="ATTEMPT-ACTIVE"))

        assert missing.status is ToolExecutionStatus.DENIED
        assert terminal.status is ToolExecutionStatus.DENIED
        assert active.status is ToolExecutionStatus.EXECUTED
        assert len(executor.calls) == 1
        assert [record.decision for record in audit.records[:2]] == [PolicyEffect.DENY, PolicyEffect.DENY]


def test_tool_proxy_emits_trusted_source_labelled_events_and_replays_idempotently(tmp_path: Path):
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        request = action(attempt_id="ATTEMPT-TOOL-EVENT")
        executor = RecordingExecutor(tmp_path / "result")
        proxy = ToolProxy(
            policy=AllowlistPolicy({("filesystem", "write")}),
            executor=executor,
            audit=SqlAlchemyAuditSink(db),
            event_sink=SqlAlchemyToolEventSink(db),
        )
        result = proxy.execute(request)
        assert result.status is ToolExecutionStatus.EXECUTED
        db.commit()
        rows = db.query(RuntimeEventRow).filter_by(
            source="tool-proxy", attempt_id="ATTEMPT-TOOL-EVENT"
        ).all()
        assert [row.kind for row in rows] == ["tool_policy_decided", "tool_action_executed"]
        assert all(row.payload["action_id"] == request.action_id for row in rows)

        # Re-delivery of the same structured action does not append another
        # normalized event, even though a new AuditRecord receives a new id.
        proxy.execute(request)
        db.commit()
        assert db.query(RuntimeEventRow).filter_by(
            source="tool-proxy", attempt_id="ATTEMPT-TOOL-EVENT"
        ).count() == 2
        assert executor.calls == [request]


def test_durable_failed_action_is_not_reexecuted_on_redelivery(tmp_path: Path):
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        request = action(attempt_id="ATTEMPT-TOOL-FAILED")

        class FailingExecutor(RecordingExecutor):
            def execute(self, request):
                self.calls.append(request)
                raise RuntimeError("fixture failed")

        executor = FailingExecutor(tmp_path / "result")
        proxy = ToolProxy(
            policy=AllowlistPolicy({("filesystem", "write")}),
            executor=executor,
            audit=SqlAlchemyAuditSink(db),
        )
        first = proxy.execute(request)
        db.commit()
        second = proxy.execute(request)
        db.commit()

        assert first.status is ToolExecutionStatus.FAILED
        assert second.status is ToolExecutionStatus.FAILED
        assert "new action identity" in (second.error or "")
        assert executor.calls == [request]


def test_tool_audit_rows_are_append_only_at_orm_boundary(tmp_path: Path):
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        request = action(attempt_id="ATTEMPT-AUDIT-IMMUTABLE")
        proxy = ToolProxy(
            policy=AllowlistPolicy({("filesystem", "write")}),
            executor=RecordingExecutor(tmp_path / "result"),
            audit=SqlAlchemyAuditSink(db),
        )
        proxy.execute(request)
        db.commit()
        row = db.query(ToolAuditRow).filter_by(
            attempt_id=request.attempt_id, event="policy_decided",
        ).one()
        row.detail = "tampered"
        with pytest.raises(ValueError, match="append-only"):
            db.flush()
        db.rollback()

        row = db.query(ToolAuditRow).filter_by(
            attempt_id=request.attempt_id, event="policy_decided",
        ).one()
        db.delete(row)
        with pytest.raises(ValueError, match="append-only"):
            db.flush()


def test_sqlalchemy_audit_persists_a_network_request_with_a_bytes_body(tmp_path: Path):
    # network.request requires a bytes body; the durable audit and event
    # records must store it as JSON instead of crashing the Tool Proxy.
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        db.add(AttemptRow(id="ATTEMPT-NET-BYTES", task_id="TASK-NET-BYTES", status="running",
                          dispatch_key="dispatch-net-bytes", agent="test", created_at=utcnow()))
        db.commit()
        proxy = ToolProxy(
            policy=AllowlistPolicy({("network", "request")}),
            executor=SandboxToolExecutor(FixtureSandbox(tmp_path / "fixture"), network_actions_allowed=False),
            audit=SqlAlchemyAuditSink(db),
            attempt_guard=SqlAlchemyAttemptGuard(db),
            event_sink=SqlAlchemyToolEventSink(db),
        )
        result = proxy.execute(ActionRequest(
            attempt_id="ATTEMPT-NET-BYTES", actor="agent:test", tool="network", action="request",
            resource="https://example.invalid", context={"method": "POST", "body": b"secret-ish payload"},
        ))
        db.commit()
        assert result.status is ToolExecutionStatus.FAILED
        assert "network.request is disabled" in (result.error or "")
        rows = db.query(ToolAuditRow).filter_by(attempt_id="ATTEMPT-NET-BYTES").all()
        assert {row.event for row in rows} >= {"policy_decided", "action_failed"}
        body = rows[0].context["body"]
        assert body.startswith("<bytes len=18 sha256=") and "secret-ish" not in body
        kinds = {e.kind for e in db.query(RuntimeEventRow).filter_by(attempt_id="ATTEMPT-NET-BYTES", source="tool-proxy")}
        assert {"tool_policy_decided", "tool_action_failed"} <= kinds
