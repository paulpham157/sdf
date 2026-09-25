"""Claude Code in ``api-key`` mode against a real E2B sandbox (ticket 07).

Gated on SDF_LIVE_E2B=1 plus E2B_API_KEY.  The wiring test uses a DUMMY key
generated per run; the Attempt test is gated additionally on
SDF_ANTHROPIC_API_KEY (read from the environment or, via SDF_DOTENV, from the
operator's git-ignored ``.env``).  Keys are compared by SHA-256 only and the
scrollback of every Herdr pane is checked programmatically for the key and
its approved tail, so no value is printed.  Teardown always kills the sandbox.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import secrets
import time
from pathlib import Path

import pytest
from e2b import Sandbox
from sqlalchemy.orm import sessionmaker

from sdf_core.credential_injection import create_credentialed_transport
from sdf_core.credentials import load_dotenv
from sdf_core.db import Base, EvidenceRow, RuntimeEventRow, make_engine
from sdf_core.fixture_loop import AgentFixtureLoop
from sdf_core.herdr_runtime import HerdrRuntime
from sdf_core.model import utcnow
from sdf_core.runtime import CredentialMetadata, RuntimeController, SqlAlchemyRuntimeEventSink
from tests.test_e2b_agents_template_live import _DIALOGS
from tests.test_e2b_herdr_transport_live import _assert_gone

TEMPLATE = os.environ.get("SDF_E2B_AGENTS_TEMPLATE", "sdf-herdr-agents")

# The operator's .env may live outside this checkout (a worktree); only names load.
_OPERATOR_ENV: dict[str, str] = {}
if os.environ.get("SDF_DOTENV"):
    load_dotenv(os.environ["SDF_DOTENV"], _OPERATOR_ENV)
load_dotenv(environ=_OPERATOR_ENV)

def _setting(name: str) -> str:
    return os.environ.get(name) or _OPERATOR_ENV.get(name, "")

E2B_KEY = _setting("E2B_API_KEY")
LIVE = os.environ.get("SDF_LIVE_E2B") == "1" and bool(E2B_KEY)
ANTHROPIC_KEY = _setting("SDF_ANTHROPIC_API_KEY")

pytestmark = pytest.mark.skipif(not LIVE, reason="live E2B check: set SDF_LIVE_E2B=1 and E2B_API_KEY to run")

def _digest(value: str) -> str:
    return hashlib.sha256(value.encode()).hexdigest()

def _environ(key: str) -> dict[str, str]:
    """A clean environment: never the host shell's ANTHROPIC_* proxy variables."""

    environ = {"E2B_API_KEY": E2B_KEY, "SDF_CREDENTIAL_MODE_CLAUDE": "api-key", "SDF_ANTHROPIC_API_KEY": key}
    if _setting("E2B_DOMAIN"):
        environ["E2B_DOMAIN"] = _setting("E2B_DOMAIN")
    return environ

class _Box:
    """A credentialed Claude transport whose sandbox is always killed."""

    def __init__(self, key: str):
        self.transport, self.injection = create_credentialed_transport(
            template=TEMPLATE, agents=("claude",), environ=_environ(key), timeout_seconds=900
        )
        self.runtime = HerdrRuntime(transport=self.transport, timeout_ms=180_000)
        self.created: list[str] = []

    def shell(self, script: str, timeout_ms: int = 60_000) -> str:
        binary, self.transport.herdr_binary = self.transport.herdr_binary, "sh"
        try:
            return self.transport.run(("sh", "-c", script), timeout_ms)
        finally:
            self.transport.herdr_binary = binary
            if self.transport.sandbox_id and self.transport.sandbox_id not in self.created:
                self.created.append(self.transport.sandbox_id)

    def herdr(self, *parts: str) -> str:
        output = self.runtime._raw(self.runtime._command(*parts))
        if self.transport.sandbox_id and self.transport.sandbox_id not in self.created:
            self.created.append(self.transport.sandbox_id)
        return output

    def scrollback(self) -> str:
        """Every pane's recent scrollback, for secret-absence checks only."""

        panes = json.loads(self.herdr("pane", "list"))["result"]["panes"]
        return "\n".join(
            self.herdr("pane", "read", pane["pane_id"], "--source", "recent", "--lines", "1000") for pane in panes
        )

    def close(self) -> None:
        if self.transport.sandbox_id and self.transport.sandbox_id not in self.created:
            self.created.append(self.transport.sandbox_id)
        try:
            self.transport.close()
        finally:
            for sandbox_id in self.created:
                try:
                    Sandbox.kill(sandbox_id, api_key=E2B_KEY)
                except Exception:  # noqa: BLE001 - best-effort teardown
                    pass
            for sandbox_id in self.created:
                _assert_gone(sandbox_id)

