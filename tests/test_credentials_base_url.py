"""Custom provider base URL rules (ticket 05, ADR-0007).

Pure tests: plain mappings only, no DNS, no network, no E2B.
"""

import pytest

from sdf_core.credentials import (
    CONFLICTING_VARIABLES,
    ConnectionMaterial,
    CredentialConfigError,
    resolve_credential_plan,
)

DUMMY_ANTHROPIC = "sk-ant-dummy-0123456789abcdef"
DUMMY_OPENAI = "sk-openai-dummy-fedcba9876543210"

AGENTS = {
    "claude": ("SDF_ANTHROPIC_API_KEY", DUMMY_ANTHROPIC, "SDF_ANTHROPIC_BASE_URL", "ANTHROPIC_BASE_URL"),
    "codex": ("SDF_OPENAI_API_KEY", DUMMY_OPENAI, "SDF_OPENAI_BASE_URL", "OPENAI_BASE_URL"),
}


def _env(agent: str, **extra: str) -> dict[str, str]:
    key_var, key, _, _ = AGENTS[agent]
    return {f"SDF_CREDENTIAL_MODE_{agent.upper()}": "api-key", key_var: key, **extra}


# --- Valid https public URL becomes the provider base-URL variable ------------


@pytest.mark.parametrize("agent", AGENTS)
@pytest.mark.parametrize(
    "url",
    [
        "https://api.example.com",
        "https://gateway.example.com/v1",
        "https://gateway.example.com:8443/anthropic/",
        "HTTPS://Api.Example.com",
        "https://8.8.8.8/v1",
        "https://[2606:4700:4700::1111]/v1",
    ],
)
def test_valid_https_public_url_becomes_provider_base_url(agent, url):
    _, key, source, target = AGENTS[agent]
    plan = resolve_credential_plan(agent, _env(agent, **{source: url}))
    assert plan.set_variables[target] == url
    assert target not in plan.strip_variables
    assert not set(plan.set_variables) & set(plan.strip_variables)


@pytest.mark.parametrize("agent", AGENTS)
def test_base_url_is_trimmed(agent):
    _, _, source, target = AGENTS[agent]
    plan = resolve_credential_plan(agent, _env(agent, **{source: "  https://api.example.com/v1  "}))
    assert plan.set_variables[target] == "https://api.example.com/v1"


# --- Unset base URL adds no base-URL variable ---------------------------------


@pytest.mark.parametrize("agent", AGENTS)
@pytest.mark.parametrize("value", [None, "", "   "])
def test_unset_base_url_adds_no_base_url_variable(agent, value):
    _, _, source, target = AGENTS[agent]
    env = _env(agent) if value is None else _env(agent, **{source: value})
    plan = resolve_credential_plan(agent, env)
    assert target not in plan.set_variables
    assert target in plan.strip_variables


# --- Unreachable or insecure URLs are rejected with an explanation ------------

REJECTED = {
    "http": "http://api.example.com/v1",
    "no scheme": "api.example.com/v1",
    "ftp": "ftp://api.example.com",
    "no host": "https:///v1",
    "localhost": "https://localhost:8317",
    "localhost upper": "https://LOCALHOST/v1",
    "localhost fqdn": "https://localhost./v1",
    "localhost subdomain": "https://proxy.localhost/v1",
    "127.0.0.1": "https://127.0.0.1:8317",
    "127/8": "https://127.200.3.4/v1",
    "127 short form": "https://127.1/v1",
    "127 decimal": "https://2130706433/v1",
    "127 hex": "https://0x7f000001/v1",
    "::1": "https://[::1]:8317/v1",
    "v4-mapped loopback": "https://[::ffff:127.0.0.1]/v1",
    "10/8": "https://10.1.2.3/v1",
    "172.16/12 low": "https://172.16.0.1/v1",
    "172.16/12 high": "https://172.31.255.254/v1",
    "192.168/16": "https://192.168.1.10/v1",
    "169.254/16": "https://169.254.169.254/latest",
    "0.0.0.0": "https://0.0.0.0:8317",
    "::": "https://[::]/v1",
    "fe80::/10": "https://[fe80::1]/v1",
    "fc00::/7": "https://[fd12:3456::1]/v1",
    "cgnat": "https://100.64.0.1/v1",
    "bad port": "https://api.example.com:99999/v1",
    "userinfo": "https://user:pass@api.example.com/v1",
}


