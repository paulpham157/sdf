"""Durable provider-sandbox lifecycle for one Attempt.

Process-local create counts die on restart. These rows do not. ``absent`` is
written only when a caller passes proof from a lookup other than the kill
return value. A create that never recorded an id stays ``creating`` and blocks
a new dispatch. Nothing here clears that row.
"""

from __future__ import annotations

from collections.abc import Callable, Mapping
from dataclasses import dataclass
from typing import TypeVar
from uuid import uuid4

from sqlalchemy import select
from sqlalchemy.orm import Session

from .db import AttemptRemoteSandboxRow, AttemptRow
from .model import utcnow

PHASE_CREATING = "creating"
PHASE_IDENTIFIED = "identified"
PHASE_ABSENT = "absent"
REMOTE_UNCERTAIN = "uncertain"
REMOTE_STOPPED = "stopped"

Kill = Callable[[str, str], None]
Absent = Callable[[str, str], bool]
SessionFactory = Callable[[], Session]
_T = TypeVar("_T")


@dataclass(frozen=True)
class ReconcileResult:
    resolved: bool
    blocked: str | None
    sandboxes: tuple[dict[str, str | None], ...]


def in_new_session(session_factory: SessionFactory, fn: Callable[[Session], _T]) -> _T:
    """Open a session, run ``fn``, and close it.

    ``fn`` commits its own writes. The factory must not be a session that
    already holds a transaction on the same SQLite file. ``LiveRunAdapter``
    keeps the run's session for that reason and does not call this.
    """

    db = session_factory()
    try:
        return fn(db)
    finally:
        db.close()


def open_remote_sandbox(db: Session, attempt_id: str, provider: str) -> str:
    """Commit a ``creating`` row on ``db`` before the provider call. Returns its id."""

    attempt = db.get(AttemptRow, attempt_id)
    if attempt is None:
        raise RuntimeError(f"cannot record a remote effect for missing attempt {attempt_id}")
    if not provider.strip():
        raise RuntimeError("remote sandbox provider must be non-empty")
    effect_id = f"RS-{uuid4().hex[:16]}"
    db.add(AttemptRemoteSandboxRow(
        id=effect_id, attempt_id=attempt_id, provider=provider.strip()[:32],
        sandbox_id=None, phase=PHASE_CREATING, created_at=utcnow(),
    ))
    attempt.remote_effect = REMOTE_UNCERTAIN
    db.commit()
    return effect_id


def identify_remote_sandbox(db: Session, effect_id: str, sandbox_id: str) -> None:
    """Record the id the provider returned. Does not mean the sandbox is gone."""

    if not sandbox_id.strip():
        raise RuntimeError("sandbox id must be non-empty")
    row = db.get(AttemptRemoteSandboxRow, effect_id)
    if row is None:
        raise RuntimeError(f"remote sandbox row not found: {effect_id}")
    if row.phase == PHASE_ABSENT:
        return
    row.sandbox_id = sandbox_id.strip()[:120]
    row.phase = PHASE_IDENTIFIED
    db.commit()


def observe_remote_sandbox(db: Session, attempt_id: str, provider: str, sandbox_id: str) -> str:
    """Add an identified row for an id an operator found.

    This does not delete or resolve a ``creating`` row that never received an id.
    """

    attempt = db.get(AttemptRow, attempt_id)
    if attempt is None:
        raise RuntimeError(f"attempt not found: {attempt_id}")
    if not sandbox_id.strip() or not provider.strip():
        raise RuntimeError("observed sandbox requires a provider and an id")
    effect_id = f"RS-{uuid4().hex[:16]}"
    db.add(AttemptRemoteSandboxRow(
        id=effect_id, attempt_id=attempt_id, provider=provider.strip()[:32],
        sandbox_id=sandbox_id.strip()[:120], phase=PHASE_IDENTIFIED, created_at=utcnow(),
    ))
    attempt.remote_effect = REMOTE_UNCERTAIN
    db.commit()
    return effect_id


