"""Create-time credential injection against a real E2B sandbox.

Gated on SDF_LIVE_E2B=1 plus E2B_API_KEY.  Every credential is a DUMMY value
generated per run; the box is compared by variable name and SHA-256 digest
only, so no value is ever printed.  Teardown always kills the sandbox.
"""

import hashlib
import os
import secrets

import pytest
from e2b import Sandbox

from sdf_core.credential_injection import create_credentialed_transport
from sdf_core.credentials import CONFLICTING_VARIABLES
from sdf_core.herdr_runtime import HerdrRuntime
from sdf_core.runtime import CredentialMetadata
from tests.test_e2b_herdr_transport_live import TEMPLATE, _assert_gone

LIVE = os.environ.get("SDF_LIVE_E2B") == "1" and bool(os.environ.get("E2B_API_KEY"))

pytestmark = pytest.mark.skipif(not LIVE, reason="live E2B check: set SDF_LIVE_E2B=1 and E2B_API_KEY to run")

def _digest(value: str) -> str:
    return hashlib.sha256(value.encode()).hexdigest()

# Prints NAME=<sha256 of value> for every provider variable in the box; the
# value itself never leaves the sandbox.
_ENV_DIGESTS = (
    "for n in " + " ".join(sorted({n for names in CONFLICTING_VARIABLES.values() for n in names})) + "; do "
    'eval "set -- \\"\\${$n+x}\\" \\"\\${$n}\\""; '
    '[ "$1" = x ] && printf "%s=%s\\n" "$n" "$(printf %s "$2" | sha256sum | cut -d" " -f1)"; '
    "done; true"
)

def test_dummy_credentials_are_injected_by_name_and_conflicts_are_absent():
    claude_key = "sk-ant-dummy-" + secrets.token_hex(16)
    openai_key = "sk-dummy-" + secrets.token_hex(16)
    environ = {
        "E2B_API_KEY": os.environ["E2B_API_KEY"],
        **({"E2B_DOMAIN": os.environ["E2B_DOMAIN"]} if os.environ.get("E2B_DOMAIN") else {}),
        "SDF_CREDENTIAL_MODE_CLAUDE": "api-key",
        "SDF_ANTHROPIC_API_KEY": claude_key,
        "SDF_CREDENTIAL_MODE_CODEX": "api-key",
        "SDF_OPENAI_API_KEY": openai_key,
    }
    transport, injection = create_credentialed_transport(
        template=TEMPLATE,
        agents=("claude", "codex"),
        environ=environ,
        timeout_seconds=300,
        # Conflicting names a caller might pass; all must be stripped.
        envs={"ANTHROPIC_BASE_URL": "http://127.0.0.1:1", "CODEX_API_KEY": "dummy", "SDF_KEEP": "1"},
    )
    sandbox_id = None
    try:
        runtime = HerdrRuntime(transport=transport)
        transport.herdr_binary = "sh"
        observed = transport.run(("sh", "-c", _ENV_DIGESTS), 60_000)
        sandbox_id = transport.sandbox_id
        seeded = transport.run(
            ("sh", "-c", 'test -s "$HOME/.codex/auth.json" && test -s "$HOME/.claude.json" && echo seeded'),
            30_000,
        )

        digests = dict(line.split("=", 1) for line in observed.split())
        assert digests == {
            "ANTHROPIC_API_KEY": _digest(claude_key),
            "OPENAI_API_KEY": _digest(openai_key),
        }
        assert seeded.strip() == "seeded"
        assert runtime.credentials == {
            "claude": CredentialMetadata("api-key", None),
            "codex": CredentialMetadata("api-key", None),
        }
        assert "SDF_KEEP" in injection.envs
    finally:
        try:
            transport.close()
        finally:
            if sandbox_id is not None:
                try:
                    Sandbox.kill(sandbox_id, api_key=os.environ["E2B_API_KEY"])
                except Exception:  # noqa: BLE001 - best-effort teardown
                    pass
    _assert_gone(sandbox_id)
