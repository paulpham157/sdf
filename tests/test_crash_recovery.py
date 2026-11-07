"""Host crash between an Attempt starting and finishing (ADR 0041).

Production startup is the FastAPI lifespan on ``sdf_core.api.app``. That startup
fails an Attempt left ``running`` when no executor still holds it, and moves the
Task ``running`` to ``evaluating`` to ``failed`` so a new dispatch key can enter
``ready``. ``RuntimeController.recover`` is not part of this path (ADR 0029).
"""

import gc
import os
from datetime import timedelta, timezone
from pathlib import Path

import pytest

from sdf_core.adapter import AdapterResult, FakeNativeAdapter
from sdf_core.api import SessionLocal
from sdf_core.db import AttemptRow, TaskRow
from sdf_core.dispatch_key import derived_dispatch_key
from sdf_core.execution import ExecutionService
from sdf_core.fixtures import resolve_fixture
from sdf_core.model import utcnow
from sdf_core.recovery import (
    AttemptFenceError, LEASE_TTL, clear_remote_effect, recover_orphaned_attempts, register_remote_effect,
    reset_recovery_state,
)
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


def _expire_heartbeat(db, attempt_id: str) -> None:
    attempt = db.get(AttemptRow, attempt_id)
    attempt.executor_heartbeat_at = utcnow() - LEASE_TTL - timedelta(seconds=1)
    db.commit()


def test_host_crash_mid_run_is_recovered_and_task_is_retryable(tmp_path: Path, monkeypatch) -> None:
    """An Attempt abandoned between begin and finish is retryable after restart.

    A real restart mints a new ``EXECUTOR_ID``. The previous process's
    heartbeat is expired (the lease has elapsed), which is how a new process
    distinguishes a dead neighbor from a live one. Lifespan startup then
    fails the orphan so a new dispatch key can enter ``ready``.
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
    del begun
    del service
    gc.collect()
    reset_recovery_state()
    db = SessionLocal()
    try:
        _expire_heartbeat(db, orphan_id)
    finally:
        db.close()

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
    # A real restart: new executor id, previous heartbeat still fresh. The
    # orphan looks like a live neighbor until the lease expires.
    reset_recovery_state()
    with admin_client():
        pass
    db = SessionLocal()
    try:
        assert db.get(AttemptRow, live_id).status == "running"
        assert db.get(AttemptRow, fresh_id).status == "running"
        _expire_heartbeat(db, live_id)
    finally:
        db.close()

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


def test_expired_lease_is_settled_on_next_dispatch_without_restart(tmp_path: Path, monkeypatch) -> None:
    """A new /run after the lease expires settles the orphan; no extra restart."""

    monkeypatch.chdir(tmp_path)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    adapter = CountingAdapter()
    monkeypatch.setattr("sdf_core.api.configured_run_adapter", lambda _db: adapter)
    task_id = "TASK-EXPIRE-ON-RUN"
    orphan_id = "ATTEMPT-EXPIRE-ON-RUN"
    with admin_client() as client:
        created = client.post("/tasks", json={
            "id": task_id, "title": "expire on run", "idempotency_key": "task-expire-on-run",
            "acceptance_criteria": [],
        })
        assert created.status_code == 201, created.text
        db = SessionLocal()
        try:
            task = db.get(TaskRow, task_id)
            task.status = "running"
            db.add(AttemptRow(
                id=orphan_id, task_id=task_id, status="running",
                dispatch_key="dispatch-expire-on-run", agent="native", created_at=utcnow(),
                executor_id="dead-process",
                executor_heartbeat_at=utcnow() - LEASE_TTL - timedelta(seconds=1),
            ))
            db.commit()
        finally:
            db.close()
        retry = client.post(f"/tasks/{task_id}/run", json={
            "fixture_dir": str(fixture),
            "dispatch_key": "dispatch-expire-on-run-2",
            "instructions": "retry after lease expired",
        })
        assert retry.status_code == 200, retry.text
        assert retry.json()["attempt_id"] != orphan_id
        assert adapter.calls == 1
    db = SessionLocal()
    try:
        assert db.get(AttemptRow, orphan_id).status == "failed"
        assert db.get(AttemptRow, orphan_id).error_class == "killed"
    finally:
        db.close()


def test_recovery_does_not_overwrite_a_completed_attempt(tmp_path: Path, monkeypatch) -> None:
    """If finish commits first, recovery's UPDATE WHERE status=running matches nothing."""

    monkeypatch.chdir(tmp_path)
    attempt_id = "ATTEMPT-CAS-FINISH-WINS"
    task_id = "TASK-CAS-FINISH-WINS"
    db = SessionLocal()
    try:
        _running_attempt(db, task_id, attempt_id, "dispatch-cas-finish-wins")
        db.commit()
    finally:
        db.close()

    def complete_then_look_dead(attempt, *, now=None):
        other = SessionLocal()
        try:
            row = other.get(AttemptRow, attempt.id)
            row.status = "completed"
            task = other.get(TaskRow, attempt.task_id)
            task.status = "succeeded"
            other.commit()
        finally:
            other.close()
        return False

    monkeypatch.setattr("sdf_core.recovery.executor_is_live", complete_then_look_dead)
    db = SessionLocal()
    try:
        assert recover_orphaned_attempts(db) == ()
    finally:
        db.close()
    db = SessionLocal()
    try:
        attempt = db.get(AttemptRow, attempt_id)
        task = db.get(TaskRow, task_id)
        assert attempt.status == "completed"
        assert attempt.error_class is None
        assert task.status == "succeeded"
    finally:
        db.close()


