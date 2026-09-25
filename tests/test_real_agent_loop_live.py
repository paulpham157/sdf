"""Live Attempt: Real agent loop with independent evidence (issue #2).

Gated on SDF_LIVE_E2B=1 plus E2B_API_KEY (the repo-root ``.env`` is loaded
into a private mapping, never into ``os.environ``) and on the herdr-e2b
plugin's ``codex-personal`` connection.  Two cases: positive (agent fixes bug,
both criteria pass) and negative (agent completes without meeting criterion,
target fails but guard holds). Terminal output persisted only as Artifact;
public boundary verified; network.request verified denied; cleanup kills all sandboxes.
"""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path

import pytest
from e2b import Sandbox
from fastapi.testclient import TestClient

from sdf_core.adapter import RuntimeAgentAdapter
from sdf_core.api import app, get_db, configured_tool_policy
from sdf_core.credential_injection import create_credentialed_transport
from sdf_core.credentials import load_dotenv
from sdf_core.db import (
    ArtifactRow, EvidenceRow, RuntimeEventRow, TaskRow, GraphNodeRow, DecisionEdgeRow,
    make_engine, Base,
)
from sdf_core.execution import ExecutionService
from sdf_core.herdr_runtime import HerdrRuntime
from sdf_core.model import utcnow
from sdf_core.plugin_bridge import PluginConnectionBridge
from sdf_core.policy import ActionRequest
from sdf_core.runtime import (
    CredentialMetadata,
    RuntimeController,
    RuntimeSession,
    RuntimeStatus,
    SqlAlchemyRuntimeEventSink,
)
from sqlalchemy.orm import sessionmaker
from tests.test_e2b_agents_template_live import TEMPLATE
from tests.test_e2b_herdr_transport_live import _assert_gone, _running_ids
from tests.test_herdr_e2b_execution import POSITIVE, NEGATIVE

CONNECTION = os.environ.get("SDF_LIVE_CODEX_CONNECTION", "codex-personal")
ENV = dict(os.environ)
if os.environ.get("SDF_DOTENV"):
    load_dotenv(os.environ["SDF_DOTENV"], ENV)
load_dotenv(environ=ENV)
LIVE = os.environ.get("SDF_LIVE_E2B") == "1" and bool(ENV.get("E2B_API_KEY"))

pytestmark = pytest.mark.skipif(not LIVE, reason="live E2B check: set SDF_LIVE_E2B=1 with E2B_API_KEY (env or .env)")


def _secrets(material_value: str) -> dict[str, str]:
    """Every secret that must never reach a pane, keyed by a printable name."""

    secrets = {"CODEX_AUTH_JSON": material_value}
    try:
        tokens = json.loads(material_value).get("tokens") or {}
    except ValueError:
        tokens = {}
    for name, value in tokens.items():
        if isinstance(value, str) and len(value) >= 12:
            secrets[f"CODEX_AUTH_JSON.tokens.{name}"] = value
    for name in ("E2B_API_KEY", "SDF_ANTHROPIC_API_KEY", "SDF_OPENAI_API_KEY", "ANTHROPIC_API_KEY"):
        if len(ENV.get(name, "")) >= 12:
            secrets[name] = ENV[name]
    return secrets


def _leaks(text: str, secrets: dict[str, str]) -> list[str]:
    # Match a 16-character slice too, so a wrapped or truncated secret still counts.
    return sorted(
        name for name, value in secrets.items() if value in text or any(
            value[i : i + 16] in text for i in range(0, max(len(value) - 16, 0) + 1, 8)
        )
    )


# The user's pinned model for live Codex Attempts
MODEL = "gpt-6-luna"


def _fixture_with_two_functions(tmp_path: Path) -> Path:
    """Fixture with both add (buggy) and sub (correct) functions for criterion and guard checks."""
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "calc.py").write_text(
        "def add(a, b):\n    return a - b\n\ndef sub(a, b):\n    return a - b\n",
        encoding="utf-8"
    )
    return fixture


