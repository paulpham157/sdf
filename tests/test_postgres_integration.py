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
            "TRUNCATE TABLE decision_edges, evidence, tool_audits, runtime_events, routing_decisions, "
            "artifacts, attempts, tasks, graph_nodes, objective_metrics, "
            "outcome_observations, agent_claims, agent_messages, agent_run_members, agent_runs, "
            "plan_tasks, plans, decision_requests, api_tokens, auth_audit RESTART IDENTITY CASCADE"
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
        first = AuditRecord.for_request(request, event=AuditEvent.POLICY_DECIDED, decision=decision, executed=False, outcome="allow", detail="first")
        sink.append(first)
        sink.append(first)  # redelivery of the same record
        db.commit()
        assert db.query(ToolAuditRow).filter_by(attempt_id="ATTEMPT-PG-AUDIT").count() == 1
        # ADR 0026: execution outcomes are deduplicated per action.
        for detail in ("ran", "redelivered"):
            sink.append(AuditRecord.for_request(request, event=AuditEvent.ACTION_EXECUTED, decision=decision, executed=True, outcome="executed", detail=detail))
        db.commit()
        assert db.query(ToolAuditRow).filter_by(attempt_id="ATTEMPT-PG-AUDIT", event="action_executed").count() == 1


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


def test_postgres_plan_gate_and_agent_comm_triggers_fence_direct_writes(postgres_engine, tmp_path):
    from sqlalchemy import insert, update

    from sdf_core.agent_messaging import Member, MessageHub
    from sdf_core.db import AgentMessageRow, AgentRunRow, AttemptRow, DecisionRequestRow, PlanRow
    from sdf_core.model import utcnow
    from sdf_core.plans import PlanSpec, decide_plan, propose_plan, task_id_for
    from tests.agent_comm_support import make_fixture, plan_dict

    db = _session(postgres_engine)
    try:
        plan = propose_plan(db, PlanSpec(**plan_dict(make_fixture(tmp_path), "PLAN-PG")))
        db.commit()
        attempt = {"id": "ATTEMPT-PG-GATE", "task_id": task_id_for("PLAN-PG", "add"), "status": "running",
                   "dispatch_key": "pg-gate", "agent": "test", "created_at": utcnow()}
        # Core statements skip the ORM guards; the database itself must refuse.
        for statement in (
            insert(AttemptRow).values(**attempt),
            insert(AgentRunRow).values(id="RUN-PG", plan_id="PLAN-PG", status="running", config={},
                                       started_at=utcnow()),
            update(PlanRow).where(PlanRow.id == "PLAN-PG").values(status="approved"),
        ):
            with pytest.raises(DBAPIError, match="plan gate|requires a human"):
                db.execute(statement)
            db.rollback()
        decide_plan(db, "PLAN-PG", approve=True, decided_by="hung")
        db.commit()
        db.execute(insert(AttemptRow).values(**attempt))
        db.execute(insert(AgentRunRow).values(id="RUN-PG", plan_id="PLAN-PG", status="running", config={},
                                              started_at=utcnow()))
        db.commit()
        hub = MessageHub(db, "RUN-PG", {"lead-1": Member("lead-1", "lead", None, "sbx-l"),
                                        "worker-1": Member("worker-1", "worker", "add", "sbx-w")})
        hub.ingest("worker-1", b'{"id": "%s", "kind": "report", "body": "done"}' % (b"b" * 32), sandbox_id="sbx-w")
        db.commit()
        for statement in (
            update(AgentMessageRow).values(body="edited"),
            update(PlanRow).where(PlanRow.id == "PLAN-PG").values(title="changed"),
            update(DecisionRequestRow).where(DecisionRequestRow.id == plan.decision_request_id).values(
                status="rejected"),
            text("DELETE FROM agent_claims"),
            text("DELETE FROM plans"),
        ):
            with pytest.raises(DBAPIError):
                db.execute(statement)
            db.rollback()
    finally:
        db.close()


