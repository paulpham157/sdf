"""Headless Goose runtime (ADR-0013).

``goose run -i - --output-format json --no-profile`` runs one non-interactive
turn (the prompt on stdin) and prints a startup banner followed by one JSON
document with the session's messages and cumulative usage.  Provider, base URL,
model and key reference live in the seeded declarative custom provider
``~/.config/goose/custom_providers/sdf_openai.json`` (``goose-provider`` seed,
engine ``openai``); ``api_key_env`` there is the key variable's NAME, so the
value only exists in the sandbox environment.

Hardening (verified against 1.56.0 with a local mock endpoint):

* Goose read no endpoint, key, mode or extension setting from a workspace
  ``.goose/``, ``config.yaml`` or ``.env``; ``.goose`` is still refused (host and
  sandbox) as defense in depth, since it is where Goose keeps project state.
* Every ``GOOSE_*`` and ``OPENAI_*`` variable and the XDG base directories are
  unset per turn (``GOOSE_PATH_ROOT`` / ``XDG_CONFIG_HOME`` would move the
  seeded provider), then the keyring, session naming (an extra model call)
  and telemetry are switched off.
* ``--no-profile`` loads no configured extensions.  ``GOOSE_MODE=chat`` (no
  tool calls) unless the fresh production E2B claim (ADR-0008) succeeded, then
  ``GOOSE_MODE=auto`` with the builtin ``developer`` extension (shell, edit).
* Goose reports the requested model only; the served model is not reported.
"""

from __future__ import annotations

import json
from typing import Any, Mapping

from .herdr_runtime import HerdrRuntimeError
from .headless_runtime import HeadlessAgentRuntime, HeadlessBinding, HeadlessUsage, TurnResult, as_int, build_wrapper
from .qwen_runtime import _replace_session_usage

GOOSE_VERSION = "1.56.0"
AGENT_KIND = "goose"
GOOSE_PROVIDER = "sdf_openai"
PROJECT_CONFIG_ENTRIES = (".goose",)


class GooseRuntimeError(HerdrRuntimeError):
    """Goose or its sandbox returned an unusable result."""


_WRAPPER = build_wrapper(
    agent=AGENT_KIND,
    unset_prefixes=("GOOSE_", "OPENAI_"),
    unset_names=("XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"),
    exports=(
        "GOOSE_DISABLE_KEYRING=1",
        "GOOSE_DISABLE_SESSION_NAMING=true",
        "GOOSE_TELEMETRY_ENABLED=false",
        "GOOSE_TELEMETRY_OFF=1",
    ),
    refused_entries=PROJECT_CONFIG_ENTRIES,
    command_tail=f'"$@" --provider {GOOSE_PROVIDER} --model "$model"',
)


def parse_output(raw: str, seen: int = 0) -> tuple[tuple[str, ...], HeadlessUsage | None, tuple[str, ...], int]:
    """Parse ``goose run --output-format json``: (lines, session usage, errors, message count).

    Only messages after the first ``seen`` (earlier turns of a resumed
    session) become output.
    """

    document = None
    for index, line in enumerate(raw.splitlines(keepends=True)):
        if line.startswith("{"):
            try:
                document = json.loads("".join(raw.splitlines(keepends=True)[index:]))
            except json.JSONDecodeError:
                document = None
            break
    if not isinstance(document, Mapping):
        lines = tuple(line for line in raw.splitlines() if line.strip())
        return lines, None, ("Goose output was not a JSON document",), seen
    messages = [m for m in document.get("messages") or () if isinstance(m, Mapping)]
    lines: list[str] = []
    errors: list[str] = []
    requested = None
    for message in messages[seen:]:
        meta = message.get("metadata") if isinstance(message.get("metadata"), Mapping) else {}
        inference = meta.get("inference") if isinstance(meta.get("inference"), Mapping) else {}
        requested = inference.get("requestedModel") or requested
        if message.get("role") != "assistant":
            continue
        for part in message.get("content") or ():
            if not isinstance(part, Mapping):
                continue
            kind = part.get("type")
            if kind == "text" and isinstance(part.get("text"), str) and part["text"].strip():
                lines.append(part["text"])
            elif kind == "toolRequest":
                call = part.get("toolCall") if isinstance(part.get("toolCall"), Mapping) else {}
                value = call.get("value") if isinstance(call.get("value"), Mapping) else {}
                lines.append(f"[tool {value.get('name', '?')}]")
    meta = document.get("metadata") if isinstance(document.get("metadata"), Mapping) else {}
    usage = HeadlessUsage(
        messages=sum(1 for m in messages if m.get("role") == "assistant"),
        input_tokens=as_int(meta.get("input_tokens")),
        output_tokens=as_int(meta.get("output_tokens")),
        cache_read_tokens=as_int(meta.get("cache_read_input_tokens")),
        total_tokens=as_int(meta.get("total_tokens")),
    )
    cost = meta.get("cost_usd")
    if isinstance(cost, (int, float)) and not isinstance(cost, bool):
        usage.cost = float(cost)
    usage.add_models(requested=requested)
    status = meta.get("status")
    if status not in (None, "completed"):
        detail = str(meta.get("error") or status)[:500]
        errors.append(detail)
        lines.append(f"[error {detail}]")
    return tuple(lines), usage, tuple(errors), len(messages)


class GooseHeadlessRuntime(HeadlessAgentRuntime):
    """``AgentRuntime`` for the ``goose`` agent kind, one turn per ``send``."""

    AGENT_KIND = AGENT_KIND
    LABEL = "Goose"
    VERSION = GOOSE_VERSION
    ERROR = GooseRuntimeError
    WRAPPER = _WRAPPER
    PROJECT_CONFIG_ENTRIES = PROJECT_CONFIG_ENTRIES
    CANCEL_PATTERN = "[g]oose run -i -"

    def agent_argv(self, binding: HeadlessBinding, prompt_file: str) -> list[str]:
        argv = ["env", f"GOOSE_MODE={'auto' if binding.auto else 'chat'}", self._binary, "run", "-i", "-",
                "--output-format", "json", "--no-profile", "-n", f"sdf-{binding.attempt_id}"]
        if binding.turns:
            argv.append("-r")
        if binding.auto:
            argv += ["--with-builtin", "developer"]
        return argv

    def parse_turn(self, binding: HeadlessBinding, body: str) -> TurnResult:
        lines, usage, errors, count = parse_output(body, binding.state.get("messages", 0))
        binding.state["messages"] = count
        if usage is not None:
            _replace_session_usage(binding, usage)
        return TurnResult(lines, errors)

    def usage(self, session_id: str) -> dict[str, Any]:
        result = super().usage(session_id)
        result["served_model_reported"] = False
        return result


__all__ = ["GOOSE_PROVIDER", "GOOSE_VERSION", "GooseHeadlessRuntime", "GooseRuntimeError", "parse_output"]
