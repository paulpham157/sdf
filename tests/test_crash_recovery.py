"""Host crash between an Attempt starting and finishing (ADR 0041).

Production startup is the FastAPI lifespan on ``sdf_core.api.app``. That startup
fails an Attempt left ``running`` when no executor still holds it, and moves the
Task ``running`` to ``evaluating`` to ``failed`` so a new dispatch key can enter
``ready``. ``RuntimeController.recover`` is not part of this path (ADR 0029).
"""

import gc
from datetime import timedelta
from pathlib import Path

import pytest

from sdf_core.adapter import AdapterResult, FakeNativeAdapter
from sdf_core.api import SessionLocal
from sdf_core.db import AttemptRow, TaskRow
from sdf_core.execution import ExecutionService
from sdf_core.model import utcnow
from sdf_core.recovery import LEASE_TTL, AttemptFenceError, recover_orphaned_attempts
from tests.auth_support import admin_client

# Terminal states an abandoned Attempt may end in. The model has no separate
# "abandoned" status; cancellation is the abandon transition from running.
_ORPHAN_TERMINAL = frozenset({"failed", "cancelled"})


class CountingAdapter:
    """Fake runtime adapter; records how many times a host asks it to run."""

    provider = "fake"
    evidence_mode = "synthetic"

    def __init__(self) -> None:
        self.calls = 0
        self._fake = FakeNativeAdapter("app.py", "print('new')\n")

    def run(self, *, attempt_id: str, workspace: Path, instructions: str):
        self.calls += 1
        return self._fake.run(attempt_id=attempt_id, workspace=workspace, instructions=instructions)


def test_host_crash_mid_run_is_recovered_and_task_is_retryable(tmp_path: Path, monkeypatch) -> None:
    """An Attempt abandoned between begin and finish is retryable after startup.

    The process is modeled as dying after ``begin`` has committed the Attempt
    and the Task ``running`` and before the adapter runs: the session and the
    service are dropped, and the durable rows are what a new process would
    read. The restarted host is this app entered through its lifespan, the
    same startup uvicorn runs. The invariant is that the orphan is then
    failed or cancelled, and a new dispatch key creates a new Attempt and
    runs it.
    """

    monkeypatch.chdir(tmp_path)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    adapter = CountingAdapter()
    # The restarted host's POST /run builds its adapter here. The crash below
    # never reaches adapter.run.
    monkeypatch.setattr("sdf_core.api.configured_run_adapter", lambda _db: adapter)

    task_id = "TASK-HOST-CRASH"
    db = SessionLocal()
    try:
        db.add(TaskRow(
            id=task_id, title="crash mid-run", status="created",
            idempotency_key="task-host-crash", acceptance_criteria=[],
            created_at=utcnow(),
        ))
        db.commit()
        service = ExecutionService(
            db,
            workspace_root=tmp_path / ".sdf-workspaces",
            artifact_root=tmp_path / ".sdf-artifacts",
            adapter=adapter,
        )
        begun = service.begin(task_id=task_id, dispatch_key="dispatch-host-crash-1", fixture=fixture)
        orphan_id = begun.attempt.id
        assert begun.attempt.status == "running"
        assert db.get(TaskRow, task_id).status == "running"
        assert adapter.calls == 0
    finally:
        db.close()
    # In-memory execution state dies with the process. Do not call finish,
    # adapter.run, or RuntimeController.recover: production startup does not.
    del service

    # uvicorn startup. The lifespan is the recovery entrypoint a restarted
    # host actually runs; there is no other one.
    with admin_client() as restarted:
        retry = restarted.post(f"/tasks/{task_id}/run", json={
            "fixture_dir": str(fixture),
            "dispatch_key": "dispatch-host-crash-2",
            "instructions": "retry after host crash",
        })

    db = SessionLocal()
    try:
        orphan = db.get(AttemptRow, orphan_id)
        task = db.get(TaskRow, task_id)
        assert orphan is not None and task is not None
        assert orphan.status in _ORPHAN_TERMINAL, (
            "a restarted host must leave the attempt abandoned between begin and finish "
            f"in a terminal failed state; after startup it is {orphan.status!r} and the task is "
            f"{task.status!r}; a new dispatch returned {retry.status_code}: {retry.text}"
        )
        assert retry.status_code == 200, retry.text
        body = retry.json()
        assert body["attempt_id"] != orphan_id
        assert adapter.calls == 1
        started = db.get(AttemptRow, body["attempt_id"])
        assert started is not None and started.task_id == task_id
        assert started.status != "running"
    finally:
        db.close()