def test_postgres_api_tokens_and_auth_audit_are_fenced(postgres_engine):
    """ADR 0015 on PostgreSQL: tokens authenticate through the API; the audit
    log is append-only and a token's identity/roles/hash are immutable and
    revocation final, even for Core statements that skip the ORM guards."""

    from fastapi.testclient import TestClient
    from sqlalchemy import update

    from sdf_core import auth
    from sdf_core.api import app, get_db
    from sdf_core.db import ApiTokenRow, AuthAuditRow

    factory = sessionmaker(postgres_engine, expire_on_commit=False)

    def pg_db():
        db = factory()
        try:
            yield db
        finally:
            db.close()

    db = _session(postgres_engine)
    try:
        row, plaintext = auth.create_token(db, principal="pg-operator", roles=["operator"], created_by="test")
        db.commit()
        app.dependency_overrides[get_db] = pg_db
        try:
            client = TestClient(app, headers={"Authorization": f"Bearer {plaintext}"})
            assert client.get("/auth/whoami").json()["name"] == "pg-operator"
            created = client.post("/tasks", json={"id": "TASK-PG-AUTH", "title": "t", "idempotency_key": "pg-a"})
            assert created.status_code == 201, created.text
            assert TestClient(app).get("/auth/whoami").status_code == 401
        finally:
            app.dependency_overrides.pop(get_db, None)
        db.expire_all()
        events = {(r.event, r.principal) for r in db.query(AuthAuditRow).all()}
        assert {("token_created", "test"), ("mutation", "pg-operator"), ("auth_failed", None)} <= events
        assert db.get(ApiTokenRow, row.id).last_used_at is not None

        for statement in (
            update(AuthAuditRow).values(principal="someone-else"),
            text("DELETE FROM auth_audit"),
            update(ApiTokenRow).where(ApiTokenRow.id == row.id).values(roles=["admin"]),
            update(ApiTokenRow).where(ApiTokenRow.id == row.id).values(principal="hung"),
            update(ApiTokenRow).where(ApiTokenRow.id == row.id).values(secret_hash="0" * 64),
            text("DELETE FROM api_tokens"),
        ):
            with pytest.raises(DBAPIError):
                db.execute(statement)
            db.rollback()
        auth.revoke_token(db, row.id, revoked_by="test")
        db.commit()
        with pytest.raises(DBAPIError):
            db.execute(update(ApiTokenRow).where(ApiTokenRow.id == row.id).values(revoked_at=None))
        db.rollback()
        with pytest.raises(auth.AuthError, match="revoked"):
            auth.verify_token(db, plaintext, key=auth.pepper())
    finally:
        db.close()


@pytest.mark.skipif(not POSTGRES_URL, reason="SDF_POSTGRES_TEST_URL is not set")
def test_tool_audit_principal_column_round_trips_and_migrates_on_postgresql(postgres_engine):
    request = ActionRequest(attempt_id="ATTEMPT-PG-PRINCIPAL", actor="agent:test", tool="filesystem",
                            action="write", resource="a.txt", context={})
    decision = AllowlistPolicy({("filesystem", "write")}).decide(request)
    with _session(postgres_engine) as db:
        SqlAlchemyAuditSink(db, principal="op-pg").append(AuditRecord.for_request(
            request, event=AuditEvent.POLICY_DECIDED, decision=decision,
            executed=False, outcome="decided", detail=decision.reason,
        ))
        db.commit()
        row = db.query(ToolAuditRow).filter_by(attempt_id="ATTEMPT-PG-PRINCIPAL").one()
        assert row.principal == "op-pg"
        # ADR 0026: a second principal's decision on the same action is its own
        # row; execution outcomes stay deduplicated by the partial unique index.
        SqlAlchemyAuditSink(db, principal="op-pg-2").append(AuditRecord.for_request(
            request, event=AuditEvent.POLICY_DECIDED, decision=decision,
            executed=False, outcome="decided", detail=decision.reason,
        ))
        for _ in range(2):
            SqlAlchemyAuditSink(db, principal="op-pg").append(AuditRecord.for_request(
                request, event=AuditEvent.ACTION_EXECUTED, decision=decision,
                executed=True, outcome="executed", detail="ok",
            ))
        db.commit()
        rows = db.query(ToolAuditRow).filter_by(attempt_id="ATTEMPT-PG-PRINCIPAL").all()
        assert sorted(r.principal for r in rows if r.event == "policy_decided") == ["op-pg", "op-pg-2"]
        assert [r.event for r in rows].count("action_executed") == 1

    config = Config(str(Path(__file__).parents[1] / "alembic.ini"))
    os.environ["SDF_DATABASE_URL"] = POSTGRES_URL
    command.downgrade(config, "0017_trace_id")
    try:
        assert "principal" not in {c["name"] for c in inspect(postgres_engine).get_columns("tool_audits")}
        assert "principal" not in {c["name"] for c in inspect(postgres_engine).get_columns("attempts")}
        with postgres_engine.connect() as conn:  # duplicates folded, append-only trigger restored
            assert conn.execute(text("SELECT COUNT(*) FROM tool_audits WHERE attempt_id='ATTEMPT-PG-PRINCIPAL' "
                                     "AND event='policy_decided'")).scalar() == 1
            assert conn.execute(text("SELECT tgenabled FROM pg_trigger WHERE tgname='trg_tool_audits_append_only'"
                                     )).scalar() == "O"
    finally:
        command.upgrade(config, "head")
    assert "principal" in {c["name"] for c in inspect(postgres_engine).get_columns("tool_audits")}
    assert "principal" in {c["name"] for c in inspect(postgres_engine).get_columns("attempts")}
    assert "uq_tool_audits_execution_event" in {i["name"] for i in inspect(postgres_engine).get_indexes("tool_audits")}


