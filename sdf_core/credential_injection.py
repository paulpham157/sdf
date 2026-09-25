"""Create-time Agent Credential injection for the persistent runtime (ADR-0007).

Credential plans for every agent kind the runtime will start are resolved
*before* the sandbox exists, so a configuration error never provisions one.
Plan variables become sandbox-wide ``envs`` at ``Sandbox.create``; conflicting
provider names are stripped; each plan's seed steps run once, right after
creation and before any agent starts.  Seed commands name variables only and
let the sandbox's own environment supply the value, so no secret appears in a
command line, a Herdr pane or Evidence.
"""

from __future__ import annotations

import inspect
import shlex
from collections.abc import Iterable, Mapping
from dataclasses import dataclass, field
from types import MappingProxyType
from typing import Any

from .credentials import CONFLICTING_VARIABLES, ConnectionReader, CredentialConfigError, CredentialPlan, SeedStep, resolve_credential_plan
from .e2b_herdr_transport import E2BHerdrTransport
from .herdr_runtime import HerdrRuntimeError
from .plugin_bridge import PluginConnectionBridge
from .runtime import CredentialMetadata

# Seed scripts read their credential from ``process.env`` inside the box; the
# host only ever sends the variable NAME.  Existing state is merged, never
# clobbered, mirroring the herdr-e2b plugin's fleet seeds.
_CLAUDE_APPROVE_KEY = (
    'const fs=require("fs"),f=process.env.HOME+"/.claude.json",v=process.argv[1];'
    'const k=(process.env[v]||"").slice(-20);if(!k){process.exit(0)}'
    'let c={};try{c=JSON.parse(fs.readFileSync(f,"utf8"))}catch{}'
    'c.hasCompletedOnboarding=true;c.customApiKeyResponses=c.customApiKeyResponses||{};'
    'const a=c.customApiKeyResponses.approved||[];if(!a.includes(k))a.push(k);'
    'c.customApiKeyResponses.approved=a;c.customApiKeyResponses.rejected=c.customApiKeyResponses.rejected||[];'
    'fs.writeFileSync(f,JSON.stringify(c),{mode:0o600})'
)
_CODEX_API_KEY_AUTH = (
    'const fs=require("fs"),d=process.env.HOME+"/.codex",f=d+"/auth.json",v=process.argv[1];'
    'const k=process.env[v]||"";if(!k){process.exit(0)}fs.mkdirSync(d,{recursive:true});'
    'try{if(fs.statSync(f).size>0)process.exit(0)}catch{}'
    'fs.writeFileSync(f,JSON.stringify({auth_mode:"apikey",OPENAI_API_KEY:k}),{mode:0o600})'
)
_CODEX_SESSION_AUTH = (
    'const fs=require("fs"),d=process.env.HOME+"/.codex",f=d+"/auth.json",v=process.argv[1];'
    'const s=process.env[v]||"";if(!s){process.exit(0)}fs.mkdirSync(d,{recursive:true});'
    'try{if(fs.statSync(f).size>0)process.exit(0)}catch{}'
    'fs.writeFileSync(f,s,{mode:0o600})'
)
_SEED_SCRIPTS: Mapping[str, str] = MappingProxyType(
    {
        "claude-approve-api-key": _CLAUDE_APPROVE_KEY,
        "codex-auth-json-api-key": _CODEX_API_KEY_AUTH,
        "codex-auth-json": _CODEX_SESSION_AUTH,
    }
)

_CREDENTIAL_NAMES = frozenset(name for names in CONFLICTING_VARIABLES.values() for name in names)

def seed_command(step: SeedStep) -> str:
    """Render one seed step as a sandbox command carrying the variable name only."""

    script = _SEED_SCRIPTS.get(step.action)
    if script is None:
        raise CredentialConfigError(f"unknown credential seed action {step.action!r}")
    # Reference the variable once as ``$NAME`` too, so a reader of the command
    # line can see which sandbox variable feeds it; node receives the name.
    return f"if [ -n \"${step.variable}\" ]; then node -e {shlex.quote(script)} {shlex.quote(step.variable)}; fi"

@dataclass(frozen=True, slots=True)
class CredentialInjection:
    """Resolved create-time injection: sandbox envs, strips, seeds, metadata."""

    envs: Mapping[str, str]
    strip_variables: tuple[str, ...]
    seed_commands: tuple[str, ...]
    metadata: Mapping[str, CredentialMetadata] = field(default_factory=dict)

    def __post_init__(self) -> None:
        object.__setattr__(self, "envs", MappingProxyType(dict(self.envs)))
        object.__setattr__(self, "metadata", MappingProxyType(dict(self.metadata)))
        for name, value in self.envs.items():
            if name in _CREDENTIAL_NAMES and value and any(value in command for command in self.seed_commands):
                raise CredentialConfigError(f"a credential seed command carries the value of {name}; seeds may name it only")

    def __repr__(self) -> str:
        envs = "{" + ", ".join(f"{name!r}: '<REDACTED>'" for name in self.envs) + "}"
        return (
            f"CredentialInjection(envs={envs}, strip_variables={self.strip_variables!r}, "
            f"seed_commands=<{len(self.seed_commands)} by name>, metadata={dict(self.metadata)!r})"
        )

    __str__ = __repr__

