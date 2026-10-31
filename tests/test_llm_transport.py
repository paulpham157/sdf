"""ADR 0034: each agent kind has one LLM transport. The host is an endpoint, not a protocol."""

from __future__ import annotations

from pathlib import Path

import pytest
from sqlalchemy import select
from sqlalchemy.orm import sessionmaker

from sdf_core.agent_sandbox import AgentSpec, E2BAgentSandbox
from sdf_core import usage
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


def test_allow_listed_names_select_the_host():
    env = {
        "SDF_CREDENTIAL_MODE_QWEN": "api-key",
        "QWEN_API_KEY": "qwen-unit-test-value",
        "QWEN_BASE_URL": "https://llm.example.com/v1",
        "QWEN_MODEL": "org-model",
        "VULTR_INFERENCE_API_KEY": "not-a-credential",
        "GATEWAY_API_KEY": "not-a-credential",
    }
    plan = resolve_credential_plan("qwen", env)
    assert plan.set_variables["QWEN_BASE_URL"] == "https://llm.example.com/v1"
    assert plan.set_variables["QWEN_API_KEY"] == "qwen-unit-test-value"
    assert "VULTR_INFERENCE_API_KEY" not in plan.set_variables
    assert "GATEWAY_API_KEY" not in plan.set_variables
    assert llm_attempt_provider("qwen", env) == "openai:llm.example.com"

    run_env = run_environ("codex", {
        "OPENAI_API_KEY": "openai-unit-test-value",
        "OPENAI_BASE_URL": "https://llm.example.com/v1",
        "OPENAI_MODEL": "org-model",
        "VULTR_INFERENCE_API_KEY": "not-a-credential",
    })
    assert run_env["OPENAI_BASE_URL"] == "https://llm.example.com/v1"
    assert run_env["OPENAI_MODEL"] == "org-model"
    assert "VULTR_INFERENCE_API_KEY" not in run_env
    net = run_network(run_env, agent="codex")
    assert net["allow_out"] == ["llm.example.com"]
    assert net["deny_out"] == ["0.0.0.0/0"]


def test_official_defaults_when_claude_and_codex_urls_are_unset():
    pi = run_environ("pi", {"PI_API_KEY": "pi-unit-test-value"})
    assert "PI_BASE_URL" not in pi
    assert run_network(pi, agent="pi")["allow_out"] == []
    with pytest.raises(CredentialConfigError, match="requires PI_BASE_URL"):
        resolve_credential_plan("pi", pi)

    codex = run_environ("codex", {"OPENAI_API_KEY": "openai-unit-test-value"})
    assert "OPENAI_BASE_URL" not in codex
    assert run_network(codex, agent="codex")["allow_out"] == ["api.openai.com"]
    assert llm_attempt_provider("codex", codex) == "openai:api.openai.com"
    assert llm_attempt_provider("claude", {}) == "anthropic:api.anthropic.com"
    assert llm_attempt_provider("pi", {}) == "openai:unspecified"

    claude = run_environ("claude", {"ANTHROPIC_API_KEY": "anthropic-unit-test-value"})
    assert "ANTHROPIC_BASE_URL" not in claude
    assert "VULTR_INFERENCE_API_KEY" not in claude
    plan = resolve_credential_plan("claude", claude)
    assert set(plan.set_variables) == {"ANTHROPIC_API_KEY"}
    assert run_network(claude, agent="claude")["allow_out"] == ["api.anthropic.com"]


def test_subscription_mode_does_not_inject_a_base_url():
    env = run_environ("codex", {
        "SDF_CREDENTIAL_MODE_CODEX": "subscription",
        "SDF_CONNECTION_CODEX": "codex-personal",
    })
    assert "OPENAI_BASE_URL" not in env
    assert "OPENAI_MODEL" not in env
    assert run_network(env, agent="codex")["allow_out"] == ["api.openai.com"]


