"""Review of #50 / PR #66: run prices, Evidence mode, retry parents and tiers are not caller-set."""

from __future__ import annotations

import logging
import math
import uuid
from datetime import datetime, timezone

import pytest

from sdf_core.agent_sandbox import e2b_factory
from sdf_core.api import SessionLocal, app, factory_evidence_mode
from sdf_core.coordinator import RunConfig, ServerConfigError, server_pricing
from sdf_core.db import AgentRunRow, AttemptRow
from sdf_core.model import utcnow
from tests.agent_comm_support import fake_factory, lead_behavior, make_fixture, plan_dict, worker_behavior
from tests.auth_support import admin_client, client_as
from tests.check_support import write_checks

client = client_as("operator", principal="planner")
approver = client_as("approver", principal="hung")
admin = admin_client()
FAST = {"poll_interval_s": 0.01, "stall_s": 0.5, "max_wall_s": 60}


@pytest.fixture()
def fake_agents(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    running: set[str] = set()
    app.state.agent_sandbox_factory = fake_factory(tmp_path / "boxes", running, {
        "lead": lead_behavior, "worker": worker_behavior()}, [])
    app.state.agent_cleanup_check = lambda ids: ids & running
    try:
        yield running
    finally:
        app.state.agent_sandbox_factory = None
        app.state.agent_cleanup_check = None


def _approved_plan(tmp_path) -> str:
    plan_id = f"PLAN-RCR-{uuid.uuid4().hex[:8]}"
    body = client.post("/plans", json=plan_dict(make_fixture(tmp_path), plan_id)).json()
    assert approver.post(f"/plans/{plan_id}/approve", json={"expected_sha256": body["content_sha256"]}).status_code == 200
    return plan_id


def _runs(plan_id: str) -> int:
    with SessionLocal() as db:
        return db.query(AgentRunRow).filter_by(plan_id=plan_id).count()


# --- B1: prices and Evidence mode --------------------------------------------

@pytest.mark.parametrize("field, value", [
    ("price_in_per_m", 0.0), ("price_out_per_m", -1000.0), ("sandbox_usd_per_s", -1.0),
    ("sandbox_usd_per_s", 0.0), ("evidence_mode", "live"), ("evidence_mode", "bogus"),
])
def test_caller_cannot_set_prices_or_evidence_mode(tmp_path, fake_agents, field, value):
    plan_id = _approved_plan(tmp_path)
    response = client.post(f"/plans/{plan_id}/runs", json={"config": {**FAST, field: value}, "wait": True})
    assert response.status_code == 422, response.text
    assert "set by the server" in response.json()["detail"]
    assert _runs(plan_id) == 0


def test_cost_cap_is_still_enforced_with_server_prices(tmp_path, fake_agents):
    plan_id = _approved_plan(tmp_path)
    run = client.post(f"/plans/{plan_id}/runs", json={"config": {**FAST, "max_cost_usd": 0.000001}, "wait": True})
    assert run.status_code == 202, run.text
    body = run.json()
    assert body["status"] == "failed" and "cost cap" in body["error"]
    assert body["config"]["sandbox_usd_per_s"] == RunConfig().sandbox_usd_per_s
    assert body["config"]["evidence_mode"] == "synthetic"  # a fake factory never yields live Evidence


def test_server_prices_come_from_the_environment(tmp_path, fake_agents, monkeypatch):
    monkeypatch.setenv("SDF_RUN_PRICE_IN_PER_M", "2.5")
    plan_id = _approved_plan(tmp_path)
    run = client.post(f"/plans/{plan_id}/runs", json={"config": {**FAST, "max_cost_usd": 0.000001}, "wait": True})
    assert run.json()["config"]["price_in_per_m"] == 2.5


@pytest.mark.parametrize("raw", ["-1", "nan", "inf", "abc", "5000"])
def test_invalid_server_price_is_a_server_error_not_a_free_run(tmp_path, fake_agents, monkeypatch, raw):
    monkeypatch.setenv("SDF_RUN_SANDBOX_USD_PER_S", raw)
    with pytest.raises(ServerConfigError):
        server_pricing()
    plan_id = _approved_plan(tmp_path)
    assert client.post(f"/plans/{plan_id}/runs", json={"config": FAST, "wait": True}).status_code == 503
    assert _runs(plan_id) == 0


def test_evidence_mode_follows_the_agent_factory():
    assert factory_evidence_mode(e2b_factory({})) == "live"
    assert factory_evidence_mode(lambda spec: None) == "synthetic"
    assert factory_evidence_mode(None) == "synthetic"
    assert RunConfig.from_request({}, evidence_mode="live").evidence_mode == "live"


@pytest.mark.parametrize("data", [
    {"evidence_mode": "bogus"}, {"price_in_per_m": -0.1}, {"price_out_per_m": math.nan},
    {"sandbox_usd_per_s": math.inf}, {"sandbox_usd_per_s": True}, {"price_in_per_m": "1"},
])
def test_stored_config_is_validated_too(data):
    with pytest.raises((TypeError, ValueError)):
        RunConfig.from_dict(data)


# --- S2: retry parent and model tier -----------------------------------------

def _task(criteria=()):
    task_id = f"TASK-RCR-{uuid.uuid4().hex[:8]}"
    created = admin.post("/tasks", json={"id": task_id, "title": "retry", "idempotency_key": task_id.lower(),
                                         "acceptance_criteria": list(criteria)})
    assert created.status_code == 201
    return task_id


def _fixture(tmp_path, *, passing: bool):
    fixture = tmp_path / "fixture"
    fixture.mkdir(exist_ok=True)
    write_checks(fixture, {"verdict": ["python", "-c", "pass" if passing else "raise SystemExit(1)"]})
    return fixture


def _run(task_id, fixture, **extra):
    return admin.post(f"/tasks/{task_id}/run", json={
        "fixture_dir": str(fixture), "criterion_checks": {"ok": ["verdict"]}, **extra})


def test_parent_that_passed_every_check_cannot_be_retried(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    fixture = _fixture(tmp_path, passing=True)
    task_id = _task(["ok"])
    first = _run(task_id, fixture)
    assert first.status_code == 200 and first.json()["attempt_status"] == "completed"
    retry = _run(task_id, fixture, parent_attempt_id=first.json()["attempt_id"])
    assert retry.status_code == 409 and "nothing to retry" in retry.json()["detail"]


def test_parent_must_be_finished(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    fixture = _fixture(tmp_path, passing=False)
    task_id = _task(["ok"])
    parent_id = f"ATTEMPT-RUNNING-{uuid.uuid4().hex[:8]}"
    with SessionLocal() as db:
        db.add(AttemptRow(id=parent_id, task_id=task_id, status="running", dispatch_key=parent_id,
                          agent="native", model_tier="basic", cost_usd=0.0, created_at=utcnow()))
        db.commit()
    retry = _run(task_id, fixture, parent_attempt_id=parent_id)
    assert retry.status_code == 409 and "only a finished attempt" in retry.json()["detail"]


def test_parent_must_belong_to_the_same_task(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    fixture = _fixture(tmp_path, passing=False)
    other, task_id = _task(["ok"]), _task(["ok"])
    parent = _run(other, fixture).json()["attempt_id"]
    assert _run(task_id, fixture, parent_attempt_id=parent).status_code == 409


def test_retry_tier_defaults_to_the_parent_and_cannot_go_down(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    fixture = _fixture(tmp_path, passing=False)
    task_id = _task(["ok"])
    first = _run(task_id, fixture, model_tier="medium").json()
    assert first["model_tier"] == "medium"
    lower = _run(task_id, fixture, parent_attempt_id=first["attempt_id"], model_tier="basic")
    assert lower.status_code == 409 and "below the parent" in lower.json()["detail"]
    same = _run(task_id, fixture, parent_attempt_id=first["attempt_id"]).json()
    assert same["model_tier"] == "medium" and same["parent_attempt_id"] == first["attempt_id"]
    assert same["escalation_reason"].startswith(f"escalated from {first['attempt_id']} (tier medium, ")
    higher = _run(task_id, fixture, parent_attempt_id=same["attempt_id"], model_tier="high").json()
    assert higher["model_tier"] == "high"


# --- S3: who recorded an observation ------------------------------------------

class _Collect(logging.Handler):
    def __init__(self):
        super().__init__(logging.INFO)
        self.records: list[logging.LogRecord] = []

    def emit(self, record):
        self.records.append(record)


def test_observation_records_the_principal_in_the_audit_log():
    admin.post("/business-contexts", json={"id": "BC-RCR", "title": "c", "owner": "p", "source": "t"})
    admin.post("/objectives", json={"id": "OBJ-RCR", "title": "o", "business_context_id": "BC-RCR",
                                    "owner": "p", "source": "t"})
    logger, handler = logging.getLogger("sdf.api"), _Collect()
    level, disabled = logger.level, logger.disabled
    logger.addHandler(handler)
    logger.setLevel(logging.INFO)
    logger.disabled = False  # another test's dictConfig may have disabled existing loggers
    try:
        response = admin.post("/objectives/OBJ-RCR/observations", json={
            "metric_name": "m", "value": 1.0, "observed_at": datetime.now(timezone.utc).isoformat(),
            "source": "s", "mode": "synthetic"})
    finally:
        logger.removeHandler(handler)
        logger.setLevel(level)
        logger.disabled = disabled
    assert response.status_code == 201
    records = [r for r in handler.records if r.getMessage() == "observation_recorded"]
    assert records and records[-1].observation_id == response.json()["id"]
    assert records[-1].principal  # the authenticated caller, not a body field
