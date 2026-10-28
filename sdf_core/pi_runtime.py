"""Headless Pi runtime over a persistent sandbox transport (ADR-0012).

Pi (``@earendil-works/pi-coding-agent``) needs no terminal multiplexer:
``pi -p --mode json`` runs one non-interactive turn and prints JSON events.
Like the OpenCode runtime (ADR-0011) this drives Pi through the transport's
``exec_argv``; the transport (E2B or Daytona) still owns sandbox creation,
credential seeding, workspace staging/collection and teardown.

Provider, base URL, model and key reference live in the seeded
``~/.pi/agent/models.json`` (``pi-models`` seed); ``apiKey`` there is the key
variable's NAME, so the value only exists in the sandbox environment.

Hardening, because the Attempt workspace is untrusted content:

* Pi merges ``<cwd>/.pi/settings.json`` and loads
  ``<cwd>/.pi/{extensions,skills,prompts,themes,mcp,SYSTEM.md,APPEND_SYSTEM.md}``.
  Pi 1.x adds ``--no-approve`` (ignore project-local files), which every turn
  passes, but a workspace with ``.pi`` is still refused, on the host before
  staging and again in the sandbox before every turn (the agent itself could
  create one), so no Pi release's project-trust rules are relied on alone.
* Extension, skill, prompt-template and theme discovery and built-in MCP
  (``--no-mcp``) are disabled by flag.
* Every ``PI_*`` variable is unset per turn (``PI_CODING_AGENT_DIR`` would move
  models.json/settings/extensions); ``PI_OFFLINE``, ``PI_SKIP_VERSION_CHECK``
  and ``PI_TELEMETRY=0`` are then set so Pi makes no calls besides inference.
* The prompt goes in on stdin: Pi has no ``--`` and would read a leading
  ``-`` or ``@`` argument as a flag or a file.
* Pi has no permission system.  Its write tools (``bash``, ``edit``,
  ``write``) are enabled only after the one fresh production E2B claim
  (ADR-0008); elsewhere Pi gets ``--tools read,grep,find,ls``.
* Extensions (ADR-0017): discovery stays off; only the image-baked, pinned
  packages in ``PI_EXTENSIONS`` load, each by explicit ``-e`` path, chosen by
  ``SDF_PI_EXTENSIONS``.  Read-only turns load only the ones marked
  ``read_only`` (``--tools`` stays a hard allowlist either way).  Their
  versions are checked in the sandbox before the session starts.
"""

from __future__ import annotations

import json
import os
import re
import threading
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Mapping, Protocol, Sequence

from .herdr_runtime import HerdrRuntimeError, HerdrUnsupportedOperation, _claim_fresh_e2b_permission_bypass
from .runtime import CredentialMetadata, RuntimeSession, RuntimeStatus

# Pinned to the npm ``latest`` release of ``@earendil-works/pi-coding-agent``
# verified for SDF (2026-10-28).  The runtime depends on ``pi -p --mode json``
# flags and event shapes, the models.json schema (1.x: ``apiKey`` ``$NAME``
# reads an environment variable) and the project-config layout of that release.
PI_VERSION = "1.4.0"
AGENT_KIND = "pi"
# Provider id in the seeded models.json.
PI_PROVIDER = "sdf"
READ_ONLY_TOOLS = "read,grep,find,ls"


@dataclass(frozen=True)
class PiExtension:
    """One image-baked Pi extension (ADR-0017)."""

    version: str
    # npm ``dist.integrity`` of the package tarball; the template's
    # package-lock.json must carry the same value (``npm ci`` verifies it).
    integrity: str
    # Extension entry point, relative to the package directory.
    entry: str
    # True when the extension may load in a read-only turn: it registers no
    # write tool and has no side effects beyond refusing tool calls.
    read_only: bool


