"""Subscription connection material from the installed herdr-e2b plugin (ADR-0007).

The only SDF code that depends on herdr-e2b plugin internals.  A checked-in
Node helper imports the plugin's own ``src/connections.js`` and prints the
named connection's material as one JSON line; this module reads it through a
pipe with stdin closed and returns it as :class:`ConnectionMaterial`.  The
material is never written to disk or logged.

:class:`PluginConnectionBridge` matches ``ConnectionReader``, so it can be
passed to ``resolve_credential_plan(..., read_connection=...)``.

Error messages name paths and connection ids, never material.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
from datetime import datetime
from collections.abc import Mapping
from pathlib import Path

from sdf_core.credentials import ConnectionMaterial, CredentialConfigError

PLUGIN_GLOB = "github/e2b-dev.herdr-e2b-*"
CONNECTIONS_MODULE = Path("src") / "connections.js"
HELPER_PATH = Path(__file__).with_name("plugin_bridge_helper.mjs")

# Variables the plugin needs to find its config; nothing else from the host
# environment (provider keys, proxy base URLs) reaches the helper.
_CHILD_VARIABLES = ("HOME", "PATH", "USER", "XDG_CONFIG_HOME", "HERDR_PLUGIN_CONFIG_DIR")


def default_plugins_root(environ: Mapping[str, str] | None = None) -> Path:
    """Herdr's plugins directory: ``$XDG_CONFIG_HOME/herdr/plugins`` or ``~/.config/herdr/plugins``."""
    env = os.environ if environ is None else environ
    config_home = env.get("XDG_CONFIG_HOME", "").strip()
    if config_home:
        return Path(config_home) / "herdr" / "plugins"
    home = env.get("HOME", "").strip()
    return (Path(home) if home else Path.home()) / ".config" / "herdr" / "plugins"


def find_plugin(plugins_root: Path) -> Path:
    """The installed herdr-e2b plugin directory; the newest one if several."""
    candidates = [path for path in Path(plugins_root).glob(PLUGIN_GLOB) if (path / CONNECTIONS_MODULE).is_file()]
    if not candidates:
        raise CredentialConfigError(
            f"herdr-e2b plugin not found: searched {Path(plugins_root) / PLUGIN_GLOB} for {CONNECTIONS_MODULE}"
        )
    return max(candidates, key=lambda path: path.stat().st_mtime)


class PluginConnectionBridge:
    """Reads a named subscription connection through the plugin's own code."""

    def __init__(
        self,
        plugins_root: Path | None = None,
        *,
        node: str | None = None,
        environ: Mapping[str, str] | None = None,
        timeout: float = 30.0,
    ) -> None:
        self._environ = dict(os.environ if environ is None else environ)
        self._plugins_root = Path(plugins_root) if plugins_root is not None else default_plugins_root(self._environ)
        self._node = node
        self._timeout = timeout

    def __repr__(self) -> str:
        return f"PluginConnectionBridge(plugins_root={str(self._plugins_root)!r})"

    def __call__(self, agent: str, connection_id: str) -> ConnectionMaterial:
        plugin = find_plugin(self._plugins_root)
        node = self._node or shutil.which("node", path=self._environ.get("PATH"))
        if not node:
            raise CredentialConfigError("node is required to read herdr-e2b connections but was not found on PATH")
        try:
            completed = subprocess.run(
                [node, str(HELPER_PATH), str(plugin / CONNECTIONS_MODULE), connection_id],
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                env={name: self._environ[name] for name in _CHILD_VARIABLES if name in self._environ},
                timeout=self._timeout,
                check=False,
            )
        except FileNotFoundError:
            raise CredentialConfigError(f"node executable {node!r} could not be started") from None
        except subprocess.TimeoutExpired:
            raise CredentialConfigError(
                f"reading connection {connection_id!r} from the herdr-e2b plugin timed out"
            ) from None
        reply = _parse_reply(completed.stdout, connection_id)
        if not reply.get("ok"):
            if reply.get("error") == "unknown-connection":
                raise CredentialConfigError(
                    f"unknown herdr-e2b connection {connection_id!r}; run e2b-box auth list"
                )
            raise CredentialConfigError(
                f"the herdr-e2b plugin could not provide connection {connection_id!r}; "
                f"run e2b-box auth check {connection_id}"
            )
        harness = reply.get("harness")
        if harness != agent:
            raise CredentialConfigError(
                f"connection {connection_id!r} is for harness {harness!r}, not agent {agent!r}"
            )
        variables = reply.get("variables")
        if not isinstance(variables, dict) or not all(
            isinstance(name, str) and isinstance(value, str) for name, value in variables.items()
        ):
            raise CredentialConfigError(f"connection {connection_id!r} returned malformed material")
        return ConnectionMaterial(variables, expires_at=_expiry(reply.get("expiresAt"), connection_id))


def _expiry(raw: object, connection_id: str) -> datetime | None:
    """The plugin's ISO-8601 ``expiresAt``, or None when the connection never expires."""
    if raw is None:
        return None
    try:
        if not isinstance(raw, str):
            raise ValueError
        expires_at = datetime.fromisoformat(raw.replace("Z", "+00:00"))
    except ValueError:
        raise CredentialConfigError(f"connection {connection_id!r} returned an unreadable expiry") from None
    if expires_at.tzinfo is None:
        raise CredentialConfigError(f"connection {connection_id!r} returned an expiry without a timezone")
    return expires_at


def _parse_reply(stdout: bytes, connection_id: str) -> dict:
    # Never echo stdout: on success it carries the material.
    try:
        reply = json.loads(stdout.decode("utf-8"))
    except (UnicodeDecodeError, ValueError):
        reply = None
    if not isinstance(reply, dict):
        raise CredentialConfigError(
            f"the herdr-e2b plugin bridge returned unreadable output for connection {connection_id!r}"
        )
    return reply
