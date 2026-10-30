"""#60 / ADR 0033: a pending *material* agent decision request blocks its scope until a human resolves it."""

from __future__ import annotations

import json
import threading
import time
import uuid
from pathlib import Path

import pytest
from sqlalchemy import select
from sqlalchemy.orm import sessionmaker

from sdf_core.agent_messaging import EnvelopeError, Member, MessageHub
from sdf_core.coordinator import Coordinator, RunConfig
from sdf_core.db import (
    AgentMessageRow, AgentRunMemberRow, AgentRunRow, AttemptRow, Base, DecisionRequestRow, EvidenceRow, PlanGateError,
    make_engine,
)
from sdf_core.decision_gate import DecisionGateError, check_decision_gate, is_material, pending_material
from sdf_core.execution import ExecutionService
from sdf_core.model import utcnow
from sdf_core.plans import PlanSpec, RuleDecisionPolicy, decide_plan, propose_plan, resolve_request, task_id_for
from tests.agent_comm_support import FIXES, fake_factory, lead_behavior, make_fixture, plan_dict, worker_behavior
from tests.auth_support import client_as

QUESTION = "Should deleted accounts keep their invoices?"
CONSEQUENCE = "Keeping them retains personal data; deleting them breaks tax reporting."


@pytest.fixture()
def engine(tmp_path: Path):
    # A file database: the test resolves requests from a second session while the run is going.
    engine = make_engine(f"sqlite+pysqlite:///{tmp_path / 'gate.sqlite'}")
    Base.metadata.create_all(engine)
    try:
        yield engine
    finally:
        engine.dispose()


@pytest.fixture()
def db(engine):
    session = sessionmaker(engine, expire_on_commit=False)()
    try:
        yield session
    finally:
        session.close()


@pytest.fixture()
def fixture(tmp_path: Path) -> Path:
    return make_fixture(tmp_path)


def _approved(db, fixture, plan_id):
    plan = propose_plan(db, PlanSpec(**plan_dict(fixture, plan_id)))
    db.commit()
    decide_plan(db, plan.id, approve=True, decided_by="hung")
    db.commit()
    return plan


def _coordinator(db, tmp_path, behaviors, policy=None):
    running: set[str] = set()
    created: list = []
    return Coordinator(
        db, factory=fake_factory(tmp_path / "boxes", running, behaviors, created),
        workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art", policy=policy,
        cleanup_check=lambda ids: ids & running,
    ), running, created


def asks_then_works(*, material: bool, asked_at: dict | None = None):
    """Asks a question, then (wrongly) goes ahead anyway: fixes the code and reports done in the same turn."""

    def behave(fake, prompt, turn):
        for message in fake.read_json():
            if message.get("from_role") != "lead":
                continue
            if asked_at is not None:
                asked_at[fake.spec.agent_id] = turn
            key = message["body"].split("subtask ", 1)[1].split(".", 1)[0]
            args = ["request-decision", QUESTION, "--option", "keep", "--option", "delete"]
            if material:
                args += ["--material", "--consequence", CONSEQUENCE]
            out = fake.msg(*args)
            assert "queued" in out, out
            name, content = FIXES[key]
            (fake.ws / name).write_text(content)
            fake.msg("report", "--status", "done", f"Subtask {key} done.")
    return behave


def _config(wall: float = 60) -> RunConfig:
    return RunConfig(poll_interval_s=0.01, stall_s=0.3, max_wall_s=wall, evidence_mode="synthetic")


def _resolve_when_pending(engine, plan_id: str, outcome: str, resolution: str = "", seen: dict | None = None):
    """From another session (like the API): wait for a pending material request, then resolve it as a human."""

    def work():
        Session = sessionmaker(engine, expire_on_commit=False)
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            with Session() as other:
                runs = list(other.scalars(select(AgentRunRow.id).where(AgentRunRow.plan_id == plan_id)))
                gate = pending_material(other, run_ids=runs)
                if gate:
                    time.sleep(0.3)  # several coordinator polls while blocked
                    if seen is not None:
                        seen["add_evidence_after_wait"] = list(other.scalars(select(EvidenceRow.criterion)))
                    resolve_request(other, gate[0].request_id, outcome=outcome, resolved_by="hung",
                                    resolver_kind="human", resolution=resolution)
                    other.commit()
                    return
            time.sleep(0.02)

    thread = threading.Thread(target=work)
    thread.start()
    return thread


