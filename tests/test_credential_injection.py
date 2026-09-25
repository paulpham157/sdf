"""Create-time Agent Credential injection (ADR-0007), provider-free.

The live counterpart is tests/test_credential_injection_live.py.
"""

import pytest

from sdf_core.credential_injection import (
    CredentialInjection,
    create_credentialed_transport,
    prepare_credential_injection,
)
from sdf_core.credentials import ConnectionMaterial, CredentialConfigError, CredentialMode
from sdf_core.herdr_runtime import HerdrBindingSnapshot, HerdrRuntime, HerdrRuntimeError
from sdf_core.runtime import CredentialMetadata, RuntimeController, RuntimeEventKind, RuntimeSession
from tests.test_e2b_herdr_transport import ENV, FakeFactory
from tests.test_herdr_runtime import FakeHerdr

CLAUDE_KEY = "sk-ant-dummy-0000000000000000000000000000"
OPENAI_KEY = "sk-openai-dummy-111111111111111111111111"
CODEX_SESSION = '{"auth_mode":"chatgpt","tokens":{"access_token":"dummy-bearer-2222"}}'

API_KEY_ENV = {
    **ENV,
    "SDF_CREDENTIAL_MODE_CLAUDE": "api-key",
    "SDF_ANTHROPIC_API_KEY": CLAUDE_KEY,
    "SDF_CREDENTIAL_MODE_CODEX": "api-key",
    "SDF_OPENAI_API_KEY": OPENAI_KEY,
    # Host shell provider variables that must never be forwarded.
    "ANTHROPIC_API_KEY": "host-proxy-key",
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:8080",
}

SUBSCRIPTION_ENV = {
    **ENV,
    "SDF_CREDENTIAL_MODE_CODEX": "subscription",
    "SDF_CONNECTION_CODEX": "codex-personal",
}

def _reader(agent, connection_id):
    assert (agent, connection_id) == ("codex", "codex-personal")
    return ConnectionMaterial({"CODEX_AUTH_JSON": CODEX_SESSION})

SECRETS = (CLAUDE_KEY, OPENAI_KEY, CODEX_SESSION, "dummy-bearer-2222", "host-proxy-key")

def _assert_no_secret(text):
    for secret in SECRETS:
        assert secret not in text

# --- resolution ---------------------------------------------------------------

def test_plans_resolve_for_every_agent_kind_into_sandbox_wide_envs():
    injection = prepare_credential_injection(("claude", "codex"), API_KEY_ENV)

    assert injection.envs == {"ANTHROPIC_API_KEY": CLAUDE_KEY, "OPENAI_API_KEY": OPENAI_KEY}
    assert injection.metadata["claude"] == CredentialMetadata(CredentialMode.API_KEY.value, None)
    assert injection.metadata["codex"] == CredentialMetadata(CredentialMode.API_KEY.value, None)

def test_host_shell_provider_variables_are_never_forwarded():
    injection = prepare_credential_injection(("claude",), API_KEY_ENV)

    assert injection.envs["ANTHROPIC_API_KEY"] == CLAUDE_KEY
    assert "ANTHROPIC_BASE_URL" not in injection.envs
    assert "ANTHROPIC_BASE_URL" in injection.strip_variables

def test_conflicting_names_are_stripped_from_caller_envs():
    injection = prepare_credential_injection(
        ("codex",),
        SUBSCRIPTION_ENV,
        read_connection=_reader,
        base_envs={"OPENAI_API_KEY": "stale", "OPENAI_BASE_URL": "http://x", "KEEP_ME": "1"},
    )

    assert injection.envs == {"KEEP_ME": "1", "CODEX_AUTH_JSON": CODEX_SESSION}
    assert {"OPENAI_API_KEY", "OPENAI_BASE_URL"} <= set(injection.strip_variables)
    assert injection.metadata["codex"] == CredentialMetadata("subscription", "codex-personal")

def test_duplicate_agent_kinds_resolve_once():
    injection = prepare_credential_injection(("codex", "codex"), SUBSCRIPTION_ENV, read_connection=_reader)

    assert list(injection.metadata) == ["codex"]
    assert len(injection.seed_commands) == 1