def test_stale_finish_does_not_overwrite_recovery(tmp_path: Path, monkeypatch) -> None:
    """A worker whose session still says running cannot undo recovery or the Task."""

    monkeypatch.chdir(tmp_path)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    db = SessionLocal()
    service = ExecutionService(
        db, workspace_root=tmp_path / ".sdf-workspaces", artifact_root=tmp_path / ".sdf-artifacts",
    )
    db.add(TaskRow(
        id="TASK-CAS-RECOVER-WINS", title="stale finish", status="created",
        idempotency_key="task-cas-recover-wins", acceptance_criteria=[], created_at=utcnow(),
    ))
    db.commit()
    begun = service.begin(task_id="TASK-CAS-RECOVER-WINS", dispatch_key="dispatch-cas-recover-wins", fixture=fixture)
    attempt_id = begun.attempt.id
    workspace = begun.workspace
    assert service.db.get(AttemptRow, attempt_id).status == "running"

    from sdf_core import recovery as recovery_mod
    with recovery_mod._lock:
        recovery_mod._owners.pop(attempt_id, None)
    other = SessionLocal()
    try:
        settled = recover_orphaned_attempts(other)
    finally:
        other.close()
    assert attempt_id in settled
    check = SessionLocal()
    try:
        assert check.get(AttemptRow, attempt_id).status == "failed"
        assert check.get(AttemptRow, attempt_id).error_class == "killed"
        assert check.get(TaskRow, "TASK-CAS-RECOVER-WINS").status == "failed"
    finally:
        check.close()

    # The worker's session still has the pre-recovery Attempt identity
    # (expire_on_commit=False). Shared in-memory SQLite may refresh the Task.
    assert service.db.get(AttemptRow, attempt_id).status == "running"
    result = AdapterResult(
        attempt_id=attempt_id, status="completed", changed_files=("app.py",),
        stdout="", stderr="", exit_code=0,
    )
    (workspace / "app.py").write_text("print('new')\n", encoding="utf-8")
    with pytest.raises((ValueError, AttemptFenceError), match="is not running"):
        service.finish(attempt_id=attempt_id, result=result, fixture=fixture, commands=[])

    check = SessionLocal()
    try:
        assert check.get(AttemptRow, attempt_id).status == "failed"
        assert check.get(AttemptRow, attempt_id).error_class == "killed"
        assert check.get(TaskRow, "TASK-CAS-RECOVER-WINS").status == "failed"
    finally:
        check.close()


