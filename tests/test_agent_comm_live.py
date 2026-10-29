"""Live ADR 0014 end-to-end: plan gate + lead/worker agents in E2B on Vultr glm-5.3.

Skipped by default.  Gated on ``SDF_LIVE_E2B=1`` and ``SDF_LIVE_AGENT_COMM=1`` plus
``E2B_API_KEY`` and ``VULTR_INFERENCE_API_KEY`` (env or the repo-root ``.env``,
loaded into a private mapping; presence is checked, values never printed).

Flow, all through the HTTP API (FastAPI TestClient, no listening socket):
propose plan -> start run (409, not approved) -> human approves -> run a lead
(``SDF_LIVE_LEAD_KIND``, default opencode) and two workers
(``SDF_LIVE_WORKER_KIND``, default pi), one sandbox each (``SDF_E2B_AGENT_TEMPLATE``,
default ``sdf-herdr-agents``),
on the toy repo (fix ``add`` in calc.py, add ``mul`` to calc2.py).  Success
needs host Evidence for both subtasks.  Afterwards no sandbox of the run may
still be running.  A JSON report goes to ``/tmp/sdf-live-reports``.
"""

from __future__ import annotations

import json
import os
import time
import uuid
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from sdf_core.agent_sandbox import DEFAULT_TEMPLATE, agent_environ, describe_env, e2b_factory, running_sandbox_ids
from sdf_core.api import app
from sdf_core.credentials import load_dotenv
from tests.agent_comm_support import make_fixture, plan_dict
from tests.auth_support import client_as

ENV = dict(os.environ)
load_dotenv(environ=ENV)
LIVE = (ENV.get("SDF_LIVE_E2B") == "1" and ENV.get("SDF_LIVE_AGENT_COMM") == "1"
        and bool(ENV.get("E2B_API_KEY")) and bool(ENV.get("VULTR_INFERENCE_API_KEY")))
pytestmark = pytest.mark.skipif(not LIVE, reason="live: set SDF_LIVE_E2B=1 SDF_LIVE_AGENT_COMM=1 with E2B/Vultr keys")

LEAD_KIND = ENV.get("SDF_LIVE_LEAD_KIND", "opencode")
WORKER_KIND = ENV.get("SDF_LIVE_WORKER_KIND", "pi")
# E.g. a private staging build of the image before it replaces the public one.
TEMPLATE = ENV.get("SDF_E2B_AGENT_TEMPLATE", DEFAULT_TEMPLATE)
REPORT_DIR = Path("/tmp/sdf-live-reports")


def test_live_plan_gate_lead_and_two_workers(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    monkeypatch.setenv("SDF_WORKSPACE_ROOT", str(tmp_path / "ws"))
    monkeypatch.setenv("SDF_ARTIFACT_ROOT", str(tmp_path / "art"))
    env = agent_environ(ENV, {LEAD_KIND, WORKER_KIND})
    print("agent env (presence only):", describe_env(env))
    key = ENV["E2B_API_KEY"]
    seen: set[str] = set()

    def check(ids: set[str]) -> set[str]:
        seen.update(ids)
        return ids & running_sandbox_ids(key)

    app.state.agent_sandbox_factory = e2b_factory(env, template=TEMPLATE)
    app.state.agent_cleanup_check = check
    # ADR 0015: ephemeral tokens in the test DB - an operator proposes and
    # runs, a different human approver approves; agents get no token.
    client = client_as("operator", principal="live-operator")
    approver = client_as("approver", principal="hung")
    assert TestClient(app).post("/plans", json={}).status_code == 401
    report: dict = {"lead_kind": LEAD_KIND, "worker_kind": WORKER_KIND, "template": TEMPLATE, "steps": []}
    t0 = time.monotonic()
    try:
        plan_id = f"PLAN-LIVE-{uuid.uuid4().hex[:8]}"
        proposed = client.post("/plans", json=plan_dict(make_fixture(tmp_path), plan_id))
        assert proposed.status_code == 201, proposed.text
        report["steps"].append({"propose": proposed.status_code, "plan_sha256": proposed.json()["content_sha256"]})

        config = {"lead": {"kind": LEAD_KIND}, "worker": {"kind": WORKER_KIND},
                  "max_cost_usd": 0.19, "max_wall_s": 1200}
        early = client.post(f"/plans/{plan_id}/runs", json={"config": config, "wait": True})
        report["steps"].append({"start_before_approval": early.status_code, "detail": early.json()["detail"]})
        assert early.status_code == 409

        assert client.post(f"/plans/{plan_id}/approve", json={}).status_code == 403
        approved = approver.post(f"/plans/{plan_id}/approve", json={
            "decided_by": "spoofed-name", "rationale": "live E2E: toy subtasks with host checks",
            "expected_sha256": proposed.json()["content_sha256"]})
        assert approved.status_code == 200, approved.text
        assert approved.json()["decision_request"]["resolved_by"] == "hung"
        report["steps"].append({"approve": approved.status_code,
                                "decision_request": approved.json()["decision_request"]})

        started = time.monotonic()
        run = client.post(f"/plans/{plan_id}/runs", json={"config": config, "wait": True}).json()
        report["run"] = run
        report["run_wall_s"] = round(time.monotonic() - started, 1)
        report["messages"] = client.get(f"/runs/{run['id']}/messages").json()
        report["decision_requests"] = client.get("/decision-requests", params={"run_id": run["id"]}).json()
    finally:
        app.state.agent_sandbox_factory = None
        app.state.agent_cleanup_check = None
        report["total_wall_s"] = round(time.monotonic() - t0, 1)
        try:
            report["account_running_after"] = len(running_sandbox_ids(key))
            report["run_sandboxes_still_running"] = sorted(seen & running_sandbox_ids(key))
        except Exception as exc:  # noqa: BLE001
            report["cleanup_check_error"] = str(exc)[:300]
        REPORT_DIR.mkdir(exist_ok=True)
        out = REPORT_DIR / f"agent-comm-{int(time.time())}.json"
        out.write_text(json.dumps(report, indent=2, default=str))
        print("report:", out)

    assert report.get("run_sandboxes_still_running") == []
    assert run["status"] == "succeeded", run["error"]
    assert run["started_by"] == "live-operator"
    for key_, sub in run["summary"]["subtasks"].items():
        assert sub["verdict"] == "PASS", key_
        assert all(e["status"] == "PASS" and e["mode"] == "live" for e in sub["attempts"][-1]["evidence"])
