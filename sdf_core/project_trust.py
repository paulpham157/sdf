"""Per-kind project trust for Attempt workspaces (ADR-0021).

``SDF_<AGENT>_PROJECT_TRUST`` = ``always`` (default) or ``never``.

``always`` lets an agent load the Attempt workspace's own project config
(``.pi/``, ``.claude/`` + ``CLAUDE.md``, ``.codex/``, ``opencode.json`` /
``.opencode/``, ``.qwen/`` + ``QWEN.md`` + ``.env``, ``.goose/``), the
equivalent of Pi's ``defaultProjectTrust: always``: nobody is at the sandbox to
answer a trust prompt.  It takes effect only together with the one fresh
production E2B claim (ADR-0008), the same condition that enables write tools
and permission skipping; anywhere else every kind keeps its pre-ADR-0021
refusal/ignore behaviour.  ``never`` keeps that behaviour everywhere.

Whatever the project says, SDF still pins on the command line (or in a
higher-precedence source) the endpoint, key and model and the sub-agent
switches (ADR-0020); the egress allowlist stays the network boundary.
"""

from __future__ import annotations

import os
import re
from typing import Mapping

ALWAYS = "always"
NEVER = "never"
VALUES = (ALWAYS, NEVER)
DEFAULT = ALWAYS
_AGENT = re.compile(r"[a-z]+")


def env_name(agent: str) -> str:
    if not _AGENT.fullmatch(agent):
        raise ValueError("bad agent kind")
    return f"SDF_{agent.upper()}_PROJECT_TRUST"


def setting(agent: str, environ: Mapping[str, str] | None = None) -> str:
    """The configured trust for ``agent``: ``always`` unless set to ``never``.

    An empty value means the default; anything else is refused so a typo
    cannot silently change the policy.
    """

    name = env_name(agent)
    raw = (os.environ if environ is None else environ).get(name, "")
    value = raw.strip().lower()
    if not value:
        return DEFAULT
    if value not in VALUES:
        raise ValueError(f"{name} must be one of {', '.join(VALUES)}, got {raw!r} (ADR-0021)")
    return value


def wants_trust(agent: str, environ: Mapping[str, str] | None = None) -> bool:
    """True when the setting allows project config (before the E2B claim)."""

    return setting(agent, environ) == ALWAYS


def effective(agent: str, *, fresh_e2b: bool, environ: Mapping[str, str] | None = None) -> bool:
    """Project config is loaded only with ``always`` AND the fresh E2B claim."""

    return bool(fresh_e2b) and wants_trust(agent, environ)


__all__ = ["ALWAYS", "DEFAULT", "NEVER", "VALUES", "effective", "env_name", "setting", "wants_trust"]