def test_keyless_retry_after_recovered_failure_replays_failed_attempt(tmp_path: Path, monkeypatch) -> None:
    """A keyless POST after crash recovery returns the killed Attempt and does not run again."""

    monkeypatch.chdir(tmp_path)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    adapter = CountingAdapter()
    monkeypatch.setattr("sdf_core.api.configured_run_adapter", lambda _db: adapter)
    task_id = "TASK-KEYLESS-AFTER-KILL"
    body = {"fixture_dir": str(fixture), "instructions": "complete the task"}
    resolved = os.fspath(resolve_fixture(str(fixture)))
    key = derived_dispatch_key(
        task_id=task_id, fixture_dir=resolved, instructions=body["instructions"],
        criterion_checks=None, validation_target_kind=None, validation_target_id=None,
        parent_attempt_id=None, model_tier=None,
    )
    with admin_client() as client:
        created = client.post("/tasks", json={
            "id": task_id, "title": "keyless after kill",
            "idempotency_key": "task-keyless-after-kill", "acceptance_criteria": [],
        })
        assert created.status_code == 201, created.text
        planted_id = "ATTEMPT-KEYLESS-AFTER-KILL"
        db = SessionLocal()
        try:
            task = db.get(TaskRow, task_id)
            task.status = "running"
            db.add(AttemptRow(
                id=planted_id, task_id=task_id, status="running", dispatch_key=key,
                agent="native", created_at=utcnow(),
            ))
            db.commit()
        finally:
            db.close()
        response = client.post(f"/tasks/{task_id}/run", json=body)
        assert response.status_code == 200, response.text
        assert response.json()["attempt_id"] == planted_id
        assert response.json()["task_status"] == "failed"
        assert adapter.calls == 0
    db = SessionLocal()
    try:
        attempt = db.get(AttemptRow, planted_id)
        assert attempt.status == "failed"
        assert attempt.error_class == "killed"
        assert attempt.dispatch_key == key
        assert db.query(AttemptRow).filter_by(task_id=task_id).count() == 1
    finally:
        db.close()

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
    from sdf_core.db import ArtifactRow, DecisionEdgeRow, EvidenceRow, GraphNodeRow
    from sdf_core.recovery import _owners

    # Drop the in-memory claim so recovery can reap. Keep the service object
    # so finish still runs against a stale "running" identity map.
    _owners.pop(attempt_id, None)
    side = SessionLocal()
    try:
        assert attempt_id in recover_orphaned_attempts(side)
    finally:
        side.close()
    assert db.get(AttemptRow, attempt_id).status == "running"

    with pytest.raises(AttemptFenceError, match="is not running"):
        _finish(service, attempt_id, fixture)

    db.expire_all()
    attempt = db.get(AttemptRow, attempt_id)
    task = db.get(TaskRow, task_id)
    assert attempt.status == "failed"
    assert attempt.error_class == "killed"
    assert task.status == "failed"
    assert db.query(ArtifactRow).filter_by(attempt_id=attempt_id).count() == 0
    assert db.query(EvidenceRow).filter_by(attempt_id=attempt_id).count() == 0
    assert db.query(GraphNodeRow).filter(GraphNodeRow.id.like(f"{attempt_id}-%")).count() == 0
    assert db.query(DecisionEdgeRow).filter_by(target_id=attempt_id, source_kind="artifact").count() == 0
    artifact_root = tmp_path / ".sdf-artifacts"
    leftovers = list(artifact_root.glob(f"{attempt_id}*")) if artifact_root.exists() else []
    assert leftovers == []
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


def test_forked_worker_does_not_reap_a_siblings_fresh_attempt(tmp_path: Path, monkeypatch) -> None:
    """A worker that inherited the arbiter id remints, then leaves a sibling's Attempt running."""

    monkeypatch.chdir(tmp_path)
    from sdf_core import recovery as rec

    task_id = "TASK-FORK-SIBLING"
    db, service, begun, _fixture = _begun(tmp_path, task_id)
    attempt_id = begun.attempt.id
    saved_id = rec.EXECUTOR_ID
    saved_owners = list(rec._owners.items())
    arbiter_id = f"1-{os.getpid()}"  # not this process's pid prefix after we overwrite it
    try:
        # Worker A forked from the arbiter, reminted, and claimed the Attempt.
        rec.EXECUTOR_ID = arbiter_id
        rec._after_fork_in_child()
        worker_a = rec.EXECUTOR_ID
        assert worker_a.startswith(f"{os.getpid()}-") and worker_a != arbiter_id
        begun.attempt.executor_id = worker_a
        db.commit()
        # Worker B forked from the same arbiter, not from A, so its owner map is empty.
        rec.EXECUTOR_ID = arbiter_id
        with rec._lock:
            rec._owners.clear()
        rec._after_fork_in_child()
        assert rec.EXECUTOR_ID != worker_a
        assert attempt_id not in recover_orphaned_attempts(db)
        db.expire_all()
        assert db.get(AttemptRow, attempt_id).status == "running"
    finally:
        rec.EXECUTOR_ID = saved_id
        with rec._lock:
            rec._owners.clear()
            for key, owner in saved_owners:
                if owner is not None:
                    rec._owners[key] = owner
        db.close()
        del service


def test_held_owner_survives_a_stale_heartbeat_when_the_refresher_is_stopped(
    tmp_path: Path, monkeypatch,
) -> None:
    monkeypatch.chdir(tmp_path)
    from sdf_core.recovery import shutdown_executor_heartbeat

    shutdown_executor_heartbeat()
    task_id = "TASK-HELD-STALE"
    db, service, begun, _fixture = _begun(tmp_path, task_id)
    attempt_id = begun.attempt.id
    db.get(AttemptRow, attempt_id).executor_heartbeat_at = utcnow() - LEASE_TTL - timedelta(seconds=5)
    db.commit()
    later = utcnow()
    assert attempt_id not in recover_orphaned_attempts(db, now=later)
    db.expire_all()
    assert db.get(AttemptRow, attempt_id).status == "running"

    from sdf_core.recovery import _owners

    _owners.pop(attempt_id, None)
    assert attempt_id in recover_orphaned_attempts(db, now=later)
    db.expire_all()
    assert db.get(AttemptRow, attempt_id).status == "failed"
    db.close()
    del service


