"""Host crash between begin and finish, and the dispatch-key fence.

Production startup is the FastAPI lifespan on ``sdf_core.api.app``. That startup
fails an Attempt left ``running`` when its executor lease is dead, and moves the
Task ``running`` → ``evaluating`` → ``failed`` so a new dispatch key can enter
``ready``. ``RuntimeController.recover`` is not part of this path (ADR 0029).

A missing ``dispatch_key`` is rejected by ``POST /run`` (ADR 0031). These tests
keep the crash regression and do not treat a keyless retry as a second success.
"""

from __future__ import annotations

import gc
import os
import threading
from concurrent.futures import ThreadPoolExecutor
from datetime import timedelta
from pathlib import Path

import pytest
from sqlalchemy import create_engine, event, func, select
from sqlalchemy.orm import sessionmaker

from sdf_core.adapter import AdapterResult, FakeNativeAdapter
from sdf_core.api import SessionLocal
from sdf_core.db import ArtifactRow, AttemptRow, Base, EvidenceRow, GraphNodeRow, TaskRow
from sdf_core.execution import ExecutionService
from sdf_core.model import utcnow
from sdf_core.recovery import (
    LEASE_TTL,
    AttemptFenceError,
    cancel_running_attempt,
    recover_orphaned_attempts,
    release_executor,
    reset_recovery_state,
    settle_task_if_idle,
)
from sdf_core.state import TaskState
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
        self._lock = threading.Lock()
        self._fake = FakeNativeAdapter("app.py", "print('new')\n")

    def run(self, *, attempt_id: str, workspace: Path, instructions: str):
        with self._lock:
            self.calls += 1
        return self._fake.run(attempt_id=attempt_id, workspace=workspace, instructions=instructions)


def _expire_heartbeat(db, attempt_id: str) -> None:
    attempt = db.get(AttemptRow, attempt_id)
    attempt.executor_heartbeat_at = utcnow() - LEASE_TTL - timedelta(seconds=1)
    db.commit()


def test_host_crash_mid_run_is_recovered_and_task_is_retryable(tmp_path: Path, monkeypatch) -> None:
    """An Attempt abandoned between begin and finish is retryable after startup.

    The process is modeled as dying after ``begin`` has committed the Attempt
    and the Task ``running`` and before the adapter runs: the service is
    dropped, and the durable rows are what a new request would read. The
    restarted host is this app entered through its lifespan. The orphan is
    then failed, and a new dispatch key creates a new Attempt and runs it.
    Replaying the crashed key does not run the adapter again.
    """

    monkeypatch.chdir(tmp_path)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    adapter = CountingAdapter()
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
        assert begun.attempt.executor_id
        assert db.get(TaskRow, task_id).status == "running"
        assert adapter.calls == 0
        graph_before = db.query(GraphNodeRow).filter_by(id=orphan_id).count()
        evidence_before = db.query(EvidenceRow).filter_by(attempt_id=orphan_id).count()
    finally:
        db.close()
    # In-memory execution state dies with the process. Do not call finish,
    # adapter.run, or RuntimeController.recover: production startup does not.
    del begun
    del service
    gc.collect()

    with admin_client() as restarted:
        same_key = restarted.post(f"/tasks/{task_id}/run", json={
            "fixture_dir": str(fixture),
            "dispatch_key": "dispatch-host-crash-1",
            "instructions": "replay the crashed key",
        })
        assert same_key.status_code == 200, same_key.text
        assert same_key.json()["attempt_id"] == orphan_id
        assert adapter.calls == 0
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
        assert orphan.error_class == "killed"
        assert db.query(GraphNodeRow).filter_by(id=orphan_id).count() == graph_before
        assert db.query(EvidenceRow).filter_by(attempt_id=orphan_id).count() == evidence_before == 0
        assert same_key.status_code == 200, same_key.text
        assert same_key.json()["attempt_id"] == orphan_id
        assert retry.status_code == 200, retry.text
        body = retry.json()
        assert body["attempt_id"] != orphan_id
        assert adapter.calls == 1
        started = db.get(AttemptRow, body["attempt_id"])
        assert started is not None and started.task_id == task_id
        assert started.status != "running"
    finally:
        db.close()


