"""Persistent Herdr transport against a real E2B sandbox.

Gated on SDF_LIVE_E2B=1 plus E2B_API_KEY; teardown always kills the sandbox.
"""

import os
import time

import pytest
from e2b import Sandbox

from sdf_core.e2b_herdr_transport import E2BHerdrTransport
from sdf_core.herdr_runtime import HerdrRuntime, HerdrRuntimeError

LIVE = os.environ.get("SDF_LIVE_E2B") == "1" and bool(os.environ.get("E2B_API_KEY"))
TEMPLATE = os.environ.get("SDF_E2B_HERDR_TEMPLATE", "sdf-herdr-codex")

pytestmark = pytest.mark.skipif(not LIVE, reason="live E2B check: set SDF_LIVE_E2B=1 and E2B_API_KEY to run")

def _running_ids() -> set[str]:
    paginator = Sandbox.list(api_key=os.environ["E2B_API_KEY"])
    ids: set[str] = set()
    while paginator.has_next:
        ids.update(item.sandbox_id for item in paginator.next_items())
    return ids

def _assert_gone(sandbox_id: str, within_seconds: float = 20) -> None:
    deadline = time.monotonic() + within_seconds
    while sandbox_id in _running_ids():
        assert time.monotonic() < deadline, "sandbox still listed after kill"
        time.sleep(1)

@pytest.fixture
def owned():
    """Yield a transport plus the ids it created; kill every one of them."""

    created: list[str] = []
    transport = E2BHerdrTransport(template=TEMPLATE)
    try:
        yield transport, created
    finally:
        try:
            transport.close()
        finally:
            for sandbox_id in created:
                try:
                    Sandbox.kill(sandbox_id, api_key=os.environ["E2B_API_KEY"])
                except Exception:  # noqa: BLE001 - best-effort teardown
                    pass

def test_create_two_commands_close(owned):
    transport, created = owned
    runtime = HerdrRuntime(transport=transport)

    first = runtime._raw(runtime._command("--version"))
    created.append(transport.sandbox_id)
    second = runtime._raw(runtime._command("--version"))

    assert first.strip() and first == second
    assert transport.sandbox_id == created[0]
    transport.close()
    transport.close()
    assert transport.sandbox_id is None
    _assert_gone(created[0])

def test_host_timeout_kills_the_sandbox(owned):
    transport, created = owned
    transport.herdr_binary = "sh"
    transport.run(("sh", "-c", "true"), 30_000)
    created.append(transport.sandbox_id)

    with pytest.raises(HerdrRuntimeError, match="timed out"):
        transport.run(("sh", "-c", "sleep 60"), 3_000)

    assert transport.sandbox_id is None
    _assert_gone(created[0])
