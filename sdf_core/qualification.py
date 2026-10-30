"""Qualify agents for routing from trusted execution evidence (#58, ADR 0030).

Routing inputs come only from records the SDF server writes itself:

* **Live attempt** - an Attempt with a ``usage_ledger`` ``sandbox`` row. Only
  the live run adapter writes those rows (ADR 0023), and the row's ``agent``
  names the agent kind that ran.
* **Success** - Evaluator Evidence (append-only, ADR 0002): an Attempt counts
  as accepted only when it has Evidence and every row is ``PASS``.
* **Cost** - the Attempt's ledger rows. An Attempt whose turn or sandbox row
  has ``cost_source = 'missing'`` has no measured cost and is left out of the
  cost mean.
* **Latency** - the ``seconds`` that the server measured on the ledger
  ``sandbox`` row (sandbox lifetime, the whole run).
* **Availability** - the most recent live Attempt has a ``runtime_started``
  event in a trusted runtime stream (``run-<provider>``, ADR 0029) and did not
  end in an infrastructure error class.
* **Load** - the agent's in-progress Attempts (attributed by a routing
  decision or a ledger row) divided by the run capacity.
* **Capability** - ``verified-fixture-loop`` once the agent has an accepted
  live Attempt, plus ``tool:<tool>.<action>`` for each Tool Proxy action
  executed in an accepted Attempt (trusted ``tool_action_executed`` events).

Fields a caller could have set are ignored: ``attempts.cost_usd``,
``latency_ms``, ``provider`` and the token counts (ADR 0027), outcome
observations, agent Claims and runtime output text. A missing measurement
does not fall back to a configured value: the agent is left unqualified,
with a reason.
"""

from __future__ import annotations

from collections import defaultdict
from dataclasses import dataclass, field
from math import isfinite
from typing import Any, Iterable, Mapping, Sequence
from uuid import uuid4

from sqlalchemy import select
from sqlalchemy.orm import Session

from .db import AttemptRow, EvidenceRow, RoutingDecisionRow, RuntimeEventRow, UsageLedgerRow
from .events import TRUSTED_ACTION_SOURCES
from .escalation import ModelTier
from .model import AttemptState, utcnow
from .routing import ModelProfile, ModelRouter, RoutingMeasurement, RoutingRequest

VERIFIED_LOOP = "verified-fixture-loop"
TRUSTED_RUNTIME_SOURCE_PREFIX = "run-"
INFRA_ERRORS = frozenset({
    "sandbox_error", "provider_4xx", "provider_429", "provider_5xx", "cleanup_failed", "config_error",
})
ACTIVE_STATES = frozenset({AttemptState.CREATED.value, AttemptState.DISPATCHED.value, AttemptState.RUNNING.value})
DEFAULT_WINDOW = 20


@dataclass(frozen=True, slots=True)
class AgentQualification:
    """The measured routing inputs for one agent kind, or why there are none."""

    agent: str
    measurement: RoutingMeasurement | None
    reason: str
    accepted_attempts: int = 0
    provenance: Mapping[str, Any] = field(default_factory=dict)

    @property
    def qualified(self) -> bool:
        return self.measurement is not None

    def as_dict(self) -> dict[str, Any]:
        m = self.measurement
        return {
            "agent": self.agent,
            "qualified": self.qualified,
            "reason": self.reason,
            "accepted_attempts": self.accepted_attempts,
            "measurement": None if m is None else {
                "capabilities": sorted(m.capabilities), "available": m.available, "latency_ms": m.latency_ms,
                "cost_usd": m.cost_usd, "success_rate": m.success_rate, "sample_count": m.sample_count,
                "load": m.load,
            },
            "provenance": dict(self.provenance),
        }


def _accepted(evidence: Sequence[EvidenceRow]) -> bool:
    return bool(evidence) and all(row.status == "PASS" for row in evidence)


def _measured_cost(rows: Sequence[UsageLedgerRow]) -> float | None:
    if not rows or any(row.cost_source == "missing" for row in rows):
        return None
    total = sum(row.cost_usd or 0.0 for row in rows)
    return total if isfinite(total) and total >= 0 else None