def test_runtime_events_provenance_check_and_append_only_trigger(postgres_engine):
    """ADR 0029: tool_* kinds only from tool-proxy; rows are never updated or deleted."""

    insert_sql = ("INSERT INTO runtime_events (source, attempt_id, session_id, sequence, kind, status, payload, "
                  "created_at) VALUES (:source, 'ATTEMPT-PG-PROV', :session, :seq, :kind, 'running', '{}', now())")
    for source, kind in (("run-e2b", "tool_policy_decided"), ("tool-proxy", "runtime_output_observed")):
        with pytest.raises(DBAPIError, match="ck_runtime_events_provenance"):
            with postgres_engine.begin() as connection:
                connection.execute(text(insert_sql), {"source": source, "session": "S", "seq": 1, "kind": kind})
    with postgres_engine.begin() as connection:
        connection.execute(text(insert_sql), {"source": "tool-proxy", "session": "tool:X", "seq": 1,
                                              "kind": "tool_requested"})
        connection.execute(text(insert_sql), {"source": "run-e2b", "session": "S", "seq": 1,
                                              "kind": "runtime_output_observed"})
    for statement in (
        "UPDATE runtime_events SET status = 'completed' WHERE attempt_id = 'ATTEMPT-PG-PROV'",
        "DELETE FROM runtime_events WHERE attempt_id = 'ATTEMPT-PG-PROV'",
    ):
        with pytest.raises(DBAPIError, match="append-only"):
            with postgres_engine.begin() as connection:
                connection.execute(text(statement))


def test_routing_decisions_are_append_only_in_postgres(postgres_engine):
    """ADR 0030: a recorded routing decision is never rewritten."""

    with postgres_engine.begin() as connection:
        connection.execute(text(
            "INSERT INTO routing_decisions (id, attempt_id, selected_agent, mode, reason, request, candidates, "
            "created_at) VALUES ('ROUTE-PG', 'ATTEMPT-PG-ROUTE', 'pi', 'explicit', 'test', NULL, '[]', now())"))
    for statement in ("UPDATE routing_decisions SET selected_agent = 'opencode' WHERE id = 'ROUTE-PG'",
                      "DELETE FROM routing_decisions WHERE id = 'ROUTE-PG'"):
        with pytest.raises(DBAPIError, match="append-only"):
            with postgres_engine.begin() as connection:
                connection.execute(text(statement))
    with pytest.raises(DBAPIError, match="ck_routing_decisions_mode"):
        with postgres_engine.begin() as connection:
            connection.execute(text(
                "INSERT INTO routing_decisions (id, attempt_id, selected_agent, mode, reason, candidates, created_at) "
                "VALUES ('ROUTE-PG-2', 'ATTEMPT-PG-ROUTE-2', 'pi', 'caller', 'x', '[]', now())"))


@pytest.mark.skipif(not POSTGRES_URL, reason="SDF_POSTGRES_TEST_URL is not set")
@pytest.mark.parametrize("table", ["evidence", "attempts"])
def test_evidence_mode_check_constraint_and_migration_0021_on_postgresql(postgres_engine, monkeypatch, table):
    """ADR 0031: only 'synthetic' and 'live' are stored; 0021 never rewrites rows."""
    sql = {
        "evidence": "INSERT INTO evidence (id, attempt_id, kind, status, command, confidence, evidence_mode, "
                    "created_at) VALUES (:id, 'ATTEMPT-PG-MODE', 'evaluation', 'PASS', 'c', 1.0, :mode, now())",
        "attempts": "INSERT INTO attempts (id, task_id, status, dispatch_key, agent, evidence_mode, created_at) "
                    "VALUES (:id, 'TASK-PG-MODE', 'created', :id, 'pi', :mode, now())",
    }
    for each in sql.values():
        with postgres_engine.begin() as connection:
            connection.execute(text(each), {"id": "PG-LIVE", "mode": "live"})
        with pytest.raises(DBAPIError, match="evidence_mode"), postgres_engine.begin() as connection:
            connection.execute(text(each), {"id": "PG-BAD", "mode": "production"})

    config = Config(str(Path(__file__).parents[1] / "alembic.ini"))
    monkeypatch.setenv("SDF_DATABASE_URL", POSTGRES_URL)
    command.downgrade(config, "0020_routing_decisions")
    try:
        with postgres_engine.begin() as connection:  # constraint gone; a stray value can exist pre-0021
            connection.execute(text(sql[table]), {"id": "PG-ODD", "mode": "staging"})
        with pytest.raises(RuntimeError, match=f"1 {table} row"):
            command.upgrade(config, "head")
        with postgres_engine.connect() as connection:  # nothing was rewritten
            assert dict(connection.execute(text(f"SELECT id, evidence_mode FROM {table}")).all()) == {
                "PG-LIVE": "live", "PG-ODD": "staging"}
    finally:
        with postgres_engine.begin() as connection:
            connection.execute(text("TRUNCATE TABLE evidence, attempts"))

    # Operator path for large tables: pre-create NOT VALID, the migration validates and reuses it.
    with postgres_engine.begin() as connection:
        connection.execute(text("ALTER TABLE evidence ADD CONSTRAINT ck_evidence_evidence_mode "
                                "CHECK (evidence_mode IN ('synthetic', 'live')) NOT VALID"))
    command.upgrade(config, "head")
    with postgres_engine.connect() as connection:
        rows = dict(connection.execute(text(
            "SELECT conname, convalidated FROM pg_constraint "
            "WHERE conname IN ('ck_evidence_evidence_mode', 'ck_attempts_evidence_mode')")).all())
    assert rows == {"ck_evidence_evidence_mode": True, "ck_attempts_evidence_mode": True}
    with pytest.raises(DBAPIError, match="evidence_mode"), postgres_engine.begin() as connection:
        connection.execute(text(sql[table]), {"id": "PG-BAD2", "mode": "production"})


