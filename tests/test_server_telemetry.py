"""Regression tests for #50: Attempt telemetry is server-measured, never caller-supplied."""

from pathlib import Path
import uuid

import pytest
from sqlalchemy.orm import sessionmaker

from sdf_core import usage
from sdf_core.adapter import FakeNativeAdapter
from sdf_core.api import SessionLocal
from sdf_core.db import AttemptRow, Base, TaskRow, make_engine
from sdf_core.execution import ExecutionService
from sdf_core.measurement import build_scorecard_from_db
from sdf_core.model import utcnow
from tests.auth_support import admin_client


client = admin_client()

FORGED = [
    ("cost_usd", 0.0001), ("cost_usd", 999.0), ("provider", "gpt-forged"), ("latency_ms", 1.0),
    ("input_tokens", 1), ("output_tokens", 1), ("error_class", None), ("escalation_reason", "forged"),
]


def _task(prefix="TEL"):
    task_id = f"TASK-{prefix}-{uuid.uuid4().hex[:8]}"
    created = client.post("/tasks", json={"id": task_id, "title": "telemetry", "idempotency_key": task_id.lower(),
                                          "acceptance_criteria": []})
    assert created.status_code == 201
    return task_id


@pytest.mark.parametrize("field, value", FORGED)
def test_run_request_rejects_caller_supplied_telemetry(tmp_path, monkeypatch, field, value):
    monkeypatch.chdir(tmp_path)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    task_id = _task()

    response = client.post(f"/tasks/{task_id}/run", json={"fixture_dir": str(fixture), field: value})

    assert response.status_code == 422
    with SessionLocal() as db:
        assert db.query(AttemptRow).filter_by(task_id=task_id).count() == 0


def test_escalation_reason_is_derived_from_the_parent_attempt(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    task_id = _task("ESC")
    first = client.post(f"/tasks/{task_id}/run", json={"fixture_dir": str(fixture)})
    assert first.status_code == 200
    parent_id = first.json()["attempt_id"]

    second = client.post(f"/tasks/{task_id}/run", json={
        "fixture_dir": str(fixture), "parent_attempt_id": parent_id, "model_tier": "medium",
    })

    assert second.status_code == 200, second.text
    reason = second.json()["escalation_reason"]
    assert reason is not None and reason.startswith(f"escalated from {parent_id} (tier basic, ")
    assert second.json()["provider"] == "fake"


class _MeteringFake(FakeNativeAdapter):
    """A fake adapter that writes usage-ledger rows, as a metered agent would."""

    def __init__(self, db):
        super().__init__("app.py", "print('new')\n")
        self.db = db

    def run(self, *, attempt_id, workspace, instructions):
        usage.record_turn(self.db, attempt_id=attempt_id, agent="pi", turn=1, session_id="s", sandbox_id=None,
                          sandbox_provider=None, usage=usage.TurnUsage(input_tokens=40, output_tokens=6, cost_usd=0.25),
                          status="ok", error_class=None)
        usage.record_turn(self.db, attempt_id=attempt_id, agent="pi", turn=2, session_id="s", sandbox_id=None,
                          sandbox_provider=None, usage=usage.TurnUsage(input_tokens=60, output_tokens=4, cost_usd=0.5),
                          status="ok", error_class=None)
        self.db.flush()
        return super().run(attempt_id=attempt_id, workspace=workspace, instructions=instructions)


class _Boom(FakeNativeAdapter):
    def run(self, **kwargs):
        raise TimeoutError("agent timed out")


def _service_db(tmp_path):
    engine = make_engine()
    Base.metadata.create_all(engine)
    db = sessionmaker(engine, expire_on_commit=False)()
    db.add(TaskRow(id="TASK-LEDGER", title="t", status="created", idempotency_key="task-ledger",
                   acceptance_criteria=["ok"], created_at=utcnow()))
    db.commit()
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    return db, fixture


def test_fake_mode_telemetry_and_scorecard_come_from_the_usage_ledger(tmp_path: Path):
    db, fixture = _service_db(tmp_path)
    service = ExecutionService(db, workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art", adapter=_MeteringFake(db))

    attempt = service.run(task_id="TASK-LEDGER", dispatch_key="d-ledger", fixture=fixture, instructions="go", commands=[],
                          criterion_checks={"ok": [["python", "-c", "print('ok')"]]})

    assert attempt.status == "completed"
    assert attempt.cost_usd == pytest.approx(0.75)
    assert (attempt.input_tokens, attempt.output_tokens) == (100, 10)
    assert attempt.provider == "fake"
    assert attempt.latency_ms is not None and attempt.latency_ms >= 0
    assert build_scorecard_from_db(db).cost_per_accepted_task == pytest.approx(0.75)


def test_service_run_no_longer_accepts_telemetry_arguments(tmp_path: Path):
    db, fixture = _service_db(tmp_path)
    service = ExecutionService(db, workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art",
                               adapter=FakeNativeAdapter("app.py", "x\n"))
    with pytest.raises(TypeError):
        service.run(task_id="TASK-LEDGER", dispatch_key="d-kw", fixture=fixture, instructions="go", commands=[],
                    cost_usd=123.0)


def test_adapter_failure_records_measured_latency_and_error_class(tmp_path: Path):
    db, fixture = _service_db(tmp_path)
    service = ExecutionService(db, workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art",
                               adapter=_Boom("app.py", "x\n"))
    with pytest.raises(TimeoutError):
        service.run(task_id="TASK-LEDGER", dispatch_key="d-boom", fixture=fixture, instructions="go", commands=[])
    attempt = db.query(AttemptRow).filter_by(dispatch_key="d-boom").one()
    assert attempt.status == "failed"
    assert attempt.provider == "fake" and attempt.latency_ms is not None
    assert attempt.error_class is not None
