"""Settle Attempts left ``running`` when their executor process is gone (ADR 0029).

This is not ``RuntimeController.recover``. That call restores a runtime-event
stream. This one moves Task and Attempt rows along transitions that already
exist, and only when the executor is dead.

Liveness of another process is a lock that dies with that process: an
exclusive ``flock`` beside a SQLite file, or a PostgreSQL session advisory
lock. A stale heartbeat does not reap a process that still holds the lock.
A fresh heartbeat does not keep a process whose lock is gone. The heartbeat
is only the signal when the database cannot host that lock (in-memory
SQLite, which no second OS process shares).

A running row with no ``executor_id`` is not swept unless the operator sets
``SDF_RECOVER_UNLEASED_ATTEMPTS=1`` after every pre-lease process has stopped.
That switch is a deployment decision. Leaving it off does not fail a live
worker that never wrote a lease, and turning it on will.

External work is not exactly-once. The adapter for one dispatch key runs at
most once here. Process death does not prove a remote sandbox has exited.
A new key can start another one.
"""

from __future__ import annotations

import fcntl
import hashlib
import os
import threading
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path
from uuid import uuid4

from sqlalchemy import exists, func, or_, select, text, update
from sqlalchemy.exc import DBAPIError, OperationalError
from sqlalchemy.orm import Session, sessionmaker
from sqlalchemy.pool import StaticPool

from .db import AttemptRow, TaskRow
from .model import AttemptState, TaskState, utcnow
from .observability import get_logger
from .state import transition_attempt, transition_task


# Used only when this database cannot hold a cross-process lock (in-memory
# SQLite). A file SQLite or PostgreSQL lock overrides the heartbeat.
LEASE_TTL = timedelta(seconds=30)
_HEARTBEAT_INTERVAL_S = 10.0
UNLEASED_RECOVERY_ENV = "SDF_RECOVER_UNLEASED_ATTEMPTS"

EXECUTOR_ID = uuid4().hex
_executor_pid = os.getpid()

# Attempts this process has started and not yet finished. A missing entry is
# not death: the process lock is. The set only tells the heartbeat which rows
# to refresh. It is cleared in a forked child.
_claimed: set[str] = set()
_lock = threading.Lock()
_refresher_started = False
_process_lease_file = None
_process_lease_connection = None
_LOCK_WAIT = "200ms"

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


def current_executor_id() -> str:
    """The executor id of this OS process, including after ``fork``.

    The id is minted at import. A forked child would otherwise keep the
    parent's id and an empty claim set, and then treat the parent's fresh
    Attempt as its own abandoned run. A pid check covers a fork that happened
    before ``register_at_fork`` ran.
    """

    _ensure_process_identity()
    return EXECUTOR_ID


def _ensure_process_identity() -> None:
    global EXECUTOR_ID, _executor_pid, _process_lease_file, _process_lease_connection, _refresher_started
    if os.getpid() == _executor_pid:
        return
    _executor_pid = os.getpid()
    EXECUTOR_ID = uuid4().hex
    with _lock:
        _claimed.clear()
    # Drop the child's copy of the parent's lock fd. Closing it does not
    # release the parent's lock: the parent still holds its own descriptor.
    if _process_lease_file is not None:
        try:
            _process_lease_file.close()
        except Exception:  # noqa: BLE001
            pass
        _process_lease_file = None
    if _process_lease_connection is not None:
        try:
            _process_lease_connection.close()
        except Exception:  # noqa: BLE001
            pass
        _process_lease_connection = None
    _refresher_started = False


def _after_fork_child() -> None:
    _ensure_process_identity()


if hasattr(os, "register_at_fork"):
    os.register_at_fork(after_in_child=_after_fork_child)


def claim_executor(owner: object, attempt: AttemptRow) -> None:
    """Stamp this process on ``attempt`` and remember it for heartbeats.

    ``owner`` is not a liveness proof. The process lock is. Dropping
    ``owner`` (request end, a decision gate, garbage collection) does not
    make the Attempt dead. The columns commit with the transition to
    ``running``.
    """

    del owner  # retained so callers stay the same; liveness is not this object
    with _lock:
        _claimed.add(attempt.id)
    attempt.executor_id = current_executor_id()
    attempt.executor_heartbeat_at = utcnow()


