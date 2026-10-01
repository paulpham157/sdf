"""Live-gated Daytona Herdr transport smoke.

Gated on ``SDF_LIVE_DAYTONA=1`` plus ``DAYTONA_API_KEY`` (process env and/or
repo-root ``.env`` loaded into a private mapping, never dumped into
``os.environ`` by this module). Secrets are never printed. Teardown always
deletes the sandbox.
"""

from __future__ import annotations

import os
import time

import pytest

from sdf_core.credentials import load_dotenv
from sdf_core.daytona_herdr_transport import DaytonaHerdrTransport
from sdf_core.herdr_runtime import HerdrRuntimeError

ENV = dict(os.environ)
if os.environ.get("SDF_DOTENV"):
    load_dotenv(os.environ["SDF_DOTENV"], ENV)
load_dotenv(environ=ENV)

LIVE = os.environ.get("SDF_LIVE_DAYTONA") == "1" and bool(ENV.get("DAYTONA_API_KEY"))
SNAPSHOT = ENV.get("SDF_DAYTONA_SNAPSHOT") or os.environ.get("SDF_DAYTONA_SNAPSHOT", "daytona-small")

pytestmark = pytest.mark.skipif(
    not LIVE,
    reason="live Daytona check: set SDF_LIVE_DAYTONA=1 and DAYTONA_API_KEY (env or .env) to run",
)


def _assert_gone(sandbox_id: str, within_seconds: float = 30) -> None:
    """Best-effort: get() should fail once the sandbox is destroyed."""

    from daytona import Daytona, DaytonaConfig

    client = Daytona(DaytonaConfig(api_key=ENV["DAYTONA_API_KEY"]))
    deadline = time.monotonic() + within_seconds
    while time.monotonic() < deadline:
        try:
            client.get(sandbox_id)
        except Exception:  # noqa: BLE001 - absence is the success signal
            return
        time.sleep(1)
    raise AssertionError("sandbox still retrievable after delete")


@pytest.fixture
def owned():
    """Yield a transport plus the ids it created; delete every one of them."""

    created: list[str] = []
    transport = DaytonaHerdrTransport(snapshot=SNAPSHOT, environ=ENV)
    try:
        yield transport, created
    finally:
        try:
            transport.close()
        finally:
            if created and ENV.get("DAYTONA_API_KEY"):
                try:
                    from daytona import Daytona, DaytonaConfig

                    client = Daytona(DaytonaConfig(api_key=ENV["DAYTONA_API_KEY"]))
                    for sandbox_id in created:
                        try:
                            client.delete(client.get(sandbox_id))
                        except Exception:  # noqa: BLE001 - best-effort teardown
                            pass
                except Exception:  # noqa: BLE001 - best-effort teardown
                    pass


def test_create_command_close(owned):
    transport, created = owned
    # Default snapshots may not ship herdr; exercise process.exec with a shell.
    transport.herdr_binary = "sh"
    out = transport.run(("sh", "-c", "echo sdf-daytona-ok"), 30_000)
    created.append(transport.sandbox_id)
    assert "sdf-daytona-ok" in out
    assert transport.sandbox_id == created[0]
    transport.close()
    transport.close()
    assert transport.sandbox_id is None
    _assert_gone(created[0])


def test_host_timeout_deletes_the_sandbox(owned):
    transport, created = owned
    transport.herdr_binary = "sh"
    transport.run(("sh", "-c", "true"), 30_000)
    created.append(transport.sandbox_id)

    with pytest.raises(HerdrRuntimeError, match="timed out"):
        transport.run(("sh", "-c", "sleep 60"), 3_000)

    assert transport.sandbox_id is None
    _assert_gone(created[0])


def test_workspace_stages_and_collects_round_trip(owned, tmp_path):
    transport, created = owned
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    fixture.joinpath("hello.txt").write_text("local-bytes\n", encoding="utf-8")
    nested = fixture / "pkg"
    nested.mkdir()
    nested.joinpath("mod.py").write_bytes(b"\x00binary\xff")

    # Touch the sandbox first so created[] records the id even if stage fails.
    transport.herdr_binary = "sh"
    transport.run(("sh", "-c", "true"), 30_000)
    created.append(transport.sandbox_id)

    remote = transport.stage_workspace("ATTEMPT-DAYTONA", fixture)
    assert remote == "/tmp/sdf/ATTEMPT-DAYTONA"

    fixture.joinpath("hello.txt").write_text("stale-local\n", encoding="utf-8")
    transport.collect_workspace("ATTEMPT-DAYTONA", fixture)
    assert fixture.joinpath("hello.txt").read_text(encoding="utf-8") == "local-bytes\n"
    assert fixture.joinpath("pkg", "mod.py").read_bytes() == b"\x00binary\xff"

    transport.close()
    _assert_gone(created[0])


def test_create_time_envs_are_visible_inside_the_sandbox(owned):
    """Prove ADR-0007 create-time env injection without requiring a Herdr/node image."""

    transport, created = owned
    secret = "sdf-daytona-env-proof-not-a-provider-key"
    # Rebuild with explicit envs (same create path credentialed transport uses).
    transport.close()
    transport = DaytonaHerdrTransport(
        snapshot=SNAPSHOT,
        environ=ENV,
        envs={"SDF_PROOF_TOKEN": secret},
    )
    try:
        transport.herdr_binary = "sh"
        out = transport.run(
            (
                "sh",
                "-c",
                'if [ -n "$SDF_PROOF_TOKEN" ]; then echo env-present; else echo env-missing; fi',
            ),
            30_000,
        )
        created.append(transport.sandbox_id)
        assert "env-present" in out
        assert secret not in out
    finally:
        transport.close()
        if created:
            _assert_gone(created[-1])