def verify_remote_sandbox_absent(db: Session, effect_id: str, *, absent: bool) -> bool:
    """Mark ``absent`` only when ``absent`` is the result of a separate lookup.

    A row with no sandbox id is never marked absent. ``absent=False`` changes nothing.
    """

    if not absent:
        return False
    row = db.get(AttemptRemoteSandboxRow, effect_id)
    if row is None or not row.sandbox_id or row.phase == PHASE_CREATING:
        return False
    row.phase = PHASE_ABSENT
    db.commit()
    refresh_remote_effect(db, row.attempt_id)
    return True


def refresh_remote_effect(db: Session, attempt_id: str) -> bool:
    """Set ``stopped`` only when every recorded create for the Attempt is ``absent``."""

    attempt = db.get(AttemptRow, attempt_id)
    if attempt is None:
        raise RuntimeError(f"attempt not found: {attempt_id}")
    rows = list(db.scalars(select(AttemptRemoteSandboxRow).where(
        AttemptRemoteSandboxRow.attempt_id == attempt_id,
    )))
    if not rows or any(row.phase != PHASE_ABSENT for row in rows):
        return False
    attempt.remote_effect = REMOTE_STOPPED
    db.commit()
    return True


def unresolved_effect_id(db: Session, task_id: str) -> str | None:
    """An Attempt-sandbox row on this Task that is not verified absent."""

    return db.scalar(
        select(AttemptRemoteSandboxRow.id)
        .join(AttemptRow, AttemptRow.id == AttemptRemoteSandboxRow.attempt_id)
        .where(
            AttemptRow.task_id == task_id,
            AttemptRemoteSandboxRow.phase != PHASE_ABSENT,
        )
    )


def sandbox_rows(db: Session, attempt_id: str) -> list[AttemptRemoteSandboxRow]:
    return list(db.scalars(
        select(AttemptRemoteSandboxRow)
        .where(AttemptRemoteSandboxRow.attempt_id == attempt_id)
        .order_by(AttemptRemoteSandboxRow.created_at, AttemptRemoteSandboxRow.id)
    ))


def release_verified_sandboxes(db: Session, attempt_id: str, environ: Mapping[str, str] | None) -> None:
    """After this process's own stop attempt, commit only verified absence.

    Uses ``db`` and commits inside refresh or reconcile. When no lookup is
    configured, rows that are not already ``absent`` stay as they are. The
    kill return value is not written.
    """

    kill, absent = probe_from_environ(environ)
    if absent is None:
        from .recovery import note_remote_effect_stopped

        note_remote_effect_stopped(db, attempt_id)
        return
    reconcile_attempt(db, attempt_id, kill=kill, absent=absent)


def absence_for(environ: Mapping[str, str] | None, provider: str) -> Callable[[str], bool] | None:
    """One-id lookup for ``provider``, or None when this process must not call out."""

    _kill, lookup = probe_from_environ(environ)
    if lookup is None:
        return None

    def absent(sandbox_id: str) -> bool:
        return lookup(provider, sandbox_id)

    return absent


def reconcile_attempt(
    db: Session,
    attempt_id: str,
    *,
    kill: Kill | None,
    absent: Absent | None,
) -> ReconcileResult:
    """Kill known ids best-effort, then trust only ``absent``.

    ``creating`` rows with no id are left in place. Kill's return value is ignored.
    Without ``absent``, no row changes.
    """

    rows = sandbox_rows(db, attempt_id)
    if absent is None:
        return _result(db, attempt_id, rows, blocked="probe_unavailable")
    for row in rows:
        if row.phase != PHASE_IDENTIFIED or not row.sandbox_id:
            continue
        if kill is not None:
            try:
                kill(row.provider, row.sandbox_id)
            except Exception:
                pass
        try:
            gone = bool(absent(row.provider, row.sandbox_id))
        except Exception:
            gone = False
        verify_remote_sandbox_absent(db, row.id, absent=gone)
    rows = sandbox_rows(db, attempt_id)
    blocked = None
    if any(row.phase == PHASE_CREATING or not row.sandbox_id for row in rows):
        blocked = "unknown_create"
    elif any(row.phase != PHASE_ABSENT for row in rows):
        blocked = "still_present"
    return _result(db, attempt_id, rows, blocked=blocked)


