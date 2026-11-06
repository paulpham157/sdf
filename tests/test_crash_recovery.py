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
    # Dropping the service is not process death. A decision gate and a
    # request ending both drop it while the Attempt is still running.
    del begun
    del service
    gc.collect()
    with admin_client() as restarted:
        same_key = restarted.post(f"/tasks/{task_id}/run", json={
            "fixture_dir": str(fixture),
            "dispatch_key": "dispatch-host-crash-1",
            "instructions": "replay while the process is alive",
        })
        other_key = restarted.post(f"/tasks/{task_id}/run", json={
            "fixture_dir": str(fixture),
            "dispatch_key": "dispatch-host-crash-2",
            "instructions": "second key while the first is running",
        })
    db = SessionLocal()
    try:
        orphan = db.get(AttemptRow, orphan_id)
        assert orphan.status == "running"
        assert orphan.error_class is None
        assert db.query(GraphNodeRow).filter_by(id=orphan_id).count() == graph_before
        assert db.query(EvidenceRow).filter_by(attempt_id=orphan_id).count() == evidence_before == 0
        assert same_key.status_code == 200, same_key.text
        assert same_key.json()["attempt_id"] == orphan_id
        assert other_key.status_code == 409, other_key.text
        assert adapter.calls == 0
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
        db.add(AttemptRow(
            id="ATTEMPT-RECOVER-ONCE", task_id="TASK-RECOVER-ONCE", status="running",
            dispatch_key="dispatch-recover-once", agent="native", created_at=utcnow(),
            executor_id="dead-foreign", executor_heartbeat_at=utcnow() - LEASE_TTL - timedelta(seconds=5),
        ))
        db.get(TaskRow, "TASK-RECOVER-ONCE").status = "running"
        db.commit()
        orphan_id = "ATTEMPT-RECOVER-ONCE"
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
    row = db.get(AttemptRow, attempt_id)
    row.executor_id = "dead-other"
    row.executor_heartbeat_at = utcnow() - LEASE_TTL - timedelta(seconds=5)
    db.commit()
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


def test_stale_heartbeat_does_not_reap_a_process_that_holds_the_lock(tmp_path: Path) -> None:
    """A live OS process keeps its Attempt even when the heartbeat is old.

    When that process exits, the lock goes with it. Recovery then fails the
    Attempt even if the heartbeat is fresh. A new key is a second adapter
    call; the same key is not. Process death does not prove a remote sandbox
    has exited.
    """

    import subprocess
    import sys

    from sdf_core.recovery import executor_lease_path

    engine = create_engine(f"sqlite:///{tmp_path / 'lock.sqlite'}", connect_args={"timeout": 30})
    Base.metadata.create_all(engine)
    factory = sessionmaker(engine, expire_on_commit=False)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    executor_id = "live-other-process"
    path = executor_lease_path(engine, executor_id)
    assert path is not None
    path.parent.mkdir(parents=True, exist_ok=True)
    holder = subprocess.Popen(
        [sys.executable, "-c",
         "import fcntl,sys\n"
         "fh=open(sys.argv[1],'a+')\n"
         "fcntl.flock(fh.fileno(), fcntl.LOCK_EX)\n"
         "print('ready', flush=True)\n"
         "sys.stdin.read()\n",
         str(path)],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True,
    )
    try:
        assert holder.stdout.readline().strip() == "ready"
        with factory() as db:
            db.add(TaskRow(
                id="TASK-LOCK", title="lock", status="running", idempotency_key="task-lock",
                acceptance_criteria=[], created_at=utcnow(),
            ))
            db.add(AttemptRow(
                id="ATTEMPT-LOCK", task_id="TASK-LOCK", status="running", dispatch_key="dispatch-lock",
                agent="native", created_at=utcnow(), executor_id=executor_id,
                executor_heartbeat_at=utcnow() - LEASE_TTL - timedelta(seconds=60),
            ))
            db.commit()
            assert recover_orphaned_attempts(db) == ()
            assert db.get(AttemptRow, "ATTEMPT-LOCK").status == "running"
            # A heartbeat refresh committing while recovery runs still leaves
            # the locked Attempt running. The lock, not the timestamp, wins.
            attempt = db.get(AttemptRow, "ATTEMPT-LOCK")
            attempt.executor_heartbeat_at = utcnow()
            db.commit()
            assert recover_orphaned_attempts(db) == ()
            assert db.get(AttemptRow, "ATTEMPT-LOCK").status == "running"
    finally:
        if holder.poll() is None:
            holder.stdin.close()
            holder.wait(timeout=5)
    with factory() as db:
        attempt = db.get(AttemptRow, "ATTEMPT-LOCK")
        attempt.executor_heartbeat_at = utcnow()
        db.commit()
        assert recover_orphaned_attempts(db) == ("ATTEMPT-LOCK",)
        assert db.get(AttemptRow, "ATTEMPT-LOCK").status == "failed"
        assert db.get(TaskRow, "TASK-LOCK").status == "failed"
        adapter = CountingAdapter()
        service = ExecutionService(
            db, workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art", adapter=adapter,
        )
        replay = service.run(
            task_id="TASK-LOCK", dispatch_key="dispatch-lock", fixture=fixture,
            instructions="same key", commands=[],
        )
        assert replay.id == "ATTEMPT-LOCK" and adapter.calls == 0
        fresh = service.run(
            task_id="TASK-LOCK", dispatch_key="dispatch-lock-new", fixture=fixture,
            instructions="new key", commands=[],
        )
        assert fresh.id != "ATTEMPT-LOCK" and adapter.calls == 1


