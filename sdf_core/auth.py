"""API authentication and authorization (ADR 0015).

Mechanism: opaque bearer tokens ``sdf_<id>.<secret>``.  The id is a public
lookup key; the 256-bit random secret is never stored.  The database keeps
``HMAC-SHA256(pepper, secret)`` where the pepper (``SDF_AUTH_PEPPER``) lives
only in the server environment, so a database dump alone cannot verify a
guess.  The comparison is constant time (``hmac.compare_digest``) and an
unknown id still costs one HMAC, so timing does not reveal which ids exist.

A slow password hash (argon2/bcrypt/scrypt) protects *low-entropy human
passwords* against offline guessing.  These secrets are 256-bit random
values, for which offline guessing is already infeasible; a keyed fast hash
is the standard choice for API tokens and avoids a per-request KDF that would
turn every unauthenticated request into a CPU-exhaustion vector.

Identity always comes from the credential.  Request fields never name the
caller.  Plaintext tokens are returned once by :func:`create_token` and never
logged.
"""

from __future__ import annotations

import hashlib
import hmac
import ipaddress
import os
import re
import secrets
import threading
import time
from collections import deque
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from functools import lru_cache
from typing import Any, Iterable

from sqlalchemy import select
from sqlalchemy.orm import Session

from .credentials import AGENT_KINDS
from .db import ApiTokenRow, AuthAuditRow
from .model import utcnow

ROLES = ("viewer", "operator", "approver", "admin")
_IMPLIES: dict[str, frozenset[str]] = {
    "viewer": frozenset({"viewer"}),
    "operator": frozenset({"operator", "viewer"}),
    "approver": frozenset({"approver", "viewer"}),
    "admin": frozenset(ROLES),
}
TOKEN_KINDS = ("human", "service")
_PRIVILEGED = {"approver", "admin"}
_NAME = re.compile(r"[A-Za-z0-9._@+-]{1,120}")
# Agent, sandbox, host and policy identities (plans._RESERVED_ACTORS plus
# sandbox ids, and the coordinator's ``<kind>:<agent id>`` labels for every
# agent kind) can never be principals, so they can never hold approver.
_KINDS = "|".join(sorted(map(re.escape, AGENT_KINDS)))
_RESERVED = re.compile(r"(?i)(sdf|host|lead(-\d+)?|manager|workers?(-\d+)?|agent[:._-].*|policy[:._-].*|"
                       r"sandbox[:._-].*|sbx[:._-].*|e2b[:._-].*|dev|system|root|anonymous|"
                       rf"(?:{_KINDS})(?:[:._-].*)?)")
_TOKEN = re.compile(r"sdf_([0-9a-f]{16})\.([A-Za-z0-9_-]{43})")
PEPPER_MIN_LENGTH = 32
DEFAULT_EXPIRY_DAYS = 90
MAX_EXPIRY_DAYS = 365
LAST_USED_RESOLUTION = timedelta(seconds=60)


class AuthError(Exception):
    """Authentication failed (HTTP 401)."""


class AuthConfigError(Exception):
    """Authentication is not configured (HTTP 503)."""


@dataclass(frozen=True)
class Principal:
    name: str
    roles: frozenset[str]
    kind: str
    token_id: str | None
    method: str = "api_token"

    @property
    def effective_roles(self) -> frozenset[str]:
        out: set[str] = set()
        for role in self.roles:
            out |= _IMPLIES.get(role, frozenset())
        return frozenset(out)

    def has(self, role: str) -> bool:
        return role in self.effective_roles

    @property
    def can_approve(self) -> bool:
        """Approval needs the role, a human token and a non-agent identity."""
        return self.has("approver") and self.kind == "human" and not is_reserved_name(self.name)

    def as_record(self) -> dict[str, Any]:
        return {"name": self.name, "kind": self.kind, "token_id": self.token_id, "method": self.method,
                "roles": sorted(self.roles)}


def is_reserved_name(name: str) -> bool:
    return bool(_RESERVED.fullmatch(name or ""))


def _truthy(value: str | None) -> bool:
    return (value or "").strip().lower() in {"1", "true", "yes", "on"}


@lru_cache(maxsize=1)
def _dotenv() -> dict[str, str]:
    from .credentials import load_dotenv

    env: dict[str, str] = {}
    try:
        load_dotenv(environ=env)
    except Exception:  # noqa: BLE001 - a broken .env means "not configured"
        return {}
    return env


def setting(name: str) -> str:
    """os.environ wins (even when empty); otherwise the repo-root .env."""
    if name in os.environ:
        return os.environ[name]
    return _dotenv().get(name, "")


