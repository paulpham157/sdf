"""ADR 0014: plan-approval gate, messaging hub, sdf-msg shim, coordinator."""

from __future__ import annotations

import json
import subprocess
import sys
import threading
from pathlib import Path

import pytest
from sqlalchemy import select
from sqlalchemy.orm import sessionmaker

from sdf_core.agent_messaging import EnvelopeError, Member, MessageHub, sanitize_body
from sdf_core.coordinator import Coordinator, RunConfig, request_cancel
from sdf_core.db import (
    AgentClaimRow,
    AgentMessageRow,
    AgentRunRow,
    AttemptRow,
    Base,
    DecisionEdgeRow,
    DecisionRequestRow,
    EvidenceRow,
    PlanGateError,
    PlanRow,
    TaskRow,
    make_engine,
)
from sdf_core.execution import ExecutionService
from sdf_core.model import utcnow
from sdf_core.plans import (
    NoAutoResolvePolicy,
    PlanSpec,
    RuleDecisionPolicy,
    decide_plan,
    propose_plan,
    resolve_request,
    task_id_for,
)
from tests.agent_comm_support import SHIM, fake_factory, lead_behavior, make_fixture, plan_dict, worker_behavior


@pytest.fixture()
def db():
    engine = make_engine("sqlite+pysqlite:///:memory:")
    Base.metadata.create_all(engine)
    session = sessionmaker(engine, expire_on_commit=False)()
    try:
        yield session
    finally:
        session.close()
        engine.dispose()


@pytest.fixture()
def fixture(tmp_path: Path) -> Path:
    return make_fixture(tmp_path)


def _plan(db, fixture, plan_id="PLAN-T1") -> PlanRow:
    plan = propose_plan(db, PlanSpec(**plan_dict(fixture, plan_id)))
    db.commit()
    return plan


# --- plan gate ---------------------------------------------------------------

def test_plan_spec_requires_host_checks_for_every_criterion(fixture):
    data = plan_dict(fixture)
    data["subtasks"][0]["criterion_checks"].pop("sub still subtracts")
    with pytest.raises(ValueError, match="needs a host check"):
        PlanSpec(**data)
    data = plan_dict(fixture)
    data["subtasks"][0]["criterion_checks"]["add returns the sum"] = [["bash", "-c", "true"]]
    with pytest.raises(ValueError, match="executable"):
        PlanSpec(**data)
    data = plan_dict(fixture)
    data["subtasks"][0]["allowed_paths"] = ["../etc/passwd"]
    with pytest.raises(ValueError, match="relative"):
        PlanSpec(**data)


