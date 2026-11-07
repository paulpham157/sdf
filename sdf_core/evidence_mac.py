"""Content MAC for an Evidence row (ADR 0043).

The MAC is an HMAC written once, at insert. ``verify_attempt_revision``
recomputes the current payload. A same-store rewrite of the verdict, the
bound artifact sha256, the Attempt/revision binding, or the acceptance
metadata below fails that check unless the MAC is recomputed with the key
this process holds.

Current payload (``mac_version`` 2) binds:

* identity of the row and the record it belongs to: id, attempt id,
  revision id, result-tree sha256, objective id, artifact ref, and that
  artifact's sha256;
* the verdict: kind, status, command, exit code, confidence, criterion;
* acceptance metadata: evidence class and mode, producer and verifier
  identity and role, subject artifact sha256, oracle digest, environment
  digest, oracle visibility, input seed, and the measured, collected, and
  expiry timestamps.

Format 1 is the previous payload (verdict and binding only). A stored MAC
that still matches format 1 is not current. Verification reports that and
does not re-sign the row. A missing or malformed MAC is not current either.
Neither case is a pass.

The key is ``SDF_EVIDENCE_MAC_KEY`` when that variable is set to a non-empty
value. Hermetic tests leave it unset and use a fixed development key so
insert and verify agree in one process. That key is a public constant in
this module, not a secret, and it is not in the database. A MAC match under
the development key is not authenticity against an attacker who knows
``_DEV_KEY``: they can recompute the HMAC after any rewrite. A deployment
that needs that claim sets ``SDF_EVIDENCE_MAC_KEY``. With a configured key,
a MAC produced under the development key does not verify.

The MAC binds the verifier identity string that was stored. It does not
establish that the named verifier is an independent party, or that the
string is anyone other than what the key holder wrote. Acceptance still
compares that string to the Attempt's agent; the MAC does not do that
comparison.
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
CURRENT_MAC_VERSION = 2
LEGACY_MAC_VERSION = 1


def evidence_mac_key_source() -> str:
    """``configured`` when ``SDF_EVIDENCE_MAC_KEY`` is set, else ``development``.

    ``development`` means the public ``_DEV_KEY`` fallback. A match under that
    key is not an authenticity claim against someone who knows the constant.
    """

    configured = os.environ.get("SDF_EVIDENCE_MAC_KEY")
    if configured:
        return "configured"
    return "development"


def evidence_mac_key() -> bytes:
    configured = os.environ.get("SDF_EVIDENCE_MAC_KEY")
    if configured:
        return hashlib.sha256(configured.encode("utf-8")).digest()
    return hashlib.sha256(_DEV_KEY).digest()


def _canonical_timestamp(value: Any) -> str | None:
    """UTC ISO-8601. Naive values are UTC, matching ``utcnow`` stored by SQLite."""

    if value is None:
        return None
    if not isinstance(value, datetime):
        return str(value)
    if value.tzinfo is None:
        value = value.replace(tzinfo=timezone.utc)
    else:
        value = value.astimezone(timezone.utc)
    return value.isoformat()


def _payload_v1(row: Any, artifact_sha256: str | None) -> dict[str, Any]:
    """Format 1, unchanged, so a previously stamped row can be recognized."""

    return {
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


def _evidence_mode(row: Any) -> str:
    mode = getattr(row, "evidence_mode", None)
    # The column default is synthetic. Stamp and verify must agree when the
    # ORM has not copied that default onto the instance yet.
    if mode is None:
        return "synthetic"
    return mode


def _payload_v2(row: Any, artifact_sha256: str | None) -> dict[str, Any]:
    payload = _payload_v1(row, artifact_sha256)
    payload.update(
        {
            "mac_version": CURRENT_MAC_VERSION,
            "evidence_class": getattr(row, "evidence_class", None),
            "evidence_mode": _evidence_mode(row),
            "producer_identity": getattr(row, "producer_identity", None),
            "producer_role": getattr(row, "producer_role", None),
            "verifier_identity": getattr(row, "verifier_identity", None),
            "verifier_role": getattr(row, "verifier_role", None),
            "subject_artifact_sha256": getattr(row, "subject_artifact_sha256", None),
            "oracle_digest": getattr(row, "oracle_digest", None),
            "environment_digest": getattr(row, "environment_digest", None),
            "oracle_visibility": getattr(row, "oracle_visibility", None),
            "input_seed": getattr(row, "input_seed", None),
            "measured_at": _canonical_timestamp(getattr(row, "measured_at", None)),
            "collected_at": _canonical_timestamp(getattr(row, "collected_at", None)),
            "expires_at": _canonical_timestamp(getattr(row, "expires_at", None)),
        }
    )
    return payload


def evidence_content_mac(
    row: Any,
    *,
    artifact_sha256: str | None,
    version: int = CURRENT_MAC_VERSION,
) -> str:
    """Hex HMAC-SHA256 of the requested payload version for ``row``."""

    if version == CURRENT_MAC_VERSION:
        payload = _payload_v2(row, artifact_sha256)
    elif version == LEGACY_MAC_VERSION:
        payload = _payload_v1(row, artifact_sha256)
    else:
        raise ValueError(f"unknown evidence content mac version {version}")
    canonical = json.dumps(payload, sort_keys=True, separators=(",", ":"), default=str).encode("utf-8")
    return hmac.new(evidence_mac_key(), canonical, hashlib.sha256).hexdigest()


def content_mac_problem(row: Any, *, artifact_sha256: str | None) -> str | None:
    """None when ``row.content_mac`` is a current-format MAC of these bytes.

    A missing, malformed, or format-1 MAC is a problem. Format 1 is reported
    as old and is not rewritten. A current MAC that does not match is a problem.
    """

    row_id = getattr(row, "id", None) or "?"
    mac = getattr(row, "content_mac", None)
    if not isinstance(mac, str) or not _HEX64.fullmatch(mac):
        return f"evidence {row_id} has no current content mac and is not authentic"
    current = evidence_content_mac(row, artifact_sha256=artifact_sha256, version=CURRENT_MAC_VERSION)
    if hmac.compare_digest(current, mac):
        return None
    legacy = evidence_content_mac(row, artifact_sha256=artifact_sha256, version=LEGACY_MAC_VERSION)
    if hmac.compare_digest(legacy, mac):
        return (
            f"evidence {row_id} content mac is an old format that does not bind "
            "acceptance metadata; it was not re-signed"
        )
    return (
        f"evidence {row_id} content mac does not match its stored verdict, "
        "acceptance metadata, or attempt binding"
    )
