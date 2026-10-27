"""Headless OpenCode runtime over a persistent sandbox transport (ADR-0011).

OpenCode needs no terminal multiplexer: ``opencode run --format json`` runs
one non-interactive turn and prints JSON events.  This runtime therefore
drives OpenCode directly through the transport's ``exec_argv`` instead of
through Herdr panes; the transport (E2B or Daytona) still owns sandbox
creation, credential seeding, workspace staging/collection and teardown.

Provider, base URL, key reference and models live in the seeded OpenCode
config (``opencode-config`` seed); the key value only ever exists in the
sandbox environment.  ``--auto`` (OpenCode's permission skip) is passed only
when the transport grants the one fresh production E2B claim (ADR-0008).
"""

from __future__ import annotations

import json
import re
import threading
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Mapping, Protocol, Sequence

from .herdr_runtime import HerdrRuntimeError, HerdrUnsupportedOperation, _claim_fresh_e2b_permission_bypass
from .runtime import CredentialMetadata, RuntimeSession, RuntimeStatus

# Pinned to the npm ``latest`` release verified for SDF (2026-10-22).  The
# runtime depends on ``opencode run`` flags (``--format json``, ``--auto``,
# ``--session``, ``--model``) and its JSON event shapes, so a different
# version fails closed instead of being trusted blindly.
OPENCODE_VERSION = "1.19.4"
AGENT_KIND = "opencode"
_EXIT_MARKER = "SDF_OPENCODE_EXIT="
_TIMEOUT_EXITS = frozenset({124, 137})
_KILL_AFTER_S = 10
_ATTEMPT_ID = re.compile(r"[A-Za-z0-9_-]+")
_VERSION = re.compile(r"(?:^|\s)v?(\d+\.\d+\.\d+)(?:\s|$)")

# ``sh -c`` wrapper: cd into the Attempt workspace, bound the turn with
# coreutils ``timeout`` (so it ends before the transport's watchdog would kill
# the whole sandbox), and always exit 0 with an explicit exit marker so a
# failed turn is reported by this runtime, not swallowed by the transport.
_WRAPPER = (
    'dir=$1; secs=$2; err=$3; shift 3; cd "$dir" || { echo ' + _EXIT_MARKER + '97; exit 0; }; '
    'timeout --kill-after=' + str(_KILL_AFTER_S) + ' "$secs" "$@" 2>"$err"; rc=$?; '
    'echo ' + _EXIT_MARKER + '$rc; if [ "$rc" -ne 0 ]; then tail -c 2000 "$err"; fi; exit 0'
)


class OpenCodeRuntimeError(HerdrRuntimeError):
    """OpenCode or its sandbox returned an unusable result."""


class SandboxExecTransport(Protocol):
    def exec_argv(self, argv: Sequence[str], timeout_ms: int) -> str: ...


@dataclass(slots=True)
class OpenCodeUsage:
    """Token/cost totals as reported by OpenCode's ``step_finish`` events."""

    steps: int = 0
    total_tokens: int = 0
    input_tokens: int = 0
    output_tokens: int = 0
    reasoning_tokens: int = 0
    cache_read_tokens: int = 0
    cost: float = 0.0

    def add(self, part: Mapping[str, Any]) -> None:
        tokens = part.get("tokens") if isinstance(part.get("tokens"), Mapping) else {}
        cache = tokens.get("cache") if isinstance(tokens.get("cache"), Mapping) else {}
        self.steps += 1
        self.total_tokens += _int(tokens.get("total"))
        self.input_tokens += _int(tokens.get("input"))
        self.output_tokens += _int(tokens.get("output"))
        self.reasoning_tokens += _int(tokens.get("reasoning"))
        self.cache_read_tokens += _int(cache.get("read"))
        cost = part.get("cost")
        if isinstance(cost, (int, float)) and not isinstance(cost, bool):
            self.cost += float(cost)

    def as_dict(self) -> dict[str, Any]:
        return {
            "steps": self.steps,
            "total_tokens": self.total_tokens,
            "input_tokens": self.input_tokens,
            "output_tokens": self.output_tokens,
            "reasoning_tokens": self.reasoning_tokens,
            "cache_read_tokens": self.cache_read_tokens,
            "cost": round(self.cost, 6),
        }


