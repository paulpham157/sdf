"""Headless Aider runtime (ADR-0013).

``aider --message-file F`` runs one non-interactive request/edit cycle and
exits.  Endpoint and model are passed on argv (``--openai-api-base``,
``--model openai/<id>`` through LiteLLM) and the key only as
``OPENAI_API_KEY``, which the shared wrapper exports from the variable the
``aider-key-ref`` seed recorded by NAME.  Aider is a pair-programming tool, not
an autonomous multi-step agent: one turn is one model exchange plus its edits.

Hardening (verified against 0.86.2 with a local mock endpoint):

* Aider merges ``.aider.conf.yml``, ``.env``, ``.aider.model.settings.yml`` and
  ``.aider.model.metadata.json`` from the cwd, the git root and ``$HOME``; any of
  them can redirect the endpoint/key or set lint/test commands.  A workspace
  with ``.aider*`` or ``.env`` is refused on the host before staging; in the
  sandbox the wrapper refuses when the workspace or any ancestor holds one.
  Every security-relevant flag is also set explicitly (command line wins).
* A workspace ``.git`` is refused too: Aider's GitPython calls would run
  repository-configured programs (``core.fsmonitor``, filters, ...).  The
  wrapper instead creates a fresh private repository per turn (``git init`` +
  ``git add -A``, no commit) for the repo map, and removes it - together with
  Aider's ``.aider.tags.cache.v*`` (a pickle cache) - after the turn.  Chat and
  input history go to ``/tmp``; ``--no-gitignore`` keeps ``.gitignore`` intact.
* Every ``AIDER_*``, ``OPENAI_*`` and ``LITELLM_*`` variable is unset per turn.
* ``--chat-mode ask --no-suggest-shell-commands`` (no edits, no shell) unless
  the fresh production E2B claim (ADR-0008) succeeded, then ``code`` mode.
* Aider prints token counts per exchange; it does not report the served model.
"""

from __future__ import annotations

import re
from typing import Any

from .herdr_runtime import HerdrRuntimeError
from .headless_runtime import HeadlessAgentRuntime, HeadlessBinding, HeadlessUsage, TurnResult, build_wrapper

AIDER_VERSION = "0.86.2"
AGENT_KIND = "aider"
PROJECT_CONFIG_ENTRIES = (".aider*", ".env", ".git")


class AiderRuntimeError(HerdrRuntimeError):
    """Aider or its sandbox returned an unusable result."""


_WRAPPER = build_wrapper(
    agent=AGENT_KIND,
    unset_prefixes=("AIDER_", "OPENAI_", "LITELLM_"),
    unset_names=("GIT_DIR", "GIT_WORK_TREE", "GIT_CONFIG", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"),
    exports=("GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"),
    refused_entries=PROJECT_CONFIG_ENTRIES,
    ancestor_entries=(".env", ".aider.conf.yml", ".aider.model.settings.yml", ".aider.model.metadata.json",
                      ".aider/oauth-keys.env"),
    key_ref_file="aider-api-key-name",
    command_tail='sh -c \'git init -q && git add -A >/dev/null 2>&1; exec "$@"\' sdf-aider-git "$@" '
                 '--model "openai/$model" --openai-api-base "$url"',
    cleanup_entries=(".git", ".aider.tags.cache.v*"),
)

_TOKENS = re.compile(r"^Tokens: ([\d.]+)([kKmM]?) sent(?:, ([\d.]+)([kKmM]?) cache \w+)*?, ([\d.]+)([kKmM]?) received")
_COST = re.compile(r"Cost: \$([\d.]+) message")
_MODEL_LINE = re.compile(r"^(?:Main )?[Mm]odel: (?:openai/)?(\S+)")
_ERROR = re.compile(r"^(litellm\.\w+|.*Error: |The API provider)")


def _num(value: str, suffix: str) -> int:
    scale = {"": 1, "k": 1_000, "m": 1_000_000}[suffix.lower()]
    return int(round(float(value) * scale))


def parse_output(raw: str, model: str | None = None) -> tuple[tuple[str, ...], HeadlessUsage, tuple[str, ...]]:
    """Parse Aider's plain output: (lines, this turn's usage, errors)."""

    lines: list[str] = []
    errors: list[str] = []
    usage = HeadlessUsage()
    usage.add_models(requested=model)
    for line in raw.splitlines():
        if not line.strip():
            continue
        tokens = _TOKENS.match(line.strip())
        if tokens:
            usage.messages += 1
            usage.input_tokens += _num(tokens.group(1), tokens.group(2))
            usage.output_tokens += _num(tokens.group(5), tokens.group(6))
            cost = _COST.search(line)
            if cost:
                usage.cost += float(cost.group(1))
            continue
        requested = _MODEL_LINE.match(line.strip())
        if requested:
            usage.add_models(requested=requested.group(1))
        if _ERROR.match(line.strip()):
            errors.append(line.strip()[:500])
        lines.append(line)
    if not usage.messages and not errors:
        errors.append("Aider made no model call")
    return tuple(lines), usage, tuple(errors)


class AiderHeadlessRuntime(HeadlessAgentRuntime):
    """``AgentRuntime`` for the ``aider`` agent kind, one turn per ``send``."""

    AGENT_KIND = AGENT_KIND
    LABEL = "Aider"
    VERSION = AIDER_VERSION
    ERROR = AiderRuntimeError
    WRAPPER = _WRAPPER
    PROJECT_CONFIG_ENTRIES = PROJECT_CONFIG_ENTRIES
    CANCEL_PATTERN = "[a]ider --message-file"

    def agent_argv(self, binding: HeadlessBinding, prompt_file: str) -> list[str]:
        base = self.scratch(binding)
        argv = [
            self._binary, "--message-file", prompt_file,
            "--no-auto-commits", "--no-dirty-commits", "--no-gitignore", "--no-check-update",
            "--analytics-disable", "--no-show-model-warnings", "--no-pretty", "--no-fancy-input",
            "--no-detect-urls", "--disable-playwright", "--no-auto-lint", "--no-auto-test",
            "--no-show-release-notes", "--yes-always", "--encoding", "utf-8",
            "--chat-history-file", f"{base}.chat.md", "--input-history-file", f"{base}.input",
            "--restore-chat-history" if binding.turns else "--no-restore-chat-history",
        ]
        if binding.auto:
            argv += ["--chat-mode", "code"]
        else:
            argv += ["--chat-mode", "ask", "--no-suggest-shell-commands"]
        return argv

    def parse_turn(self, binding: HeadlessBinding, body: str) -> TurnResult:
        lines, usage, errors = parse_output(body, self._model)
        binding.usage.merge(usage)
        return TurnResult(lines, errors)

    def usage(self, session_id: str) -> dict[str, Any]:
        result = super().usage(session_id)
        result["served_model_reported"] = False
        return result


__all__ = ["AIDER_VERSION", "AiderHeadlessRuntime", "AiderRuntimeError", "parse_output"]
