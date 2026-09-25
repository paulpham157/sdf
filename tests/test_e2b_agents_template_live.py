"""Claude and Codex start from the sdf-herdr-agents template with no dialog.

Gated on SDF_LIVE_E2B=1 plus E2B_API_KEY; no Agent Credential is injected (that
is the credential-injection ticket's job), so this proves only that each agent
reaches its interactive prompt. Teardown always kills the sandbox.
"""

import json
import os
import re
import time

import pytest
from e2b import Sandbox

from sdf_core.e2b_herdr_transport import E2BHerdrTransport
from sdf_core.herdr_runtime import HerdrRuntime
from sdf_core.runtime import RuntimeSession, RuntimeStatus

LIVE = os.environ.get("SDF_LIVE_E2B") == "1" and bool(os.environ.get("E2B_API_KEY"))
TEMPLATE = os.environ.get("SDF_E2B_AGENTS_TEMPLATE", "sdf-herdr-agents")
# Credential-like host variables must never reach the sandbox.
_STRIPPED = ("ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "OPENAI_API_KEY")
# First-run dialogs the template's shared state must suppress.
_DIALOGS = re.compile(
    r"choose the text style|select login method|let's get started|welcome to claude code"
    r"|bypass permissions mode|yes, i accept|trust this folder|do you trust the files|dark mode",
    re.IGNORECASE,
)

pytestmark = pytest.mark.skipif(not LIVE, reason="live E2B check: set SDF_LIVE_E2B=1 and E2B_API_KEY to run")


@pytest.fixture
def runtime():
    environ = {key: value for key, value in os.environ.items() if key not in _STRIPPED}
    transport = E2BHerdrTransport(template=TEMPLATE, environ=environ, timeout_seconds=600)
    created: list[str] = []
    try:
        yield HerdrRuntime(transport=transport, timeout_ms=120_000), transport, created
    finally:
        if transport.sandbox_id:
            created.append(transport.sandbox_id)
        try:
            transport.close()
        finally:
            for sandbox_id in created:
                try:
                    Sandbox.kill(sandbox_id, api_key=os.environ["E2B_API_KEY"])
                except Exception:  # noqa: BLE001 - best-effort teardown
                    pass


def _agent_state(runtime: HerdrRuntime, session_id: str) -> dict:
    payload = json.loads(runtime._raw(runtime._command("agent", "get", session_id)))
    agent = payload.get("result", payload)
    return agent.get("agent", agent) if isinstance(agent, dict) else {}


def _wait_ready(runtime: HerdrRuntime, session_id: str, within_seconds: float = 60, settle_seconds: float = 12) -> dict:
    """Wait for idle + interactive_ready, then require it to hold.

    Claude reports idle before its TUI draws a late first-run dialog (for
    example folder trust), at which point Herdr flips it to blocked.
    """

    deadline = time.monotonic() + within_seconds
    ready_since = None
    while True:
        state = _agent_state(runtime, session_id)
        assert state.get("agent_status") != "blocked", f"agent blocked by a prompt: {state}"
        if state.get("agent_status") == "idle" and state.get("interactive_ready") is True:
            ready_since = ready_since or time.monotonic()
            if time.monotonic() - ready_since >= settle_seconds:
                return state
        else:
            ready_since = None
        assert time.monotonic() < deadline, f"agent never became ready: {state}"
        time.sleep(2)


def _screen(runtime: HerdrRuntime, session_id: str) -> str:
    return runtime._raw(runtime._command("agent", "read", session_id, "--source", "visible"))


def test_template_has_no_baked_credentials(runtime):
    herdr, transport, _ = runtime
    transport.herdr_binary = "sh"
    listing = herdr._raw(("sh", "-c", "ls -a ~ ~/.claude ~/.codex 2>/dev/null; env | cut -d= -f1"))
    assert "auth.json" not in listing and ".credentials.json" not in listing
    for name in _STRIPPED + ("CODEX_AUTH_JSON",):
        assert re.search(rf"^{name}$", listing, re.MULTILINE) is None


@pytest.mark.parametrize("agent", ["claude", "codex"])
def test_agent_starts_as_a_runtime_session_without_first_run_dialogs(runtime, agent):
    herdr, _, _ = runtime
    session = herdr.start(attempt_id=f"ATTEMPT-LIVE-{agent.upper()}", agent=agent)

    assert isinstance(session, RuntimeSession)
    assert session.agent == agent and session.session_id
    assert session.status is RuntimeStatus.RUNNING
    _wait_ready(herdr, session.session_id)
    pane = _screen(herdr, session.session_id)
    assert pane.strip()
    assert _DIALOGS.search(pane) is None, pane[-2000:]
