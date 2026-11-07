"""Settle Attempts left ``running`` with no live executor (ADR 0041).

Liveness is a lease, not ``RuntimeController.recover`` (ADR 0029 only restores
a runtime-event stream). This process treats an Attempt as live while the
``ExecutionService`` that started it is still alive. Another process is live
while its heartbeat is newer than :data:`LEASE_TTL`. Lease expiry does not
prove remote work has stopped: a ``live`` Attempt, or a registered effect
that has not confirmed stop, stays ``running``.
"""

from __future__ import annotations

import os
import threading
import time
from collections.abc import Callable
from datetime import datetime, timedelta, timezone
from uuid import uuid4
from weakref import WeakValueDictionary

from sqlalchemy import select, update
from sqlalchemy.orm import Session, sessionmaker
from sqlalchemy.pool import StaticPool

from .db import AttemptRow, TaskRow
from .impact import EvidenceMode
from .model import AttemptState, TaskState, utcnow
from .observability import get_logger
from .state import transition_attempt, transition_task


# A dead executor is invisible until this elapses. The refresher wakes more
# often than that, so another process is settled within about one lease of
# its last heartbeat.
LEASE_TTL = timedelta(seconds=30)
_HEARTBEAT_INTERVAL_S = 10

def _mint_executor_id() -> str:
    """Unique after fork: the OS pid plus a random id.

    A forking server (uvicorn workers, gunicorn) copies the parent's id into
    every child. The pid makes a copied id fail the check in the child, and
    the uuid keeps two restarts of the same pid distinct.
    """

    return f"{os.getpid()}-{uuid4().hex}"


# Read at call time. ``remint_executor_id`` replaces it in this process.
EXECUTOR_ID = _mint_executor_id()

# attempt id -> the ExecutionService (or other owner) that is running it.
# The value is weak: dropping the owner drops the claim, including when the
# test models a crash by deleting the service in this same process.
_owners: WeakValueDictionary[str, object] = WeakValueDictionary()
# attempt id -> returns True only when that attempt's external effects are
# confirmed finished. A False or an exception is not quiescence. This map is
# the in-process provider handle. It dies with the process. It is not a
# promise that an arbitrary external effect can be exactly-once.
_remote_effects: dict[str, Callable[[], bool]] = {}
_lock = threading.Lock()
_stop = threading.Event()
_supervisor: threading.Thread | None = None

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


class AttemptFenceError(ValueError):
    """This process cannot settle the Attempt (ADR 0041).

    The row was not ``running`` for ``EXECUTOR_ID``. Callers must not write
    the Attempt or its Task after this.
    """

    def __init__(self, attempt_id: str, *, status: str | None, executor_id: str | None):
        self.attempt_id = attempt_id
        self.status = status
        self.executor_id = executor_id
        super().__init__(
            f"attempt {attempt_id} is not running "
            f"(status={status!r}, executor_id={executor_id!r})"
        )


def settle_owned_attempt(db: Session, attempt_id: str, target: str, **columns: object) -> AttemptRow:
    """Set status only while the row is ``running`` for this executor.

    The WHERE clause is the fence. Zero rows leaves the Attempt and the Task
    untouched. This does not commit.
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
        raise AttemptFenceError(attempt_id, status=status, executor_id=owner)
    attempt = db.get(AttemptRow, attempt_id)
    if attempt is not None:
        attempt.status = target
        for key, value in columns.items():
            setattr(attempt, key, value)
    return attempt


def require_owned_running(db: Session, attempt_id: str) -> AttemptRow:
    """Reload the Attempt and refuse unless this executor still has it running.

    Callers that capture files must call this first. A stale identity-map
    status is not enough: recovery may have committed on another session.
    """

    attempt = db.get(AttemptRow, attempt_id, populate_existing=True)
    status = None if attempt is None else attempt.status
    owner = None if attempt is None else attempt.executor_id
    if attempt is None or status != AttemptState.RUNNING.value or owner != EXECUTOR_ID:
        _log.warning(
            "refusing to settle attempt %s: status=%s executor_id=%s this_executor=%s",
            attempt_id, status, owner, EXECUTOR_ID,
        )
        raise AttemptFenceError(attempt_id, status=status, executor_id=owner)
    return attempt


def executor_is_live(attempt: AttemptRow, *, now: datetime | None = None) -> bool:
    """True when some executor still owns this running Attempt.

    A local owner that is still in ``_owners`` is authoritative for this
    process, even when its heartbeat is older than the lease (the refresher
    may be dead). A dropped local owner is dead even if the heartbeat is
    fresh. Another process is live only while its heartbeat is inside the lease.
    """

    now = now or utcnow()
    with _lock:
        held_here = attempt.id in _owners
    if held_here and attempt.executor_id == EXECUTOR_ID:
        return True
    if not attempt.executor_id or attempt.executor_heartbeat_at is None:
        return False
    age = now - _as_utc(attempt.executor_heartbeat_at)
    if age > LEASE_TTL:
        return False
    if attempt.executor_id == EXECUTOR_ID:
        return False
    return True


def remint_executor_id() -> str:
    """Mint an executor id for this OS process and drop copied claims."""

    global EXECUTOR_ID
    with _lock:
        _owners.clear()
    EXECUTOR_ID = _mint_executor_id()
    return EXECUTOR_ID


def ensure_process_executor() -> str:
    """Remint when this process inherited another process's id.

    Lifespan startup calls this before recovery. A child whose copied id
    starts with the parent's pid does not match ``os.getpid()``.
    """

    prefix = f"{os.getpid()}-"
    if not str(EXECUTOR_ID).startswith(prefix):
        return remint_executor_id()
    return EXECUTOR_ID


def _after_fork_in_child() -> None:
    """Forked workers must not keep the parent's id or its owner map."""

    global _supervisor
    _stop.set()
    _supervisor = None
    _stop.clear()
    remint_executor_id()