def _active_attempts(db: Session, agent: str) -> int:
    """In-progress Attempts attributed to ``agent`` by a server record."""

    routed = set(db.scalars(select(RoutingDecisionRow.attempt_id).where(RoutingDecisionRow.selected_agent == agent)))
    metered = set(db.scalars(select(UsageLedgerRow.attempt_id).where(UsageLedgerRow.agent == agent)))
    ids = routed | metered
    if not ids:
        return 0
    return len(db.scalars(select(AttemptRow.id).where(AttemptRow.id.in_(ids),
                                                       AttemptRow.status.in_(ACTIVE_STATES))).all())


def qualify_agents(
    db: Session,
    agents: Iterable[str],
    *,
    capacity: int = 2,
    window: int = DEFAULT_WINDOW,
) -> dict[str, AgentQualification]:
    """Measure each agent kind from trusted rows; never raises for missing data."""

    if capacity < 1:
        raise ValueError("capacity must be at least 1")
    names = list(dict.fromkeys(a.strip().lower() for a in agents if a and a.strip()))
    sandbox_rows = db.scalars(
        select(UsageLedgerRow).where(UsageLedgerRow.kind == "sandbox", UsageLedgerRow.agent.in_(names))
        .order_by(UsageLedgerRow.created_at.desc(), UsageLedgerRow.id.desc())
    ).all()
    by_agent: dict[str, list[UsageLedgerRow]] = defaultdict(list)
    for row in sandbox_rows:
        by_agent[row.agent or ""].append(row)

    result: dict[str, AgentQualification] = {}
    for agent in names:
        # One sandbox row per live attempt; newest first, bounded window.
        seen: dict[str, UsageLedgerRow] = {}
        for row in by_agent.get(agent, []):
            seen.setdefault(row.attempt_id, row)
        attempt_rows = {a.id: a for a in db.scalars(select(AttemptRow).where(AttemptRow.id.in_(list(seen)))).all()}
        terminal = [aid for aid in seen if attempt_rows.get(aid) is not None
                    and attempt_rows[aid].status not in ACTIVE_STATES][:window]
        active = _active_attempts(db, agent)
        load = min(1.0, active / capacity)
        if not terminal:
            result[agent] = AgentQualification(agent, None, "no finished live attempt measured")
            continue

        evidence: dict[str, list[EvidenceRow]] = defaultdict(list)
        for ev in db.scalars(select(EvidenceRow).where(EvidenceRow.attempt_id.in_(terminal))).all():
            evidence[ev.attempt_id].append(ev)
        ledger: dict[str, list[UsageLedgerRow]] = defaultdict(list)
        for row in db.scalars(select(UsageLedgerRow).where(UsageLedgerRow.attempt_id.in_(terminal))).all():
            ledger[row.attempt_id].append(row)
        events: dict[str, list[RuntimeEventRow]] = defaultdict(list)
        for ev in db.scalars(select(RuntimeEventRow).where(RuntimeEventRow.attempt_id.in_(terminal))).all():
            events[ev.attempt_id].append(ev)

        accepted = [aid for aid in terminal if _accepted(evidence[aid])]
        costs = {aid: c for aid in terminal if (c := _measured_cost(ledger[aid])) is not None}
        latencies = {aid: seen[aid].seconds * 1000 for aid in terminal
                     if seen[aid].seconds is not None and isfinite(seen[aid].seconds) and seen[aid].seconds >= 0}
        latest = terminal[0]
        started = any(e.kind == "runtime_started" and e.source.startswith(TRUSTED_RUNTIME_SOURCE_PREFIX)
                      for e in events[latest])
        latest_error = attempt_rows[latest].error_class
        available = started and latest_error not in INFRA_ERRORS
        capabilities = {VERIFIED_LOOP} if accepted else set()
        for aid in accepted:
            for e in events[aid]:
                if e.kind == "tool_action_executed" and e.source in TRUSTED_ACTION_SOURCES:
                    payload = e.payload if isinstance(e.payload, dict) else {}
                    if payload.get("tool") and payload.get("action"):
                        capabilities.add(f"tool:{payload['tool']}.{payload['action']}")
        provenance = {
            "attempt_ids": terminal,
            "accepted_attempt_ids": accepted,
            "evidence_ids": sorted(ev.id for aid in terminal for ev in evidence[aid]),
            "ledger_ids": sorted(row.id for aid in terminal for row in ledger[aid]),
            "cost_attempt_ids": sorted(costs),
            "availability_attempt_id": latest,
            "availability_error_class": latest_error,
            "runtime_started_observed": started,
            "active_attempts": active,
            "capacity": capacity,
        }
        missing = [name for name, values in (("cost", costs), ("latency", latencies)) if not values]
        if missing:
            result[agent] = AgentQualification(
                agent, None, f"unmeasured {' and '.join(missing)} (no trusted ledger value)", len(accepted), provenance)
            continue
        measurement = RoutingMeasurement(
            capabilities=frozenset(capabilities),
            available=available,
            latency_ms=round(sum(latencies.values()) / len(latencies), 1),
            cost_usd=round(sum(costs.values()) / len(costs), 8),
            success_rate=round(len(accepted) / len(terminal), 6),
            sample_count=len(terminal),
            load=load,
        )
        reason = "qualified" if available else (
            "unavailable: " + (f"last live attempt ended in {latest_error}" if latest_error in INFRA_ERRORS
                               else "no trusted runtime_started observation"))
        result[agent] = AgentQualification(agent, measurement, reason, len(accepted), provenance)
    return result