def test_startup_recovery_is_idempotent(tmp_path: Path, monkeypatch) -> None:
    """Settling the same orphan twice does not move it again."""

    monkeypatch.chdir(tmp_path)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    db = SessionLocal()
    try:
        db.add(TaskRow(
            id="TASK-RECOVER-ONCE", title="once", status="created", idempotency_key="task-recover-once",
            acceptance_criteria=[], created_at=utcnow(),
        ))
        db.commit()
        service = ExecutionService(
            db, workspace_root=tmp_path / ".sdf-workspaces", artifact_root=tmp_path / ".sdf-artifacts",
        )
        orphan_id = service.begin(task_id="TASK-RECOVER-ONCE", dispatch_key="dispatch-recover-once", fixture=fixture).attempt.id
        release_executor(orphan_id)
        assert recover_orphaned_attempts(db) == (orphan_id,)
        attempt = db.get(AttemptRow, orphan_id)
        task = db.get(TaskRow, "TASK-RECOVER-ONCE")
        snapshot = (
            attempt.status, attempt.error_class, task.status,
            attempt.executor_id, attempt.executor_heartbeat_at,
            db.query(EvidenceRow).filter_by(attempt_id=orphan_id).count(),
        )
        assert snapshot[:3] == ("failed", "killed", "failed")
        assert recover_orphaned_attempts(db) == ()
        attempt = db.get(AttemptRow, orphan_id)
        task = db.get(TaskRow, "TASK-RECOVER-ONCE")
        assert (
            attempt.status, attempt.error_class, task.status,
            attempt.executor_id, attempt.executor_heartbeat_at,
            db.query(EvidenceRow).filter_by(attempt_id=orphan_id).count(),
        ) == snapshot
    finally:
        db.close()
        del service
        gc.collect()


def test_live_executor_is_not_reaped_and_unleased_rows_stay(tmp_path: Path, monkeypatch) -> None:
    """A held claim and a fresh foreign heartbeat stay running. A stale one does not.

    A running row with no executor id is not swept.
    """

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

    def _running(task_id: str, attempt_id: str, key: str, **fields) -> None:
        db.add(TaskRow(
            id=task_id, title=task_id, status="running", idempotency_key=task_id,
            acceptance_criteria=[], created_at=utcnow(),
        ))
        db.add(AttemptRow(
            id=attempt_id, task_id=task_id, status="running", dispatch_key=key,
            agent="native", created_at=utcnow(), **fields,
        ))

    _running(
        "TASK-LIVE-FOREIGN", "ATTEMPT-LIVE-FOREIGN", "dispatch-live-foreign",
        executor_id="other-executor", executor_heartbeat_at=utcnow(),
    )
    _running(
        "TASK-STALE-FOREIGN", "ATTEMPT-STALE-FOREIGN", "dispatch-stale-foreign",
        executor_id="other-executor",
        executor_heartbeat_at=utcnow() - LEASE_TTL - timedelta(seconds=1),
    )
    _running("TASK-UNLEASED", "ATTEMPT-UNLEASED", "dispatch-unleased")
    db.commit()

    with admin_client():
        pass
    db.expire_all()
    assert db.get(AttemptRow, live_id).status == "running"
    assert db.get(TaskRow, "TASK-LIVE-HELD").status == "running"
    assert db.get(AttemptRow, "ATTEMPT-LIVE-FOREIGN").status == "running"
    assert db.get(TaskRow, "TASK-LIVE-FOREIGN").status == "running"
    assert db.get(AttemptRow, "ATTEMPT-STALE-FOREIGN").status == "failed"
    assert db.get(AttemptRow, "ATTEMPT-STALE-FOREIGN").error_class == "killed"
    assert db.get(TaskRow, "TASK-STALE-FOREIGN").status == "failed"
    assert db.get(AttemptRow, "ATTEMPT-UNLEASED").status == "running"
    assert db.get(TaskRow, "TASK-UNLEASED").status == "running"
    # The held Attempt can still be finished by its owner.
    finished = service.finish(
        attempt_id=live_id,
        result=AdapterResult(attempt_id=live_id, status="succeeded", changed_files=(), exit_code=0,
                             stdout="", stderr=""),
        fixture=fixture, commands=[],
    )
    assert finished.status == "completed"
    db.close()


def test_restarted_process_does_not_reap_a_fresh_foreign_heartbeat(tmp_path: Path, monkeypatch) -> None:
    """A new executor id treats a fresh heartbeat as a live neighbor."""

    monkeypatch.chdir(tmp_path)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    db = SessionLocal()
    try:
        db.add(TaskRow(
            id="TASK-RESTART-FRESH", title="fresh", status="created", idempotency_key="task-restart-fresh",
            acceptance_criteria=[], created_at=utcnow(),
        ))
        db.commit()
        service = ExecutionService(
            db, workspace_root=tmp_path / ".sdf-workspaces", artifact_root=tmp_path / ".sdf-artifacts",
        )
        attempt_id = service.begin(
            task_id="TASK-RESTART-FRESH", dispatch_key="dispatch-restart-fresh", fixture=fixture,
        ).attempt.id
        reset_recovery_state()
        assert recover_orphaned_attempts(db) == ()
        assert db.get(AttemptRow, attempt_id).status == "running"
        _expire_heartbeat(db, attempt_id)
        assert recover_orphaned_attempts(db) == (attempt_id,)
        assert db.get(AttemptRow, attempt_id).status == "failed"
        assert db.get(TaskRow, "TASK-RESTART-FRESH").status == "failed"
    finally:
        db.close()
        del service
        gc.collect()


