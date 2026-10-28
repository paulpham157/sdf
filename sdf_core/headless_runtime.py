"""Shared headless runtime for OpenAI-compatible CLI agent kinds (ADR-0013).

Qwen Code, Goose and Aider all run one non-interactive turn per process, like
Pi (ADR-0012), so they share this base: the transport (E2B or Daytona) owns
sandbox creation, credential seeding, workspace staging/collection and
teardown; the runtime only runs ``sh -c <wrapper>`` turns over ``exec_argv``.

The wrapper, per turn and before the agent starts:

* unsets every variable with one of the kind's prefixes (its own config knobs
  and ``OPENAI_*``), then exports the kind's fixed offline/telemetry switches;
* ``cd``s into the staged workspace and refuses (exit marker, agent never
  started) if the workspace holds an entry the agent would load as project
  config, or an ancestor holds a config file the agent searches upward for -
  the workspace is untrusted content and the agent itself could create one;
* checks the endpoint and model sandbox variables, and for kinds that only read
  ``OPENAI_API_KEY`` exports it from the variable the seed recorded by NAME
  (the value is never on argv or disk);
* runs the agent under ``timeout`` with the prompt on stdin (or a private file),
  prints its stdout and an explicit exit marker.

Write-capable modes are enabled only after the one fresh production E2B claim
(ADR-0008); elsewhere each kind runs in its own read-only / no-tool mode.
"""

from __future__ import annotations

import re
import shlex
import threading
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, ClassVar, Mapping, Protocol, Sequence

from .herdr_runtime import HerdrRuntimeError, HerdrUnsupportedOperation, _claim_fresh_e2b_permission_bypass
from .runtime import CredentialMetadata, RuntimeSession, RuntimeStatus

_TIMEOUT_EXITS = frozenset({124, 137})
PROJECT_CONFIG_EXIT = 96
NO_WORKSPACE_EXIT = 97
NO_ENDPOINT_EXIT = 95
NO_KEY_EXIT = 94
_SETUP_EXITS = frozenset({PROJECT_CONFIG_EXIT, NO_WORKSPACE_EXIT, NO_ENDPOINT_EXIT, NO_KEY_EXIT})
KILL_AFTER_S = 10
_ATTEMPT_ID = re.compile(r"[A-Za-z0-9_-]+")
MODEL_ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9._@/+-]{0,199}")
_VERSION = re.compile(r"(?:^|\s)v?(\d+\.\d+\.\d+)(?:\s|$)")
_NAME = re.compile(r"[A-Z][A-Z0-9_]*")
_ENTRY = re.compile(r"[A-Za-z0-9._*/-]+")


class SandboxExecTransport(Protocol):
    def exec_argv(self, argv: Sequence[str], timeout_ms: int) -> str: ...


@dataclass(slots=True)
class HeadlessUsage:
    """Token totals for one Attempt; ``served_models`` only from the endpoint."""

    messages: int = 0
    input_tokens: int = 0
    output_tokens: int = 0
    cache_read_tokens: int = 0
    total_tokens: int = 0
    cost: float = 0.0
    requested_models: list[str] = field(default_factory=list)
    served_models: list[str] = field(default_factory=list)

    def add_models(self, requested: Any = None, served: Any = None) -> None:
        for value, bucket in ((requested, self.requested_models), (served, self.served_models)):
            if isinstance(value, str) and value and value not in bucket:
                bucket.append(value)

    def merge(self, other: "HeadlessUsage") -> None:
        for name in ("messages", "input_tokens", "output_tokens", "cache_read_tokens", "total_tokens", "cost"):
            setattr(self, name, getattr(self, name) + getattr(other, name))
        self.add_models()
        for name in ("requested_models", "served_models"):
            bucket = getattr(self, name)
            bucket.extend(value for value in getattr(other, name) if value not in bucket)

    def as_dict(self) -> dict[str, Any]:
        return {
            "messages": self.messages,
            "input_tokens": self.input_tokens,
            "output_tokens": self.output_tokens,
            "cache_read_tokens": self.cache_read_tokens,
            "total_tokens": self.total_tokens or self.input_tokens + self.output_tokens,
            "cost": round(self.cost, 6),
            "requested_models": list(self.requested_models),
            "served_models": list(self.served_models),
        }


