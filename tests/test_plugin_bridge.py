"""Plugin bridge: Node.js helper integration for herdr-e2b connections (ADR-0008).

Fake plugin directory in temp files only, no network, node-based tests
skip when node is not available. Live tests require SDF_LIVE_PLUGIN=1.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
from pathlib import Path
from typing import Any
from unittest.mock import patch

import pytest

from sdf_core.credentials import ConnectionMaterial, CredentialConfigError, resolve_credential_plan

# Dummy credentials for testing
DUMMY_CODEX_AUTH = "dummy-codex-auth-json-secret"
DUMMY_CLAUDE_TOKEN = "sk-ant-oat01-dummy-oauth-token"


def _assert_no_secret(message: str) -> None:
    """Assert that secret values do not appear in message."""
    secrets = [DUMMY_CODEX_AUTH, DUMMY_CLAUDE_TOKEN]
    for secret in secrets:
        assert secret not in message, f"Secret {secret!r} found in message: {message}"


# --- Fixtures ----------------------------------------------------------------


@pytest.fixture
def plugins_root(tmp_path: Path) -> Path:
    """Create a fake plugins root with a stub herdr-e2b connection."""
    plugins = tmp_path / "plugins"
    plugin_dir = plugins / "github" / "e2b-dev.herdr-e2b-fake123" / "src"
    plugin_dir.mkdir(parents=True)

    # Create package.json to mark plugin dir as ES module
    (plugin_dir.parent.parent / "package.json").write_text('{"type":"module"}\n')

    # Create stub connections.js
    connections_js = plugin_dir / "connections.js"
    connections_js.write_text(
        """export function readConnections() {
  return [
    { id: "codex-personal", harness: "codex" },
    { id: "claude-work", harness: "claude" },
    { id: "codex-expired", harness: "codex" },
  ];
}

export function connectionMaterial(record) {
  if (record.id === "codex-personal") {
    return {
      env: { CODEX_AUTH_JSON: "dummy-codex-auth-json-secret" },
      expiresAt: null,
    };
  }
  if (record.id === "claude-work") {
    return {
      env: { CLAUDE_CODE_OAUTH_TOKEN: "sk-ant-oat01-dummy-oauth-token" },
      expiresAt: null,
    };
  }
  if (record.id === "codex-expired") {
    throw Error("Connection 'codex-expired' has expired; please refresh");
  }
  throw Error(`Unknown connection: ${record.id}`);
}
"""
    )

    return plugins


@pytest.fixture
def plugins_root_with_env_leakage(tmp_path: Path) -> Path:
    """Create a plugin that would expose env vars if not allowlisted."""
    plugins = tmp_path / "plugins"
    plugin_dir = plugins / "github" / "e2b-dev.herdr-e2b-test456" / "src"
    plugin_dir.mkdir(parents=True)

    (plugin_dir.parent.parent / "package.json").write_text('{"type":"module"}\n')

    connections_js = plugin_dir / "connections.js"
    connections_js.write_text(
        """export function readConnections() {
  return [{ id: "test-env-leak", harness: "codex" }];
}

export function connectionMaterial(record) {
  return {
    env: {
      CODEX_AUTH_JSON: String(process.env.ANTHROPIC_API_KEY ?? "absent"),
    },
  };
}
"""
    )

    return plugins


@pytest.fixture
def plugins_root_malformed_material(tmp_path: Path) -> Path:
    """Create a plugin that returns non-string material values."""
    plugins = tmp_path / "plugins"
    plugin_dir = plugins / "github" / "e2b-dev.herdr-e2b-bad789" / "src"
    plugin_dir.mkdir(parents=True)

    (plugin_dir.parent.parent / "package.json").write_text('{"type":"module"}\n')

    connections_js = plugin_dir / "connections.js"
    connections_js.write_text(
        """export function readConnections() {
  return [{ id: "bad-material", harness: "codex" }];
}