def test_pi_seed_does_not_make_the_example_price_look_reported(db):
    """ADR 0027: an SDF price table is an estimate. A cost the agent reported stays reported."""

    estimated = usage.record_turn(
        db, attempt_id="A-PRICE", agent="pi", turn=1, session_id="s", sandbox_id="sb",
        sandbox_provider="e2b", status="completed", error_class=None,
        usage=usage.TurnUsage(input_tokens=1_000_000, output_tokens=1_000_000, cost_usd=None, model="glm-5.3"),
    )
    assert estimated.source == "reported" and estimated.cost_source == "estimated"
    assert estimated.cost_usd == pytest.approx(0.75 + 3.0)
    overridden = usage.record_turn(
        db, attempt_id="A-PRICE", agent="pi", turn=2, session_id="s", sandbox_id="sb",
        sandbox_provider="e2b", status="completed", error_class=None,
        environ={"SDF_PRICE_GLM_5_3": "1,2"},
        usage=usage.TurnUsage(input_tokens=1_000_000, output_tokens=1_000_000, cost_usd=0, model="glm-5.3"),
    )
    assert overridden.cost_source == "estimated" and overridden.cost_usd == pytest.approx(1 + 2)
    reported = usage.record_turn(
        db, attempt_id="A-PRICE", agent="pi", turn=3, session_id="s", sandbox_id="sb",
        sandbox_provider="e2b", status="completed", error_class=None,
        environ={"SDF_PRICE_GLM_5_3": "9,9"},
        usage=usage.TurnUsage(input_tokens=10, output_tokens=10, cost_usd=0.42, model="glm-5.3"),
    )
    assert reported.cost_source == "reported" and reported.cost_usd == pytest.approx(0.42)


def test_begin_attempt_raises_when_credential_environ_is_missing(db, tmp_path: Path):
    """The production call reads the attribute. A sandbox that omits it raises on 3.11+."""

    from sdf_core.coordinator import Coordinator, _Subtask

    coordinator = Coordinator(
        db, factory=lambda _spec: None, workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art",
    )
    sub = _Subtask(key="add", spec={}, task_id="T-MISSING", worker="worker-1")

    class _Missing:
        spec = AgentSpec("worker-1", "worker", "pi")

    with pytest.raises(AttributeError):
        coordinator._begin_attempt(
            "RUN-MISSING", sub, tmp_path, _Missing.spec, None,
            evidence_mode="synthetic", sandbox=_Missing(),  # type: ignore[arg-type]
        )
    assert sub.attempt_id is None
    assert db.scalars(select(AttemptRow)).all() == []


def test_sandbox_egress_follows_configured_host():
    box = E2BAgentSandbox(
        AgentSpec("worker-1", "worker", "pi"),
        environ={"PI_BASE_URL": "https://llm.example.com/v1", "PI_API_KEY": "pi-unit-test-value"},
    )
    assert box._network["allow_out"] == ["llm.example.com"]
    assert box.credential_environ["PI_BASE_URL"] == "https://llm.example.com/v1"
    assert "pi-unit-test-value" not in str(box.credential_environ)
    default = E2BAgentSandbox(AgentSpec("worker-2", "worker", "goose"), environ={})
    assert default._network["allow_out"] == []


def test_run_attempt_provider_follows_configured_host(monkeypatch, tmp_path: Path):
    import sdf_core.run_adapter as ra

    monkeypatch.setattr(ra.time, "sleep", lambda _s: None)
    db = _db()
    adapter = configured_run_adapter(db, environ={
        "SDF_RUN_ADAPTER": "live",
        "E2B_API_KEY": "e2b_unit_test_not_a_key",
        "SDF_RUN_AGENT": "qwen",
        "QWEN_API_KEY": "qwen-unit-test-value",
        "QWEN_BASE_URL": "https://llm.example.com/v1",
        "QWEN_MODEL": "org-model",
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
        sandbox.credential_environ = {"PI_BASE_URL": url, "OPENCODE_BASE_URL": url}
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