@dataclass(slots=True)
class _Binding:
    attempt_id: str
    workspace: str
    auto: bool
    opencode_session: str | None = None
    usage: OpenCodeUsage = field(default_factory=OpenCodeUsage)


def parse_events(raw: str) -> tuple[tuple[str, ...], str | None, OpenCodeUsage, tuple[str, ...]]:
    """Parse ``opencode run --format json`` stdout.

    Returns (output lines, OpenCode session id, usage, error messages).
    Non-JSON lines are kept verbatim as output.
    """

    lines: list[str] = []
    errors: list[str] = []
    session: str | None = None
    usage = OpenCodeUsage()
    for line in raw.splitlines():
        if not line.strip():
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            lines.append(line)
            continue
        if not isinstance(event, Mapping):
            lines.append(line)
            continue
        kind = event.get("type")
        part = event.get("part") if isinstance(event.get("part"), Mapping) else {}
        sid = event.get("sessionID", part.get("sessionID"))
        if isinstance(sid, str) and sid:
            session = sid
        if kind == "text":
            text = part.get("text")
            if isinstance(text, str) and text.strip():
                lines.append(text)
        elif kind == "tool_use":
            state = part.get("state") if isinstance(part.get("state"), Mapping) else {}
            lines.append(f"[tool {part.get('tool', '?')}: {state.get('status', '?')}]")
        elif kind == "step_finish":
            usage.add(part)
        elif kind == "error":
            detail = event.get("error", part)
            message = detail.get("data", detail) if isinstance(detail, Mapping) else detail
            if isinstance(message, Mapping):
                message = message.get("message", json.dumps(message, sort_keys=True))
            errors.append(str(message)[:500])
            lines.append(f"[error {str(message)[:500]}]")
    return tuple(lines), session, usage, tuple(errors)