def test_injection_repr_never_shows_values():
    injection = prepare_credential_injection(("claude", "codex"), API_KEY_ENV)

    _assert_no_secret(repr(injection))
    _assert_no_secret(str(injection))

# --- seed commands --------------------------------------------------------------

@pytest.mark.parametrize(
    ("agents", "environ", "variable"),
    [
        (("claude",), API_KEY_ENV, "$ANTHROPIC_API_KEY"),
        (("codex",), API_KEY_ENV, "$OPENAI_API_KEY"),
        (("codex",), SUBSCRIPTION_ENV, "$CODEX_AUTH_JSON"),
    ],
)
def test_seed_commands_reference_variable_names_only(agents, environ, variable):
    injection = prepare_credential_injection(agents, environ, read_connection=_reader)

    assert injection.seed_commands
    joined = "\n".join(injection.seed_commands)
    assert variable in joined
    _assert_no_secret(joined)

def test_a_seed_command_carrying_a_value_is_refused():
    injection = prepare_credential_injection(("claude",), API_KEY_ENV)
    with pytest.raises(CredentialConfigError, match="ANTHROPIC_API_KEY") as caught:
        CredentialInjection(
            envs=injection.envs,
            strip_variables=injection.strip_variables,
            seed_commands=(f"echo {CLAUDE_KEY}",),
            metadata=injection.metadata,
        )
    _assert_no_secret(str(caught.value))

# --- creation ordering -----------------------------------------------------------

def test_configuration_error_is_raised_before_any_sandbox_is_created():
    factory = FakeFactory()
    environ = {**ENV, "SDF_CREDENTIAL_MODE_CLAUDE": "api-key"}  # no SDF_ANTHROPIC_API_KEY

    with pytest.raises(CredentialConfigError, match="SDF_ANTHROPIC_API_KEY"):
        create_credentialed_transport(
            template="herdr-claude", agents=("claude",), environ=environ, sandbox_factory=factory
        )

    assert factory.creates == []

def test_one_misconfigured_agent_blocks_creation_for_all():
    factory = FakeFactory()
    environ = {**API_KEY_ENV, "SDF_CREDENTIAL_MODE_CODEX": "subscription"}

    with pytest.raises(CredentialConfigError, match="SDF_CONNECTION_CODEX"):
        create_credentialed_transport(
            template="herdr", agents=("claude", "codex"), environ=environ, sandbox_factory=factory
        )

    assert factory.creates == []

def test_plan_variables_are_passed_as_create_envs_and_seeds_run_once_before_agents():
    factory = FakeFactory()
    transport, injection = create_credentialed_transport(
        template="herdr-codex",
        agents=("claude", "codex"),
        environ=API_KEY_ENV,
        sandbox_factory=factory,
        envs={"ANTHROPIC_BASE_URL": "http://127.0.0.1:8080", "KEEP_ME": "1"},
    )
    runtime = HerdrRuntime(transport=transport, credentials=injection.metadata)

    runtime._raw(runtime._command("--version"))
    runtime._raw(runtime._command("--version"))

    assert len(factory.creates) == 1
    assert factory.creates[0]["envs"] == {
        "KEEP_ME": "1",
        "ANTHROPIC_API_KEY": CLAUDE_KEY,
        "OPENAI_API_KEY": OPENAI_KEY,
    }
    commands = [cmd for cmd, _ in factory.sandbox.runs]
    seeds = list(injection.seed_commands)
    assert commands[: len(seeds)] == seeds
    assert commands[len(seeds) :] == ["herdr --version", "herdr --version"]
    for cmd in commands:
        _assert_no_secret(cmd)

def test_seed_failure_kills_the_sandbox_and_reports_no_value():
    from e2b import CommandExitException

    factory = FakeFactory()
    factory.sandbox._error = CommandExitException(stderr="boom", stdout="", exit_code=1, error="boom")
    transport, _ = create_credentialed_transport(
        template="herdr-claude", agents=("claude",), environ=API_KEY_ENV, sandbox_factory=factory
    )

    with pytest.raises(HerdrRuntimeError, match="seed") as caught:
        transport.run(("herdr", "--version"), 1_000)

    assert factory.sandbox.kills
    assert transport.sandbox_id is None
    _assert_no_secret(str(caught.value))

