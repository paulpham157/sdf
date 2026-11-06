"""Settle Attempts left ``running`` with no live executor (ADR 0041).

Liveness is a lease, not ``RuntimeController.recover`` (ADR 0029 only restores
a runtime-event stream). This process treats an Attempt as live while the
``ExecutionService`` that started it is still alive. Another process is live
while its heartbeat is newer than :data:`LEASE_TTL`.
"""

from __future__ import annotations

import threading
import time
from datetime import datetime, timedelta, timezone
from uuid import uuid4
from weakref import WeakValueDictionary

from sqlalchemy import select, update
from sqlalchemy.orm import Session, sessionmaker
from sqlalchemy.pool import StaticPool

from .db import AttemptRow, TaskRow
from .model import AttemptState, TaskState, utcnow
from .observability import get_logger
from .state import transition_attempt, transition_task


# A dead executor is invisible until this elapses. The refresher wakes more
# often than that, so another process is settled within about one lease of
# its last heartbeat.
LEASE_TTL = timedelta(seconds=30)
_HEARTBEAT_INTERVAL_S = 10

# Stable for this process. A restarted process mints a new id, so a fresh
# heartbeat stamped here and a fresh heartbeat stamped next door are not the
# same executor.
EXECUTOR_ID = uuid4().hex

# attempt id -> the ExecutionService (or other owner) that is running it.
# The value is weak: dropping the owner drops the claim, including when the
# test models a crash by deleting the service in this same process.
_owners: WeakValueDictionary[str, object] = WeakValueDictionary()
_lock = threading.Lock()
_refresher_started = False

_log = get_logger("recovery")


def claim_executor(owner: object, attempt: AttemptRow) -> None:
    """Record that ``owner`` is executing ``attempt`` until ``owner`` is gone.

    The columns are committed by the caller, in the same transaction that
    moves the Attempt to ``running``.
    """

    with _lock:
        _owners[attempt.id] = owner
    attempt.executor_id = EXECUTOR_ID
    attempt.executor_heartbeat_at = utcnow()


def executor_is_live(attempt: AttemptRow, *, now: datetime | None = None) -> bool:
    """True when some executor still owns this running Attempt."""

    now = now or utcnow()
    with _lock:
        held_here = attempt.id in _owners
    # This process still has the object that called begin. A crashed caller
    # that only dropped that object (the host-crash test) is not held.
    if held_here and attempt.executor_id in (None, EXECUTOR_ID):
        return True
    if not attempt.executor_id or attempt.executor_heartbeat_at is None:
        return False
    age = now - _as_utc(attempt.executor_heartbeat_at)
    if age > LEASE_TTL:
        return False
    if attempt.executor_id == EXECUTOR_ID:
        # We stamped it, and the owner object is gone. Do not wait out the
        # lease: this process is not executing it.
        return held_here
    return True


def heartbeat_refresher_enabled(session_factory: sessionmaker) -> bool:
    """False for the one-connection in-memory SQLite used by hermetic tests.

    A second thread cannot safely share that connection. Production
    PostgreSQL, and any engine with its own connections, can refresh.
    """

    engine = session_factory.kw.get("bind")
    if engine is None:
        return False
    if engine.dialect.name == "sqlite" and isinstance(engine.pool, StaticPool):
        return False
    return True


def start_executor_heartbeat(session_factory: sessionmaker) -> None:
    """Refresh leases, then sweep orphans, until the process exits.

    The first sweep is the caller's: startup calls
    :func:`recover_orphaned_attempts` itself. This loop covers a lease that
    expires later, so a dead neighbor is settled without another restart.
    """

    global _refresher_started
    if not heartbeat_refresher_enabled(session_factory):
        return
    with _lock:
        if _refresher_started:
            return
        _refresher_started = True
    thread = threading.Thread(
        target=_refresh_loop, args=(session_factory,), name="sdf-executor-lease", daemon=True,
    )
    thread.start()


def compare_and_swap_attempt(
    db: Session,
    attempt: AttemptRow,
    *,
    from_status: AttemptState,
    to_status: AttemptState,
    extra: dict[str, object] | None = None,
) -> bool:
    """Persist ``to_status`` only if the row is still ``from_status``.

    Returns True when this session won. On loss, expires ``attempt`` so a
    later flush cannot overwrite the durable winner.
    """

    transition_attempt(from_status, to_status)
    values: dict[str, object] = {"status": to_status.value}
    if extra:
        values.update(extra)
    result = db.execute(
        update(AttemptRow)
        .where(AttemptRow.id == attempt.id, AttemptRow.status == from_status.value)
        .values(**values)
        .execution_options(synchronize_session=False)
    )
    if result.rowcount != 1:
        db.expire(attempt)
        return False
    attempt.status = to_status.value
    if extra:
        for key, value in extra.items():
            setattr(attempt, key, value)
    return True