ADD_CRITERIA = {"add returns the sum", "sub still subtracts", "test_calc passes"}


# --- the gate holds -------------------------------------------------------------


def test_pending_material_request_blocks_its_subtask_and_never_completes(db, fixture, tmp_path, monkeypatch):
    plan = _approved(db, fixture, "PLAN-G1")
    asked_at: dict = {}

    def lead_behavior_with_ping(fake, prompt, turn):
        messages = fake.read_json()
        if any(m.get("from") == "worker-2" for m in messages):
            fake.msg("send", "worker-1", "worker-2 is done; what is your status?")
        replay = iter([messages])
        original = fake.read_json
        fake.read_json = lambda: next(replay, [])
        try:
            lead_behavior(fake, prompt, turn)
        finally:
            fake.read_json = original

    def worker2(fake, prompt, turn):
        worker_behavior()(fake, prompt, turn)
        if (fake.ws / "calc2.py").read_text().count("def mul") and not getattr(fake, "pinged", False):
            fake.pinged = True
            fake.msg("send", "lead", "mul is done")

    coordinator, running, created = _coordinator(db, tmp_path, {
        "lead": lead_behavior_with_ping, "worker-2": worker2,
        "worker-1": asks_then_works(material=True, asked_at=asked_at)})
    run = coordinator.execute(coordinator.create_run(plan.id, _config(wall=2.5)).id)
    # Nobody decided: the run waited for the human until the wall cap; it never succeeded.
    assert run.status == "failed" and "wall" in run.error
    subs = run.summary["subtasks"]
    assert subs["mul"]["verdict"] == "PASS"  # the other subtask was not blocked
    assert subs["add"]["verdict"] is None and subs["add"]["attempts"] == []  # never verified
    add_task = task_id_for(plan.id, "add")
    attempts = list(db.scalars(select(AttemptRow.id).where(AttemptRow.task_id == add_task)))
    assert db.scalars(select(EvidenceRow).where(EvidenceRow.attempt_id.in_(attempts))).all() == []
    request = db.scalar(select(DecisionRequestRow).where(DecisionRequestRow.run_id == run.id))
    assert request.status == "pending" and is_material(db, request)
    [entry] = run.summary["material_decisions"]
    assert entry["request_id"] == request.id and entry["scope"] == "add" and entry["status"] == "pending"
    assert entry["consequence"] == CONSEQUENCE and entry["options"] == ["keep", "delete"]
    assert "verification:add" in entry["held"] and "turn:worker-1" in entry["held"]
    worker = next(s for s in created if s.spec.agent_id == "worker-1")
    assert worker.inbox_count() >= 1  # it had unread mail (the lead's ping) and still got no turn
    assert worker.turns == asked_at["worker-1"]  # no further turn while its decision was pending
    # Outside the run, too, the Task cannot be dispatched or recorded.
    with pytest.raises(DecisionGateError, match=request.id):
        check_decision_gate(db, add_task)
    with pytest.raises(DecisionGateError):
        ExecutionService(db, workspace_root=tmp_path / "ws2", artifact_root=tmp_path / "art2").begin(
            task_id=add_task, dispatch_key="after-run", fixture=fixture)
    check_decision_gate(db, task_id_for(plan.id, "mul"))  # a different subtask is not blocked
    # An Attempt already running when the gate closes cannot record Evidence; it stays running.
    import sdf_core.execution as execution_module
    from sdf_core.adapter import AdapterResult
    from sdf_core.db import AttemptRow as _Attempt
    service = ExecutionService(db, workspace_root=tmp_path / "ws3", artifact_root=tmp_path / "art3")
    monkeypatch.setattr(execution_module, "check_decision_gate", lambda *_a, **_k: None)
    attempt_id = service.begin(task_id=add_task, dispatch_key="in-flight", fixture=fixture).attempt.id
    monkeypatch.undo()
    with pytest.raises(DecisionGateError):
        service.finish(attempt_id=attempt_id, result=AdapterResult(attempt_id=attempt_id, status="succeeded", changed_files=()),
                       fixture=fixture, commands=[])
    assert db.get(_Attempt, attempt_id).status == "running"
    assert running == set()