def test_unleased_rows_stay_until_the_operator_asks(tmp_path: Path, monkeypatch) -> None:
    """A null executor id is not a dead lease. The opt-in does not touch a held one."""

    engine = create_engine(f"sqlite:///{tmp_path / 'unleased.sqlite'}")
    Base.metadata.create_all(engine)
    factory = sessionmaker(engine, expire_on_commit=False)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    with factory() as db:
        db.add(TaskRow(
            id="TASK-HELD", title="held", status="created", idempotency_key="task-held-u",
            acceptance_criteria=[], created_at=utcnow(),
        ))
        db.commit()
        service = ExecutionService(
            db, workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art",
        )
        held_id = service.begin(task_id="TASK-HELD", dispatch_key="dispatch-held-u", fixture=fixture).attempt.id
        db.add(TaskRow(
            id="TASK-OLD", title="old", status="running", idempotency_key="task-old-u",
            acceptance_criteria=[], created_at=utcnow(),
        ))
        db.add(AttemptRow(
            id="ATTEMPT-OLD", task_id="TASK-OLD", status="running", dispatch_key="dispatch-old-u",
            agent="native", created_at=utcnow(),
        ))
        db.commit()
        monkeypatch.delenv("SDF_RECOVER_UNLEASED_ATTEMPTS", raising=False)
        assert recover_orphaned_attempts(db) == ()
        assert db.get(AttemptRow, "ATTEMPT-OLD").status == "running"
        assert db.get(AttemptRow, held_id).status == "running"
        monkeypatch.setenv("SDF_RECOVER_UNLEASED_ATTEMPTS", "1")
        assert recover_orphaned_attempts(db) == ("ATTEMPT-OLD",)
        assert db.get(AttemptRow, "ATTEMPT-OLD").status == "failed"
        assert db.get(TaskRow, "TASK-OLD").status == "failed"
        assert db.get(AttemptRow, held_id).status == "running"
        assert db.get(TaskRow, "TASK-HELD").status == "running"


