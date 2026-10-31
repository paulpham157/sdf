"""Usage/cost ledger (ADR 0023).

One append-only :class:`~sdf_core.db.UsageLedgerRow` per agent turn and one
per sandbox lifetime.  Numbers carry a ``source``:

* ``reported`` - the agent/provider reported them (headless JSON events, the
  Codex rollout ``token_count``, Claude transcript ``usage``);
* ``estimated`` - SDF computed them (sandbox seconds x rate, tokens x price);
* ``missing`` - nothing usable was reported (e.g. Claude Code against an
  Anthropic-compatible endpoint that answers ``usage: 0/0``).

``cost_source`` is tracked separately: tokens may be reported while the cost
is estimated from :data:`PRICES_PER_MTOK` (or ``SDF_PRICE_<MODEL>``).
"""

from __future__ import annotations

import json
import os
import re
from collections.abc import Mapping
from dataclasses import dataclass, field
from typing import Any

from sqlalchemy.orm import Session

from .db import UsageLedgerRow
from .model import utcnow

# USD per million tokens (input, output) for the example model id ``glm-5.3``.
# This is not a vendor invoice and not tied to a host: any other model is
# priced only through ``SDF_PRICE_<MODEL>=<in>,<out>`` (model upper-cased,
# non-alnum -> ``_``).  ADR 0034.
PRICES_PER_MTOK: dict[str, tuple[float, float]] = {
    "glm-5.3": (0.75, 3.00),
}
# E2B base sandbox (2 vCPU / 512 MiB) list price; override with
# ``SDF_E2B_USD_PER_SECOND`` / ``SDF_DAYTONA_USD_PER_SECOND``.
SANDBOX_USD_PER_SECOND: dict[str, float] = {"e2b": 0.0000303, "daytona": 0.0000303}


@dataclass(slots=True)
class TurnUsage:
    input_tokens: int | None = None
    output_tokens: int | None = None
    cache_read_tokens: int | None = None
    cost_usd: float | None = None
    model: str | None = None
    detail: dict[str, Any] = field(default_factory=dict)

    @property
    def has_tokens(self) -> bool:
        return bool((self.input_tokens or 0) + (self.output_tokens or 0) + (self.cache_read_tokens or 0))


def _price_env_name(model: str) -> str:
    return "SDF_PRICE_" + re.sub(r"[^A-Z0-9]", "_", model.upper())


def price_for(model: str | None, environ: Mapping[str, str] | None = None) -> tuple[float, float] | None:
    if not model:
        return None
    env = os.environ if environ is None else environ
    raw = env.get(_price_env_name(model))
    if raw:
        try:
            a, b = (float(x) for x in raw.split(","))
            return a, b
        except ValueError:
            return None
    bare = model.split("/")[-1].lower()
    return PRICES_PER_MTOK.get(bare)


def sandbox_rate(provider: str, environ: Mapping[str, str] | None = None) -> float:
    env = os.environ if environ is None else environ
    raw = env.get(f"SDF_{provider.upper()}_USD_PER_SECOND")
    try:
        return float(raw) if raw else SANDBOX_USD_PER_SECOND.get(provider, 0.0)
    except ValueError:
        return SANDBOX_USD_PER_SECOND.get(provider, 0.0)


def headless_delta(before: Mapping[str, Any], after: Mapping[str, Any]) -> TurnUsage:
    """Per-turn usage from two cumulative headless ``runtime.usage()`` dicts."""

    def d(key: str) -> int:
        return max(0, int(after.get(key) or 0) - int(before.get(key) or 0))

    cost = max(0.0, float(after.get("cost") or 0.0) - float(before.get("cost") or 0.0))
    served = list(after.get("served_models") or after.get("requested_models") or [])
    return TurnUsage(
        input_tokens=d("input_tokens"), output_tokens=d("output_tokens"), cache_read_tokens=d("cache_read_tokens"),
        cost_usd=cost or None, model=served[-1] if served else None,
    )


# Read inside the sandbox after a Herdr (codex/claude) turn: cumulative usage
# from the agent's own session files.  Prints one JSON line; never secrets.
SESSION_USAGE_SCRIPT = r'''
import glob, json, os, sys
agent = sys.argv[1]; h = os.path.expanduser("~")
def lines(pattern):
    for f in sorted(glob.glob(pattern, recursive=True)):
        for line in open(f, errors="replace"):
            try: yield json.loads(line)
            except Exception: pass
out = {"input_tokens": 0, "output_tokens": 0, "cache_read_tokens": 0, "models": [], "found": False}
if agent == "claude":
    per = {}
    for e in lines(h + "/.claude/projects/**/*.jsonl"):
        m = e.get("message") or {}
        if e.get("type") == "assistant" and m.get("id"):
            out["found"] = True
            if m.get("model") and m["model"] not in out["models"]: out["models"].append(m["model"])
            u = m.get("usage") or {}; s = per.setdefault(m["id"], [0, 0, 0])
            s[0] = max(s[0], (u.get("input_tokens") or 0) + (u.get("cache_creation_input_tokens") or 0))
            s[1] = max(s[1], u.get("output_tokens") or 0); s[2] = max(s[2], u.get("cache_read_input_tokens") or 0)
    out["input_tokens"] = sum(s[0] for s in per.values()); out["output_tokens"] = sum(s[1] for s in per.values())
    out["cache_read_tokens"] = sum(s[2] for s in per.values())
elif agent == "codex":
    last = None
    for e in lines(h + "/.codex/sessions/**/*.jsonl"):
        p = e.get("payload") or {}
        if e.get("type") == "turn_context" and p.get("model") and p["model"] not in out["models"]: out["models"].append(p["model"])
        if p.get("type") == "token_count" and p.get("info"): last = p["info"].get("total_token_usage")
    if last:
        out["found"] = True; cached = last.get("cached_input_tokens") or 0
        out["input_tokens"] = max(0, (last.get("input_tokens") or 0) - cached)
        out["cache_read_tokens"] = cached; out["output_tokens"] = last.get("output_tokens") or 0
print(json.dumps(out))
'''