def _db_two_criteria():
    """Database with two acceptance criteria: one target (add returns sum) and one guard (sub unchanged)."""
    from sdf_core.db import Base, make_engine
    from sqlalchemy.orm import sessionmaker

    engine = make_engine()
    Base.metadata.create_all(engine)
    db = sessionmaker(engine, expire_on_commit=False)()
    db.add(GraphNodeRow(
        id="ASSUMPTION-CALC", kind="assumption", title="add() and sub() are correct",
        source="test", owner="product", confidence=1.0, created_at=utcnow()
    ))
    db.add(TaskRow(
        id="TASK-CALC", title="fix add", status="created", idempotency_key="task-calc-two-criteria",
        acceptance_criteria=["add returns the sum", "sub unchanged"],
        created_at=utcnow()
    ))
    db.commit()
    return db


# Criterion checks for the two criteria
CHECK_ADD = [["python3", "-c", "from calc import add; assert add(2, 3) == 5"]]
CHECK_SUB = [["python3", "-c", "from calc import sub; assert sub(5, 3) == 2"]]


def _check_only_calc_changed(fixture: Path) -> list[list[str]]:
    """Guard check run in the Attempt workspace: no file besides calc.py was
    added, removed or modified relative to the fixture it was copied from.
    Agent/tool caches (__pycache__, .git, dotfiles) are not fixture files."""

    script = (
        "import pathlib, sys\n"
        "def files(root):\n"
        "    root = pathlib.Path(root)\n"
        "    return {str(p.relative_to(root)): p.read_bytes() for p in root.rglob('*')\n"
        "            if p.is_file() and not any(part.startswith('.') or part == '__pycache__' for part in p.relative_to(root).parts)}\n"
        "before, after = files(sys.argv[1]), files('.')\n"
        "changed = {k for k in before.keys() | after.keys() if before.get(k) != after.get(k)}\n"
        "assert changed <= {'calc.py'}, sorted(changed)\n"
    )
    return [["python3", "-c", script, str(fixture)]]


class _Recorder:
    """Pass-through runtime wrapper that only observes: sessions, box, panes."""

    def __init__(self, runtime: HerdrRuntime, transport) -> None:
        self.runtime = runtime
        self.transport = transport
        self.sessions: list[RuntimeSession] = []
        self.sandbox_ids: set[str] = set()
        self.panes: list[str] = []
        self.during_attempt = None

    def __getattr__(self, name):
        return getattr(self.runtime, name)

    def _keep(self, session: RuntimeSession) -> RuntimeSession:
        self.sessions.append(session)
        if self.transport.sandbox_id:
            self.sandbox_ids.add(self.transport.sandbox_id)
        return session

    def bind_workspace(self, attempt_id, workspace):
        self.runtime.bind_workspace(attempt_id, workspace)
        if self.transport.sandbox_id:
            self.sandbox_ids.add(self.transport.sandbox_id)

    def start(self, **kwargs):
        return self._keep(self.runtime.start(**kwargs))

    def send(self, session_id, text):
        session = self._keep(self.runtime.send(session_id, text))
        # The Tool Proxy only admits actions for an active Attempt, so any
        # structured-action probe runs here, mid-Attempt, not after run().
        if self.during_attempt is not None:
            self.during_attempt(session.attempt_id)
        return session

    def status(self, session_id):
        session = self._keep(self.runtime.status(session_id))
        for source in ("visible", "recent-unwrapped"):
            self.panes.append(self.runtime._raw(self.runtime._command("agent", "read", session_id, "--source", source)))
        return session

    def terminate(self, session_id):
        return self._keep(self.runtime.terminate(session_id))


