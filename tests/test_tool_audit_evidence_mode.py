"""Tool Proxy audit rows carry their Attempt's evidence mode (ADR 0031, #71)."""

from __future__ import annotations

import importlib.util
from datetime import UTC, datetime
from pathlib import Path

import pytest
import sqlalchemy as sa
from alembic.config import Config
from alembic.migration import MigrationContext
from alembic.operations import Operations
from sqlalchemy import create_engine, insert, select, text
from sqlalchemy.exc import IntegrityError
from sqlalchemy.orm import sessionmaker

from alembic import command
from sdf_core.api import SessionLocal
from sdf_core.db import AttemptRow, Base, TaskRow, ToolAuditRow, make_engine
from sdf_core.execution import ExecutionService
from sdf_core.model import utcnow
from sdf_core.policy import ActionRequest, AllowlistPolicy, AuditEvent, AuditRecord
from sdf_core.tools import SqlAlchemyAuditSink
from tests.auth_support import admin_client
from tests.test_evidence_mode import CHECK, _live
from tests.test_run_adapter import _Completing

ALEMBIC = Config(str(Path(__file__).parents[1] / "alembic.ini"))
POLICY = AllowlistPolicy({("filesystem", "read")})


@pytest.fixture()
def db():
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as session:
        session.add(TaskRow(id="T", title="t", status="created", idempotency_key="t",
                            acceptance_criteria=["tests pass"], created_at=utcnow()))
        session.commit()
        yield session


def _attempt(db, tmp_path, attempt_id, mode):
    db.add(AttemptRow(id=attempt_id, task_id="T", status="running", dispatch_key=attempt_id, agent="pi",
                      evidence_mode=mode, created_at=utcnow()))
    db.commit()
    workspace = tmp_path / "work" / attempt_id
    workspace.mkdir(parents=True, exist_ok=True)
    (workspace / "a.txt").write_text("x")
    return ExecutionService(db, workspace_root=tmp_path / "work", artifact_root=tmp_path / "art")


def _tool(service, attempt_id, resource="a.txt", action="read"):
    return service.execute_tool(attempt_id=attempt_id, policy=POLICY, require_containment=False,
                                request=ActionRequest(attempt_id=attempt_id, actor="agent:pi", tool="filesystem",
                                                      action=action, resource=resource))


def _modes(db, attempt_id):
    return {(r.event, r.evidence_mode) for r in db.scalars(select(ToolAuditRow).where(
        ToolAuditRow.attempt_id == attempt_id))}


# --- writer: the Attempt's mode at write time -------------------------------------

@pytest.mark.parametrize("mode", ["synthetic", "live"])
def test_every_audit_event_carries_the_attempt_mode(db, tmp_path, mode):
    service = _attempt(db, tmp_path, f"A-{mode}", mode)
    _tool(service, f"A-{mode}")                    # allowed: decided, claimed, executed
    _tool(service, f"A-{mode}", action="write")    # denied: decided only
    rows = _modes(db, f"A-{mode}")
    assert {event for event, _ in rows} == {"policy_decided", "action_claimed", "action_executed"}
    assert {m for _, m in rows} == {mode}


def test_rows_written_before_the_promotion_stay_synthetic(db, tmp_path):
    service = _attempt(db, tmp_path, "A-PROMO", "synthetic")
    _tool(service, "A-PROMO", resource="a.txt")
    before = {r.audit_id for r in db.scalars(select(ToolAuditRow).where(ToolAuditRow.attempt_id == "A-PROMO"))}
    db.get(AttemptRow, "A-PROMO").evidence_mode = "live"   # the sandbox now exists (ADR 0031 §4)
    db.commit()
    (tmp_path / "work" / "A-PROMO" / "b.txt").write_text("y")
    _tool(service, "A-PROMO", resource="b.txt")
    for row in db.scalars(select(ToolAuditRow).where(ToolAuditRow.attempt_id == "A-PROMO")):
        assert row.evidence_mode == ("synthetic" if row.audit_id in before else "live"), row.event


def test_an_audit_for_an_unknown_attempt_is_synthetic(db):
    request = ActionRequest(attempt_id="NO-SUCH-ATTEMPT", actor="agent:x", tool="filesystem", action="read",
                            resource="a")
    decision = POLICY.decide(request)
    SqlAlchemyAuditSink(db).append(AuditRecord.for_request(
        request, event=AuditEvent.POLICY_DECIDED, decision=decision, executed=False, outcome="decided",
        detail=decision.reason))
    db.flush()
    assert _modes(db, "NO-SUCH-ATTEMPT") == {("policy_decided", "synthetic")}


