"""Credential resolution core (ticket 04, ADR-0007).

Pure tests: plain mappings and temp files only, no network, no E2B.
"""

from pathlib import Path

import pytest

from sdf_core.credentials import (
    CONFLICTING_VARIABLES,
    CredentialConfigError,
    CredentialMode,
    ConnectionMaterial,
    load_dotenv,
    resolve_credential_plan,
)

DUMMY_ANTHROPIC = "sk-ant-dummy-0123456789abcdef"
DUMMY_OPENAI = "sk-openai-dummy-fedcba9876543210"
SHELL_SECRETS = {
    "ANTHROPIC_API_KEY": "sk-shell-anthropic-proxy-secret",
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:8317",
    "OPENAI_API_KEY": "sk-shell-openai-proxy-secret",
    "OPENAI_BASE_URL": "http://localhost:8318/v1",
}
ALL_SECRETS = [DUMMY_ANTHROPIC, DUMMY_OPENAI, *SHELL_SECRETS.values()]


def _assert_no_secret(message: str) -> None:
    for secret in ALL_SECRETS:
        assert secret not in message


# --- Missing or unknown mode fails with an error naming the variable ---------


@pytest.mark.parametrize(
    ("agent", "variable"),
    [("claude", "SDF_CREDENTIAL_MODE_CLAUDE"), ("codex", "SDF_CREDENTIAL_MODE_CODEX")],
)
def test_missing_mode_names_the_variable(agent, variable):
    env = {"SDF_ANTHROPIC_API_KEY": DUMMY_ANTHROPIC, "SDF_OPENAI_API_KEY": DUMMY_OPENAI, **SHELL_SECRETS}
    with pytest.raises(CredentialConfigError) as error:
        resolve_credential_plan(agent, env)
    assert variable in str(error.value)
    _assert_no_secret(str(error.value))


@pytest.mark.parametrize("value", ["", "  ", "API-KEY-typo", "oauth", DUMMY_ANTHROPIC])
def test_unknown_mode_names_the_variable_not_the_value(value):
    env = {"SDF_CREDENTIAL_MODE_CLAUDE": value, "SDF_ANTHROPIC_API_KEY": DUMMY_ANTHROPIC}
    with pytest.raises(CredentialConfigError) as error:
        resolve_credential_plan("claude", env)
    message = str(error.value)
    assert "SDF_CREDENTIAL_MODE_CLAUDE" in message
    assert "subscription" in message and "api-key" in message
    _assert_no_secret(message)


def test_mode_of_other_agent_is_not_a_fallback():
    env = {"SDF_CREDENTIAL_MODE_CODEX": "api-key", "SDF_ANTHROPIC_API_KEY": DUMMY_ANTHROPIC}
    with pytest.raises(CredentialConfigError, match="SDF_CREDENTIAL_MODE_CLAUDE"):
        resolve_credential_plan("claude", env)


def test_unknown_agent_kind_is_rejected():
    with pytest.raises(CredentialConfigError, match="agent kind"):
        resolve_credential_plan("gemini", {})


# --- api-key plans ------------------------------------------------------------


def test_claude_api_key_plan_sets_anthropic_key_and_strips_conflicts():
    env = {"SDF_CREDENTIAL_MODE_CLAUDE": "api-key", "SDF_ANTHROPIC_API_KEY": DUMMY_ANTHROPIC}
    plan = resolve_credential_plan("claude", env)
    assert plan.agent == "claude"
    assert plan.mode is CredentialMode.API_KEY
    assert plan.connection_id is None
    assert dict(plan.set_variables) == {"ANTHROPIC_API_KEY": DUMMY_ANTHROPIC}
    assert set(plan.strip_variables) == set(CONFLICTING_VARIABLES["claude"]) - {"ANTHROPIC_API_KEY"}
    assert {"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN"} <= set(plan.strip_variables)