def release_executor(attempt_id: str) -> None:
    """Stop heartbeating an Attempt. This does not settle it."""

    with _lock:
        _claimed.discard(attempt_id)


def hold_process_lease(session_factory: sessionmaker) -> None:
    """Hold the lock that dies with this process, before any Attempt is published."""

    engine = session_factory.kw.get("bind")
    if engine is not None:
        hold_engine_lease(engine)


def hold_engine_lease(engine) -> None:
    """Hold this process's executor lock on ``engine`` for the process lifetime."""

    global _process_lease_file, _process_lease_connection
    if engine.dialect.name == "postgresql":
        if _process_lease_connection is not None:
            return
        connection = engine.raw_connection()
        cursor = connection.cursor()
        cursor.execute("SELECT pg_advisory_lock(%s)", (_lock_key(EXECUTOR_ID),))
        connection.commit()
        _process_lease_connection = connection
        return
    path = executor_lease_path(engine, EXECUTOR_ID)
    if path is None or _process_lease_file is not None:
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    handle = path.open("a+")
    fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
    _process_lease_file = handle


def executor_lease_path(engine, executor_id: str) -> Path | None:
    """Lock file for a SQLite database file. ``None`` when there is no file."""

    if engine.dialect.name != "sqlite":
        return None
    database = engine.url.database
    if not database or database == ":memory:":
        return None
    digest = hashlib.sha256(executor_id.encode()).hexdigest()
    return Path(database).resolve().parent / ".sdf-executor-leases" / digest


