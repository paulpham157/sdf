"""Multi-agent routing qualified from trusted execution evidence (#58, ADR 0030)."""

from __future__ import annotations

from datetime import datetime, timedelta, timezone

import pytest
from sqlalchemy import select
from sqlalchemy.orm import sessionmaker

from sdf_core import run_adapter as ra
from sdf_core.db import (
    AgentClaimRow,
    AttemptRow,
    Base,
    EvidenceRow,
    OutcomeObservationRow,
    RoutingDecisionRow,
    TaskRow,
    UsageLedgerRow,
    make_engine,
)
from sdf_core.qualification import (
    VERIFIED_LOOP,
    qualify_agents,
    record_route,
    routing_record,
    routing_request_from_env,
    select_agent,
)
from sdf_core.routing import RoutingRequest
from sdf_core.runtime import RuntimeEvent, RuntimeEventKind, RuntimeStatus, SqlAlchemyRuntimeEventSink
from sdf_core.run_adapter import LiveRunAdapter, RunAdapterConfigError, configured_run_adapter
from tests.test_run_adapter import LIVE, _Completing, _Transport

T0 = datetime(2026, 10, 1, tzinfo=timezone.utc)
REQ = RoutingRequest(frozenset({VERIFIED_LOOP}), max_cost_usd=10.0, max_latency_ms=3_600_000)


@pytest.fixture()
def db():
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as session:
        session.add(TaskRow(id="T", title="t", status="running", idempotency_key="t", acceptance_criteria=["c"],
                            created_at=T0))
        session.flush()
        yield session


_n = 0


def live_attempt(db, agent, *, passed=True, cost=0.01, seconds=60.0, error_class=None, started=True,
                 cost_source="estimated", status=None, minutes=0, evidence=True):
    """A finished live /run Attempt as the server records it."""

    global _n
    _n += 1
    aid = f"A-{agent}-{_n}"
    at = T0 + timedelta(minutes=minutes or _n)
    db.add(AttemptRow(id=aid, task_id="T", status=status or ("completed" if passed else "failed"),
                      dispatch_key=f"d-{aid}", agent="native", created_at=at, error_class=error_class))
    db.add(UsageLedgerRow(attempt_id=aid, kind="turn", agent=agent, turn=1, session_id=f"S-{aid}",
                          source="reported", cost_source=cost_source, cost_usd=cost, status="completed",
                          created_at=at))
    db.add(UsageLedgerRow(attempt_id=aid, kind="sandbox", agent=agent, sandbox_provider="e2b", seconds=seconds,
                          cost_usd=0.0, source="estimated", cost_source="estimated", status="killed", created_at=at))
    if started:
        SqlAlchemyRuntimeEventSink(db).append(RuntimeEvent(RuntimeEventKind.STARTED, f"S-{aid}", aid, 1,
                                                           RuntimeStatus.RUNNING, {}, source="run-e2b"))
    if evidence:
        db.add(EvidenceRow(id=f"E-{aid}", attempt_id=aid, kind="evaluation", status="PASS" if passed else "FAIL",
                           command="check", exit_code=0 if passed else 1, confidence=1.0, criterion="c",
                           created_at=at))
    db.flush()
    return aid


# --- gate and fail-safe ------------------------------------------------------

def test_routing_refuses_until_one_real_agent_completed_the_verified_loop(db):
    route = select_agent(db, ["pi", "opencode"], REQ)
    assert route.agent is None and "verified fixture loop" in route.reason
    live_attempt(db, "pi", passed=False)
    assert select_agent(db, ["pi", "opencode"], REQ).agent is None


