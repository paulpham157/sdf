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

The key is ``SDF_EVIDENCE_MAC_KEY`` when that variable is set. Hermetic tests
leave it unset and use a fixed development key so insert and verify agree in
one process. That key is not in the database. A writer who only updates the
evidence row and ``artifacts.sha256`` cannot produce a matching MAC.
"""

from __future__ import annotations

import hashlib
import hmac
import json
import os
from datetime import datetime, timezone
from typing import Any

_DEV_KEY = b"sdf-evidence-mac-v1-development"

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


def evidence_mac_key() -> bytes:
    configured = os.environ.get("SDF_EVIDENCE_MAC_KEY")
    if configured:
        return hashlib.sha256(configured.encode("utf-8")).digest()
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
            payload[field] = _canonical_mac_value(getattr(row, field, None))
    canonical = json.dumps(payload, sort_keys=True, separators=(",", ":"), default=str).encode("utf-8")
    return hmac.new(evidence_mac_key(), canonical, hashlib.sha256).hexdigest()