@dataclass(slots=True)
class HeadlessBinding:
    attempt_id: str
    workspace: str
    auto: bool
    turns: int = 0
    usage: HeadlessUsage = field(default_factory=HeadlessUsage)
    # Per-kind continuation state (session id, message count, ...).
    state: dict[str, Any] = field(default_factory=dict)


@dataclass(frozen=True, slots=True)
class TurnResult:
    lines: tuple[str, ...]
    errors: tuple[str, ...] = ()


def build_wrapper(
    *,
    agent: str,
    unset_prefixes: Sequence[str],
    unset_names: Sequence[str] = (),
    exports: Sequence[str] = (),
    refused_entries: Sequence[str] = (),
    ancestor_entries: Sequence[str] = (),
    key_ref_file: str | None = None,
    command_tail: str = '"$@"',
    cleanup_entries: Sequence[str] = (),
) -> str:
    """The ``sh -c`` turn wrapper.

    Positional args: workspace, turn seconds, stdout file, stderr file, prompt
    file, model (empty: ``$SDF_<AGENT>_MODEL``), prompt, then the agent argv;
    the base URL is ``$SDF_<AGENT>_BASE_URL``.  ``command_tail`` is the
    shell words that run the agent (it may use ``$model`` / ``$url``).  Every
    piece is a fixed literal from this module; nothing operator-controlled is
    spliced into the script.
    """

    if not re.fullmatch(r"[a-z]+", agent):
        raise ValueError("bad agent kind")
    upper = agent.upper()
    marker = f"SDF_{upper}_EXIT="
    for prefix in unset_prefixes:
        if not _NAME.fullmatch(prefix.rstrip("_")):
            raise ValueError("bad prefix")
    for name in unset_names:
        if not _NAME.fullmatch(name):
            raise ValueError("bad variable name")
    for entry in (*refused_entries, *ancestor_entries, *cleanup_entries):
        if not _ENTRY.fullmatch(entry) or ".." in entry.split("/"):
            raise ValueError("bad entry")

    def leave(code: int) -> str:
        return f"{{ echo {marker}{code}; exit 0; }}"

    pattern = "\\|".join(prefix for prefix in unset_prefixes)
    parts = [
        f'dir=$1; secs=$2; out=$3; err=$4; pf=$5; model=${{6:-$SDF_{upper}_MODEL}}; url=$SDF_{upper}_BASE_URL; '
        'prompt=$7; shift 7; ',
        f'for v in $(env | sed -n "s/^\\(\\({pattern}\\)[A-Za-z0-9_]*\\)=.*/\\1/p"); do unset "$v"; done; ',
    ]
    if unset_names:
        parts.append(f"unset {' '.join(unset_names)}; ")
    if exports:
        parts.append(f"export {' '.join(exports)}; ")
    parts.append(f'cd "$dir" || {leave(NO_WORKSPACE_EXIT)}; ')
    if refused_entries:
        # Unquoted on purpose: ``.aider*`` globs; an unmatched glob stays literal.
        parts.append(
            f'for p in {" ".join(refused_entries)}; do if [ -e "$p" ] || [ -L "$p" ]; then {leave(PROJECT_CONFIG_EXIT)}; fi; done; '
        )
    if ancestor_entries:
        checks = " || ".join(f'[ -e "$d/{e}" ] || [ -L "$d/{e}" ]' for e in ancestor_entries)
        parts.append(
            'd=$(pwd -P); while :; do '
            f'if {checks}; then {leave(PROJECT_CONFIG_EXIT)}; fi; '
            '[ "$d" = / ] && break; d=$(dirname "$d"); done; '
        )
    parts.append(f'[ -n "$model" ] && [ -n "$url" ] || {leave(NO_ENDPOINT_EXIT)}; ')
    if key_ref_file is not None:
        if not re.fullmatch(r"[a-z0-9-]+", key_ref_file):
            raise ValueError("bad key ref file")
        parts.append(
            f'kn=$(cat "$HOME/.config/sdf/{key_ref_file}" 2>/dev/null); '
            f'case "$kn" in ""|[!A-Z]*|*[!A-Z0-9_]*) {leave(NO_KEY_EXIT)};; esac; '
            # Safe: ``kn`` is validated as an identifier above.
            'eval "k=\\${$kn:-}"; '
            f'[ -n "$k" ] || {leave(NO_KEY_EXIT)}; '
            'export OPENAI_API_KEY="$k"; unset k kn; '
        )
    parts.append(
        '(umask 077; printf "%s" "$prompt" > "$pf"); '
        f'timeout --kill-after={KILL_AFTER_S} "$secs" {command_tail} <"$pf" >"$out" 2>"$err"; rc=$?; '
    )
    if cleanup_entries:
        parts.append(f"rm -rf -- {' '.join(cleanup_entries)}; ")
    parts.append(
        f'cat "$out"; printf "\\n%s%s\\n" {marker} "$rc"; '
        'if [ "$rc" -ne 0 ]; then tail -c 2000 "$err"; fi; exit 0'
    )
    return "".join(parts)


