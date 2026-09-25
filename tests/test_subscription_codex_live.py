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
from pathlib import Path

import pytest
from e2b import Sandbox

from sdf_core.adapter import RuntimeAgentAdapter
from sdf_core.credential_injection import create_credentialed_transport
from sdf_core.credentials import load_dotenv
from sdf_core.db import EvidenceRow, RuntimeEventRow, TaskRow
from sdf_core.execution import ExecutionService
from sdf_core.herdr_runtime import HerdrRuntime
from sdf_core.plugin_bridge import PluginConnectionBridge
from sdf_core.runtime import (
    CredentialMetadata,
    RuntimeController,
    RuntimeSession,
    RuntimeStatus,
    SqlAlchemyRuntimeEventSink,
)
from tests.test_e2b_agents_template_live import TEMPLATE
from tests.test_e2b_herdr_transport_live import _assert_gone
from tests.test_herdr_e2b_execution import CHECK, POSITIVE, _db, _fixture

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


# The user's pinned model for live Codex Attempts; passed as test-side start
# argv only.  Production code carries no model selection (ADR 0005, 07a).
MODEL = "gpt-6-luna"


class _Recorder:
    """Pass-through runtime wrapper that only observes: sessions, box, panes.

    It changes no status and drives no keys: turn completion and Codex
    prompt resubmission are the runtime's own job (#16).
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

    def start(self, **kwargs):
        return self._keep(self.runtime.start(**kwargs))

    def send(self, session_id, text):
        return self._keep(self.runtime.send(session_id, text))

    def status(self, session_id):
        session = self._keep(self.runtime.status(session_id))
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
    runtime = _Recorder(
        HerdrRuntime(transport=transport, timeout_ms=240_000, agent_args={"codex": ("-m", MODEL)}), transport
    )
    try:
        db = _db()
        service = ExecutionService(
            db, workspace_root=tmp_path / "workspaces", artifact_root=tmp_path / "artifacts",
            # The controller records the live Runtime Session lifecycle (ticket 08).
            adapter=RuntimeAgentAdapter(
                RuntimeController(runtime, event_sink=SqlAlchemyRuntimeEventSink(db), source="herdr-e2b"),
                agent="codex",
            ),
        )
        attempt = service.run(
            task_id="TASK-CALC", dispatch_key="dispatch-calc-subscription", fixture=_fixture(tmp_path),
            instructions=POSITIVE, commands=[], criterion_checks={"add returns the sum": CHECK},
            validation_target=("assumption", "ASSUMPTION-CALC"),
        )
        evidence = db.query(EvidenceRow).filter_by(attempt_id=attempt.id).all()
        events = db.query(RuntimeEventRow).filter_by(attempt_id=attempt.id).order_by(RuntimeEventRow.sequence).all()
        pane = "\n".join(runtime.panes + [line for s in runtime.sessions for line in s.output])
        leaked = _leaks(pane, secrets) + _leaks("\n".join((repr(injection), *injection.seed_commands)), secrets)
        print(json.dumps({
            "attempt": attempt.id, "task": db.get(TaskRow, "TASK-CALC").status,
            "statuses": [s.status.value for s in runtime.sessions], "model": MODEL,
            "credential": runtime.sessions[0].credential.as_dict() if runtime.sessions else None,
            "evidence": [(e.criterion, e.status) for e in evidence], "leaked": leaked,
            # Kinds, sources and sequences only: payloads carry pane text.
            "events": [(e.sequence, e.source, e.kind, e.status) for e in events],
            "pane_sha256": hashlib.sha256(pane.encode()).hexdigest(), "sandboxes": sorted(runtime.sandbox_ids),
        }))

        assert leaked == [], f"secrets visible in pane scrollback: {leaked}"
        assert runtime.sessions, "no Runtime Session was started"
        expected = CredentialMetadata("subscription", CONNECTION)
        credentials_ok = all(s.credential == expected for s in runtime.sessions)
        assert credentials_ok, "a Runtime Session lacks the subscription metadata"
        # Booleans only: assertion rewriting would otherwise echo the pane.
        answered = "calc.py" in pane
        assert answered, "codex did not answer the prompt in its pane"
        pinned = MODEL in pane
        assert pinned, "the Codex pane does not show the pinned model"
        final = [s.status for s in runtime.sessions]
        assert RuntimeStatus.COMPLETED in final, "the Codex turn never completed"
        assert db.get(TaskRow, "TASK-CALC").status == "succeeded"
        assert [(e.criterion, e.status) for e in evidence] == [("add returns the sum", "PASS")]
        assert [(e.sequence, e.source, e.kind, e.status) for e in events] == [
            (1, "herdr-e2b", "runtime_started", "running"),
            (2, "herdr-e2b", "runtime_input_sent", "completed"),
            (3, "herdr-e2b", "runtime_output_observed", "completed"),
            (4, "herdr-e2b", "runtime_terminated", "terminated"),
        ]
        assert events[0].payload == {"credential_mode": "subscription", "connection_id": CONNECTION}
        assert len({e.session_id for e in events}) == 1
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
