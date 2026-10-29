"""Regression tests for #51: Tool Proxy actor binding and principal audit."""

from datetime import datetime, timezone
import uuid

from alembic import command
from alembic.config import Config
from sqlalchemy import create_engine, inspect, text

from sdf_core.api import SessionLocal
from sdf_core.db import AttemptRow, RuntimeEventRow, TaskRow, ToolAuditRow
from tests.auth_support import admin_client, client_as


def _attempt(tmp_path, monkeypatch, *, agent="test", owner="op-alice", status="running"):
    monkeypatch.chdir(tmp_path)
    suffix = uuid.uuid4().hex[:8]
    attempt_id = f"ATTEMPT-ACTOR-{suffix}"
    with SessionLocal() as db:
        db.add(TaskRow(id=f"TASK-ACTOR-{suffix}", title="actor", status="running",
                       idempotency_key=f"task-actor-{suffix}", acceptance_criteria=[],
                       created_at=datetime.now(timezone.utc)))
        db.add(AttemptRow(id=attempt_id, task_id=f"TASK-ACTOR-{suffix}", status=status,
                          dispatch_key=f"dispatch-actor-{suffix}", agent=agent, principal=owner,
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
    assert "agent:root" in rows[0].detail           # claim recorded outside the hash
    assert not any(k.startswith("sdf_") for k in rows[0].context)
    with SessionLocal() as db:
        event = db.query(RuntimeEventRow).filter_by(source="tool-proxy", attempt_id=attempt_id).one()
        assert event.payload["principal"] == "op-alice"


def test_forged_denial_does_not_shadow_the_legitimate_action_audit(tmp_path, monkeypatch):
    attempt_id, workspace = _attempt(tmp_path, monkeypatch, owner="op-bob")
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
    attempt_id, workspace = _attempt(tmp_path, monkeypatch, agent="pi", owner="op-carol")
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
    attempt_id, workspace = _attempt(tmp_path, monkeypatch, owner="op-dave")
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


# -- review round: S1 reserved keys / identity, S2 per-delivery decisions,
# -- S3 attempt ownership, terminal-attempt ordering.

def test_reserved_sdf_context_keys_are_rejected(tmp_path, monkeypatch):
    attempt_id, workspace = _attempt(tmp_path, monkeypatch)
    monkeypatch.setenv("SDF_TOOL_ALLOWLIST", "filesystem:write")
    operator = client_as("operator", principal="op-alice")
    for key in ("sdf_claimed_actor", "SDF_anything"):
        body = _write()
        body["context"][key] = "agent:root"
        assert operator.post(f"/attempts/{attempt_id}/actions", json=body).status_code == 422
    assert _audits(attempt_id) == []
    assert not (workspace / "out.txt").exists()


def test_forged_actor_keeps_action_identity_and_every_denial_is_recorded(tmp_path, monkeypatch):
    attempt_id, workspace = _attempt(tmp_path, monkeypatch)
    monkeypatch.setenv("SDF_TOOL_ALLOWLIST", "filesystem:write")
    operator = client_as("operator", principal="op-alice")

    first = operator.post(f"/attempts/{attempt_id}/actions", json=_write("agent:root"))
    second = operator.post(f"/attempts/{attempt_id}/actions", json=_write("agent:root"))
    ok = operator.post(f"/attempts/{attempt_id}/actions", json=_write())

    assert first.status_code == second.status_code == 403
    # The claimed actor is not hashed: the denial names the real action.
    assert first.json()["detail"]["action_id"] == ok.json()["action_id"]
    decisions = [(r.decision, r.principal) for r in _audits(attempt_id) if r.event == "policy_decided"]
    assert decisions.count(("deny", "op-alice")) == 2   # one row per denial
    assert ("allow", "op-alice") in decisions
    assert ok.json()["status"] == "executed"


def test_second_principal_replaying_an_action_is_audited_with_its_principal(tmp_path, monkeypatch):
    attempt_id, workspace = _attempt(tmp_path, monkeypatch, owner="op-alice")
    monkeypatch.setenv("SDF_TOOL_ALLOWLIST", "filesystem:write")
    alice = client_as("operator", principal="op-alice")
    admin = client_as("admin", principal="admin-bob")

    a = alice.post(f"/attempts/{attempt_id}/actions", json=_write())
    b = admin.post(f"/attempts/{attempt_id}/actions", json=_write())
    b_forged = admin.post(f"/attempts/{attempt_id}/actions", json=_write("agent:root"))
    a_forged = alice.post(f"/attempts/{attempt_id}/actions", json=_write("agent:root"))

    assert a.json()["action_id"] == b.json()["action_id"]
    assert b.status_code == 200 and b.json()["status"] == "executed"  # idempotent replay
    assert b_forged.status_code == a_forged.status_code == 403
    rows = _audits(attempt_id)
    decisions = [(r.decision, r.principal) for r in rows if r.event == "policy_decided"]
    assert sorted(decisions) == sorted([("allow", "op-alice"), ("allow", "admin-bob"),
                                        ("deny", "admin-bob"), ("deny", "op-alice")])
    # The execution fence still holds: one claim and one execution.
    assert [r.event for r in rows].count("action_executed") == 1
    assert [r.event for r in rows].count("action_claimed") == 1
    with SessionLocal() as db:
        events = db.query(RuntimeEventRow).filter_by(source="tool-proxy", attempt_id=attempt_id).all()
        assert sorted(e.payload.get("principal") for e in events if e.kind == "tool_policy_decided") == \
            sorted(["op-alice", "admin-bob", "admin-bob", "op-alice"])


def test_operator_cannot_drive_another_principals_attempt(tmp_path, monkeypatch):
    attempt_id, workspace = _attempt(tmp_path, monkeypatch, owner="op-alice")
    monkeypatch.setenv("SDF_TOOL_ALLOWLIST", "filesystem:write")
    mallory = client_as("operator", principal="op-mallory")

    response = mallory.post(f"/attempts/{attempt_id}/actions", json=_write())

    assert response.status_code == 403
    assert response.json()["detail"]["error"] == "attempt_not_owned"
    assert not (workspace / "out.txt").exists()
    rows = _audits(attempt_id)
    assert [(r.event, r.decision, r.principal) for r in rows] == [("policy_decided", "deny", "op-mallory")]


def test_unowned_attempt_is_admin_only(tmp_path, monkeypatch):
    attempt_id, workspace = _attempt(tmp_path, monkeypatch, owner=None)
    monkeypatch.setenv("SDF_TOOL_ALLOWLIST", "filesystem:write")

    operator = client_as("operator", principal="op-alice")
    assert operator.post(f"/attempts/{attempt_id}/actions", json=_write()).json()["detail"]["error"] \
        == "attempt_not_owned"
    admin = client_as("admin", principal="admin-root")
    ok = admin.post(f"/attempts/{attempt_id}/actions", json=_write())
    assert ok.status_code == 200 and ok.json()["status"] == "executed"


def test_http_run_records_the_starting_principal_as_owner(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    suffix = uuid.uuid4().hex[:8]
    operator = client_as("operator", principal="op-runner")
    created = admin_client().post("/tasks", json={"id": f"TASK-OWN-{suffix}", "title": "own",
                                                  "idempotency_key": f"own-{suffix}", "acceptance_criteria": []})
    assert created.status_code == 201
    run = operator.post(f"/tasks/TASK-OWN-{suffix}/run", json={"fixture_dir": str(fixture), "instructions": "x"})
    assert run.status_code == 200, run.text
    with SessionLocal() as db:
        assert db.get(AttemptRow, run.json()["attempt_id"]).principal == "op-runner"


def test_terminal_attempt_with_mismatched_actor_is_403(tmp_path, monkeypatch):
    attempt_id, workspace = _attempt(tmp_path, monkeypatch, status="accepted")
    monkeypatch.setenv("SDF_TOOL_ALLOWLIST", "filesystem:write")
    operator = client_as("operator", principal="op-alice")

    forged = operator.post(f"/attempts/{attempt_id}/actions", json=_write("agent:root"))
    honest = operator.post(f"/attempts/{attempt_id}/actions", json=_write())

    assert forged.status_code == 403 and forged.json()["detail"]["error"] == "actor_mismatch"
    assert honest.status_code == 200 and honest.json()["status"] == "denied"  # attempt guard


def test_migration_0018_adds_and_removes_principal_column(tmp_path, monkeypatch):
    url = f"sqlite:///{tmp_path / 'm.sqlite'}"
    monkeypatch.setenv("SDF_DATABASE_URL", url)
    config = Config("alembic.ini")

    command.upgrade(config, "head")
    engine = create_engine(url)
    assert "principal" in {c["name"] for c in inspect(engine).get_columns("tool_audits")}
    indexes = {i["name"] for i in inspect(engine).get_indexes("tool_audits")}
    assert {"ix_tool_audits_principal", "uq_tool_audits_execution_event"} <= indexes
    assert "uq_tool_audits_attempt_action_event" not in indexes
    assert "principal" in {c["name"] for c in inspect(engine).get_columns("attempts")}
    with engine.begin() as conn:
        # Two decision rows for the same action are allowed; a second claim is not.
        for audit_id, event in (("a1", "policy_decided"), ("a2", "policy_decided"), ("a3", "action_claimed")):
            conn.execute(text(
                "INSERT INTO tool_audits (audit_id, attempt_id, action_id, actor, tool, action, resource, "
                "context, event, decision, executed, outcome, detail, created_at, evidence_mode) "
                "VALUES (:id, 'AT', 'AC', 'agent:x', 't', 'a', 'r', '{}', :ev, 'allow', 0, 'o', 'd', "
                "'2026-01-01', 'synthetic')"), {"id": audit_id, "ev": event})
        try:
            conn.execute(text(
                "INSERT INTO tool_audits (audit_id, attempt_id, action_id, actor, tool, action, resource, "
                "context, event, decision, executed, outcome, detail, created_at, evidence_mode) "
                "VALUES ('a4', 'AT', 'AC', 'agent:x', 't', 'a', 'r', '{}', 'action_claimed', 'allow', "
                "0, 'o', 'd', '2026-01-01', 'synthetic')"))
            raise AssertionError("duplicate claim accepted")
        except Exception as exc:  # noqa: BLE001 - IntegrityError
            assert "UNIQUE" in str(exc).upper()
    engine.dispose()

    command.downgrade(config, "0017_trace_id")
    engine = create_engine(url)
    assert "principal" not in {c["name"] for c in inspect(engine).get_columns("tool_audits")}
    assert "principal" not in {c["name"] for c in inspect(engine).get_columns("attempts")}
    with engine.connect() as conn:
        assert conn.execute(text("SELECT version_num FROM alembic_version")).scalar() == "0017_trace_id"
        # The per-delivery duplicate decision was folded back to one row.
        assert conn.execute(text("SELECT COUNT(*) FROM tool_audits WHERE event='policy_decided'")).scalar() == 1
    engine.dispose()

    command.upgrade(config, "head")
    engine = create_engine(url)
    assert "principal" in {c["name"] for c in inspect(engine).get_columns("tool_audits")}
    engine.dispose()