# Where the template installs them (infra/e2b/herdr-agents/pi-extensions).
PI_EXTENSIONS_DIR = "/opt/sdf-pi-extensions"
# The allowlist, in load order.  Nothing else can be loaded: ``-e`` paths are
# built from this map only.
PI_EXTENSIONS: dict[str, PiExtension] = {
    # Guardrail hooks (blocks force-push, hard reset, rm -rf /, dev servers).
    "@cad0p/pi-steering": PiExtension(
        "0.2.0",
        "sha512-LPpxSzl2wTjOgC/MtSAtVC4Qwp9xs29x/8oJ1gPY6r/99O5EOY7FBmdLPoRrZnKP7SaoPuVwI+eLZTzy9H0SHQ==",
        "src/index.ts", read_only=True,
    ),
    # ``apply_patch`` tool (replaces ``edit``/``write``): a write tool.
    "@paulpham157/apply-patch": PiExtension(
        "0.3.1",
        "sha512-qqO0zcG0hW7lTdst0GXw+85ex3cJ+T94iLic4R3mpPakwrp1B/jOAU1cov72fiQBSyf9NJO2eRj9BeJdNz41Rw==",
        "src/index.ts", read_only=False,
    ),
}
DEFAULT_PI_EXTENSIONS: tuple[str, ...] = tuple(PI_EXTENSIONS)
# Vetted and rejected (ADR-0017); named so the error says why.
UNAVAILABLE_PI_EXTENSIONS = {
    "pi-fabric": (
        "pi-fabric routes prompts and code to its own third-party model services and embeds a second Pi; "
        "it also needs Node >= 24 (the template runs Node 22)"
    ),
    "pi-lens": (
        "pi-lens 4.4.6 intermittently never finishes its turn_end/agent_end hooks under Pi 0.73.1 "
        "print mode, so the headless turn hangs until the timeout"
    ),
}
PI_EXTENSIONS_ENV = "SDF_PI_EXTENSIONS"


def resolve_pi_extensions(value: str | None) -> tuple[str, ...]:
    """``SDF_PI_EXTENSIONS``: unset means the default set, empty means none.

    A comma-separated list of allowlisted package names; the result is in the
    allowlist's load order.  Anything else is refused.
    """

    if value is None:
        return DEFAULT_PI_EXTENSIONS
    names = {item.strip() for item in value.split(",") if item.strip()}
    for name in sorted(names):
        if name in UNAVAILABLE_PI_EXTENSIONS:
            raise ValueError(f"{PI_EXTENSIONS_ENV}: {UNAVAILABLE_PI_EXTENSIONS[name]} (ADR-0017)")
        if name not in PI_EXTENSIONS:
            raise ValueError(
                f"{PI_EXTENSIONS_ENV}: {name!r} is not an image-baked Pi extension; "
                f"allowed: {', '.join(PI_EXTENSIONS)} (ADR-0017)"
            )
    return tuple(name for name in PI_EXTENSIONS if name in names)


def extensions_for_turn(extensions: Sequence[str], *, auto: bool) -> tuple[str, ...]:
    """The extensions a turn loads: all configured ones only in write mode."""

    return tuple(name for name in extensions if auto or PI_EXTENSIONS[name].read_only)
_EXIT_MARKER = "SDF_PI_EXIT="
_TIMEOUT_EXITS = frozenset({124, 137})
_PROJECT_CONFIG_EXIT = 96
_NO_WORKSPACE_EXIT = 97
_NO_MODEL_EXIT = 95
# Prints one line per package: its package.json on one line, or nothing.
_EXTENSION_PROBE = (
    'd=$1; shift; for p; do f="$d/node_modules/$p/package.json"; '
    '[ -f "$f" ] && tr -d "\\n\\r" < "$f"; echo; done'
)
_KILL_AFTER_S = 10
_ATTEMPT_ID = re.compile(r"[A-Za-z0-9_-]+")
_MODEL = re.compile(r"[A-Za-z0-9][A-Za-z0-9._@/+-]{0,199}")
_VERSION = re.compile(r"(?:^|\s)v?(\d+\.\d+\.\d+)(?:\s|$)")
# Pi state directory name, relative to the turn's cwd.
PROJECT_CONFIG_DIR = ".pi"

