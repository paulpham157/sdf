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
import re
import shlex
from collections.abc import Iterable, Mapping
from dataclasses import dataclass, field
from types import MappingProxyType
from typing import Any

from .credentials import CONFLICTING_VARIABLES, ConnectionReader, CredentialConfigError, CredentialPlan, SeedStep, resolve_credential_plan
from .daytona_herdr_transport import DaytonaHerdrTransport
from .e2b_herdr_transport import E2BHerdrTransport
from .herdr_runtime import HerdrRuntimeError
from .plugin_bridge import PluginConnectionBridge
from .runtime import CredentialMetadata

SANDBOX_PROVIDERS = frozenset({"e2b", "daytona"})
DEFAULT_SANDBOX_PROVIDER = "e2b"

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
    # The template ships the file at 0644; ``mode`` applies only on creation.
    'fs.writeFileSync(f,JSON.stringify(c),{mode:0o600});fs.chmodSync(f,0o600)'
)
_CODEX_API_KEY_AUTH = (
    'const fs=require("fs"),d=process.env.HOME+"/.codex",f=d+"/auth.json",v=process.argv[1];'
    'const k=process.env[v]||"";if(!k){process.exit(0)}fs.mkdirSync(d,{recursive:true});'
    'try{if(fs.statSync(f).size>0)process.exit(0)}catch{}'
    'fs.writeFileSync(f,JSON.stringify({auth_mode:"apikey",OPENAI_API_KEY:k}),{mode:0o600})'
)
# Sets the top-level ``openai_base_url`` key in ~/.codex/config.toml: any
# earlier top-level assignment is dropped and the new one prepended, so tables
# and other keys survive.  A JSON string literal is a valid TOML basic string.
_CODEX_CONFIG_BASE_URL = (
    'const fs=require("fs"),d=process.env.HOME+"/.codex",f=d+"/config.toml",v=process.argv[1];'
    'const u=process.env[v]||"";if(!u){process.exit(0)}fs.mkdirSync(d,{recursive:true});'
    'let t="";try{t=fs.readFileSync(f,"utf8")}catch{}let top=true;'
    'const kept=t.split("\\n").filter(l=>{if(/^\\s*\\[/.test(l))top=false;return !(top&&/^\\s*openai_base_url\\s*=/.test(l))});'
    'fs.writeFileSync(f,"openai_base_url = "+JSON.stringify(u)+"\\n"+kept.join("\\n"))'
)
_CODEX_SESSION_AUTH = (
    'const fs=require("fs"),d=process.env.HOME+"/.codex",f=d+"/auth.json",v=process.argv[1];'
    'const s=process.env[v]||"";if(!s){process.exit(0)}fs.mkdirSync(d,{recursive:true});'
    'try{if(fs.statSync(f).size>0)process.exit(0)}catch{}'
    'fs.writeFileSync(f,s,{mode:0o600})'
)
# Writes the OpenCode user config (ADR-0011): one OpenAI-compatible custom
# provider whose ``apiKey`` is the literal reference ``{env:NAME}`` (NAME is
# argv[1]), so OpenCode reads the key from its own environment and the value is
# never written to disk.  Base URL and models come from the plan's
# ``SDF_OPENCODE_*`` sandbox variables.  The provider id is the model prefix.
# OpenCode 1.19.4 allows ``bash`` and ``edit`` by default, which would be
# permission skipping everywhere.  The seed makes both ``ask`` (headless runs
# auto-reject them) and denies ``webfetch``; ``opencode run --auto`` approves
# the asks only after a fresh production E2B sandbox claim (ADR-0008/0011).
_OPENCODE_CONFIG = (
    'const fs=require("fs"),e=process.env,d=e.HOME+"/.config/opencode",f=d+"/opencode.json",v=process.argv[1];'
    'if(!e[v]){process.exit(0)}const u=e.SDF_OPENCODE_BASE_URL||"",m=e.SDF_OPENCODE_MODEL||"",s=e.SDF_OPENCODE_SMALL_MODEL||m;'
    'const i=m.indexOf("/");if(!u||i<1){console.error("opencode-config: base URL or model missing");process.exit(1)}'
    'const p=m.slice(0,i),models={};for(const x of [m,s]){if(!x.startsWith(p+"/")){console.error("opencode-config: small model provider differs");process.exit(1)}models[x.slice(i+1)]={tool_call:true}}'
    'const c={$schema:"https://opencode.ai/config.json",model:m,small_model:s,autoupdate:false,share:"disabled",'
    'permission:{edit:"ask",bash:"ask",webfetch:"deny"},provider:{[p]:{npm:"@ai-sdk/openai-compatible",name:p,'
    'options:{baseURL:u,apiKey:"{env:"+v+"}"},models}}};'
    'fs.mkdirSync(d,{recursive:true,mode:0o700});fs.writeFileSync(f,JSON.stringify(c,null,2)+"\\n",{mode:0o600});fs.chmodSync(f,0o600)'
)
# Writes Pi's user model registry ~/.pi/agent/models.json (ADR-0012): one
# OpenAI-compatible provider ``sdf`` whose ``apiKey`` is the key variable's
# NAME (argv[1]).  Pi 0.73.1 resolves a non-``!`` apiKey as an environment
# variable name first, so the key value is never written to disk.  Base URL and
# model come from the plan's ``SDF_PI_*`` sandbox variables.  Token prices are
# filled in only for the default Vultr glm-5.3 (USD per 1M tokens); any other
# model reports tokens with a zero cost.  The URL is matched by an escaped
# URL and model are matched by escaped regexes because a seed command may not
# contain a plan variable's value (``SDF_PI_BASE_URL`` / ``SDF_PI_MODEL``).
_PI_MODELS = (
    'const fs=require("fs"),e=process.env,d=e.HOME+"/.pi/agent",f=d+"/models.json",v=process.argv[1];'
    'if(!e[v]){process.exit(0)}const u=e.SDF_PI_BASE_URL||"",m=e.SDF_PI_MODEL||"";'
    'if(!u||!m||!/^[A-Z][A-Z0-9_]*_API_KEY$/.test(v)){console.error("pi-models: base URL, model or key name missing");process.exit(1)}'
    'const price=/^https:\\/\\/api\\.vultrinference\\.com\\/v1\\/*$/.test(u)&&/^glm-5\\.3$/.test(m)?[0.75,3]:[0,0];'
    'const c={providers:{sdf:{baseUrl:u,api:"openai-completions",apiKey:v,models:[{id:m,name:m,reasoning:true,'
    'input:["text"],contextWindow:200000,maxTokens:32000,cost:{input:price[0],output:price[1],cacheRead:0,cacheWrite:0}}]}}};'
    'fs.mkdirSync(d,{recursive:true,mode:0o700});fs.writeFileSync(f,JSON.stringify(c,null,2)+"\\n",{mode:0o600});fs.chmodSync(f,0o600)'
)
# Herdr's server starts from the template entrypoint before ``Sandbox.create``
# envs apply to later commands, so an agent pane does not inherit them.  This
# seed writes the plan's variables (named on argv, read from ``process.env``)
# to a private file that every interactive bash sources.  The source line goes
# first in ``.bashrc``, before Debian's non-interactive early return.
_AGENT_ENV_FILE = ".config/sdf/agent-env.sh"
_PANE_ENV = (
    'const fs=require("fs"),h=process.env.HOME,d=h+"/.config/sdf",f=h+"/' + _AGENT_ENV_FILE + '",r=h+"/.bashrc";'
    "const q=v=>\"'\"+v.replace(/'/g,\"'\\\\''\")+\"'\";"
    'fs.mkdirSync(d,{recursive:true,mode:0o700});'
    'fs.writeFileSync(f,process.argv.slice(1).filter(n=>process.env[n]).map(n=>"export "+n+"="+q(process.env[n])+"\\n").join(""),{mode:0o600});'
    'fs.chmodSync(f,0o600);'
    'const l="[ -r \\"$HOME/' + _AGENT_ENV_FILE + '\\" ] && . \\"$HOME/' + _AGENT_ENV_FILE + '\\"\\n";'
    'let b="";try{b=fs.readFileSync(r,"utf8")}catch{}if(!b.includes(l))fs.writeFileSync(r,l+b)'
)
# Claude asks whether to trust a folder the first time it opens there; the
# Attempt workspace is created per Attempt, so trust it right before start.
_CLAUDE_TRUST_WORKSPACE = (
    'const fs=require("fs"),f=process.env.HOME+"/.claude.json",p=process.argv[1];'
    'let c={};try{c=JSON.parse(fs.readFileSync(f,"utf8"))}catch{}'
    'c.projects=c.projects||{};c.projects[p]=Object.assign({},c.projects[p],{hasTrustDialogAccepted:true});'
    'fs.writeFileSync(f,JSON.stringify(c),{mode:0o600});fs.chmodSync(f,0o600)'
)
# Codex 0.157 shows its folder-trust dialog for a fresh Attempt directory even
# with a ``-c projects.<dir>.trust_level`` override (observed live, #16); only a
# ``[projects."<dir>"]`` table in ~/.codex/config.toml suppresses it.  Appended
# once, so earlier top-level keys (``openai_base_url``) stay first.
_CODEX_TRUST_WORKSPACE = (
    'const fs=require("fs"),d=process.env.HOME+"/.codex",f=d+"/config.toml",p=process.argv[1];'
    'fs.mkdirSync(d,{recursive:true});let t="";try{t=fs.readFileSync(f,"utf8")}catch{}'
    'const h="[projects."+JSON.stringify(p)+"]";if(t.split("\\n").includes(h))process.exit(0);'
    'fs.writeFileSync(f,t+(t&&!t.endsWith("\\n")?"\\n":"")+"\\n"+h+"\\ntrust_level = \\"trusted\\"\\n")'
)
_WORKSPACE_TRUST: Mapping[str, str] = MappingProxyType(
    {"claude": _CLAUDE_TRUST_WORKSPACE, "codex": _CODEX_TRUST_WORKSPACE}
)
_SEED_SCRIPTS: Mapping[str, str] = MappingProxyType(
    {
        "claude-approve-api-key": _CLAUDE_APPROVE_KEY,
        "codex-auth-json-api-key": _CODEX_API_KEY_AUTH,
        "codex-config-base-url": _CODEX_CONFIG_BASE_URL,
        "codex-auth-json": _CODEX_SESSION_AUTH,
        "opencode-config": _OPENCODE_CONFIG,
        "pi-models": _PI_MODELS,
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

def pane_env_command(variables: Iterable[str]) -> str:
    """Render the seed that exposes ``variables`` to Herdr agent panes, by name."""

    names = tuple(dict.fromkeys(variables))
    for name in names:
        if not re.fullmatch(r"[A-Z_][A-Z0-9_]*", name):
            raise CredentialConfigError(f"invalid sandbox variable name {name!r}")
    return f"node -e {shlex.quote(_PANE_ENV)} {' '.join(names)}"

@dataclass(frozen=True, slots=True)
class CredentialInjection:
    """Resolved create-time injection: sandbox envs, strips, seeds, metadata."""

    envs: Mapping[str, str]
    strip_variables: tuple[str, ...]
    seed_commands: tuple[str, ...]
    metadata: Mapping[str, CredentialMetadata] = field(default_factory=dict)
    # Every variable a plan set is credential material, whatever its name;
    # provider names are always guarded too.
    credential_variables: frozenset[str] = frozenset()

    def __post_init__(self) -> None:
        object.__setattr__(self, "envs", MappingProxyType(dict(self.envs)))
        object.__setattr__(self, "metadata", MappingProxyType(dict(self.metadata)))
        object.__setattr__(self, "credential_variables", frozenset(self.credential_variables) | _CREDENTIAL_NAMES)
        for name, value in self.envs.items():
            if name in self.credential_variables and value and any(value in command for command in self.seed_commands):
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
    seeds += (pane_env_command(sorted(credential_envs)),)
    metadata = {
        agent: CredentialMetadata(plan.mode.value, plan.connection_id) for agent, plan in plans.items()
    }
    return CredentialInjection(envs, strip, seeds, metadata, frozenset(credential_envs))

def _lazy_plugin_bridge(environ: Mapping[str, str]) -> ConnectionReader:
    bridge: list[ConnectionReader] = []

    def read(agent: str, connection_id: str) -> Any:
        if not bridge:
            bridge.append(PluginConnectionBridge(environ=environ))
        return bridge[0](agent, connection_id)

    return read


def resolve_sandbox_provider(environ: Mapping[str, str], provider: str | None = None) -> str:
    """Return ``e2b`` (default) or ``daytona`` from an explicit arg or env knob."""

    raw = provider if provider is not None else environ.get("SDF_SANDBOX_PROVIDER", DEFAULT_SANDBOX_PROVIDER)
    selected = (raw or DEFAULT_SANDBOX_PROVIDER).strip().lower() or DEFAULT_SANDBOX_PROVIDER
    if selected not in SANDBOX_PROVIDERS:
        raise CredentialConfigError(
            f"SDF_SANDBOX_PROVIDER must be one of {', '.join(sorted(SANDBOX_PROVIDERS))}; got {raw!r}"
        )
    return selected


class CredentialSeedingMixin:
    """Provider-agnostic create-time credential seeding (ADR-0007)."""

    credential_metadata: Mapping[str, CredentialMetadata]
    _seed_commands: tuple[str, ...]

    def _init_credential_seeding(self, injection: CredentialInjection) -> None:
        self.credential_metadata = injection.metadata
        self._seed_commands = injection.seed_commands
        # Use the provider lifecycle lock for both sandbox operations and
        # seeding. Besides making close wait for the complete seed sequence,
        # one reentrant lock preserves lock order for E2B's permission claim
        # and for seed-failure cleanup (which closes from inside _ensure_sandbox).
        self._credential_seed_lock = self._sandbox_lock  # type: ignore[attr-defined]

    def _ensure_sandbox(self) -> Any:
        # The lock spans both the freshness check and seed sequence. It is
        # reentrant because each seed command calls _exec -> _ensure_sandbox.
        with self._credential_seed_lock:
            fresh = self._sandbox is None and self._sandbox_id is None  # type: ignore[attr-defined]
            sandbox = super()._ensure_sandbox()  # type: ignore[misc]
            if fresh and self._seed_commands:
                self._seed()
            return sandbox

    def close(self, timeout_ms: int = 30_000) -> None:
        # Match _ensure_sandbox's lock order (credential seed, then provider
        # sandbox). Acquiring the provider lock first would deadlock if a seed
        # command needed _ensure_sandbox while close waited for this lock.
        # RLock also lets seed-failure cleanup close the sandbox reentrantly.
        with self._credential_seed_lock:
            super().close(timeout_ms)  # type: ignore[misc]

    def _seed(self) -> None:
        timeout_ms = self.timeout_seconds * 1000  # type: ignore[attr-defined]
        for command in self._seed_commands:
            try:
                self._exec(command, timeout_ms)  # type: ignore[attr-defined]
            except HerdrRuntimeError:
                # Command output is not echoed: a seed handles credential state.
                self._kill_after_timeout()  # type: ignore[attr-defined]
                raise HerdrRuntimeError("credential seed step failed; sandbox killed") from None

    def prepare_agent_workspace(self, agent: str, workspace: str) -> None:
        """Pre-trust an Attempt workspace for an agent kind that asks about it."""

        script = _WORKSPACE_TRUST.get(agent)
        if script is None:
            return
        command = f"node -e {shlex.quote(script)} {shlex.quote(workspace)}"
        self._exec(command, self.timeout_seconds * 1000)  # type: ignore[attr-defined]


class CredentialedE2BHerdrTransport(CredentialSeedingMixin, E2BHerdrTransport):
    """E2B Herdr transport that seeds agent credentials once after creation."""

    def __init__(self, *, injection: CredentialInjection, **kwargs: Any) -> None:
        super().__init__(envs=injection.envs, **kwargs)
        self._init_credential_seeding(injection)


class CredentialedDaytonaHerdrTransport(CredentialSeedingMixin, DaytonaHerdrTransport):
    """Daytona Herdr transport that seeds agent credentials once after creation."""

    def __init__(self, *, injection: CredentialInjection, **kwargs: Any) -> None:
        super().__init__(envs=injection.envs, **kwargs)
        self._init_credential_seeding(injection)


def create_credentialed_transport(
    *,
    agents: Iterable[str],
    environ: Mapping[str, str],
    template: str | None = None,
    snapshot: str | None = None,
    provider: str | None = None,
    read_connection: ConnectionReader | None = None,
    envs: Mapping[str, str] | None = None,
    **transport_kwargs: Any,
) -> tuple[CredentialedE2BHerdrTransport | CredentialedDaytonaHerdrTransport, CredentialInjection]:
    """Resolve credentials for ``agents``, then build the persistent transport.

    Provider selection: ``provider=`` or ``SDF_SANDBOX_PROVIDER`` (default
    ``e2b``). E2B uses ``template=``; Daytona uses ``snapshot=`` (``template=``
    is accepted as an alias). The sandbox itself is created lazily on the first
    command; a credential configuration error surfaces here, so no sandbox is
    ever created for it.
    """

    selected = resolve_sandbox_provider(environ, provider)
    # The connection must outlive the box, so check against its real lifetime.
    if selected == "daytona":
        timeout_default = inspect.signature(DaytonaHerdrTransport).parameters["timeout_seconds"].default
        image = (snapshot or template or environ.get("SDF_DAYTONA_SNAPSHOT", "")).strip()
        if not image:
            raise CredentialConfigError(
                "Daytona credentialed transport requires snapshot= (or template= / SDF_DAYTONA_SNAPSHOT)"
            )
        timeout_seconds = transport_kwargs.get("timeout_seconds", timeout_default)
        injection = prepare_credential_injection(
            agents, environ, read_connection=read_connection, base_envs=envs, sandbox_timeout_seconds=timeout_seconds
        )
        transport = CredentialedDaytonaHerdrTransport(
            injection=injection, snapshot=image, environ=environ, **transport_kwargs
        )
        return transport, injection

    timeout_default = inspect.signature(E2BHerdrTransport).parameters["timeout_seconds"].default
    image = (template or "").strip()
    if not image:
        raise CredentialConfigError("E2B credentialed transport requires template=")
    timeout_seconds = transport_kwargs.get("timeout_seconds", timeout_default)
    injection = prepare_credential_injection(
        agents, environ, read_connection=read_connection, base_envs=envs, sandbox_timeout_seconds=timeout_seconds
    )
    transport = CredentialedE2BHerdrTransport(
        injection=injection, template=image, environ=environ, **transport_kwargs
    )
    return transport, injection
