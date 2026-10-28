"""ADR 0022: read-only, run-scoped SDF Core access for agents (authz + relay)."""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
import tomllib
import uuid
from pathlib import Path

import pytest
from sqlalchemy import select
from sqlalchemy.orm import sessionmaker

from sdf_core import core_mcp
from sdf_core.agent_sandbox import _install_script
from sdf_core.auth import is_reserved_name
from sdf_core.coordinator import Coordinator, RunConfig
from sdf_core.core_mcp import AgentScope, CoreAuthzError, CoreQueryService, scope_for
from sdf_core.db import AgentMessageRow, Base, DecisionRequestRow, PlanRow, make_engine
from sdf_core.plans import PlanSpec, decide_plan, propose_plan, task_id_for
from tests.agent_comm_support import CORE_SHIM, fake_factory, lead_behavior, make_fixture, plan_dict, worker_behavior


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


def _approved(db, fixture: Path, plan_id: str) -> PlanRow:
    plan = propose_plan(db, PlanSpec(**plan_dict(fixture, plan_id)))
    db.commit()
    decide_plan(db, plan.id, approve=True, decided_by="hung", expected_sha256=plan.content_sha256)
    db.commit()
    return plan


def _coordinator(db, tmp_path, behaviors):
    running: set[str] = set()
    created: list = []
    return Coordinator(db, factory=fake_factory(tmp_path / "boxes", running, behaviors, created),
                       workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art",
                       cleanup_check=lambda ids: ids & running), running, created


FAST = RunConfig(poll_interval_s=0.01, stall_s=0.5, max_wall_s=60, evidence_mode="synthetic")


@pytest.fixture()
def two_runs(db, tmp_path):
    """Two approved plans, each with a created run (no sandboxes needed for authz)."""
    out = []
    for n in (1, 2):
        fixture = make_fixture(tmp_path / f"f{n}")
        plan = _approved(db, fixture, f"PLAN-R{n}")
        coordinator, _, _ = _coordinator(db, tmp_path / f"c{n}", {"lead": lead_behavior, "worker": worker_behavior()})
        run = coordinator.create_run(plan.id, FAST)
        db.commit()
        out.append((plan, run))
    return out


def _svc(db, run, agent_id="worker-1") -> CoreQueryService:
    return CoreQueryService(db, scope_for(db, run.id, agent_id))


def _raw(tool, **args) -> bytes:
    return json.dumps({"id": uuid.uuid4().hex, "tool": tool, "args": args}).encode()


def test_scope_is_a_run_viewer_that_can_never_approve(db, two_runs):
    (_, run), _ = two_runs
    scope = scope_for(db, run.id, "lead-1")
    assert scope.roles == {"run_viewer"} and scope.kind == "agent" and scope.can_approve is False
    assert not scope.has("approver") and not scope.has("viewer")
    # ADR 0015: agent ids can never be API principals either.
    assert is_reserved_name("lead-1") and is_reserved_name("worker-2") and is_reserved_name("pi:worker-1")
    with pytest.raises(CoreAuthzError):
        CoreQueryService(db, AgentScope(run.id, run.plan_id, "lead-1", "lead", None, None,
                                        roles=frozenset({"run_viewer", "approver"})))
    with pytest.raises(CoreAuthzError):
        scope_for(db, run.id, "worker-9")


@pytest.mark.parametrize("tool", sorted(core_mcp.DENIED_TOOLS) + ["drop_tables", ""])
def test_writes_and_unknown_tools_are_denied(db, two_runs, tool):
    (_, run), _ = two_runs
    res = _svc(db, run).handle(_raw(tool, plan_id=run.plan_id), sandbox_id=None)
    assert res["ok"] is False and res["denied"] is True


def test_cross_run_access_is_denied(db, two_runs):
    (plan1, run1), (plan2, run2) = two_runs
    svc = _svc(db, run1)
    other_task = task_id_for(plan2.id, "add")
    other_decision = db.scalar(select(DecisionRequestRow.id).where(DecisionRequestRow.subject_id == plan2.id))
    for tool, args in [("run_status", {"run_id": run2.id}), ("get_plan", {"plan_id": plan2.id}),
                       ("get_task", {"task_id": other_task}), ("get_decision", {"decision_id": other_decision}),
                       ("get_evidence", {"attempt_id": "ATT-nope"}), ("read_message", {"message_id": "MSG-nope"})]:
        res = svc.handle(_raw(tool, **args), sandbox_id=None)
        assert res["ok"] is False and res["denied"] is True, (tool, res)
    # The agent's own objects are readable.
    assert svc.handle(_raw("get_task"), sandbox_id=None)["result"]["task_id"] == task_id_for(plan1.id, "add")
    decisions = svc.handle(_raw("list_decisions"), sandbox_id=None)["result"]
    assert [d["subject_id"] for d in decisions] == [plan1.id]
    own = svc.handle(_raw("get_decision", decision_id=decisions[0]["id"]), sandbox_id=None)["result"]
    assert own["status"] == "approved" and "principal" not in own["decision_node"]["metadata"]


