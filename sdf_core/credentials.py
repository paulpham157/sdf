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
from collections.abc import Callable, Iterable, Mapping, MutableMapping
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
        # Pi reads its provider and key reference from the seeded
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
        # Qwen Code and Goose (ADR-0013) get their endpoint, model and
        # key per turn from the headless wrapper, which unsets every variable
        # with the agent's own prefix (and OPENAI_*) first.  These are the ones
        # that would move the agent's config or widen its permissions; the
        # shared OPENAI_* names stay with codex so the kinds can share a box.
        "qwen": (
            "QWEN_CODE_SYSTEM_SETTINGS_PATH",
            "QWEN_CODE_SYSTEM_DEFAULTS_PATH",
            "QWEN_HOME",
            "QWEN_RUNTIME_DIR",
            "QWEN_SANDBOX",
        ),
        "goose": (
            "GOOSE_PROVIDER",
            "GOOSE_MODEL",
            "GOOSE_MODE",
            "GOOSE_PATH_ROOT",
            "GOOSE_ADDITIONAL_CONFIG_FILES",
        ),
    }
)

# Official endpoints used when Claude or Codex leave their base URL unset.
# Codex speaks the OpenAI Responses API at this host; Claude speaks Messages.
# Headless OpenAI-compatible kinds have no default host (ADR 0034).
OFFICIAL_OPENAI_BASE_URL = "https://api.openai.com/v1"
OFFICIAL_ANTHROPIC_BASE_URL = "https://api.anthropic.com"
LLM_PROTOCOLS = ("openai", "anthropic")


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
    # Egress and provenance when ``base_url_source`` is unset. None means the
    # kind has no default host.
    official_base_url: str | None = None
    # api-key mode fails closed when ``base_url_source`` is unset.
    base_url_required: bool = False
    subscription_supported: bool = True
    # ADR 0034: exactly one LLM transport. ``responses_api`` is a constraint
    # inside ``openai`` (Codex requires Responses, not Chat Completions only).
    protocol: str = "openai"
    responses_api: bool = False