# ``sh -c`` wrapper.  Positional args: workspace, turn seconds, stdout file,
# stderr file, session dir, model (empty: ``$SDF_PI_MODEL``), prompt, then the
# Pi argv.  It always exits 0 with an explicit exit marker so a failed turn is
# reported by this runtime, not swallowed by the transport.  Only the
# ``message_end`` / ``tool_execution_end`` events are printed: ``--mode json``
# also streams one ``message_update`` per token.
_PURGE_PI_ENV = 'for v in $(env | sed -n "s/^\\(PI_[A-Za-z0-9_]*\\)=.*/\\1/p"); do unset "$v"; done; '
_WRAPPER = (
    'dir=$1; secs=$2; out=$3; err=$4; sess=$5; model=${6:-$SDF_PI_MODEL}; prompt=$7; shift 7; '
    + _PURGE_PI_ENV +
    'export PI_OFFLINE=1 PI_SKIP_VERSION_CHECK=1 PI_TELEMETRY=0; '
    'cd "$dir" || { echo ' + _EXIT_MARKER + str(_NO_WORKSPACE_EXIT) + '; exit 0; }; '
    'if [ -e ' + PROJECT_CONFIG_DIR + ' ] || [ -L ' + PROJECT_CONFIG_DIR + ' ]; then echo '
    + _EXIT_MARKER + str(_PROJECT_CONFIG_EXIT) + '; exit 0; fi; '
    '[ -n "$model" ] || { echo ' + _EXIT_MARKER + str(_NO_MODEL_EXIT) + '; exit 0; }; '
    '(umask 077; mkdir -p "$sess"); '
    'printf "%s" "$prompt" | timeout --kill-after=' + str(_KILL_AFTER_S) + ' "$secs" "$@" --model "$model" '
    '>"$out" 2>"$err"; rc=$?; '
    'grep -E \'^\\{"type":"(message_end|tool_execution_end)"\' "$out" || true; '
    'echo ' + _EXIT_MARKER + '$rc; if [ "$rc" -ne 0 ]; then tail -c 2000 "$err"; fi; exit 0'
)


class PiRuntimeError(HerdrRuntimeError):
    """Pi or its sandbox returned an unusable result."""


class SandboxExecTransport(Protocol):
    def exec_argv(self, argv: Sequence[str], timeout_ms: int) -> str: ...


@dataclass(slots=True)
class PiUsage:
    """Token/cost totals from Pi's assistant ``message_end`` events.

    ``served_models`` is what the endpoint reported: pi-ai sets
    ``responseModel`` only when it differs from the requested id, so a message
    without it was served by the requested ``model``.
    """

    messages: int = 0
    input_tokens: int = 0
    output_tokens: int = 0
    cache_read_tokens: int = 0
    cache_write_tokens: int = 0
    total_tokens: int = 0
    cost: float = 0.0
    requested_models: list[str] = field(default_factory=list)
    served_models: list[str] = field(default_factory=list)

    def add(self, message: Mapping[str, Any]) -> None:
        usage = message.get("usage") if isinstance(message.get("usage"), Mapping) else {}
        cost = usage.get("cost") if isinstance(usage.get("cost"), Mapping) else {}
        self.messages += 1
        self.input_tokens += _int(usage.get("input"))
        self.output_tokens += _int(usage.get("output"))
        self.cache_read_tokens += _int(usage.get("cacheRead"))
        self.cache_write_tokens += _int(usage.get("cacheWrite"))
        self.total_tokens += _int(usage.get("totalTokens"))
        total = cost.get("total")
        if isinstance(total, (int, float)) and not isinstance(total, bool):
            self.cost += float(total)
        requested = message.get("model")
        served = message.get("responseModel") or requested
        for value, bucket in ((requested, self.requested_models), (served, self.served_models)):
            if isinstance(value, str) and value and value not in bucket:
                bucket.append(value)

    def merge(self, other: "PiUsage") -> None:
        for name in ("messages", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "total_tokens", "cost"):
            setattr(self, name, getattr(self, name) + getattr(other, name))
        for name in ("requested_models", "served_models"):
            bucket = getattr(self, name)
            bucket.extend(value for value in getattr(other, name) if value not in bucket)

    def as_dict(self) -> dict[str, Any]:
        return {
            "messages": self.messages,
            "input_tokens": self.input_tokens,
            "output_tokens": self.output_tokens,
            "cache_read_tokens": self.cache_read_tokens,
            "cache_write_tokens": self.cache_write_tokens,
            "total_tokens": self.total_tokens,
            "cost": round(self.cost, 6),
            "requested_models": list(self.requested_models),
            "served_models": list(self.served_models),
        }


