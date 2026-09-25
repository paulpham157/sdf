"""Codex in api-key mode against a real sdf-herdr-agents sandbox (ticket 08).

The wiring test is gated on SDF_LIVE_E2B=1 plus E2B_API_KEY and uses a DUMMY
OpenAI key generated per run: auth.json must be in API-key auth mode, the base
URL must land in config.toml, Codex must reach its prompt with no login
screen, and the key must never appear in pane scrollback.  The live Attempt
additionally needs SDF_OPENAI_API_KEY.  The repo-root .env (or the file named
by SDF_DOTENV) is loaded for E2B_API_KEY; values are never printed, only
names and SHA-256 digests.  Teardown always kills the sandbox.
"""

import hashlib
import json
import os
import re
import secrets
import time

import pytest
from e2b import Sandbox

from sdf_core.credential_injection import create_credentialed_transport
from sdf_core.credentials import load_dotenv
from sdf_core.herdr_runtime import HerdrRuntime
from sdf_core.runtime import CredentialMetadata
from tests.test_e2b_agents_template_live import TEMPLATE, _screen, _wait_ready

# A private copy: loading .env must not leak into other tests' os.environ.
ENV = dict(os.environ)
load_dotenv(ENV.get("SDF_DOTENV") or None, ENV)

LIVE = ENV.get("SDF_LIVE_E2B") == "1" and bool(ENV.get("E2B_API_KEY"))
BASE_URL = "https://gateway.example.com/openai/v1"
_LOGIN = re.compile(r"sign in with chatgpt|provide your own api key|welcome to codex|log ?in", re.IGNORECASE)

pytestmark = pytest.mark.skipif(not LIVE, reason="live E2B check: set SDF_LIVE_E2B=1 and E2B_API_KEY to run")

def _assert_gone(sandbox_id: str, within_seconds: float = 20) -> None:
    deadline = time.monotonic() + within_seconds
    while True:
        paginator = Sandbox.list(api_key=ENV["E2B_API_KEY"], **_domain())
        ids: set[str] = set()
        while paginator.has_next:
            ids.update(item.sandbox_id for item in paginator.next_items())
        if sandbox_id not in ids:
            return
        assert time.monotonic() < deadline, "sandbox still listed after kill"
        time.sleep(1)

def _domain() -> dict[str, str]:
    return {"domain": ENV["E2B_DOMAIN"]} if ENV.get("E2B_DOMAIN") else {}

def _digest(value: str) -> str:
    return hashlib.sha256(value.encode()).hexdigest()

def _environ(**credential: str) -> dict[str, str]:
    # Only E2B and SDF_* names: the host shell's provider variables never pass.
    return {
        "E2B_API_KEY": ENV["E2B_API_KEY"],
        **({"E2B_DOMAIN": ENV["E2B_DOMAIN"]} if ENV.get("E2B_DOMAIN") else {}),
        "SDF_CREDENTIAL_MODE_CODEX": "api-key",
        **credential,
    }

@pytest.fixture
def codex_box():
    """Yield a factory for a credentialed Codex runtime; kill every sandbox it made."""

    made: list = []

    def build(environ: dict[str, str]):
        transport, injection = create_credentialed_transport(
            template=TEMPLATE, agents=("codex",), environ=environ, timeout_seconds=600
        )
        made.append(transport)
        return HerdrRuntime(transport=transport, timeout_ms=120_000), transport, injection

    yield build
    ids = []
    for transport in made:
        if transport.sandbox_id:
            ids.append(transport.sandbox_id)
        try:
            transport.close()
        finally:
            if transport.sandbox_id:
                try:
                    Sandbox.kill(transport.sandbox_id, api_key=ENV["E2B_API_KEY"], **_domain())
                except Exception:  # noqa: BLE001 - best-effort teardown
                    pass
    for sandbox_id in ids:
        _assert_gone(sandbox_id)

def _box(runtime: HerdrRuntime, transport, script: str) -> str:
    binary, transport.herdr_binary = transport.herdr_binary, "sh"
    try:
        return runtime._raw(("sh", "-c", script))
    finally:
        transport.herdr_binary = binary

# The box reports structure and digests only; no credential value leaves it.
_AUTH_SHAPE = (
    'node -e \'const a=JSON.parse(require("fs").readFileSync(process.env.HOME+"/.codex/auth.json","utf8"));'
    'const h=require("crypto").createHash("sha256").update(a.OPENAI_API_KEY||"").digest("hex");'
    'console.log(JSON.stringify({keys:Object.keys(a).sort(),auth_mode:a.auth_mode,key_sha256:h}))\'; '
    'stat -c %a "$HOME/.codex/auth.json"'
)

def test_dummy_key_seeds_api_key_auth_and_codex_starts_without_login(codex_box):
    dummy = "sk-dummy-" + secrets.token_hex(24)
    runtime, transport, injection = codex_box(_environ(SDF_OPENAI_API_KEY=dummy, SDF_OPENAI_BASE_URL=BASE_URL))
    assert dummy not in repr(injection) and all(dummy not in c for c in injection.seed_commands)

    shape, mode = _box(runtime, transport, _AUTH_SHAPE).strip().splitlines()
    assert json.loads(shape) == {"keys": ["OPENAI_API_KEY", "auth_mode"], "auth_mode": "apikey", "key_sha256": _digest(dummy)}
    assert mode == "600"
    config = _box(runtime, transport, 'cat "$HOME/.codex/config.toml"')
    assert f'openai_base_url = "{BASE_URL}"' in config.splitlines()

    session = runtime.start(attempt_id="ATTEMPT-LIVE-CODEX-APIKEY", agent="codex")
    assert session.credential == CredentialMetadata("api-key", None)
    _wait_ready(runtime, session.session_id)
    pane = _screen(runtime, session.session_id)
    scrollback = runtime._raw(
        runtime._command("agent", "read", session.session_id, "--source", "recent-unwrapped", "--lines", "1000")
    )
    assert pane.strip()
    assert _LOGIN.search(pane) is None, f"login screen visible (pane sha256 {_digest(pane)})"
    for text in (pane, scrollback):
        assert dummy not in text and dummy[-20:] not in text, "dummy key leaked into pane scrollback"

@pytest.mark.skipif(
    not ENV.get("SDF_OPENAI_API_KEY"),
    reason="live Codex Attempt needs a real OpenAI key: set SDF_OPENAI_API_KEY to run",
)
def test_live_attempt_with_a_real_openai_key(codex_box):
    key = ENV["SDF_OPENAI_API_KEY"]
    credential = {"SDF_OPENAI_API_KEY": key}
    if ENV.get("SDF_OPENAI_BASE_URL"):
        credential["SDF_OPENAI_BASE_URL"] = ENV["SDF_OPENAI_BASE_URL"]
    runtime, _, _ = codex_box(_environ(**credential))

    session = runtime.start(attempt_id="ATTEMPT-LIVE-CODEX-REAL", agent="codex")
    _wait_ready(runtime, session.session_id)
    runtime.send(session.session_id, "Reply with exactly the word PONG and nothing else.")
    output = "\n".join(runtime.stream(session.session_id))
    assert "PONG" in output
    assert key not in output and key[-20:] not in output
