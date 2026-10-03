"""Unit tests for public run-adapter selection (no live keys required)."""

from __future__ import annotations

import pytest
from sqlalchemy.orm import sessionmaker

from sdf_core.adapter import FakeNativeAdapter
from sdf_core.db import Base, make_engine
from sdf_core.run_adapter import (
    RunAdapterConfigError,
    _ClosingHerdrRunAdapter,
    configured_run_adapter,
    resolve_sandbox_provider,
)


def _db():
    engine = make_engine()
    Base.metadata.create_all(engine)
    return sessionmaker(engine, expire_on_commit=False)()


def test_configured_run_adapter_defaults_to_fake():
    adapter = configured_run_adapter(_db(), environ={})
    assert isinstance(adapter, FakeNativeAdapter)


def test_configured_run_adapter_fake_explicit():
    adapter = configured_run_adapter(_db(), environ={"SDF_RUN_ADAPTER": "fake"})
    assert isinstance(adapter, FakeNativeAdapter)


def test_configured_run_adapter_rejects_unknown_mode():
    with pytest.raises(RunAdapterConfigError, match="SDF_RUN_ADAPTER"):
        configured_run_adapter(_db(), environ={"SDF_RUN_ADAPTER": "acp"})


def test_resolve_sandbox_provider_defaults_to_e2b():
    assert resolve_sandbox_provider({}) == "e2b"
    assert resolve_sandbox_provider({"SDF_SANDBOX_PROVIDER": ""}) == "e2b"
    assert resolve_sandbox_provider({"SDF_SANDBOX_PROVIDER": "E2B"}) == "e2b"


def test_resolve_sandbox_provider_rejects_daytona_without_colliding():
    with pytest.raises(RunAdapterConfigError, match="issue #44"):
        resolve_sandbox_provider({"SDF_SANDBOX_PROVIDER": "daytona"})


def test_resolve_sandbox_provider_rejects_unknown():
    with pytest.raises(RunAdapterConfigError, match="SDF_SANDBOX_PROVIDER"):
        resolve_sandbox_provider({"SDF_SANDBOX_PROVIDER": "fly"})


def test_herdr_mode_builds_closing_adapter_without_creating_sandbox():
    adapter = configured_run_adapter(
        _db(),
        environ={"SDF_RUN_ADAPTER": "herdr", "SDF_RUN_AGENT": "codex"},
    )
    assert isinstance(adapter, _ClosingHerdrRunAdapter)
    assert adapter._agent == "codex"
    assert adapter._template == "sdf-herdr-agents"


def test_herdr_mode_fails_closed_without_e2b_key(tmp_path):
    adapter = configured_run_adapter(
        _db(),
        environ={
            "SDF_RUN_ADAPTER": "live",
            "SDF_CREDENTIAL_MODE_CODEX": "subscription",
            "SDF_CONNECTION_CODEX": "codex-personal",
        },
    )
    workspace = tmp_path / "ws"
    workspace.mkdir()
    with pytest.raises(RunAdapterConfigError, match="E2B_API_KEY"):
        adapter.run(attempt_id="ATTEMPT-X", workspace=workspace, instructions="noop")