def parse_session_usage(raw: str) -> dict[str, Any] | None:
    for line in reversed((raw or "").strip().splitlines()):
        try:
            data = json.loads(line)
        except ValueError:
            continue
        if isinstance(data, dict) and "input_tokens" in data:
            return data
    return None


def session_delta(before: Mapping[str, Any] | None, after: Mapping[str, Any] | None) -> TurnUsage:
    if not after or not after.get("found"):
        return TurnUsage(detail={"reason": "no session usage records"})
    prev = before or {}
    models = list(after.get("models") or [])
    return TurnUsage(
        input_tokens=max(0, int(after.get("input_tokens") or 0) - int(prev.get("input_tokens") or 0)),
        output_tokens=max(0, int(after.get("output_tokens") or 0) - int(prev.get("output_tokens") or 0)),
        cache_read_tokens=max(0, int(after.get("cache_read_tokens") or 0) - int(prev.get("cache_read_tokens") or 0)),
        model=models[-1] if models else None,
    )


def record_turn(
    db: Session, *, attempt_id: str, agent: str, turn: int, session_id: str | None, sandbox_id: str | None,
    sandbox_provider: str | None, usage: TurnUsage, status: str, error_class: str | None,
    seconds: float | None = None, model: str | None = None, run_id: str | None = None,
    environ: Mapping[str, str] | None = None,
) -> UsageLedgerRow:
    """Append one turn row.  0/0 tokens are recorded as ``missing``, not 0."""

    model = usage.model or model
    if usage.has_tokens:
        source = "reported"
    else:
        source = "missing"
    cost, cost_source = usage.cost_usd, "reported" if usage.cost_usd else "missing"
    if not cost and usage.has_tokens:
        price = price_for(model, environ)
        if price:
            cost = ((usage.input_tokens or 0) * price[0] + (usage.output_tokens or 0) * price[1]) / 1_000_000
            cost_source = "estimated"
    row = UsageLedgerRow(
        attempt_id=attempt_id, run_id=run_id, kind="turn", agent=agent, turn=turn, session_id=session_id,
        sandbox_id=sandbox_id, sandbox_provider=sandbox_provider, model=model,
        input_tokens=usage.input_tokens if source == "reported" else None,
        output_tokens=usage.output_tokens if source == "reported" else None,
        cache_read_tokens=usage.cache_read_tokens if source == "reported" else None,
        seconds=seconds, cost_usd=round(cost, 8) if cost else None, source=source, cost_source=cost_source,
        status=status, error_class=error_class, detail=usage.detail or None, created_at=utcnow(),
    )
    db.add(row)
    return row


def record_sandbox(
    db: Session, *, attempt_id: str, agent: str, sandbox_id: str | None, sandbox_provider: str,
    seconds: float, status: str, error_class: str | None, detail: dict[str, Any] | None = None,
    run_id: str | None = None, environ: Mapping[str, str] | None = None,
) -> UsageLedgerRow:
    rate = sandbox_rate(sandbox_provider, environ)
    row = UsageLedgerRow(
        attempt_id=attempt_id, run_id=run_id, kind="sandbox", agent=agent, sandbox_id=sandbox_id,
        sandbox_provider=sandbox_provider, seconds=round(seconds, 3),
        cost_usd=round(seconds * rate, 8) if rate else None, source="estimated",
        cost_source="estimated" if rate else "missing", status=status, error_class=error_class,
        detail=detail, created_at=utcnow(),
    )
    db.add(row)
    return row


def ledger_rows(db: Session, attempt_id: str) -> list[UsageLedgerRow]:
    from sqlalchemy import select

    return list(db.scalars(select(UsageLedgerRow).where(UsageLedgerRow.attempt_id == attempt_id)
                           .order_by(UsageLedgerRow.id)).all())


def ledger_payload(row: UsageLedgerRow) -> dict[str, Any]:
    return {
        "id": row.id, "attempt_id": row.attempt_id, "run_id": row.run_id, "kind": row.kind, "agent": row.agent,
        "turn": row.turn, "session_id": row.session_id, "sandbox_id": row.sandbox_id,
        "sandbox_provider": row.sandbox_provider, "model": row.model, "input_tokens": row.input_tokens,
        "output_tokens": row.output_tokens, "cache_read_tokens": row.cache_read_tokens, "seconds": row.seconds,
        "cost_usd": row.cost_usd, "source": row.source, "cost_source": row.cost_source, "status": row.status,
        "error_class": row.error_class, "detail": row.detail,
        "created_at": row.created_at.isoformat() if row.created_at else None,
    }