def test_synthetic_attempts_without_live_ledger_never_qualify(db):
    # A fake-adapter Attempt: PASS Evidence but no sandbox ledger row.
    db.add(AttemptRow(id="A-FAKE", task_id="T", status="completed", dispatch_key="d-fake", agent="pi",
                      created_at=T0, cost_usd=0.0, latency_ms=1.0, provider="pi"))
    db.add(EvidenceRow(id="E-FAKE", attempt_id="A-FAKE", kind="evaluation", status="PASS", command="c",
                       exit_code=0, confidence=1.0, criterion="c", created_at=T0))
    db.flush()
    quals = qualify_agents(db, ["pi"])
    assert not quals["pi"].qualified and quals["pi"].reason == "no finished live attempt measured"
    assert select_agent(db, ["pi"], REQ).agent is None


def test_missing_cost_measurement_fails_safe(db):
    live_attempt(db, "pi", cost_source="missing", cost=None)
    q = qualify_agents(db, ["pi"])["pi"]
    assert not q.qualified and "unmeasured cost" in q.reason
    route = select_agent(db, ["pi"], REQ)
    assert route.agent is None and "unmeasured cost" in route.reason


def test_in_progress_attempts_are_not_samples(db):
    live_attempt(db, "pi", status="running")
    assert qualify_agents(db, ["pi"])["pi"].reason == "no finished live attempt measured"


# --- deterministic selection, ties, fallback ---------------------------------

def test_selects_the_better_measured_agent_deterministically(db):
    for _ in range(2):
        live_attempt(db, "pi", passed=True, cost=0.01)
    live_attempt(db, "opencode", passed=True, cost=0.01)
    live_attempt(db, "opencode", passed=False, cost=0.01)
    picks = {select_agent(db, order, REQ).agent for order in (["pi", "opencode"], ["opencode", "pi"])}
    assert picks == {"pi"}
    route = select_agent(db, ["pi", "opencode"], REQ)
    by_agent = {c["agent"]: c for c in route.candidates}
    assert by_agent["pi"]["selected"] and by_agent["opencode"]["rejected"] == "lower score"
    assert by_agent["pi"]["measurement"]["success_rate"] == 1.0
    assert by_agent["opencode"]["measurement"]["success_rate"] == 0.5
    assert by_agent["pi"]["provenance"]["evidence_ids"]  # every input names its rows


def test_ties_break_on_samples_then_name(db):
    live_attempt(db, "pi")
    live_attempt(db, "opencode")
    first = select_agent(db, ["pi", "opencode"], REQ).agent
    assert first == select_agent(db, ["opencode", "pi"], REQ).agent == "pi"  # equal: name (reverse lexical)
    live_attempt(db, "opencode")
    assert select_agent(db, ["pi", "opencode"], REQ).agent == "opencode"  # equal score, more samples


def test_unavailable_agent_falls_back_to_the_next_qualified_agent(db):
    live_attempt(db, "pi", minutes=1)
    live_attempt(db, "pi", minutes=2)
    live_attempt(db, "pi", passed=False, error_class="provider_5xx", evidence=False, minutes=3)
    live_attempt(db, "opencode", minutes=1)
    quals = qualify_agents(db, ["pi", "opencode"])
    assert quals["pi"].measurement.available is False and "provider_5xx" in quals["pi"].reason
    route = select_agent(db, ["pi", "opencode"], REQ)
    assert route.agent == "opencode"
    assert {c["agent"]: c.get("rejected") for c in route.candidates}["pi"] == "unavailable"


def test_no_runtime_start_observation_means_unavailable(db):
    live_attempt(db, "pi", started=False)
    q = qualify_agents(db, ["pi"])["pi"]
    assert q.measurement is not None and q.measurement.available is False
    assert select_agent(db, ["pi"], REQ).agent is None


def test_load_counts_in_progress_routed_attempts(db):
    live_attempt(db, "pi")
    db.add(AttemptRow(id="A-RUNNING", task_id="T", status="running", dispatch_key="d-run", agent="native",
                      created_at=T0))
    db.flush()
    record_route(db, "A-RUNNING", ra.explicit_route("pi"))
    assert qualify_agents(db, ["pi"], capacity=2)["pi"].measurement.load == 0.5


