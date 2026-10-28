"""ADR-0021: per-agent project trust; SDF endpoint/key/model and sub-agent bans still win."""

from __future__ import annotations

import json
import shutil
import subprocess

import pytest

from sdf_core import project_trust, subagents
from sdf_core.credential_injection import _CLAUDE_TRUST_WORKSPACE, CLAUDE_SETTINGS_SUFFIX
from sdf_core.herdr_runtime import _agent_start_args, _fresh_e2b_permission_bypass_available

KINDS = ("claude", "codex", "opencode", "pi", "qwen", "goose")


@pytest.mark.parametrize("kind", KINDS)
def test_default_is_always_and_never_is_honoured(kind, monkeypatch):
    name = project_trust.env_name(kind)
    assert name == f"SDF_{kind.upper()}_PROJECT_TRUST"
    monkeypatch.delenv(name, raising=False)
    assert project_trust.setting(kind) == "always" and project_trust.wants_trust(kind)
    monkeypatch.setenv(name, "")
    assert project_trust.wants_trust(kind)
    monkeypatch.setenv(name, "never")
    assert not project_trust.wants_trust(kind)
    assert project_trust.effective(kind, fresh_e2b=True) is False


@pytest.mark.parametrize("value", ["yes", "1", "trusted", "ALWAYS!"])
def test_invalid_setting_fails_closed_with_the_adr(value, monkeypatch):
    monkeypatch.setenv("SDF_PI_PROJECT_TRUST", value)
    with pytest.raises(ValueError, match="ADR-0021"):
        project_trust.setting("pi")


def test_trust_needs_the_fresh_e2b_sandbox():
    assert project_trust.effective("pi", fresh_e2b=False) is False
    assert project_trust.effective("pi", fresh_e2b=True) is True
    # Non-E2B transports never qualify.
    assert _fresh_e2b_permission_bypass_available(object()) is False
    assert _fresh_e2b_permission_bypass_available(None) is False


def test_claude_loads_the_project_only_with_sdf_flag_settings():
    path = "/home/user" + CLAUDE_SETTINGS_SUFFIX
    trusted = _agent_start_args("claude", "/w", allow_dangerous_permissions=True, trust_project=True,
                                claude_settings=path)
    assert trusted[trusted.index("--setting-sources") + 1] == "user,project,local"
    assert trusted[trusted.index("--settings") + 1] == path
    # Without the pinning file the project stays out.
    no_file = _agent_start_args("claude", "/w", allow_dangerous_permissions=True, trust_project=True)
    assert no_file[no_file.index("--setting-sources") + 1] == "user" and "--settings" not in no_file
    untrusted = _agent_start_args("claude", "/w", trust_project=False, claude_settings=path)
    assert untrusted[untrusted.index("--setting-sources") + 1] == "user"
    for args in (trusted, no_file, untrusted):
        assert args[: len(subagents.CLAUDE_ARGS)] == subagents.CLAUDE_ARGS  # ADR-0020 still first


@pytest.mark.parametrize("trust, level", [(True, "trusted"), (False, "untrusted")])
def test_codex_trust_level_follows_the_setting_and_policy_stays_pinned(trust, level):
    args = _agent_start_args("codex", "/tmp/sdf/A", trust_project=trust)
    pairs = [args[i + 1] for i, a in enumerate(args) if a == "-c"]
    assert f'projects."/tmp/sdf/A".trust_level="{level}"' in pairs
    assert {'approval_policy="on-request"', 'sandbox_mode="workspace-write"',
            "sandbox_workspace_write.network_access=false", "features.multi_agent=false"} <= set(pairs)


def test_codex_default_comes_from_the_environment(monkeypatch):
    monkeypatch.setenv("SDF_CODEX_PROJECT_TRUST", "never")
    assert 'projects."/A".trust_level="untrusted"' in _agent_start_args("codex", "/A")
    monkeypatch.delenv("SDF_CODEX_PROJECT_TRUST")
    assert 'projects."/A".trust_level="trusted"' in _agent_start_args("codex", "/A")


@pytest.mark.skipif(shutil.which("node") is None, reason="node not installed")
@pytest.mark.parametrize("with_key", [True, False])
def test_claude_settings_file_pins_endpoint_and_moves_the_key_to_a_helper(tmp_path, with_key):
    key = "sk-ant-test-0123456789-secret"
    env = {"HOME": str(tmp_path), "PATH": "/usr/bin:/bin:/usr/local/bin", "ANTHROPIC_BASE_URL": "https://ours.example"}
    if with_key:
        env["ANTHROPIC_API_KEY"] = key
    (tmp_path / ".config/sdf").mkdir(parents=True)
    (tmp_path / ".config/sdf/agent-env.sh").write_text(f"export ANTHROPIC_API_KEY='{key}'\n")
    out = subprocess.run(["node", "-e", _CLAUDE_TRUST_WORKSPACE, "/tmp/sdf/W"], env=env, capture_output=True,
                         text=True, check=True).stdout
    path = out.strip().splitlines()[-1]
    assert path == str(tmp_path) + CLAUDE_SETTINGS_SUFFIX
    text = open(path).read()
    assert key not in text  # names only, never the value
    settings = json.loads(text)
    pinned = settings["env"]
    assert pinned["ANTHROPIC_BASE_URL"] == "https://ours.example"
    assert pinned["ANTHROPIC_API_KEY"] == "" and pinned["ANTHROPIC_AUTH_TOKEN"] == ""
    assert pinned["CLAUDE_CODE_USE_BEDROCK"] == pinned["CLAUDE_CODE_USE_VERTEX"] == ""
    assert (oct((tmp_path / ".config/sdf/claude-settings.json").stat().st_mode & 0o777)) == "0o600"
    if with_key:
        helper = subprocess.run(["sh", "-c", settings["apiKeyHelper"]], env={"HOME": str(tmp_path), "PATH": env["PATH"]},
                                capture_output=True, text=True, check=True).stdout
        assert helper == key
    else:
        assert "apiKeyHelper" not in settings
    trust = json.loads((tmp_path / ".claude.json").read_text())
    assert trust["projects"]["/tmp/sdf/W"]["hasTrustDialogAccepted"] is True


@pytest.mark.skipif(shutil.which("node") is None, reason="node not installed")
def test_pi_seed_writes_default_project_trust(tmp_path):
    from sdf_core.credential_injection import _PI_MODELS

    env = {"HOME": str(tmp_path), "PATH": "/usr/bin:/bin:/usr/local/bin", "SDF_PI_BASE_URL": "https://x.example/v1",
           "SDF_PI_MODEL": "m", "SDF_TEST_API_KEY": "k-secret"}
    (tmp_path / ".pi/agent").mkdir(parents=True)
    (tmp_path / ".pi/agent/settings.json").write_text('{"theme":"dark","defaultProjectTrust":"never"}')
    subprocess.run(["node", "-e", _PI_MODELS, "SDF_TEST_API_KEY"], env=env, capture_output=True, text=True, check=True)
    st = json.loads((tmp_path / ".pi/agent/settings.json").read_text())
    assert st == {"theme": "dark", "defaultProjectTrust": "always"}