if hasattr(os, "register_at_fork"):
    os.register_at_fork(after_in_child=_after_fork_in_child)


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
    """Refresh leases, then sweep orphans, until shutdown.

    A supervisor restarts the refresher if that thread dies. The first sweep
    is still the caller's: startup calls :func:`recover_orphaned_attempts`.
    """

    global _supervisor
    if not heartbeat_refresher_enabled(session_factory):
        return
    with _lock:
        if _supervisor is not None and _supervisor.is_alive():
            return
        _stop.clear()
        _supervisor = threading.Thread(
            target=_supervise, args=(session_factory,), name="sdf-executor-lease-supervisor", daemon=True,
        )
        _supervisor.start()


def shutdown_executor_heartbeat(timeout: float = 5.0) -> None:
    """Stop the refresher and join it so shutdown does not leave the thread running."""

    _stop.set()
    thread = _supervisor
    if thread is not None and thread.is_alive() and thread is not threading.current_thread():
        thread.join(timeout)


def register_remote_effect(attempt_id: str, confirm_stopped: Callable[[], bool]) -> None:
    """Remember that ``attempt_id`` can still cause effects outside the row.

    ``confirm_stopped`` returns True only when those effects are confirmed
    finished. False, or any exception, means recovery must leave the Attempt
    ``running`` and must not free its Task for another execution. Lease expiry
    is not a substitute for this confirmation.

    The callback is process-local. A restarted process does not have it. A
    ``live`` Attempt with no callback is therefore also unresolved: this
    process will not claim the provider sandbox is gone.
    """

    with _lock:
        _remote_effects[attempt_id] = confirm_stopped


def clear_remote_effect(attempt_id: str) -> None:
    """Drop a process-local effect registration. Does not touch the row."""

    with _lock:
        _remote_effects.pop(attempt_id, None)


def _remote_effect_blocks_settle(attempt: AttemptRow) -> bool:
    """True when this Attempt must not be failed merely because its lease expired."""

    with _lock:
        confirm_stopped = _remote_effects.get(attempt.id)
    if confirm_stopped is not None:
        try:
            stopped = bool(confirm_stopped())
        except Exception:
            _log.warning(
                "remote effect for attempt %s was not confirmed stopped",
                attempt.id, exc_info=True,
            )
            return True
        if not stopped:
            return True
        clear_remote_effect(attempt.id)
        return False
    # confirm_live commits this before teardown. After the creating process is
    # gone there is no sandbox id here and no provider call. Do not treat the
    # lease timeout as proof the sandbox has stopped.
    return attempt.evidence_mode == EvidenceMode.LIVE.value


