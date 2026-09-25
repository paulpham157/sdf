"""ExecutionService driven through the E2B headless adapter.

The fake-runner tests are deterministic. The live test runs a real Codex agent
in a disposable E2B box and is gated on SDF_LIVE_E2B=1 plus E2B_API_KEY.
"""

import json
import os
import shutil
from pathlib import Path

import pytest
from sqlalchemy.orm import sessionmaker

from sdf_core.db import ArtifactRow, Base, DecisionEdgeRow, EvidenceRow, GraphNodeRow, TaskRow, make_engine
from sdf_core.execution import ExecutionService
from sdf_core.herdr_e2b import HerdrE2BAdapter, HerdrE2BNativeAdapter
from sdf_core.model import utcnow

LIVE = os.environ.get("SDF_LIVE_E2B") == "1" and bool(os.environ.get("E2B_API_KEY")) and shutil.which("e2b-box")
CHECK = [["python3", "-c", "from calc import add; assert add(2, 3) == 5"]]
POSITIVE = "Fix the bug in calc.py so add(a, b) returns a + b. Change nothing else."
NEGATIVE = "Add a module docstring to calc.py. Do not change any function body."

def _fixture(tmp_path: Path) -> Path:
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "calc.py").write_text("def add(a, b):\n    return a - b\n", encoding="utf-8")
    return fixture

def _db():
    engine = make_engine()
    Base.metadata.create_all(engine)
    db = sessionmaker(engine, expire_on_commit=False)()
    db.add(GraphNodeRow(id="ASSUMPTION-CALC", kind="assumption", title="add() is correct", source="test", owner="product", confidence=1.0, created_at=utcnow()))
    db.add(TaskRow(id="TASK-CALC", title="fix add", status="created", idempotency_key="task-calc", acceptance_criteria=["add returns the sum"], created_at=utcnow()))
    db.commit()
    return db

def _run(tmp_path: Path, adapter, instructions: str):
    db = _db()
    service = ExecutionService(db, workspace_root=tmp_path / "workspaces", artifact_root=tmp_path / "artifacts", adapter=adapter)
    attempt = service.run(
        task_id="TASK-CALC", dispatch_key="dispatch-calc", fixture=_fixture(tmp_path), instructions=instructions,
        commands=[], criterion_checks={"add returns the sum": CHECK}, validation_target=("assumption", "ASSUMPTION-CALC"),
    )
    relations = {edge.relation for edge in db.query(DecisionEdgeRow).filter_by(target_id="ASSUMPTION-CALC")}
    return db, attempt, relations

def _fake_runner(new_source: str):
    def runner(command, cwd, _timeout, _env):
        if command[1] == "run":
            (Path(cwd) / "calc.py").write_text(new_source, encoding="utf-8")
            return json.dumps({"ok": True, "status": "done", "sandboxId": "sb-fake", "pull": {"ok": True}, "agent": {"exitCode": 0, "stdout": "edited"}})
        return "{}"
    return runner

def _adapter(runner) -> HerdrE2BNativeAdapter:
    return HerdrE2BNativeAdapter(HerdrE2BAdapter(runner=runner, environ={"E2B_API_KEY": "<REDACTED>"}), live=True)