def test_codex_api_key_plan_sets_openai_key_and_strips_conflicts():
    env = {"SDF_CREDENTIAL_MODE_CODEX": "api-key", "SDF_OPENAI_API_KEY": DUMMY_OPENAI}
    plan = resolve_credential_plan("codex", env)
    assert plan.mode is CredentialMode.API_KEY
    assert dict(plan.set_variables) == {"OPENAI_API_KEY": DUMMY_OPENAI}
    assert set(plan.strip_variables) == {"CODEX_API_KEY", "CODEX_AUTH_JSON", "OPENAI_BASE_URL"}


def test_set_and_strip_never_overlap():
    for agent, key_var, key in (("claude", "SDF_ANTHROPIC_API_KEY", DUMMY_ANTHROPIC), ("codex", "SDF_OPENAI_API_KEY", DUMMY_OPENAI)):
        plan = resolve_credential_plan(agent, {f"SDF_CREDENTIAL_MODE_{agent.upper()}": "api-key", key_var: key})
        assert not set(plan.set_variables) & set(plan.strip_variables)


def test_seed_steps_reference_variable_names_only():
    env = {"SDF_CREDENTIAL_MODE_CLAUDE": "api-key", "SDF_ANTHROPIC_API_KEY": DUMMY_ANTHROPIC}
    plan = resolve_credential_plan("claude", env)
    assert plan.seed_steps
    for step in plan.seed_steps:
        assert step.variable in plan.set_variables
        assert DUMMY_ANTHROPIC not in repr(step)


@pytest.mark.parametrize(
    ("agent", "variable"), [("claude", "SDF_ANTHROPIC_API_KEY"), ("codex", "SDF_OPENAI_API_KEY")]
)
def test_missing_api_key_names_the_sdf_variable(agent, variable):
    env = {f"SDF_CREDENTIAL_MODE_{agent.upper()}": "api-key", **SHELL_SECRETS}
    with pytest.raises(CredentialConfigError) as error:
        resolve_credential_plan(agent, env)
    assert variable in str(error.value)
    _assert_no_secret(str(error.value))


def test_blank_api_key_is_missing():
    env = {"SDF_CREDENTIAL_MODE_CODEX": "api-key", "SDF_OPENAI_API_KEY": "   "}
    with pytest.raises(CredentialConfigError, match="SDF_OPENAI_API_KEY"):
        resolve_credential_plan("codex", env)


# --- Shell provider variables never reach a plan ------------------------------


@pytest.mark.parametrize("agent", ["claude", "codex"])
def test_shell_provider_variables_never_reach_a_plan(agent):
    env = {
        "SDF_CREDENTIAL_MODE_CLAUDE": "api-key",
        "SDF_CREDENTIAL_MODE_CODEX": "api-key",
        "SDF_ANTHROPIC_API_KEY": DUMMY_ANTHROPIC,
        "SDF_OPENAI_API_KEY": DUMMY_OPENAI,
        **SHELL_SECRETS,
    }
    plan = resolve_credential_plan(agent, env)
    rendered = repr(plan)
    for value in SHELL_SECRETS.values():
        assert value not in plan.set_variables.values()
        assert value not in rendered
    assert "ANTHROPIC_BASE_URL" not in plan.set_variables
    assert "OPENAI_BASE_URL" not in plan.set_variables


def test_plan_repr_redacts_secret_values():
    env = {"SDF_CREDENTIAL_MODE_CLAUDE": "api-key", "SDF_ANTHROPIC_API_KEY": DUMMY_ANTHROPIC}
    plan = resolve_credential_plan("claude", env)
    assert DUMMY_ANTHROPIC not in repr(plan)
    assert DUMMY_ANTHROPIC not in str(plan)
    assert "ANTHROPIC_API_KEY" in repr(plan)


def test_plan_is_immutable():
    env = {"SDF_CREDENTIAL_MODE_CLAUDE": "api-key", "SDF_ANTHROPIC_API_KEY": DUMMY_ANTHROPIC}
    plan = resolve_credential_plan("claude", env)
    with pytest.raises(TypeError):
        plan.set_variables["ANTHROPIC_API_KEY"] = "other"  # type: ignore[index]


