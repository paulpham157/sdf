"""Settle Attempts left ``running`` when their executor is gone (ADR 0029).

This is not ``RuntimeController.recover``. That call restores a runtime-event
stream. This one moves Task and Attempt rows along the transitions that
already exist, and only for a lease that is dead.

A lease is this process's :data:`EXECUTOR_ID` plus a heartbeat. This process
treats an Attempt as live while the object that claimed it is still alive.
Another process is live while its heartbeat is inside :data:`LEASE_TTL`.
A running row with no ``executor_id`` is not swept: a startup pass cannot
tell that row from a worker that never took a lease.

External work is not exactly-once. The adapter for a dispatch key runs at
most once here. A crash can leave that work unfinished, or finished outside
this process, and a later new key can run it again.
"""

from __future__ import annotations

import threading
import time
from datetime import datetime, timedelta, timezone
from uuid import uuid4
from weakref import WeakValueDictionary

from sqlalchemy import or_, select, update
from sqlalchemy.orm import Session, sessionmaker
from sqlalchemy.pool import StaticPool

from .db import AttemptRow, TaskRow
from .model import AttemptState, TaskState, utcnow
from .observability import get_logger
from .state import transition_attempt, transition_task


# Another process is treated as dead only after this long without a heartbeat.
# The refresher wakes more often, so a live executor does not age out.
LEASE_TTL = timedelta(seconds=30)
_HEARTBEAT_INTERVAL_S = 10.0

# Stable for this process. A restarted process mints a new id, so its startup
# does not own the previous process's rows.
EXECUTOR_ID = uuid4().hex

# attempt id → the object that is executing it. Weak: dropping the object
# drops the claim, which is how a test models a crash inside this process.
_owners: WeakValueDictionary[str, object] = WeakValueDictionary()
_lock = threading.Lock()
_refresher_started = False

_log = get_logger("recovery")


class AttemptFenceError(ValueError):
    """The Attempt was not ``running`` for this executor.

    Callers must not write the Attempt or its Task after this. ``/run`` maps
    it to 409 because it is a :class:`ValueError`.
    """

    def __init__(self, attempt_id: str, *, status: str | None, executor_id: str | None):
        self.attempt_id = attempt_id
        self.status = status
        self.executor_id = executor_id
        super().__init__(
            f"attempt {attempt_id} is not running for this executor "
            f"(status={status!r}, executor_id={executor_id!r})"
        )


def claim_executor(owner: object, attempt: AttemptRow) -> None:
    """Record that ``owner`` is executing ``attempt`` until ``owner`` is gone.

    The columns are committed by the caller, in the same transaction that
    moves the Attempt to ``running``.
    """

    with _lock:
        _owners[attempt.id] = owner
    attempt.executor_id = EXECUTOR_ID
    attempt.executor_heartbeat_at = utcnow()


def release_executor(attempt_id: str) -> None:
    """Drop this process's claim. A terminal Attempt does not need one."""

    with _lock:
        _owners.pop(attempt_id, None)


def executor_is_live(attempt: AttemptRow, *, now: datetime | None = None) -> bool:
    """True when some executor still owns this running Attempt.

    This process's own id is live only while the claiming object is alive,
    even if the heartbeat is fresh: dropping that object is an in-process
    crash. A different id is live only while its heartbeat is inside the lease.
    No id is not a live lease (and is not reaped).
    """

    if not attempt.executor_id:
        return False
    if attempt.executor_id == EXECUTOR_ID:
        with _lock:
            return attempt.id in _owners
    if attempt.executor_heartbeat_at is None:
        return False
    now = now or utcnow()
    return now - _as_utc(attempt.executor_heartbeat_at) <= LEASE_TTL


def heartbeat_refresher_enabled(session_factory: sessionmaker) -> bool:
    """False for the one-connection in-memory SQLite used by hermetic tests.

    A second thread cannot safely share that connection. PostgreSQL, and any
    engine that checks out its own connections, can refresh.
    """

    engine = session_factory.kw.get("bind")
    if engine is None:
        return False
    if engine.dialect.name == "sqlite" and isinstance(engine.pool, StaticPool):
        return False
    return True