def compare_and_swap_attempt(
    db: Session,
    attempt: AttemptRow,
    *,
    from_status: AttemptState,
    to_status: AttemptState,
    extra: dict[str, object] | None = None,
    observed_lease: tuple[str | None, datetime | None] | None = None,
) -> bool:
    """Persist ``to_status`` only if the row is still ``from_status``.

    When ``observed_lease`` is set, the update also requires the durable
    ``executor_id`` and ``executor_heartbeat_at`` to still be that snapshot.
    A heartbeat renewal committed after the SELECT then matches zero rows.
    Callers that only fence status (cancel) omit it.

    Returns True when this session won. On loss, expires ``attempt`` so a
    later flush cannot overwrite the durable winner.
    """

    transition_attempt(from_status, to_status)
    values: dict[str, object] = {"status": to_status.value}
    if extra:
        values.update(extra)
    clauses = [AttemptRow.id == attempt.id, AttemptRow.status == from_status.value]
    if observed_lease is not None:
        executor_id, heartbeat = observed_lease
        clauses.append(AttemptRow.executor_id.is_not_distinct_from(executor_id))
        clauses.append(AttemptRow.executor_heartbeat_at.is_not_distinct_from(heartbeat))
    result = db.execute(
        update(AttemptRow)
        .where(*clauses)
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
    """Fail running Attempts whose executor is gone and whose effects are confirmed stopped.

    A stale or missing lease is not enough when the Attempt can still cause
    effects. A held local owner, a fresh foreign heartbeat, a heartbeat that
    changed after this SELECT, a ``live`` Attempt this process cannot see a
    stopped sandbox for, or a registered effect whose stop is not confirmed,
    all stay ``running``. The Task stays ``running`` with them, so a new
    dispatch cannot enter ``ready``.

    Attempt, when it is settled: ``running`` to ``failed``. ``error_class``
    is ``killed`` when it was empty. The update matches the snapshot
    ``executor_id`` and ``executor_heartbeat_at`` as well as status, so a
    renewal between the SELECT and the UPDATE does not lose.

    Task: ``running`` to ``evaluating`` to ``failed``, and only when no
    Attempt of that Task is still running. No new transitions are added.

    A second call writes nothing for an Attempt it already settled, and it
    does not settle one it left running.
    """

    now = now or utcnow()
    running = list(db.scalars(select(AttemptRow).where(AttemptRow.status == AttemptState.RUNNING.value)))
    live_tasks: set[str] = set()
    settled: list[str] = []
    for attempt in running:
        # Captured before liveness checks. A renewal that commits inside that
        # window must not be the value this UPDATE requires.
        observed_lease = (attempt.executor_id, attempt.executor_heartbeat_at)
        if executor_is_live(attempt, now=now) or _remote_effect_blocks_settle(attempt):
            live_tasks.add(attempt.task_id)
            continue
        extra = {"error_class": "killed"} if attempt.error_class is None else None
        if not compare_and_swap_attempt(
            db, attempt, from_status=AttemptState.RUNNING, to_status=AttemptState.FAILED, extra=extra,
            observed_lease=observed_lease,
        ):
            fresh = db.get(AttemptRow, attempt.id, populate_existing=True)
            if fresh is not None and fresh.status == AttemptState.RUNNING.value:
                live_tasks.add(attempt.task_id)
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

    return remint_executor_id()


def _supervise(session_factory: sessionmaker) -> None:
    while not _stop.is_set():
        worker = threading.Thread(
            target=_refresh_loop, args=(session_factory,), name="sdf-executor-lease", daemon=True,
        )
        worker.start()
        while worker.is_alive() and not _stop.is_set():
            worker.join(timeout=0.5)
        if _stop.is_set():
            worker.join(timeout=2)
            return
        _log.warning("executor lease refresher stopped; restarting")


def _refresh_loop(session_factory: sessionmaker) -> None:
    while not _stop.is_set():
        if _stop.wait(_HEARTBEAT_INTERVAL_S):
            return
        db = session_factory()
        try:
            _write_heartbeats(db)
            recover_orphaned_attempts(db)
        except Exception:  # noqa: BLE001 - the supervisor restarts a dead refresher
            db.rollback()
            _log.warning("executor lease sweep failed", exc_info=True)
            return
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
        db.execute(
            update(AttemptRow)
            .where(
                AttemptRow.id == attempt_id,
                AttemptRow.status == AttemptState.RUNNING.value,
                AttemptRow.executor_id == EXECUTOR_ID,
            )
            .values(executor_heartbeat_at=now)
            .execution_options(synchronize_session=False)
        )
    db.commit()


def _as_utc(value: datetime) -> datetime:
    if value.tzinfo is None:
        return value.replace(tzinfo=timezone.utc)
    return value.astimezone(timezone.utc)
