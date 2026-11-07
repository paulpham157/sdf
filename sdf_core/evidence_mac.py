"""Content MAC for an Evidence row (ADR 0043).

The MAC is an HMAC over the verdict fields and the sha256 of the artifact the
row points at. It is written once, at insert. ``verify_attempt_revision``
recomputes it. A rewrite that changes the status, command, confidence, the
artifact's recorded sha256, or (from version 2) an acceptance column, and
does not recompute the MAC with the server key, no longer verifies.

Version 1 is the migration 0029 payload: verdict fields, bound artifact
sha256, revision binding, and objective id. Version 2 adds the acceptance
columns listed in ``CONTENT_MAC_V2_FIELDS``. New inserts use version 2.
Existing MACs are verified with the payload their stored version names.

The key is ``SDF_EVIDENCE_MAC_KEY`` (at least 32 characters). When it is
unset or shorter, insert and the API refuse to start, and verification does
not fall back to a key. ``SDF_EVIDENCE_MAC_ALLOW_INSECURE_DEV_KEY=1`` is the
only way to use the public ``_DEV_KEY`` constant, and a match under that
constant is not authenticity. The key is never logged. Rotating it is an
operator re-stamp; this module does not rotate or invent a secret.

The MAC binds the stored verifier string. It does not prove that the named
verifier is an independent party.
"""

from __future__ import annotations

import hashlib
import hmac
import json
import os
import re
from datetime import datetime, timezone
from typing import Any

_DEV_KEY = b"sdf-evidence-mac-v1-development"
_HEX64 = re.compile(r"^[0-9a-f]{64}$")
_MIN_KEY_CHARS = 32

CONTENT_MAC_V1 = 1
CONTENT_MAC_V2 = 2
CONTENT_MAC_CURRENT = CONTENT_MAC_V2
CONTENT_MAC_VERSIONS = frozenset({CONTENT_MAC_V1, CONTENT_MAC_V2})

# Acceptance columns covered from version 2 (ADR 0043). Version 1 does not
# include these, so a stored v1 MAC is recomputed without them.
CONTENT_MAC_V2_FIELDS = (
    "verifier_identity",
    "verifier_role",
    "expires_at",
    "oracle_visibility",
    "subject_artifact_sha256",
    "oracle_digest",
    "evidence_class",
    "evidence_mode",
    "producer_identity",
    "producer_role",
    "environment_digest",
    "collected_at",
    "input_seed",
)


class EvidenceMacKeyError(RuntimeError):
    """The Evidence MAC key is missing or too short to stamp or verify."""


def _key_error() -> EvidenceMacKeyError:
    return EvidenceMacKeyError(
        "SDF_EVIDENCE_MAC_KEY must be set to at least 32 characters. "
        "The public development key is not used. "
        "SDF_EVIDENCE_MAC_ALLOW_INSECURE_DEV_KEY=1 is only for a local fixture "
        "and is not an authenticity claim."
    )


def evidence_mac_key_source() -> str:
    """``configured``, ``insecure-development``, or ``missing``.

    A short ``SDF_EVIDENCE_MAC_KEY`` is ``missing``. The allow-flag does not
    override a key that was set. The returned name is safe to log.
    """

    configured = os.environ.get("SDF_EVIDENCE_MAC_KEY")
    if configured:
        if len(configured) >= _MIN_KEY_CHARS:
            return "configured"
        return "missing"
    if os.environ.get("SDF_EVIDENCE_MAC_ALLOW_INSECURE_DEV_KEY") == "1":
        return "insecure-development"
    return "missing"


def require_evidence_mac_key() -> str:
    """Return the key source, or raise when no usable key is configured."""

    source = evidence_mac_key_source()
    if source == "missing":
        raise _key_error()
    return source


def evidence_mac_key() -> bytes:
    source = require_evidence_mac_key()
    if source == "configured":
        return hashlib.sha256(os.environ["SDF_EVIDENCE_MAC_KEY"].encode("utf-8")).digest()
    return hashlib.sha256(_DEV_KEY).digest()


def _canonical_mac_value(value: Any) -> Any:
    """Normalize values so insert-time and load-time encodings match."""

    if isinstance(value, datetime):
        if value.tzinfo is None:
            value = value.replace(tzinfo=timezone.utc)
        else:
            value = value.astimezone(timezone.utc)
        return value.isoformat()
    return value


def resolve_content_mac_version(row: Any, version: int | None = None) -> int | None:
    """Return the MAC version to use for ``row``, or None when it is unset."""

    if version is not None:
        return int(version)
    stored = getattr(row, "content_mac_version", None)
    if stored is None:
        return None
    return int(stored)


def evidence_content_mac(
    row: Any,
    *,
    artifact_sha256: str | None,
    version: int | None = None,
) -> str:
    """Hex HMAC-SHA256 of the fields ``version`` covers on ``row``.

    When ``version`` is omitted, the row's ``content_mac_version`` is used,
    and a row with no version is hashed as the current write version so a
    new insert and a caller that has not set the column yet agree.
    """

    resolved = resolve_content_mac_version(row, version)
    if resolved is None:
        resolved = CONTENT_MAC_CURRENT
    payload: dict[str, Any] = {
        "id": row.id,
        "attempt_id": row.attempt_id,
        "kind": row.kind,
        "status": row.status,
        "command": row.command,
        "exit_code": row.exit_code,
        "confidence": row.confidence,
        "criterion": row.criterion,
        "artifact_ref": row.artifact_ref,
        "artifact_sha256": artifact_sha256,
        "revision_id": row.revision_id,
        "tree_sha256": row.tree_sha256,
        "objective_id": getattr(row, "objective_id", None),
    }
    if resolved >= CONTENT_MAC_V2:
        payload["mac_version"] = CONTENT_MAC_V2
        for field in CONTENT_MAC_V2_FIELDS:
            value = getattr(row, field, None)
            # The column default is synthetic. Stamp often runs before that
            # default is copied onto the instance; a later load returns it.
            if field == "evidence_mode" and value is None:
                value = "synthetic"
            payload[field] = _canonical_mac_value(value)
    canonical = json.dumps(payload, sort_keys=True, separators=(",", ":"), default=str).encode("utf-8")
    return hmac.new(evidence_mac_key(), canonical, hashlib.sha256).hexdigest()


def artifact_sha256_for_content_mac(row: Any) -> str | None:
    """Sha256 bound into the MAC, or ``""`` when a detached row cannot be read."""

    ref = getattr(row, "artifact_ref", None)
    if not ref:
        return None
    from sqlalchemy.orm import object_session

    from .db import ArtifactRow

    session = object_session(row)
    if session is None:
        return ""
    artifact = session.get(ArtifactRow, ref)
    return artifact.sha256 if artifact is not None else None


def stored_content_mac_ok(row: Any) -> bool:
    """True only for a current MAC under a configured key.

    Format 1, a missing MAC, and the public development key are not ok.
    Nothing is rewritten.
    """

    if evidence_mac_key_source() != "configured":
        return False
    if getattr(row, "content_mac_version", None) != CONTENT_MAC_CURRENT:
        return False
    mac = getattr(row, "content_mac", None)
    if not isinstance(mac, str) or not _HEX64.fullmatch(mac):
        return False
    expected = evidence_content_mac(
        row, artifact_sha256=artifact_sha256_for_content_mac(row), version=CONTENT_MAC_CURRENT,
    )
    return hmac.compare_digest(expected, mac)