def test_reconnecting_to_an_existing_sandbox_does_not_reseed():
    factory = FakeFactory()
    connected = FakeFactory()
    transport, _ = create_credentialed_transport(
        template="herdr-claude",
        agents=("claude",),
        environ=API_KEY_ENV,
        sandbox_factory=factory,
        sandbox_connector=lambda sandbox_id, **kw: connected.sandbox,
        sandbox_id="existing",
    )

    transport.run(("herdr", "--version"), 1_000)

    assert factory.creates == []
    assert [cmd for cmd, _ in connected.sandbox.runs] == ["herdr --version"]

# --- Runtime Session metadata ------------------------------------------------------

def test_runtime_session_credential_metadata_is_optional_and_secret_free():
    plain = RuntimeSession("s", "a", "codex", status=None)  # type: ignore[arg-type]
    assert plain.credential is None
    assert CredentialMetadata("subscription", "codex-personal").as_dict() == {
        "credential_mode": "subscription",
        "connection_id": "codex-personal",
    }

def test_every_runtime_session_records_its_agents_credential_metadata():
    injection = prepare_credential_injection(("codex",), SUBSCRIPTION_ENV, read_connection=_reader)
    runtime = HerdrRuntime(runner=FakeHerdr(), credentials=injection.metadata)

    started = runtime.start(attempt_id="ATTEMPT-CRED", agent="codex")
    expected = CredentialMetadata("subscription", "codex-personal")
    assert started.credential == expected
    for session in (
        runtime.send(started.session_id, "hello"),
        runtime.status(started.session_id),
        runtime.reconnect(started.session_id),
        runtime.cancel(started.session_id),
    ):
        assert session.credential == expected
        _assert_no_secret(repr(session))

def test_runtime_refuses_an_agent_kind_without_a_resolved_plan():
    injection = prepare_credential_injection(("codex",), SUBSCRIPTION_ENV, read_connection=_reader)
    runner = FakeHerdr()
    runtime = HerdrRuntime(runner=runner, credentials=injection.metadata)

    with pytest.raises(HerdrRuntimeError, match="no resolved Agent Credential for claude"):
        runtime.start(attempt_id="ATTEMPT-X", agent="claude")
    assert runner.calls == []

def test_runtime_takes_credential_metadata_from_a_credentialed_transport():
    factory = FakeFactory()
    transport, injection = create_credentialed_transport(
        template="herdr-codex", agents=("codex",), environ=SUBSCRIPTION_ENV,
        read_connection=_reader, sandbox_factory=factory,
    )

    runtime = HerdrRuntime(transport=transport)

    assert runtime.credentials == injection.metadata

def test_binding_snapshot_persists_credential_metadata_and_round_trips():
    metadata = CredentialMetadata("subscription", "codex-personal")
    snapshot = HerdrBindingSnapshot("a", "s", "codex", "w", "p", credential=metadata)

    payload = snapshot.as_dict()
    assert payload["credential_mode"] == "subscription"
    assert payload["connection_id"] == "codex-personal"
    assert HerdrBindingSnapshot.from_mapping(payload) == snapshot
    # Snapshots from before credential metadata still load.
    legacy = HerdrBindingSnapshot("a", "s", "codex", "w", "p")
    assert "credential_mode" not in legacy.as_dict()
    assert HerdrBindingSnapshot.from_mapping(legacy.as_dict()).credential is None

def test_restored_binding_keeps_credential_metadata():
    runtime = HerdrRuntime(runner=FakeHerdr())
    metadata = CredentialMetadata("api-key", None)
    restored = runtime.restore_binding(HerdrBindingSnapshot("a", "agent-1", "codex", "w", "p", credential=metadata))

    assert restored.credential == metadata
    assert runtime.reconnect("agent-1").credential == metadata

def test_snapshot_rejects_an_unknown_credential_mode():
    payload = {**HerdrBindingSnapshot("a", "s", "codex", "w", "p").as_dict(), "credential_mode": "sk-leaked"}
    with pytest.raises(ValueError, match="credential_mode") as caught:
        HerdrBindingSnapshot.from_mapping(payload)
    assert "sk-leaked" not in str(caught.value)