class OpenCodeHeadlessRuntime:
    """``AgentRuntime`` for the ``opencode`` agent kind, one turn per ``send``."""

    def __init__(
        self,
        *,
        transport: SandboxExecTransport,
        turn_timeout_s: int = 600,
        control_timeout_ms: int = 60_000,
        expected_version: str | None = OPENCODE_VERSION,
        binary: str = "opencode",
        model: str | None = None,
        credentials: Mapping[str, CredentialMetadata] | None = None,
    ) -> None:
        if turn_timeout_s <= 0 or control_timeout_ms <= 0:
            raise ValueError("timeouts must be positive")
        if not callable(getattr(transport, "exec_argv", None)):
            raise ValueError("OpenCode runtime needs a transport with exec_argv")
        self._transport = transport
        self._turn_timeout_s = turn_timeout_s
        self._control_timeout_ms = control_timeout_ms
        self.expected_version = expected_version
        self._binary = binary
        self._model = model
        if credentials is None:
            credentials = getattr(transport, "credential_metadata", None)
        self.credentials = dict(credentials) if credentials is not None else None
        self._environment_close = getattr(transport, "close", None)
        self._workspaces: dict[str, str] = {}
        self._bindings: dict[str, _Binding] = {}
        self._sessions: dict[str, RuntimeSession] = {}
        self._lock = threading.Lock()
        self._counter = 0
        self._closed = False

    # --- workspace ---------------------------------------------------------
    def bind_workspace(self, attempt_id: str, workspace: Path) -> None:
        path = Path(workspace).expanduser().resolve()
        if not path.is_dir():
            raise ValueError("workspace must be an existing directory")
        stage = getattr(self._transport, "stage_workspace", None)
        if not callable(stage):
            raise OpenCodeRuntimeError("OpenCode runtime needs a transport that stages workspaces")
        self._workspaces[attempt_id] = stage(attempt_id, path)

    def collect_workspace(self, attempt_id: str, workspace: Path) -> None:
        collect = getattr(self._transport, "collect_workspace", None)
        if callable(collect):
            collect(attempt_id, Path(workspace).expanduser().resolve())

    # --- lifecycle ---------------------------------------------------------
    def start(self, *, attempt_id: str, agent: str) -> RuntimeSession:
        if not attempt_id.strip() or not agent.strip():
            raise ValueError("attempt_id and agent must be non-empty")
        if agent != AGENT_KIND:
            raise OpenCodeRuntimeError(f"OpenCode runtime cannot start agent kind {agent!r}")
        if not _ATTEMPT_ID.fullmatch(attempt_id):
            raise ValueError("attempt_id must contain only letters, digits, underscores, or hyphens")
        credential = None
        if self.credentials is not None:
            credential = self.credentials.get(agent)
            if credential is None:
                raise OpenCodeRuntimeError(
                    "no resolved Agent Credential for opencode; it was not injected at sandbox creation; "
                    "set SDF_CREDENTIAL_MODE_OPENCODE"
                )
        existing = next((sid for sid, b in self._bindings.items() if b.attempt_id == attempt_id), None)
        if existing is not None:
            return self._sessions[existing]
        workspace = self._workspaces.get(attempt_id)
        if workspace is None:
            raise OpenCodeRuntimeError("bind_workspace must stage the Attempt workspace before start")
        if self.expected_version is not None:
            self.require_compatible()
        # ADR-0008/0011: claim at the start boundary, after setup succeeded.
        auto = _claim_fresh_e2b_permission_bypass(self._transport)
        with self._lock:
            self._counter += 1
            session_id = f"opencode-{attempt_id}-{self._counter}"
        self._bindings[session_id] = _Binding(attempt_id, workspace, auto)
        session = RuntimeSession(session_id, attempt_id, agent, RuntimeStatus.CREATED, credential=credential)
        self._sessions[session_id] = session
        return session

    def installed_version(self) -> str:
        raw = self._transport.exec_argv([self._binary, "--version"], self._control_timeout_ms)
        match = _VERSION.search(raw.strip())
        if match is None:
            raise OpenCodeRuntimeError("OpenCode version response was not recognized")
        return match.group(1)

    def require_compatible(self) -> str:
        version = self.installed_version()
        if version != self.expected_version:
            raise OpenCodeRuntimeError(
                f"OpenCode {version} is installed but SDF is pinned to {self.expected_version}"
            )
        return version

    def permission_skipping(self, session_id: str) -> bool:
        return self._known(session_id)[0].auto

    def usage(self, session_id: str) -> dict[str, Any]:
        return self._known(session_id)[0].usage.as_dict()

    def command(self, session_id: str, input_text: str) -> list[str]:
        """The exact argv a turn runs (no credential material: names only)."""

        binding, _ = self._known(session_id)
        if not input_text.strip():
            raise ValueError("input_text must be non-empty")
        argv = [self._binary, "run", "--format", "json"]
        if self._model is not None:
            argv += ["--model", self._model]
        if binding.auto:
            argv.append("--auto")
        if binding.opencode_session is not None:
            argv += ["--session", binding.opencode_session]
        # ``--`` keeps a prompt that starts with ``-`` a message (verified on 1.19.4).
        argv += ["--", input_text]
        err = f"/tmp/sdf-opencode-{binding.attempt_id}.err"
        return ["sh", "-c", _WRAPPER, "sdf-opencode", binding.workspace, str(self._turn_timeout_s), err, *argv]

    def send(self, session_id: str, input_text: str) -> RuntimeSession:
        binding, current = self._known(session_id)
        if current.status in (RuntimeStatus.CANCELLED, RuntimeStatus.TERMINATED):
            return current
        argv = self.command(session_id, input_text)
        self._set(session_id, RuntimeStatus.RUNNING, current.output)
        # The transport watchdog sits after OpenCode's own timeout + kill grace.
        budget_ms = (self._turn_timeout_s + _KILL_AFTER_S + 20) * 1000
        raw = self._transport.exec_argv(argv, budget_ms)
        body, rc = _split_exit(raw)
        lines, oc_session, usage, errors = parse_events(body)
        if oc_session is not None:
            binding.opencode_session = oc_session
        for name in OpenCodeUsage.__slots__:
            setattr(binding.usage, name, getattr(binding.usage, name) + getattr(usage, name))
        output = current.output + lines
        if rc == 0 and not errors:
            return self._set(session_id, RuntimeStatus.COMPLETED, output)
        self._set(session_id, RuntimeStatus.TERMINATED if rc in _TIMEOUT_EXITS else current.status, output)
        if rc in _TIMEOUT_EXITS:
            raise OpenCodeRuntimeError(f"OpenCode turn exceeded {self._turn_timeout_s}s and was killed")
        if rc is None:
            raise OpenCodeRuntimeError("OpenCode turn ended without an exit marker")
        detail = "; ".join(errors) or (lines[-1] if lines else "")
        raise OpenCodeRuntimeError(f"OpenCode run failed (exit {rc}): {detail[-1000:]}")

    def stream(self, session_id: str) -> tuple[str, ...]:
        return self._known(session_id)[1].output

    def status(self, session_id: str) -> RuntimeSession:
        return self._known(session_id)[1]

    def cancel(self, session_id: str) -> RuntimeSession:
        _, current = self._known(session_id)
        if not self._closed:
            # ``[o]`` keeps pkill from matching (and killing) its own ``sh -c``.
            try:
                self._transport.exec_argv(["sh", "-c", "pkill -f '[o]pencode run' || true"], self._control_timeout_ms)
            finally:
                # Sandbox destruction is the descendant-cleanup boundary.
                self._close_environment()
        return self._set(session_id, RuntimeStatus.CANCELLED, current.output)

    def terminate(self, session_id: str) -> RuntimeSession:
        _, current = self._known(session_id)
        self._close_environment()
        return self._set(session_id, RuntimeStatus.TERMINATED, current.output)

    def reconnect(self, session_id: str) -> RuntimeSession:
        raise HerdrUnsupportedOperation("headless OpenCode turns are not reconnectable; start a new Attempt")

    # --- helpers -----------------------------------------------------------
    def _close_environment(self) -> None:
        if self._closed:
            return
        if callable(self._environment_close):
            self._environment_close(self._control_timeout_ms)
        self._closed = True

    def _known(self, session_id: str) -> tuple[_Binding, RuntimeSession]:
        try:
            return self._bindings[session_id], self._sessions[session_id]
        except KeyError as exc:
            raise KeyError(f"unknown OpenCode session: {session_id}") from exc

    def _set(self, session_id: str, status: RuntimeStatus, output: tuple[str, ...]) -> RuntimeSession:
        current = self._sessions[session_id]
        updated = RuntimeSession(current.session_id, current.attempt_id, current.agent, status, output, credential=current.credential)
        self._sessions[session_id] = updated
        return updated


def _split_exit(raw: str) -> tuple[str, int | None]:
    lines = raw.splitlines()
    for index in range(len(lines) - 1, -1, -1):
        if lines[index].startswith(_EXIT_MARKER):
            try:
                rc = int(lines[index][len(_EXIT_MARKER):].strip())
            except ValueError:
                return raw, None
            body = lines[:index]
            tail = lines[index + 1:]
            return "\n".join(body + tail), rc
    return raw, None


def _int(value: Any) -> int:
    return value if isinstance(value, int) and not isinstance(value, bool) else 0


__all__ = [
    "OPENCODE_VERSION",
    "OpenCodeHeadlessRuntime",
    "OpenCodeRuntimeError",
    "OpenCodeUsage",
    "parse_events",
]
