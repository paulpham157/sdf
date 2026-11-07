"""Unit tests for the versioned Evidence content MAC (ADR 0043)."""

from __future__ import annotations

import hashlib
import hmac
import json
from datetime import datetime, timezone
from types import SimpleNamespace

import pytest

from sdf_core.evidence_mac import (
    CONTENT_MAC_V1,
    CONTENT_MAC_V2,
    CONTENT_MAC_V2_FIELDS,
    evidence_content_mac,
    evidence_mac_key,
)


def _v1_row(**overrides):
    values = dict(
        id="EV-1",
        attempt_id="A-1",
        kind="evaluation",
        status="PASS",
        command="pytest",
        exit_code=0,
        confidence=1.0,
        criterion="tests pass",
        artifact_ref="ART-1",
        revision_id="REV-1",
        tree_sha256="ab" * 32,
        objective_id=None,
        content_mac_version=CONTENT_MAC_V1,
    )
    values.update(overrides)
    return SimpleNamespace(**values)


def test_v1_payload_matches_the_migration_0029_canonical_bytes():
    row = _v1_row()
    digest = "cd" * 32
    expected_payload = {
        "id": row.id,
        "attempt_id": row.attempt_id,
        "kind": row.kind,
        "status": row.status,
        "command": row.command,
        "exit_code": row.exit_code,
        "confidence": row.confidence,
        "criterion": row.criterion,
        "artifact_ref": row.artifact_ref,
        "artifact_sha256": digest,
        "revision_id": row.revision_id,
        "tree_sha256": row.tree_sha256,
        "objective_id": row.objective_id,
    }
    expected = hmac.new(
        evidence_mac_key(),
        json.dumps(expected_payload, sort_keys=True, separators=(",", ":"), default=str).encode("utf-8"),
        hashlib.sha256,
    ).hexdigest()
    assert evidence_content_mac(row, artifact_sha256=digest, version=CONTENT_MAC_V1) == expected
    # A v2 hash of the same verdict fields must not collide with v1.
    v2_row = _v1_row(content_mac_version=CONTENT_MAC_V2)
    assert evidence_content_mac(v2_row, artifact_sha256=digest, version=CONTENT_MAC_V2) != expected


def test_unset_or_short_mac_key_is_not_the_public_development_key(monkeypatch):
    """A missing or short key must not fall back to the public development constant."""

    monkeypatch.delenv("SDF_EVIDENCE_MAC_KEY", raising=False)
    monkeypatch.delenv("SDF_EVIDENCE_MAC_ALLOW_INSECURE_DEV_KEY", raising=False)
    with pytest.raises(Exception) as missing:
        evidence_mac_key()
    missing_text = str(missing.value)
    assert "SDF_EVIDENCE_MAC_KEY" in missing_text
    assert "sdf-evidence-mac-v1-development" not in missing_text
    monkeypatch.setenv("SDF_EVIDENCE_MAC_KEY", "too-short")
    with pytest.raises(Exception) as short:
        evidence_mac_key()
    short_text = str(short.value)
    assert "too-short" not in short_text
    assert "sdf-evidence-mac-v1-development" not in short_text


def test_v2_covers_each_acceptance_column():
    collected = datetime(2026, 1, 2, 3, 4, 5, tzinfo=timezone.utc)
    base = _v1_row(
        content_mac_version=CONTENT_MAC_V2,
        verifier_identity="sdf-evaluator",
        verifier_role="evaluator",
        expires_at=datetime(2027, 1, 1, tzinfo=timezone.utc),
        oracle_visibility="visible",
        subject_artifact_sha256="11" * 32,
        oracle_digest="22" * 32,
        evidence_class="verification",
        evidence_mode="synthetic",
        producer_identity="agent:test",
        producer_role="agent",
        environment_digest="33" * 32,
        collected_at=collected,
        input_seed="seed-1",
    )
    digest = "cd" * 32
    original = evidence_content_mac(base, artifact_sha256=digest, version=CONTENT_MAC_V2)
    forged = {
        "verifier_identity": "forged-verifier",
        "verifier_role": "stakeholder",
        "expires_at": datetime(2099, 1, 1, tzinfo=timezone.utc),
        "oracle_visibility": "held_out",
        "subject_artifact_sha256": "aa" * 32,
        "oracle_digest": "bb" * 32,
        "evidence_class": "execution",
        "evidence_mode": "live",
        "producer_identity": "forged-producer",
        "producer_role": "human",
        "environment_digest": "cc" * 32,
        "collected_at": datetime(2020, 1, 1, tzinfo=timezone.utc),
        "input_seed": "forged-input-seed",
    }
    assert set(forged) == set(CONTENT_MAC_V2_FIELDS)
    for field, value in forged.items():
        changed = SimpleNamespace(**{**base.__dict__, field: value})
        assert evidence_content_mac(changed, artifact_sha256=digest, version=CONTENT_MAC_V2) != original, field