# --- subscription mode: connection id, no silent fallback ---------------------


def test_subscription_requires_connection_name():
    env = {"SDF_CREDENTIAL_MODE_CODEX": "subscription", "SDF_OPENAI_API_KEY": DUMMY_OPENAI}
    with pytest.raises(CredentialConfigError, match="SDF_CONNECTION_CODEX") as error:
        resolve_credential_plan("codex", env, read_connection=lambda agent, cid: pytest.fail("not reached"))
    _assert_no_secret(str(error.value))


def test_subscription_never_falls_back_to_api_key():
    env = {
        "SDF_CREDENTIAL_MODE_CLAUDE": "subscription",
        "SDF_CONNECTION_CLAUDE": "claude-work",
        "SDF_ANTHROPIC_API_KEY": DUMMY_ANTHROPIC,
    }
    with pytest.raises(CredentialConfigError, match="claude-work") as error:
        resolve_credential_plan("claude", env)  # no connection reader available
    _assert_no_secret(str(error.value))


def test_subscription_plan_uses_connection_material_only():
    token = "oauth-dummy-token-abcdef"
    seen = []

    def reader(agent, connection_id):
        seen.append((agent, connection_id))
        return ConnectionMaterial(variables={"CLAUDE_CODE_OAUTH_TOKEN": token})

    env = {
        "SDF_CREDENTIAL_MODE_CLAUDE": "subscription",
        "SDF_CONNECTION_CLAUDE": "claude-work",
        "SDF_ANTHROPIC_API_KEY": DUMMY_ANTHROPIC,
        **SHELL_SECRETS,
    }
    plan = resolve_credential_plan("claude", env, read_connection=reader)
    assert seen == [("claude", "claude-work")]
    assert plan.mode is CredentialMode.SUBSCRIPTION
    assert plan.connection_id == "claude-work"
    assert dict(plan.set_variables) == {"CLAUDE_CODE_OAUTH_TOKEN": token}
    assert "ANTHROPIC_API_KEY" in plan.strip_variables
    assert token not in repr(plan)


def test_subscription_material_outside_agent_variables_is_rejected():
    def reader(agent, connection_id):
        return ConnectionMaterial(variables={"OPENAI_API_KEY": DUMMY_OPENAI})

    env = {"SDF_CREDENTIAL_MODE_CLAUDE": "subscription", "SDF_CONNECTION_CLAUDE": "c1"}
    with pytest.raises(CredentialConfigError, match="OPENAI_API_KEY") as error:
        resolve_credential_plan("claude", env, read_connection=reader)
    _assert_no_secret(str(error.value))


# --- .env loading ---------------------------------------------------------------


def test_dotenv_loads_without_overriding_real_environment(tmp_path: Path):
    dotenv = tmp_path / ".env"
    dotenv.write_text(
        "# comment\n"
        "\n"
        "SDF_CREDENTIAL_MODE_CLAUDE=api-key\n"
        "export SDF_CREDENTIAL_MODE_CODEX=subscription\n"
        'SDF_ANTHROPIC_API_KEY="from-dotenv-dummy"\n'
        "SDF_OPENAI_API_KEY='quoted-dummy'\n"
        "SDF_CONNECTION_CODEX=codex-personal # trailing comment\n"
        "not a valid line\n"
    )
    environ = {"SDF_ANTHROPIC_API_KEY": "real-env-wins-dummy", "SDF_CREDENTIAL_MODE_CODEX": ""}
    loaded = load_dotenv(dotenv, environ)
    assert environ["SDF_ANTHROPIC_API_KEY"] == "real-env-wins-dummy"
    assert environ["SDF_CREDENTIAL_MODE_CODEX"] == ""  # present-but-empty still wins
    assert environ["SDF_CREDENTIAL_MODE_CLAUDE"] == "api-key"
    assert environ["SDF_OPENAI_API_KEY"] == "quoted-dummy"
    assert environ["SDF_CONNECTION_CODEX"] == "codex-personal"
    assert set(loaded) == {"SDF_CREDENTIAL_MODE_CLAUDE", "SDF_OPENAI_API_KEY", "SDF_CONNECTION_CODEX"}


