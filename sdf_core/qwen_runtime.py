"""Headless Qwen Code runtime (ADR-0013).

``qwen -o json --bare --auth-type openai`` runs one non-interactive turn (the
prompt on stdin) against an OpenAI-compatible endpoint and prints one JSON
array of events.  ``--bare`` skips user/project ``settings.json``, hooks, MCP
servers, extensions, skills and memory discovery; endpoint and model are passed
on argv (``--openai-base-url``, ``-m``) and the key only as ``OPENAI_API_KEY``,
which the shared wrapper exports from the variable the ``qwen-key-ref`` seed
recorded by NAME.

Hardening (verified against 0.26.2 with a local mock endpoint):

* Even with ``--bare``, Qwen Code loads the first ``.env`` (or ``.qwen/.env``)
  it finds walking up from the cwd, which can redirect the endpoint and key or
  set ``QWEN_*`` switches.  A workspace with ``.env`` / ``.qwen`` is refused on
  the host before staging; in the sandbox the wrapper refuses before every turn
  when the workspace or any ancestor holds ``.env`` / ``.qwen/.env`` /
  ``.qwen/settings.json``.
* Every ``QWEN_*``, ``OPENAI_*`` and ``GEMINI_*`` variable is unset per turn;
  usage statistics and telemetry are then switched off and the CLI's own
  sandbox/relaunch disabled.
* ``--approval-mode plan`` (reads only: edits and shell are denied) unless the
  fresh production E2B claim (ADR-0008) succeeded, then ``yolo``.
* ADR-0021: with that claim and ``SDF_QWEN_PROJECT_TRUST=always`` (default)
  the refusal and ``--bare`` are dropped, so ``.qwen/`` settings, hooks, MCP
  servers, ``QWEN.md`` and ``.env`` load; endpoint, key, model and
  ``--exclude-tools`` stay on argv/env and win.
"""

from __future__ import annotations

import json
import uuid
from typing import Any, Mapping

from . import subagents
from .herdr_runtime import HerdrRuntimeError
from .headless_runtime import HeadlessAgentRuntime, HeadlessBinding, HeadlessUsage, TurnResult, as_int, build_wrapper

QWEN_VERSION = "0.26.2"
AGENT_KIND = "qwen"
PROJECT_CONFIG_ENTRIES = (".qwen", ".env")


class QwenRuntimeError(HerdrRuntimeError):
    """Qwen Code or its sandbox returned an unusable result."""


_WRAPPER_ARGS: dict[str, Any] = dict(
    agent=AGENT_KIND,
    unset_prefixes=("QWEN_", "OPENAI_", "GEMINI_"),
    exports=(
        "QWEN_USAGE_STATISTICS_ENABLED=false",
        "QWEN_TELEMETRY_ENABLED=false",
        "QWEN_SANDBOX=false",
        "QWEN_CODE_NO_RELAUNCH=true",
    ),
    key_ref_file="qwen-api-key-name",
    command_tail='"$@" -m "$model" --openai-base-url "$url"',
)
_WRAPPER = build_wrapper(
    **_WRAPPER_ARGS,
    refused_entries=PROJECT_CONFIG_ENTRIES,
    ancestor_entries=(".env", ".qwen/.env", ".qwen/settings.json"),
)
# ADR-0021 trusted project: no refusal, no ``--bare``.  Verified (0.26.2, mock
# endpoints): a hostile ``.qwen/settings.json`` (``security.auth`` key/baseUrl,
# ``model.name``, ``modelProviders``, ``env``, ``tools.core`` naming ``agent``)
# plus ``.qwen/.env`` and ``.env`` did not move the endpoint, key or model away
# from the argv/``OPENAI_API_KEY`` the wrapper sets (exported variables beat
# ``.env``), ``--exclude-tools`` still removed ``agent``, and ``QWEN.md`` was
# honoured.
_TRUSTED_WRAPPER = build_wrapper(**_WRAPPER_ARGS)