def _running_attempt(db, task_id: str, attempt_id: str, dispatch_key: str, **fields) -> None:
    db.add(TaskRow(
        id=task_id, title=task_id, status="running", idempotency_key=task_id,
        acceptance_criteria=[], created_at=utcnow(),
    ))
    db.add(AttemptRow(
        id=attempt_id, task_id=task_id, status="running", dispatch_key=dispatch_key,
        agent="native", created_at=utcnow(), **fields,
    ))


def test_startup_recovery_is_idempotent(tmp_path: Path, monkeypatch) -> None:
    """Settling the same orphan twice does not move it again."""

    monkeypatch.chdir(tmp_path)
    attempt_id = "ATTEMPT-RECOVER-ONCE"
    task_id = "TASK-RECOVER-ONCE"
    db = SessionLocal()
    try:
        _running_attempt(db, task_id, attempt_id, "dispatch-recover-once")
        db.commit()
    finally:
        db.close()

    with admin_client():
        pass
    db = SessionLocal()
    try:
        attempt = db.get(AttemptRow, attempt_id)
        task = db.get(TaskRow, task_id)
        assert attempt is not None and task is not None
        assert attempt.status == "failed"
        assert attempt.error_class == "killed"
        assert task.status == "failed"
        snapshot = (
            attempt.status, attempt.error_class, task.status,
            attempt.executor_id, attempt.executor_heartbeat_at,
        )
    finally:
        db.close()

    with admin_client():
        pass
    db = SessionLocal()
    try:
        attempt = db.get(AttemptRow, attempt_id)
        task = db.get(TaskRow, task_id)
        assert (
            attempt.status, attempt.error_class, task.status,
            attempt.executor_id, attempt.executor_heartbeat_at,
        ) == snapshot
        assert attempt_id not in recover_orphaned_attempts(db)
    finally:
        db.close()


def test_live_executor_is_not_reaped_on_startup(tmp_path: Path, monkeypatch) -> None:
    """A held executor and a fresh foreign heartbeat stay running. A stale one does not."""

    monkeypatch.chdir(tmp_path)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    db = SessionLocal()
    service = ExecutionService(
        db, workspace_root=tmp_path / ".sdf-workspaces", artifact_root=tmp_path / ".sdf-artifacts",
    )
    db.add(TaskRow(
        id="TASK-LIVE-HELD", title="held", status="created", idempotency_key="task-live-held",
        acceptance_criteria=[], created_at=utcnow(),
    ))
    db.commit()
    begun = service.begin(task_id="TASK-LIVE-HELD", dispatch_key="dispatch-live-held", fixture=fixture)
    live_id = begun.attempt.id
    fresh_id = "ATTEMPT-LIVE-FOREIGN"
    stale_id = "ATTEMPT-STALE-FOREIGN"
    _running_attempt(
        db, "TASK-LIVE-FOREIGN", fresh_id, "dispatch-live-foreign",
        executor_id="other-executor", executor_heartbeat_at=utcnow(),
    )
    _running_attempt(
        db, "TASK-STALE-FOREIGN", stale_id, "dispatch-stale-foreign",
        executor_id="other-executor", executor_heartbeat_at=utcnow() - LEASE_TTL - timedelta(seconds=1),
    )
    db.commit()
    db.close()

    with admin_client():
        pass
    db = SessionLocal()
    try:
        assert db.get(AttemptRow, live_id).status == "running"
        assert db.get(TaskRow, "TASK-LIVE-HELD").status == "running"
        assert db.get(AttemptRow, fresh_id).status == "running"
        assert db.get(TaskRow, "TASK-LIVE-FOREIGN").status == "running"
        assert db.get(AttemptRow, stale_id).status == "failed"
        assert db.get(AttemptRow, stale_id).error_class == "killed"
        assert db.get(TaskRow, "TASK-STALE-FOREIGN").status == "failed"
    finally:
        db.close()

    del begun
    del service
    gc.collect()
    with admin_client():
        pass
    db = SessionLocal()
    try:
        assert db.get(AttemptRow, live_id).status == "failed"
        assert db.get(TaskRow, "TASK-LIVE-HELD").status == "failed"
        assert db.get(AttemptRow, fresh_id).status == "running"
        held = (db.get(AttemptRow, live_id).status, db.get(TaskRow, "TASK-LIVE-HELD").status)
    finally:
        db.close()

    with admin_client():
        pass
    db = SessionLocal()
    try:
        assert (db.get(AttemptRow, live_id).status, db.get(TaskRow, "TASK-LIVE-HELD").status) == held
        assert db.get(AttemptRow, fresh_id).status == "running"
    finally:
        db.close()


