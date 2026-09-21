import os
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from threading import Barrier

import pytest
from alembic import command
from alembic.config import Config
from sqlalchemy import inspect, text
from sqlalchemy.exc import DBAPIError
from sqlalchemy.orm import sessionmaker

from sdf_core.adapter import FakeNativeAdapter
from sdf_core.db import GraphNodeRow, RuntimeEventRow, TaskRow, ToolAuditRow, make_engine
from sdf_core.execution import ExecutionService
from sdf_core.model import utcnow
from sdf_core.policy import ActionRequest, AllowlistPolicy, AuditEvent, AuditRecord
from sdf_core.runtime import RuntimeEvent, RuntimeEventConflictError, RuntimeEventKind, RuntimeStatus, SqlAlchemyRuntimeEventSink
from sdf_core.tools import SqlAlchemyAuditSink


POSTGRES_URL = os.getenv("SDF_POSTGRES_TEST_URL")


@pytest.fixture(scope="module")
def postgres_engine():
    if not POSTGRES_URL:
        pytest.skip("SDF_POSTGRES_TEST_URL is not set")
    config = Config(str(Path(__file__).parents[1] / "alembic.ini"))
    os.environ["SDF_DATABASE_URL"] = POSTGRES_URL
    command.upgrade(config, "head")
    engine = make_engine(POSTGRES_URL)
    try:
        yield engine
    finally:
        engine.dispose()


@pytest.fixture(autouse=True)
def clean_postgres(postgres_engine):
    with postgres_engine.begin() as connection:
        connection.execute(text(
            "TRUNCATE TABLE decision_edges, evidence, tool_audits, runtime_events, "
            "artifacts, attempts, tasks, graph_nodes, objective_metrics, "
            "outcome_observations RESTART IDENTITY CASCADE"
        ))


def _session(postgres_engine):
    return sessionmaker(postgres_engine, expire_on_commit=False)()


def _runtime_event(*, payload=None):
    return RuntimeEvent(
        source="runtime", attempt_id="ATTEMPT-PG-EVENT", session_id="SESSION-PG-EVENT",
        sequence=1, kind=RuntimeEventKind.OUTPUT_OBSERVED, status=RuntimeStatus.RUNNING,
        payload=payload or {"text": "same"},
    )


@pytest.mark.skipif(not POSTGRES_URL, reason="SDF_POSTGRES_TEST_URL is not set")
def test_vertical_slice_runs_against_postgresql(tmp_path: Path, postgres_engine):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    with _session(postgres_engine) as db:
        db.add(GraphNodeRow(id="ASSUMPTION-PG-001", kind="assumption", title="postgres works", source="test", owner="product", confidence=1.0, created_at=utcnow()))
        db.add(TaskRow(id="TASK-PG-001", title="postgres slice", status="created", idempotency_key="task-pg-001", acceptance_criteria=["command succeeds"], created_at=utcnow()))
        db.commit()
        service = ExecutionService(db, workspace_root=tmp_path / "workspaces", artifact_root=tmp_path / "artifacts", adapter=FakeNativeAdapter("app.py", "print('new')\n"))
        attempt = service.run(
            task_id="TASK-PG-001",
            dispatch_key="dispatch-pg-001",
            fixture=fixture,
            instructions="update",
            commands=[["python", "-c", "print('ok')"]],
            criterion_checks={"command succeeds": [["python", "-c", "print('ok')"]]},
            validation_target=("assumption", "ASSUMPTION-PG-001"),
        )
        assert attempt.status == "completed"
        assert db.get(TaskRow, "TASK-PG-001").status == "succeeded"