export function connectionMaterial(record) {
  return {
    env: {
      CODEX_AUTH_JSON: 42,  // Not a string!
    },
  };
}
"""
    )

    return plugins


# --- Happy path tests --------------------------------------------------------


@pytest.mark.skipif(shutil.which("node") is None, reason="node not available")
def test_plugin_bridge_happy_path_codex_personal(plugins_root: Path):
    """Load codex-personal connection via plugin bridge."""
    from sdf_core.plugin_bridge import PluginConnectionBridge

    bridge = PluginConnectionBridge(plugins_root=plugins_root)
    material = bridge("codex", "codex-personal")

    assert isinstance(material, ConnectionMaterial)
    assert material.variables == {"CODEX_AUTH_JSON": DUMMY_CODEX_AUTH}


@pytest.mark.skipif(shutil.which("node") is None, reason="node not available")
def test_plugin_bridge_happy_path_claude_work(plugins_root: Path):
    """Load claude-work connection via plugin bridge."""
    from sdf_core.plugin_bridge import PluginConnectionBridge

    bridge = PluginConnectionBridge(plugins_root=plugins_root)
    material = bridge("claude", "claude-work")

    assert isinstance(material, ConnectionMaterial)
    assert material.variables == {"CLAUDE_CODE_OAUTH_TOKEN": DUMMY_CLAUDE_TOKEN}


# --- End-to-end integration with resolve_credential_plan ---------------------


@pytest.mark.skipif(shutil.which("node") is None, reason="node not available")
def test_plugin_bridge_with_resolve_credential_plan(plugins_root: Path):
    """End-to-end: PluginConnectionBridge integrates with resolve_credential_plan."""
    from sdf_core.plugin_bridge import PluginConnectionBridge

    bridge = PluginConnectionBridge(plugins_root=plugins_root)
    env = {
        "SDF_CREDENTIAL_MODE_CODEX": "subscription",
        "SDF_CONNECTION_CODEX": "codex-personal",
    }
    plan = resolve_credential_plan("codex", env, read_connection=bridge)

    assert plan.agent == "codex"
    assert plan.connection_id == "codex-personal"
    assert plan.set_variables == {"CODEX_AUTH_JSON": DUMMY_CODEX_AUTH}
    # Ensure repr doesn't leak the secret
    assert DUMMY_CODEX_AUTH not in repr(plan)
    assert "CODEX_AUTH_JSON" in repr(plan)


# --- Error handling: missing plugin -----------------------------------------------


def test_missing_plugin_names_searched_path(tmp_path: Path):
    """When plugin dir doesn't exist, error names the searched path."""
    from sdf_core.plugin_bridge import find_plugin

    plugins_root = tmp_path / "nonexistent" / "plugins"
    with pytest.raises(CredentialConfigError) as error:
        find_plugin(plugins_root)

    message = str(error.value)
    assert str(plugins_root) in message
    _assert_no_secret(message)


def test_empty_plugins_root_names_searched_path(tmp_path: Path):
    """When plugin root exists but is empty, error names the searched path."""
    from sdf_core.plugin_bridge import find_plugin

    plugins_root = tmp_path / "plugins"
    plugins_root.mkdir(parents=True)

    with pytest.raises(CredentialConfigError) as error:
        find_plugin(plugins_root)

    message = str(error.value)
    assert str(plugins_root) in message
    _assert_no_secret(message)


# --- Error handling: unknown connection id -----------------------------------------------


@pytest.mark.skipif(shutil.which("node") is None, reason="node not available")
def test_unknown_connection_id_names_the_id(plugins_root: Path):
    """When connection id is not found, error names the id."""
    from sdf_core.plugin_bridge import PluginConnectionBridge

    bridge = PluginConnectionBridge(plugins_root=plugins_root)
    with pytest.raises(CredentialConfigError) as error:
        bridge("codex", "nope")

    message = str(error.value)
    assert "nope" in message
    _assert_no_secret(message)


# --- Error handling: harness mismatch -----------------------------------------------


@pytest.mark.skipif(shutil.which("node") is None, reason="node not available")
def test_harness_mismatch_names_id_and_both_harnesses(plugins_root: Path):
    """When connection harness != agent, error names connection and both harnesses."""
    from sdf_core.plugin_bridge import PluginConnectionBridge

    bridge = PluginConnectionBridge(plugins_root=plugins_root)
    # codex-personal is a codex connection, but we ask for it as claude
    with pytest.raises(CredentialConfigError) as error:
        bridge("claude", "codex-personal")

    message = str(error.value)
    assert "codex-personal" in message
    assert "codex" in message
    assert "claude" in message
    _assert_no_secret(message)


# --- Error handling: connection throws error -----------------------------------------------


@pytest.mark.skipif(shutil.which("node") is None, reason="node not available")
def test_connection_expired_error_contains_id_not_message(plugins_root: Path):
    """When connectionMaterial throws, error contains id but not the thrown message."""
    from sdf_core.plugin_bridge import PluginConnectionBridge

    bridge = PluginConnectionBridge(plugins_root=plugins_root)
    with pytest.raises(CredentialConfigError) as error:
        bridge("codex", "codex-expired")

    message = str(error.value)
    assert "codex-expired" in message
    # The thrown message "has expired; please refresh" should not be in error
    assert "has expired" not in message
    _assert_no_secret(message)