@pytest.mark.parametrize("agent", AGENTS)
@pytest.mark.parametrize("url", REJECTED.values(), ids=REJECTED.keys())
def test_unreachable_or_insecure_base_url_is_rejected_naming_the_variable(agent, url):
    _, _, source, _ = AGENTS[agent]
    with pytest.raises(CredentialConfigError) as error:
        resolve_credential_plan(agent, _env(agent, **{source: url}))
    message = str(error.value)
    assert source in message
    assert url not in message
    assert DUMMY_ANTHROPIC not in message and DUMMY_OPENAI not in message


@pytest.mark.parametrize(
    ("url", "reason"),
    [
        ("http://api.example.com", "https"),
        ("https://localhost/v1", "sandbox cannot reach"),
        ("https://10.0.0.1/v1", "sandbox cannot reach"),
    ],
)
def test_rejection_explains_why(url, reason):
    with pytest.raises(CredentialConfigError, match=reason):
        resolve_credential_plan("claude", _env("claude", SDF_ANTHROPIC_BASE_URL=url))


def test_172_outside_private_range_is_public():
    plan = resolve_credential_plan("claude", _env("claude", SDF_ANTHROPIC_BASE_URL="https://172.32.0.1/v1"))
    assert plan.set_variables["ANTHROPIC_BASE_URL"] == "https://172.32.0.1/v1"


def test_hostnames_are_not_resolved(monkeypatch: pytest.MonkeyPatch):
    import socket

    def _no_dns(*args, **kwargs):
        raise AssertionError("base URL validation must not resolve DNS")

    monkeypatch.setattr(socket, "getaddrinfo", _no_dns)
    monkeypatch.setattr(socket, "gethostbyname", _no_dns)
    plan = resolve_credential_plan("codex", _env("codex", SDF_OPENAI_BASE_URL="https://api.example.com/v1"))
    assert plan.set_variables["OPENAI_BASE_URL"] == "https://api.example.com/v1"


# --- Base URL is an api-key-mode feature; shell variables never reach a plan --


@pytest.mark.parametrize("agent", AGENTS)
def test_shell_base_url_never_reaches_a_plan(agent):
    _, _, _, target = AGENTS[agent]
    env = _env(agent, ANTHROPIC_BASE_URL="https://shell.example.com", OPENAI_BASE_URL="https://shell.example.com")
    plan = resolve_credential_plan(agent, env)
    assert target not in plan.set_variables
    assert "https://shell.example.com" not in repr(plan)


def test_other_agents_base_url_does_not_apply():
    plan = resolve_credential_plan("claude", _env("claude", SDF_OPENAI_BASE_URL="https://api.example.com"))
    assert "ANTHROPIC_BASE_URL" not in plan.set_variables
    assert "OPENAI_BASE_URL" not in plan.set_variables


def test_base_url_never_reaches_a_subscription_plan():
    # A base URL set for a subscription agent is rejected, not silently dropped (#14).
    env = {
        "SDF_CREDENTIAL_MODE_CLAUDE": "subscription",
        "SDF_CONNECTION_CLAUDE": "work",
        "SDF_ANTHROPIC_BASE_URL": "https://api.example.com",
    }
    material = ConnectionMaterial({"CLAUDE_CODE_OAUTH_TOKEN": "oauth-dummy"})
    with pytest.raises(CredentialConfigError, match="SDF_ANTHROPIC_BASE_URL"):
        resolve_credential_plan("claude", env, read_connection=lambda agent, cid: material)
    # Without a base URL the plan still strips the provider's base URL name.
    del env["SDF_ANTHROPIC_BASE_URL"]
    plan = resolve_credential_plan("claude", env, read_connection=lambda agent, cid: material)
    assert "ANTHROPIC_BASE_URL" not in plan.set_variables
    assert "ANTHROPIC_BASE_URL" in plan.strip_variables
    assert set(plan.strip_variables) <= set(CONFLICTING_VARIABLES["claude"])