def test_sandbox_binding_and_malformed_requests(db, two_runs):
    (_, run), _ = two_runs
    member_scope = scope_for(db, run.id, "worker-1")
    svc = CoreQueryService(db, AgentScope(run.id, run.plan_id, "worker-1", "worker", "add", "sbx-1"))
    assert svc.handle(_raw("run_status"), sandbox_id="sbx-2")["denied"] is True
    assert svc.handle(_raw("run_status"), sandbox_id="sbx-1")["ok"] is True
    assert svc.handle(b"not json", sandbox_id="sbx-1")["ok"] is False
    assert svc.handle(json.dumps({"id": "x", "tool": "run_status"}).encode(), sandbox_id="sbx-1")["id"] is None
    assert svc.handle(b"{" + b" " * core_mcp.MAX_REQUEST_BYTES + b"}", sandbox_id="sbx-1")["ok"] is False
    assert member_scope.sandbox_id is None  # no sandbox started in this fixture


def test_plan_hash_is_reverified_and_host_paths_stay_on_host(db, two_runs):
    (plan, run), _ = two_runs
    res = _svc(db, run).handle(_raw("get_plan"), sandbox_id=None)["result"]
    assert res["content_sha256"] == plan.content_sha256 and res["hash_verified"] is True
    assert "fixture_dir" not in res["content"] and res["approval"]["status"] == "approved"


def test_query_cap(db, two_runs, monkeypatch):
    (_, run), _ = two_runs
    monkeypatch.setattr(core_mcp, "MAX_QUERIES_PER_AGENT", 2)
    svc = _svc(db, run)
    assert [svc.handle(_raw("run_status"), sandbox_id=None)["ok"] for _ in range(3)] == [True, True, False]


def test_relay_end_to_end_with_real_shim_and_message_visibility(db, tmp_path):
    fixture = make_fixture(tmp_path / "f")
    plan = _approved(db, fixture, "PLAN-E2E")
    seen: dict[str, str] = {}

    def worker(fake, prompt, turn):
        if fake.spec.agent_id == "worker-1" and turn == 1:
            seen["plan"] = fake.core("get_plan")
            seen["task"] = fake.core("get_task")
            seen["cross"] = fake.core("get_task", json.dumps({"task_id": "PLAN-OTHER--add"}))
            seen["approve"] = fake.core("approve_plan")  # refused by the shim itself
            # A forged request that bypasses the shim is refused by the host.
            rid = uuid.uuid4().hex
            req = fake.core_dir / "req"
            (req / f"{rid}.json").write_text(json.dumps({"id": rid, "tool": "approve_plan",
                                                         "args": {"plan_id": plan.id}}))
            resp = fake.core_dir / "resp" / f"{rid}.json"
            for _ in range(2000):
                if resp.exists():
                    break
                time.sleep(0.01)
            seen["forged"] = resp.read_text()
            seen["msgs"] = fake.core("list_messages")
        worker_behavior()(fake, prompt, turn)

    coordinator, running, created = _coordinator(db, tmp_path, {"lead": lead_behavior, "worker": worker})
    run = coordinator.execute(coordinator.create_run(plan.id, FAST).id)
    assert run.status == "succeeded", run.error
    got = json.loads(seen["plan"])
    assert got["content_sha256"] == plan.content_sha256 and got["hash_verified"] is True
    assert json.loads(seen["task"])["subtask_key"] == "add"
    assert "denied" in seen["cross"] and "unknown tool" in seen["approve"]
    forged = json.loads(seen["forged"])
    assert forged["ok"] is False and forged["denied"] is True
    msgs = json.loads(seen["msgs"])
    assert msgs and all("worker-1" in (m["from"], m["to"]) or m["to"] is None for m in msgs)
    log = run.summary["core_queries"]
    assert [(q["tool"], q["ok"]) for q in log] == [
        ("get_plan", True), ("get_task", True), ("get_task", False), ("approve_plan", False), ("list_messages", True)]
    assert db.get(PlanRow, plan.id).status == "approved"
    # Queries never wrote to the hub.
    assert db.scalar(select(AgentMessageRow).where(AgentMessageRow.kind == "decision")) is None
    assert running == set()