def test_in_memory_database_does_not_start_the_lease_refresher() -> None:
    from sdf_core.api import SessionLocal as factory
    from sdf_core.recovery import heartbeat_refresher_enabled

    assert heartbeat_refresher_enabled(factory) is False


def _begun(tmp_path: Path, task_id: str):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    db = SessionLocal()
    db.add(TaskRow(
        id=task_id, title=task_id, status="created", idempotency_key=task_id,
        acceptance_criteria=[], created_at=utcnow(),
    ))
    db.commit()
    service = ExecutionService(
        db, workspace_root=tmp_path / ".sdf-workspaces", artifact_root=tmp_path / ".sdf-artifacts",
    )
    begun = service.begin(task_id=task_id, dispatch_key=f"dispatch-{task_id}", fixture=fixture)
    return db, service, begun, fixture


def _finish(service, attempt_id: str, fixture: Path):
    return service.finish(
        attempt_id=attempt_id,
        result=AdapterResult(attempt_id=attempt_id, status="succeeded", changed_files=()),
        fixture=fixture,
        commands=[],
    )


def test_finish_does_not_overwrite_a_reaped_attempt(tmp_path: Path, monkeypatch) -> None:
    monkeypatch.chdir(tmp_path)
    task_id = "TASK-FENCE-STALE"
    db, service, begun, fixture = _begun(tmp_path, task_id)
    attempt_id = begun.attempt.id
    db.get(AttemptRow, attempt_id).executor_heartbeat_at = utcnow() - LEASE_TTL - timedelta(seconds=1)
    db.commit()
    assert attempt_id in recover_orphaned_attempts(db)
    assert db.get(AttemptRow, attempt_id).status == "failed"
    assert db.get(AttemptRow, attempt_id).error_class == "killed"
    assert db.get(TaskRow, task_id).status == "failed"

    with pytest.raises(AttemptFenceError):
        _finish(service, attempt_id, fixture)

    db.expire_all()
    attempt = db.get(AttemptRow, attempt_id)
    task = db.get(TaskRow, task_id)
    assert attempt.status == "failed"
    assert attempt.error_class == "killed"
    assert task.status == "failed"
    db.close()


def test_finish_from_another_executor_is_refused(tmp_path: Path, monkeypatch) -> None:
    monkeypatch.chdir(tmp_path)
    task_id = "TASK-FENCE-OTHER"
    db, service, begun, fixture = _begun(tmp_path, task_id)
    attempt_id = begun.attempt.id
    attempt = db.get(AttemptRow, attempt_id)
    attempt.executor_id = "other-executor"
    db.commit()
    before = (
        db.get(AttemptRow, attempt_id).status,
        db.get(AttemptRow, attempt_id).error_class,
        db.get(TaskRow, task_id).status,
    )

    with pytest.raises(AttemptFenceError):
        _finish(service, attempt_id, fixture)

    db.expire_all()
    attempt = db.get(AttemptRow, attempt_id)
    task = db.get(TaskRow, task_id)
    assert (attempt.status, attempt.error_class, task.status) == before
    assert attempt.status == "running" and task.status == "running"
    db.close()


def test_begin_then_finish_still_settles(tmp_path: Path, monkeypatch) -> None:
    monkeypatch.chdir(tmp_path)
    task_id = "TASK-FENCE-OK"
    db, service, begun, fixture = _begun(tmp_path, task_id)
    workspace = begun.workspace
    result = FakeNativeAdapter("app.py", "print('new')\n").run(
        attempt_id=begun.attempt.id, workspace=workspace, instructions="complete the task",
    )
    finished = service.finish(
        attempt_id=begun.attempt.id, result=result, fixture=fixture, commands=[],
    )
    assert finished.status == "completed"
    task = db.get(TaskRow, task_id)
    assert task.status != "running"
    db.close()