@pytest.mark.parametrize("task_case", [
    {
        "name": "positive",
        "instructions": POSITIVE,
        "expected_task_status": "succeeded",
        "expected_edge": "validates",
        "criteria_expected": {"add returns the sum": "PASS", "sub unchanged": "PASS"},
    },
    {
        "name": "negative",
        "instructions": NEGATIVE,
        "expected_task_status": "failed",
        "expected_edge": "contradicts",
        "criteria_expected": {"add returns the sum": "FAIL", "sub unchanged": "PASS"},
    },
])
def test_live_real_agent_loop_with_independent_evidence(tmp_path: Path, task_case: dict):
    """Run a real Codex agent with positive and negative/guard criteria; verify network.request denied."""
    environ = {
        "E2B_API_KEY": ENV["E2B_API_KEY"],
        **({"E2B_DOMAIN": ENV["E2B_DOMAIN"]} if ENV.get("E2B_DOMAIN") else {}),
        **{name: ENV[name] for name in ("HOME", "PATH", "USER", "XDG_CONFIG_HOME") if name in ENV},
        "SDF_CREDENTIAL_MODE_CODEX": "subscription",
        "SDF_CONNECTION_CODEX": CONNECTION,
    }
    bridge = PluginConnectionBridge(environ=environ)
    material = bridge("codex", CONNECTION)
    secrets = _secrets(material.variables["CODEX_AUTH_JSON"])

    transport, injection = create_credentialed_transport(
        template=TEMPLATE, agents=("codex",), environ=environ, read_connection=bridge, timeout_seconds=900
    )
    runtime = _Recorder(
        HerdrRuntime(transport=transport, timeout_ms=240_000, agent_args={"codex": ("-m", MODEL)}), transport
    )

    try:
        # Create a test database session with two criteria
        db_for_run = _db_two_criteria()
        service = ExecutionService(
            db_for_run, workspace_root=tmp_path / "workspaces", artifact_root=tmp_path / "artifacts",
            adapter=RuntimeAgentAdapter(
                RuntimeController(runtime, event_sink=SqlAlchemyRuntimeEventSink(db_for_run), source="herdr-e2b"),
                agent="codex",
            ),
        )


        # ====== NETWORK POLICY PROBE (runs mid-Attempt, see _Recorder.send) ======
        # Through the real Tool Proxy on the live Attempt workspace, with the
        # production E2B containment backend.  Even an allowlist that names
        # network:request cannot run it: the backend has no routed network
        # action, so the executor fails the request (no fetch happens).  The
        # server-configured policy in E2B mode strips network:request even when
        # an operator lists it, so that request is denied before execution.
        from sdf_core.e2b_containment import E2BContainmentBackend
        from sdf_core.policy import AllowlistPolicy

        probe_results: list = []

        def _network_request(attempt_id: str, probe: str) -> ActionRequest:
            # action_id is derived from every field, so the probe label keeps the two requests distinct.
            return ActionRequest(
                attempt_id=attempt_id, actor="agent:codex", tool="network", action="request",
                resource="https://example.invalid", context={"method": "GET", "body": b"", "probe": probe},
            )

        def _network_probe(attempt_id: str) -> None:
            backend = E2BContainmentBackend(template=TEMPLATE, environ={"E2B_API_KEY": ENV["E2B_API_KEY"]})
            forced_request = _network_request(attempt_id, "forced")
            forced = service.execute_tool(
                attempt_id=attempt_id, request=forced_request, policy=AllowlistPolicy({("network", "request")}),
                containment_backend=backend, require_containment=True,
            )
            saved = {name: os.environ.get(name) for name in ("SDF_CONTAINMENT_BACKEND", "SDF_TOOL_ALLOWLIST")}
            os.environ.update(SDF_CONTAINMENT_BACKEND="e2b", SDF_TOOL_ALLOWLIST="network:request")
            try:
                policy_request = _network_request(attempt_id, "policy")
                denied = service.execute_tool(
                    attempt_id=attempt_id, request=policy_request, policy=configured_tool_policy(),
                    containment_backend=backend, require_containment=True,
                )
            finally:
                for name, value in saved.items():
                    if value is None:
                        os.environ.pop(name, None)
                    else:
                        os.environ[name] = value
            probe_results.extend((forced, denied, forced_request, policy_request))

        runtime.during_attempt = _network_probe

        # Run the attempt with two criterion checks
        attempt = service.run(
            task_id="TASK-CALC", dispatch_key=f"dispatch-real-agent-{task_case['name']}",
            fixture=(fixture := _fixture_with_two_functions(tmp_path)),
            instructions=task_case["instructions"], commands=[],
            # Guard: sub() still correct AND calc.py is the only changed file.
            criterion_checks={"add returns the sum": CHECK_ADD,
                              "sub unchanged": CHECK_SUB + _check_only_calc_changed(fixture)},
            validation_target=("assumption", "ASSUMPTION-CALC"),
        )

        # Extract data from the test database
        evidence = db_for_run.query(EvidenceRow).filter_by(attempt_id=attempt.id).all()
        # Runtime lifecycle only; the network probe adds separate source="tool-proxy" rows.
        events = db_for_run.query(RuntimeEventRow).filter_by(attempt_id=attempt.id, source="herdr-e2b").order_by(RuntimeEventRow.sequence).all()
        artifacts = db_for_run.query(ArtifactRow).filter_by(attempt_id=attempt.id).all()
        task_row = db_for_run.get(TaskRow, "TASK-CALC")

        # Collect pane text
        pane = "\n".join(runtime.panes + [line for s in runtime.sessions for line in s.output])
        leaked = _leaks(pane, secrets) + _leaks("\n".join((repr(injection), *injection.seed_commands)), secrets)

        # Find LOG artifact
        log_artifact = None
        for art in artifacts:
            if "-LOG" in art.id:
                log_artifact = art
                break

        # Verify terminal output is only in Artifact, not in Evidence
        evidence_artifact_refs = [e.artifact_ref for e in evidence]
        if log_artifact:
            assert log_artifact.id not in evidence_artifact_refs, \
                "LOG artifact should not be referenced by Evidence rows"

        # Print diagnostic info
        criteria_statuses = {e.criterion: e.status for e in evidence}
        print(json.dumps({
            "case": task_case["name"],
            "attempt": attempt.id,
            "task": task_row.status if task_row else None,
            "sandboxes": sorted(runtime.sandbox_ids),
            "statuses": [s.status.value for s in runtime.sessions],
            "criteria": criteria_statuses,
            "artifacts": [a.id for a in artifacts],
            "leaked": leaked,
            "events": [(e.sequence, e.source, e.kind, e.status) for e in events],
        }))

        # Assertions
        assert leaked == [], f"secrets visible in pane scrollback: {leaked}"
        assert runtime.sessions, "no Runtime Session was started"

        expected_credential = CredentialMetadata("subscription", CONNECTION)
        credentials_ok = all(s.credential == expected_credential for s in runtime.sessions)
        assert credentials_ok, "a Runtime Session lacks the subscription metadata"

        # Verify task status
        assert task_row.status == task_case["expected_task_status"], \
            f"expected task status {task_case['expected_task_status']}, got {task_row.status}"

        # Verify both criteria (target + guard)
        for criterion, expected_status in task_case["criteria_expected"].items():
            found = [(e.criterion, e.status) for e in evidence if e.criterion == criterion]
            assert found, f"criterion '{criterion}' not found in evidence"
            assert found[0][1] == expected_status, \
                f"expected criterion '{criterion}' status {expected_status}, got {found[0][1]}"

        # Verify edge to assumption
        edges = db_for_run.query(DecisionEdgeRow).filter_by(target_id="ASSUMPTION-CALC").all()
        edge_relations = {e.relation for e in edges}
        assert task_case["expected_edge"] in edge_relations, \
            f"expected edge relation {task_case['expected_edge']}, got {edge_relations}"

        # Verify runtime events
        assert events, "no runtime events were recorded"
        expected_events = [
            (1, "herdr-e2b", "runtime_started", "running"),
            (2, "herdr-e2b", "runtime_input_sent", "completed"),
            (3, "herdr-e2b", "runtime_output_observed", "completed"),
            (4, "herdr-e2b", "runtime_terminated", "terminated"),
        ]
        actual_events = [(e.sequence, e.source, e.kind, e.status) for e in events]
        assert actual_events == expected_events, \
            f"expected events {expected_events}, got {actual_events}"

        # Verify single session ID
        session_ids = {e.session_id for e in events}
        assert len(session_ids) == 1, f"expected single session ID, got {len(session_ids)}"

        # Verify runtime completion (agent egress allowed: Codex reached OpenAI API)
        final_statuses = [s.status for s in runtime.sessions]
        assert RuntimeStatus.COMPLETED in final_statuses, "the Codex turn never completed"
        assert "calc.py" in pane, "codex did not answer; agent egress likely blocked"

        # ====== NETWORK POLICY: REAL ASSERTION ======
        # Agent egress is allowed (Codex reached OpenAI API successfully).
        # Structured network.request is denied by the E2B containment backend.
        # Verify the configured tool policy denies network.request.
        forced, denied, forced_request, policy_request = probe_results
        assert forced.status.value == "failed" and "network.request is disabled" in (forced.error or "")
        assert denied.decision.effect.value == "deny" and denied.status.value != "succeeded"
        tool_events = [
            (e.kind, (e.payload or {}).get("action_id"), (e.payload or {}).get("decision"))
            for e in db_for_run.query(RuntimeEventRow).filter_by(attempt_id=attempt.id, source="tool-proxy")
        ]
        assert {k for k, _, _ in tool_events} >= {"tool_policy_decided", "tool_action_failed"}
        assert {a for _, a, _ in tool_events} == {forced_request.action_id, policy_request.action_id}
        # Terminal output created no tool/policy events.
        assert not [e for e in events if e.kind.startswith("tool_")]
        print(json.dumps({"attempt": attempt.id, "network": {
            "forced": forced.status.value, "policy": denied.decision.effect.value,
            "tool_events": sorted((kind, decision) for kind, _, decision in tool_events)}}))

        # ====== PUBLIC BOUNDARY VERIFICATION ======
        def override_get_db():
            try:
                yield db_for_run
            finally:
                pass

        app.dependency_overrides[get_db] = override_get_db

        try:
            client = TestClient(app)

            # Test GET /tasks/{id}/trace with tight assertions
            trace_response = client.get(f"/tasks/TASK-CALC/trace")
            assert trace_response.status_code == 200, f"trace endpoint failed: {trace_response.text}"
            trace_data = trace_response.json()

            # Verify nodes contain attempt, artifacts (DIFF, LOG), and evidence
            nodes = trace_data.get("nodes", [])
            node_ids = {node.get("id") for node in nodes}
            assert attempt.id in node_ids, f"attempt {attempt.id} not in trace nodes"

            diff_artifacts = [a for a in artifacts if "-DIFF" in a.id]
            log_artifacts = [a for a in artifacts if "-LOG" in a.id]
            assert any(a.id in node_ids for a in diff_artifacts), "DIFF artifact not in trace nodes"
            assert any(a.id in node_ids for a in log_artifacts), "LOG artifact not in trace nodes"

            evidence_output_ids = [a.id for a in artifacts if "-OUTPUT" in a.id]
            assert any(a_id in node_ids for a_id in evidence_output_ids), \
                "evidence output artifact not in trace nodes"

            # Verify runtime events in trace
            runtime_events = trace_data.get("runtime_events", [])
            by_source = {src: [e for e in runtime_events if e["source"] == src] for src in ("herdr-e2b", "tool-proxy")}
            assert len(by_source["herdr-e2b"]) == 4, f"expected 4 runtime events in trace, got {len(by_source['herdr-e2b'])}"
            assert len(by_source["tool-proxy"]) == len(runtime_events) - 4 >= 3, "tool-proxy probe events missing from trace"

            # Test GET /evidence/{id} for each criterion
            for evidence_row in evidence:
                evidence_response = client.get(f"/evidence/{evidence_row.id}")
                assert evidence_response.status_code == 200, \
                    f"evidence endpoint failed for {evidence_row.id}: {evidence_response.text}"
                evidence_data = evidence_response.json()
                assert evidence_data.get("criterion") in ["add returns the sum", "sub unchanged"], \
                    f"unexpected criterion in evidence: {evidence_data.get('criterion')}"
                assert evidence_data.get("status") in task_case["criteria_expected"].values(), \
                    f"unexpected evidence status: {evidence_data.get('status')}"

            # Test GET /attempts/{id}/runtime-events
            runtime_events_response = client.get(f"/attempts/{attempt.id}/runtime-events")
            assert runtime_events_response.status_code == 200, \
                f"runtime-events endpoint failed: {runtime_events_response.text}"
            runtime_events_data = runtime_events_response.json()
            herdr_rows = [e for e in runtime_events_data if e["source"] == "herdr-e2b"]
            assert len(herdr_rows) == 4, f"expected 4 runtime events, got {len(herdr_rows)}"
            assert {e["source"] for e in runtime_events_data} == {"herdr-e2b", "tool-proxy"}

        finally:
            app.dependency_overrides.clear()

    finally:
        try:
            transport.close()
        finally:
            for sandbox_id in runtime.sandbox_ids:
                try:
                    Sandbox.kill(sandbox_id, api_key=ENV["E2B_API_KEY"])
                except Exception:  # noqa: BLE001 - best-effort teardown
                    pass

    # Verify all sandboxes are gone
    for sandbox_id in runtime.sandbox_ids:
        _assert_gone(sandbox_id)