def test_non_material_request_does_not_block(db, fixture, tmp_path):
    plan = _approved(db, fixture, "PLAN-G2")
    coordinator, running, _ = _coordinator(db, tmp_path, {
        "lead": lead_behavior, "worker": worker_behavior(), "worker-1": asks_then_works(material=False)})
    run = coordinator.execute(coordinator.create_run(plan.id, _config()).id)
    assert run.status == "succeeded", run.error
    request = db.scalar(select(DecisionRequestRow).where(DecisionRequestRow.run_id == run.id))
    assert request.status == "pending" and not is_material(db, request)  # recorded, unanswered, blocked nothing
    assert run.summary["material_decisions"] == []
    check_decision_gate(db, task_id_for(plan.id, "add"))


def test_human_approval_unblocks_and_only_then_is_evidence_recorded(engine, db, fixture, tmp_path):
    plan = _approved(db, fixture, "PLAN-G3")
    coordinator, running, created = _coordinator(db, tmp_path, {
        "lead": lead_behavior, "worker": worker_behavior(), "worker-1": asks_then_works(material=True)})
    seen: dict = {}
    resolver = _resolve_when_pending(engine, plan.id, "approved", "keep them", seen)
    run = coordinator.execute(coordinator.create_run(plan.id, _config()).id)
    resolver.join()
    assert run.status == "succeeded", run.error
    # While pending, none of the add subtask's criteria had Evidence.
    assert not ADD_CRITERIA & set(seen["add_evidence_after_wait"])
    assert run.summary["subtasks"]["add"]["verdict"] == "PASS"
    request = db.scalar(select(DecisionRequestRow).where(DecisionRequestRow.run_id == run.id)
                        .execution_options(populate_existing=True))
    assert (request.status, request.resolver_kind, request.resolved_by) == ("approved", "human", "hung")
    [entry] = run.summary["material_decisions"]
    assert entry["status"] == "approved" and "verification:add" in entry["held"]
    decisions = [m for m in db.scalars(select(AgentMessageRow).where(
        AgentMessageRow.run_id == run.id, AgentMessageRow.kind == "decision"))]
    assert [m.to_agent for m in decisions] == ["worker-1"] and "APPROVED" in decisions[0].body
    assert running == set()


def test_human_rejection_lifts_the_block_and_tells_the_agent(engine, db, fixture, tmp_path):
    plan = _approved(db, fixture, "PLAN-G4")
    coordinator, running, _ = _coordinator(db, tmp_path, {
        "lead": lead_behavior, "worker": worker_behavior(), "worker-1": asks_then_works(material=True)})
    resolver = _resolve_when_pending(engine, plan.id, "rejected", "neither: escalate to legal")
    run = coordinator.execute(coordinator.create_run(plan.id, _config()).id)
    resolver.join()
    # Rejection is a human resolution: the gate lifts, the host verifies on Evidence as usual,
    # and the agent is told the outcome (it may not take the rejected option).
    assert run.status == "succeeded", run.error
    [decision] = [m for m in db.scalars(select(AgentMessageRow).where(
        AgentMessageRow.run_id == run.id, AgentMessageRow.kind == "decision"))]
    assert decision.to_agent == "worker-1" and "REJECTED" in decision.body and "escalate to legal" in decision.body
    assert run.summary["material_decisions"][0]["status"] == "rejected"


