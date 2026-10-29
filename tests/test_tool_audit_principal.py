"""Regression tests for #51: Tool Proxy actor binding and principal audit."""

from datetime import datetime, timezone
import uuid

from alembic import command
from alembic.config import Config
from sqlalchemy import create_engine, inspect, text

from sdf_core.api import SessionLocal
from sdf_core.db import AttemptRow, RuntimeEventRow, TaskRow, ToolAuditRow
from tests.auth_support import client_as


def _attempt(tmp_path, monkeypatch, *, agent="test"):
    monkeypatch.chdir(tmp_path)
    suffix = uuid.uuid4().hex[:8]
    attempt_id = f"ATTEMPT-ACTOR-{suffix}"
    with SessionLocal() as db:
        db.add(TaskRow(id=f"TASK-ACTOR-{suffix}", title="actor", status="running",
                       idempotency_key=f"task-actor-{suffix}", acceptance_criteria=[],
                       created_at=datetime.now(timezone.utc)))
        db.add(AttemptRow(id=attempt_id, task_id=f"TASK-ACTOR-{suffix}", status="running",
                          dispatch_key=f"dispatch-actor-{suffix}", agent=agent,
                          created_at=datetime.now(timezone.utc)))
        db.commit()
    workspace = tmp_path / ".sdf-workspaces" / attempt_id
    workspace.mkdir(parents=True)
    return attempt_id, workspace


def _audits(attempt_id):
    with SessionLocal() as db:
        return db.query(ToolAuditRow).filter_by(attempt_id=attempt_id).order_by(ToolAuditRow.created_at).all()


def _write(actor=None, *, data="x"):
    body = {"tool": "filesystem", "action": "write", "resource": "out.txt", "context": {"data": data}}
    if actor is not None:
        body["actor"] = actor
    return body


def test_forged_actor_is_denied_and_audited_with_real_principal(tmp_path, monkeypatch):
    attempt_id, workspace = _attempt(tmp_path, monkeypatch)
    monkeypatch.setenv("SDF_TOOL_ALLOWLIST", "filesystem:write")
    operator = client_as("operator", principal="op-alice")

    response = operator.post(f"/attempts/{attempt_id}/actions", json=_write("agent:root"))

    assert response.status_code == 403
    assert response.json()["detail"]["error"] == "actor_mismatch"
    assert not (workspace / "out.txt").exists()
    rows = _audits(attempt_id)
    assert [(r.event, r.decision, r.executed) for r in rows] == [("policy_decided", "deny", False)]
    assert rows[0].actor == "agent:test"            # server-derived, not the forged value
    assert rows[0].principal == "op-alice"          # the authenticated caller
    assert rows[0].context.get("sdf_claimed_actor") == "agent:root"
    with SessionLocal() as db:
        event = db.query(RuntimeEventRow).filter_by(source="tool-proxy", attempt_id=attempt_id).one()
        assert event.payload["principal"] == "op-alice"


def test_forged_denial_does_not_shadow_the_legitimate_action_audit(tmp_path, monkeypatch):
    attempt_id, workspace = _attempt(tmp_path, monkeypatch)
    monkeypatch.setenv("SDF_TOOL_ALLOWLIST", "filesystem:write")
    operator = client_as("operator", principal="op-bob")

    assert operator.post(f"/attempts/{attempt_id}/actions", json=_write("agent:root")).status_code == 403
    ok = operator.post(f"/attempts/{attempt_id}/actions", json=_write())

    assert ok.status_code == 200
    assert ok.json()["status"] == "executed"
    allowed = [r for r in _audits(attempt_id) if r.action_id == ok.json()["action_id"]]
    assert ("policy_decided", "allow") in {(r.event, r.decision) for r in allowed}
    assert {r.principal for r in _audits(attempt_id)} == {"op-bob"}


def test_omitted_or_matching_actor_is_derived_from_attempt_agent(tmp_path, monkeypatch):
    attempt_id, workspace = _attempt(tmp_path, monkeypatch, agent="pi")
    monkeypatch.setenv("SDF_TOOL_ALLOWLIST", "filesystem:write")
    operator = client_as("operator", principal="op-carol")

    omitted = operator.post(f"/attempts/{attempt_id}/actions", json=_write(data="one"))
    matching = operator.post(f"/attempts/{attempt_id}/actions", json=_write("agent:pi", data="two"))

    assert omitted.status_code == 200 and matching.status_code == 200
    assert omitted.json()["actor"] == matching.json()["actor"] == "agent:pi"
    assert omitted.json()["principal"] == "op-carol"
    assert (workspace / "out.txt").read_text() == "two"
    rows = _audits(attempt_id)
    assert rows and all(r.actor == "agent:pi" and r.principal == "op-carol" for r in rows)


def test_policy_denied_action_is_audited_with_principal(tmp_path, monkeypatch):
    attempt_id, workspace = _attempt(tmp_path, monkeypatch)
    monkeypatch.delenv("SDF_TOOL_ALLOWLIST", raising=False)
    operator = client_as("operator", principal="op-dave")

    response = operator.post(f"/attempts/{attempt_id}/actions", json=_write())

    assert response.status_code == 200
    assert response.json()["status"] == "denied"
    rows = _audits(attempt_id)
    assert [(r.decision, r.principal, r.actor) for r in rows] == [("deny", "op-dave", "agent:test")]


def test_non_operator_cannot_reach_tool_proxy(tmp_path, monkeypatch):
    attempt_id, workspace = _attempt(tmp_path, monkeypatch)
    monkeypatch.setenv("SDF_TOOL_ALLOWLIST", "filesystem:write")
    viewer = client_as("viewer", principal="viewer-eve")

    assert viewer.post(f"/attempts/{attempt_id}/actions", json=_write()).status_code == 403
    assert _audits(attempt_id) == []
    assert not (workspace / "out.txt").exists()


def test_migration_0018_adds_and_removes_principal_column(tmp_path, monkeypatch):
    url = f"sqlite:///{tmp_path / 'm.sqlite'}"
    monkeypatch.setenv("SDF_DATABASE_URL", url)
    config = Config("alembic.ini")

    command.upgrade(config, "head")
    engine = create_engine(url)
    assert "principal" in {c["name"] for c in inspect(engine).get_columns("tool_audits")}
    assert "ix_tool_audits_principal" in {i["name"] for i in inspect(engine).get_indexes("tool_audits")}
    engine.dispose()

    command.downgrade(config, "0017_trace_id")
    engine = create_engine(url)
    assert "principal" not in {c["name"] for c in inspect(engine).get_columns("tool_audits")}
    with engine.connect() as conn:
        assert conn.execute(text("SELECT version_num FROM alembic_version")).scalar() == "0017_trace_id"
    engine.dispose()

    command.upgrade(config, "head")
    engine = create_engine(url)
    assert "principal" in {c["name"] for c in inspect(engine).get_columns("tool_audits")}
    engine.dispose()