def test_same_key_from_two_connections_runs_once(tmp_path: Path) -> None:
    """Two database sessions, as two hosts would share, still make one Attempt."""

    engine = create_engine(
        f"sqlite:///{tmp_path / 'cross.sqlite'}", connect_args={"timeout": 30},
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
    with factory() as db:
        db.add(TaskRow(
            id="TASK-CROSS", title="cross", status="created", idempotency_key="task-cross",
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
                task_id="TASK-CROSS", dispatch_key="dispatch-cross", fixture=fixture,
                instructions="cross", commands=[],
            ).id
        finally:
            db.close()

    with ThreadPoolExecutor(max_workers=2) as pool:
        ids = list(pool.map(lambda _index: once(), (0, 1)))
    assert ids[0] == ids[1]
    assert adapter.calls == 1
    with factory() as db:
        rows = db.query(AttemptRow).filter_by(dispatch_key="dispatch-cross").all()
        assert len(rows) == 1


def test_postgres_advisory_lock_keeps_a_stale_heartbeat() -> None:
    """PostgreSQL treats a held advisory lock as live, whatever the heartbeat says."""

    import subprocess
    import sys

    url = os.getenv("SDF_POSTGRES_TEST_URL")
    if not url:
        pytest.skip("SDF_POSTGRES_TEST_URL is not set")
    from sdf_core.db import make_engine

    engine = make_engine(url)
    factory = sessionmaker(engine, expire_on_commit=False)
    executor_id = f"pg-lock-{os.urandom(4).hex()}"
    task_id = f"TASK-{executor_id}"
    attempt_id = f"ATTEMPT-{executor_id}"
    holder = subprocess.Popen(
        [sys.executable, "-c",
         "import os, sys\n"
         "from sdf_core.recovery import _lock_key\n"
         "from sqlalchemy import create_engine, text\n"
         "engine = create_engine(os.environ['SDF_POSTGRES_TEST_URL'])\n"
         "conn = engine.raw_connection()\n"
         "cur = conn.cursor()\n"
         "cur.execute('SELECT pg_advisory_lock(%s)', (_lock_key(sys.argv[1]),))\n"
         "conn.commit()\n"
         "print('ready', flush=True)\n"
         "sys.stdin.read()\n",
         executor_id],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    )
    try:
        ready = holder.stdout.readline().strip()
        assert ready == "ready", holder.stderr.read()
        with factory() as db:
            db.add(TaskRow(
                id=task_id, title="pg-lock", status="running",
                idempotency_key=f"idem-{executor_id}", acceptance_criteria=[], created_at=utcnow(),
            ))
            db.add(AttemptRow(
                id=attempt_id, task_id=task_id, status="running", dispatch_key=f"d-{executor_id}",
                agent="native", created_at=utcnow(), executor_id=executor_id, evidence_mode="synthetic",
                executor_heartbeat_at=utcnow() - LEASE_TTL - timedelta(seconds=120),
            ))
            db.commit()
            assert recover_orphaned_attempts(db) == ()
            assert db.get(AttemptRow, attempt_id).status == "running"
    finally:
        if holder.poll() is None:
            holder.stdin.close()
            holder.wait(timeout=5)
    with factory() as db:
        assert recover_orphaned_attempts(db) == (attempt_id,)
        assert db.get(AttemptRow, attempt_id).status == "failed"
        db.delete(db.get(AttemptRow, attempt_id))
        db.delete(db.get(TaskRow, task_id))
        db.commit()
    engine.dispose()


def test_heartbeat_refresh_between_read_and_update_keeps_the_attempt(tmp_path: Path) -> None:
    """On a database with no process lock, a committed refresh wins the CAS.

    In-memory SQLite is that database. The recovery predicate matches the
    heartbeat it read. A newer committed heartbeat makes the update match
    nothing, so the Attempt stays running.
    """

    from sdf_core.db import make_engine
    from sdf_core.recovery import _reap

    engine = make_engine()
    Base.metadata.create_all(engine)
    factory = sessionmaker(engine, expire_on_commit=False)
    with factory() as db:
        stale = utcnow() - LEASE_TTL - timedelta(seconds=60)
        db.add(TaskRow(
            id="TASK-HB", title="hb", status="running", idempotency_key="task-hb",
            acceptance_criteria=[], created_at=utcnow(),
        ))
        db.add(AttemptRow(
            id="ATTEMPT-HB", task_id="TASK-HB", status="running", dispatch_key="dispatch-hb",
            agent="native", created_at=utcnow(), executor_id="foreign-hb",
            executor_heartbeat_at=stale,
        ))
        db.commit()
        attempt = db.get(AttemptRow, "ATTEMPT-HB")
        seen = attempt.executor_heartbeat_at
        with factory() as other:
            other.execute(
                AttemptRow.__table__.update()
                .where(AttemptRow.id == "ATTEMPT-HB")
                .values(executor_heartbeat_at=utcnow())
            )
            other.commit()
        assert attempt.executor_heartbeat_at == seen
        assert _reap(db, attempt, now=utcnow(), engine=engine) is False
        db.expire_all()
        assert db.get(AttemptRow, "ATTEMPT-HB").status == "running"
        assert db.get(TaskRow, "TASK-HB").status == "running"


def test_killed_subprocess_is_recovered_and_the_same_key_does_not_rerun(tmp_path: Path) -> None:
    """SIGKILL drops the process lock. The parent then settles the Attempt.

    Deleting the service object in this process does not. A new key may run
    the adapter again. That is not a promise that a remote sandbox exited.
    """

    import subprocess
    import sys

    database = tmp_path / "child.sqlite"
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    child = subprocess.Popen(
        [sys.executable, "-c",
         "import sys\n"
         "from pathlib import Path\n"
         "from sqlalchemy.orm import sessionmaker\n"
         "from sdf_core.db import Base, TaskRow, make_engine\n"
         "from sdf_core.execution import ExecutionService\n"
         "from sdf_core.model import utcnow\n"
         "from sdf_core.recovery import hold_engine_lease\n"
         "root, database, fixture = Path(sys.argv[1]), Path(sys.argv[2]), Path(sys.argv[3])\n"
         "engine = make_engine(f'sqlite:///{database}')\n"
         "Base.metadata.create_all(engine)\n"
         "hold_engine_lease(engine)\n"
         "db = sessionmaker(engine, expire_on_commit=False)()\n"
         "db.add(TaskRow(id='TASK-CHILD', title='c', status='created', idempotency_key='child',\n"
         "               acceptance_criteria=[], created_at=utcnow()))\n"
         "db.commit()\n"
         "service = ExecutionService(db, workspace_root=root/'ws', artifact_root=root/'art')\n"
         "begun = service.begin(task_id='TASK-CHILD', dispatch_key='dispatch-child', fixture=fixture)\n"
         "print(begun.attempt.id, flush=True)\n"
         "sys.stdin.read()\n",
         str(tmp_path), str(database), str(fixture)],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    )
    attempt_id = child.stdout.readline().strip()
    assert attempt_id.startswith("ATTEMPT-"), child.stderr.read()
    engine = create_engine(f"sqlite:///{database}", connect_args={"timeout": 30})
    factory = sessionmaker(engine, expire_on_commit=False)
    with factory() as db:
        assert recover_orphaned_attempts(db) == ()
        assert db.get(AttemptRow, attempt_id).status == "running"
    child.kill()
    child.wait(timeout=5)
    with factory() as db:
        assert recover_orphaned_attempts(db) == (attempt_id,)
        assert db.get(AttemptRow, attempt_id).status == "failed"
        assert db.get(AttemptRow, attempt_id).error_class == "killed"
        adapter = CountingAdapter()
        service = ExecutionService(db, workspace_root=tmp_path / "ws2", artifact_root=tmp_path / "art2", adapter=adapter)
        replay = service.run(
            task_id="TASK-CHILD", dispatch_key="dispatch-child", fixture=fixture, instructions="same", commands=[],
        )
        assert replay.id == attempt_id and adapter.calls == 0


def test_fork_after_import_does_not_settle_the_parent_attempt(tmp_path: Path) -> None:
    """A child that imported recovery before fork must not use the parent's id."""

    from sdf_core.recovery import hold_engine_lease

    engine = create_engine(f"sqlite:///{tmp_path / 'fork.sqlite'}")
    Base.metadata.create_all(engine)
    hold_engine_lease(engine)
    factory = sessionmaker(engine, expire_on_commit=False)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    db = factory()
    db.add(TaskRow(
        id="TASK-FORK", title="fork", status="created", idempotency_key="task-fork",
        acceptance_criteria=[], created_at=utcnow(),
    ))
    db.commit()
    service = ExecutionService(db, workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art")
    attempt_id = service.begin(task_id="TASK-FORK", dispatch_key="dispatch-fork", fixture=fixture).attempt.id
    from sdf_core.recovery import current_executor_id
    parent_id = current_executor_id()
    pid = os.fork()
    if pid == 0:
        try:
            child_db = factory()
            recovered = recover_orphaned_attempts(child_db)
            child_id = current_executor_id()
            status = child_db.get(AttemptRow, attempt_id).status
            os._exit(0 if recovered == () and status == "running" and child_id != parent_id else 1)
        except Exception:
            os._exit(2)
    _gone, code = os.waitpid(pid, 0)
    assert code == 0
    assert db.get(AttemptRow, attempt_id).status == "running"
    db.close()


def test_database_clock_not_host_clock_decides_a_foreign_heartbeat(tmp_path: Path, monkeypatch) -> None:
    """A host clock set far ahead does not expire a heartbeat the database just wrote."""

    from sqlalchemy import func
    from sdf_core.db import make_engine

    engine = make_engine()
    Base.metadata.create_all(engine)
    factory = sessionmaker(engine, expire_on_commit=False)
    with factory() as db:
        db.add(TaskRow(
            id="TASK-CLOCK", title="clock", status="running", idempotency_key="task-clock",
            acceptance_criteria=[], created_at=utcnow(),
        ))
        db.add(AttemptRow(
            id="ATTEMPT-CLOCK", task_id="TASK-CLOCK", status="running", dispatch_key="dispatch-clock",
            agent="native", created_at=utcnow(), executor_id="other-clock",
        ))
        db.commit()
        db.execute(
            AttemptRow.__table__.update().where(AttemptRow.id == "ATTEMPT-CLOCK").values(executor_heartbeat_at=func.now())
        )
        db.commit()

        def skewed():
            return utcnow() + timedelta(hours=5)

        monkeypatch.setattr("sdf_core.recovery.utcnow", skewed)
        assert recover_orphaned_attempts(db) == ()
        assert db.get(AttemptRow, "ATTEMPT-CLOCK").status == "running"


def test_two_keys_one_task_only_one_adapter_on_postgres() -> None:
    """Two PostgreSQL workers with different keys: one Attempt, the loser never calls the adapter."""

    url = os.getenv("SDF_POSTGRES_TEST_URL")
    if not url:
        pytest.skip("SDF_POSTGRES_TEST_URL is not set")
    from sdf_core.db import make_engine

    engine = make_engine(url)
    factory = sessionmaker(engine, expire_on_commit=False)
    suffix = os.urandom(4).hex()
    task_id = f"TASK-ONE-{suffix}"
    fixture = Path("/tmp") / f"sdf-one-{suffix}"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    with factory() as db:
        db.add(TaskRow(
            id=task_id, title="one", status="created", idempotency_key=f"idem-{suffix}",
            acceptance_criteria=[], created_at=utcnow(),
        ))
        db.commit()
    adapter = CountingAdapter()
    barrier = threading.Barrier(2)
    errors: list[str] = []

    def once(key: str):
        barrier.wait(timeout=10)
        db = factory()
        try:
            service = ExecutionService(
                db, workspace_root=Path("/tmp") / f"ws-{suffix}", artifact_root=Path("/tmp") / f"art-{suffix}",
                adapter=adapter,
            )
            return service.run(
                task_id=task_id, dispatch_key=key, fixture=fixture, instructions="x", commands=[],
            ).id
        except ValueError as exc:
            errors.append(str(exc))
            return None
        finally:
            db.close()

    with ThreadPoolExecutor(max_workers=2) as pool:
        ids = list(pool.map(once, (f"k-{suffix}-a", f"k-{suffix}-b")))
    winners = [item for item in ids if item]
    assert len(winners) == 1
    assert adapter.calls == 1
    assert errors and "running attempt" in errors[0]
    with factory() as db:
        assert db.query(AttemptRow).filter_by(task_id=task_id).count() == 1
        for row in db.query(AttemptRow).filter_by(task_id=task_id):
            db.delete(row)
        db.delete(db.get(TaskRow, task_id))
        db.commit()
    engine.dispose()


def test_slow_evaluation_does_not_false_recover_or_block_postgres() -> None:
    """Evaluation does not hold the Attempt row lock across the check.

    Another connection refreshes the heartbeat within a short lock timeout,
    recovery leaves the Attempt running, and a different Task can begin.
    """

    url = os.getenv("SDF_POSTGRES_TEST_URL")
    if not url:
        pytest.skip("SDF_POSTGRES_TEST_URL is not set")
    import threading
    from sqlalchemy import func, text, update
    from sdf_core.db import make_engine
    from sdf_core.evaluator import EvaluationResult

    engine = make_engine(url)
    factory = sessionmaker(engine, expire_on_commit=False)
    suffix = os.urandom(4).hex()
    task_id = f"TASK-SLOW-{suffix}"
    other_task = f"TASK-OTHER-{suffix}"
    fixture = Path("/tmp") / f"sdf-slow-{suffix}"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    started, release = threading.Event(), threading.Event()

    class Slow:
        def evaluate(self, **kwargs):
            started.set()
            assert release.wait(15)
            return EvaluationResult(status="INCONCLUSIVE", evidence=())

    with factory() as db:
        db.add_all([
            TaskRow(id=task_id, title="slow", status="created", idempotency_key=f"slow-{suffix}",
                    acceptance_criteria=[], created_at=utcnow()),
            TaskRow(id=other_task, title="other", status="created", idempotency_key=f"other-{suffix}",
                    acceptance_criteria=[], created_at=utcnow()),
        ])
        db.commit()
        service = ExecutionService(
            db, workspace_root=Path("/tmp") / f"wslow-{suffix}", artifact_root=Path("/tmp") / f"aslow-{suffix}",
            evaluator=Slow(),
        )

        def _run():
            service.run(task_id=task_id, dispatch_key=f"slow-{suffix}", fixture=fixture, instructions="x", commands=[])

        worker = threading.Thread(target=_run)
        worker.start()
        assert started.wait(10)
        with factory() as look:
            attempt_id = look.scalar(select(AttemptRow.id).where(AttemptRow.dispatch_key == f"slow-{suffix}"))
        with engine.connect() as conn:
            conn.execute(text("SET lock_timeout = '500ms'"))
            refreshed = conn.execute(
                update(AttemptRow)
                .where(AttemptRow.id == attempt_id, AttemptRow.status == "running")
                .values(executor_heartbeat_at=func.now())
            )
            conn.commit()
        assert refreshed.rowcount == 1
        with factory() as other:
            assert attempt_id not in recover_orphaned_attempts(other)
            assert other.get(AttemptRow, attempt_id).status == "running"
            begun = ExecutionService(
                other, workspace_root=Path("/tmp") / f"w2-{suffix}", artifact_root=Path("/tmp") / f"a2-{suffix}",
            ).begin(task_id=other_task, dispatch_key=f"other-{suffix}", fixture=fixture)
            assert begun.attempt.status == "running"
        release.set()
        worker.join(20)
        assert not worker.is_alive()
        db.expire_all()
        for row in db.query(AttemptRow).filter(AttemptRow.task_id.in_((task_id, other_task))):
            db.delete(row)
        db.delete(db.get(TaskRow, task_id))
        db.delete(db.get(TaskRow, other_task))
        db.commit()
    engine.dispose()