def verified_loop_completed(qualifications: Mapping[str, AgentQualification]) -> bool:
    """P4 gate: at least one real agent has an accepted live fixture-loop Attempt."""

    return any(q.accepted_attempts > 0 for q in qualifications.values())


# --- selection and recording ------------------------------------------------

@dataclass(frozen=True, slots=True)
class AgentRoute:
    """The outcome of choosing an agent for one live Attempt."""

    agent: str | None
    mode: str  # "explicit" | "measured"
    reason: str
    request: Mapping[str, Any] | None
    candidates: tuple[Mapping[str, Any], ...]


def routing_request_from_env(environ: Mapping[str, str]) -> RoutingRequest:
    """Server-side routing limits; a caller never sets them."""

    def number(name: str, default: float) -> float:
        raw = (environ.get(name) or "").strip()
        try:
            value = float(raw) if raw else default
        except ValueError as exc:
            raise ValueError(f"{name} must be a number") from exc
        if not isfinite(value) or value < 0:
            raise ValueError(f"{name} must be a finite, non-negative number")
        return value

    required = frozenset(
        c.strip() for c in (environ.get("SDF_RUN_ROUTE_REQUIRE") or VERIFIED_LOOP).split(",") if c.strip()
    )
    min_success = number("SDF_RUN_ROUTE_MIN_SUCCESS", 0.0)
    if min_success > 1:
        raise ValueError("SDF_RUN_ROUTE_MIN_SUCCESS must be between 0 and 1")
    return RoutingRequest(
        required_capabilities=required,
        max_cost_usd=number("SDF_RUN_ROUTE_MAX_COST_USD", 10.0),
        max_latency_ms=int(number("SDF_RUN_ROUTE_MAX_LATENCY_MS", 3_600_000)),
        min_success_rate=min_success,
    )


def _request_dict(request: RoutingRequest) -> dict[str, Any]:
    return {"required_capabilities": sorted(request.required_capabilities), "max_cost_usd": request.max_cost_usd,
            "max_latency_ms": request.max_latency_ms, "min_success_rate": request.min_success_rate}