def start_executor_heartbeat(session_factory: sessionmaker) -> None:
    """Refresh leases this process holds, and sweep dead ones, until exit.

    Startup calls :func:`recover_orphaned_attempts` itself. This loop covers
    a lease that expires later, so a dead neighbor is settled without another
    restart. Hermetic in-memory SQLite does not start the thread.
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
    to_status: AttemptState,
    executor_scoped: bool,
    extra: dict | None = None,
) -> bool:
    """Persist ``to_status`` only while the row is still ``running``.

    ``executor_scoped`` also requires the lease to be unset or this process.
    A finish from the owning executor uses that. Recovery and cancellation
    pass false and add their own predicates. Does not commit. On loss, the
    Attempt is expired so a later flush cannot overwrite the winner.
    """

    transition_attempt(AttemptState.RUNNING, to_status)
    conditions = [AttemptRow.id == attempt.id, AttemptRow.status == AttemptState.RUNNING.value]
    if executor_scoped:
        conditions.append(or_(AttemptRow.executor_id.is_(None), AttemptRow.executor_id == EXECUTOR_ID))
    values: dict = {"status": to_status.value}
    if extra:
        values.update(extra)
    result = db.execute(
        update(AttemptRow).where(*conditions).values(**values).execution_options(synchronize_session=False)
    )
    if result.rowcount != 1:
        db.expire(attempt)
        return False
    db.expire(attempt)
    db.refresh(attempt)
    return True


def compare_and_swap_task(db: Session, task: TaskRow, *, from_status: TaskState, to_status: TaskState) -> bool:
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
    db.expire(task)
    db.refresh(task)
    return True


def settle_task_if_idle(db: Session, task_id: str, terminal: TaskState) -> bool:
    """Move a Task ``running`` → ``evaluating`` → ``terminal`` when nothing is running.

    No new transition is added. A Task that is not ``running``, or that still
    has a running Attempt, is left as it is. Does not commit.
    """

    if terminal not in (TaskState.FAILED, TaskState.INCONCLUSIVE, TaskState.SUCCEEDED):
        raise ValueError(f"unsupported terminal task state: {terminal}")
    still = db.scalar(
        select(AttemptRow.id).where(
            AttemptRow.task_id == task_id, AttemptRow.status == AttemptState.RUNNING.value,
        ).limit(1)
    )
    if still:
        return False
    task = db.get(TaskRow, task_id)
    if task is None:
        return False
    if not compare_and_swap_task(db, task, from_status=TaskState.RUNNING, to_status=TaskState.EVALUATING):
        return False
    if not compare_and_swap_task(db, task, from_status=TaskState.EVALUATING, to_status=terminal):
        raise RuntimeError(f"task {task_id} moved to evaluating and then could not enter {terminal.value}")
    return True


def cancel_running_attempt(db: Session, attempt: AttemptRow) -> bool:
    """Cancel an Attempt only while it is still ``running``.

    Any executor may be cancelled: cancellation is an operator decision, not
    a crash. A row that recovery or finish already left terminal is unchanged.
    Does not commit, and does not move the Task; the caller does that when
    this returns true.
    """

    return compare_and_swap_attempt(db, attempt, to_status=AttemptState.CANCELLED, executor_scoped=False)


def recover_orphaned_attempts(db: Session, *, now: datetime | None = None) -> tuple[str, ...]:
    """Fail running Attempts with a dead lease, and unstick their Tasks.

    Attempt: ``running`` → ``failed``. ``error_class`` becomes ``killed`` only
    when it was empty. Task: ``running`` → ``evaluating`` → ``failed``, and
    only when no Attempt of that Task is still ``running``.

    Each write matches the status (and, for a neighbor, the heartbeat) that
    made the row look dead, so a refresh or a late finish wins and this pass
    writes nothing. A second call finds nothing running under a dead lease.

    Unleased rows (``executor_id`` unset) are skipped. Evidence, revisions,
    and graph rows are not written or deleted. Commits when it changes rows.
    """

    now = now or utcnow()
    running = list(db.scalars(select(AttemptRow).where(AttemptRow.status == AttemptState.RUNNING.value)))
    settled: list[str] = []
    try:
        for attempt in running:
            if not _reap(db, attempt, now=now):
                continue
            settled.append(attempt.id)
        if not settled:
            return ()
        for task_id in dict.fromkeys(
            attempt.task_id for attempt in running if attempt.id in set(settled)
        ):
            settle_task_if_idle(db, task_id, TaskState.FAILED)
        db.commit()
    except Exception:
        db.rollback()
        raise
    _log.info("recovered orphaned attempts", extra={"attempt_ids": settled})
    return tuple(settled)


def reset_recovery_state() -> str:
    """Drop in-process claims and mint a new executor id (a new OS process).

    Returns the new id. Does not stop a heartbeat thread. Hermetic tests do
    not start one. Production code does not call this.
    """

    global EXECUTOR_ID
    with _lock:
        _owners.clear()
    EXECUTOR_ID = uuid4().hex
    return EXECUTOR_ID


def _reap(db: Session, attempt: AttemptRow, *, now: datetime) -> bool:
    """Fail ``attempt`` if its lease is dead. False leaves the row untouched."""

    if not attempt.executor_id or executor_is_live(attempt, now=now):
        return False
    conditions = [
        AttemptRow.id == attempt.id,
        AttemptRow.status == AttemptState.RUNNING.value,
        AttemptRow.executor_id == attempt.executor_id,
    ]
    if attempt.executor_id == EXECUTOR_ID:
        # The claiming object is already gone. A finish from this process
        # changes status and this update matches nothing.
        pass
    elif attempt.executor_heartbeat_at is None:
        conditions.append(AttemptRow.executor_heartbeat_at.is_(None))
    else:
        # Match the heartbeat we judged stale. A refresh changes it and wins.
        conditions.append(AttemptRow.executor_heartbeat_at == attempt.executor_heartbeat_at)
    transition_attempt(AttemptState.RUNNING, AttemptState.FAILED)
    error_class = attempt.error_class or "killed"
    result = db.execute(
        update(AttemptRow)
        .where(*conditions)
        .values(status=AttemptState.FAILED.value, error_class=error_class)
        .execution_options(synchronize_session=False)
    )
    if result.rowcount != 1:
        db.expire(attempt)
        return False
    db.expire(attempt)
    db.refresh(attempt)
    return True


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
        ids = [attempt_id for attempt_id, owner in _owners.items() if owner is not None]
    if not ids:
        return
    now = utcnow()
    db.execute(
        update(AttemptRow)
        .where(
            AttemptRow.id.in_(ids),
            AttemptRow.status == AttemptState.RUNNING.value,
            or_(AttemptRow.executor_id.is_(None), AttemptRow.executor_id == EXECUTOR_ID),
        )
        .values(executor_id=EXECUTOR_ID, executor_heartbeat_at=now)
        .execution_options(synchronize_session=False)
    )
    db.commit()


def _as_utc(value: datetime) -> datetime:
    if value.tzinfo is None:
        return value.replace(tzinfo=timezone.utc)
    return value.astimezone(timezone.utc)