# --- Error handling: malformed helper output -----------------------------------------------


@pytest.mark.skipif(shutil.which("node") is None, reason="node not available")
def test_malformed_json_output_from_helper(plugins_root: Path, monkeypatch: pytest.MonkeyPatch):
    """When helper outputs invalid JSON, error raised without echoing raw output."""
    from sdf_core.plugin_bridge import PluginConnectionBridge

    # Monkeypatch subprocess.run to return invalid JSON
    original_run = subprocess.run

    def mock_run(*args, **kwargs):
        result = subprocess.CompletedProcess(args, returncode=0)
        result.stdout = b"not valid json\n"
        return result

    monkeypatch.setattr(subprocess, "run", mock_run)

    bridge = PluginConnectionBridge(plugins_root=plugins_root)
    with pytest.raises(CredentialConfigError) as error:
        bridge("codex", "codex-personal")

    message = str(error.value)
    # Must not echo raw output
    assert "not valid json" not in message
    _assert_no_secret(message)


@pytest.mark.skipif(shutil.which("node") is None, reason="node not available")
def test_non_string_variable_values(plugins_root_malformed_material: Path):
    """When material variables contain non-strings, error raised."""
    from sdf_core.plugin_bridge import PluginConnectionBridge

    bridge = PluginConnectionBridge(plugins_root=plugins_root_malformed_material)
    with pytest.raises(CredentialConfigError) as error:
        bridge("codex", "bad-material")

    message = str(error.value)
    _assert_no_secret(message)


# --- Error handling: node not found -----------------------------------------------


def test_node_not_found_error_mentions_node(tmp_path: Path):
    """When node executable is not found, error mentions node."""
    from sdf_core.plugin_bridge import PluginConnectionBridge

    plugins = tmp_path / "plugins"
    plugins.mkdir()

    bridge = PluginConnectionBridge(plugins_root=plugins, node="/nonexistent/node")
    with pytest.raises(CredentialConfigError) as error:
        bridge("codex", "test")

    message = str(error.value)
    assert "node" in message.lower()
    _assert_no_secret(message)


# --- Environment allowlist -----------------------------------------------


@pytest.mark.skipif(shutil.which("node") is None, reason="node not available")
def test_env_allowlist_prevents_api_key_leakage(plugins_root_with_env_leakage: Path):
    """Subprocess env is allowlisted; provider vars don't reach the plugin."""
    from sdf_core.plugin_bridge import PluginConnectionBridge

    # Set ANTHROPIC_API_KEY in this process's environment
    env = {"ANTHROPIC_API_KEY": "sk-ant-this-should-not-leak"}

    bridge = PluginConnectionBridge(
        plugins_root=plugins_root_with_env_leakage,
        environ={"ANTHROPIC_API_KEY": "sk-ant-this-should-not-leak"},
    )
    material = bridge("codex", "test-env-leak")

    # The CODEX_AUTH_JSON should be "absent", not the API key
    assert material.variables["CODEX_AUTH_JSON"] == "absent"


# --- stdin closed -----------------------------------------------


@pytest.mark.skipif(shutil.which("node") is None, reason="node not available")
def test_stdin_is_devnull(plugins_root: Path, monkeypatch: pytest.MonkeyPatch):
    """PluginConnectionBridge closes stdin (subprocess.DEVNULL)."""
    from sdf_core.plugin_bridge import PluginConnectionBridge

    captured_kwargs = {}
    original_run = subprocess.run

    def capture_run(*args, **kwargs):
        captured_kwargs.update(kwargs)
        return original_run(*args, **kwargs)

    monkeypatch.setattr(subprocess, "run", capture_run)

    bridge = PluginConnectionBridge(plugins_root=plugins_root)
    bridge("codex", "codex-personal")

    assert captured_kwargs.get("stdin") is subprocess.DEVNULL


# --- find_plugin behavior -----------------------------------------------


def test_find_plugin_picks_newest_mtime(tmp_path: Path):
    """find_plugin returns the plugin dir with the newest mtime."""
    from sdf_core.plugin_bridge import find_plugin

    plugins = tmp_path / "plugins" / "github"
    plugins.mkdir(parents=True)

    # Create two plugin dirs
    dir1 = plugins / "e2b-dev.herdr-e2b-001"
    dir2 = plugins / "e2b-dev.herdr-e2b-002"
    src1 = dir1 / "src"
    src2 = dir2 / "src"
    src1.mkdir(parents=True)
    src2.mkdir(parents=True)

    # Create connections.js in both
    (src1 / "connections.js").write_text("export function readConnections() { return []; }")
    (src2 / "connections.js").write_text("export function readConnections() { return []; }")

    # Make dir1 older
    os.utime(dir1, (0, 1000000))
    os.utime(dir2, (0, 2000000))

    result = find_plugin(plugins.parent)
    assert result.name == "e2b-dev.herdr-e2b-002"


