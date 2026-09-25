"""Live Attempt: Codex in ``subscription`` Credential Mode on the fixture Task (#14).

Gated on SDF_LIVE_E2B=1 plus E2B_API_KEY (the repo-root ``.env`` is loaded
into a private mapping, never into ``os.environ``) and on the herdr-e2b
plugin's ``codex-personal`` connection.  The Credential Mode variables are set
here, in the test environment.  Secrets are compared in-process only: a leak
is reported by variable NAME, never by value.  Teardown always kills the box.
"""

from __future__ import annotations

import hashlib
import json
import os
import time
from dataclasses import replace
from pathlib import Path

import pytest
from e2b import Sandbox

from sdf_core.adapter import RuntimeAgentAdapter
from sdf_core.credential_injection import create_credentialed_transport
from sdf_core.credentials import load_dotenv
from sdf_core.db import EvidenceRow, TaskRow
from sdf_core.execution import ExecutionService
from sdf_core.herdr_runtime import HerdrRuntime
from sdf_core.plugin_bridge import PluginConnectionBridge
from sdf_core.runtime import CredentialMetadata, RuntimeSession, RuntimeStatus
from tests.test_e2b_agents_template_live import TEMPLATE
from tests.test_e2b_herdr_transport_live import _assert_gone
from tests.test_herdr_e2b_execution import CHECK, POSITIVE, _db, _fixture

CONNECTION = os.environ.get("SDF_LIVE_CODEX_CONNECTION", "codex-personal")
ENV = dict(os.environ)
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


class _Recorder:
    """Runtime wrapper that remembers sessions, the box and the final pane.

    Herdr's ``idle`` means "ready for input", not completion, so this test
    driver supplies the Attempt lifecycle the fixture needs: wait until Codex
    is interactive before prompting, then treat its return to idle after it
    started working as the Runtime Session completing.
    """

    def __init__(self, runtime: HerdrRuntime, transport) -> None:
        self.runtime = runtime
        self.transport = transport
        self.sessions: list[RuntimeSession] = []
        self.sandbox_ids: set[str] = set()
        self.panes: list[str] = []

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

    def _state(self, session_id) -> dict:
        payload = json.loads(self.runtime._raw(self.runtime._command("agent", "get", session_id)))
        agent = payload.get("result", payload)
        return agent.get("agent", agent) if isinstance(agent, dict) else {}

    def _wait(self, session_id, ready, within_seconds: float, *, allow_blocked: bool = False) -> dict | None:
        deadline = time.monotonic() + within_seconds
        while True:
            state = self._state(session_id)
            if state.get("agent_status") == "blocked" and not allow_blocked:
                # Only the status is reported: pane text could carry anything.
                raise AssertionError("codex blocked by a dialog")
            if ready(state):
                return state
            if time.monotonic() >= deadline:
                return None
            time.sleep(1)

    def start(self, **kwargs):
        session = self._keep(self.runtime.start(**kwargs))
        ready = lambda s: s.get("agent_status") == "idle" and s.get("interactive_ready")  # noqa: E731
        assert self._wait(session.session_id, ready, 120), "codex never became interactive"
        return session

    def send(self, session_id, text):
        # Codex 0.157 takes Herdr's typed prompt into its composer but can
        # swallow the submitting Enter; press Enter again until the turn
        # visibly starts (``working``).  No text is resent, so it never doubles.
        working = lambda s: s.get("agent_status") == "working"  # noqa: E731
        session = self._keep(self.runtime.send(session_id, text))
        pane_id = self._state(session_id).get("pane_id")
        for _ in range(3):
            if self._wait(session_id, working, 8):
                break
            self.runtime._raw(self.runtime._command("pane", "send-keys", pane_id, "Enter"))
        else:
            raise AssertionError("codex never started working on the prompt")
        # A post-turn dialog (e.g. a rate-limit model switch) blocks the pane
        # after the answer; the evaluator, not the pane, judges the result.
        done = lambda s: s.get("agent_status") in {"idle", "blocked"}  # noqa: E731
        assert self._wait(session_id, done, 600, allow_blocked=True), "codex never finished its turn"
        self.answered = True
        return session

    def status(self, session_id):
        session = self.runtime.status(session_id)
        if getattr(self, "answered", False) and self._state(session_id).get("agent_status") in {"idle", "blocked"}:
            session = replace(session, status=RuntimeStatus.COMPLETED)
        session = self._keep(session)
        for source in ("visible", "recent-unwrapped"):
            self.panes.append(self.runtime._raw(self.runtime._command("agent", "read", session_id, "--source", source)))
        return session

    def terminate(self, session_id):
        return self._keep(self.runtime.terminate(session_id))


def test_live_codex_subscription_attempt(tmp_path: Path):
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
    runtime = _Recorder(HerdrRuntime(transport=transport, timeout_ms=240_000), transport)
    try:
        db = _db()
        service = ExecutionService(
            db, workspace_root=tmp_path / "workspaces", artifact_root=tmp_path / "artifacts",
            adapter=RuntimeAgentAdapter(runtime, agent="codex"),
        )
        attempt = service.run(
            task_id="TASK-CALC", dispatch_key="dispatch-calc-subscription", fixture=_fixture(tmp_path),
            instructions=POSITIVE, commands=[], criterion_checks={"add returns the sum": CHECK},
            validation_target=("assumption", "ASSUMPTION-CALC"),
        )
        evidence = db.query(EvidenceRow).filter_by(attempt_id=attempt.id).all()
        pane = "\n".join(runtime.panes + [line for s in runtime.sessions for line in s.output])
        leaked = _leaks(pane, secrets) + _leaks(repr(injection) + "\n".join(injection.seed_commands), secrets)
        print(json.dumps({
            "attempt": attempt.id, "task": db.get(TaskRow, "TASK-CALC").status,
            "statuses": [s.status.value for s in runtime.sessions],
            "credential": runtime.sessions[0].credential.as_dict() if runtime.sessions else None,
            "evidence": [(e.criterion, e.status) for e in evidence], "leaked": leaked,
            "pane_sha256": hashlib.sha256(pane.encode()).hexdigest(), "sandboxes": sorted(runtime.sandbox_ids),
        }))

        assert leaked == [], f"secrets visible in pane scrollback: {leaked}"
        assert runtime.sessions, "no Runtime Session was started"
        expected = CredentialMetadata("subscription", CONNECTION)
        assert all(s.credential == expected for s in runtime.sessions)
        assert "calc.py" in pane, "codex did not answer the prompt in its pane"
        assert db.get(TaskRow, "TASK-CALC").status == "succeeded"
        assert [(e.criterion, e.status) for e in evidence] == [("add returns the sum", "PASS")]
    finally:
        try:
            transport.close()
        finally:
            for sandbox_id in runtime.sandbox_ids:
                try:
                    Sandbox.kill(sandbox_id, api_key=ENV["E2B_API_KEY"])
                except Exception:  # noqa: BLE001 - best-effort teardown
                    pass
    for sandbox_id in runtime.sandbox_ids:
        _assert_gone(sandbox_id)