def test_lead_material_request_holds_the_whole_run_and_completion(engine, db, fixture, tmp_path):
    plan = _approved(db, fixture, "PLAN-G5")

    def lead(fake, prompt, turn):
        for message in fake.read_json():
            body = message["body"]
            if message["from"] == "sdf" and body.startswith("APPROVED PLAN"):
                for line in body.splitlines():
                    if line.startswith("- worker-"):
                        fake.msg("send", line[2:].split(" ", 1)[0], f"Please do subtask {line.split('`')[1]}.")
            elif message["from"] == "sdf" and "every subtask is verified PASS" in body:
                fake.msg("request-decision", "Release now or after review?", "--option", "now", "--option",
                         "after review", "--material", "--consequence", "Releasing now skips security review.")
                fake.msg("report", "--status", "done", "All verified.")

    coordinator, running, _ = _coordinator(db, tmp_path, {"lead": lead, "worker": worker_behavior()})
    run = coordinator.execute(coordinator.create_run(plan.id, _config(wall=2)).id)
    assert run.status == "failed" and "wall" in run.error  # all PASS, yet no success while pending
    [entry] = run.summary["material_decisions"]
    assert entry["scope"] == "run" and "completion" in entry["held"]
    for key in ("add", "mul"):
        with pytest.raises(DecisionGateError):
            check_decision_gate(db, task_id_for(plan.id, key))

    plan2 = _approved(db, make_fixture(tmp_path / "second"), "PLAN-G5B")
    coordinator, running, _ = _coordinator(db, tmp_path / "b", {"lead": lead, "worker": worker_behavior()})
    resolver = _resolve_when_pending(engine, plan2.id, "answered", "after review")
    run = coordinator.execute(coordinator.create_run(plan2.id, _config()).id)
    resolver.join()
    assert run.status == "succeeded", run.error


# --- authority, envelope and API -----------------------------------------------------


def _hub(db, fixture, plan_id="PLAN-GH"):
    plan = _approved(db, fixture, plan_id)
    run = AgentRunRow(id=f"RUN-{plan_id}", plan_id=plan.id, status="running", config={}, started_at=utcnow())
    db.add(run)
    db.flush()
    for agent_id, role, key in (("lead-1", "lead", None), ("worker-1", "worker", "add"), ("worker-2", "worker", "mul")):
        db.add(AgentRunMemberRow(run_id=run.id, agent_id=agent_id, role=role, agent_kind="pi", subtask_key=key,
                                 status="ready", turns=0))
    db.commit()
    return plan, MessageHub(db, run.id, {
        "lead-1": Member("lead-1", "lead", None, "sbx-l"),
        "worker-1": Member("worker-1", "worker", "add", "sbx-w1"),
        "worker-2": Member("worker-2", "worker", "mul", "sbx-w2")})


def _env(n: int, **fields) -> bytes:
    return json.dumps({"id": f"{n:032x}", "kind": "decision_request", "body": QUESTION, **fields}).encode()


def test_material_envelope_validation_and_policy_cannot_resolve(db, fixture):
    plan, hub = _hub(db, fixture)
    for fields, message in (
        ({"options": ["a", "b"], "material": True}, "needs a consequence"),
        ({"options": ["a", "b"], "material": True, "consequence": "  "}, "needs a consequence"),
        ({"options": ["a"], "material": True, "consequence": "x"}, "two distinct options"),
        ({"options": ["a", "a"], "material": True, "consequence": "x"}, "two distinct options"),
        ({"options": ["a", "b"], "material": "yes", "consequence": "x"}, "true or false"),
    ):
        with pytest.raises(EnvelopeError, match=message):
            hub.ingest("worker-1", _env(1, **fields), sandbox_id="sbx-w1")
        db.rollback()
    res = hub.ingest("worker-1", _env(2, options=["keep", "delete"], material=True, consequence=CONSEQUENCE),
                     sandbox_id="sbx-w1")
    db.commit()
    request = db.get(DecisionRequestRow, res.decision_request_id)
    assert res.message.meta == {"options": ["keep", "delete"], "material": True, "consequence": CONSEQUENCE,
                                "authority": "human"}
    assert [g.scope for g in pending_material(db, run_id=request.run_id)] == ["add"]
    # A configured policy never resolves a material decision (it could only pick a default for the agent).
    policy = RuleDecisionPolicy([("invoices", "answered", "keep them")])
    assert policy.decide(request) is not None  # the rule matches the text...
    with pytest.raises(PlanGateError, match="only by a human"):
        resolve_request(db, request.id, outcome="answered", resolved_by="rules", resolver_kind="policy",
                        resolution="keep them")
    db.rollback()
    assert db.get(DecisionRequestRow, request.id).status == "pending"
    # A non-material request from the other worker blocks nothing and may still be policy-answered.
    res2 = hub.ingest("worker-2", _env(3, options=["x", "y"]), sandbox_id="sbx-w2")
    resolve_request(db, res2.decision_request_id, outcome="answered", resolved_by="rules", resolver_kind="policy",
                    resolution="x")
    db.commit()
    check_decision_gate(db, task_id_for(plan.id, "mul"))
    with pytest.raises(DecisionGateError):
        check_decision_gate(db, task_id_for(plan.id, "add"))


