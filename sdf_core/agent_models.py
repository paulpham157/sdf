"""Per-agent model selection (ADR-0011).

A pure module: an agent kind plus an environment mapping becomes the model
(and, for OpenCode, the small model) that agent should use, and the CLI argv
that selects it.  Only ``SDF_*`` variables are read.  Nothing here touches the
network, a sandbox or disk.

Supported overrides, per agent kind:

* ``claude`` - ``SDF_CLAUDE_MODEL`` -> ``claude --model <model>``; unset keeps
  Claude Code's own default.
* ``codex`` - ``SDF_CODEX_MODEL`` -> ``codex -m <model>``; unset keeps Codex's
  own default.
* ``opencode`` - ``SDF_OPENCODE_MODEL`` (``provider/model``, default
  ``vultr/glm-5.3``) and ``SDF_OPENCODE_SMALL_MODEL`` (same provider, default
  ``vultr/glm-5.3-flash``).  Both are written into the seeded OpenCode config
  (``model`` / ``small_model``); ``--model`` selects the main model per run.
* ``pi`` - ``SDF_PI_MODEL`` (a bare model id, default ``glm-5.3``), written
  into the seeded ``~/.pi/agent/models.json`` and selected per turn with
  ``pi --provider sdf --model <id>`` (ADR-0012).

Error messages name variables, never values: an operator may paste a secret
into the wrong variable.
"""

from __future__ import annotations

import re
from collections.abc import Iterable, Mapping
from dataclasses import dataclass
from types import MappingProxyType

from .credentials import CredentialConfigError

MODEL_VARIABLES: Mapping[str, str] = MappingProxyType(
    {"claude": "SDF_CLAUDE_MODEL", "codex": "SDF_CODEX_MODEL", "opencode": "SDF_OPENCODE_MODEL", "pi": "SDF_PI_MODEL"}
)
SMALL_MODEL_VARIABLES: Mapping[str, str] = MappingProxyType({"opencode": "SDF_OPENCODE_SMALL_MODEL"})
# Vultr Serverless Inference is the example/default OpenCode provider only;
# every part of it is overridable (base URL in credentials.py).
DEFAULT_MODELS: Mapping[str, str] = MappingProxyType({"opencode": "vultr/glm-5.3", "pi": "glm-5.3"})
DEFAULT_SMALL_MODELS: Mapping[str, str] = MappingProxyType({"opencode": "vultr/glm-5.3-flash"})
# The flag each CLI documents for model selection.
_MODEL_FLAGS: Mapping[str, str] = MappingProxyType(
    {"claude": "--model", "codex": "-m", "opencode": "--model", "pi": "--model"}
)

_MODEL = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:@/+-]{0,199}")
# OpenCode model ids are ``<provider id>/<model id>``; the provider id becomes a
# JSON key in the seeded config, so keep it to a conservative identifier.
# Pi resolves ``--model`` against the seeded provider's model ids; a ``:``
# would be read as a thinking-level suffix, so it is not allowed.
_PI_MODEL = re.compile(r"[A-Za-z0-9][A-Za-z0-9._@/+-]{0,199}")
_OPENCODE_MODEL = re.compile(r"([a-z0-9][a-z0-9_-]{0,63})/([A-Za-z0-9][A-Za-z0-9._:@+-]{0,199})")


@dataclass(frozen=True, slots=True)
class AgentModelSelection:
    """The model choice for one agent kind; ``None`` keeps the CLI default."""

    agent: str
    model: str | None
    small_model: str | None = None

    @property
    def provider_id(self) -> str | None:
        """OpenCode provider id (the part before ``/``), else ``None``."""

        if self.agent != "opencode" or self.model is None:
            return None
        return self.model.split("/", 1)[0]


def resolve_agent_models(agent: str, environ: Mapping[str, str]) -> AgentModelSelection:
    """Resolve the model (and small model) ``agent`` should use."""

    variable = MODEL_VARIABLES.get(agent)
    if variable is None:
        # Agent kinds without a model override (e.g. cursor) keep their CLI default.
        return AgentModelSelection(agent, None)
    model = _read(variable, environ) or DEFAULT_MODELS.get(agent)
    small_variable = SMALL_MODEL_VARIABLES.get(agent)
    small = (_read(small_variable, environ) or DEFAULT_SMALL_MODELS.get(agent)) if small_variable else None
    if agent == "opencode":
        assert model is not None and small is not None and small_variable is not None
        main = _OPENCODE_MODEL.fullmatch(model)
        if main is None:
            raise CredentialConfigError(f"{variable} must be <provider>/<model>, e.g. vultr/glm-5.3")
        tiny = _OPENCODE_MODEL.fullmatch(small)
        if tiny is None:
            raise CredentialConfigError(f"{small_variable} must be <provider>/<model>, e.g. vultr/glm-5.3-flash")
        if tiny.group(1) != main.group(1):
            # One custom provider (one base URL, one key) per OpenCode sandbox.
            raise CredentialConfigError(f"{small_variable} must use the same provider as {variable}")
    if agent == "pi":
        assert model is not None
        if not _PI_MODEL.fullmatch(model):
            raise CredentialConfigError(f"{variable} must be a bare model id without ':', e.g. glm-5.3")
    return AgentModelSelection(agent, model, small)


def agent_model_args(agent: str, environ: Mapping[str, str]) -> tuple[str, ...]:
    """CLI argv that selects ``agent``'s configured model, or ``()``."""

    selection = resolve_agent_models(agent, environ)
    if selection.model is None:
        return ()
    return (_MODEL_FLAGS[agent], selection.model)


def model_agent_args(agents: Iterable[str], environ: Mapping[str, str]) -> dict[str, tuple[str, ...]]:
    """``HerdrRuntime(agent_args=...)`` mapping for every agent with a model set."""

    args = {agent: agent_model_args(agent, environ) for agent in dict.fromkeys(agents)}
    return {agent: value for agent, value in args.items() if value}


def _read(variable: str, environ: Mapping[str, str]) -> str | None:
    raw = environ.get(variable, "").strip()
    if not raw:
        return None
    if not _MODEL.fullmatch(raw):
        raise CredentialConfigError(f"{variable} is not a valid model name")
    return raw