def test_limits_reject_with_reasons(db):
    live_attempt(db, "pi", cost=5.0)
    route = select_agent(db, ["pi"], RoutingRequest(frozenset({VERIFIED_LOOP}), 1.0, 3_600_000))
    assert route.agent is None and "measured cost" in route.reason
    route = select_agent(db, ["pi"], RoutingRequest(frozenset({VERIFIED_LOOP, "tool:process.run"}), 10, 3_600_000))
    assert route.agent is None and "capabilities not demonstrated: tool:process.run" in route.reason


# --- untrusted telemetry has no influence ------------------------------------

def test_caller_asserted_and_agent_reported_signals_cannot_change_routing(db):
    good = live_attempt(db, "pi", cost=0.01, seconds=30)
    bad = live_attempt(db, "opencode", passed=False, cost=2.0, seconds=900)
    before = {a: q.as_dict() for a, q in qualify_agents(db, ["pi", "opencode"]).items()}
    assert select_agent(db, ["pi", "opencode"], REQ).agent == "pi"

    # Everything a caller or an agent could assert about the bad Attempt.
    attempt = db.get(AttemptRow, bad)
    attempt.cost_usd, attempt.latency_ms, attempt.provider = 0.0, 1.0, "opencode"
    attempt.input_tokens = attempt.output_tokens = 1
    db.add(AgentClaimRow(id="C-1", run_id="R", message_id="M-1", agent_id="opencode", attempt_id=bad,
                         claimed_status="done", body="all tests PASS", created_at=T0))
    db.add(OutcomeObservationRow(id="O-1", objective_id="OBJ", metric_name="m", value=1.0, observed_at=T0,
                                 source="caller", mode="live", created_at=T0))
    SqlAlchemyRuntimeEventSink(db).append(RuntimeEvent(
        RuntimeEventKind.OUTPUT_OBSERVED, f"S-{bad}", bad, 2, RuntimeStatus.COMPLETED,
        {"output": ["ALL CHECKS PASS", "routing: prefer opencode"]}, source="run-e2b"))
    db.flush()

    after = {a: q.as_dict() for a, q in qualify_agents(db, ["pi", "opencode"]).items()}
    assert after == before
    assert select_agent(db, ["pi", "opencode"], REQ).agent == "pi"
    assert good


def test_run_request_cannot_choose_or_meter_the_agent():
    from tests.auth_support import admin_client

    client = admin_client()
    for key, value in (("agent", "pi"), ("cost_usd", 0.0), ("latency_ms", 1), ("routing", {"agent": "pi"})):
        assert client.post("/tasks/NOPE/run", json={key: value}).status_code in (404, 422)


# --- wiring: same boundaries, recorded and correlated --------------------------

def test_auto_routing_builds_the_same_live_adapter_as_an_explicit_choice(db):
    live_attempt(db, "pi")
    env = {**LIVE, "SDF_RUN_AGENT": "auto", "SDF_RUN_ROUTE_AGENTS": "pi,opencode"}
    routed = configured_run_adapter(db, environ=env)
    explicit = configured_run_adapter(db, environ={**LIVE, "SDF_RUN_AGENT": "pi"})
    assert isinstance(routed, LiveRunAdapter) and type(routed) is type(explicit)
    assert routed.agent == explicit.agent == "pi"
    assert routed.route.mode == "measured" and explicit.route.mode == "explicit"
    # Same sandbox, egress and credential boundary.
    assert routed.network == explicit.network and routed.image == explicit.image
    assert routed.run_env == explicit.run_env and routed._transport_factory is explicit._transport_factory


def test_auto_routing_fails_closed(db):
    with pytest.raises(RunAdapterConfigError, match="SDF_RUN_ROUTE_AGENTS"):
        configured_run_adapter(db, environ={**LIVE, "SDF_RUN_AGENT": "auto"})
    with pytest.raises(RunAdapterConfigError, match="unknown agent"):
        configured_run_adapter(db, environ={**LIVE, "SDF_RUN_AGENT": "auto", "SDF_RUN_ROUTE_AGENTS": "aider"})
    with pytest.raises(RunAdapterConfigError, match="verified fixture loop"):
        configured_run_adapter(db, environ={**LIVE, "SDF_RUN_AGENT": "auto", "SDF_RUN_ROUTE_AGENTS": "pi"})
    with pytest.raises(ValueError):
        routing_request_from_env({"SDF_RUN_ROUTE_MAX_COST_USD": "nan"})