def compare_and_swap_task(
    db: Session,
    task: TaskRow,
    *,
    from_status: TaskState,
    to_status: TaskState,
) -> bool:
    """Persist ``to_status`` only if the Task row is still ``from_status``."""

    transition_task(from_status, to_status)
    result = db.execute(
        update(TaskRow)
        .where(TaskRow.id == task.id, TaskRow.status == from_status.value)
        .values(status=to_status.value)
        .execution_options(synchronize_session=False)
    )
    if result.rowcount != 1:
        db.expire(task)
        return False
    task.status = to_status.value
    return True


def recover_orphaned_attempts(db: Session, *, now: datetime | None = None) -> tuple[str, ...]:
    """Fail running Attempts with no live executor, and unstick their Tasks.

    Attempt: ``running`` to ``failed`` (existing transition). ``error_class``
    is ``killed`` when it was empty: the executor did not finish.

    Task: ``running`` to ``evaluating`` to ``failed``, and only when no
    Attempt of that Task is still running. A later dispatch can then enter
    ``ready``. No new transitions are added.

    Each write is compare-and-swap on the durable status, so a worker that
    already completed the Attempt wins, and a stale worker cannot resurrect
    a Task recovery has failed.

    A second call finds nothing running and writes nothing.
    """

    now = now or utcnow()
    running = list(db.scalars(select(AttemptRow).where(AttemptRow.status == AttemptState.RUNNING.value)))
    live_tasks: set[str] = set()
    settled: list[str] = []
    for attempt in running:
        if executor_is_live(attempt, now=now):
            live_tasks.add(attempt.task_id)
            continue
        extra = {"error_class": "killed"} if attempt.error_class is None else None
        if not compare_and_swap_attempt(
            db, attempt, from_status=AttemptState.RUNNING, to_status=AttemptState.FAILED, extra=extra,
        ):
            continue
        settled.append(attempt.id)
    if not settled:
        return ()
    for task_id in dict.fromkeys(attempt.task_id for attempt in running if attempt.id in set(settled)):
        if task_id in live_tasks:
            continue
        task = db.get(TaskRow, task_id)
        if task is None:
            continue
        if not compare_and_swap_task(
            db, task, from_status=TaskState.RUNNING, to_status=TaskState.EVALUATING,
        ):
            continue
        compare_and_swap_task(db, task, from_status=TaskState.EVALUATING, to_status=TaskState.FAILED)
    try:
        db.commit()
    except Exception:
        db.rollback()
        raise
    _log.info("recovered orphaned attempts: %s", ",".join(settled))
    return tuple(settled)


def settle_local_attempts(db: Session) -> tuple[str, ...]:
    """Fail Attempts this process stamped whose executor object is gone.

    Called on lifespan shutdown. Living ``ExecutionService`` objects keep
    their claims, so an in-flight request is left alone. Attempts whose
    owner has already been dropped (a drained request, or a crash modeled
    by deleting the service) are dead immediately even if the heartbeat is
    fresh, because ``executor_id`` is this process. Neighbor processes with
    a live heartbeat are left alone.
    """

    return recover_orphaned_attempts(db)


def reset_recovery_state() -> str:
    """Drop in-process claims and mint a new executor id (a new OS process).

    Returns the new ``EXECUTOR_ID``. Does not stop a heartbeat thread that
    may already be running; hermetic tests never start one.
    """

    global EXECUTOR_ID
    with _lock:
        _owners.clear()
    EXECUTOR_ID = uuid4().hex
    return EXECUTOR_ID


def _refresh_loop(session_factory: sessionmaker) -> None:
    while True:
        time.sleep(_HEARTBEAT_INTERVAL_S)
        db = session_factory()
        try:
            _write_heartbeats(db)
            recover_orphaned_attempts(db)
        except Exception:  # noqa: BLE001 - a sweep must not kill the refresher
            db.rollback()
            _log.warning("executor lease sweep failed", exc_info=True)
        finally:
            db.close()


def _write_heartbeats(db: Session) -> None:
    with _lock:
        ids = list(_owners.keys())
    if not ids:
        return
    now = utcnow()
    for attempt_id in ids:
        with _lock:
            if attempt_id not in _owners:
                continue
        attempt = db.get(AttemptRow, attempt_id)
        if attempt is None or attempt.status != AttemptState.RUNNING.value:
            continue
        if attempt.executor_id not in (None, EXECUTOR_ID):
            continue
        attempt.executor_id = EXECUTOR_ID
        attempt.executor_heartbeat_at = now
    db.commit()


def _as_utc(value: datetime) -> datetime:
    if value.tzinfo is None:
        return value.replace(tzinfo=timezone.utc)
    return value.astimezone(timezone.utc)
