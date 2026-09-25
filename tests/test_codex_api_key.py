"""Codex in api-key mode (ticket 08, ADR-0007): seeds and base URL, provider-free.

The seed scripts are executed with the host's node against a temporary HOME so
the exact bytes the sandbox runs are exercised.  The live counterpart is
tests/test_codex_api_key_live.py.
"""

import json
import os
import shutil
import subprocess

import pytest

from sdf_core.credential_injection import prepare_credential_injection, seed_command
from sdf_core.credentials import SeedStep, resolve_credential_plan

OPENAI_KEY = "sk-openai-dummy-333333333333333333333333"
BASE_URL = "https://gateway.example.com/openai/v1"

ENV = {"SDF_CREDENTIAL_MODE_CODEX": "api-key", "SDF_OPENAI_API_KEY": OPENAI_KEY}

needs_node = pytest.mark.skipif(shutil.which("node") is None, reason="seed scripts run under node; node not on PATH")

def _run_seed(step: SeedStep, home, variables):
    env = {"PATH": os.environ["PATH"], "HOME": str(home), **variables}
    result = subprocess.run(
        ["sh", "-c", seed_command(step)], env=env, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=30
    )
    assert result.returncode == 0
    for value in variables.values():
        assert value not in result.stdout + result.stderr
    return result

# --- plan ----------------------------------------------------------------------

def test_api_key_plan_seeds_auth_json_only_without_a_base_url():
    plan = resolve_credential_plan("codex", ENV)
    assert plan.seed_steps == (SeedStep("codex-auth-json-api-key", "OPENAI_API_KEY"),)

def test_base_url_is_applied_through_a_codex_config_seed():
    # Codex 0.157.0 ignores OPENAI_BASE_URL; it reads openai_base_url from
    # ~/.codex/config.toml (docs/research/codex-base-url.md).
    plan = resolve_credential_plan("codex", {**ENV, "SDF_OPENAI_BASE_URL": BASE_URL})
    assert SeedStep("codex-config-base-url", "OPENAI_BASE_URL") in plan.seed_steps
    assert plan.set_variables["OPENAI_BASE_URL"] == BASE_URL

def test_claude_api_key_plan_gets_no_codex_config_seed():
    plan = resolve_credential_plan(
        "claude",
        {"SDF_CREDENTIAL_MODE_CLAUDE": "api-key", "SDF_ANTHROPIC_API_KEY": "sk-ant-dummy-1", "SDF_ANTHROPIC_BASE_URL": BASE_URL},
    )
    assert all(step.action != "codex-config-base-url" for step in plan.seed_steps)

def test_seed_commands_name_variables_only():
    injection = prepare_credential_injection(("codex",), {**ENV, "SDF_OPENAI_BASE_URL": BASE_URL})
    joined = "\n".join(injection.seed_commands)
    assert "$OPENAI_API_KEY" in joined and "$OPENAI_BASE_URL" in joined
    assert OPENAI_KEY not in joined and BASE_URL not in joined

# --- auth.json seed ---------------------------------------------------------------

@needs_node
def test_auth_json_seed_writes_api_key_auth_mode(tmp_path):
    _run_seed(SeedStep("codex-auth-json-api-key", "OPENAI_API_KEY"), tmp_path, {"OPENAI_API_KEY": OPENAI_KEY})

    auth = tmp_path / ".codex" / "auth.json"
    assert json.loads(auth.read_text()) == {"auth_mode": "apikey", "OPENAI_API_KEY": OPENAI_KEY}
    assert auth.stat().st_mode & 0o777 == 0o600

@needs_node
def test_auth_json_seed_is_a_noop_without_the_variable(tmp_path):
    _run_seed(SeedStep("codex-auth-json-api-key", "OPENAI_API_KEY"), tmp_path, {})
    assert not (tmp_path / ".codex" / "auth.json").exists()

# --- config.toml base-URL seed -------------------------------------------------------

@needs_node
def test_base_url_seed_writes_openai_base_url_into_config_toml(tmp_path):
    import tomllib

    _run_seed(SeedStep("codex-config-base-url", "OPENAI_BASE_URL"), tmp_path, {"OPENAI_BASE_URL": BASE_URL})

    config = tomllib.loads((tmp_path / ".codex" / "config.toml").read_text())
    assert config == {"openai_base_url": BASE_URL}

@needs_node
def test_base_url_seed_merges_existing_config_and_replaces_a_previous_value(tmp_path):
    import tomllib

    codex = tmp_path / ".codex"
    codex.mkdir()
    (codex / "config.toml").write_text(
        'openai_base_url = "https://old.example.com"\nmodel = "gpt-5"\n\n[projects."/tmp"]\ntrust_level = "trusted"\n'
    )
    _run_seed(SeedStep("codex-config-base-url", "OPENAI_BASE_URL"), tmp_path, {"OPENAI_BASE_URL": BASE_URL})

    config = tomllib.loads((codex / "config.toml").read_text())
    assert config["openai_base_url"] == BASE_URL
    assert config["model"] == "gpt-5"
    assert config["projects"]["/tmp"]["trust_level"] == "trusted"

@needs_node
def test_base_url_seed_is_a_noop_without_the_variable(tmp_path):
    _run_seed(SeedStep("codex-config-base-url", "OPENAI_BASE_URL"), tmp_path, {})
    assert not (tmp_path / ".codex" / "config.toml").exists()
