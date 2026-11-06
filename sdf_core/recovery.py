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


class AttemptFenceError(Exception):
    """This process cannot settle the Attempt (ADR 0041).

    The row was not ``running`` for ``EXECUTOR_ID``. Callers must not write
    the Attempt or its Task after this.
    """

    def __init__(self, attempt_id: str, *, status: str | None, executor_id: str | None):
        self.attempt_id = attempt_id
        self.status = status
        self.executor_id = executor_id
        super().__init__(
            f"attempt {attempt_id} is not a running attempt owned by this executor "
            f"(status={status!r}, executor_id={executor_id!r})"
        )


def executor_is_live(attempt: AttemptRow, *, now: datetime | None = None) -> bool:
    """True when some executor still owns this running Attempt.

    A stale heartbeat is dead even if this process still holds the service
    object (a pause or a skewed clock). A fresh heartbeat from this process
    counts only while that object is still alive, so dropping it is the
    host-crash test's dead executor.
    """

    now = now or utcnow()
    with _lock:
        held_here = attempt.id in _owners
    if not attempt.executor_id or attempt.executor_heartbeat_at is None:
        return False
    age = now - _as_utc(attempt.executor_heartbeat_at)
    if age > LEASE_TTL:
        return False
    if attempt.executor_id == EXECUTOR_ID:
        return held_here
    return True


def require_owned_running(db: Session, attempt_id: str) -> AttemptRow:
    """Reload the Attempt and refuse unless this executor still has it running."""

    attempt = db.get(AttemptRow, attempt_id, populate_existing=True)
    if attempt is None or attempt.status != AttemptState.RUNNING.value or attempt.executor_id != EXECUTOR_ID:
        status = None if attempt is None else attempt.status
        owner = None if attempt is None else attempt.executor_id
        _log.warning(
            "refusing to settle attempt %s: status=%s executor_id=%s this_executor=%s",
            attempt_id, status, owner, EXECUTOR_ID,
        )
        raise AttemptFenceError(attempt_id, status=status, executor_id=owner)
    return attempt


def settle_owned_attempt(db: Session, attempt_id: str, target: str, **columns: object) -> AttemptRow:
    """Set status only while the row is ``running`` for this executor.

    The WHERE clause is the fence. Zero rows leaves the Attempt and the Task
    untouched. This does not commit. On refusal the caller rolls back any
    other pending writes from the same attempt to settle.
    """

    result = db.execute(
        update(AttemptRow)
        .where(
            AttemptRow.id == attempt_id,
            AttemptRow.status == AttemptState.RUNNING.value,
            AttemptRow.executor_id == EXECUTOR_ID,
        )
        .values(status=target, **columns)
        .execution_options(synchronize_session=False)
    )
    if result.rowcount != 1:
        attempt = db.get(AttemptRow, attempt_id, populate_existing=True)
        status = None if attempt is None else attempt.status
        owner = None if attempt is None else attempt.executor_id
        _log.warning(
            "refusing to settle attempt %s to %s: status=%s executor_id=%s this_executor=%s",
            attempt_id, target, status, owner, EXECUTOR_ID,
        )
        raise AttemptFenceError(attempt_id, status=status, executor_id=owner)
    attempt = db.get(AttemptRow, attempt_id)
    if attempt is not None:
        db.expire(attempt)
        db.refresh(attempt)
    return attempt


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


def recover_orphaned_attempts(db: Session, *, now: datetime | None = None) -> tuple[str, ...]:
    """Fail running Attempts with no live executor, and unstick their Tasks.

    Attempt: ``running`` to ``failed`` (existing transition). ``error_class``
    is ``killed`` when it was empty: the executor did not finish.

    Task: ``running`` to ``evaluating`` to ``failed``, and only when no
    Attempt of that Task is still running. A later dispatch can then enter
    ``ready``. No new transitions are added.

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
        attempt.status = transition_attempt(AttemptState.RUNNING, AttemptState.FAILED).value
        if attempt.error_class is None:
            attempt.error_class = "killed"
        settled.append(attempt.id)
    if not settled:
        return ()
    for task_id in dict.fromkeys(attempt.task_id for attempt in running if attempt.id in set(settled)):
        if task_id in live_tasks:
            continue
        task = db.get(TaskRow, task_id)
        if task is None or task.status != TaskState.RUNNING.value:
            continue
        task.status = transition_task(TaskState.RUNNING, TaskState.EVALUATING).value
        task.status = transition_task(TaskState.EVALUATING, TaskState.FAILED).value
    try:
        db.commit()
    except Exception:
        db.rollback()
        raise
    _log.info("recovered orphaned attempts: %s", ",".join(settled))
    return tuple(settled)


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