def pepper() -> str | None:
    value = setting("SDF_AUTH_PEPPER")
    return value if len(value) >= PEPPER_MIN_LENGTH else None


def dev_insecure() -> bool:
    return _truthy(setting("SDF_AUTH_DEV_INSECURE"))


def single_user_mode() -> bool:
    return _truthy(setting("SDF_AUTH_SINGLE_USER"))


def _hash(secret: str, key: str) -> str:
    return hmac.new(key.encode(), secret.encode(), hashlib.sha256).hexdigest()


def normalize_roles(roles: Iterable[str]) -> list[str]:
    out = sorted({r.strip().lower() for r in roles if r and r.strip()})
    if not out:
        raise ValueError("a token needs at least one role")
    unknown = set(out) - set(ROLES)
    if unknown:
        raise ValueError(f"unknown role(s) {sorted(unknown)}; choose from {list(ROLES)}")
    return out


def create_token(
    db: Session, *, principal: str, roles: Iterable[str], created_by: str, kind: str = "human",
    expires_in_days: int = DEFAULT_EXPIRY_DAYS, description: str | None = None, key: str | None = None,
    now: datetime | None = None,
) -> tuple[ApiTokenRow, str]:
    """Create a token; returns the row and the plaintext, which is never stored."""

    key = key or pepper()
    if not key:
        raise AuthConfigError(f"SDF_AUTH_PEPPER must be set (at least {PEPPER_MIN_LENGTH} characters)")
    principal = (principal or "").strip()
    if not _NAME.fullmatch(principal):
        raise ValueError("principal must be 1-120 characters of letters, digits and . _ @ + -")
    if is_reserved_name(principal):
        raise ValueError(f"{principal!r} is reserved for agents, sandboxes or the host and cannot hold a token")
    role_list = normalize_roles(roles)
    if kind not in TOKEN_KINDS:
        raise ValueError(f"kind must be one of {TOKEN_KINDS}")
    if kind != "human" and set(role_list) & _PRIVILEGED:
        raise ValueError("only human tokens may hold approver or admin")
    if not 1 <= int(expires_in_days) <= MAX_EXPIRY_DAYS:
        raise ValueError(f"expiry must be between 1 and {MAX_EXPIRY_DAYS} days")
    now = now or utcnow()
    token_id = secrets.token_hex(8)
    secret = secrets.token_urlsafe(32)
    row = ApiTokenRow(
        id=token_id, principal=principal, kind=kind, roles=role_list, secret_hash=_hash(secret, key),
        description=(description or None) and description[:500], created_at=now, created_by=created_by[:120],
        expires_at=now + timedelta(days=int(expires_in_days)),
    )
    db.add(row)
    db.add(AuthAuditRow(at=now, event="token_created", principal=created_by[:120], token_id=token_id,
                        detail={"for": principal, "roles": role_list, "kind": kind,
                                "expires_at": row.expires_at.isoformat()}))
    db.flush()
    return row, f"sdf_{token_id}.{secret}"


def revoke_token(db: Session, token_id: str, *, revoked_by: str) -> ApiTokenRow:
    row = db.get(ApiTokenRow, token_id)
    if row is None:
        raise LookupError("token not found")
    if row.revoked_at is not None:
        return row
    row.revoked_at = utcnow()
    row.revoked_by = revoked_by[:120]
    db.add(AuthAuditRow(at=row.revoked_at, event="token_revoked", principal=revoked_by[:120], token_id=token_id,
                        detail={"for": row.principal}))
    db.flush()
    return row


def list_tokens(db: Session) -> list[dict[str, Any]]:
    """Token metadata only: never the secret or its hash."""
    rows = db.scalars(select(ApiTokenRow).order_by(ApiTokenRow.created_at)).all()
    return [{
        "id": r.id, "principal": r.principal, "kind": r.kind, "roles": list(r.roles),
        "description": r.description, "created_by": r.created_by,
        "created_at": _iso(r.created_at), "expires_at": _iso(r.expires_at),
        "last_used_at": _iso(r.last_used_at), "revoked_at": _iso(r.revoked_at), "revoked_by": r.revoked_by,
    } for r in rows]


def _iso(value: datetime | None) -> str | None:
    return value.isoformat() if value else None


def _aware(value: datetime) -> datetime:
    return value if value.tzinfo else value.replace(tzinfo=timezone.utc)