def test_shim_writes_a_material_envelope_and_refuses_an_incomplete_one(tmp_path):
    import os
    import subprocess
    import sys

    from tests.agent_comm_support import SHIM

    home = tmp_path / "home"
    (home / ".sdf-msg").mkdir(parents=True)
    (home / ".sdf-msg" / "me").write_text("worker-1")
    env = {"HOME": str(home), "PATH": os.environ.get("PATH", "")}

    def shim(*args):
        return subprocess.run([sys.executable, str(SHIM), *args], env=env, capture_output=True, text=True, timeout=30)

    bad = shim("request-decision", QUESTION, "--option", "a", "--option", "b", "--material")
    assert "needs --consequence" in bad.stderr
    ok = shim("request-decision", QUESTION, "--option", "a", "--option", "b", "--material", "--consequence", "c")
    assert "material decision request queued" in ok.stdout, ok.stderr
    [envelope] = [json.loads(p.read_text()) for p in (home / ".sdf-msg" / "outbox").glob("*.json")]
    assert (envelope["material"], envelope["consequence"], envelope["body"]) == (True, "c", QUESTION)


def test_api_reports_materiality_scope_and_human_resolution(tmp_path):
    from sdf_core.api import SessionLocal

    operator = client_as("operator", principal="planner")
    approver = client_as("approver", principal="hung")
    plan_id = f"PLAN-GAPI-{uuid.uuid4().hex[:8]}"
    body = operator.post("/plans", json=plan_dict(make_fixture(tmp_path), plan_id)).json()
    assert approver.post(f"/plans/{plan_id}/approve", json={"expected_sha256": body["content_sha256"]}).status_code == 200
    db = SessionLocal()
    try:
        run_id = f"RUN-{plan_id}"
        db.add(AgentRunRow(id=run_id, plan_id=plan_id, status="running", config={}, started_at=utcnow()))
        db.flush()
        db.add(AgentRunMemberRow(run_id=run_id, agent_id="worker-1", role="worker", agent_kind="pi",
                                 subtask_key="add", status="ready", turns=0))
        db.commit()
        hub = MessageHub(db, run_id, {"worker-1": Member("worker-1", "worker", "add", "sbx-w1")})
        hub.ingest("worker-1", _env(7, options=["keep", "delete"], material=True, consequence=CONSEQUENCE),
                   sandbox_id="sbx-w1")
        db.commit()
    finally:
        db.close()
    [listed] = operator.get("/decision-requests", params={"run_id": run_id}).json()
    assert (listed["material"], listed["consequence"], listed["authority"], listed["blocks"]) == (
        True, CONSEQUENCE, "human", "add")
    resolved = approver.post(f"/decision-requests/{listed['id']}/resolve",
                             json={"outcome": "answered", "resolution": "keep them"})
    assert resolved.status_code == 200
    assert (resolved.json()["status"], resolved.json()["material"], resolved.json()["blocks"]) == (
        "answered", True, None)