@dataclass(slots=True)
class _Binding:
    attempt_id: str
    workspace: str
    auto: bool
    extensions: tuple[str, ...] = ()
    turns: int = 0
    usage: PiUsage = field(default_factory=PiUsage)


def parse_events(raw: str) -> tuple[tuple[str, ...], PiUsage, tuple[str, ...]]:
    """Parse the ``message_end`` / ``tool_execution_end`` lines of a Pi turn.

    Returns (output lines, usage, error messages).  Non-JSON lines are kept
    verbatim as output.
    """

    lines: list[str] = []
    errors: list[str] = []
    usage = PiUsage()
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
        if kind == "tool_execution_end":
            lines.append(f"[tool {event.get('toolName', '?')}: {'error' if event.get('isError') else 'ok'}]")
            continue
        if kind != "message_end":
            continue
        message = event.get("message") if isinstance(event.get("message"), Mapping) else {}
        if message.get("role") != "assistant":
            continue
        usage.add(message)
        for part in message.get("content") or ():
            if isinstance(part, Mapping) and part.get("type") == "text":
                text = part.get("text")
                if isinstance(text, str) and text.strip():
                    lines.append(text)
        if message.get("stopReason") in ("error", "aborted") or message.get("errorMessage"):
            detail = str(message.get("errorMessage") or message.get("stopReason"))[:500]
            errors.append(detail)
            lines.append(f"[error {detail}]")
    return tuple(lines), usage, tuple(errors)


def workspace_has_project_config(workspace: Path) -> bool:
    """True when Pi would load project settings/extensions from ``workspace``."""

    path = Path(workspace) / PROJECT_CONFIG_DIR
    return path.exists() or path.is_symlink()


