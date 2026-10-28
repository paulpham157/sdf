"""ADR 0014 HTTP flow: propose -> start refused (409) -> approve -> run -> Evidence."""

from __future__ import annotations

import uuid

import pytest
from sdf_core.api import app
from sdf_core.plans import task_id_for
from tests.agent_comm_support import fake_factory, lead_behavior, make_fixture, plan_dict, worker_behavior
from tests.auth_support import client_as

# ADR 0015: the operator proposes and runs; a different human approves.
client = client_as("operator", principal="planner")
approver = client_as("approver", principal="hung")


@pytest.fixture()
def fake_agents(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    running: set[str] = set()
    created: list = []
    app.state.agent_sandbox_factory = fake_factory(tmp_path / "boxes", running, {
        "lead": lead_behavior, "worker": worker_behavior(), "worker-2": worker_behavior(ask_first=True)}, created)
    app.state.agent_cleanup_check = lambda ids: ids & running
    try:
        yield running, created
    finally:
        app.state.agent_sandbox_factory = None
        app.state.agent_cleanup_check = None


FAST = {"poll_interval_s": 0.01, "stall_s": 0.5, "max_wall_s": 60, "evidence_mode": "synthetic"}


def test_plan_gate_and_lead_worker_run_over_http(tmp_path, fake_agents):
    running, created = fake_agents
    plan_id = f"PLAN-API-{uuid.uuid4().hex[:8]}"
    proposed = client.post("/plans", json=plan_dict(make_fixture(tmp_path), plan_id))
    assert proposed.status_code == 201, proposed.text
    body = proposed.json()
    assert body["status"] == "proposed" and body["decision_request"]["status"] == "pending"

    refused = client.post(f"/plans/{plan_id}/runs", json={"config": FAST, "wait": True})
    assert refused.status_code == 409 and "approve" in refused.json()["detail"]
    task_run = client.post(f"/tasks/{task_id_for(plan_id, 'add')}/run",
                           json={"fixture_dir": str(tmp_path / "toy"), "instructions": "bypass"})
    assert task_run.status_code == 409
    assert body["proposed_by"] == "planner"
    # An operator cannot approve, whatever name it claims.
    assert client.post(f"/plans/{plan_id}/approve", json={"decided_by": "hung"}).status_code == 403
    assert approver.post(f"/plans/{plan_id}/approve", json={"expected_sha256": "f" * 64}).status_code == 409
    approved = approver.post(f"/plans/{plan_id}/approve", json={
        "decided_by": "lead", "rationale": "small, testable", "expected_sha256": body["content_sha256"]})
    assert approved.status_code == 200 and approved.json()["status"] == "approved"
    assert approved.json()["decision_request"]["resolved_by"] == "hung"  # spoofed decided_by ignored
    assert approver.post(f"/plans/{plan_id}/reject", json={}).status_code == 409

    started = client.post(f"/plans/{plan_id}/runs", json={"config": FAST, "wait": True})
    assert started.status_code == 202, started.text
    run = started.json()
    # worker-2 asked a question nobody answered: the run still needs only Evidence, the request stays pending.
    assert run["status"] == "succeeded", run["error"]
    assert {k: v["verdict"] for k, v in run["summary"]["subtasks"].items()} == {"add": "PASS", "mul": "PASS"}
    assert {m["role"] for m in run["members"]} == {"lead", "worker"} and len(run["members"]) == 3
    assert running == set() and all(s.closed for s in created)

    messages = client.get(f"/runs/{run['id']}/messages").json()
    reports = [m for m in messages if m["kind"] == "report"]
    assert reports and all(m["claim_id"] and m["evidence"] is False for m in reports)
    pending = client.get("/decision-requests", params={"run_id": run["id"], "status_filter": "pending"}).json()
    assert len(pending) == 1 and pending[0]["requested_by"] == "worker-2"
    rid = pending[0]["id"]
    assert client.post(f"/decision-requests/{rid}/resolve",
                       json={"outcome": "approved", "resolved_by": "hung"}).status_code == 403
    resolved = approver.post(f"/decision-requests/{rid}/resolve",
                             json={"outcome": "rejected", "resolved_by": "worker-1", "resolution": "keep it minimal"})
    assert resolved.status_code == 200 and resolved.json()["resolver_kind"] == "human"
    assert resolved.json()["resolved_by"] == "hung"
    assert approver.post(f"/decision-requests/{rid}/resolve", json={"outcome": "approved"}).status_code == 409
    assert run["started_by"] == "planner"


def test_run_endpoint_without_factory_is_503_and_bad_config_422(tmp_path, monkeypatch):
    monkeypatch.delenv("SDF_AGENT_SANDBOX", raising=False)
    plan_id = f"PLAN-API-{uuid.uuid4().hex[:8]}"
    body = client.post("/plans", json=plan_dict(make_fixture(tmp_path), plan_id)).json()
    approver.post(f"/plans/{plan_id}/approve", json={"expected_sha256": body["content_sha256"]})
    assert client.post(f"/plans/{plan_id}/runs", json={"config": {"max_cost_usd": 99}}).status_code == 422
    assert client.post(f"/plans/{plan_id}/runs", json={}).status_code == 503


def test_require_plan_approval_flag_gates_legacy_task_run(tmp_path, monkeypatch):
    fixture = tmp_path / "fx"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n")
    task_id = f"TASK-GATED-{uuid.uuid4().hex[:8]}"
    client.post("/tasks", json={"id": task_id, "title": "x", "idempotency_key": task_id})
    monkeypatch.setenv("SDF_REQUIRE_PLAN_APPROVAL", "1")
    response = client.post(f"/tasks/{task_id}/run", json={"fixture_dir": str(fixture)})
    assert response.status_code == 409 and "plan" in response.json()["detail"].lower()