def test_fail_attempt_does_not_move_a_task_with_a_live_sibling(tmp_path: Path, monkeypatch) -> None:
    monkeypatch.chdir(tmp_path)
    from sdf_core.recovery import EXECUTOR_ID, _owners

    task_id = "TASK-FAIL-SIBLING"
    db, service, begun, _fixture = _begun(tmp_path, task_id)
    dead_id = begun.attempt.id
    live_id = "ATTEMPT-FAIL-SIBLING-LIVE"
    db.add(AttemptRow(
        id=live_id, task_id=task_id, status="running", dispatch_key="dispatch-fail-sibling-live",
        agent="native", created_at=utcnow(), executor_id=EXECUTOR_ID,
        executor_heartbeat_at=utcnow(),
    ))
    db.commit()

    class _Hold:
        pass

    holder = _Hold()
    _owners[live_id] = holder
    _owners.pop(dead_id, None)
    side = SessionLocal()
    try:
        assert dead_id in recover_orphaned_attempts(side)
    finally:
        side.close()
    # The caller's Attempt object is still the pre-recovery row.
    assert db.get(AttemptRow, dead_id).status == "running"

    service._fail_attempt(db.get(TaskRow, task_id), db.get(AttemptRow, dead_id))

    db.expire_all()
    dead = db.get(AttemptRow, dead_id)
    live = db.get(AttemptRow, live_id)
    task = db.get(TaskRow, task_id)
    assert dead.status == "failed" and dead.error_class == "killed"
    assert live.status == "running"
    assert task.status == "running"
    _owners.pop(live_id, None)
    db.close()