@pytest.mark.parametrize("line", ['SDF_ANTHROPIC_API_KEY="sk-dummy-value"garbage', "SDF_ANTHROPIC_API_KEY='sk-dummy-value"])
def test_dotenv_malformed_quoted_value_names_variable_not_value(tmp_path: Path, line):
    dotenv = tmp_path / ".env"
    dotenv.write_text(f"# header\n{line}\n")
    with pytest.raises(CredentialConfigError) as error:
        load_dotenv(dotenv, {})
    message = str(error.value)
    assert "SDF_ANTHROPIC_API_KEY" in message and ":2:" in message
    assert "sk-dummy-value" not in message


def test_dotenv_quoted_value_with_trailing_comment(tmp_path: Path):
    dotenv = tmp_path / ".env"
    dotenv.write_text('SDF_OPENAI_API_KEY="dummy # not a comment"  # comment\n')
    environ: dict[str, str] = {}
    load_dotenv(dotenv, environ)
    assert environ == {"SDF_OPENAI_API_KEY": "dummy # not a comment"}


def test_dotenv_missing_file_is_a_noop(tmp_path: Path):
    environ: dict[str, str] = {}
    assert load_dotenv(tmp_path / ".env", environ) == ()
    assert environ == {}


def test_dotenv_defaults_to_repo_root(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    import sdf_core.credentials as credentials

    (tmp_path / ".env").write_text("SDF_CREDENTIAL_MODE_CLAUDE=api-key\n")
    monkeypatch.setattr(credentials, "REPO_ROOT", tmp_path)
    environ: dict[str, str] = {}
    load_dotenv(environ=environ)
    assert environ == {"SDF_CREDENTIAL_MODE_CLAUDE": "api-key"}


# --- repo hygiene ---------------------------------------------------------------

REPO = Path(__file__).resolve().parents[1]


def test_dotenv_is_git_ignored():
    patterns = [line.strip() for line in (REPO / ".gitignore").read_text().splitlines()]
    assert ".env" in patterns


def test_env_example_lists_every_variable_name_without_values():
    lines = [line.strip() for line in (REPO / ".env.example").read_text().splitlines()]
    entries = {}
    for line in lines:
        if not line or line.startswith("#"):
            continue
        name, sep, value = line.partition("=")
        assert sep == "=", line
        entries[name] = value
    required = {
        "SDF_CREDENTIAL_MODE_CLAUDE",
        "SDF_CREDENTIAL_MODE_CODEX",
        "SDF_CONNECTION_CLAUDE",
        "SDF_CONNECTION_CODEX",
        "SDF_ANTHROPIC_API_KEY",
        "SDF_OPENAI_API_KEY",
        "SDF_ANTHROPIC_BASE_URL",
        "SDF_OPENAI_BASE_URL",
    }
    assert required <= set(entries)
    for name in required:
        assert entries[name] == "", f"{name} must have no value in .env.example"


# --- subscription mode: expiry margin and base URL (#14) -----------------------

from datetime import datetime, timedelta, timezone

NOW = datetime(2026, 9, 25, 12, 0, tzinfo=timezone.utc)
CODEX_SUBSCRIPTION = {"SDF_CREDENTIAL_MODE_CODEX": "subscription", "SDF_CONNECTION_CODEX": "codex-personal"}
DUMMY_SESSION = '{"auth_mode":"chatgpt","tokens":{"access_token":"dummy-bearer-expiry"}}'


def _expiring_reader(expires_at):
    def reader(agent, connection_id):
        return ConnectionMaterial({"CODEX_AUTH_JSON": DUMMY_SESSION}, expires_at=expires_at)

    return reader


@pytest.mark.parametrize("seconds_past_margin", [0, -1])
def test_connection_expiring_within_the_margin_is_refused_with_connect_command(seconds_past_margin):
    timeout = 900
    expires_at = NOW + timedelta(seconds=timeout) + timedelta(minutes=10) + timedelta(seconds=seconds_past_margin)
    with pytest.raises(CredentialConfigError) as error:
        resolve_credential_plan(
            "codex",
            CODEX_SUBSCRIPTION,
            read_connection=_expiring_reader(expires_at),
            sandbox_timeout_seconds=timeout,
            now=NOW,
        )
    message = str(error.value)
    assert "e2b-box auth connect codex" in message
    assert "codex-personal" in message
    assert DUMMY_SESSION not in message and "dummy-bearer-expiry" not in message


def test_connection_expiring_just_after_the_margin_is_accepted():
    timeout = 900
    expires_at = NOW + timedelta(seconds=timeout, minutes=10, microseconds=1)
    plan = resolve_credential_plan(
        "codex",
        CODEX_SUBSCRIPTION,
        read_connection=_expiring_reader(expires_at),
        sandbox_timeout_seconds=timeout,
        now=NOW,
    )
    assert plan.mode is CredentialMode.SUBSCRIPTION
    assert plan.connection_id == "codex-personal"


def test_margin_scales_with_the_sandbox_timeout():
    expires_at = NOW + timedelta(minutes=30)
    reader = _expiring_reader(expires_at)
    resolve_credential_plan("codex", CODEX_SUBSCRIPTION, read_connection=reader, sandbox_timeout_seconds=600, now=NOW)
    with pytest.raises(CredentialConfigError, match="e2b-box auth connect codex"):
        resolve_credential_plan("codex", CODEX_SUBSCRIPTION, read_connection=reader, sandbox_timeout_seconds=1200, now=NOW)


def test_connection_without_expiry_is_accepted():
    plan = resolve_credential_plan(
        "codex", CODEX_SUBSCRIPTION, read_connection=_expiring_reader(None), sandbox_timeout_seconds=900, now=NOW
    )
    assert plan.connection_id == "codex-personal"


def test_naive_expiry_is_rejected_rather_than_guessed():
    with pytest.raises(CredentialConfigError, match="codex-personal"):
        resolve_credential_plan(
            "codex",
            CODEX_SUBSCRIPTION,
            read_connection=_expiring_reader(datetime(2026, 10, 3)),
            sandbox_timeout_seconds=900,
            now=NOW,
        )


def test_expiry_check_refuses_claude_with_its_own_connect_command():
    def reader(agent, connection_id):
        return ConnectionMaterial({"CLAUDE_CODE_OAUTH_TOKEN": "oauth-dummy"}, expires_at=NOW)

    env = {"SDF_CREDENTIAL_MODE_CLAUDE": "subscription", "SDF_CONNECTION_CLAUDE": "claude-work"}
    with pytest.raises(CredentialConfigError, match="e2b-box auth connect claude"):
        resolve_credential_plan("claude", env, read_connection=reader, sandbox_timeout_seconds=60, now=NOW)


@pytest.mark.parametrize(
    ("agent", "variable", "connection_variable"),
    [
        ("claude", "SDF_ANTHROPIC_BASE_URL", "SDF_CONNECTION_CLAUDE"),
        ("codex", "SDF_OPENAI_BASE_URL", "SDF_CONNECTION_CODEX"),
    ],
)
def test_base_url_is_rejected_in_subscription_mode(agent, variable, connection_variable):
    env = {
        f"SDF_CREDENTIAL_MODE_{agent.upper()}": "subscription",
        connection_variable: "some-connection",
        variable: "https://api.example.com/v1",
    }
    with pytest.raises(CredentialConfigError, match=variable) as error:
        resolve_credential_plan(agent, env, read_connection=lambda a, c: pytest.fail("reader must not be called"))
    assert "subscription" in str(error.value)
    assert "https://api.example.com" not in str(error.value)


def test_connection_material_repr_hides_values_but_shows_expiry():
    material = ConnectionMaterial({"CODEX_AUTH_JSON": DUMMY_SESSION}, expires_at=NOW)
    assert DUMMY_SESSION not in repr(material)
    assert "2026-09-25" in repr(material)
