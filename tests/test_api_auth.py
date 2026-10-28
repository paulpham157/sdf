"""ADR 0015: authentication, roles, separation of duties and audit for the HTTP API."""

from __future__ import annotations

import re
import uuid
from datetime import timedelta

import pytest
from fastapi.routing import APIRoute
from fastapi.testclient import TestClient
from sqlalchemy import select

from sdf_core import auth
from sdf_core.api import SessionLocal, app, auth_failures
from sdf_core.db import ApiTokenRow, AuthAuditRow, Base, DecisionRequestRow, GraphNodeRow, make_engine
from sdf_core.model import utcnow
from tests.agent_comm_support import make_fixture, plan_dict
from tests.auth_support import bearer, client_as, issue_token

anon = TestClient(app)

ROUTES = sorted({(method, route.path) for route in app.routes if isinstance(route, APIRoute)
                 for method in route.methods if method != "HEAD"})
PROTECTED = [r for r in ROUTES if r not in auth.PUBLIC_ROUTES]


def _concrete(path: str) -> str:
    return re.sub(r"\{[^}]+\}", "x", path)


def _call(client: TestClient, method: str, path: str, **kwargs):
    if method in {"POST", "PUT", "PATCH"}:
        kwargs.setdefault("json", {})
    return client.request(method, _concrete(path), **kwargs)


def test_route_table_is_classified():
    assert len(PROTECTED) >= 30
    for method, path in PROTECTED:
        if method not in auth.SAFE_METHODS:
            # Every mutating route has an explicit, deliberate role; a new one
            # would otherwise silently need admin (fail closed) - make it explicit.
            assert (method, path) in auth.ROUTE_ROLES, (method, path)
    assert auth.required_role("DELETE", "/something-new") == "admin"
    assert auth.required_role("GET", "/something-new") == "viewer"


@pytest.mark.parametrize("method,path", PROTECTED)
def test_every_route_requires_a_token(method, path):
    resp = _call(anon, method, path)
    assert resp.status_code == 401, (method, path, resp.status_code)
    assert resp.headers.get("www-authenticate") == "Bearer"
    garbage = _call(anon, method, path, headers={"Authorization": "Bearer sdf_0123456789abcdef." + "A" * 43})
    assert garbage.status_code == 401
    auth_failures.reset()


@pytest.mark.parametrize("method,path", [r for r in PROTECTED if auth.required_role(*r) != "viewer"])
def test_every_mutating_route_forbids_a_viewer(method, path):
    viewer = client_as("viewer")
    resp = _call(viewer, method, path)
    assert resp.status_code == 403, (method, path, resp.status_code, resp.text)


def test_health_is_public_and_openapi_is_not():
    assert anon.get("/healthz").json() == {"status": "ok"}
    assert anon.get("/openapi.json").status_code == 401
    assert anon.get("/docs").status_code == 404
    spec = client_as("viewer").get("/openapi.json")
    assert spec.status_code == 200 and "/plans" in spec.json()["paths"]


def test_whoami_reports_the_token_principal():
    me = client_as("operator", principal="alice").get("/auth/whoami").json()
    assert me["name"] == "alice" and me["roles"] == ["operator"] and me["can_approve"] is False
    assert me["method"] == "api_token" and me["token_id"]


def test_roles():
    tid = f"TASK-AUTH-{uuid.uuid4().hex[:8]}"
    task = {"id": tid, "title": "t", "idempotency_key": tid}
    assert client_as("viewer").post("/tasks", json=task).status_code == 403
    assert client_as("approver").post("/tasks", json=task).status_code == 403
    assert client_as("operator").post("/tasks", json=task).status_code == 201
    assert client_as("viewer").get(f"/tasks/{tid}/trace").status_code == 200
    assert client_as("admin").get(f"/tasks/{tid}/trace").status_code == 200
    node = {"id": f"DEC-{uuid.uuid4().hex[:8]}", "title": "d", "owner": "hung"}
    assert client_as("operator").post("/decisions", json=node).status_code == 403