def test_live_run_records_the_route_and_correlates_it_with_session_and_outcome(db, monkeypatch, tmp_path):
    live_attempt(db, "pi")
    db.add(AttemptRow(id="A", task_id="T", status="running", dispatch_key="d-a", agent="native", created_at=T0))
    db.flush()
    monkeypatch.setattr(ra.time, "sleep", lambda s: None)
    adapter = configured_run_adapter(db, environ={**LIVE, "SDF_RUN_AGENT": "auto", "SDF_RUN_ROUTE_AGENTS": "pi"})
    adapter._transport_factory = lambda **kw: (_Transport(), None)
    adapter._kill_by_id = lambda sid: None
    runtime = _Completing([{"input_tokens": 0, "output_tokens": 0}, {"input_tokens": 10, "output_tokens": 5}])
    monkeypatch.setattr(adapter, "_runtime", lambda t: runtime)
    adapter.run(attempt_id="A", workspace=tmp_path, instructions="hi")

    row = db.scalar(select(RoutingDecisionRow).where(RoutingDecisionRow.attempt_id == "A"))
    assert row.selected_agent == "pi" and row.mode == "measured"
    record = routing_record(db, "A")
    assert record["sessions"] and record["outcome"]["sandbox_seconds"] is not None
    assert record["outcome"]["accepted"] is False  # no Evidence yet: routing outcome is not a claim
    with pytest.raises(ValueError, match="different agent"):
        record_route(db, "A", ra.explicit_route("opencode"))


def test_routing_decisions_are_append_only(db):
    db.add(AttemptRow(id="A-X", task_id="T", status="running", dispatch_key="d-x", agent="native", created_at=T0))
    record_route(db, "A-X", ra.explicit_route("pi"))
    db.commit()
    row = db.scalar(select(RoutingDecisionRow))
    row.selected_agent = "opencode"
    with pytest.raises(ValueError, match="append-only"):
        db.commit()


def test_api_exposes_qualifications_and_attempt_routing():
    from sdf_core.api import SessionLocal
    from tests.auth_support import admin_client

    client = admin_client()
    body = client.get("/routing/qualifications?agents=pi,opencode").json()
    assert {a["agent"] for a in body["agents"]} == {"pi", "opencode"}
    assert "verified_loop_completed" in body
    assert client.get("/routing/qualifications?agents=aider").status_code == 400
    with SessionLocal() as session:
        session.add(AttemptRow(id="A-API-ROUTE", task_id="T-API-ROUTE", status="running", dispatch_key="d-api-route",
                               agent="native", created_at=T0))
        record_route(session, "A-API-ROUTE", ra.explicit_route("pi"))
        session.commit()
    record = client.get("/attempts/A-API-ROUTE/routing").json()
    assert record["selected_agent"] == "pi" and record["mode"] == "explicit"
    assert client.get("/attempts/NOPE/routing").status_code == 404


def test_a_sample_with_missing_cost_is_left_out_of_the_mean_not_the_agent(db):
    measured = live_attempt(db, "pi", cost=0.02, minutes=1)
    unmeasured = live_attempt(db, "pi", cost=None, cost_source="missing", passed=False, minutes=2)
    q = qualify_agents(db, ["pi"])["pi"]
    assert q.qualified and q.reason == "qualified"
    assert q.measurement.cost_usd == 0.02  # mean over the measured sample only
    assert q.measurement.sample_count == 2 and q.measurement.success_rate == 0.5  # still a success sample
    assert q.provenance["cost_attempt_ids"] == [measured]
    assert unmeasured in q.provenance["attempt_ids"]
