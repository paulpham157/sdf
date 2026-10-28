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
from datetime import datetime, timedelta, timezone
from enum import StrEnum
from pathlib import Path
from types import MappingProxyType
from urllib.parse import urlsplit

REPO_ROOT = Path(__file__).resolve().parents[1]

# A subscription credential must outlive the sandbox by at least this much.
EXPIRY_MARGIN = timedelta(minutes=10)


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
        # OpenCode reads its provider, key reference, models and permissions
        # from the seeded config file; these would replace, redirect or widen
        # that config (ADR-0011).
        "opencode": ("OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG_DIR", "OPENCODE_PERMISSION"),
        # Pi 0.73.1 reads its provider and key reference from the seeded
        # ~/.pi/agent/models.json; these would move that directory (and with it
        # models.json, settings and extensions) or redirect Pi's own fetches
        # (ADR-0012).  The turn wrapper also unsets every other PI_* variable.
        "pi": (
            "PI_CODING_AGENT_DIR",
            "PI_CODING_AGENT_SESSION_DIR",
            "PI_PACKAGE_DIR",
            "PI_SHARE_VIEWER_URL",
            "PI_OAUTH_CALLBACK_HOST",
        ),
    }
)

# Vultr Serverless Inference is the example/default OpenCode provider only.
DEFAULT_OPENCODE_BASE_URL = "https://api.vultrinference.com/v1"
# Same default endpoint for Pi (ADR-0012); overridable with SDF_PI_BASE_URL.
DEFAULT_PI_BASE_URL = DEFAULT_OPENCODE_BASE_URL


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
    # Seed that applies the base URL inside the box, for agents that do not
    # read ``base_url_target`` from the environment.
    base_url_seed: str | None = None
    # ``SDF_<AGENT>_API_KEY_ENV`` may name a different host variable that holds
    # the key (ADR-0011).  The key value itself only ever comes from the env.
    api_key_env_variable: str = ""
    # When true the sandbox variable has the same name as the (possibly
    # renamed) host source, because the agent reads it by a configured
    # reference (OpenCode ``{env:NAME}``) rather than a fixed provider name.
    api_key_target_follows_source: bool = False
    # Default base URL when the source is unset (None: provider default).
    default_base_url: str | None = None
    subscription_supported: bool = True
    # Key-variable name prefixes this agent's runtime clears before it runs
    # (Pi's turn wrapper unsets every PI_*), so a renamed key there would
    # never reach the agent.
    reserved_key_prefixes: tuple[str, ...] = ()


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
            api_key_env_variable="SDF_CLAUDE_API_KEY_ENV",
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
            # Codex 0.157.0 ignores OPENAI_BASE_URL and reads ``openai_base_url``
            # from ~/.codex/config.toml (docs/research/codex-base-url.md).
            base_url_seed="codex-config-base-url",
            api_key_env_variable="SDF_CODEX_API_KEY_ENV",
        ),
        # OpenCode talks to any OpenAI-compatible endpoint through a custom
        # provider in its config; there is no SDF subscription path for it.
        "opencode": _AgentSpec(
            mode_variable="SDF_CREDENTIAL_MODE_OPENCODE",
            connection_variable="SDF_CONNECTION_OPENCODE",
            api_key_source="SDF_OPENCODE_API_KEY",
            api_key_target="SDF_OPENCODE_API_KEY",
            # Writes ~/.config/opencode/opencode.json (provider, {env:NAME} key
            # reference, base URL, models); the seed is fed by the key's NAME.
            api_key_seed="opencode-config",
            base_url_source="SDF_OPENCODE_BASE_URL",
            base_url_target="SDF_OPENCODE_BASE_URL",
            subscription_seeds=MappingProxyType({}),
            api_key_env_variable="SDF_OPENCODE_API_KEY_ENV",
            api_key_target_follows_source=True,
            default_base_url=DEFAULT_OPENCODE_BASE_URL,
            subscription_supported=False,
        ),
        # Pi (ADR-0012): one OpenAI-compatible custom provider in the seeded
        # ~/.pi/agent/models.json whose ``apiKey`` is the key variable's NAME.
        "pi": _AgentSpec(
            mode_variable="SDF_CREDENTIAL_MODE_PI",
            connection_variable="SDF_CONNECTION_PI",
            api_key_source="SDF_PI_API_KEY",
            api_key_target="SDF_PI_API_KEY",
            api_key_seed="pi-models",
            base_url_source="SDF_PI_BASE_URL",
            base_url_target="SDF_PI_BASE_URL",
            subscription_seeds=MappingProxyType({}),
            api_key_env_variable="SDF_PI_API_KEY_ENV",
            api_key_target_follows_source=True,
            default_base_url=DEFAULT_PI_BASE_URL,
            subscription_supported=False,
            reserved_key_prefixes=("PI_",),
        ),
    }
)

AGENT_KINDS: tuple[str, ...] = tuple(_AGENTS)


@dataclass(frozen=True, slots=True)
class SeedStep:
    """One in-sandbox seed action, fed from a sandbox variable by name."""

    action: str
    variable: str


