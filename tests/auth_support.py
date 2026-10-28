"""Helpers that mint ephemeral API tokens for tests (ADR 0015)."""

from __future__ import annotations

import uuid
from typing import Iterable

from fastapi.testclient import TestClient

from sdf_core import auth
from sdf_core.api import SessionLocal, app


def issue_token(roles: Iterable[str], principal: str | None = None, *, kind: str = "human",
                expires_in_days: int = 1, session_factory=SessionLocal) -> tuple[str, str]:
    """Return (token_id, plaintext) for a fresh token in the API's database."""
    principal = principal or f"user-{uuid.uuid4().hex[:8]}"
    with session_factory() as db:
        row, plaintext = auth.create_token(db, principal=principal, roles=list(roles), created_by="test",
                                           kind=kind, expires_in_days=expires_in_days)
        db.commit()
        return row.id, plaintext


def bearer(plaintext: str) -> dict[str, str]:
    return {"Authorization": f"Bearer {plaintext}"}


def client_as(*roles: str, principal: str | None = None, kind: str = "human", **kwargs) -> TestClient:
    _, plaintext = issue_token(roles or ("admin",), principal, kind=kind)
    return TestClient(app, headers=bearer(plaintext), **kwargs)


def admin_client() -> TestClient:
    return client_as("admin", principal="test-admin")
