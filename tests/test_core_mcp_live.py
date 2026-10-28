"""Live ADR 0022: an agent in E2B reads its approved plan's hash from SDF Core.

Skipped by default.  Gated on ``SDF_LIVE_E2B=1`` and ``SDF_LIVE_CORE_MCP=1`` plus
E2B/Vultr keys (presence only; values never printed).  Kind via
``SDF_LIVE_CORE_KIND`` (opencode | pi).  One sandbox, one turn: the agent must
query ``get_plan`` through ``sdf-core`` (CLI or MCP) and write the hash to
HASH.txt; the host serves requests with the coordinator's own relay while the
turn runs, and also checks a forged ``approve_plan`` is denied.
"""

from __future__ import annotations

import json
import os
import shutil
import threading
import time
import uuid
from pathlib import Path

import pytest
from sqlalchemy import select
from sqlalchemy.orm import sessionmaker

from sdf_core.agent_sandbox import DEFAULT_TEMPLATE, AgentSpec, E2BAgentSandbox, agent_environ, running_sandbox_ids
from sdf_core.coordinator import Coordinator, RunConfig
from sdf_core.core_mcp import REQ_DIR, CoreQueryService, scope_for
from sdf_core.credentials import load_dotenv
from sdf_core.db import AgentRunMemberRow, Base, PlanRow, make_engine
from sdf_core.plans import PlanSpec, decide_plan, propose_plan
from tests.agent_comm_support import make_fixture, plan_dict

ENV = dict(os.environ)
load_dotenv(environ=ENV)
LIVE = (ENV.get("SDF_LIVE_E2B") == "1" and ENV.get("SDF_LIVE_CORE_MCP") == "1"
        and bool(ENV.get("E2B_API_KEY")) and bool(ENV.get("VULTR_INFERENCE_API_KEY")))
pytestmark = pytest.mark.skipif(not LIVE, reason="live: set SDF_LIVE_E2B=1 SDF_LIVE_CORE_MCP=1 with E2B/Vultr keys")
KIND = ENV.get("SDF_LIVE_CORE_KIND", "pi")
TEMPLATE = ENV.get("SDF_E2B_AGENT_TEMPLATE", DEFAULT_TEMPLATE)
REPORT_DIR = Path("/tmp/sdf-live-reports")
# "mcp" forces OpenCode's registered sdfcore MCP server instead of the CLI.
VIA = ENV.get("SDF_LIVE_CORE_VIA", "any")

PROMPT = ("Task: read your approved plan from SDF Core. Run the shell command `sdf-core get_plan` "
          "(or call the sdf-core MCP tool get_plan). It prints JSON with a field content_sha256. "
          "Write exactly that 64-hex value, nothing else, into a file HASH.txt in the current directory. "
          "Do nothing else and then stop.")


def test_live_agent_reads_plan_hash(tmp_path):
    engine = make_engine(f"sqlite+pysqlite:///{tmp_path / 'core.db'}")
    Base.metadata.create_all(engine)
    db = sessionmaker(engine, expire_on_commit=False)()
    fixture = make_fixture(tmp_path)
    plan = propose_plan(db, PlanSpec(**plan_dict(fixture, f"PLAN-CORE-{uuid.uuid4().hex[:8]}")))
    db.commit()
    decide_plan(db, plan.id, approve=True, decided_by="hung", expected_sha256=plan.content_sha256)
    db.commit()
    coordinator = Coordinator(db, factory=lambda spec: None, workspace_root=tmp_path / "ws",
                              artifact_root=tmp_path / "art")
    role = "lead" if KIND == "opencode" else "worker"
    run = coordinator.create_run(plan.id, RunConfig())
    db.commit()
    agent_id = "lead-1" if role == "lead" else "worker-1"
    member = db.scalar(select(AgentRunMemberRow).where(AgentRunMemberRow.run_id == run.id,
                                                       AgentRunMemberRow.agent_id == agent_id))
    key = ENV["E2B_API_KEY"]
    sandbox = E2BAgentSandbox(AgentSpec(agent_id, role, KIND), environ=agent_environ(ENV, {KIND}),
                              template=TEMPLATE, turn_timeout_s=300)
    report: dict = {"kind": KIND, "template": TEMPLATE, "agent": agent_id}
    ws = tmp_path / "agent-ws"
    shutil.copytree(fixture, ws)
    t0 = time.monotonic()
    try:
        sandbox.start(ws)
        member.sandbox_id = sandbox.sandbox_id
        db.commit()
        service = CoreQueryService(db, scope_for(db, run.id, agent_id))
        core_log: list = []
        violations: list = []
        agent = type("A", (), {"sandbox": sandbox})()

        # A forged request straight into the relay dir: denied by the host.
        rid = uuid.uuid4().hex
        sandbox._fs().write(f"{REQ_DIR}/{rid}.json",
                            json.dumps({"id": rid, "tool": "approve_plan", "args": {"plan_id": plan.id}}))
        result: dict = {}
        prompt = PROMPT if VIA != "mcp" else PROMPT.replace(
            "Run the shell command `sdf-core get_plan` (or call the sdf-core MCP tool get_plan)",
            "Call the MCP tool sdfcore_get_plan (do NOT use the shell or the sdf-core CLI for this; "
            "you may use your file-write tool)")
        report["via"] = VIA
        th = threading.Thread(target=lambda: result.update(turn=sandbox.turn(prompt)))
        th.start()
        while th.is_alive():
            coordinator._serve_core(agent_id, agent, service, core_log, violations)
            time.sleep(0.5)
        coordinator._serve_core(agent_id, agent, service, core_log, violations)
        turn = result["turn"]
        report.update(turn_ok=turn.ok, turn_error=turn.error, turn_s=round(turn.seconds, 1), turn_tail=turn.tail,
                      core_log=core_log, violations=violations, usage=sandbox.usage())
        try:
            got = bytes(sandbox._fs().read(f"{sandbox.workspace_path}/HASH.txt", format="bytes")).decode().strip()
        except Exception as exc:  # noqa: BLE001
            got = f"<missing: {type(exc).__name__}>"
        report.update(hash_written=got, hash_expected=plan.content_sha256)
    finally:
        sandbox.close()
        report["alive_s"] = round(sandbox.alive_seconds(), 1)
        report["total_wall_s"] = round(time.monotonic() - t0, 1)
        report["still_running"] = sorted({sandbox.sandbox_id} & running_sandbox_ids(key)) if sandbox.sandbox_id else []
        REPORT_DIR.mkdir(exist_ok=True)
        out = REPORT_DIR / f"core-mcp-{KIND}-{int(time.time())}.json"
        out.write_text(json.dumps(report, indent=2, default=str))
        print("report:", out)

    assert report["still_running"] == []
    assert report["turn_ok"], report["turn_error"]
    assert report["hash_written"] == plan.content_sha256
    assert any(q["tool"] == "get_plan" and q["ok"] for q in report["core_log"])
    assert any(q["tool"] == "approve_plan" and q["denied"] for q in report["core_log"])
    db.expire_all()
    assert db.get(PlanRow, plan.id).status == "approved"
    db.close()
    engine.dispose()
