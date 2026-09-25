"""Agent Credential resolution (ADR-0007).

A pure policy module: an agent kind plus an environment mapping becomes a
:class:`CredentialPlan` naming the Credential Mode, the connection id, the
sandbox variables to set, the variable names to strip and the seed steps (by
variable name only).  Nothing here touches the network, E2B or disk, except
:func:`load_dotenv`, which reads the operator's repo-root ``.env``.

Error messages name variables and connection ids, never values.
"""

from __future__ import annotations

import ipaddress
import os
import re
import socket
from collections.abc import Callable, Mapping, MutableMapping
from dataclasses import dataclass, field
from enum import StrEnum
from pathlib import Path
from types import MappingProxyType
from urllib.parse import urlsplit

REPO_ROOT = Path(__file__).resolve().parents[1]


class CredentialMode(StrEnum):
    """How an Agent Credential is obtained for one agent kind."""

    SUBSCRIPTION = "subscription"
    API_KEY = "api-key"


class CredentialConfigError(ValueError):
    """Credential configuration is missing or invalid.

    Messages name the variable or connection at fault, never its value.
    """


# Provider variables that must not coexist in a sandbox with the agent's
# chosen credential; mirrors the herdr-e2b plugin's CONFLICTING_AUTH.
CONFLICTING_VARIABLES: Mapping[str, tuple[str, ...]] = MappingProxyType(
    {
        "claude": (
            "ANTHROPIC_API_KEY",
            "ANTHROPIC_AUTH_TOKEN",
            "CLAUDE_CODE_OAUTH_TOKEN",
            "CLAUDE_CODE_OAUTH_REFRESH_TOKEN",
            "ANTHROPIC_BASE_URL",
            "CLAUDE_CODE_USE_BEDROCK",
            "CLAUDE_CODE_USE_VERTEX",
            "CLAUDE_CODE_USE_FOUNDRY",
            "ANTHROPIC_PROFILE",
            "ANTHROPIC_FEDERATION_RULE_ID",
            "ANTHROPIC_ORGANIZATION_ID",
        ),
        "codex": ("OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_AUTH_JSON", "OPENAI_BASE_URL"),
    }
)


@dataclass(frozen=True, slots=True)
class _AgentSpec:
    mode_variable: str
    connection_variable: str
    api_key_source: str
    api_key_target: str
    api_key_seed: str
    base_url_source: str
    base_url_target: str
    subscription_seeds: Mapping[str, str]


_AGENTS: Mapping[str, _AgentSpec] = MappingProxyType(
    {
        "claude": _AgentSpec(
            mode_variable="SDF_CREDENTIAL_MODE_CLAUDE",
            connection_variable="SDF_CONNECTION_CLAUDE",
            api_key_source="SDF_ANTHROPIC_API_KEY",
            api_key_target="ANTHROPIC_API_KEY",
            api_key_seed="claude-approve-api-key",
            base_url_source="SDF_ANTHROPIC_BASE_URL",
            base_url_target="ANTHROPIC_BASE_URL",
            subscription_seeds=MappingProxyType({}),
        ),
        "codex": _AgentSpec(
            mode_variable="SDF_CREDENTIAL_MODE_CODEX",
            connection_variable="SDF_CONNECTION_CODEX",
            api_key_source="SDF_OPENAI_API_KEY",
            api_key_target="OPENAI_API_KEY",
            api_key_seed="codex-auth-json-api-key",
            base_url_source="SDF_OPENAI_BASE_URL",
            base_url_target="OPENAI_BASE_URL",
            subscription_seeds=MappingProxyType({"CODEX_AUTH_JSON": "codex-auth-json"}),
        ),
    }
)


@dataclass(frozen=True, slots=True)
class SeedStep:
    """One in-sandbox seed action, fed from a sandbox variable by name."""

    action: str
    variable: str


@dataclass(frozen=True, slots=True)
class ConnectionMaterial:
    """Sandbox variables a subscription connection provides."""

    variables: Mapping[str, str]

    def __repr__(self) -> str:
        return f"ConnectionMaterial(variables={_redacted(self.variables)})"