def test_late_finish_after_recovery_does_not_write(tmp_path: Path) -> None:
    """A finish that lost the race leaves the recovered rows and no Evidence."""

    engine = create_engine(f"sqlite:///{tmp_path / 'fence.sqlite'}")
    Base.metadata.create_all(engine)
    factory = sessionmaker(engine, expire_on_commit=False)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    db = factory()
    db.add(TaskRow(
        id="TASK-LATE", title="late", status="created", idempotency_key="task-late",
        acceptance_criteria=[], created_at=utcnow(),
    ))
    db.commit()
    service = ExecutionService(
        db, workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art",
    )
    attempt_id = service.begin(task_id="TASK-LATE", dispatch_key="dispatch-late", fixture=fixture).attempt.id
    release_executor(attempt_id)
    other = factory()
    try:
        assert recover_orphaned_attempts(other) == (attempt_id,)
    finally:
        other.close()
    with pytest.raises(AttemptFenceError):
        service.finish(
            attempt_id=attempt_id,
            result=AdapterResult(attempt_id=attempt_id, status="succeeded", changed_files=(), exit_code=0,
                                 stdout="done", stderr=""),
            fixture=fixture, commands=[],
        )
    db.expire_all()
    attempt = db.get(AttemptRow, attempt_id)
    assert attempt.status == "failed" and attempt.error_class == "killed"
    assert db.get(TaskRow, "TASK-LATE").status == "failed"
    assert db.query(EvidenceRow).filter_by(attempt_id=attempt_id).count() == 0
    assert db.query(ArtifactRow).filter_by(attempt_id=attempt_id).count() == 0
    db.close()


def test_cancel_and_recovery_leave_one_terminal_state(tmp_path: Path) -> None:
    """Whichever writer wins, the Attempt and Task stay on one legal terminal pair."""

    engine = create_engine(
        f"sqlite:///{tmp_path / 'race.sqlite'}", connect_args={"timeout": 30},
    )

    @event.listens_for(engine, "connect")
    def _pragmas(connection, _record):
        connection.execute("PRAGMA journal_mode=WAL")
        connection.execute("PRAGMA busy_timeout=30000")

    Base.metadata.create_all(engine)
    factory = sessionmaker(engine, expire_on_commit=False)
    with factory() as db:
        db.add(TaskRow(
            id="TASK-RACE", title="race", status="running", idempotency_key="task-race",
            acceptance_criteria=[], created_at=utcnow(),
        ))
        db.add(AttemptRow(
            id="ATTEMPT-RACE", task_id="TASK-RACE", status="running", dispatch_key="dispatch-race",
            agent="native", created_at=utcnow(), executor_id="dead-executor",
            executor_heartbeat_at=utcnow() - LEASE_TTL - timedelta(seconds=5),
        ))
        db.commit()
    barrier = threading.Barrier(2)

    def _recover():
        barrier.wait(timeout=10)
        db = factory()
        try:
            recover_orphaned_attempts(db)
        finally:
            db.close()

    def _cancel():
        barrier.wait(timeout=10)
        db = factory()
        try:
            attempt = db.get(AttemptRow, "ATTEMPT-RACE")
            if cancel_running_attempt(db, attempt):
                settle_task_if_idle(db, "TASK-RACE", TaskState.INCONCLUSIVE)
            db.commit()
        finally:
            db.close()

    with ThreadPoolExecutor(max_workers=2) as pool:
        list(pool.map(lambda fn: fn(), (_recover, _cancel)))
    with factory() as db:
        attempt = db.get(AttemptRow, "ATTEMPT-RACE")
        task = db.get(TaskRow, "TASK-RACE")
        assert attempt.status in {"failed", "cancelled"}
        assert (attempt.status, task.status) in {("failed", "failed"), ("cancelled", "inconclusive")}
        assert db.query(EvidenceRow).filter_by(attempt_id="ATTEMPT-RACE").count() == 0
        # A second pass writes nothing.
        assert recover_orphaned_attempts(db) == ()
        assert db.get(AttemptRow, "ATTEMPT-RACE").status == attempt.status