@dataclass(frozen=True, slots=True)
class ConnectionMaterial:
    """Sandbox variables a subscription connection provides, and when they expire."""

    variables: Mapping[str, str]
    expires_at: datetime | None = None

    def __repr__(self) -> str:
        expires = self.expires_at.isoformat() if self.expires_at is not None else None
        return f"ConnectionMaterial(variables={_redacted(self.variables)}, expires_at={expires!r})"


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
    sandbox_timeout_seconds: float = 0,
    now: datetime | None = None,
) -> CredentialPlan:
    """Resolve the Agent Credential plan for ``agent`` from ``environ``.

    Only ``SDF_*`` variables are read, plus the one key variable an operator
    explicitly names in ``SDF_<AGENT>_API_KEY_ENV`` (ADR-0011); the shell's
    provider variables (``ANTHROPIC_API_KEY``, ``OPENAI_BASE_URL``, ...) are
    ignored and can never be named.  There is no default mode and no fallback
    from one mode to the other, nor from a named key variable to the default.  A
    subscription connection must expire later than ``now`` plus
    ``sandbox_timeout_seconds`` plus :data:`EXPIRY_MARGIN`.
    """
    spec = _AGENTS.get(agent)
    if spec is None:
        raise CredentialConfigError(f"unknown agent kind {agent!r}; expected one of: {', '.join(_AGENTS)}")
    mode = _mode(spec, environ)
    if mode is CredentialMode.SUBSCRIPTION and not spec.subscription_supported:
        raise CredentialConfigError(
            f"{spec.mode_variable}=subscription is not supported for {agent}; set it to api-key (there is no fallback)"
        )
    if mode is CredentialMode.API_KEY:
        source = _api_key_source(spec, environ)
        key = environ.get(source, "").strip()
        if not key:
            raise CredentialConfigError(
                f"{spec.mode_variable}=api-key requires {source} to be set"
            )
        target = source if spec.api_key_target_follows_source else spec.api_key_target
        variables = {target: key}
        seeds = [SeedStep(spec.api_key_seed, target)]
        base_url = environ.get(spec.base_url_source, "").strip() or (spec.default_base_url or "")
        if base_url:
            _check_base_url(spec.base_url_source, base_url)
            variables[spec.base_url_target] = base_url
            if spec.base_url_seed is not None:
                seeds.append(SeedStep(spec.base_url_seed, spec.base_url_target))
        if agent in ("opencode", "pi"):
            # Lazy: agent_models imports CredentialConfigError from here.
            from .agent_models import MODEL_VARIABLES, SMALL_MODEL_VARIABLES, resolve_agent_models

            selection = resolve_agent_models(agent, environ)
            assert selection.model is not None
            variables[MODEL_VARIABLES[agent]] = selection.model
            if agent in SMALL_MODEL_VARIABLES:
                assert selection.small_model is not None
                variables[SMALL_MODEL_VARIABLES[agent]] = selection.small_model
        return _plan(agent, mode, None, variables, tuple(seeds))

    if spec.api_key_env_variable and environ.get(spec.api_key_env_variable, "").strip():
        raise CredentialConfigError(
            f"{spec.api_key_env_variable} is only allowed with {spec.mode_variable}=api-key, not subscription"
        )
    if environ.get(spec.base_url_source, "").strip():
        # Not echoed: operators sometimes paste credentials into URLs.
        raise CredentialConfigError(
            f"{spec.base_url_source} is only allowed with {spec.mode_variable}=api-key, not subscription"
        )
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
    _check_expiry(agent, connection_id, material.expires_at, sandbox_timeout_seconds, now)
    seeds = tuple(
        SeedStep(action, variable)
        for variable, action in spec.subscription_seeds.items()
        if variable in material.variables
    )
    return _plan(agent, mode, connection_id, material.variables, seeds)


# A renamed key variable must look like a key, so it can never select (and, for
# OpenCode, overwrite in the sandbox) PATH, HOME or similar.
_KEY_VARIABLE = re.compile(r"[A-Z][A-Z0-9_]*_API_KEY")
# Sandbox-provider and SDF control variables are never Agent Credentials.
_RESERVED_KEY_PREFIXES = ("E2B_", "DAYTONA_", "HERDR_", "SDF_CREDENTIAL_MODE_", "SDF_CONNECTION_")


def _api_key_source(spec: _AgentSpec, environ: Mapping[str, str]) -> str:
    """Return the one host variable that holds the key: renamed or default."""

    if not spec.api_key_env_variable:
        return spec.api_key_source
    name = environ.get(spec.api_key_env_variable, "").strip()
    if not name:
        return spec.api_key_source
    # A variable NAME is not secret, but the check still names only the knob in
    # case a key was pasted into it.
    if not _KEY_VARIABLE.fullmatch(name):
        raise CredentialConfigError(
            f"{spec.api_key_env_variable} must name an environment variable ending in _API_KEY"
        )
    provider_names = {n for names in CONFLICTING_VARIABLES.values() for n in names}
    if name in provider_names:
        raise CredentialConfigError(
            f"{spec.api_key_env_variable} names a shell provider variable, which SDF always ignores (ADR-0007); "
            "use a dedicated variable"
        )
    if name.startswith(_RESERVED_KEY_PREFIXES + spec.reserved_key_prefixes):
        raise CredentialConfigError(
            f"{spec.api_key_env_variable} names a sandbox-provider or SDF control variable, not an Agent Credential"
        )
    return name


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


def _check_expiry(
    agent: str,
    connection_id: str,
    expires_at: datetime | None,
    sandbox_timeout_seconds: float,
    now: datetime | None,
) -> None:
    if expires_at is None:
        return
    reconnect = f"run: e2b-box auth connect {agent}"
    if expires_at.tzinfo is None:
        raise CredentialConfigError(f"connection {connection_id!r} reports an expiry without a timezone; {reconnect}")
    deadline = (now or datetime.now(timezone.utc)) + timedelta(seconds=sandbox_timeout_seconds) + EXPIRY_MARGIN
    if expires_at <= deadline:
        raise CredentialConfigError(
            f"connection {connection_id!r} for {agent} expires at {expires_at.isoformat()}, not later than "
            f"now + sandbox timeout ({sandbox_timeout_seconds:g}s) + {EXPIRY_MARGIN}; {reconnect}"
        )


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