def split_exit(raw: str, marker: str) -> tuple[str, int | None, str]:
    """``(stdout body, exit code, stderr tail printed after the marker)``."""

    lines = raw.splitlines()
    for index in range(len(lines) - 1, -1, -1):
        if lines[index].startswith(marker):
            try:
                rc = int(lines[index][len(marker):].strip())
            except ValueError:
                return raw, None, ""
            return "\n".join(lines[:index]), rc, "\n".join(lines[index + 1:]).strip()
    return raw, None, ""


def as_int(value: Any) -> int:
    return value if isinstance(value, int) and not isinstance(value, bool) else 0


class HeadlessAgentRuntime:
    """``AgentRuntime`` base for one OpenAI-compatible CLI kind, one turn per ``send``."""

    AGENT_KIND: ClassVar[str]
    LABEL: ClassVar[str]
    VERSION: ClassVar[str]
    ADR: ClassVar[str] = "ADR-0013"
    ERROR: ClassVar[type[HerdrRuntimeError]] = HerdrRuntimeError
    WRAPPER: ClassVar[str]
    # Workspace entries refused on the host before staging (and in the box).
    PROJECT_CONFIG_ENTRIES: ClassVar[tuple[str, ...]] = ()
    # ``pkill -f`` pattern; the first letter in brackets so it never matches itself.
    CANCEL_PATTERN: ClassVar[str]
    # Private per-Attempt scratch files (output, stderr, prompt, history).
    SCRATCH_DIR: ClassVar[str] = "/tmp"

    def __init__(
        self,
        *,
        transport: SandboxExecTransport,
        turn_timeout_s: int = 600,
        control_timeout_ms: int = 60_000,
        expected_version: str | None = None,
        binary: str | None = None,
        model: str | None = None,
        credentials: Mapping[str, CredentialMetadata] | None = None,
    ) -> None:
        if turn_timeout_s <= 0 or control_timeout_ms <= 0:
            raise ValueError("timeouts must be positive")
        if not callable(getattr(transport, "exec_argv", None)):
            raise ValueError(f"{self.LABEL} runtime needs a transport with exec_argv")
        if model is not None and not MODEL_ID.fullmatch(model):
            raise ValueError("model must be a bare model id without ':'")
        self._transport = transport
        self._turn_timeout_s = turn_timeout_s
        self._control_timeout_ms = control_timeout_ms
        self.expected_version = self.VERSION if expected_version is None else expected_version
        self._binary = binary or self.AGENT_KIND
        # None: the sandbox's SDF_<AGENT>_MODEL (the plan's resolved model).
        self._model = model
        if credentials is None:
            credentials = getattr(transport, "credential_metadata", None)
        self.credentials = dict(credentials) if credentials is not None else None
        self._environment_close = getattr(transport, "close", None)
        self._workspaces: dict[str, str] = {}
        self._bindings: dict[str, HeadlessBinding] = {}
        self._sessions: dict[str, RuntimeSession] = {}
        self._lock = threading.Lock()
        self._counter = 0
        self._closed = False

    # --- per-kind hooks ------------------------------------------------------
    def agent_argv(self, binding: HeadlessBinding, prompt_file: str) -> list[str]:
        raise NotImplementedError

    def parse_turn(self, binding: HeadlessBinding, body: str) -> TurnResult:
        raise NotImplementedError

    @classmethod
    def workspace_has_project_config(cls, workspace: Path) -> str | None:
        """The first entry in ``workspace`` the agent would load as config, else None."""

        root = Path(workspace)
        for pattern in cls.PROJECT_CONFIG_ENTRIES:
            for path in sorted(root.glob(pattern)) if "*" in pattern else [root / pattern]:
                if path.exists() or path.is_symlink():
                    return path.name
        return None

    # --- workspace -----------------------------------------------------------
    def bind_workspace(self, attempt_id: str, workspace: Path) -> None:
        path = Path(workspace).expanduser().resolve()
        if not path.is_dir():
            raise ValueError("workspace must be an existing directory")
        entry = self.workspace_has_project_config(path)
        if entry is not None:
            raise self.ERROR(
                f"the Attempt workspace contains {entry}, which {self.LABEL} would load as project "
                f"config; SDF refuses to run it there ({self.ADR})"
            )
        stage = getattr(self._transport, "stage_workspace", None)
        if not callable(stage):
            raise self.ERROR(f"{self.LABEL} runtime needs a transport that stages workspaces")
        self._workspaces[attempt_id] = stage(attempt_id, path)

    def collect_workspace(self, attempt_id: str, workspace: Path) -> None:
        collect = getattr(self._transport, "collect_workspace", None)
        if callable(collect):
            collect(attempt_id, Path(workspace).expanduser().resolve())

    # --- lifecycle -----------------------------------------------------------
    def start(self, *, attempt_id: str, agent: str) -> RuntimeSession:
        if not attempt_id.strip() or not agent.strip():
            raise ValueError("attempt_id and agent must be non-empty")
        if agent != self.AGENT_KIND:
            raise self.ERROR(f"{self.LABEL} runtime cannot start agent kind {agent!r}")
        if not _ATTEMPT_ID.fullmatch(attempt_id):
            raise ValueError("attempt_id must contain only letters, digits, underscores, or hyphens")
        credential = None
        if self.credentials is not None:
            credential = self.credentials.get(agent)
            if credential is None:
                raise self.ERROR(
                    f"no resolved Agent Credential for {agent}; it was not injected at sandbox creation; "
                    f"set SDF_CREDENTIAL_MODE_{agent.upper()}"
                )
        existing = next((sid for sid, b in self._bindings.items() if b.attempt_id == attempt_id), None)
        if existing is not None:
            return self._sessions[existing]
        workspace = self._workspaces.get(attempt_id)
        if workspace is None:
            raise self.ERROR("bind_workspace must stage the Attempt workspace before start")
        if self.expected_version:
            self.require_compatible()
        # ADR-0008: claim at the start boundary, after setup succeeded.
        auto = _claim_fresh_e2b_permission_bypass(self._transport)
        with self._lock:
            self._counter += 1
            session_id = f"{self.AGENT_KIND}-{attempt_id}-{self._counter}"
        self._bindings[session_id] = HeadlessBinding(attempt_id, workspace, auto)
        session = RuntimeSession(session_id, attempt_id, agent, RuntimeStatus.CREATED, credential=credential)
        self._sessions[session_id] = session
        return session

    def installed_version(self) -> str:
        raw = self._transport.exec_argv(["sh", "-c", '"$0" --version 2>&1', self._binary], self._control_timeout_ms)
        match = _VERSION.search(raw.strip())
        if match is None:
            raise self.ERROR(f"{self.LABEL} version response was not recognized")
        return match.group(1)

    def require_compatible(self) -> str:
        version = self.installed_version()
        if version != self.expected_version:
            raise self.ERROR(f"{self.LABEL} {version} is installed but SDF is pinned to {self.expected_version}")
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
        base = self.scratch(binding)
        return [
            "sh", "-c", self.WRAPPER, f"sdf-{self.AGENT_KIND}", binding.workspace, str(self._turn_timeout_s),
            f"{base}.out", f"{base}.err", f"{base}.prompt", self._model or "", input_text,
            *self.agent_argv(binding, f"{base}.prompt"),
        ]

    def send(self, session_id: str, input_text: str) -> RuntimeSession:
        binding, current = self._known(session_id)
        if current.status in (RuntimeStatus.CANCELLED, RuntimeStatus.TERMINATED):
            return current
        argv = self.command(session_id, input_text)
        self._set(session_id, RuntimeStatus.RUNNING, current.output)
        # The transport watchdog sits after the agent's own timeout + kill grace.
        budget_ms = (self._turn_timeout_s + KILL_AFTER_S + 20) * 1000
        raw = self._transport.exec_argv(argv, budget_ms)
        body, rc, stderr_tail = split_exit(raw, self.marker())
        result = self.parse_turn(binding, body) if rc not in _SETUP_EXITS else TurnResult(())
        if rc is not None and rc not in _SETUP_EXITS:
            binding.turns += 1
        output = current.output + result.lines
        if rc == 0 and not result.errors:
            return self._set(session_id, RuntimeStatus.COMPLETED, output)
        self._set(session_id, RuntimeStatus.TERMINATED if rc in _TIMEOUT_EXITS else current.status, output)
        label, upper = self.LABEL, self.AGENT_KIND.upper()
        if rc in _TIMEOUT_EXITS:
            raise self.ERROR(f"{label} turn exceeded {self._turn_timeout_s}s and was killed")
        if rc == PROJECT_CONFIG_EXIT:
            raise self.ERROR(
                f"the Attempt workspace (or an ancestor) gained an entry {label} would load as project config; "
                f"SDF refuses to run it ({self.ADR})"
            )
        if rc == NO_WORKSPACE_EXIT:
            raise self.ERROR("the staged Attempt workspace is missing in the sandbox")
        if rc == NO_ENDPOINT_EXIT:
            raise self.ERROR(f"no {label} endpoint: SDF_{upper}_MODEL or SDF_{upper}_BASE_URL is not set in the sandbox")
        if rc == NO_KEY_EXIT:
            raise self.ERROR(f"no {label} key: the seeded key variable is missing or empty in the sandbox")
        if rc is None:
            raise self.ERROR(f"{label} turn ended without an exit marker")
        detail = "; ".join((*result.errors, stderr_tail[-600:]) if stderr_tail else result.errors)
        detail = detail or (result.lines[-1] if result.lines else "")
        raise self.ERROR(f"{label} run failed (exit {rc}): {detail[-1000:]}")

    def stream(self, session_id: str) -> tuple[str, ...]:
        return self._known(session_id)[1].output

    def status(self, session_id: str) -> RuntimeSession:
        return self._known(session_id)[1]

    def cancel(self, session_id: str) -> RuntimeSession:
        _, current = self._known(session_id)
        if not self._closed:
            try:
                self._transport.exec_argv(
                    ["sh", "-c", f"pkill -f {shlex.quote(self.CANCEL_PATTERN)} || true"], self._control_timeout_ms
                )
            finally:
                # Sandbox destruction is the descendant-cleanup boundary.
                self._close_environment()
        return self._set(session_id, RuntimeStatus.CANCELLED, current.output)

    def terminate(self, session_id: str) -> RuntimeSession:
        _, current = self._known(session_id)
        self._close_environment()
        return self._set(session_id, RuntimeStatus.TERMINATED, current.output)

    def reconnect(self, session_id: str) -> RuntimeSession:
        raise HerdrUnsupportedOperation(f"headless {self.LABEL} turns are not reconnectable; start a new Attempt")

    # --- helpers -------------------------------------------------------------
    def scratch(self, binding: HeadlessBinding) -> str:
        return f"{self.SCRATCH_DIR}/sdf-{self.AGENT_KIND}-{binding.attempt_id}"

    @classmethod
    def marker(cls) -> str:
        return f"SDF_{cls.AGENT_KIND.upper()}_EXIT="

    def _close_environment(self) -> None:
        if self._closed:
            return
        if callable(self._environment_close):
            self._environment_close(self._control_timeout_ms)
        self._closed = True

    def _known(self, session_id: str) -> tuple[HeadlessBinding, RuntimeSession]:
        try:
            return self._bindings[session_id], self._sessions[session_id]
        except KeyError as exc:
            raise KeyError(f"unknown {self.LABEL} session: {session_id}") from exc

    def _set(self, session_id: str, status: RuntimeStatus, output: tuple[str, ...]) -> RuntimeSession:
        current = self._sessions[session_id]
        updated = RuntimeSession(current.session_id, current.attempt_id, current.agent, status, output, credential=current.credential)
        self._sessions[session_id] = updated
        return updated


__all__ = [
    "HeadlessAgentRuntime",
    "HeadlessBinding",
    "HeadlessUsage",
    "TurnResult",
    "build_wrapper",
    "split_exit",
]