def test_stale_coordinator_cancel_leaves_a_failed_task(tmp_path: Path, monkeypatch) -> None:
    monkeypatch.chdir(tmp_path)
    from sqlalchemy.orm import Session as SASession

    from sdf_core.coordinator import Coordinator, _Subtask
    from sdf_core.db import AgentRunRow
    from sdf_core.recovery import EXECUTOR_ID

    task_id = "TASK-STALE-CANCEL"
    db, service, begun, _fixture = _begun(tmp_path, task_id)
    attempt_id = begun.attempt.id
    stale_task = db.get(TaskRow, task_id)
    other = SessionLocal()
    try:
        other.get(TaskRow, task_id).status = "failed"
        other.commit()
    finally:
        other.close()
    assert stale_task.status == "running"
    assert begun.attempt.executor_id == EXECUTOR_ID

    original = SASession.get

    def get(self, entity, ident, **kwargs):
        if entity is TaskRow and ident == task_id:
            return stale_task
        return original(self, entity, ident, **kwargs)

    monkeypatch.setattr(SASession, "get", get)
    run_id = "RUN-STALE-CANCEL"
    # Core insert: the ORM guard refuses an agent run whose plan is not approved.
    from sqlalchemy import insert
    db.execute(insert(AgentRunRow).values(
        id=run_id, plan_id="PLAN-STALE", status="running", config={}, started_at=utcnow(), cost_usd=0,
    ))
    db.commit()
    coord = Coordinator(db, factory=object(), workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art")
    sub = _Subtask(key="s", spec={}, task_id=task_id, worker="worker-1", attempt_id=attempt_id)
    coord._finalize(run_id, {}, {"s": sub}, config=_empty_run_config(), outcome="cancelled", error=None,
                    violations=[], timings={}, wall_s=0, closed=[], still_running=set())
    check = SessionLocal()
    try:
        assert check.get(TaskRow, task_id).status == "failed"
    finally:
        check.close()
        db.close()


def _empty_run_config():
    from sdf_core.coordinator import RunConfig

    return RunConfig()


class FakeSandboxProvider:
    """Remote sandboxes keep executing until ``kill``.

    ``recover_orphaned_attempts`` does not see this registry. A host that dies
    after ``create`` never reaches the kill that ``LiveRunAdapter._teardown``
    runs only on ``LiveRunAdapter.run``'s return or exception path.
    """

    def __init__(self) -> None:
        self._next = 0
        self.sandboxes: dict[str, dict[str, str]] = {}
        self.events: list[tuple] = []

    def create(self, attempt_id: str) -> str:
        self._next += 1
        sandbox_id = f"sbx-{self._next}"
        still_running = tuple(
            sid for sid, row in self.sandboxes.items() if row["state"] == "running"
        )
        self.sandboxes[sandbox_id] = {"attempt_id": attempt_id, "state": "running"}
        self.events.append(("create", sandbox_id, attempt_id, still_running))
        return sandbox_id

    def kill(self, sandbox_id: str) -> None:
        self.sandboxes[sandbox_id]["state"] = "killed"
        self.events.append(("kill", sandbox_id))

    def still_executing(self, sandbox_id: str) -> bool:
        return self.sandboxes[sandbox_id]["state"] == "running"

    def emit(self, sandbox_id: str, effect: str) -> None:
        """A side effect the sandbox can still perform while it is executing."""

        if not self.still_executing(sandbox_id):
            raise RuntimeError(f"{sandbox_id} is terminated; refusing {effect}")
        self.events.append(("effect", sandbox_id, effect))


class FakeProviderSandboxAdapter(FakeNativeAdapter):
    """Fake provider whose teardown runs only if ``run`` returns or raises.

    That is the same shape as ``LiveRunAdapter``: ``_teardown`` is not called
    from orphan recovery. ``begin_remote_work`` is the create that a lost host
    already did, with no kill afterwards.
    """

    def __init__(self, sandboxes: FakeSandboxProvider) -> None:
        super().__init__("app.py", "print('new')\n")
        self.sandboxes = sandboxes
        self.calls = 0

    def begin_remote_work(self, attempt_id: str) -> str:
        """Start a sandbox the way a live adapter does, and do not kill it.

        ``LiveRunAdapter.confirm_live`` commits ``evidence_mode=live`` once a
        sandbox exists. Recovery must see that, and the process-local probe,
        or a later dispatch can overlap this sandbox. The probe reports stop
        only after ``kill``; this method does not kill.
        """

        sandbox_id = self.sandboxes.create(attempt_id)
        db = SessionLocal()
        try:
            row = db.get(AttemptRow, attempt_id)
            row.evidence_mode = "live"
            db.commit()
        finally:
            db.close()

        def confirm_stopped() -> bool:
            return not self.sandboxes.still_executing(sandbox_id)

        register_remote_effect(attempt_id, confirm_stopped)
        return sandbox_id

    def run(self, *, attempt_id: str, workspace: Path, instructions: str):
        self.calls += 1
        sandbox_id = self.sandboxes.create(attempt_id)
        try:
            return super().run(attempt_id=attempt_id, workspace=workspace, instructions=instructions)
        finally:
            self.sandboxes.kill(sandbox_id)


def test_new_dispatch_does_not_overlap_a_sandbox_left_running_by_lease_expiry(
    tmp_path: Path, monkeypatch, request,
) -> None:
    """A new explicit dispatch must not overlap remote work the dead host left running.

    Lease expiry fails the Attempt row and unsticks the Task. It does not kill
    or reconcile the provider sandbox, revoke credentials, cancel a runtime, or
    wait for that work to stop. ``LiveRunAdapter._teardown`` runs only when
    ``run`` returns or raises in the host process, so a lost host leaves the
    sandbox executing.

    ADR 0041 then allows a new explicit ``dispatch_key`` (with ``parent_attempt_id``)
    to start another Attempt. This contract is stronger than that ADR: either
    sandbox A is confirmed terminated before sandbox B is created, or the new
    dispatch is rejected while A is still executing. Overlapping provider side
    effects are not allowed.

    The same explicit key must still replay the original Attempt and must not
    start a sandbox. Keyless replay of a derived key is covered by
    ``test_keyless_retry_after_recovered_failure_replays_failed_attempt``.
    """

    monkeypatch.chdir(tmp_path)
    holder: dict[str, str] = {}
    request.addfinalizer(lambda: clear_remote_effect(holder["id"]) if holder.get("id") else None)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    provider = FakeSandboxProvider()
    adapter = FakeProviderSandboxAdapter(provider)
    monkeypatch.setattr("sdf_core.api.configured_run_adapter", lambda _db: adapter)
    task_id = "TASK-REMOTE-OVERLAP"
    first_key = "dispatch-remote-overlap-1"
    db = SessionLocal()
    service = ExecutionService(
        db, workspace_root=tmp_path / ".sdf-workspaces", artifact_root=tmp_path / ".sdf-artifacts",
        adapter=adapter,
    )
    try:
        db.add(TaskRow(
            id=task_id, title="remote overlap", status="created",
            idempotency_key="task-remote-overlap", acceptance_criteria=[], created_at=utcnow(),
        ))
        db.commit()
        begun = service.begin(task_id=task_id, dispatch_key=first_key, fixture=fixture)
        orphan_id = begun.attempt.id
        holder["id"] = orphan_id
        sandbox_a = adapter.begin_remote_work(orphan_id)
        assert provider.still_executing(sandbox_a)
        assert adapter.calls == 0
    finally:
        db.close()
    # The host process is gone. Do not call adapter.run: that is the only path
    # that kills the sandbox, and a dead host does not run it.
    del begun
    del service
    gc.collect()
    reset_recovery_state()
    db = SessionLocal()
    try:
        _expire_heartbeat(db, orphan_id)
    finally:
        db.close()

    with admin_client() as restarted:
        replay = restarted.post(f"/tasks/{task_id}/run", json={
            "fixture_dir": str(fixture),
            "dispatch_key": first_key,
            "instructions": "replay the same key after the host lease expired",
        })
        assert replay.status_code == 200, replay.text
        assert replay.json()["attempt_id"] == orphan_id
        assert adapter.calls == 0
        assert [event for event in provider.events if event[0] == "create"] == [
            ("create", sandbox_a, orphan_id, ()),
        ]
        if provider.still_executing(sandbox_a):
            provider.emit(sandbox_a, "still-executing-after-recovery")
        retry = restarted.post(f"/tasks/{task_id}/run", json={
            "fixture_dir": str(fixture),
            "dispatch_key": "dispatch-remote-overlap-2",
            "parent_attempt_id": orphan_id,
            "instructions": "retry with a new explicit dispatch key",
        })

    created_while_a_running = [
        event for event in provider.events
        if event[0] == "create" and sandbox_a in event[3]
    ]
    later = [event for event in provider.events if event[0] == "create" and event[1] != sandbox_a]
    db = SessionLocal()
    try:
        rows = [
            (row.id, row.status, row.dispatch_key, row.parent_attempt_id, row.error_class)
            for row in db.query(AttemptRow).filter_by(task_id=task_id)
        ]
    finally:
        db.close()
    overlap = (
        "a new explicit dispatch_key started a provider sandbox while sandbox "
        f"{sandbox_a} was still executing. Lease recovery must confirm that remote "
        "work is terminated before the next sandbox is created, or reject the new "
        "dispatch while the first sandbox is still running. "
        f"events={provider.events!r} attempts={rows!r} retry={retry.status_code} {retry.text}"
    )
    assert not created_while_a_running, overlap
    if not later:
        assert provider.still_executing(sandbox_a), (
            "the new dispatch did not start a sandbox and sandbox A is not executing, "
            "so this was not the overlap contract. "
            f"events={provider.events!r} attempts={rows!r} retry={retry.status_code} {retry.text}"
        )
        assert 400 <= retry.status_code < 500, retry.text


def test_recovery_does_not_fail_a_lease_renewed_after_the_select(tmp_path: Path, monkeypatch) -> None:
    """A heartbeat committed after the expired SELECT must not be failed.

    Recovery loads running rows and decides liveness from that snapshot. The
    UPDATE has to require that same ``executor_id`` and ``executor_heartbeat_at``.
    A renewal committed after the snapshot must make the update miss.

    ``test_recovery_does_not_overwrite_a_completed_attempt`` commits a terminal
    status in this window. ``test_heartbeat_refresh_does_not_touch_a_failed_attempt``
    only covers a refresh that runs after the row is already failed.

    The hook below is the renewal race: the real ``executor_is_live`` sees the
    expired snapshot, another session commits a fresh heartbeat, and the stale
    decision is what recovery would act on. The UPDATE must then lose, and the
    Task must stay running. The hermetic database is one shared connection, so
    this injects the interleaving the way the terminal-status race does instead
    of using a skippable PostgreSQL barrier.
    """

    monkeypatch.chdir(tmp_path)
    attempt_id = "ATTEMPT-RENEWED-LEASE"
    task_id = "TASK-RENEWED-LEASE"
    expired = utcnow() - LEASE_TTL - timedelta(seconds=1)
    db = SessionLocal()
    try:
        _running_attempt(
            db, task_id, attempt_id, "dispatch-renewed-lease",
            executor_id="executor-still-working", executor_heartbeat_at=expired,
        )
        db.commit()
    finally:
        db.close()

    from sdf_core import recovery as recovery_mod

    real_live = recovery_mod.executor_is_live
    renewed: list = []

    def renew_after_expired_snapshot(attempt, *, now=None):
        decision = real_live(attempt, now=now)
        if attempt.id != attempt_id:
            return decision
        assert decision is False, "the SELECT snapshot must look expired before the renewal commits"
        other = SessionLocal()
        try:
            row = other.get(AttemptRow, attempt.id)
            row.executor_heartbeat_at = utcnow()
            other.commit()
            renewed.append(row.executor_heartbeat_at)
        finally:
            other.close()
        return decision

    monkeypatch.setattr(recovery_mod, "executor_is_live", renew_after_expired_snapshot)
    db = SessionLocal()
    try:
        settled = recover_orphaned_attempts(db)
    finally:
        db.close()

    db = SessionLocal()
    try:
        attempt = db.get(AttemptRow, attempt_id)
        task = db.get(TaskRow, task_id)
        assert attempt is not None and task is not None and renewed
        # SQLite returns the column without tzinfo; both instants are UTC.
        stored_heartbeat = attempt.executor_heartbeat_at
        if stored_heartbeat.tzinfo is None:
            stored_heartbeat = stored_heartbeat.replace(tzinfo=timezone.utc)
        assert stored_heartbeat == renewed[0]
        assert attempt_id not in settled and attempt.status == "running" and task.status == "running", (
            "recovery failed a lease that was renewed after the expired SELECT and before "
            f"the UPDATE (settled={settled!r}, status={attempt.status!r}, "
            f"error_class={attempt.error_class!r}, heartbeat={attempt.executor_heartbeat_at!r}, "
            f"task={task.status!r})"
        )
    finally:
        db.close()


def test_live_attempt_is_not_reaped_or_retried_without_a_confirmed_stop(
    tmp_path: Path, monkeypatch,
) -> None:
    """A live Attempt stays running when this process cannot confirm the sandbox stopped.

    No provider kill is attempted. Duplicate recovery does not settle it, the
    same dispatch key replays it, and a new dispatch is refused.
    """

    monkeypatch.chdir(tmp_path)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    adapter = CountingAdapter()
    monkeypatch.setattr("sdf_core.api.configured_run_adapter", lambda _db: adapter)
    task_id = "TASK-LIVE-UNCONFIRMED"
    attempt_id = "ATTEMPT-LIVE-UNCONFIRMED"
    dispatch_key = "dispatch-live-unconfirmed"
    db = SessionLocal()
    try:
        _running_attempt(
            db, task_id, attempt_id, dispatch_key,
            executor_id="dead-live-executor",
            executor_heartbeat_at=utcnow() - LEASE_TTL - timedelta(seconds=5),
            evidence_mode="live",
        )
        db.commit()
    finally:
        db.close()

    db = SessionLocal()
    try:
        assert recover_orphaned_attempts(db) == ()
        assert recover_orphaned_attempts(db) == ()
    finally:
        db.close()
    db = SessionLocal()
    try:
        assert db.get(AttemptRow, attempt_id).status == "running"
        assert db.get(AttemptRow, attempt_id).error_class is None
        assert db.get(TaskRow, task_id).status == "running"
    finally:
        db.close()

    with admin_client() as client:
        replay = client.post(f"/tasks/{task_id}/run", json={
            "fixture_dir": str(fixture), "dispatch_key": dispatch_key, "instructions": "same key",
        })
        assert replay.status_code == 200, replay.text
        assert replay.json()["attempt_id"] == attempt_id
        assert adapter.calls == 0
        retry = client.post(f"/tasks/{task_id}/run", json={
            "fixture_dir": str(fixture),
            "dispatch_key": "dispatch-live-unconfirmed-2",
            "parent_attempt_id": attempt_id,
            "instructions": "new key while the live sandbox is unconfirmed",
        })
        assert 400 <= retry.status_code < 500, retry.text
        assert "still running" in retry.text
        assert adapter.calls == 0
    db = SessionLocal()
    try:
        assert db.get(AttemptRow, attempt_id).status == "running"
        assert db.query(AttemptRow).filter_by(task_id=task_id).count() == 1
    finally:
        db.close()


def test_termination_failure_leaves_the_attempt_running(tmp_path: Path, monkeypatch) -> None:
    """A provider that fails or refuses to confirm stop blocks settle and a new dispatch."""

    monkeypatch.chdir(tmp_path)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    task_id = "TASK-STOP-FAILED"
    attempt_id = "ATTEMPT-STOP-FAILED"
    db = SessionLocal()
    service = ExecutionService(
        db, workspace_root=tmp_path / ".sdf-workspaces", artifact_root=tmp_path / ".sdf-artifacts",
    )
    try:
        db.add(TaskRow(
            id=task_id, title="stop failed", status="created", idempotency_key=task_id,
            acceptance_criteria=[], created_at=utcnow(),
        ))
        db.commit()
        begun = service.begin(task_id=task_id, dispatch_key="dispatch-stop-failed", fixture=fixture)
        attempt_id = begun.attempt.id
    finally:
        db.close()

    def confirm_stopped() -> bool:
        raise RuntimeError("provider unavailable")

    register_remote_effect(attempt_id, confirm_stopped)
    try:
        del begun
        del service
        gc.collect()
        reset_recovery_state()
        side = SessionLocal()
        try:
            _expire_heartbeat(side, attempt_id)
            assert recover_orphaned_attempts(side) == ()
            assert recover_orphaned_attempts(side) == ()
        finally:
            side.close()
        check = SessionLocal()
        try:
            assert check.get(AttemptRow, attempt_id).status == "running"
            assert check.get(TaskRow, task_id).status == "running"
        finally:
            check.close()
        again = SessionLocal()
        try:
            refused = ExecutionService(
                again, workspace_root=tmp_path / ".sdf-workspaces", artifact_root=tmp_path / ".sdf-artifacts",
            )
            with pytest.raises(ValueError, match="still running"):
                refused.begin(
                    task_id=task_id, dispatch_key="dispatch-stop-failed-2", fixture=fixture,
                    parent_attempt_id=attempt_id,
                )
            assert again.get(AttemptRow, attempt_id).status == "running"
            assert again.query(AttemptRow).filter_by(task_id=task_id).count() == 1
        finally:
            again.close()
    finally:
        clear_remote_effect(attempt_id)


def test_confirmed_stop_allows_settle_and_a_new_dispatch(tmp_path: Path, monkeypatch) -> None:
    """Once the registered effect reports it has stopped, lease expiry can settle and retry."""

    monkeypatch.chdir(tmp_path)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    adapter = CountingAdapter()
    monkeypatch.setattr("sdf_core.api.configured_run_adapter", lambda _db: adapter)
    task_id = "TASK-STOP-CONFIRMED"
    attempt_id = "ATTEMPT-STOP-CONFIRMED"
    db = SessionLocal()
    try:
        _running_attempt(
            db, task_id, attempt_id, "dispatch-stop-confirmed",
            executor_id="dead-after-stop",
            executor_heartbeat_at=utcnow() - LEASE_TTL - timedelta(seconds=5),
            evidence_mode="live",
        )
        db.commit()
    finally:
        db.close()
    register_remote_effect(attempt_id, lambda: True)
    try:
        side = SessionLocal()
        try:
            assert attempt_id in recover_orphaned_attempts(side)
            assert recover_orphaned_attempts(side) == ()
        finally:
            side.close()
    finally:
        clear_remote_effect(attempt_id)
    db = SessionLocal()
    try:
        assert db.get(AttemptRow, attempt_id).status == "failed"
        assert db.get(AttemptRow, attempt_id).error_class == "killed"
        assert db.get(TaskRow, task_id).status == "failed"
    finally:
        db.close()
    with admin_client() as client:
        retry = client.post(f"/tasks/{task_id}/run", json={
            "fixture_dir": str(fixture),
            "dispatch_key": "dispatch-stop-confirmed-2",
            "parent_attempt_id": attempt_id,
            "instructions": "retry after the effect was confirmed stopped",
        })
        assert retry.status_code == 200, retry.text
        assert retry.json()["attempt_id"] != attempt_id
        assert adapter.calls == 1


def test_stale_heartbeat_with_the_owner_still_alive_refuses_a_new_dispatch(
    tmp_path: Path, monkeypatch,
) -> None:
    """Lease expiry does not reap an Attempt whose executor object is still held."""

    monkeypatch.chdir(tmp_path)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    task_id = "TASK-OWNER-STILL-ALIVE"
    db = SessionLocal()
    service = ExecutionService(
        db, workspace_root=tmp_path / ".sdf-workspaces", artifact_root=tmp_path / ".sdf-artifacts",
    )
    try:
        db.add(TaskRow(
            id=task_id, title="owner alive", status="created", idempotency_key=task_id,
            acceptance_criteria=[], created_at=utcnow(),
        ))
        db.commit()
        begun = service.begin(task_id=task_id, dispatch_key="dispatch-owner-alive", fixture=fixture)
        attempt_id = begun.attempt.id
        db.get(AttemptRow, attempt_id).executor_heartbeat_at = utcnow() - LEASE_TTL - timedelta(seconds=5)
        db.commit()
        assert attempt_id not in recover_orphaned_attempts(db, now=utcnow())
        with pytest.raises(ValueError, match="still running"):
            service.begin(task_id=task_id, dispatch_key="dispatch-owner-alive-2", fixture=fixture)
        assert db.get(AttemptRow, attempt_id).status == "running"
        assert db.query(AttemptRow).filter_by(task_id=task_id).count() == 1
    finally:
        db.close()
        del service


def test_heartbeat_refresh_does_not_touch_a_failed_attempt(tmp_path: Path) -> None:
    from sdf_core.recovery import EXECUTOR_ID, _owners, _write_heartbeats

    db = SessionLocal()
    attempt_id = "ATTEMPT-HB-FAILED"
    try:
        db.add(TaskRow(
            id="TASK-HB-FAILED", title="hb", status="failed", idempotency_key="task-hb-failed",
            acceptance_criteria=[], created_at=utcnow(),
        ))
        stamp = utcnow() - timedelta(seconds=5)
        db.add(AttemptRow(
            id=attempt_id, task_id="TASK-HB-FAILED", status="failed", dispatch_key="dispatch-hb-failed",
            agent="native", created_at=utcnow(), executor_id=EXECUTOR_ID, executor_heartbeat_at=stamp,
        ))
        db.commit()
        stored = db.get(AttemptRow, attempt_id).executor_heartbeat_at

        class _Hold:
            pass

        holder = _Hold()
        _owners[attempt_id] = holder
        _write_heartbeats(db)
        db.expire_all()
        assert db.get(AttemptRow, attempt_id).executor_heartbeat_at == stored
        assert db.get(AttemptRow, attempt_id).status == "failed"
    finally:
        _owners.pop(attempt_id, None)
        db.close()