def test_concurrent_same_dispatch_key_runs_the_adapter_once(tmp_path: Path) -> None:
    """Two overlapping runs of one key leave one Attempt and one adapter call."""

    engine = create_engine(
        f"sqlite:///{tmp_path / 'same-key.sqlite'}", connect_args={"timeout": 30},
    )

    @event.listens_for(engine, "connect")
    def _pragmas(connection, _record):
        connection.execute("PRAGMA journal_mode=WAL")
        connection.execute("PRAGMA busy_timeout=30000")

    Base.metadata.create_all(engine)
    factory = sessionmaker(engine, expire_on_commit=False)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    task_id = "TASK-SAME-KEY"
    with factory() as db:
        db.add(TaskRow(
            id=task_id, title="same", status="created", idempotency_key="task-same-key",
            acceptance_criteria=[], created_at=utcnow(),
        ))
        db.commit()
    adapter = CountingAdapter()
    barrier = threading.Barrier(2)

    def once():
        barrier.wait(timeout=10)
        db = factory()
        try:
            service = ExecutionService(
                db, workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art", adapter=adapter,
            )
            return service.run(
                task_id=task_id, dispatch_key="dispatch-same", fixture=fixture,
                instructions="once", commands=[],
            ).id
        finally:
            db.close()

    with ThreadPoolExecutor(max_workers=2) as pool:
        ids = list(pool.map(lambda _index: once(), (0, 1)))
    assert ids[0] == ids[1]
    assert adapter.calls == 1
    with factory() as db:
        assert db.query(AttemptRow).filter_by(task_id=task_id).count() == 1


def test_sqlite_and_postgres_dead_lease_compare_and_swap(tmp_path: Path) -> None:
    """The dead-lease update matches SQLite and, when configured, PostgreSQL."""

    urls = [f"sqlite:///{tmp_path / 'cas.sqlite'}"]
    postgres = os.getenv("SDF_POSTGRES_TEST_URL")
    if postgres:
        urls.append(postgres)
    else:
        urls.append(None)

    for url in urls:
        if url is None:
            continue
        _assert_dialect_cas(url, suffix="sqlite" if url.startswith("sqlite") else "pg")


def _assert_dialect_cas(url: str, *, suffix: str) -> None:
    engine = create_engine(url)
    if url.startswith("sqlite"):
        Base.metadata.create_all(engine)
    factory = sessionmaker(engine, expire_on_commit=False)
    task_id = f"TASK-CAS-{suffix}"
    attempt_id = f"ATTEMPT-CAS-{suffix}"
    key = f"dispatch-cas-{suffix}"
    with factory() as db:
        for model, ident in ((AttemptRow, attempt_id), (TaskRow, task_id), (GraphNodeRow, attempt_id)):
            row = db.get(model, ident)
            if row is not None:
                db.delete(row)
        db.commit()
        db.add(TaskRow(
            id=task_id, title="cas", status="running", idempotency_key=f"idem-{suffix}-{os.urandom(4).hex()}",
            acceptance_criteria=[], created_at=utcnow(),
        ))
        db.add(AttemptRow(
            id=attempt_id, task_id=task_id, status="running", dispatch_key=key,
            agent="native", created_at=utcnow(), executor_id=f"dead-{suffix}",
            executor_heartbeat_at=utcnow() - LEASE_TTL - timedelta(seconds=5),
            evidence_mode="synthetic",
        ))
        db.add(GraphNodeRow(
            id=attempt_id, kind="attempt", title="kept", source="test", owner="sdf",
            confidence=1.0, created_at=utcnow(),
        ))
        db.commit()
        evidence_before = db.scalar(
            select(func.count()).select_from(EvidenceRow).where(EvidenceRow.attempt_id == attempt_id)
        )
        assert recover_orphaned_attempts(db) == (attempt_id,)
        attempt = db.get(AttemptRow, attempt_id)
        task = db.get(TaskRow, task_id)
        assert attempt.status == "failed" and attempt.error_class == "killed"
        assert task.status == "failed"
        assert db.get(GraphNodeRow, attempt_id) is not None
        assert db.scalar(
            select(func.count()).select_from(EvidenceRow).where(EvidenceRow.attempt_id == attempt_id)
        ) == evidence_before
        assert recover_orphaned_attempts(db) == ()
        db.delete(attempt)
        db.delete(task)
        db.delete(db.get(GraphNodeRow, attempt_id))
        db.commit()
    engine.dispose()


def test_postgres_cas_runs_when_the_url_is_set() -> None:
    if not os.getenv("SDF_POSTGRES_TEST_URL"):
        pytest.skip("SDF_POSTGRES_TEST_URL is not set")
    # The assertion above already ran the PostgreSQL URL. This name exists so
    # the skip is visible when the URL is unset, and the body is the same CAS.
    _assert_dialect_cas(os.environ["SDF_POSTGRES_TEST_URL"], suffix="pg-explicit")