def test_alembic_head_created_postgres_schema_and_append_only_triggers(postgres_engine):
    inspector = inspect(postgres_engine)
    assert "alembic_version" in inspector.get_table_names()
    assert {column["name"] for column in inspector.get_columns("evidence")} >= {"criterion", "measured_at"}
    assert {column["name"] for column in inspector.get_columns("tool_audits")} >= {"audit_id", "event"}
    with postgres_engine.begin() as connection:
        connection.execute(text("INSERT INTO evidence (id, attempt_id, kind, status, command, exit_code, artifact_ref, confidence, criterion, measured_at, evidence_mode, created_at) VALUES ('EVIDENCE-PG-TRIGGER', 'ATTEMPT-PG-TRIGGER', 'evaluation', 'PASS', 'pytest', 0, NULL, 1.0, NULL, now(), 'synthetic', now())"))
        connection.execute(text("INSERT INTO tool_audits (audit_id, attempt_id, action_id, actor, tool, action, resource, context, event, decision, executed, outcome, detail, created_at) VALUES ('AUDIT-PG-TRIGGER', 'ATTEMPT-PG-TRIGGER', 'ACTION-PG-TRIGGER', 'test', 'filesystem', 'read', '.', '{}', 'action_claimed', 'allow', false, 'claimed', 'test', now())"))
    for statement in (
        "UPDATE evidence SET status = 'FAIL' WHERE id = 'EVIDENCE-PG-TRIGGER'",
        "DELETE FROM evidence WHERE id = 'EVIDENCE-PG-TRIGGER'",
        "UPDATE tool_audits SET outcome = 'changed' WHERE audit_id = 'AUDIT-PG-TRIGGER'",
        "DELETE FROM tool_audits WHERE audit_id = 'AUDIT-PG-TRIGGER'",
    ):
        with pytest.raises(DBAPIError, match="append-only"):
            with postgres_engine.begin() as connection:
                connection.execute(text(statement))


def test_runtime_event_replay_is_idempotent_and_conflicting(postgres_engine):
    with _session(postgres_engine) as db:
        sink = SqlAlchemyRuntimeEventSink(db)
        first = sink.append(_runtime_event())
        replay = sink.append(_runtime_event())
        db.commit()
        assert replay.id == first.id
        assert db.query(RuntimeEventRow).count() == 1
        with pytest.raises(RuntimeEventConflictError):
            sink.append(_runtime_event(payload={"text": "different"}))
        db.rollback()
        assert db.query(RuntimeEventRow).count() == 1


def test_tool_audit_replay_is_idempotent_on_postgresql(postgres_engine):
    request = ActionRequest(attempt_id="ATTEMPT-PG-AUDIT", actor="agent:test", tool="filesystem", action="read", resource=".", context={})
    decision = AllowlistPolicy({("filesystem", "read")}).decide(request)
    with _session(postgres_engine) as db:
        sink = SqlAlchemyAuditSink(db)
        sink.append(AuditRecord.for_request(request, event=AuditEvent.POLICY_DECIDED, decision=decision, executed=False, outcome="allow", detail="first"))
        sink.append(AuditRecord.for_request(request, event=AuditEvent.POLICY_DECIDED, decision=decision, executed=False, outcome="allow", detail="redelivery"))
        db.commit()
        assert db.query(ToolAuditRow).count() == 1


def test_postgresql_claim_race_has_exactly_one_winner(postgres_engine):
    request = ActionRequest(attempt_id="ATTEMPT-PG-RACE", actor="agent:test", tool="filesystem", action="write", resource=".", context={})
    decision = AllowlistPolicy({("filesystem", "write")}).decide(request)
    barrier = Barrier(8)

    def claim_once(_index):
        with _session(postgres_engine) as db:
            barrier.wait()
            won = SqlAlchemyAuditSink(db).claim_action(request, decision)
            db.commit()
            return won

    with ThreadPoolExecutor(max_workers=8) as pool:
        winners = list(pool.map(claim_once, range(8)))
    with _session(postgres_engine) as db:
        assert sum(winners) == 1
        assert db.query(ToolAuditRow).filter_by(attempt_id="ATTEMPT-PG-RACE", event="action_claimed").count() == 1