def verify_token(db: Session, presented: str, *, key: str, now: datetime | None = None) -> Principal:
    """Return the token's Principal or raise AuthError (reason is for audit, not the client)."""

    now = now or utcnow()
    match = _TOKEN.fullmatch(presented or "")
    secret = match.group(2) if match else "x" * 43
    computed = _hash(secret, key)
    row = db.get(ApiTokenRow, match.group(1)) if match else None
    expected = row.secret_hash if row is not None else _hash("dummy-" + secret, key)
    if not hmac.compare_digest(computed, expected) or row is None or match is None:
        raise AuthError("malformed" if match is None else ("unknown" if row is None else "bad_secret"))
    if row.revoked_at is not None:
        raise AuthError("revoked")
    if _aware(row.expires_at) <= now:
        raise AuthError("expired")
    if row.last_used_at is None or now - _aware(row.last_used_at) >= LAST_USED_RESOLUTION:
        row.last_used_at = now
    return Principal(name=row.principal, roles=frozenset(row.roles), kind=row.kind, token_id=row.id)


# --- failed-authentication limiter --------------------------------------------

@dataclass
class FailureLimiter:
    """Per-client lockout after repeated failures (in memory, per process)."""

    max_failures: int = 10
    window_s: float = 300.0
    lockout_s: float = 300.0
    _failures: dict[str, deque] = field(default_factory=dict)
    _locked_until: dict[str, float] = field(default_factory=dict)
    _lock: threading.Lock = field(default_factory=threading.Lock)

    def retry_after(self, client: str, now: float | None = None) -> int:
        now = time.monotonic() if now is None else now
        with self._lock:
            until = self._locked_until.get(client, 0.0)
            if until > now:
                return max(1, int(until - now))
            self._locked_until.pop(client, None)
            return 0

    def failure(self, client: str, now: float | None = None) -> None:
        now = time.monotonic() if now is None else now
        with self._lock:
            hits = self._failures.setdefault(client, deque())
            hits.append(now)
            while hits and hits[0] < now - self.window_s:
                hits.popleft()
            if len(hits) >= self.max_failures:
                self._locked_until[client] = now + self.lockout_s
                hits.clear()
            if len(self._failures) > 10_000:  # bound memory under a spray of clients
                self._failures.pop(next(iter(self._failures)))

    def reset(self) -> None:
        with self._lock:
            self._failures.clear()
            self._locked_until.clear()


def limiter_from_env() -> FailureLimiter:
    def num(name: str, default: float) -> float:
        try:
            return float(setting(name) or default)
        except ValueError:
            return default
    return FailureLimiter(max_failures=int(num("SDF_AUTH_MAX_FAILURES", 10)),
                          window_s=num("SDF_AUTH_FAILURE_WINDOW_S", 300), lockout_s=num("SDF_AUTH_LOCKOUT_S", 300))


# --- request authorization policy ---------------------------------------------

PUBLIC_ROUTES = frozenset({("GET", "/healthz")})
SAFE_METHODS = frozenset({"GET", "HEAD", "OPTIONS"})

# Every route not listed falls back to viewer for safe methods and admin for
# anything else, so a new mutating route fails closed until it is classified.
ROUTE_ROLES: dict[tuple[str, str], str] = {
    ("POST", "/business-contexts"): "operator",
    ("POST", "/objectives"): "operator",
    ("POST", "/assumptions"): "operator",
    ("POST", "/constraints"): "operator",
    ("POST", "/requirements"): "operator",
    ("POST", "/tasks"): "operator",
    ("POST", "/tasks/{task_id}/run"): "operator",
    ("POST", "/attempts/{attempt_id}/actions"): "operator",
    ("POST", "/objectives/{objective_id}/metric"): "operator",
    ("POST", "/objectives/{objective_id}/observations"): "operator",
    ("POST", "/plans"): "operator",
    ("POST", "/plans/{plan_id}/runs"): "operator",
    ("POST", "/runs/{run_id}/cancel"): "operator",
    # Recording a Decision is a decision: approver, like plan approval.
    ("POST", "/decisions"): "approver",
    ("POST", "/adrs"): "approver",
    ("POST", "/plans/{plan_id}/approve"): "approver",
    ("POST", "/plans/{plan_id}/reject"): "approver",
    ("POST", "/decision-requests/{request_id}/resolve"): "approver",
}


def required_role(method: str, path: str) -> str:
    explicit = ROUTE_ROLES.get((method.upper(), path))
    if explicit:
        return explicit
    return "viewer" if method.upper() in SAFE_METHODS else "admin"


_LOOPBACK_NAMES = {"localhost"}


def is_loopback(host: str | None) -> bool:
    if not host:
        return False
    if host in _LOOPBACK_NAMES:
        return True
    try:
        return ipaddress.ip_address(host.split("%")[0]).is_loopback
    except ValueError:
        return False


DEV_PRINCIPAL = Principal(name="local-dev", roles=frozenset({"admin"}), kind="human", token_id=None,
                          method="dev_insecure")
