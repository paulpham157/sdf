"""Live HTTP proof: Objective→Task→POST /tasks/{id}/run→Herdr/E2B→Evidence→/trace (#46).

Exercises the **public** run endpoint (not ExecutionService directly). Gated on
``SDF_LIVE_FULL_LOOP=1`` or ``SDF_LIVE_E2B=1``, plus ``E2B_API_KEY`` and a Codex
subscription Credential Mode. Teardown expects no leaked sandboxes on the happy
path. Secrets are never printed.
"""

from __future__ import annotations

import os
from pathlib import Path

import pytest
from e2b import Sandbox
from fastapi.testclient import TestClient

from sdf_core.api import SessionLocal, app
from sdf_core.credentials import load_dotenv
from sdf_core.db import ArtifactRow, EvidenceRow, RuntimeEventRow
from tests.test_e2b_agents_template_live import TEMPLATE
from tests.test_e2b_herdr_transport_live import _assert_gone, _running_ids
from tests.test_herdr_e2b_execution import NEGATIVE, POSITIVE

CONNECTION = os.environ.get("SDF_LIVE_CODEX_CONNECTION", "codex-personal")
ENV = dict(os.environ)
if os.environ.get("SDF_DOTENV"):
    load_dotenv(os.environ["SDF_DOTENV"], ENV)
load_dotenv(environ=ENV)

_LIVE_FLAG = os.environ.get("SDF_LIVE_FULL_LOOP") == "1" or os.environ.get("SDF_LIVE_E2B") == "1"
LIVE = _LIVE_FLAG and bool(ENV.get("E2B_API_KEY"))
MODEL = "gpt-6-luna"

pytestmark = pytest.mark.skipif(
    not LIVE,
    reason="live full-loop HTTP check: set SDF_LIVE_FULL_LOOP=1 (or SDF_LIVE_E2B=1) with E2B_API_KEY",
)

CHECK_ADD = [["python3", "-c", "from calc import add; assert add(2, 3) == 5"]]


def _apply_live_env(monkeypatch: pytest.MonkeyPatch) -> None:
    """Configure the public run path for Herdr/E2B without echoing secrets."""

    monkeypatch.setenv("SDF_RUN_ADAPTER", "herdr")
    monkeypatch.setenv("SDF_RUN_AGENT", "codex")
    monkeypatch.setenv("SDF_E2B_AGENTS_TEMPLATE", TEMPLATE)
    monkeypatch.setenv("SDF_CREDENTIAL_MODE_CODEX", "subscription")
    monkeypatch.setenv("SDF_CONNECTION_CODEX", CONNECTION)
    monkeypatch.setenv("SDF_HERDR_CODEX_MODEL", MODEL)
    monkeypatch.setenv("E2B_API_KEY", ENV["E2B_API_KEY"])
    if ENV.get("E2B_DOMAIN"):
        monkeypatch.setenv("E2B_DOMAIN", ENV["E2B_DOMAIN"])
    for name in ("HOME", "PATH", "USER", "XDG_CONFIG_HOME"):
        if name in ENV:
            monkeypatch.setenv(name, ENV[name])


def _fixture(tmp_path: Path) -> Path:
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "calc.py").write_text(
        "def add(a, b):\n    return a - b\n",
        encoding="utf-8",
    )
    return fixture