def daytona_get_is_absent(get: Callable[[str], object], sandbox_id: str) -> bool:
    """True only when ``Daytona.get`` reports the sandbox gone.

    The SDK maps HTTP 404 to ``DaytonaNotFoundError``. A returned sandbox is
    absent only in state ``destroyed``. ``stopped`` and ``paused`` can be
    started again. ``unknown``, ``error``, authentication failures, and any
    other exception are not absence. This does not list sandboxes, so a
    create that never stored an id is not resolved here.
    """

    try:
        sandbox = get(sandbox_id)
    except Exception as exc:
        if getattr(exc, "status_code", None) == 404 or type(exc).__name__ == "DaytonaNotFoundError":
            return True
        return False
    state = getattr(sandbox, "state", None)
    value = getattr(state, "value", state)
    return str(value) == "destroyed"


def probe_from_environ(environ: Mapping[str, str] | None = None) -> tuple[Kill | None, Absent | None]:
    """Provider lookup used outside the hermetic suite.

    E2B absence is ``Sandbox.get_info`` raising not-found. ``Sandbox.kill``
    returning true is not absence. Daytona absence is :func:`daytona_get_is_absent`
    after ``Daytona.get``. ``Daytona.delete`` returning is not absence.
    Hermetic runs do not call a provider. A missing key for that provider
    is not absence.
    """

    import os

    env = os.environ if environ is None else environ
    if not isinstance(env, Mapping):
        return None, None
    # The process flag wins over a copied mapping. Unit tests pass a fake key
    # in that mapping and must not call the provider.
    if env.get("SDF_TEST_HERMETIC") == "1" or os.environ.get("SDF_TEST_HERMETIC") == "1":
        return None, None
    e2b_key = (env.get("E2B_API_KEY") or "").strip()
    daytona_key = (env.get("DAYTONA_API_KEY") or "").strip()
    if not e2b_key and not daytona_key:
        return None, None
    domain = (env.get("E2B_DOMAIN") or "").strip() or None

    def e2b_kwargs() -> dict[str, str]:
        opts = {"api_key": e2b_key}
        if domain:
            opts["domain"] = domain
        return opts

    def daytona_client():
        from daytona import Daytona, DaytonaConfig

        kwargs: dict[str, str] = {"api_key": daytona_key}
        if (env.get("DAYTONA_API_URL") or "").strip():
            kwargs["api_url"] = env["DAYTONA_API_URL"].strip()
        if (env.get("DAYTONA_TARGET") or "").strip():
            kwargs["target"] = env["DAYTONA_TARGET"].strip()
        return Daytona(DaytonaConfig(**kwargs))

    def kill(provider: str, sandbox_id: str) -> None:
        if provider in {"e2b", "e2b-box"}:
            if not e2b_key:
                raise RuntimeError("no E2B key for kill")
            from e2b import Sandbox

            Sandbox.kill(sandbox_id, **e2b_kwargs())
            return
        if provider == "daytona":
            if not daytona_key:
                raise RuntimeError("no Daytona key for delete")
            client = daytona_client()
            # delete() accepts the request and returns. That is not absence.
            client.delete(client.get(sandbox_id), timeout=60, wait=False)
            return
        raise RuntimeError(f"no kill wired for provider {provider}")

    def absent(provider: str, sandbox_id: str) -> bool:
        if provider in {"e2b", "e2b-box"}:
            if not e2b_key:
                return False
            from e2b import Sandbox, SandboxNotFoundException

            try:
                Sandbox.get_info(sandbox_id, **e2b_kwargs())
            except SandboxNotFoundException:
                return True
            except Exception:
                return False
            return False
        if provider == "daytona":
            if not daytona_key:
                return False
            return daytona_get_is_absent(daytona_client().get, sandbox_id)
        return False

    return kill, absent


def _result(db: Session, attempt_id: str, rows: list[AttemptRemoteSandboxRow], *, blocked: str | None) -> ReconcileResult:
    attempt = db.get(AttemptRow, attempt_id)
    resolved = attempt is not None and attempt.remote_effect == REMOTE_STOPPED and blocked is None
    payload = tuple(
        {"id": row.id, "provider": row.provider, "sandbox_id": row.sandbox_id, "phase": row.phase}
        for row in rows
    )
    return ReconcileResult(resolved=resolved, blocked=blocked, sandboxes=payload)