def parse_events(raw: str) -> tuple[tuple[str, ...], HeadlessUsage | None, tuple[str, ...]]:
    """Parse ``qwen -o json`` output: (output lines, session usage, errors).

    The ``result`` event's ``usage`` is the session total (it grows across
    ``--resume`` turns); ``stats.models`` is keyed by the model id the endpoint
    reported serving.
    """

    start = raw.find("[")
    try:
        events = json.loads(raw[start:]) if start >= 0 else None
    except json.JSONDecodeError:
        events = None
    if not isinstance(events, list):
        lines = tuple(line for line in raw.splitlines() if line.strip())
        return lines, None, ("Qwen Code output was not a JSON event list",)
    lines: list[str] = []
    errors: list[str] = []
    usage: HeadlessUsage | None = None
    requested = None
    for event in events:
        if not isinstance(event, Mapping):
            continue
        kind = event.get("type")
        if kind == "system" and event.get("subtype") == "init":
            requested = event.get("model")
        elif kind == "assistant":
            message = event.get("message") if isinstance(event.get("message"), Mapping) else {}
            for part in message.get("content") or ():
                if not isinstance(part, Mapping):
                    continue
                if part.get("type") == "text" and isinstance(part.get("text"), str) and part["text"].strip():
                    lines.append(part["text"])
                elif part.get("type") == "tool_use":
                    lines.append(f"[tool {part.get('name', '?')}]")
        elif kind == "result":
            usage = _usage(event, requested)
            denials = event.get("permission_denials") or ()
            if denials:
                lines.append(f"[{len(denials)} tool call(s) denied by approval mode]")
            if event.get("is_error"):
                error = event.get("error") if isinstance(event.get("error"), Mapping) else {}
                detail = str(error.get("message") or event.get("subtype") or "error")[:500]
                errors.append(detail)
                lines.append(f"[error {detail}]")
    if usage is None and not errors:
        errors.append("Qwen Code printed no result event")
    return tuple(lines), usage, tuple(errors)


def _usage(event: Mapping[str, Any], requested: Any) -> HeadlessUsage:
    raw = event.get("usage") if isinstance(event.get("usage"), Mapping) else {}
    usage = HeadlessUsage(
        messages=as_int(event.get("num_turns")),
        input_tokens=as_int(raw.get("input_tokens")),
        output_tokens=as_int(raw.get("output_tokens")),
        cache_read_tokens=as_int(raw.get("cache_read_input_tokens")),
        total_tokens=as_int(raw.get("total_tokens")),
    )
    usage.add_models(requested=requested)
    stats = event.get("stats") if isinstance(event.get("stats"), Mapping) else {}
    models = stats.get("models") if isinstance(stats.get("models"), Mapping) else {}
    for served in models:
        usage.add_models(served=served)
    return usage


class QwenHeadlessRuntime(HeadlessAgentRuntime):
    """``AgentRuntime`` for the ``qwen`` agent kind, one turn per ``send``."""

    AGENT_KIND = AGENT_KIND
    LABEL = "Qwen Code"
    VERSION = QWEN_VERSION
    ERROR = QwenRuntimeError
    WRAPPER = _WRAPPER
    TRUSTED_WRAPPER = _TRUSTED_WRAPPER
    PROJECT_CONFIG_ENTRIES = PROJECT_CONFIG_ENTRIES

    def agent_argv(self, binding: HeadlessBinding, prompt_file: str) -> list[str]:
        session = binding.state.setdefault("session_id", str(uuid.uuid4()))
        argv = [self._binary, "-o", "json"]
        if not binding.project_trusted:
            argv.append("--bare")
        argv += ["--auth-type", "openai"]
        argv += list(subagents.QWEN_ARGS)  # ADR-0020
        argv += ["--approval-mode", "yolo" if binding.auto else "plan"]
        argv += ["--resume", session] if binding.turns else ["--session-id", session]
        return argv

    def parse_turn(self, binding: HeadlessBinding, body: str) -> TurnResult:
        lines, usage, errors = parse_events(body)
        if usage is not None:
            _replace_session_usage(binding, usage)
        return TurnResult(lines, errors)


def _replace_session_usage(binding: HeadlessBinding, snapshot: HeadlessUsage) -> None:
    """Session-cumulative counters: keep the latest snapshot (add if it reset)."""

    current = binding.usage
    if snapshot.input_tokens >= current.input_tokens and snapshot.output_tokens >= current.output_tokens:
        merged = HeadlessUsage(
            snapshot.messages, snapshot.input_tokens, snapshot.output_tokens, snapshot.cache_read_tokens,
            snapshot.total_tokens, snapshot.cost, list(current.requested_models), list(current.served_models),
        )
        merged.merge(HeadlessUsage(requested_models=snapshot.requested_models, served_models=snapshot.served_models))
        binding.usage = merged
    else:
        current.merge(snapshot)


__all__ = ["QWEN_VERSION", "QwenHeadlessRuntime", "QwenRuntimeError", "parse_events"]
