"""Per-agent model selection (ADR-0011).

A pure module: an agent kind plus an environment mapping becomes the model
that agent should use, and the CLI argv that selects it.  Only the
allow-listed ``*_MODEL`` variable for that kind is read.  Nothing here
touches the network, a sandbox or disk.

Supported overrides, per agent kind:

* ``claude`` - ``ANTHROPIC_MODEL`` -> ``claude --model <model>``; unset keeps
  Claude Code's own default.
* ``codex`` - ``OPENAI_MODEL`` -> ``codex -m <model>``; unset keeps Codex's
  own default.
* ``opencode`` - ``OPENCODE_MODEL`` (``<local provider id>/<model>``, default
  ``vultr/glm-5.3``).  The prefix is the seeded OpenCode provider id, which
  OpenCode requires; it is not an API family (ADR 0034).  The same id is the
  config's ``model`` and ``small_model``.  ``--model`` selects it per run.
* ``pi`` - ``PI_MODEL`` (a bare model id, default ``glm-5.3``), written into
  the seeded ``~/.pi/agent/models.json`` and selected per turn with
  ``pi --provider sdf --model <id>`` (ADR-0012).
* ``qwen`` / ``goose`` - ``QWEN_MODEL`` / ``GOOSE_MODEL`` (a bare model id,
  default ``glm-5.3``) served by the agent's OpenAI-compatible endpoint; the
  headless runtimes pass it per turn (``qwen -m``, ``goose --model``)
  (ADR-0013).

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
    {"claude": "ANTHROPIC_MODEL", "codex": "OPENAI_MODEL", "opencode": "OPENCODE_MODEL", "pi": "PI_MODEL",
     "qwen": "QWEN_MODEL", "goose": "GOOSE_MODEL"}
)
# OpenCode's ``vultr/`` prefix is a local provider id in its seeded config,
# not a host. The base URL is whatever ``OPENCODE_BASE_URL`` names.
DEFAULT_OPENCODE_MODEL = "vultr/glm-5.3"
DEFAULT_MODELS: Mapping[str, str] = MappingProxyType(
    {"opencode": DEFAULT_OPENCODE_MODEL, "pi": "glm-5.3", "qwen": "glm-5.3", "goose": "glm-5.3"}
)
# Kinds whose model id is a bare id on an OpenAI-compatible endpoint (ADR-0012/0013).
BARE_MODEL_AGENTS = frozenset({"pi", "qwen", "goose"})
# The flag each CLI documents for model selection.
_MODEL_FLAGS: Mapping[str, str] = MappingProxyType(
    {"claude": "--model", "codex": "-m", "opencode": "--model", "pi": "--model",
     "qwen": "-m", "goose": "--model"}
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
        # Unknown agent kinds have no model override and keep their CLI default.
        return AgentModelSelection(agent, None)
    model = _read(variable, environ) or DEFAULT_MODELS.get(agent)
    # OpenCode's title agent uses the same model. There is no second env var.
    small = model if agent == "opencode" else None
    if agent == "opencode":
        assert model is not None
        if _OPENCODE_MODEL.fullmatch(model) is None:
            raise CredentialConfigError(f"{variable} must be <provider>/<model>, e.g. vultr/glm-5.3")
    if agent in BARE_MODEL_AGENTS:
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