def _propose(client: TestClient, tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    plan_id = f"PLAN-AUTH-{uuid.uuid4().hex[:8]}"
    body = plan_dict(make_fixture(tmp_path), plan_id)
    body["proposed_by"] = "someone-else"  # ignored: the proposer is the token's principal
    resp = client.post("/plans", json=body)
    assert resp.status_code == 201, resp.text
    return plan_id, resp.json()


def _decision_node(plan_json) -> GraphNodeRow:
    with SessionLocal() as db:
        request = db.get(DecisionRequestRow, plan_json["decision_request"]["id"])
        return db.get(GraphNodeRow, request.decision_node_id)


def test_separation_of_duties_and_spoofed_approver(tmp_path, monkeypatch):
    solo = client_as("operator", "approver", principal="solo")
    plan_id, body = _propose(solo, tmp_path, monkeypatch)
    assert body["proposed_by"] == "solo"
    denied = solo.post(f"/plans/{plan_id}/approve", json={"expected_sha256": body["content_sha256"]})
    assert denied.status_code == 403 and "separation of duties" in denied.json()["detail"]

    other = client_as("approver", principal="bob")
    ok = other.post(f"/plans/{plan_id}/approve", json={
        "decided_by": "solo", "approver": "solo", "expected_sha256": body["content_sha256"]})
    assert ok.status_code == 200, ok.text
    assert ok.json()["decision_request"]["resolved_by"] == "bob"
    node = _decision_node(ok.json())
    assert node.owner == "bob"
    meta = node.metadata_json
    assert meta["decided_by"] == "bob" and meta["proposed_by"] == "solo" and meta["self_approval"] is False
    assert meta["principal"]["name"] == "bob" and meta["principal"]["method"] == "api_token"
    assert meta["principal"]["token_id"]


def test_proposer_may_reject_own_plan(tmp_path, monkeypatch):
    solo = client_as("operator", "approver", principal="solo2")
    plan_id, _ = _propose(solo, tmp_path, monkeypatch)
    assert solo.post(f"/plans/{plan_id}/reject", json={"rationale": "changed my mind"}).status_code == 200


def test_single_user_mode_allows_self_approval_and_records_it(tmp_path, monkeypatch):
    monkeypatch.setenv("SDF_AUTH_SINGLE_USER", "1")
    solo = client_as("operator", "approver", principal="hung-solo")
    plan_id, body = _propose(solo, tmp_path, monkeypatch)
    ok = solo.post(f"/plans/{plan_id}/approve", json={"expected_sha256": body["content_sha256"]})
    assert ok.status_code == 200, ok.text
    assert _decision_node(ok.json()).metadata_json["self_approval"] is True
    assert solo.get("/auth/whoami").json()["single_user_mode"] is True


def test_agent_and_service_identities_cannot_approve():
    with SessionLocal() as db:
        for name in ("worker-1", "lead", "agent:lead", "sandbox-abc", "e2b_box", "host", "sdf", "SYSTEM"):
            with pytest.raises(ValueError):
                auth.create_token(db, principal=name, roles=["viewer"], created_by="test")
        for role in ("approver", "admin"):
            with pytest.raises(ValueError):
                auth.create_token(db, principal="ci-bot", roles=[role], created_by="test", kind="service")
        with pytest.raises(ValueError):
            auth.create_token(db, principal="x", roles=[], created_by="test")
        with pytest.raises(ValueError):
            auth.create_token(db, principal="x", roles=["root"], created_by="test")
    # Defence in depth: a principal that somehow holds approver still cannot decide.
    assert not auth.Principal("ci", frozenset({"approver"}), "service", "t").can_approve
    assert not auth.Principal("worker-2", frozenset({"admin"}), "human", "t").can_approve
    assert auth.Principal("hung", frozenset({"admin"}), "human", "t").can_approve
    service = client_as("operator", principal="ci-bot", kind="service")
    assert service.post("/decision-requests/x/resolve", json={"outcome": "approved"}).status_code == 403


def test_revoked_and_expired_tokens_are_refused():
    token_id, plaintext = issue_token(["viewer"], "carol")
    client = TestClient(app, headers=bearer(plaintext))
    assert client.get("/auth/whoami").status_code == 200
    with SessionLocal() as db:
        auth.revoke_token(db, token_id, revoked_by="test")
        db.commit()
        with pytest.raises(ValueError):  # revocation is final
            db.get(ApiTokenRow, token_id).revoked_by = "someone"
            db.flush()
        db.rollback()
    assert client.get("/auth/whoami").status_code == 401

    with SessionLocal() as db:
        row, old = auth.create_token(db, principal="dave", roles=["viewer"], created_by="test",
                                     expires_in_days=1, now=utcnow() - timedelta(days=2))
        db.commit()
    assert TestClient(app, headers=bearer(old)).get("/auth/whoami").status_code == 401
    with pytest.raises(ValueError):
        with SessionLocal() as db:
            auth.create_token(db, principal="eve", roles=["viewer"], created_by="t", expires_in_days=10_000)


def test_tampered_secret_and_wrong_pepper_are_refused():
    token_id, plaintext = issue_token(["viewer"], "frank")
    tampered = plaintext[:-1] + ("A" if plaintext[-1] != "A" else "B")
    assert TestClient(app, headers=bearer(tampered)).get("/auth/whoami").status_code == 401
    with SessionLocal() as db:
        with pytest.raises(auth.AuthError):
            auth.verify_token(db, plaintext, key="another-pepper-" + "z" * 40)
        assert auth.verify_token(db, plaintext, key=auth.pepper()).name == "frank"


def test_token_is_stored_hashed_and_last_used_is_tracked():
    token_id, plaintext = issue_token(["viewer"], "grace")
    with SessionLocal() as db:
        row = db.get(ApiTokenRow, token_id)
        assert row.last_used_at is None
        assert plaintext.split(".", 1)[1] not in row.secret_hash and len(row.secret_hash) == 64
        listed = [t for t in auth.list_tokens(db) if t["id"] == token_id][0]
        assert "secret_hash" not in listed and plaintext not in str(listed)
    TestClient(app, headers=bearer(plaintext)).get("/auth/whoami")
    with SessionLocal() as db:
        assert db.get(ApiTokenRow, token_id).last_used_at is not None


def test_repeated_failures_lock_out_the_client():
    _, good = issue_token(["viewer"], "heidi")
    for _ in range(auth_failures.max_failures):
        assert anon.get("/auth/whoami", headers=bearer("nope")).status_code == 401
    locked = TestClient(app, headers=bearer(good)).get("/auth/whoami")
    assert locked.status_code == 429 and int(locked.headers["retry-after"]) > 0
    assert anon.get("/healthz").status_code == 200
    auth_failures.reset()
    assert TestClient(app, headers=bearer(good)).get("/auth/whoami").status_code == 200


def test_unconfigured_auth_is_503_and_refuses_to_start(monkeypatch):
    monkeypatch.setenv("SDF_AUTH_PEPPER", "")
    assert anon.get("/auth/whoami").status_code == 503
    monkeypatch.setenv("SDF_AUTH_PEPPER", "too-short")
    assert auth.pepper() is None
    with pytest.raises(RuntimeError, match="refuses to start"):
        with TestClient(app):
            pass


def test_dev_insecure_mode_is_loopback_only(monkeypatch):
    monkeypatch.setenv("SDF_AUTH_PEPPER", "")
    monkeypatch.setenv("SDF_AUTH_DEV_INSECURE", "1")
    with TestClient(app, base_url="http://127.0.0.1", client=("127.0.0.1", 50000)) as local:
        me = local.get("/auth/whoami")
        assert me.status_code == 200 and me.json()["name"] == "local-dev"
        assert local.get("/auth/whoami", headers={"X-Forwarded-For": "203.0.113.9"}).status_code == 403
    remote_client = TestClient(app, base_url="http://127.0.0.1", client=("203.0.113.9", 50000))
    assert remote_client.get("/auth/whoami").status_code == 403
    exposed = TestClient(app, base_url="http://0.0.0.0", client=("127.0.0.1", 50000))
    assert exposed.get("/auth/whoami").status_code == 403
    assert anon.get("/auth/whoami").status_code == 403  # client host "testclient"


def test_audit_records_principal_for_mutations_and_failures():
    tid = f"TASK-AUD-{uuid.uuid4().hex[:8]}"
    client_as("operator", principal="ivan").post("/tasks", json={"id": tid, "title": "t", "idempotency_key": tid})
    client_as("viewer", principal="judy").post("/tasks", json={"id": tid + "x", "title": "t"})
    assert anon.get("/auth/whoami").status_code == 401
    with SessionLocal() as db:
        rows = db.scalars(select(AuthAuditRow).order_by(AuthAuditRow.id.desc()).limit(50)).all()
        mutation = next(r for r in rows if r.principal == "ivan" and r.event == "mutation")
        assert mutation.route == "/tasks" and mutation.status_code == 201 and mutation.token_id
        denied = next(r for r in rows if r.principal == "judy")
        assert denied.event == "denied" and denied.status_code == 403
        assert any(r.event == "auth_failed" and r.principal is None for r in rows)
        assert any(r.event == "token_created" for r in db.scalars(select(AuthAuditRow)).all())
        mutation.status_code = 200
        with pytest.raises(ValueError):
            db.flush()
        db.rollback()
        with pytest.raises(ValueError):
            db.delete(db.get(AuthAuditRow, mutation.id))
            db.flush()
        db.rollback()
        token = db.scalars(select(ApiTokenRow)).first()
        with pytest.raises(ValueError):
            db.delete(token)
            db.flush()
        db.rollback()
        token = db.scalars(select(ApiTokenRow)).first()
        token.roles = ["admin"]
        with pytest.raises(ValueError):
            db.flush()
        db.rollback()


def test_cli_create_list_revoke(tmp_path, monkeypatch, capsys):
    from sdf_core import auth_cli

    url = f"sqlite+pysqlite:///{tmp_path / 'cli.db'}"
    Base.metadata.create_all(make_engine(url))
    monkeypatch.setenv("SDF_DATABASE_URL", url)
    assert auth_cli.main(["create", "--principal", "hung", "--role", "approver", "--role", "operator"]) == 0
    out, err = capsys.readouterr()
    plaintext = out.strip()
    assert re.fullmatch(r"sdf_[0-9a-f]{16}\.[A-Za-z0-9_-]{43}", plaintext)
    assert plaintext not in err and "shown only once" in err
    token_id = plaintext[4:20]
    assert auth_cli.main(["list"]) == 0
    listed, _ = capsys.readouterr()
    assert token_id in listed and plaintext.split(".")[1] not in listed
    assert auth_cli.main(["revoke", token_id]) == 0
    assert auth_cli.main(["revoke", "0" * 16]) == 1
    assert auth_cli.main(["create", "--principal", "worker-1", "--role", "viewer"]) == 2
    monkeypatch.setenv("SDF_DATABASE_URL", "")
    assert auth_cli.main(["list"]) == 2