def test_find_plugin_ignores_dir_without_connections_js(tmp_path: Path):
    """find_plugin ignores dirs that don't have src/connections.js."""
    from sdf_core.plugin_bridge import find_plugin

    plugins = tmp_path / "plugins" / "github"
    plugins.mkdir(parents=True)

    # Create dir without src/connections.js
    dir1 = plugins / "e2b-dev.herdr-e2b-001"
    dir1.mkdir(parents=True)

    # Create dir with src/connections.js
    dir2 = plugins / "e2b-dev.herdr-e2b-002"
    src2 = dir2 / "src"
    src2.mkdir(parents=True)
    (src2 / "connections.js").write_text("export function readConnections() { return []; }")

    result = find_plugin(plugins.parent)
    assert result.name == "e2b-dev.herdr-e2b-002"


# --- default_plugins_root -----------------------------------------------


def test_default_plugins_root_with_xdg_config_home(tmp_path: Path):
    """default_plugins_root uses XDG_CONFIG_HOME when set and non-empty."""
    from sdf_core.plugin_bridge import default_plugins_root

    environ = {"XDG_CONFIG_HOME": str(tmp_path / "xdg"), "HOME": str(tmp_path / "home")}
    result = default_plugins_root(environ)

    assert result == Path(tmp_path / "xdg") / "herdr" / "plugins"


def test_default_plugins_root_skips_empty_xdg_config_home(tmp_path: Path):
    """default_plugins_root falls back to HOME when XDG_CONFIG_HOME is empty."""
    from sdf_core.plugin_bridge import default_plugins_root

    environ = {"XDG_CONFIG_HOME": "", "HOME": str(tmp_path / "home")}
    result = default_plugins_root(environ)

    assert result == Path(tmp_path / "home") / ".config" / "herdr" / "plugins"


def test_default_plugins_root_uses_home_fallback(tmp_path: Path):
    """default_plugins_root uses HOME when XDG_CONFIG_HOME is not set."""
    from sdf_core.plugin_bridge import default_plugins_root

    environ = {"HOME": str(tmp_path / "home")}
    result = default_plugins_root(environ)

    assert result == Path(tmp_path / "home") / ".config" / "herdr" / "plugins"


