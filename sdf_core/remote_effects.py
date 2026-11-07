"""Durable provider-sandbox lifecycle for one Attempt.

Process-local create counts die on restart. These rows do not. ``absent`` is
written only when a caller passes proof from a lookup other than the kill
return value. A create that never recorded an id stays ``creating`` and blocks
a new dispatch. Nothing here clears that row.
"""

from __future__ import annotations

from collections.abc import Callable, Mapping
from dataclasses import dataclass
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


@dataclass(frozen=True)
class ReconcileResult:
    resolved: bool
    blocked: str | None
    sandboxes: tuple[dict[str, str | None], ...]


def open_remote_sandbox(db: Session, attempt_id: str, provider: str) -> str:
    """Commit a ``creating`` row before the provider call. Returns its id."""

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


def probe_from_environ(environ: Mapping[str, str] | None = None) -> tuple[Kill | None, Absent | None]:
    """E2B lookup used outside the hermetic suite.

    ``Sandbox.kill`` returning true is not treated as absence. Absence is
    ``Sandbox.get_info`` raising not-found. Any other error is not absence.
    Daytona has no lookup wired here. Hermetic runs do not call the provider.
    """

    import os

    env = os.environ if environ is None else environ
    if not isinstance(env, Mapping):
        return None, None
    # The process flag wins over a copied mapping. Unit tests pass a fake key
    # in that mapping and must not call the provider.
    if env.get("SDF_TEST_HERMETIC") == "1" or os.environ.get("SDF_TEST_HERMETIC") == "1":
        return None, None
    api_key = (env.get("E2B_API_KEY") or "").strip()
    if not api_key:
        return None, None
    domain = (env.get("E2B_DOMAIN") or "").strip() or None

    def kill(provider: str, sandbox_id: str) -> None:
        if provider not in {"e2b", "e2b-box"}:
            raise RuntimeError(f"no kill wired for provider {provider}")
        from e2b import Sandbox

        opts: dict[str, str] = {"api_key": api_key}
        if domain:
            opts["domain"] = domain
        Sandbox.kill(sandbox_id, **opts)

    def absent(provider: str, sandbox_id: str) -> bool:
        if provider not in {"e2b", "e2b-box"}:
            return False
        from e2b import Sandbox, SandboxNotFoundException

        opts = {"api_key": api_key}
        if domain:
            opts["domain"] = domain
        try:
            Sandbox.get_info(sandbox_id, **opts)
        except SandboxNotFoundException:
            return True
        except Exception:
            return False
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