def test_started_event_persists_secret_free_credential_metadata():
    injection = prepare_credential_injection(("claude",), API_KEY_ENV)
    controller = RuntimeController(HerdrRuntime(runner=FakeHerdr(), credentials=injection.metadata))

    controller.start(attempt_id="ATTEMPT-EVT", agent="claude")

    started = [event for event in controller.events if event.kind is RuntimeEventKind.STARTED]
    assert started[0].payload == {"credential_mode": "api-key", "connection_id": None}
    _assert_no_secret(repr(controller.events))

def test_started_event_payload_is_unchanged_without_credentials():
    controller = RuntimeController(HerdrRuntime(runner=FakeHerdr()))
    controller.start(attempt_id="ATTEMPT-PLAIN", agent="codex")
    assert controller.events[0].payload == {}


# --- subscription expiry and default connection reader (#14) --------------------


def _expiring(expires_at):
    def reader(agent, connection_id):
        return ConnectionMaterial({"CODEX_AUTH_JSON": CODEX_SESSION}, expires_at=expires_at)

    return reader


def test_expiring_connection_is_refused_before_any_sandbox_is_created():
    from datetime import datetime, timedelta, timezone

    factory = FakeFactory()
    # Valid for 20 minutes: enough for a 5-minute box, not for a 15-minute one.
    reader = _expiring(datetime.now(timezone.utc) + timedelta(minutes=20))

    with pytest.raises(CredentialConfigError, match="e2b-box auth connect codex") as caught:
        create_credentialed_transport(
            template="herdr", agents=("codex",), environ=SUBSCRIPTION_ENV, read_connection=reader,
            sandbox_factory=factory, timeout_seconds=900,
        )
    assert factory.creates == []
    _assert_no_secret(str(caught.value))

    transport, injection = create_credentialed_transport(
        template="herdr", agents=("codex",), environ=SUBSCRIPTION_ENV, read_connection=reader,
        sandbox_factory=factory, timeout_seconds=300,
    )
    assert injection.metadata["codex"] == CredentialMetadata("subscription", "codex-personal")


def test_plugin_bridge_is_the_default_connection_reader(monkeypatch):
    built = []

    class FakeBridge:
        def __init__(self, *, environ):
            built.append(environ)

        def __call__(self, agent, connection_id):
            return _reader(agent, connection_id)

    monkeypatch.setattr("sdf_core.credential_injection.PluginConnectionBridge", FakeBridge)
    injection = prepare_credential_injection(("codex",), SUBSCRIPTION_ENV)

    assert built == [SUBSCRIPTION_ENV]
    assert injection.envs == {"CODEX_AUTH_JSON": CODEX_SESSION}
    assert injection.metadata["codex"] == CredentialMetadata("subscription", "codex-personal")


def test_api_key_mode_never_builds_the_plugin_bridge(monkeypatch):
    def refuse(**_):
        raise AssertionError("bridge must not be built for api-key agents")

    monkeypatch.setattr("sdf_core.credential_injection.PluginConnectionBridge", refuse)
    prepare_credential_injection(("claude", "codex"), API_KEY_ENV)


def test_codex_subscription_seed_writes_the_session_to_codex_auth_json_by_name():
    injection = prepare_credential_injection(("codex",), SUBSCRIPTION_ENV, read_connection=_reader)

    (command,) = injection.seed_commands
    assert '"$CODEX_AUTH_JSON"' in command
    assert "/.codex" in command and "auth.json" in command
    _assert_no_secret(command)


def test_claude_subscription_sets_oauth_token_without_a_seed():
    environ = {**ENV, "SDF_CREDENTIAL_MODE_CLAUDE": "subscription", "SDF_CONNECTION_CLAUDE": "claude-work"}
    injection = prepare_credential_injection(
        ("claude",), environ,
        read_connection=lambda agent, cid: ConnectionMaterial({"CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-dummy"}),
    )
    assert injection.envs == {"CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-dummy"}
    assert "ANTHROPIC_API_KEY" in injection.strip_variables
    assert injection.seed_commands == ()
    assert injection.metadata["claude"] == CredentialMetadata("subscription", "claude-work")