ConnectionReader = Callable[[str, str], ConnectionMaterial]


@dataclass(frozen=True, slots=True)
class CredentialPlan:
    """The secret-bearing outcome of resolution; its repr never shows values."""

    agent: str
    mode: CredentialMode
    connection_id: str | None
    set_variables: Mapping[str, str]
    strip_variables: tuple[str, ...]
    seed_steps: tuple[SeedStep, ...] = field(default=())

    def __post_init__(self) -> None:
        object.__setattr__(self, "set_variables", MappingProxyType(dict(self.set_variables)))

    def __repr__(self) -> str:
        return (
            f"CredentialPlan(agent={self.agent!r}, mode={self.mode.value!r}, "
            f"connection_id={self.connection_id!r}, set_variables={_redacted(self.set_variables)}, "
            f"strip_variables={self.strip_variables!r}, seed_steps={self.seed_steps!r})"
        )

    __str__ = __repr__


def resolve_credential_plan(
    agent: str,
    environ: Mapping[str, str],
    *,
    read_connection: ConnectionReader | None = None,
) -> CredentialPlan:
    """Resolve the Agent Credential plan for ``agent`` from ``environ``.

    Only ``SDF_*`` variables are read; the shell's provider variables
    (``ANTHROPIC_API_KEY``, ``OPENAI_BASE_URL``, ...) are ignored.  There is
    no default mode and no fallback from one mode to the other.
    """
    spec = _AGENTS.get(agent)
    if spec is None:
        raise CredentialConfigError(f"unknown agent kind {agent!r}; expected one of: {', '.join(_AGENTS)}")
    mode = _mode(spec, environ)
    if mode is CredentialMode.API_KEY:
        key = environ.get(spec.api_key_source, "").strip()
        if not key:
            raise CredentialConfigError(
                f"{spec.mode_variable}=api-key requires {spec.api_key_source} to be set"
            )
        variables = {spec.api_key_target: key}
        base_url = environ.get(spec.base_url_source, "").strip()
        if base_url:
            _check_base_url(spec.base_url_source, base_url)
            variables[spec.base_url_target] = base_url
        seeds = (SeedStep(spec.api_key_seed, spec.api_key_target),)
        return _plan(agent, mode, None, variables, seeds)

    connection_id = environ.get(spec.connection_variable, "").strip()
    if not connection_id:
        raise CredentialConfigError(
            f"{spec.mode_variable}=subscription requires {spec.connection_variable} to name a herdr-e2b connection"
        )
    if read_connection is None:
        raise CredentialConfigError(
            f"no connection reader available to resolve subscription connection {connection_id!r} for {agent}"
        )
    material = read_connection(agent, connection_id)
    allowed = set(CONFLICTING_VARIABLES[agent])
    unexpected = sorted(set(material.variables) - allowed)
    if unexpected:
        raise CredentialConfigError(
            f"connection {connection_id!r} provides variables not valid for {agent}: {', '.join(unexpected)}"
        )
    if not any(value.strip() for value in material.variables.values()):
        raise CredentialConfigError(f"connection {connection_id!r} provides no credential for {agent}")
    seeds = tuple(
        SeedStep(action, variable)
        for variable, action in spec.subscription_seeds.items()
        if variable in material.variables
    )
    return _plan(agent, mode, connection_id, material.variables, seeds)


def load_dotenv(
    path: Path | str | None = None,
    environ: MutableMapping[str, str] | None = None,
) -> tuple[str, ...]:
    """Load ``KEY=value`` lines from the repo-root ``.env`` into ``environ``.

    Variables already present in ``environ`` (even empty) win.  A missing
    file is a no-op.  Returns the names that were loaded, never the values.
    """
    target = os.environ if environ is None else environ
    dotenv = Path(path) if path is not None else REPO_ROOT / ".env"
    if not dotenv.is_file():
        return ()
    loaded: list[str] = []
    for number, line in enumerate(dotenv.read_text(encoding="utf-8").splitlines(), start=1):
        entry = _parse_line(line, dotenv, number)
        if entry is None:
            continue
        name, value = entry
        if name in target:
            continue
        target[name] = value
        loaded.append(name)
    return tuple(loaded)


