"""ADR 0034: each agent kind has one LLM transport, and Vultr is only an example endpoint."""

from __future__ import annotations

from pathlib import Path

import pytest
from sqlalchemy import select
from sqlalchemy.orm import sessionmaker

from sdf_core.agent_sandbox import DEFAULT_NETWORK, AgentSpec, E2BAgentSandbox
from sdf_core.credentials import (
    AGENT_KINDS,
    LLM_PROTOCOLS,
    CredentialConfigError,
    llm_attempt_provider,
    protocol_of,
    responses_api_required,
    resolve_credential_plan,
)
from sdf_core.db import AttemptRow, Base, make_engine
from sdf_core.run_adapter import configured_run_adapter, run_environ, run_network
from tests.agent_comm_support import fake_factory, lead_behavior, make_fixture, worker_behavior
from tests.test_agent_comm import FAST, _approved
from tests.test_run_adapter import _Completing, _Transport, _db


@pytest.fixture()
def db():
    engine = make_engine("sqlite+pysqlite:///:memory:")
    Base.metadata.create_all(engine)
    session = sessionmaker(engine, expire_on_commit=False)()
    try:
        yield session
    finally:
        session.close()
        engine.dispose()


@pytest.fixture()
def fixture(tmp_path: Path) -> Path:
    return make_fixture(tmp_path)


def test_each_kind_maps_to_exactly_one_protocol_family():
    families = {kind: protocol_of(kind) for kind in AGENT_KINDS}
    assert set(families) == set(AGENT_KINDS)
    assert set(families.values()) <= set(LLM_PROTOCOLS)
    assert set(families.values()) == set(LLM_PROTOCOLS)
    assert families["claude"] == "anthropic"
    assert all(families[kind] == "openai" for kind in AGENT_KINDS if kind != "claude")
    # Codex is OpenAI-compatible and specifically the Responses API.
    assert responses_api_required("codex") is True
    assert all(responses_api_required(kind) is False for kind in AGENT_KINDS if kind != "codex")
    assert "vultr" not in families.values()
    with pytest.raises(CredentialConfigError, match="unknown agent kind"):
        protocol_of("vultr")


def test_non_vultr_base_url_and_arbitrary_key_name():
    env = {
        "SDF_CREDENTIAL_MODE_QWEN": "api-key",
        "SDF_QWEN_API_KEY_ENV": "GATEWAY_API_KEY",
        "GATEWAY_API_KEY": "gateway-unit-test-value",
        "SDF_QWEN_BASE_URL": "https://llm.example.com/v1",
        "SDF_QWEN_MODEL": "org-model",
    }
    plan = resolve_credential_plan("qwen", env)
    assert plan.set_variables["SDF_QWEN_BASE_URL"] == "https://llm.example.com/v1"
    assert plan.set_variables["GATEWAY_API_KEY"] == "gateway-unit-test-value"
    assert "VULTR_INFERENCE_API_KEY" not in plan.set_variables
    assert llm_attempt_provider("qwen", env) == "openai:llm.example.com"

    run_env = run_environ("codex", {
        "SDF_CODEX_API_KEY_ENV": "GATEWAY_API_KEY",
        "GATEWAY_API_KEY": "gateway-unit-test-value",
        "SDF_OPENAI_BASE_URL": "https://llm.example.com/v1",
        "SDF_CODEX_MODEL": "org-model",
    })
    assert run_env["SDF_CODEX_API_KEY_ENV"] == "GATEWAY_API_KEY"
    assert run_env["SDF_OPENAI_BASE_URL"] == "https://llm.example.com/v1"
    assert run_env["SDF_CODEX_MODEL"] == "org-model"
    net = run_network(run_env, agent="codex")
    assert net["allow_out"] == ["llm.example.com"]
    assert "api.vultrinference.com" not in net["allow_out"]
    assert net["deny_out"] == ["0.0.0.0/0"]


