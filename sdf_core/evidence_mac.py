"""Content MAC for an Evidence row (ADR 0043).

The MAC is an HMAC over the verdict fields and the sha256 of the artifact the
row points at. It is written once, at insert. ``verify_attempt_revision``
recomputes it. A rewrite that changes the status, command, confidence, or the
artifact's recorded sha256 and does not recompute the MAC with the server key
no longer verifies.

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
from typing import Any

_DEV_KEY = b"sdf-evidence-mac-v1-development"


def evidence_mac_key() -> bytes:
    configured = os.environ.get("SDF_EVIDENCE_MAC_KEY")
    if configured:
        return hashlib.sha256(configured.encode("utf-8")).digest()
    return hashlib.sha256(_DEV_KEY).digest()


def evidence_content_mac(
    row: Any,
    *,
    artifact_sha256: str | None,
) -> str:
    """Hex HMAC-SHA256 of the verdict fields currently stored on ``row``."""

    payload = {
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
    canonical = json.dumps(payload, sort_keys=True, separators=(",", ":"), default=str).encode("utf-8")
    return hmac.new(evidence_mac_key(), canonical, hashlib.sha256).hexdigest()