_AGENTS: Mapping[str, _AgentSpec] = MappingProxyType(
    {
        "claude": _AgentSpec(
            mode_variable="SDF_CREDENTIAL_MODE_CLAUDE",
            connection_variable="SDF_CONNECTION_CLAUDE",
            api_key_source="ANTHROPIC_API_KEY",
            api_key_target="ANTHROPIC_API_KEY",
            api_key_seed="claude-approve-api-key",
            base_url_source="ANTHROPIC_BASE_URL",
            base_url_target="ANTHROPIC_BASE_URL",
            subscription_seeds=MappingProxyType({}),
            official_base_url=OFFICIAL_ANTHROPIC_BASE_URL,
            protocol="anthropic",
        ),
        "codex": _AgentSpec(
            mode_variable="SDF_CREDENTIAL_MODE_CODEX",
            connection_variable="SDF_CONNECTION_CODEX",
            api_key_source="OPENAI_API_KEY",
            api_key_target="OPENAI_API_KEY",
            api_key_seed="codex-auth-json-api-key",
            base_url_source="OPENAI_BASE_URL",
            base_url_target="OPENAI_BASE_URL",
            subscription_seeds=MappingProxyType({"CODEX_AUTH_JSON": "codex-auth-json"}),
            # Codex (since 0.157, still 0.165) ignores OPENAI_BASE_URL and reads ``openai_base_url``
            # from ~/.codex/config.toml (docs/research/codex-base-url.md). The host must
            # implement the OpenAI Responses API (POST /v1/responses), not Chat Completions only.
            base_url_seed="codex-config-base-url",
            official_base_url=OFFICIAL_OPENAI_BASE_URL,
            responses_api=True,
        ),
        # OpenCode talks to any OpenAI-compatible endpoint through a custom
        # provider in its config; there is no SDF subscription path for it.
        "opencode": _AgentSpec(
            mode_variable="SDF_CREDENTIAL_MODE_OPENCODE",
            connection_variable="SDF_CONNECTION_OPENCODE",
            api_key_source="OPENCODE_API_KEY",
            api_key_target="OPENCODE_API_KEY",
            # Writes ~/.config/opencode/opencode.json (provider, {env:NAME} key
            # reference, base URL, models); the seed is fed by the key's NAME.
            api_key_seed="opencode-config",
            base_url_source="OPENCODE_BASE_URL",
            base_url_target="OPENCODE_BASE_URL",
            subscription_seeds=MappingProxyType({}),
            base_url_required=True,
            subscription_supported=False,
        ),
        # Pi (ADR-0012): one OpenAI-compatible custom provider in the seeded
        # ~/.pi/agent/models.json whose ``apiKey`` is ``$PI_API_KEY``.
        "pi": _AgentSpec(
            mode_variable="SDF_CREDENTIAL_MODE_PI",
            connection_variable="SDF_CONNECTION_PI",
            api_key_source="PI_API_KEY",
            api_key_target="PI_API_KEY",
            api_key_seed="pi-models",
            base_url_source="PI_BASE_URL",
            base_url_target="PI_BASE_URL",
            subscription_seeds=MappingProxyType({}),
            base_url_required=True,
            subscription_supported=False,
        ),
        # Qwen Code (ADR-0013): ``--bare --auth-type openai``; the seed records the
        # key variable's NAME and the wrapper exports it as OPENAI_API_KEY per turn.
        "qwen": _AgentSpec(
            mode_variable="SDF_CREDENTIAL_MODE_QWEN",
            connection_variable="SDF_CONNECTION_QWEN",
            api_key_source="QWEN_API_KEY",
            api_key_target="QWEN_API_KEY",
            api_key_seed="qwen-key-ref",
            base_url_source="QWEN_BASE_URL",
            base_url_target="QWEN_BASE_URL",
            subscription_seeds=MappingProxyType({}),
            base_url_required=True,
            subscription_supported=False,
        ),
        # Goose (ADR-0013): one declarative custom provider (engine openai) in the
        # seeded ~/.config/goose/custom_providers/sdf_openai.json, ``api_key_env`` = NAME.
        "goose": _AgentSpec(
            mode_variable="SDF_CREDENTIAL_MODE_GOOSE",
            connection_variable="SDF_CONNECTION_GOOSE",
            api_key_source="GOOSE_API_KEY",
            api_key_target="GOOSE_API_KEY",
            api_key_seed="goose-provider",
            base_url_source="GOOSE_BASE_URL",
            base_url_target="GOOSE_BASE_URL",
            subscription_seeds=MappingProxyType({}),
            base_url_required=True,
            subscription_supported=False,
        ),
    }
)

AGENT_KINDS: tuple[str, ...] = tuple(_AGENTS)
# Kinds whose plan carries the resolved model as a sandbox variable.
_MODEL_SEEDED_AGENTS = frozenset({"opencode", "pi", "qwen", "goose"})


def protocol_of(agent: str) -> str:
    """The one LLM transport family for ``agent`` (ADR 0034): ``openai`` or ``anthropic``."""

    return _spec(agent).protocol


def responses_api_required(agent: str) -> bool:
    """True when ``agent`` requires the OpenAI Responses API, not Chat Completions only."""

    return _spec(agent).responses_api


def resolved_llm_base_url(agent: str, environ: Mapping[str, str]) -> str:
    """Base URL the kind will call: the allow-listed variable, or the official API.

    Does not validate the URL (callers that inject it still use ``_check_base_url``).
    Unset Claude and Codex URLs are the official APIs. Headless kinds
    (opencode, pi, qwen, goose) have no default; an unset URL is ``\"\"``.
    """

    spec = _spec(agent)
    explicit = environ.get(spec.base_url_source, "").strip()
    if explicit:
        return explicit
    return spec.official_base_url or ""


def endpoint_host(url: str) -> str:
    """Hostname of a base URL, or ``\"\"`` when it has none. Never echoes the URL."""

    try:
        return (urlsplit(url).hostname or "").rstrip(".").lower()
    except ValueError:
        return ""


def llm_attempt_provider(agent: str, environ: Mapping[str, str]) -> str:
    """Provenance label for ``attempts.provider``: ``{protocol}:{host}``.

    The host comes from the configured base URL, else the kind's official
    default. A headless kind with no base URL is ``unspecified``. It is never
    a vendor name such as ``vultr``.
    """

    host = endpoint_host(resolved_llm_base_url(agent, environ)) or "unspecified"
    return f"{protocol_of(agent)}:{host}"[:120]