class PiHeadlessRuntime:
    """``AgentRuntime`` for the ``pi`` agent kind, one turn per ``send``."""

    def __init__(
        self,
        *,
        transport: SandboxExecTransport,
        turn_timeout_s: int = 600,
        control_timeout_ms: int = 60_000,
        expected_version: str | None = PI_VERSION,
        binary: str = "pi",
        model: str | None = None,
        credentials: Mapping[str, CredentialMetadata] | None = None,
        extensions: Sequence[str] | None = None,
        extensions_dir: str | None = None,
    ) -> None:
        if turn_timeout_s <= 0 or control_timeout_ms <= 0:
            raise ValueError("timeouts must be positive")
        if not callable(getattr(transport, "exec_argv", None)):
            raise ValueError("Pi runtime needs a transport with exec_argv")
        # ADR-0017: None reads SDF_PI_EXTENSIONS (unset: the default set).
        if extensions is None:
            self.extensions = resolve_pi_extensions(os.environ.get(PI_EXTENSIONS_ENV))
        else:
            self.extensions = resolve_pi_extensions(",".join(extensions))
        self._extensions_dir = extensions_dir or PI_EXTENSIONS_DIR
        if model is not None and not _MODEL.fullmatch(model):
            raise ValueError("model must be a bare model id without ':'")
        self._transport = transport
        self._turn_timeout_s = turn_timeout_s
        self._control_timeout_ms = control_timeout_ms
        self.expected_version = expected_version
        self._binary = binary
        # None: the sandbox's SDF_PI_MODEL (the one the seed registered).
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
        if workspace_has_project_config(path):
            raise PiRuntimeError(
                "the Attempt workspace contains .pi/; Pi project settings and extensions are untrusted, "
                "so SDF refuses to run it there (ADR-0012)"
            )
        stage = getattr(self._transport, "stage_workspace", None)
        if not callable(stage):
            raise PiRuntimeError("Pi runtime needs a transport that stages workspaces")
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
            raise PiRuntimeError(f"Pi runtime cannot start agent kind {agent!r}")
        if not _ATTEMPT_ID.fullmatch(attempt_id):
            raise ValueError("attempt_id must contain only letters, digits, underscores, or hyphens")
        credential = None
        if self.credentials is not None:
            credential = self.credentials.get(agent)
            if credential is None:
                raise PiRuntimeError(
                    "no resolved Agent Credential for pi; it was not injected at sandbox creation; "
                    "set SDF_CREDENTIAL_MODE_PI"
                )
        existing = next((sid for sid, b in self._bindings.items() if b.attempt_id == attempt_id), None)
        if existing is not None:
            return self._sessions[existing]
        workspace = self._workspaces.get(attempt_id)
        if workspace is None:
            raise PiRuntimeError("bind_workspace must stage the Attempt workspace before start")
        if self.expected_version is not None:
            self.require_compatible()
        if self.extensions:
            self.require_extensions()
        # ADR-0008/0012: claim at the start boundary, after setup succeeded.
        auto = _claim_fresh_e2b_permission_bypass(self._transport)
        with self._lock:
            self._counter += 1
            session_id = f"pi-{attempt_id}-{self._counter}"
        self._bindings[session_id] = _Binding(
            attempt_id, workspace, auto, extensions_for_turn(self.extensions, auto=auto))
        session = RuntimeSession(session_id, attempt_id, agent, RuntimeStatus.CREATED, credential=credential)
        self._sessions[session_id] = session
        return session

    def installed_version(self) -> str:
        # Pi 0.73 printed its version on stderr, 1.x on stdout: read both.
        # ``PI_PACKAGE_DIR`` would change the reported version: purge ``PI_*`` as turns do.
        raw = self._transport.exec_argv(["sh", "-c", _PURGE_PI_ENV + '"$0" --version 2>&1', self._binary],
                                        self._control_timeout_ms)
        match = _VERSION.search(raw.strip())
        if match is None:
            raise PiRuntimeError("Pi version response was not recognized")
        return match.group(1)

    def require_compatible(self) -> str:
        version = self.installed_version()
        if version != self.expected_version:
            raise PiRuntimeError(f"Pi {version} is installed but SDF is pinned to {self.expected_version}")
        return version

    def require_extensions(self) -> dict[str, str]:
        """Check the configured extensions are baked into the sandbox at their pins."""

        names = list(self.extensions)
        raw = self._transport.exec_argv(
            ["sh", "-c", _EXTENSION_PROBE, "sdf-pi-ext", self._extensions_dir, *names], self._control_timeout_ms)
        lines = raw.split("\n")
        found: dict[str, str] = {}
        for index, name in enumerate(names):
            line = lines[index].strip() if index < len(lines) else ""
            try:
                meta = json.loads(line) if line else {}
            except ValueError:
                meta = {}
            if not isinstance(meta, dict) or meta.get("name") != name:
                raise PiRuntimeError(f"Pi extension {name} is not installed in the sandbox image (ADR-0017); use the "
                    f"sdf-herdr-agents template or set {PI_EXTENSIONS_ENV}= to run without extensions")
            version = str(meta.get("version"))
            if version != PI_EXTENSIONS[name].version:
                raise PiRuntimeError(
                    f"Pi extension {name} {version} is installed but SDF is pinned to {PI_EXTENSIONS[name].version}"
                )
            found[name] = version
        return found

    def loaded_extensions(self, session_id: str) -> tuple[str, ...]:
        """The extensions this session's turns load (ADR-0017)."""

        return self._known(session_id)[0].extensions

    def permission_skipping(self, session_id: str) -> bool:
        return self._known(session_id)[0].auto

    def usage(self, session_id: str) -> dict[str, Any]:
        return self._known(session_id)[0].usage.as_dict()

    def command(self, session_id: str, input_text: str) -> list[str]:
        """The exact argv a turn runs (no credential material: names only)."""

        binding, _ = self._known(session_id)
        if not input_text.strip():
            raise ValueError("input_text must be non-empty")
        base = f"/tmp/sdf-pi-{binding.attempt_id}"
        argv = [
            self._binary, "-p", "--mode", "json",
            "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes",
            "--no-mcp", "--no-approve",
        ]
        # ADR-0017: discovery stays off; explicit paths to the baked allowlist only.
        for name in binding.extensions:
            argv += ["-e", f"{self._extensions_dir}/node_modules/{name}/{PI_EXTENSIONS[name].entry}"]
        argv += ["--session-dir", f"{base}/sessions"]
        if binding.turns:
            argv.append("--continue")
        if not binding.auto:
            argv += ["--tools", READ_ONLY_TOOLS]
        argv += ["--provider", PI_PROVIDER]
        return [
            "sh", "-c", _WRAPPER, "sdf-pi", binding.workspace, str(self._turn_timeout_s),
            f"{base}.jsonl", f"{base}.err", f"{base}/sessions", self._model or "", input_text, *argv,
        ]

    def send(self, session_id: str, input_text: str) -> RuntimeSession:
        binding, current = self._known(session_id)
        if current.status in (RuntimeStatus.CANCELLED, RuntimeStatus.TERMINATED):
            return current
        argv = self.command(session_id, input_text)
        self._set(session_id, RuntimeStatus.RUNNING, current.output)
        # The transport watchdog sits after Pi's own timeout + kill grace.
        budget_ms = (self._turn_timeout_s + _KILL_AFTER_S + 20) * 1000
        raw = self._transport.exec_argv(argv, budget_ms)
        body, rc = _split_exit(raw)
        lines, usage, errors = parse_events(body)
        binding.usage.merge(usage)
        if rc not in (_PROJECT_CONFIG_EXIT, _NO_WORKSPACE_EXIT, _NO_MODEL_EXIT):
            binding.turns += 1
        output = current.output + lines
        if rc == 0 and not errors:
            return self._set(session_id, RuntimeStatus.COMPLETED, output)
        self._set(session_id, RuntimeStatus.TERMINATED if rc in _TIMEOUT_EXITS else current.status, output)
        if rc in _TIMEOUT_EXITS:
            raise PiRuntimeError(f"Pi turn exceeded {self._turn_timeout_s}s and was killed")
        if rc == _PROJECT_CONFIG_EXIT:
            raise PiRuntimeError("the Attempt workspace gained a .pi/ entry; Pi refuses project config (ADR-0012)")
        if rc == _NO_WORKSPACE_EXIT:
            raise PiRuntimeError("the staged Attempt workspace is missing in the sandbox")
        if rc == _NO_MODEL_EXIT:
            raise PiRuntimeError("no Pi model: SDF_PI_MODEL is not set in the sandbox")
        if rc is None:
            raise PiRuntimeError("Pi turn ended without an exit marker")
        detail = "; ".join(errors) or (lines[-1] if lines else "")
        raise PiRuntimeError(f"Pi run failed (exit {rc}): {detail[-1000:]}")

    def stream(self, session_id: str) -> tuple[str, ...]:
        return self._known(session_id)[1].output

    def status(self, session_id: str) -> RuntimeSession:
        return self._known(session_id)[1]

    def cancel(self, session_id: str) -> RuntimeSession:
        _, current = self._known(session_id)
        if not self._closed:
            # ``[p]`` keeps pkill from matching (and killing) its own ``sh -c``.
            try:
                self._transport.exec_argv(["sh", "-c", "pkill -f '[p]i -p --mode json' || true"], self._control_timeout_ms)
            finally:
                # Sandbox destruction is the descendant-cleanup boundary.
                self._close_environment()
        return self._set(session_id, RuntimeStatus.CANCELLED, current.output)

    def terminate(self, session_id: str) -> RuntimeSession:
        _, current = self._known(session_id)
        self._close_environment()
        return self._set(session_id, RuntimeStatus.TERMINATED, current.output)

    def reconnect(self, session_id: str) -> RuntimeSession:
        raise HerdrUnsupportedOperation("headless Pi turns are not reconnectable; start a new Attempt")

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
            raise KeyError(f"unknown Pi session: {session_id}") from exc

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
            return "\n".join(lines[:index] + lines[index + 1:]), rc
    return raw, None


def _int(value: Any) -> int:
    return value if isinstance(value, int) and not isinstance(value, bool) else 0


__all__ = [
    "DEFAULT_PI_EXTENSIONS",
    "PI_EXTENSIONS",
    "PI_EXTENSIONS_DIR",
    "PI_EXTENSIONS_ENV",
    "PI_PROVIDER",
    "PI_VERSION",
    "PiExtension",
    "PiHeadlessRuntime",
    "PiRuntimeError",
    "PiUsage",
    "extensions_for_turn",
    "parse_events",
    "resolve_pi_extensions",
    "workspace_has_project_config",
]