def select_agent(
    db: Session,
    agents: Sequence[str],
    request: RoutingRequest,
    *,
    capacity: int = 2,
) -> AgentRoute:
    """Pick one agent kind from trusted measurements, or none with reasons."""

    quals = qualify_agents(db, agents, capacity=capacity)
    if not verified_loop_completed(quals):
        return AgentRoute(None, "measured",
                          "not qualified: no agent has completed the verified fixture loop with live Evidence",
                          _request_dict(request), tuple(q.as_dict() for q in quals.values()))
    profiles = [ModelProfile(name, ModelTier.BASIC, frozenset(), 0.0, 0) for name in quals]
    measurements = {name: q.measurement for name, q in quals.items() if q.measurement is not None}
    decision = ModelRouter().route_measured(request, profiles, measurements)
    candidates = []
    for name, q in quals.items():
        entry = q.as_dict()
        if decision.profile is not None and decision.profile.name == name:
            entry["selected"] = True
        else:
            entry["selected"] = False
            entry["rejected"] = q.reason if not q.qualified else decision.rejected.get(name, "lower score")
        candidates.append(entry)
    if decision.profile is None:
        reasons = "; ".join(f"{c['agent']}: {c.get('rejected')}" for c in candidates)
        return AgentRoute(None, "measured", f"no qualified agent ({reasons})", _request_dict(request),
                          tuple(candidates))
    return AgentRoute(decision.profile.name, "measured", decision.reason, _request_dict(request), tuple(candidates))


def explicit_route(agent: str) -> AgentRoute:
    return AgentRoute(agent, "explicit", "configured by SDF_RUN_AGENT; not measured routing", None, ())


def record_route(db: Session, attempt_id: str, route: AgentRoute) -> RoutingDecisionRow:
    """Append the routing decision for one Attempt (idempotent per Attempt)."""

    if route.agent is None:
        raise ValueError("cannot record a route without a selected agent")
    existing = db.scalar(select(RoutingDecisionRow).where(RoutingDecisionRow.attempt_id == attempt_id))
    if existing is not None:
        if existing.selected_agent != route.agent:
            raise ValueError("attempt already has a routing decision for a different agent")
        return existing
    row = RoutingDecisionRow(
        id=f"ROUTE-{uuid4().hex[:12]}", attempt_id=attempt_id, selected_agent=route.agent, mode=route.mode,
        reason=route.reason[:500], request=dict(route.request) if route.request is not None else None,
        candidates=[dict(c) for c in route.candidates], created_at=utcnow(),
    )
    db.add(row)
    db.flush()
    return row


def routing_record(db: Session, attempt_id: str) -> dict[str, Any] | None:
    """The decision plus its outcome, correlated to the Attempt's sessions.

    The outcome is read now from Evidence, the Attempt's server-set error
    class and the usage ledger; runtime events give the sessions only.
    """

    row = db.scalar(select(RoutingDecisionRow).where(RoutingDecisionRow.attempt_id == attempt_id))
    if row is None:
        return None
    attempt = db.get(AttemptRow, attempt_id)
    evidence = db.scalars(select(EvidenceRow).where(EvidenceRow.attempt_id == attempt_id)).all()
    ledger = db.scalars(select(UsageLedgerRow).where(UsageLedgerRow.attempt_id == attempt_id)).all()
    events = db.scalars(select(RuntimeEventRow).where(RuntimeEventRow.attempt_id == attempt_id)).all()
    sandbox = next((r for r in ledger if r.kind == "sandbox"), None)
    return {
        "id": row.id,
        "attempt_id": row.attempt_id,
        "selected_agent": row.selected_agent,
        "mode": row.mode,
        "reason": row.reason,
        "request": row.request,
        "candidates": row.candidates,
        "created_at": row.created_at,
        "sessions": sorted({e.session_id for e in events if e.source.startswith(TRUSTED_RUNTIME_SOURCE_PREFIX)}),
        "outcome": {
            "attempt_status": attempt.status if attempt else None,
            "error_class": attempt.error_class if attempt else None,
            "evidence": sorted((e.criterion or "", e.status) for e in evidence),
            "accepted": _accepted(list(evidence)),
            "measured_cost_usd": _measured_cost(list(ledger)),
            "sandbox_seconds": sandbox.seconds if sandbox else None,
            "evidence_ids": sorted(e.id for e in evidence),
            "ledger_ids": sorted(r.id for r in ledger),
        },
    }
