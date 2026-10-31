"""Acceptance gate for Verification Evidence (ADR 0034).

An agent reporting completion is not success. A PASS may accept an artifact
only when the Evidence is verification produced by someone other than the
Attempt's agent, bound to that artifact's bytes, and not expired.
"""

from __future__ import annotations

import hashlib
import json
from datetime import datetime, timedelta, timezone
from typing import Any

from .model import utcnow
from .oracle import EMPTY_ORACLE_DIGEST

EVIDENCE_CLASSES = ("execution", "verification", "outcome")
EVIDENCE_CLASS_VERIFICATION = "verification"
EVIDENCE_CLASS_SQL = (
    "evidence_class IS NULL OR evidence_class IN ("
    + ", ".join(f"'{value}'" for value in EVIDENCE_CLASSES)
    + ")"
)

# The deterministic evaluator, never an Attempt's agent (ADR 0002, ADR 0034).
EVALUATOR_IDENTITY = "sdf-evaluator"
EVALUATOR_ROLE = "evaluator"
AGENT_ROLE = "agent"
# Freshness window for evaluator-written verification. Null expires_at on a
# legacy row means "no expiry was recorded", which cannot accept.
VERIFICATION_TTL = timedelta(hours=24)


class AcceptanceError(ValueError):
    """Verification Evidence cannot accept the artifact (ADR 0034)."""


def _aware(value: datetime) -> datetime:
    if value.tzinfo is None:
        return value.replace(tzinfo=timezone.utc)
    return value


def _field(evidence: Any, name: str) -> Any:
    if isinstance(evidence, dict):
        return evidence.get(name)
    return getattr(evidence, name, None)


def check_verification_acceptance(
    evidence: Any,
    *,
    attempt_agent: str,
    artifact_sha256: str,
    now: datetime | None = None,
) -> None:
    """Refuse Verification Evidence that is not independent, not bound, or stale.

    ``attempt_agent`` is the Attempt's ``agent`` (the party that produced the
    workspace). ``artifact_sha256`` is the artifact being accepted, usually
    the diff artifact's recorded sha256.

    Raises :class:`AcceptanceError` with a reason that names the rule.
    """

    evidence_id = _field(evidence, "evidence_id") or _field(evidence, "id") or "?"
    if _field(evidence, "evidence_class") != EVIDENCE_CLASS_VERIFICATION:
        raise AcceptanceError(
            f"evidence {evidence_id} is not verification evidence and cannot accept an artifact"
        )
    verifier = _field(evidence, "verifier_identity")
    role = _field(evidence, "verifier_role")
    if not verifier or role == AGENT_ROLE or verifier == attempt_agent:
        raise AcceptanceError(
            f"verification evidence {evidence_id} verifier {verifier!r} ({role}) is the attempt's agent "
            f"{attempt_agent!r}; the attempt's agent cannot verify its own result"
        )
    subject = _field(evidence, "subject_artifact_sha256")
    if not isinstance(subject, str) or subject != artifact_sha256:
        raise AcceptanceError(
            f"verification evidence {evidence_id} subject_artifact_sha256 {subject} does not match "
            f"the artifact being accepted ({artifact_sha256})"
        )
    oracle = _field(evidence, "oracle_digest")
    if not isinstance(oracle, str) or oracle.strip() == "":
        raise AcceptanceError(
            f"verification evidence {evidence_id} oracle_digest is missing and cannot accept an artifact"
        )
    if oracle == EMPTY_ORACLE_DIGEST:
        raise AcceptanceError(
            f"verification evidence {evidence_id} oracle_digest is the empty digest "
            f"({EMPTY_ORACLE_DIGEST}) and does not cover a registered check"
        )
    expires_at = _field(evidence, "expires_at")
    if expires_at is None:
        raise AcceptanceError(
            f"verification evidence {evidence_id} has no expires_at and requires re-validation"
        )
    if not isinstance(expires_at, datetime):
        raise AcceptanceError(
            f"verification evidence {evidence_id} has an unreadable expires_at and requires re-validation"
        )
    current = _aware(now or utcnow())
    if _aware(expires_at) <= current:
        raise AcceptanceError(
            f"verification evidence {evidence_id} expired at {_aware(expires_at).isoformat()} "
            "and requires re-validation"
        )


def environment_digest(*, command: str, runner: str | None, oracle_digest: str | None) -> str:
    """Sha256 of the evaluator-controlled environment that produced a check.

    The agent's workspace is not an input. Two checks of the same command,
    runner and oracle therefore share a digest on the same interpreter.
    """

    import platform

    payload = json.dumps(
        {
            "command": command,
            "oracle_digest": oracle_digest,
            "platform": platform.platform(),
            "python": platform.python_version(),
            "runner": runner,
        },
        sort_keys=True,
        separators=(",", ":"),
    ).encode()
    return hashlib.sha256(payload).hexdigest()