@pytest.fixture
def plugin(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setattr("sdf_core.herdr_e2b.shutil.which", lambda _: "/usr/local/bin/e2b-box")

def test_pulled_agent_edit_becomes_diff_artifact_and_validating_evidence(tmp_path: Path, plugin):
    adapter = _adapter(_fake_runner("def add(a, b):\n    return a + b\n"))
    db, attempt, relations = _run(tmp_path, adapter, POSITIVE)
    assert db.get(TaskRow, "TASK-CALC").status == "succeeded"
    assert relations == {"validates"}
    diff = db.get(ArtifactRow, f"{attempt.id}-DIFF")
    assert "+    return a + b" in Path(diff.uri).read_text()
    assert adapter.last_result.sandbox_id == "sb-fake"
    assert adapter.last_result.cleanup_succeeded

def test_agent_completion_without_meeting_criterion_contradicts(tmp_path: Path, plugin):
    adapter = _adapter(_fake_runner('"""Calculator."""\ndef add(a, b):\n    return a - b\n'))
    db, _attempt, relations = _run(tmp_path, adapter, NEGATIVE)
    assert db.get(TaskRow, "TASK-CALC").status == "failed"
    assert relations == {"contradicts"}
    assert db.query(EvidenceRow).filter_by(status="FAIL").count() == 1

@pytest.mark.skipif(not LIVE, reason="set SDF_LIVE_E2B=1 with E2B_API_KEY and e2b-box for the live Codex loop")
@pytest.mark.parametrize("instructions, task_status, relation", [
    (POSITIVE, "succeeded", "validates"),
    (NEGATIVE, "failed", "contradicts"),
])
def test_live_codex_e2b_loop(tmp_path: Path, instructions: str, task_status: str, relation: str):
    adapter = HerdrE2BNativeAdapter(template="codex", agent="codex", timeout_ms=300_000, live=True)
    db, attempt, relations = _run(tmp_path, adapter, instructions)
    result = adapter.last_result
    print(json.dumps({"attempt": attempt.id, "sandbox": result.sandbox_id, "box": result.raw.get("box"),
                      "task": db.get(TaskRow, "TASK-CALC").status, "relations": sorted(relations)}))
    assert result.cleanup_succeeded
    assert db.get(TaskRow, "TASK-CALC").status == task_status
    assert relations == {relation}


class _InteractiveHerdr:
    """Herdr 0.9.1 driving an interactive agent: a finished turn reads ``idle``.

    The prompt edits the Attempt workspace, as the agent would, and Herdr
    never reports ``done``; no test-side status override is involved.
    """

    def __init__(self, agent: str, new_source: str):
        self.agent, self.new_source, self.workspace = agent, new_source, None

    def __call__(self, command, _timeout_ms):
        operation = tuple(command[1:3])
        if operation == ("workspace", "create"):
            self.workspace = Path(command[command.index("--cwd") + 1])
            return json.dumps({"result": {"workspace": {"workspace_id": "ws-i"}, "root_pane": {"pane_id": "pane-i"}}})
        if operation == ("agent", "start"):
            return json.dumps({"result": {"agent": {"name": f"{self.agent}-i", "agent_status": "unknown", "pane_id": "pane-i"}}})
        if operation == ("agent", "get"):
            return json.dumps({"result": {"agent": {"agent_status": "idle", "interactive_ready": True}}})
        if operation == ("agent", "prompt"):
            (self.workspace / "calc.py").write_text(self.new_source, encoding="utf-8")
            return ""
        if operation == ("agent", "read"):
            return "edited calc.py\n"
        if operation == ("pane", "close"):
            return json.dumps({"type": "ok"})
        if operation == ("pane", "process-info"):
            return json.dumps({"process_info": {"shell_pid": 1, "foreground_processes": [{"pid": 1, "name": "bash"}]}})
        raise AssertionError(command)


@pytest.mark.parametrize("agent", ["codex", "claude"])
def test_execution_service_evaluates_an_interactive_herdr_attempt_whose_turn_ends_idle(tmp_path: Path, agent: str):
    from sdf_core.adapter import RuntimeAgentAdapter
    from sdf_core.herdr_runtime import HerdrRuntime
    from sdf_core.runtime import RuntimeController

    runtime = HerdrRuntime(runner=_InteractiveHerdr(agent, "def add(a, b):\n    return a + b\n"))
    controller = RuntimeController(runtime, source="herdr")
    db, attempt, relations = _run(tmp_path, RuntimeAgentAdapter(controller, agent=agent), POSITIVE)

    assert db.get(TaskRow, "TASK-CALC").status == "succeeded"
    assert relations == {"validates"}
    assert [(e.criterion, e.status) for e in db.query(EvidenceRow).filter_by(attempt_id=attempt.id)] == [
        ("add returns the sum", "PASS")
    ]
    assert [(e.kind.value, e.status.value) for e in controller.events] == [
        ("runtime_started", "running"),
        ("runtime_input_sent", "completed"),
        ("runtime_output_observed", "completed"),
        ("runtime_terminated", "terminated"),
    ]