def _assert_no_secret(text: str, key: str) -> None:
    # Asserted by boolean only, so a failure never renders the value.
    assert (key in text) is False, "the Anthropic key appeared in Herdr pane scrollback"
    assert (key[-20:] in text) is False, "the approved key tail appeared in Herdr pane scrollback"

def _agent(box: _Box, session_id: str) -> dict:
    payload = json.loads(box.herdr("agent", "get", session_id))["result"]
    return payload.get("agent", payload)

def _wait_idle(box: _Box, session_id: str, within: float = 90, settle: float = 8) -> None:
    """Idle + interactive_ready that holds; a late dialog flips it to blocked."""

    deadline, since = time.monotonic() + within, None
    while True:
        state = _agent(box, session_id)
        assert state.get("agent_status") != "blocked", "Claude is blocked by a dialog"
        if state.get("agent_status") == "idle" and state.get("interactive_ready") is True:
            since = since or time.monotonic()
            if time.monotonic() - since >= settle:
                return
        else:
            since = None
        assert time.monotonic() < deadline, f"Claude never became idle: {state.get('agent_status')}"
        time.sleep(2)

# Prints the seed-file shape as names, lengths and digests; never values.
_SEED_SHAPE = r"""
node -e '
const fs=require("fs"),h=process.env.HOME,c=JSON.parse(fs.readFileSync(h+"/.claude.json","utf8"));
const crypto=require("crypto"),d=v=>crypto.createHash("sha256").update(v).digest("hex");
const r=c.customApiKeyResponses||{},f=h+"/.config/sdf/agent-env.sh";
console.log(JSON.stringify({
  onboarded:c.hasCompletedOnboarding===true,
  approved:(r.approved||[]).map(d), rejected:r.rejected||null,
  claudeMode:(fs.statSync(h+"/.claude.json").mode&0o777).toString(8),
  envMode:(fs.statSync(f).mode&0o777).toString(8),
  envNames:fs.readFileSync(f,"utf8").split("\n").filter(Boolean).map(l=>l.split("=")[0]),
  bashrcSources:fs.readFileSync(h+"/.bashrc","utf8").split("\n")[0].includes(".config/sdf/agent-env.sh"),
  trusted:Object.keys(c.projects||{}).filter(p=>c.projects[p].hasTrustDialogAccepted===true),
}))'
"""

def test_dummy_key_wiring_seeds_expected_shape_and_no_secret_reaches_a_pane():
    key = "sk-ant-dummy-" + secrets.token_hex(24)
    box = _Box(key)
    try:
        for command in box.injection.seed_commands:
            _assert_no_secret(command, key)
        attempt = "ATTEMPT-LIVE-CLAUDE-WIRING"
        box.shell(f"mkdir -p /tmp/sdf/{attempt}")
        box.runtime.bind_remote_workspace(attempt, f"/tmp/sdf/{attempt}")
        session = box.runtime.start(attempt_id=attempt, agent="claude")
        _wait_idle(box, session.session_id)

        shape = json.loads(box.shell(_SEED_SHAPE))
        # The pane holds Claude, so its environment is read from /proc, by digest.
        pane_env = box.shell(
            "for p in $(pgrep -x claude); do tr '\\0' '\\n' < /proc/$p/environ | grep -c '^ANTHROPIC_API_KEY='; done"
        )
        pane_digest = box.shell(
            "p=$(pgrep -x claude | head -1); tr '\\0' '\\n' < /proc/$p/environ | sed -n 's/^ANTHROPIC_API_KEY=//p' | tr -d '\\n' | sha256sum | cut -d' ' -f1"
        )
        screen = box.herdr("agent", "read", session.session_id, "--source", "visible")
        scrollback = box.scrollback()

        assert shape["onboarded"] is True
        assert shape["approved"] == [_digest(key[-20:])] and shape["rejected"] == []
        assert shape["claudeMode"] == "600" and shape["envMode"] == "600"
        assert shape["envNames"] == ["export ANTHROPIC_API_KEY"]
        assert shape["bashrcSources"] is True
        assert f"/tmp/sdf/{attempt}" in shape["trusted"]
        assert pane_env.split() and set(pane_env.split()) == {"1"}
        assert pane_digest.strip() == _digest(key)
        assert _DIALOGS.search(screen) is None, "a first-run dialog is showing"
        assert re.search(r"api key|detected a custom", screen, re.IGNORECASE) is None
        assert box.runtime.credentials == {"claude": CredentialMetadata("api-key", None)}
        _assert_no_secret(scrollback, key)
    finally:
        box.close()