def test_default_plugins_root_falls_back_to_path_home(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """default_plugins_root falls back to Path.home() when HOME is not in environ."""
    from sdf_core.plugin_bridge import default_plugins_root

    environ = {}
    monkeypatch.setattr(Path, "home", lambda: tmp_path / "pathome")
    result = default_plugins_root(environ)

    assert result == Path(tmp_path / "pathome") / ".config" / "herdr" / "plugins"


# --- No material on disk -----------------------------------------------


@pytest.mark.skipif(shutil.which("node") is None, reason="node not available")
def test_no_secret_written_to_disk(plugins_root: Path, tmp_path: Path):
    """After successful credential read, no secret is written to disk."""
    from sdf_core.plugin_bridge import PluginConnectionBridge

    # Change to tmp_path to ensure nothing leaks
    original_cwd = os.getcwd()
    os.chdir(tmp_path)

    try:
        bridge = PluginConnectionBridge(plugins_root=plugins_root)
        bridge("codex", "codex-personal")

        # Walk the entire tmp_path and verify no secret is present
        for root, dirs, files in os.walk(tmp_path):
            for file in files:
                filepath = Path(root) / file
                if filepath == plugins_root:
                    continue
                try:
                    content = filepath.read_text(errors="ignore")
                    assert DUMMY_CODEX_AUTH not in content, f"Secret found in {filepath}"
                except Exception:
                    pass
    finally:
        os.chdir(original_cwd)


# --- PLUGIN_GLOB and HELPER_PATH -----------------------------------------------


def test_plugin_glob_constant_exists():
    """PLUGIN_GLOB constant is defined."""
    from sdf_core.plugin_bridge import PLUGIN_GLOB

    assert PLUGIN_GLOB == "github/e2b-dev.herdr-e2b-*"


def test_helper_path_exists():
    """HELPER_PATH points to an existing file."""
    from sdf_core.plugin_bridge import HELPER_PATH

    assert HELPER_PATH.exists()
    assert HELPER_PATH.suffix == ".mjs"
    assert "plugin_bridge_helper" in HELPER_PATH.name


# --- Ownership: no plugin internals in other modules -----------------------------------------------


REPO = Path(__file__).resolve().parents[1]


def test_no_plugin_internals_in_credentials():
    """sdf_core/credentials.py does not reference plugin internals."""
    credentials = (REPO / "sdf_core" / "credentials.py").read_text()

    forbidden = ["connections.js", "connectionMaterial", "readConnections", "e2b-dev.herdr-e2b"]
    for marker in forbidden:
        assert marker not in credentials, f"Plugin internal {marker!r} found in credentials.py"


def test_no_plugin_internals_in_other_modules():
    """Other sdf_core/*.py files do not reference plugin internals."""
    forbidden = ["connections.js", "connectionMaterial", "readConnections", "e2b-dev.herdr-e2b"]

    for module in (REPO / "sdf_core").glob("*.py"):
        if module.name in {"plugin_bridge.py", "credentials.py"}:
            continue

        content = module.read_text()
        for marker in forbidden:
            assert marker not in content, f"Plugin internal {marker!r} found in {module.name}"


# --- Live test (gated by environment variable) -----------------------------------------------


@pytest.mark.skipif(
    os.environ.get("SDF_LIVE_PLUGIN") != "1",
    reason="set SDF_LIVE_PLUGIN=1 to test live herdr-e2b plugin",
)
def test_live_plugin_codex_personal():
    """Live: read the real herdr-e2b codex-personal connection.

    Never print the actual secret; assert on shape and keys only.
    """
    from sdf_core.plugin_bridge import PluginConnectionBridge

    material = PluginConnectionBridge()("codex", "codex-personal")

    # Assert only on names and computed booleans: pytest's assertion rewriting
    # would otherwise echo the credential on failure.
    names = sorted(material.variables)
    assert names == ["CODEX_AUTH_JSON"]
    value = material.variables["CODEX_AUTH_JSON"]
    shape_ok = isinstance(value, str) and bool(value.strip())
    assert shape_ok, "CODEX_AUTH_JSON is not a non-empty string"
    try:
        parsed_is_object = isinstance(json.loads(value), dict)
    except ValueError:
        parsed_is_object = False
    assert parsed_is_object, "CODEX_AUTH_JSON is not a JSON object"


# --- expiry (#14) ----------------------------------------------------------------


def _plugin_with_expiry(tmp_path: Path, expires_at: str) -> Path:
    plugins = tmp_path / "plugins"
    src = plugins / "github" / "e2b-dev.herdr-e2b-exp" / "src"
    src.mkdir(parents=True)
    (src.parent / "package.json").write_text('{"type":"module"}\n')
    (src / "connections.js").write_text(
        'export function readConnections() { return [{ id: "codex-personal", harness: "codex" }] }\n'
        "export function connectionMaterial() {\n"
        f'  return {{ env: {{ CODEX_AUTH_JSON: "{DUMMY_CODEX_AUTH}" }}, expiresAt: {expires_at} }}\n'
        "}\n"
    )
    return plugins


@pytest.mark.skipif(shutil.which("node") is None, reason="node is required for the plugin bridge")
def test_plugin_bridge_reports_connection_expiry(tmp_path: Path):
    from datetime import datetime, timezone

    from sdf_core.plugin_bridge import PluginConnectionBridge

    material = PluginConnectionBridge(_plugin_with_expiry(tmp_path, '"2026-10-03T08:00:00.000Z"'))(
        "codex", "codex-personal"
    )
    assert material.expires_at == datetime(2026, 10, 3, 8, 0, tzinfo=timezone.utc)


@pytest.mark.skipif(shutil.which("node") is None, reason="node is required for the plugin bridge")
def test_plugin_bridge_null_expiry_means_no_expiry(plugins_root: Path):
    from sdf_core.plugin_bridge import PluginConnectionBridge

    assert PluginConnectionBridge(plugins_root)("codex", "codex-personal").expires_at is None


@pytest.mark.skipif(shutil.which("node") is None, reason="node is required for the plugin bridge")
@pytest.mark.parametrize("expires_at", ['"not-a-date"', "12345", '"2026-10-03T08:00:00"'])
def test_plugin_bridge_rejects_unreadable_expiry(tmp_path: Path, expires_at: str):
    from sdf_core.plugin_bridge import PluginConnectionBridge

    with pytest.raises(CredentialConfigError, match="codex-personal") as error:
        PluginConnectionBridge(_plugin_with_expiry(tmp_path, expires_at))("codex", "codex-personal")
    _assert_no_secret(str(error.value))
