"""Deterministic dispatch key for a keyless ``POST /tasks/{id}/run`` (ADR 0041).

A missing ``dispatch_key`` is not a new Attempt. The same task and the same
logical request always hash to the same key, and ADR 0031 replays that key.
"""

from __future__ import annotations

import hashlib
import json
from typing import Any


# Bump when the canonical payload changes. Old keys then miss and a repeated
# keyless request would start a new Attempt, so a change belongs in an ADR.
_CANONICAL_VERSION = 1


def derived_dispatch_key(
    *,
    task_id: str,
    fixture_dir: str,
    instructions: str,
    criterion_checks: dict[str, list[str]] | None,
    validation_target_kind: str | None,
    validation_target_id: str | None,
    parent_attempt_id: str | None,
    model_tier: str | None,
) -> str:
    """Hash the fields that identify one logical execution of ``task_id``.

    ``fixture_dir`` is the resolved directory the server will run, not the
    raw request string, so two spellings of the same directory are one
    execution. Fixture file contents are not included: a client retry of the
    same request must replay even if those bytes changed underneath it. A
    deliberate new Attempt sends an explicit ``dispatch_key``.

    ``model_tier`` omitted on a first Attempt is ``basic``, the tier
    ``retry_tier`` would store. Omitted on a retry means "inherit the
    parent", which is a different execution from an explicit tier.

    A validation target counts only when both kind and id are present, matching
    ``run_task``. Check-id list order is significant. Dict key order is not.
    """

    if parent_attempt_id is None and model_tier is None:
        tier: str | None = "basic"
    else:
        tier = model_tier
    if validation_target_kind and validation_target_id:
        kind: str | None = validation_target_kind
        target: str | None = validation_target_id
    else:
        kind, target = None, None
    payload: dict[str, Any] = {
        "v": _CANONICAL_VERSION,
        "task_id": task_id,
        "fixture_dir": fixture_dir,
        "instructions": instructions,
        "criterion_checks": criterion_checks,
        "validation_target_kind": kind,
        "validation_target_id": target,
        "parent_attempt_id": parent_attempt_id,
        "model_tier": tier,
    }
    encoded = json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    digest = hashlib.sha256(encoded.encode("utf-8")).hexdigest()
    return f"keyless-{digest}"