CALC_BUG = "def add(a, b):\n    return a - b\n"
PROMPT = (
    "Fix the bug in calc.py in the current directory so add(a, b) returns a + b. "
    "Change nothing else. When done, reply with the single word DONE."
)
CHECK = [["python3", "-c", "from calc import add; assert add(2, 3) == 5"]]

@pytest.mark.skipif(not ANTHROPIC_KEY, reason="live Claude Attempt: set SDF_ANTHROPIC_API_KEY (operator .env via SDF_DOTENV) to run")
def test_live_claude_attempt_on_an_operator_key_answers_and_records_evidence(tmp_path: Path):
    box = _Box(ANTHROPIC_KEY)
    engine = make_engine()
    Base.metadata.create_all(engine)
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    (workspace / "calc.py").write_text(CALC_BUG, encoding="utf-8")
    attempt = "ATTEMPT-LIVE-CLAUDE-API-KEY"
    try:
        with sessionmaker(engine, expire_on_commit=False)() as db:
            controller = RuntimeController(box.runtime, event_sink=SqlAlchemyRuntimeEventSink(db), source="herdr-e2b")
            original_start = box.runtime.start

            def start_and_wait(*, attempt_id: str, agent: str):
                session = original_start(attempt_id=attempt_id, agent=agent)
                _wait_idle(box, session.session_id)  # Claude reaches idle with no dialog
                return session

            box.runtime.start = start_and_wait  # type: ignore[method-assign]
            result = AgentFixtureLoop(controller).run(
                attempt_id=attempt,
                agent="claude",
                workspace=workspace,
                instructions=PROMPT,
                criteria=("add returns the sum",),
                criterion_checks={"add returns the sum": CHECK},
            )
            answered = "\n".join(box.runtime.stream(result.session.session_id))
            scrollback = box.scrollback()
            for evidence in result.evaluation.evidence:
                db.add(EvidenceRow(
                    id=evidence.evidence_id, attempt_id=attempt, kind=evidence.kind, status=evidence.status,
                    command=evidence.command, exit_code=evidence.exit_code, confidence=evidence.confidence,
                    criterion=evidence.criterion, measured_at=utcnow(), evidence_mode="live", created_at=utcnow(),
                ))
            db.commit()
            started = db.query(RuntimeEventRow).filter_by(attempt_id=attempt, kind="runtime_started").one()
            events = db.query(RuntimeEventRow).filter_by(attempt_id=attempt).order_by(RuntimeEventRow.sequence).all()
            evidence_rows = db.query(EvidenceRow).filter_by(attempt_id=attempt).all()

            print(json.dumps({
                "attempt": attempt, "sandbox": box.created[:1], "credential": started.payload,
                "evaluation": result.evaluation.status,
                # Kinds, sources and sequences only: payloads carry pane text.
                "events": [(e.sequence, e.source, e.kind, e.status) for e in events], "evidence": [(row.criterion, row.status) for row in evidence_rows],
            }))
            assert result.session.credential == CredentialMetadata("api-key", None)
            assert started.payload == {"credential_mode": "api-key", "connection_id": None}
            assert [(e.sequence, e.source, e.kind) for e in events] == [
                (1, "herdr-e2b", "runtime_started"),
                (2, "herdr-e2b", "runtime_input_sent"),
                (3, "herdr-e2b", "runtime_output_observed"),
            ]
            assert len({e.session_id for e in events}) == 1
            assert "DONE" in answered, "Claude did not answer the prompt"
            assert re.search(r"not logged in|/login|invalid api key", answered, re.IGNORECASE) is None
            assert _DIALOGS.search(answered) is None
            assert "return a + b" in (workspace / "calc.py").read_text(encoding="utf-8")
            assert result.accepted is True
            assert [(row.criterion, row.status, row.evidence_mode) for row in evidence_rows] == [
                ("add returns the sum", "PASS", "live")
            ]
            _assert_no_secret(scrollback, ANTHROPIC_KEY)
            _assert_no_secret(answered, ANTHROPIC_KEY)
    finally:
        box.close()