def inference_allow_hosts(environ: Mapping[str, str], kinds: Iterable[str]) -> list[str]:
    """Egress hosts for the given kinds' resolved LLM endpoints, in order."""

    hosts: list[str] = []
    for kind in kinds:
        host = endpoint_host(resolved_llm_base_url(kind, environ))
        if host and host not in hosts:
            hosts.append(host)
    return hosts


def inference_network(environ: Mapping[str, str], kinds: Iterable[str]) -> dict[str, object]:
    """E2B network lock: the resolved LLM host(s), everything else denied."""

    return {
        "allow_out": inference_allow_hosts(environ, kinds),
        "deny_out": ["0.0.0.0/0"],
        "allow_public_traffic": False,
    }


def _spec(agent: str) -> _AgentSpec:
    spec = _AGENTS.get(agent)
    if spec is None:
        raise CredentialConfigError(f"unknown agent kind {agent!r}; expected one of: {', '.join(_AGENTS)}")
    return spec


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

    Credential Mode is ``SDF_CREDENTIAL_MODE_<AGENT>``. In api-key mode the
    key, base URL and model are the allow-listed names for that kind
    (``ANTHROPIC_*``, ``OPENAI_*``, ``PI_*``, ``QWEN_*``, ``OPENCODE_*``,
    ``GOOSE_*``). There is no alternate name and no vendor-key fallback.
    There is no default mode and no fallback from one mode to the other. A
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
        source = spec.api_key_source
        key = environ.get(source, "").strip()
        if not key:
            raise CredentialConfigError(
                f"{spec.mode_variable}=api-key requires {source} to be set"
            )
        target = spec.api_key_target
        variables = {target: key}
        seeds = [SeedStep(spec.api_key_seed, target)]
        base_url = environ.get(spec.base_url_source, "").strip()
        if spec.base_url_required and not base_url:
            raise CredentialConfigError(
                f"{spec.mode_variable}=api-key requires {spec.base_url_source} to be set"
            )
        if base_url:
            _check_base_url(spec.base_url_source, base_url)
            variables[spec.base_url_target] = base_url
            if spec.base_url_seed is not None:
                seeds.append(SeedStep(spec.base_url_seed, spec.base_url_target))
        if agent in _MODEL_SEEDED_AGENTS:
            # Lazy: agent_models imports CredentialConfigError from here.
            from .agent_models import MODEL_VARIABLES, resolve_agent_models

            selection = resolve_agent_models(agent, environ)
            assert selection.model is not None
            variables[MODEL_VARIABLES[agent]] = selection.model
        return _plan(agent, mode, None, variables, tuple(seeds))

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


def _live_gate_opted_in() -> bool:
    """True when a live test has set its own ``SDF_LIVE_*=1`` flag."""

    return any(name.startswith("SDF_LIVE_") and value == "1" for name, value in os.environ.items())


def _hermetic_skips_dotenv(dotenv: Path) -> bool:
    """The hermetic suite does not read the repo-root ``.env``.

    A test that points ``REPO_ROOT`` at a fixture directory still loads that
    file: that is how the server's lifespan is tested. A live gate
    (``SDF_LIVE_*=1``) still reads the real file.
    """

    if os.environ.get("SDF_TEST_HERMETIC") != "1" or _live_gate_opted_in():
        return False
    repo_dotenv = Path(__file__).resolve().parents[1] / ".env"
    try:
        return dotenv.resolve() == repo_dotenv.resolve()
    except OSError:
        return False


def load_dotenv(
    path: Path | str | None = None,
    environ: MutableMapping[str, str] | None = None,
) -> tuple[str, ...]:
    """Load ``KEY=value`` lines from the repo-root ``.env`` into ``environ``.

    Variables already present in ``environ`` (even empty) win.  A missing
    file is a no-op.  Returns the names that were loaded, never the values.

    The hermetic suite sets ``SDF_TEST_HERMETIC=1`` and then does not read the
    repo-root file, so a developer ``.env`` cannot steer those tests. An
    explicit ``path``, or a ``REPO_ROOT`` pointed at a fixture, is still
    loaded. A live gate (``SDF_LIVE_*=1``) still loads the default file;
    those tests opt in and take keys from ``.env``.
    """
    target = os.environ if environ is None else environ
    dotenv = Path(path) if path is not None else REPO_ROOT / ".env"
    if path is None and _hermetic_skips_dotenv(dotenv):
        return ()
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