def prepare_credential_injection(
    agents: Iterable[str],
    environ: Mapping[str, str],
    *,
    read_connection: ConnectionReader | None = None,
    base_envs: Mapping[str, str] | None = None,
    sandbox_timeout_seconds: float = 0,
) -> CredentialInjection:
    """Resolve every agent kind's plan; raise before anything is provisioned.

    Subscription connections are read through the installed herdr-e2b plugin
    unless ``read_connection`` is given; the bridge is only built when an
    agent actually needs it.
    """

    reader = read_connection or _lazy_plugin_bridge(environ)
    plans: dict[str, CredentialPlan] = {}
    for agent in agents:
        if agent not in plans:
            plans[agent] = resolve_credential_plan(
                agent, environ, read_connection=reader, sandbox_timeout_seconds=sandbox_timeout_seconds
            )
    if not plans:
        raise CredentialConfigError("no agent kinds to credential; name the agents the runtime will start")

    credential_envs: dict[str, str] = {}
    for plan in plans.values():
        credential_envs.update(plan.set_variables)
    strip = tuple(dict.fromkeys(name for plan in plans.values() for name in plan.strip_variables))
    clash = sorted(set(strip) & set(credential_envs))
    if clash:
        raise CredentialConfigError(f"agent credentials conflict in one sandbox: {', '.join(clash)}")

    envs = {name: value for name, value in (base_envs or {}).items() if name not in strip}
    envs.update(credential_envs)
    seeds = tuple(seed_command(step) for plan in plans.values() for step in plan.seed_steps)
    metadata = {
        agent: CredentialMetadata(plan.mode.value, plan.connection_id) for agent, plan in plans.items()
    }
    return CredentialInjection(envs, strip, seeds, metadata)

def _lazy_plugin_bridge(environ: Mapping[str, str]) -> ConnectionReader:
    bridge: list[ConnectionReader] = []

    def read(agent: str, connection_id: str) -> Any:
        if not bridge:
            bridge.append(PluginConnectionBridge(environ=environ))
        return bridge[0](agent, connection_id)

    return read


class CredentialedE2BHerdrTransport(E2BHerdrTransport):
    """E2B Herdr transport that seeds agent credentials once after creation."""

    def __init__(self, *, injection: CredentialInjection, **kwargs: Any) -> None:
        super().__init__(envs=injection.envs, **kwargs)
        self.credential_metadata = injection.metadata
        self._seed_commands = injection.seed_commands

    def _ensure_sandbox(self) -> Any:
        fresh = self._sandbox is None and self._sandbox_id is None
        sandbox = super()._ensure_sandbox()
        if fresh and self._seed_commands:
            self._seed()
        return sandbox

    def _seed(self) -> None:
        timeout_ms = self.timeout_seconds * 1000
        for command in self._seed_commands:
            try:
                self._exec(command, timeout_ms)
            except HerdrRuntimeError:
                # Command output is not echoed: a seed handles credential state.
                self._kill_after_timeout()
                raise HerdrRuntimeError("credential seed step failed; sandbox killed") from None

def create_credentialed_transport(
    *,
    template: str,
    agents: Iterable[str],
    environ: Mapping[str, str],
    read_connection: ConnectionReader | None = None,
    envs: Mapping[str, str] | None = None,
    **transport_kwargs: Any,
) -> tuple[CredentialedE2BHerdrTransport, CredentialInjection]:
    """Resolve credentials for ``agents``, then build the persistent transport.

    The sandbox itself is created lazily on the first command; a credential
    configuration error surfaces here, so no sandbox is ever created for it.
    """

    # The connection must outlive the box, so check against its real lifetime.
    timeout_seconds = transport_kwargs.get(
        "timeout_seconds", inspect.signature(E2BHerdrTransport).parameters["timeout_seconds"].default
    )
    injection = prepare_credential_injection(
        agents, environ, read_connection=read_connection, base_envs=envs, sandbox_timeout_seconds=timeout_seconds
    )
    transport = CredentialedE2BHerdrTransport(
        injection=injection, template=template, environ=environ, **transport_kwargs
    )
    return transport, injection