_LINE = re.compile(r"^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$")


def _parse_line(line: str, dotenv: Path, number: int) -> tuple[str, str] | None:
    stripped = line.strip()
    if not stripped or stripped.startswith("#"):
        return None
    match = _LINE.match(stripped)
    if match is None:
        return None
    name, raw = match.groups()
    if raw[:1] in {'"', "'"}:
        end = raw.find(raw[0], 1)
        rest = raw[end + 1 :].strip() if end > 0 else ""
        if end < 0 or (rest and not rest.startswith("#")):
            # Never echo the value: it is likely a secret.
            raise CredentialConfigError(f"{dotenv}:{number}: malformed quoted value for {name}")
        return name, raw[1:end]
    return name, re.split(r"\s+#", raw, maxsplit=1)[0].strip()


# Dotted numeric hosts (``127.1``, ``2130706433``, ``0x7f000001``) that the
# sandbox's resolver would read as legacy IPv4 forms.
_NUMERIC_HOST = re.compile(r"^(?:0x[0-9a-f]*|[0-9]+)(?:\.(?:0x[0-9a-f]*|[0-9]+)){0,3}$")


def _check_base_url(variable: str, url: str) -> None:
    """Reject base URLs a cloud sandbox cannot (safely) reach.

    Purely syntactic: hostnames are never resolved.  The URL is not echoed,
    since operators sometimes paste credentials into it.
    """
    unreachable = f"{variable} points at a loopback, link-local, private or unspecified address, which a cloud sandbox cannot reach"
    try:
        parts = urlsplit(url)
        parts.port  # noqa: B018 - raises ValueError for an out-of-range port
    except ValueError:
        raise CredentialConfigError(f"{variable} is not a valid URL") from None
    if parts.scheme.lower() != "https":
        raise CredentialConfigError(f"{variable} must use https")
    if parts.username is not None or parts.password is not None:
        raise CredentialConfigError(f"{variable} must not embed credentials")
    host = (parts.hostname or "").rstrip(".")
    if not host:
        raise CredentialConfigError(f"{variable} has no host")
    if host == "localhost" or host.endswith(".localhost"):
        raise CredentialConfigError(unreachable)
    address: ipaddress.IPv4Address | ipaddress.IPv6Address | None
    try:
        address = ipaddress.ip_address(host)
    except ValueError:
        address = None
        if _NUMERIC_HOST.match(host):
            try:
                address = ipaddress.IPv4Address(socket.inet_aton(host))
            except OSError:
                raise CredentialConfigError(f"{variable} has an invalid numeric host") from None
    if address is None:
        return
    if isinstance(address, ipaddress.IPv6Address) and address.ipv4_mapped is not None:
        address = address.ipv4_mapped
    if not address.is_global or address.is_multicast:
        raise CredentialConfigError(unreachable)


def _mode(spec: _AgentSpec, environ: Mapping[str, str]) -> CredentialMode:
    choices = " or ".join(mode.value for mode in CredentialMode)
    raw = environ.get(spec.mode_variable)
    if raw is None or not raw.strip():
        raise CredentialConfigError(f"{spec.mode_variable} is not set; declare {choices}")
    try:
        return CredentialMode(raw.strip())
    except ValueError:
        # The value is deliberately not echoed: it may be a misplaced secret.
        raise CredentialConfigError(f"{spec.mode_variable} has an unknown value; expected {choices}") from None


def _plan(
    agent: str,
    mode: CredentialMode,
    connection_id: str | None,
    variables: Mapping[str, str],
    seeds: tuple[SeedStep, ...],
) -> CredentialPlan:
    strip = tuple(name for name in CONFLICTING_VARIABLES[agent] if name not in variables)
    return CredentialPlan(agent, mode, connection_id, variables, strip, seeds)


def _redacted(variables: Mapping[str, str]) -> str:
    return "{" + ", ".join(f"{name!r}: '<REDACTED>'" for name in variables) + "}"