@pytest.mark.skipif(not POSTGRES_URL, reason="SDF_POSTGRES_TEST_URL is not set")
def test_tool_audit_evidence_mode_check_and_migration_0022_on_postgresql(postgres_engine, monkeypatch):
    """#71: tool_audits holds only known modes; 0022 keeps the trigger and the partial index, never rewrites."""
    sql = ("INSERT INTO tool_audits (audit_id, attempt_id, action_id, actor, tool, action, resource, context, event, "
           "decision, executed, outcome, detail, created_at, evidence_mode) VALUES (:id, 'ATTEMPT-PG-TA', :id, "
           "'test', 'filesystem', 'read', '.', '{}', 'policy_decided', 'allow', false, 'decided', 'test', now(), :mode)")

    def schema():
        with postgres_engine.connect() as connection:
            triggers = {r[0] for r in connection.execute(text(
                "SELECT tgname FROM pg_trigger WHERE tgrelid = 'tool_audits'::regclass AND NOT tgisinternal"))}
            indexes = dict(connection.execute(text(
                "SELECT indexname, indexdef FROM pg_indexes WHERE tablename = 'tool_audits'")).all())
        return triggers, indexes

    with postgres_engine.begin() as connection:
        connection.execute(text(sql), {"id": "TA-LIVE", "mode": "live"})
    with pytest.raises(DBAPIError, match="evidence_mode"), postgres_engine.begin() as connection:
        connection.execute(text(sql), {"id": "TA-BAD", "mode": "production"})
    at_head = schema()
    assert "trg_tool_audits_append_only" in at_head[0]
    assert "WHERE" in at_head[1]["uq_tool_audits_execution_event"]

    config = Config(str(Path(__file__).parents[1] / "alembic.ini"))
    monkeypatch.setenv("SDF_DATABASE_URL", POSTGRES_URL)
    command.downgrade(config, "0021_evidence_mode_check")
    try:
        with postgres_engine.begin() as connection:
            connection.execute(text(sql), {"id": "TA-ODD", "mode": "staging"})
        with pytest.raises(RuntimeError, match="1 tool_audits row"):
            command.upgrade(config, "head")
        with postgres_engine.connect() as connection:  # nothing was rewritten
            assert dict(connection.execute(text("SELECT audit_id, evidence_mode FROM tool_audits")).all()) == {
                "TA-LIVE": "live", "TA-ODD": "staging"}
    finally:
        with postgres_engine.begin() as connection:
            connection.execute(text("TRUNCATE TABLE tool_audits"))
    # Operator path for a large table: pre-created NOT VALID is reused and validated.
    with postgres_engine.begin() as connection:
        connection.execute(text("ALTER TABLE tool_audits ADD CONSTRAINT ck_tool_audits_evidence_mode "
                                "CHECK (evidence_mode IN ('synthetic', 'live')) NOT VALID"))
    command.upgrade(config, "head")
    with postgres_engine.connect() as connection:
        assert connection.execute(text("SELECT convalidated FROM pg_constraint "
                                       "WHERE conname = 'ck_tool_audits_evidence_mode'")).scalar() is True
    assert schema() == at_head  # trigger and indexes unchanged
    with pytest.raises(DBAPIError, match="append-only"), postgres_engine.begin() as connection:
        connection.execute(text(sql), {"id": "TA-X", "mode": "live"})
        connection.execute(text("UPDATE tool_audits SET evidence_mode = 'synthetic' WHERE audit_id = 'TA-X'"))