def process_holds_lease(engine, executor_id: str) -> bool | None:
    """Whether some live process holds ``executor_id``'s lock.

    ``True`` the lock is held. ``False`` this call acquired it, so no process
    held it, and the lock was released again. ``None`` this database cannot
    express a cross-process lock.
    """

    if engine.dialect.name == "postgresql":
        connection = engine.raw_connection()
        try:
            cursor = connection.cursor()
            cursor.execute("SELECT pg_try_advisory_lock(%s)", (_lock_key(executor_id),))
            acquired = bool(cursor.fetchone()[0])
            if acquired:
                cursor.execute("SELECT pg_advisory_unlock(%s)", (_lock_key(executor_id),))
            connection.commit()
        finally:
            connection.close()
        return not acquired
    path = executor_lease_path(engine, executor_id)
    if path is None:
        return None
    path.parent.mkdir(parents=True, exist_ok=True)
    handle = path.open("a+")
    try:
        fcntl.flock(handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        return True
    else:
        fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
        return False
    finally:
        handle.close()


def executor_is_live(attempt: AttemptRow, *, now: datetime | None = None, engine=None, db: Session | None = None) -> bool:
    """True when the Attempt's executor should not be settled.

    This process's own id is live while this process is alive. Garbage
    collection of the ``ExecutionService`` is not death (a decision gate
    leaves the Attempt running after the request returns). Another id is
    live when its process lock is held, even if the heartbeat looks old.
    Without a lock (in-memory SQLite), a foreign id uses the database clock,
    not the host's ``utcnow``.
    """

    if not attempt.executor_id:
        return False
    if attempt.executor_id == current_executor_id():
        return True
    if engine is not None:
        held = process_holds_lease(engine, attempt.executor_id)
        if held is not None:
            return held
    if db is None:
        if attempt.executor_heartbeat_at is None:
            return False
        now = now or utcnow()
        return now - _as_utc(attempt.executor_heartbeat_at) <= LEASE_TTL
    return _heartbeat_fresh_on_database_clock(db, attempt)


def heartbeat_refresher_enabled(session_factory: sessionmaker) -> bool:
    """False for the one-connection in-memory SQLite used by hermetic tests."""

    engine = session_factory.kw.get("bind")
    if engine is None:
        return False
    if engine.dialect.name == "sqlite" and isinstance(engine.pool, StaticPool):
        return False
    return True


def start_executor_heartbeat(session_factory: sessionmaker) -> None:
    """Hold this process's lock, then refresh heartbeats until exit.

    The lock is what other processes trust. The heartbeat remains for
    in-memory databases and for operators reading the row. Hermetic
    in-memory SQLite does not start the thread.
    """

    global _refresher_started
    hold_process_lease(session_factory)
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
    Does not commit. On loss, the Attempt is expired so a later flush cannot
    overwrite the winner.
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

    The ``running`` check is inside the UPDATE, so a sibling Attempt that
    commits between a separate SELECT and this write cannot be skipped.
    Does not commit.
    """

    if terminal not in (TaskState.FAILED, TaskState.INCONCLUSIVE, TaskState.SUCCEEDED):
        raise ValueError(f"unsupported terminal task state: {terminal}")
    running_sibling = exists(
        select(AttemptRow.id).where(
            AttemptRow.task_id == task_id,
            AttemptRow.status == AttemptState.RUNNING.value,
        )
    )
    result = db.execute(
        update(TaskRow)
        .where(TaskRow.id == task_id, TaskRow.status == TaskState.RUNNING.value, ~running_sibling)
        .values(status=TaskState.EVALUATING.value)
        .execution_options(synchronize_session=False)
    )
    if result.rowcount != 1:
        task = db.get(TaskRow, task_id)
        if task is not None:
            db.expire(task)
        return False
    task = db.get(TaskRow, task_id)
    if task is None:
        return False
    db.expire(task)
    db.refresh(task)
    if not compare_and_swap_task(db, task, from_status=TaskState.EVALUATING, to_status=terminal):
        raise RuntimeError(f"task {task_id} moved to evaluating and then could not enter {terminal.value}")
    return True


def cancel_running_attempt(db: Session, attempt: AttemptRow) -> bool:
    """Cancel an Attempt only while it is still ``running``. Does not commit."""

    return compare_and_swap_attempt(db, attempt, to_status=AttemptState.CANCELLED, executor_scoped=False)


def recover_orphaned_attempts(
    db: Session, *, now: datetime | None = None, task_id: str | None = None,
) -> tuple[str, ...]:
    """Fail running Attempts whose executor is dead, and unstick their Tasks.

    Unleased rows are included only when ``SDF_RECOVER_UNLEASED_ATTEMPTS`` is
    ``1``. Each write matches the status that was read. A second call writes
    nothing. Evidence, revisions, and graph rows are not changed. Commits
    when it changes rows.
    """

    now = now or utcnow()
    engine = db.get_bind()
    statement = select(AttemptRow).where(AttemptRow.status == AttemptState.RUNNING.value)
    if task_id is not None:
        statement = statement.where(AttemptRow.task_id == task_id)
    running = list(db.scalars(statement))
    settled: list[str] = []
    include_unleased = os.environ.get(UNLEASED_RECOVERY_ENV) == "1"
    try:
        for attempt in running:
            if attempt.executor_id:
                if not _reap(db, attempt, now=now, engine=engine):
                    continue
            elif include_unleased:
                if not _reap_unleased(db, attempt):
                    continue
            else:
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
    """Drop in-process claims and mint a new executor id (a test double for a new process).

    Does not release an OS or PostgreSQL lock already held for the previous
    id. Production code does not call this. Hermetic tests do not start the
    heartbeat thread.
    """

    global EXECUTOR_ID
    with _lock:
        _claimed.clear()
    EXECUTOR_ID = uuid4().hex
    return EXECUTOR_ID


def _reap(db: Session, attempt: AttemptRow, *, now: datetime, engine) -> bool:
    if not attempt.executor_id or executor_is_live(attempt, now=now, engine=engine, db=db):
        return False
    if not _lock_attempt_nowait(db, attempt.id):
        return False
    conditions = [
        AttemptRow.id == attempt.id,
        AttemptRow.status == AttemptState.RUNNING.value,
        AttemptRow.executor_id == attempt.executor_id,
    ]
    # A foreign row is reaped only after its process lock was observed free
    # (or, with no lock, after the heartbeat we read was already stale).
    # Matching that heartbeat stops a refresh that committed in between.
    # A lock that appeared since the liveness check wins: do not reap.
    if attempt.executor_id != EXECUTOR_ID:
        held = process_holds_lease(engine, attempt.executor_id)
        if held is True:
            return False
    else:
        held = None
    if attempt.executor_id != EXECUTOR_ID and held is None:
        if attempt.executor_heartbeat_at is None:
            conditions.append(AttemptRow.executor_heartbeat_at.is_(None))
        else:
            conditions.append(AttemptRow.executor_heartbeat_at == attempt.executor_heartbeat_at)
    transition_attempt(AttemptState.RUNNING, AttemptState.FAILED)
    result = db.execute(
        update(AttemptRow)
        .where(*conditions)
        .values(status=AttemptState.FAILED.value, error_class=attempt.error_class or "killed")
        .execution_options(synchronize_session=False)
    )
    if result.rowcount != 1:
        db.expire(attempt)
        return False
    db.expire(attempt)
    db.refresh(attempt)
    return True


def _reap_unleased(db: Session, attempt: AttemptRow) -> bool:
    """Fail a running Attempt that never took a lease. The caller opted in."""

    transition_attempt(AttemptState.RUNNING, AttemptState.FAILED)
    result = db.execute(
        update(AttemptRow)
        .where(
            AttemptRow.id == attempt.id,
            AttemptRow.status == AttemptState.RUNNING.value,
            AttemptRow.executor_id.is_(None),
        )
        .values(status=AttemptState.FAILED.value, error_class=attempt.error_class or "killed")
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
    """Refresh one Attempt at a time so a locked row cannot stall the others.

    A single ``UPDATE ... WHERE id IN (...)`` takes the locks in order. If
    the first id is busy for a long transaction, the rest of the batch waits
    and their heartbeats go stale. Each id is its own short transaction, and
    PostgreSQL gives up after :data:`_LOCK_WAIT` instead of waiting.
    """

    with _lock:
        ids = list(_claimed)
    for attempt_id in ids:
        try:
            _heartbeat_one(db, attempt_id)
        except (OperationalError, DBAPIError):
            db.rollback()


def _heartbeat_one(db: Session, attempt_id: str) -> None:
    _set_lock_timeout(db)
    result = db.execute(
        update(AttemptRow)
        .where(
            AttemptRow.id == attempt_id,
            AttemptRow.status == AttemptState.RUNNING.value,
            or_(AttemptRow.executor_id.is_(None), AttemptRow.executor_id == current_executor_id()),
        )
        .values(executor_id=current_executor_id(), executor_heartbeat_at=func.now())
        .execution_options(synchronize_session=False)
    )
    db.commit()
    if result.rowcount != 1:
        with _lock:
            _claimed.discard(attempt_id)


def _lock_attempt_nowait(db: Session, attempt_id: str) -> bool:
    """False when the row is busy. Recovery then leaves it for the holder."""

    try:
        _set_lock_timeout(db)
        locked = db.scalar(
            select(AttemptRow.id).where(AttemptRow.id == attempt_id).with_for_update(nowait=True)
        )
    except (OperationalError, DBAPIError):
        db.rollback()
        return False
    return locked is not None


def _set_lock_timeout(db: Session) -> None:
    bind = db.get_bind()
    if bind is not None and bind.dialect.name == "postgresql":
        db.execute(text(f"SET LOCAL lock_timeout = '{_LOCK_WAIT}'"))


def _heartbeat_fresh_on_database_clock(db: Session, attempt: AttemptRow) -> bool:
    """True when the row's heartbeat is inside the lease on the database clock.

    Host ``utcnow`` is not used. Two machines with skewed clocks otherwise
    expire a lease the database still considers fresh.
    """

    if attempt.executor_heartbeat_at is None:
        return False
    dialect = db.get_bind().dialect.name
    if dialect == "postgresql":
        statement = text(
            "SELECT executor_heartbeat_at >= (CURRENT_TIMESTAMP - INTERVAL '30 seconds') "
            "FROM attempts WHERE id = :id"
        )
    else:
        statement = text(
            "SELECT executor_heartbeat_at >= datetime('now', '-30 seconds') "
            "FROM attempts WHERE id = :id"
        )
    return bool(db.scalar(statement, {"id": attempt.id}))


def _lock_key(executor_id: str) -> int:
    digest = hashlib.sha256(b"sdf-executor-lease:" + executor_id.encode()).digest()
    return int.from_bytes(digest[:8], "big", signed=True)


def _as_utc(value: datetime) -> datetime:
    if value.tzinfo is None:
        return value.replace(tzinfo=timezone.utc)
    return value.astimezone(timezone.utc)