def test_no_attempt_or_run_before_human_approval(db, fixture, tmp_path):
    plan = _plan(db, fixture)
    assert plan.status == "proposed"
    request = db.get(DecisionRequestRow, plan.decision_request_id)
    assert request.status == "pending" and request.subject_kind == "plan"
    task_id = task_id_for(plan.id, "add")
    service = ExecutionService(db, workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art")
    with pytest.raises(PlanGateError):
        service.begin(task_id=task_id, dispatch_key="k1", fixture=fixture)
    # Bypassing the service: the ORM fence refuses the row itself.
    db.add(AttemptRow(id="ATTEMPT-BYPASS", task_id=task_id, status="running", dispatch_key="k-bypass",
                      agent="x", created_at=utcnow()))
    with pytest.raises(PlanGateError):
        db.flush()
    db.rollback()
    db.add(AgentRunRow(id="RUN-BYPASS", plan_id=plan.id, status="running", config={}, started_at=utcnow()))
    with pytest.raises(PlanGateError):
        db.flush()
    db.rollback()
    coordinator = Coordinator(db, factory=lambda spec: None, workspace_root=tmp_path / "ws",
                              artifact_root=tmp_path / "art")
    with pytest.raises(PlanGateError):
        coordinator.create_run(plan.id)
    # Forcing the plan row to approved without a human-approved request is refused.
    plan.status = "approved"
    with pytest.raises(ValueError):
        db.flush()
    db.rollback()
    assert db.scalars(select(AttemptRow)).all() == []


def test_only_a_named_human_can_approve(db, fixture):
    plan = _plan(db, fixture)
    for actor in ("lead", "lead-1", "workers", "worker-1", "sdf", "manager", "policy:rules", "agent:opencode", ""):
        with pytest.raises(PlanGateError):
            decide_plan(db, plan.id, approve=True, decided_by=actor)
    with pytest.raises(PlanGateError, match="sha256"):
        decide_plan(db, plan.id, approve=True, decided_by="hung", expected_sha256="0" * 64)
    with pytest.raises(PlanGateError):
        resolve_request(db, plan.decision_request_id, outcome="approved", resolved_by="rules",
                        resolver_kind="policy")
    decide_plan(db, plan.id, approve=True, decided_by="hung", rationale="looks right",
                expected_sha256=plan.content_sha256)
    db.commit()
    request = db.get(DecisionRequestRow, plan.decision_request_id)
    assert (plan.status, request.status, request.resolver_kind, request.resolved_by) == (
        "approved", "approved", "human", "hung")
    edge = db.scalar(select(DecisionEdgeRow).where(DecisionEdgeRow.source_id == request.decision_node_id))
    assert (edge.relation, edge.target_kind, edge.target_id) == ("constrains", "plan", plan.id)
    with pytest.raises(PlanGateError):
        decide_plan(db, plan.id, approve=False, decided_by="hung")
    # Approved content and resolved requests are final.
    plan.content = {**plan.content, "title": "changed"}
    with pytest.raises(ValueError, match="immutable"):
        db.flush()
    db.rollback()
    request = db.get(DecisionRequestRow, plan.decision_request_id)
    request.status = "rejected"
    with pytest.raises(ValueError, match="final"):
        db.flush()
    db.rollback()


def test_rejected_plan_never_runs(db, fixture, tmp_path):
    plan = _plan(db, fixture)
    decide_plan(db, plan.id, approve=False, decided_by="hung", rationale="no")
    db.commit()
    service = ExecutionService(db, workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art")
    with pytest.raises(PlanGateError):
        service.begin(task_id=task_id_for(plan.id, "mul"), dispatch_key="k", fixture=fixture)


def test_require_plan_approval_refuses_unplanned_tasks(db, fixture, tmp_path):
    db.add(TaskRow(id="TASK-LOOSE", title="x", status="created", idempotency_key="loose",
                   acceptance_criteria=["a"], created_at=utcnow()))
    db.commit()
    service = ExecutionService(db, workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art")
    with pytest.raises(PlanGateError):
        service.begin(task_id="TASK-LOOSE", dispatch_key="k", fixture=fixture, require_plan_approval=True)
    assert service.begin(task_id="TASK-LOOSE", dispatch_key="k", fixture=fixture).attempt.status == "running"


# --- decision policies -------------------------------------------------------

def test_policies_never_approve_and_default_resolves_nothing():
    with pytest.raises(ValueError):
        RuleDecisionPolicy([(".*", "approved", "ok")])
    request = DecisionRequestRow(question="May I delete prod?")
    assert NoAutoResolvePolicy().decide(request) is None
    decision = RuleDecisionPolicy([("delete", "rejected", "never delete")]).decide(request)
    assert decision.outcome == "rejected"


# --- hub -----------------------------------------------------------------------

def _hub(db, fixture, approve=True) -> MessageHub:
    plan = _plan(db, fixture)
    if approve:
        decide_plan(db, plan.id, approve=True, decided_by="hung")
    run = AgentRunRow(id="RUN-H", plan_id=plan.id, status="running", config={}, started_at=utcnow())
    db.add(run)
    db.commit()
    return MessageHub(db, run.id, {
        "lead-1": Member("lead-1", "lead", None, "sbx-lead"),
        "worker-1": Member("worker-1", "worker", "add", "sbx-w1"),
        "worker-2": Member("worker-2", "worker", "mul", "sbx-w2"),
    }, max_messages_per_agent=5)


def _env(**fields) -> bytes:
    return json.dumps({"id": "a" * 31 + str(fields.pop("n", 0)), **fields}).encode()


def test_hub_authenticates_by_sandbox_and_ignores_claimed_sender(db, fixture):
    hub = _hub(db, fixture)
    res = hub.ingest("worker-1", _env(to="lead", kind="message", body="hi", **{"from": "lead-1"}),
                     sandbox_id="sbx-w1")
    assert res.message.from_agent == "worker-1" and res.recipients == ["lead-1"]
    assert res.message.meta["claimed_from_ignored"] == "lead-1"
    with pytest.raises(EnvelopeError, match="not bound"):
        hub.ingest("worker-1", _env(n=1, to="lead", kind="message", body="x"), sandbox_id="sbx-w2")
    # Redelivery of the same envelope is idempotent.
    again = hub.ingest("worker-1", _env(to="lead", kind="message", body="hi"), sandbox_id="sbx-w1")
    assert again.message.id == res.message.id and again.recipients == []


def test_hub_routing_rules(db, fixture):
    hub = _hub(db, fixture)
    assert hub.ingest("lead-1", _env(to="workers", kind="message", body="go"), sandbox_id="sbx-lead").recipients == [
        "worker-1", "worker-2"]
    for to, err in (("workers", "only the lead"), ("worker-1", "itself"), ("nobody", "unknown recipient")):
        with pytest.raises(EnvelopeError, match=err):
            hub.ingest("worker-1", _env(n=2, to=to, kind="message", body="x"), sandbox_id="sbx-w1")
    with pytest.raises(EnvelopeError, match="may not send"):
        hub.ingest("worker-1", _env(n=3, to="lead", kind="decision", body="approved"), sandbox_id="sbx-w1")
    with pytest.raises(EnvelopeError, match="may not send"):
        hub.ingest("worker-1", _env(n=4, to="lead", kind="system", body="x"), sandbox_id="sbx-w1")


def test_report_is_a_claim_not_evidence_and_decision_request_stays_pending(db, fixture):
    hub = _hub(db, fixture)
    res = hub.ingest("worker-1", _env(kind="report", status="done", body="all tests pass!"), sandbox_id="sbx-w1")
    assert res.claim is not None and res.claim.claimed_status == "done"
    assert db.scalars(select(EvidenceRow)).all() == []
    res = hub.ingest("worker-2", _env(n=5, kind="decision_request", body="Ship it?", options=["yes", "no"]),
                     sandbox_id="sbx-w2")
    request = db.get(DecisionRequestRow, res.decision_request_id)
    assert (request.status, request.requested_by, request.options) == ("pending", "worker-2", ["yes", "no"])
    with pytest.raises(PlanGateError):
        resolve_request(db, request.id, outcome="approved", resolved_by="rules", resolver_kind="policy")
    with pytest.raises(PlanGateError):
        resolve_request(db, request.id, outcome="approved", resolved_by="worker-2", resolver_kind="human")
    resolve_request(db, request.id, outcome="answered", resolved_by="hung", resolver_kind="human",
                    resolution="yes, ship")
    db.commit()
    assert request.status == "answered" and request.decision_node_id


def test_messages_and_claims_are_append_only(db, fixture):
    hub = _hub(db, fixture)
    res = hub.ingest("worker-1", _env(kind="report", body="done"), sandbox_id="sbx-w1")
    db.commit()
    res.message.body = "edited"
    with pytest.raises(ValueError, match="append-only"):
        db.flush()
    db.rollback()
    db.delete(db.get(AgentClaimRow, res.claim.id))
    with pytest.raises(ValueError, match="append-only"):
        db.flush()
    db.rollback()


def test_body_sanitized_and_limits_enforced(db, fixture):
    clean, truncated = sanitize_body("ok\u202eevil\x00\u200b" + "x" * 10, limit=8)
    assert "\u202e" not in clean and "\x00" not in clean and truncated
    hub = _hub(db, fixture)
    with pytest.raises(EnvelopeError, match="bytes"):
        hub.ingest("worker-1", _env(to="lead", kind="message", body="x" * 40000), sandbox_id="sbx-w1")
    with pytest.raises(EnvelopeError, match="32 hex"):
        hub.ingest("worker-1", json.dumps({"id": "../x", "kind": "message", "to": "lead", "body": "x"}).encode(),
                   sandbox_id="sbx-w1")
    long = hub.ingest("worker-1", _env(to="lead", kind="message", body="y" * 9000), sandbox_id="sbx-w1")
    assert long.message.truncated and len(long.message.body) <= 8100
    for n in range(1, 5):
        hub.ingest("worker-1", _env(n=n, to="lead", kind="message", body="m"), sandbox_id="sbx-w1")
    with pytest.raises(EnvelopeError, match="cap"):
        hub.ingest("worker-1", _env(n=9, to="lead", kind="message", body="m"), sandbox_id="sbx-w1")


# --- shim ------------------------------------------------------------------------

def _shim(home: Path, *args: str, stdin: str | None = None) -> str:
    out = subprocess.run([sys.executable, str(SHIM), *args], env={"HOME": str(home)}, input=stdin,
                          capture_output=True, text=True, timeout=30)
    return out.stdout + out.stderr


def test_shim_cli_writes_envelopes_and_reads_inbox(tmp_path):
    (tmp_path / ".sdf-msg").mkdir()
    (tmp_path / ".sdf-msg" / "me").write_text("worker-1")
    assert _shim(tmp_path, "whoami").strip() == "worker-1"
    _shim(tmp_path, "send", "lead", "hello", "there")
    _shim(tmp_path, "report", "--status", "done", "fixed")
    _shim(tmp_path, "request-decision", "--option", "a", "--option", "b", "which?")
    out = tmp_path / ".sdf-msg" / "outbox"
    envelopes = sorted((json.loads(p.read_text()) for p in out.glob("*.json")), key=lambda e: e["ts"])
    assert [(e["kind"], e["to"]) for e in envelopes] == [
        ("message", "lead"), ("report", "manager"), ("decision_request", "manager")]
    assert envelopes[1]["status"] == "done" and envelopes[2]["options"] == ["a", "b"]
    assert "limit" in _shim(tmp_path, "send", "lead", "-", stdin="z" * 9000)
    inbox = tmp_path / ".sdf-msg" / "inbox"
    (inbox / "00000001-x.json").write_text(json.dumps({"seq": 1, "from": "lead-1", "from_role": "lead",
                                                      "kind": "message", "body": "do it"}))
    (inbox / ".00000002-y.json.part").write_text("partial")
    text = _shim(tmp_path, "read")
    assert "do it" in text and "untrusted" in text and "partial" not in text
    assert "No new messages" in _shim(tmp_path, "read")


def test_shim_mcp_stdio(tmp_path):
    (tmp_path / ".sdf-msg").mkdir()
    (tmp_path / ".sdf-msg" / "me").write_text("lead-1")
    requests = [
        {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18"}},
        {"jsonrpc": "2.0", "method": "notifications/initialized"},
        {"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
        {"jsonrpc": "2.0", "id": 3, "method": "tools/call",
         "params": {"name": "send_message", "arguments": {"to": "worker-1", "body": "do add"}}},
        {"jsonrpc": "2.0", "id": 4, "method": "tools/call",
         "params": {"name": "report", "arguments": {"body": "x", "status": "bogus"}}},
    ]
    out = _shim(tmp_path, "mcp", stdin="\n".join(json.dumps(r) for r in requests) + "\n")
    replies = {r["id"]: r for r in map(json.loads, out.splitlines())}
    assert replies[1]["result"]["serverInfo"]["name"] == "sdf-msg"
    assert {t["name"] for t in replies[2]["result"]["tools"]} == {
        "send_message", "read_messages", "report", "request_decision"}
    assert "queued" in replies[3]["result"]["content"][0]["text"]
    assert replies[4]["result"]["isError"] is True


# --- coordinator (fake sandboxes, real shim, real Evaluator) ---------------------

def _approved(db, fixture, plan_id="PLAN-C1") -> PlanRow:
    plan = _plan(db, fixture, plan_id)
    decide_plan(db, plan.id, approve=True, decided_by="hung", expected_sha256=plan.content_sha256)
    db.commit()
    return plan


def _coordinator(db, tmp_path, behaviors, policy=None):
    running: set[str] = set()
    created: list = []
    coordinator = Coordinator(
        db, factory=fake_factory(tmp_path / "boxes", running, behaviors, created),
        workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art", policy=policy,
        cleanup_check=lambda ids: ids & running,
    )
    return coordinator, running, created


FAST = RunConfig(poll_interval_s=0.01, stall_s=0.5, max_wall_s=60, evidence_mode="synthetic")


def test_run_succeeds_only_with_pass_evidence_for_every_subtask(db, fixture, tmp_path):
    plan = _approved(db, fixture)
    coordinator, running, created = _coordinator(db, tmp_path, {
        "lead": lead_behavior, "worker": worker_behavior(), "worker-1": worker_behavior(ask_first=True)},
        policy=RuleDecisionPolicy([("refactor", "answered", "No: keep the change minimal.")]))
    run = coordinator.create_run(plan.id, FAST)
    run = coordinator.execute(run.id)
    assert run.status == "succeeded", run.error
    subs = run.summary["subtasks"]
    assert {k: v["verdict"] for k, v in subs.items()} == {"add": "PASS", "mul": "PASS"}
    for key, sub in subs.items():
        task = db.get(TaskRow, task_id_for(plan.id, key))
        assert task.status == "succeeded"
        evidence = db.scalars(select(EvidenceRow).where(EvidenceRow.attempt_id == sub["attempts"][-1]["attempt_id"])).all()
        assert len(evidence) == 3 and all(e.status == "PASS" for e in evidence)
    # Claims recorded separately from Evidence; the lead's final report is one.
    claims = db.scalars(select(AgentClaimRow).where(AgentClaimRow.run_id == run.id)).all()
    assert {c.agent_id for c in claims} == {"worker-1", "worker-2", "lead-1"}
    # The worker's decision request was answered by policy (never approved) and delivered.
    request = db.scalar(select(DecisionRequestRow).where(DecisionRequestRow.run_id == run.id))
    assert (request.status, request.resolver_kind, request.resolved_by) == ("answered", "policy", "policy:rules")
    kinds = [m.kind for m in db.scalars(select(AgentMessageRow).where(AgentMessageRow.run_id == run.id))]
    assert "decision" in kinds and "decision_request" in kinds
    assert all(s.closed for s in created) and running == set()
    assert run.summary["sandboxes_still_running"] == []


def test_lying_worker_and_test_tampering_fail_the_run(db, fixture, tmp_path):
    plan = _approved(db, fixture, "PLAN-C2")
    coordinator, running, created = _coordinator(db, tmp_path, {
        "lead": lead_behavior, "worker": worker_behavior(),
        "worker-1": worker_behavior(honest=False, tamper_tests=True)})
    run = coordinator.execute(coordinator.create_run(plan.id, FAST).id)
    assert run.status == "failed"
    add = run.summary["subtasks"]["add"]
    assert add["verdict"] == "FAIL" and add["rounds"] == 2
    first, second = add["attempts"]
    assert set(first["out_of_scope"]) == {"test_calc.py", "test_calc2.py", "evil.py"}
    retry = db.get(AttemptRow, second["attempt_id"])
    assert retry.parent_attempt_id == first["attempt_id"]
    assert run.summary["subtasks"]["mul"]["verdict"] == "PASS"
    assert db.get(TaskRow, task_id_for(plan.id, "add")).status == "failed"
    assert running == set()


def test_silent_workers_are_verified_by_host_on_stall(db, fixture, tmp_path):
    plan = _approved(db, fixture, "PLAN-C3")

    def silent(fake, prompt, turn):  # fixes the code but never reports
        for message in fake.read_json():
            if message.get("from_role") == "lead":
                key = message["body"].split("subtask ", 1)[1].split(".", 1)[0]
                from tests.agent_comm_support import FIXES
                name, content = FIXES[key]
                (fake.ws / name).write_text(content)

    coordinator, running, _ = _coordinator(db, tmp_path, {"lead": lead_behavior, "worker": silent})
    run = coordinator.execute(coordinator.create_run(plan.id, FAST).id)
    assert run.status == "succeeded", run.error
    assert running == set()


def test_cancel_closes_every_sandbox(db, fixture, tmp_path):
    plan = _approved(db, fixture, "PLAN-C4")
    gate = threading.Event()

    def stuck(fake, prompt, turn):
        gate.wait(5)

    coordinator, running, created = _coordinator(db, tmp_path, {"lead": stuck, "worker": stuck})
    run = coordinator.create_run(plan.id, FAST)
    timer = threading.Timer(0.3, lambda: (request_cancel(run.id), gate.set()))
    timer.start()
    run = coordinator.execute(run.id)
    timer.join()
    assert run.status == "cancelled"
    assert running == set() and all(s.closed for s in created)
    for key in ("add", "mul"):
        attempts = db.scalars(select(AttemptRow).where(AttemptRow.task_id == task_id_for(plan.id, key))).all()
        assert [a.status for a in attempts] == ["cancelled"]


def test_cost_cap_stops_the_run(db, fixture, tmp_path):
    plan = _approved(db, fixture, "PLAN-C5")
    coordinator, running, _ = _coordinator(db, tmp_path, {"lead": lead_behavior, "worker": worker_behavior()})
    config = RunConfig(poll_interval_s=0.01, stall_s=0.5, max_wall_s=60, max_cost_usd=0.000001)
    run = coordinator.execute(coordinator.create_run(plan.id, config).id)
    assert run.status == "failed" and "cost cap" in run.error
    assert running == set()


def test_run_config_validation():
    with pytest.raises(ValueError):
        RunConfig.from_dict({"max_cost_usd": 50})
    with pytest.raises(ValueError):
        RunConfig.from_dict({"bogus": 1})
    config = RunConfig.from_dict({"worker": {"kind": "opencode", "model": "glm-5.3"}})
    assert config.worker.kind == "opencode"
    # An unsupported kind is a 422 at the API, not a run that fails later.
    with pytest.raises(ValueError, match="not supported by the coordinator"):
        RunConfig.from_dict({"lead": {"kind": "claude"}})
    with pytest.raises(ValueError, match="worker kind"):
        RunConfig.from_dict({"worker": {"kind": "goose"}})
    with pytest.raises(ValueError):
        RunConfig.from_dict({"worker": "pi"})


def test_pending_human_decision_suspends_stall_and_is_never_auto_resolved(db, fixture, tmp_path):
    plan = _approved(db, fixture, "PLAN-C6")

    def asks_and_waits(fake, prompt, turn):
        for message in fake.read_json():
            if message.get("from_role") == "lead":
                fake.msg("request-decision", "Should I also rewrite calc.py from scratch?")

    coordinator, running, _ = _coordinator(db, tmp_path, {
        "lead": lead_behavior, "worker": worker_behavior(), "worker-1": asks_and_waits})
    config = RunConfig(poll_interval_s=0.01, stall_s=0.2, max_wall_s=3, evidence_mode="synthetic")
    run = coordinator.execute(coordinator.create_run(plan.id, config).id)
    # Without the pending request the run would have stalled out after 0.2 s; it waited for the wall cap.
    assert run.status == "failed" and "wall" in run.error
    request = db.scalar(select(DecisionRequestRow).where(DecisionRequestRow.run_id == run.id))
    assert request.status == "pending" and run.summary["pending_decision_requests"] == [request.id]
    assert run.summary["subtasks"]["mul"]["verdict"] == "PASS"
    assert running == set()