@pytest.mark.parametrize(
    "case",
    [
        {
            "name": "positive",
            "suffix": "POS",
            "instructions": POSITIVE,
            "expected_task_status": "succeeded",
            "expected_edge": "validates",
            "expected_evidence": "PASS",
        },
        {
            "name": "negative",
            "suffix": "NEG",
            "instructions": NEGATIVE,
            "expected_task_status": "failed",
            "expected_edge": "contradicts",
            "expected_evidence": "FAIL",
        },
    ],
)
def test_live_full_loop_via_public_run_endpoint(tmp_path: Path, monkeypatch: pytest.MonkeyPatch, case: dict):
    """Business Context → Objective → Task → POST /run → evaluator Evidence → /trace."""

    monkeypatch.chdir(tmp_path)
    _apply_live_env(monkeypatch)

    before = _running_ids()
    client = TestClient(app)
    suffix = case["suffix"]
    bc_id = f"BC-FULL-{suffix}"
    obj_id = f"OBJ-FULL-{suffix}"
    assumption_id = f"ASSUMPTION-FULL-{suffix}"
    task_id = f"TASK-FULL-{suffix}"
    criterion = "add returns the sum"

    assert client.post(
        "/business-contexts",
        json={"id": bc_id, "title": "Live full loop", "owner": "product", "source": "live-test"},
    ).status_code == 201
    assert client.post(
        "/objectives",
        json={
            "id": obj_id,
            "title": "Prove public run path",
            "business_context_id": bc_id,
            "owner": "product",
            "source": "live-test",
        },
    ).status_code == 201
    assert client.post(
        "/assumptions",
        json={
            "id": assumption_id,
            "title": "add() is correct",
            "owner": "product",
            "source": "live-test",
        },
    ).status_code == 201
    created = client.post(
        "/tasks",
        json={
            "id": task_id,
            "title": f"Live full loop {case['name']}",
            "objective_id": obj_id,
            "idempotency_key": f"live-full-loop-{case['name']}",
            "acceptance_criteria": [criterion],
        },
    )
    assert created.status_code == 201

    fixture = _fixture(tmp_path)
    run = client.post(
        f"/tasks/{task_id}/run",
        json={
            "dispatch_key": f"live-full-http-{case['name']}",
            "fixture_dir": str(fixture),
            "instructions": case["instructions"],
            "criterion_checks": {criterion: CHECK_ADD},
            "validation_target_kind": "assumption",
            "validation_target_id": assumption_id,
        },
    )
    assert run.status_code == 200, run.text
    body = run.json()
    assert body["task_status"] == case["expected_task_status"]
    attempt_id = body["attempt_id"]

    db = SessionLocal()
    try:
        evidence = db.query(EvidenceRow).filter_by(attempt_id=attempt_id).all()
        artifacts = db.query(ArtifactRow).filter_by(attempt_id=attempt_id).all()
        events = (
            db.query(RuntimeEventRow)
            .filter_by(attempt_id=attempt_id, source="herdr-e2b")
            .order_by(RuntimeEventRow.sequence)
            .all()
        )
        assert evidence, "evaluator produced no Evidence"
        assert all(row.attempt_id == attempt_id for row in evidence)
        assert [(row.criterion, row.status) for row in evidence] == [(criterion, case["expected_evidence"])]
        # Evidence must reference evaluator output, not agent self-report / LOG.
        log_ids = {a.id for a in artifacts if a.id.endswith("-LOG")}
        assert all(row.artifact_ref not in log_ids for row in evidence)
        assert events, "no herdr-e2b runtime events on public path"
        assert [e.kind for e in events] == [
            "runtime_started",
            "runtime_input_sent",
            "runtime_output_observed",
            "runtime_terminated",
        ]
    finally:
        db.close()

    trace = client.get(f"/tasks/{task_id}/trace")
    assert trace.status_code == 200, trace.text
    trace_body = trace.json()
    ids = {node["id"] for node in trace_body["nodes"]}
    assert {bc_id, obj_id, task_id, attempt_id, assumption_id} <= ids
    assert any(node["id"].endswith("-DIFF") for node in trace_body["nodes"] if node["kind"] == "artifact")
    assert any(
        edge["source_id"].startswith("EVIDENCE-")
        and edge["target_id"] == assumption_id
        and edge["relation"] == case["expected_edge"]
        for edge in trace_body["edges"]
    )
    herdr_events = [e for e in trace_body.get("runtime_events", []) if e.get("source") == "herdr-e2b"]
    assert len(herdr_events) == 4

    leaked = _running_ids() - before
    assert not leaked, f"sandbox(es) still listed after public run: {sorted(leaked)}"
    for sandbox_id in leaked:
        try:
            Sandbox.kill(sandbox_id, api_key=ENV["E2B_API_KEY"])
        except Exception:  # noqa: BLE001
            pass
        _assert_gone(sandbox_id)