def test_a_tool_call_during_a_live_run_is_audited_live(db, tmp_path, monkeypatch):
    """The agent's Tool Proxy call happens inside its turn, after the sandbox exists."""

    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n")
    seen = {}

    class CallsTheProxy(_Completing):
        def send(self, session_id, input_text):
            attempt = db.scalar(select(AttemptRow).where(AttemptRow.dispatch_key == "live-tool"))
            service = ExecutionService(db, workspace_root=tmp_path / "work", artifact_root=tmp_path / "art")
            seen["status"] = _tool(service, attempt.id, resource="app.py").status.value
            seen["id"] = attempt.id
            return super().send(session_id, input_text)

    run = ExecutionService(db, workspace_root=tmp_path / "work", artifact_root=tmp_path / "art",
                           adapter=_live(db, monkeypatch, runtime=CallsTheProxy()))
    attempt = run.run(task_id="T", dispatch_key="live-tool", fixture=fixture, instructions="x", commands=[],
                      criterion_checks=CHECK)
    assert attempt.evidence_mode == "live" and seen["status"] == "executed"
    assert {m for _, m in _modes(db, seen["id"])} == {"live"}


# --- readers: API response and timeline ------------------------------------------

def test_actions_response_and_timeline_carry_the_audit_mode(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    monkeypatch.setenv("SDF_TOOL_ALLOWLIST", "filesystem:read")
    attempt_id = "ATTEMPT-API-TOOL-MODE"
    with SessionLocal() as api_db:
        api_db.add(TaskRow(id="TASK-API-TOOL-MODE", title="t", status="running", idempotency_key="api-tool-mode",
                           acceptance_criteria=[], created_at=datetime.now(UTC)))
        api_db.add(AttemptRow(id=attempt_id, task_id="TASK-API-TOOL-MODE", status="running",
                              dispatch_key="api-tool-mode", agent="test", evidence_mode="live",
                              created_at=datetime.now(UTC)))
        api_db.commit()
    (tmp_path / ".sdf-workspaces" / attempt_id).mkdir(parents=True)
    (tmp_path / ".sdf-workspaces" / attempt_id / "a.txt").write_text("x")
    client = admin_client()
    body = client.post(f"/attempts/{attempt_id}/actions", json={
        "actor": "agent:test", "tool": "filesystem", "action": "read", "resource": "a.txt"}).json()
    assert body["evidence_mode"] == "live", body
    entries = client.get(f"/runs/{attempt_id}/timeline").json()["entries"]
    audits = [e for e in entries if e.get("kind") in {"policy_decided", "action_claimed", "action_executed"}]
    assert audits and {e["evidence_mode"] for e in audits} == {"live"}


# --- constraint and migration 0022 ----------------------------------------------

def _audit_values(audit_id="AU", event="policy_decided", mode="synthetic", action_id="ACT"):
    return {"audit_id": audit_id, "attempt_id": "A", "action_id": action_id, "actor": "a", "tool": "filesystem",
            "action": "read", "resource": "r", "context": {}, "event": event, "decision": "allow",
            "executed": False, "outcome": "o", "detail": "d", "created_at": utcnow(), "evidence_mode": mode}


def test_create_all_schema_rejects_an_unknown_mode(db):
    with pytest.raises(IntegrityError):
        db.execute(insert(ToolAuditRow).values(**_audit_values(mode="production")))
    db.rollback()


AUDIT_SQL = ("INSERT INTO tool_audits (audit_id, attempt_id, action_id, actor, tool, action, resource, context, "
             "event, decision, executed, outcome, detail, created_at, evidence_mode) VALUES "
             "(:id, 'A', :action, 'a', 'filesystem', 'read', 'r', '{}', :event, 'allow', 0, 'o', 'd', '2026-10-30', "
             ":mode)")
DEFAULT_SQL = ("INSERT INTO tool_audits (audit_id, attempt_id, action_id, actor, tool, action, resource, context, "
               "event, decision, executed, outcome, detail, created_at) VALUES "
               "(:id, 'A', 'ACT-D', 'a', 'filesystem', 'read', 'r', '{}', 'policy_decided', 'allow', 0, 'o', 'd', "
               "'2026-10-30')")


def _schema(engine):
    with engine.connect() as c:
        objects = dict(c.execute(text("SELECT name, sql FROM sqlite_master WHERE tbl_name = 'tool_audits' "
                                      "AND type IN ('index', 'trigger') AND sql IS NOT NULL")).all())
        columns = c.execute(text("PRAGMA table_info(tool_audits)")).all()
    return objects, columns


def test_migration_0022_keeps_indexes_defaults_and_rows_on_sqlite(tmp_path, monkeypatch):
    url = f"sqlite:///{tmp_path / 'm.sqlite'}"
    monkeypatch.setenv("SDF_DATABASE_URL", url)
    command.upgrade(ALEMBIC, "0021_evidence_mode_check")
    engine = create_engine(url)
    with engine.begin() as c:
        c.execute(text(AUDIT_SQL), {"id": "OLD", "action": "ACT-OLD", "event": "action_executed", "mode": "synthetic"})
    before = _schema(engine)
    assert "WHERE event <> 'policy_decided'" in before[0]["uq_tool_audits_execution_event"]

    command.upgrade(ALEMBIC, "head")
    assert _schema(engine) == before  # same indexes (incl. the partial WHERE), same column defaults
    with engine.begin() as c:
        assert c.execute(text("SELECT evidence_mode FROM tool_audits WHERE audit_id = 'OLD'")).scalar() == "synthetic"
        c.execute(text(DEFAULT_SQL), {"id": "DEFAULTED"})
        assert c.execute(text("SELECT evidence_mode FROM tool_audits WHERE audit_id = 'DEFAULTED'")).scalar() \
            == "synthetic"
        c.execute(text(AUDIT_SQL), {"id": "L1", "action": "ACT-L", "event": "policy_decided", "mode": "live"})
        c.execute(text(AUDIT_SQL), {"id": "L2", "action": "ACT-L", "event": "policy_decided", "mode": "live"})
    with pytest.raises(IntegrityError), engine.begin() as c:  # the idempotency fence still holds
        c.execute(text(AUDIT_SQL), {"id": "DUP", "action": "ACT-OLD", "event": "action_executed", "mode": "live"})
    with pytest.raises(IntegrityError), engine.begin() as c:  # the new CHECK
        c.execute(text(AUDIT_SQL), {"id": "BAD", "action": "ACT-B", "event": "policy_decided", "mode": "production"})

    command.downgrade(ALEMBIC, "0021_evidence_mode_check")
    assert _schema(engine) == before
    with engine.begin() as c:
        assert c.execute(text("SELECT COUNT(*) FROM tool_audits")).scalar() == 4
    command.upgrade(ALEMBIC, "head")
    engine.dispose()


def test_migration_0022_refuses_unknown_modes_and_keeps_the_rows(tmp_path, monkeypatch):
    url = f"sqlite:///{tmp_path / 'legacy.sqlite'}"
    monkeypatch.setenv("SDF_DATABASE_URL", url)
    command.upgrade(ALEMBIC, "0021_evidence_mode_check")
    engine = create_engine(url)
    with engine.begin() as c:
        c.execute(text(AUDIT_SQL), {"id": "OLD", "action": "A1", "event": "policy_decided", "mode": "synthetic"})
        c.execute(text(AUDIT_SQL), {"id": "ODD", "action": "A2", "event": "policy_decided", "mode": "staging"})
    with pytest.raises(RuntimeError, match="1 tool_audits row"):
        command.upgrade(ALEMBIC, "head")
    with engine.begin() as c:
        assert dict(c.execute(text("SELECT audit_id, evidence_mode FROM tool_audits")).all()) == {
            "OLD": "synthetic", "ODD": "staging"}
        assert c.execute(text("SELECT version_num FROM alembic_version")).scalar() == "0021_evidence_mode_check"
    engine.dispose()


def test_migration_0022_restores_an_index_a_rebuild_lost(tmp_path, monkeypatch):
    """The safety net: whatever the batch rebuild drops or changes is put back verbatim."""

    url = f"sqlite:///{tmp_path / 'r.sqlite'}"
    monkeypatch.setenv("SDF_DATABASE_URL", url)
    command.upgrade(ALEMBIC, "0021_evidence_mode_check")
    path = Path(__file__).parents[1] / "alembic" / "versions" / "0022_tool_audit_mode_check.py"
    spec = importlib.util.spec_from_file_location("m0022", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    engine = create_engine(url)
    before = _schema(engine)
    with engine.begin() as c:
        ops = Operations(MigrationContext.configure(c))
        monkeypatch.setattr(module, "op", ops)

        def lossy(batch):
            batch.create_check_constraint("ck_tmp", sa.text(module._RULE))
            batch.drop_index("uq_tool_audits_execution_event")

        module._sqlite_rebuild(c, lossy)
    assert _schema(engine) == before
    engine.dispose()