def test_example_defaults_remain_when_nothing_is_configured():
    """Do not fail closed: an unset OpenAI-family URL still has the example endpoint."""

    env = run_environ("pi", {"SDF_PI_API_KEY": "pi-unit-test-value"})
    assert env["SDF_PI_BASE_URL"] == "https://api.vultrinference.com/v1"
    assert env.get("SDF_PI_API_KEY_ENV") is None
    assert run_network(env, agent="pi")["allow_out"] == list(DEFAULT_NETWORK["allow_out"])
    # The example endpoint does not depend on the key variable being named VULTR_*.
    named = run_environ("codex", {"SDF_CODEX_API_KEY_ENV": "GATEWAY_API_KEY", "GATEWAY_API_KEY": "k"})
    assert named["SDF_CODEX_API_KEY_ENV"] == "GATEWAY_API_KEY"
    assert named["SDF_OPENAI_BASE_URL"] == "https://api.vultrinference.com/v1"
    assert named["SDF_CODEX_MODEL"] == "glm-5.3"
    assert run_network(named, agent="codex")["allow_out"] == ["api.vultrinference.com"]
    # Unset Codex outside /run is the official Responses host, not a vendor name.
    assert llm_attempt_provider("codex", {}) == "openai:api.openai.com"
    assert llm_attempt_provider("claude", {}) == "anthropic:api.anthropic.com"
    assert llm_attempt_provider("pi", {}) == "openai:api.vultrinference.com"
    assert llm_attempt_provider("pi", {}) != "vultr"


def test_subscription_mode_does_not_get_the_example_base_url():
    env = run_environ("codex", {
        "SDF_CREDENTIAL_MODE_CODEX": "subscription",
        "SDF_CONNECTION_CODEX": "codex-personal",
    })
    assert "SDF_OPENAI_BASE_URL" not in env
    assert "SDF_CODEX_MODEL" not in env
    assert run_network(env, agent="codex")["allow_out"] == ["api.openai.com"]


def test_sandbox_egress_follows_configured_host():
    box = E2BAgentSandbox(
        AgentSpec("worker-1", "worker", "pi"),
        environ={"SDF_PI_BASE_URL": "https://llm.example.com/v1", "SDF_PI_API_KEY": "pi-unit-test-value"},
    )
    assert box._network["allow_out"] == ["llm.example.com"]
    assert box.credential_environ["SDF_PI_BASE_URL"] == "https://llm.example.com/v1"
    assert "pi-unit-test-value" not in str(box.credential_environ)
    default = E2BAgentSandbox(AgentSpec("worker-2", "worker", "goose"), environ={})
    assert default._network["allow_out"] == ["api.vultrinference.com"]


def test_run_attempt_provider_follows_configured_host(monkeypatch, tmp_path: Path):
    import sdf_core.run_adapter as ra

    monkeypatch.setattr(ra.time, "sleep", lambda _s: None)
    db = _db()
    adapter = configured_run_adapter(db, environ={
        "SDF_RUN_ADAPTER": "live",
        "E2B_API_KEY": "e2b_unit_test_not_a_key",
        "SDF_RUN_AGENT": "qwen",
        "SDF_QWEN_API_KEY_ENV": "GATEWAY_API_KEY",
        "GATEWAY_API_KEY": "gateway-unit-test-value",
        "SDF_QWEN_BASE_URL": "https://llm.example.com/v1",
        "SDF_QWEN_MODEL": "org-model",
    })
    assert adapter.network["allow_out"] == ["llm.example.com"]
    adapter._transport_factory = lambda **_kw: (_Transport(), None)
    adapter._kill_by_id = lambda _sid: None
    adapter._runtime = lambda _t: _Completing([
        {"input_tokens": 0, "output_tokens": 0},
        {"input_tokens": 3, "output_tokens": 1},
    ])
    adapter.run(attempt_id="A", workspace=tmp_path, instructions="hi")
    provider = db.get(AttemptRow, "A").provider
    assert provider == "e2b:openai:llm.example.com"
    assert provider != "vultr"
    assert "vultr" not in provider


def test_coordinator_attempt_provider_follows_configured_host(db, fixture, tmp_path: Path):
    from sdf_core.coordinator import Coordinator

    plan = _approved(db, fixture, "PLAN-LLMHOST")
    running: set[str] = set()
    created: list = []
    base = fake_factory(tmp_path / "boxes", running, {"lead": lead_behavior, "worker": worker_behavior()}, created)
    url = "https://llm.example.com/v1"

    def factory(spec):
        sandbox = base(spec)
        sandbox.credential_environ = {"SDF_PI_BASE_URL": url, "SDF_OPENCODE_BASE_URL": url}
        return sandbox

    coordinator = Coordinator(
        db, factory=factory, workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art",
        cleanup_check=lambda ids: ids & running,
    )
    run = coordinator.execute(coordinator.create_run(plan.id, FAST).id)
    assert run.status == "succeeded", run.error
    attempts = db.scalars(select(AttemptRow)).all()
    assert attempts
    assert {attempt.provider for attempt in attempts} == {"openai:llm.example.com"}