def test_worker_sees_only_its_own_messages_lead_sees_all(db, tmp_path):
    fixture = make_fixture(tmp_path / "f")
    plan = _approved(db, fixture, "PLAN-VIS")
    coordinator, _, _ = _coordinator(db, tmp_path, {"lead": lead_behavior, "worker": worker_behavior()})
    run = coordinator.execute(coordinator.create_run(plan.id, FAST).id)
    all_msgs = db.scalars(select(AgentMessageRow).where(AgentMessageRow.run_id == run.id)).all()
    lead = _svc(db, run, "lead-1").query("list_messages", {"limit": 100})
    w2 = _svc(db, run, "worker-2").query("list_messages", {"limit": 100})
    assert len(lead) == len(all_msgs) and 0 < len(w2) < len(all_msgs)
    hidden = next(m for m in all_msgs if "worker-2" not in (m.from_agent, m.to_agent) and m.to_agent)
    with pytest.raises(CoreAuthzError):
        _svc(db, run, "worker-2").query("read_message", {"message_id": hidden.id})


@pytest.mark.parametrize("kind", ["opencode", "claude", "codex", "pi", "qwen", "goose"])
def test_registration_is_user_level_and_idempotent(tmp_path, kind):
    env = {"HOME": str(tmp_path), "PATH": os.environ.get("PATH", "")}
    for _ in range(2):
        subprocess.run([sys.executable, "-c", core_mcp.registration_script(kind)], env=env, check=True, cwd=tmp_path)
    if kind == "opencode":
        c = json.loads((tmp_path / ".config/opencode/opencode.json").read_text())
        assert c["mcp"]["servers"]["sdfcore"]["command"] == core_mcp.MCP_COMMAND
    elif kind == "claude":
        assert json.loads((tmp_path / ".claude.json").read_text())["mcpServers"]["sdf-core"]["command"] == "python3"
    elif kind == "codex":
        text = (tmp_path / ".codex/config.toml").read_text()
        assert text.count("[mcp_servers.sdf_core]") == 1
        assert tomllib.loads(text)["mcp_servers"]["sdf_core"]["args"] == core_mcp.MCP_COMMAND[1:]
    else:
        assert not any(tmp_path.iterdir())  # CLI only; nothing written
    # Never in the workspace / project config.
    assert not list(tmp_path.glob("ws/**/*"))


def test_install_script_installs_core_and_goose_args():
    script = _install_script("worker-1", False, "pi", core=True)
    assert core_mcp.CORE_SHIM_PATH in script and core_mcp.REQ_DIR in script
    assert core_mcp.CORE_SHIM_PATH not in _install_script("worker-1", False, "pi", core=False)
    assert core_mcp.goose_extension_args() == ["--with-extension", "sdfcore:python3 /usr/local/bin/sdf-core mcp"]


def _goose_argv(core: bool, auto: bool) -> list[str]:
    from sdf_core.goose_runtime import GooseHeadlessRuntime
    from sdf_core.headless_runtime import HeadlessBinding

    rt = GooseHeadlessRuntime(transport=type("T", (), {"exec_argv": lambda *a: ""})(), core_mcp=core)
    binding = HeadlessBinding("ATT-1", "/tmp/sdf/ATT-1", auto)
    return rt.agent_argv(binding, "/tmp/p")


def test_goose_turn_argv_carries_the_core_extension_only_when_enabled():
    with_core = _goose_argv(True, True)
    i = with_core.index("--with-extension")
    assert with_core[i + 1] == "sdfcore:python3 /usr/local/bin/sdf-core mcp"
    assert "--no-profile" in with_core and with_core.count("--with-extension") == 1
    assert "--with-extension" not in _goose_argv(False, True)
    # Chat (read-only, no tools) turns load no extension at all.
    assert "--with-extension" not in _goose_argv(True, False)


def test_e2b_agent_sandbox_hosts_goose_but_the_coordinator_roles_do_not():
    from sdf_core.agent_sandbox import SANDBOX_KINDS, SUPPORTED_KINDS, AgentSpec, E2BAgentSandbox

    assert "goose" in SANDBOX_KINDS and "goose" not in SUPPORTED_KINDS
    E2BAgentSandbox(AgentSpec("worker-1", "worker", "goose"), environ={})
    with pytest.raises(ValueError):
        E2BAgentSandbox(AgentSpec("worker-1", "worker", "qwen"), environ={})


def test_shim_mcp_lists_read_tools_plus_message_post(tmp_path):
    lines = "\n".join(json.dumps(m) for m in [
        {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}},
        {"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
        {"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "approve_plan", "arguments": {}}},
    ]) + "\n"
    out = subprocess.run([sys.executable, str(CORE_SHIM), "mcp"], input=lines, capture_output=True, text=True,
                         env={"HOME": str(tmp_path), "PATH": os.environ.get("PATH", "")}, timeout=20)
    replies = [json.loads(line) for line in out.stdout.splitlines()]
    names = {t["name"] for t in replies[1]["result"]["tools"]}
    assert names == set(core_mcp.READ_TOOLS) | {"send_message"}
    assert replies[2]["result"]["isError"] is True and not (tmp_path / ".sdf-core/req").exists() or \
        not list((tmp_path / ".sdf-core/req").iterdir())
