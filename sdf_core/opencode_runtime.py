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
With that claim and ``SDF_OPENCODE_PROJECT_TRUST=always`` (the default) the
workspace's own ``opencode.json`` / ``.opencode/`` / ``AGENTS.md`` load, under
SDF's user config re-applied as ``OPENCODE_CONFIG_CONTENT`` (ADR-0021).
"""

from __future__ import annotations

import json
import re
import threading
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Mapping, Protocol, Sequence

from . import project_trust
from .herdr_runtime import HerdrRuntimeError, HerdrUnsupportedOperation, _claim_fresh_e2b_permission_bypass
from .turn_guard import GRACE_S, guard_argv, kill_argv
from .runtime import CredentialMetadata, RuntimeSession, RuntimeStatus

# Pinned to the npm ``latest`` release of ``@opencode/cli`` (OpenCode 2)
# verified for SDF (2026-10-28).  The runtime depends on ``opencode serve`` /
# ``opencode run --server`` flags (``--format json``, ``--auto``, ``--session``,
# ``--model``), the server's ``/api/mcp`` and session-message routes, and the
# JSON event shapes, so a different version fails closed instead of being
# trusted blindly.
OPENCODE_VERSION = "2.2.0"
AGENT_KIND = "opencode"
_EXIT_MARKER = "SDF_OPENCODE_EXIT="
_USAGE_EVENT = "sdf_usage"
_TIMEOUT_EXITS = frozenset({124, 137})
_KILL_AFTER_S = 10
_SERVER_READY_S = 30
_MCP_READY_S = 20
_ATTEMPT_ID = re.compile(r"[A-Za-z0-9_-]+")
_VERSION = re.compile(r"(?:^|\s)v?(\d+\.\d+\.\d+)(?:\s|$)")

# Environment the turn driver forces after dropping every inherited
# ``OPENCODE_*`` variable (config/permission overrides, server URLs and
# passwords, plugin and feature flags).
_BASE_FORCED_ENV = {
    "OPENCODE_DISABLE_AUTOUPDATE": "1",
    "OPENCODE_DISABLE_MODELS_FETCH": "1",
    "OPENCODE_DISABLE_CLAUDE_CODE": "1",
}
# Untrusted project (``SDF_OPENCODE_PROJECT_TRUST=never`` or no fresh E2B
# claim): project config is disabled under both spellings OpenCode 2 reads, as
# before ADR-0021 (the workspace's ``opencode.json`` / ``.opencode/`` could
# allow every permission, redirect the provider ``baseURL`` or load plugins).
_FORCED_ENV = {
    "OPENCODE_DISABLE_PROJECT_CONFIG": "1",
    "OPENCODE_CONFIG_PROJECT_DISABLE": "1",
    **_BASE_FORCED_ENV,
}
# Trusted project (ADR-0021): project config loads, and the driver replaces
# this sentinel with the text of SDF's seeded user config.  OpenCode 2.2.0
# merges ``OPENCODE_CONFIG_CONTENT`` after global, ``OPENCODE_CONFIG`` and
# project files (only managed ``/etc/opencode`` comes later), so SDF's
# provider ``baseURL``/key reference, model, permissions (sub-agent denies,
# ADR-0020) and MCP servers win over a hostile project config (verified with
# a mock endpoint; the background service must not be reused, which the
# private ``opencode serve`` per turn guarantees).
USER_CONFIG_SENTINEL = "@sdf-user-config"
_TRUSTED_FORCED_ENV = {"OPENCODE_CONFIG_CONTENT": USER_CONFIG_SENTINEL, **_BASE_FORCED_ENV}

# ``python3 -c`` turn driver (ADR-0018).  OpenCode 2 connects MCP servers when
# a server first boots a directory, so a one-shot ``run --standalone`` never
# offers MCP tools (agent-comm needs them).  The driver therefore:
#   1. drops inherited ``OPENCODE_*`` (keeps the injected ``OPENCODE_API_KEY``)
#      and forces ``_FORCED_ENV``; cd workspace;
#   2. starts a private ``opencode serve`` on a free 127.0.0.1 port with a
#      random per-turn password (env only, never printed);
#   3. waits until the server answers ``/api/mcp`` for the workspace and every
#      MCP server in the user config has settled;
#   4. runs ``opencode run --server URL ...`` with the prompt on stdin (2.2.0
#      JSON-quotes a message argument containing spaces or quotes, repeats it
#      after ``--`` and reads a leading ``-`` as a flag; stdin is verbatim),
#      bounded by the turn timeout
#      (TERM, then KILL after the grace), so it ends before the transport's
#      watchdog would kill the sandbox;
#   5. prints a synthetic ``sdf_usage`` event summed from the server's message
#      list for this turn (OpenCode 2 emits no ``step_finish`` for the final
#      text step), then the exit marker; the server is always killed.
# It always exits 0 with an explicit marker so a failed turn is reported by
# this runtime, not swallowed by the transport.
_DRIVER = r"""
import base64, json, os, secrets, signal, socket, subprocess, sys, time, urllib.parse, urllib.request
M = sys.argv[1]; wd, secs, grace, err = sys.argv[2], int(sys.argv[3]), int(sys.argv[4]), sys.argv[5]
srv_wait, mcp_wait = int(sys.argv[6]), int(sys.argv[7]); forced = json.loads(sys.argv[8])
prompt, argv = sys.argv[9], sys.argv[10:]
_sdf_key = os.environ.get("OPENCODE_API_KEY", "")
for k in [k for k in os.environ if k.startswith("OPENCODE_")]:
    del os.environ[k]
os.environ.update(forced)
if _sdf_key:
    os.environ["OPENCODE_API_KEY"] = _sdf_key
def done(rc, tail=""):
    print(M + str(rc)); tail and print(tail[-2000:]); sys.stdout.flush()
def user_config():
    base = os.environ.get("XDG_CONFIG_HOME") or os.path.join(os.path.expanduser("~"), ".config")
    return os.path.join(base, "opencode", "opencode.json")
if os.environ.get("OPENCODE_CONFIG_CONTENT") == "@sdf-user-config":
    try:
        os.environ["OPENCODE_CONFIG_CONTENT"] = open(user_config()).read()
    except OSError:
        done(96, "SDF OpenCode user config is missing"); sys.exit(0)
try:
    os.chdir(wd)
except OSError:
    done(97); sys.exit(0)
# OpenCode 2 takes its project directory from $PWD, not the process cwd: a
# stale PWD (the image WORKDIR) would point the agent at the wrong folder.
os.environ["PWD"] = os.getcwd()
pw = secrets.token_hex(24)
s = socket.socket(); s.bind(("127.0.0.1", 0)); port = s.getsockname()[1]; s.close()
url = "http://127.0.0.1:%d" % port
auth = "Basic " + base64.b64encode(("opencode:" + pw).encode()).decode()
def get(path):
    rq = urllib.request.Request(url + path, headers={"Authorization": auth, "x-opencode-directory": wd})
    with urllib.request.urlopen(rq, timeout=10) as r:
        return json.load(r)
def expected():
    try:
        c = json.load(open(user_config()))
        return {n for n, v in c["mcp"]["servers"].items() if not (isinstance(v, dict) and v.get("enabled") is False)}
    except Exception:
        return set()
def kill(p):
    try:
        os.killpg(p.pid, signal.SIGTERM)
        try:
            p.wait(grace)
        except subprocess.TimeoutExpired:
            os.killpg(p.pid, signal.SIGKILL); p.wait()
    except ProcessLookupError:
        pass
srv = subprocess.Popen([argv[0], "serve", "--hostname", "127.0.0.1", "--port", str(port)],
                       env=dict(os.environ, OPENCODE_SERVER_PASSWORD=pw), stdin=subprocess.DEVNULL,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
try:
    want, t0, up, note = expected(), time.time(), False, ""
    while time.time() - t0 < srv_wait + mcp_wait:
        if srv.poll() is not None:
            break
        try:
            data = get("/api/mcp").get("data") or []
            up = True
            ready = {m.get("name") for m in data if (m.get("status") or {}).get("status") not in ("pending", "connecting", None)}
            if want <= ready:
                note = ""; break
            note = "[sdf: MCP servers not ready: %s]" % ",".join(sorted(want - ready))
            if time.time() - t0 >= srv_wait + mcp_wait - 0.3:
                break
        except Exception:
            if not up and time.time() - t0 > srv_wait:
                break
        time.sleep(0.25)
    if not up:
        done(98, "OpenCode server did not become ready"); sys.exit(0)
    if note:
        print(note)
    cmd = [argv[0], argv[1], "--server", url] + argv[2:]
    with open(err, "w") as ef:
        run = subprocess.Popen(cmd, env=dict(os.environ, OPENCODE_PASSWORD=pw), stdin=subprocess.PIPE,
                               stdout=subprocess.PIPE, stderr=ef, text=True, start_new_session=True)
        try:
            out, _ = run.communicate(prompt, timeout=secs); rc = run.returncode
        except subprocess.TimeoutExpired:
            os.killpg(run.pid, signal.SIGTERM)
            try:
                out, _ = run.communicate(timeout=grace); rc = 124
            except subprocess.TimeoutExpired:
                os.killpg(run.pid, signal.SIGKILL); out, _ = run.communicate(); rc = 137
    sys.stdout.write(out or "")
    sid = None
    for line in (out or "").splitlines():
        try:
            e = json.loads(line); sid = e.get("sessionID") or sid
        except Exception:
            pass
    if sid:
        try:
            tok = {"input": 0, "output": 0, "reasoning": 0, "cache": {"read": 0, "write": 0}}
            cost, steps, cur, stop = 0.0, 0, None, False
            while not stop:
                q = {"limit": "200", "order": "desc"}
                if cur:
                    q["cursor"] = cur
                page = get("/api/session/%s/message?%s" % (urllib.parse.quote(sid, safe=""), urllib.parse.urlencode(q)))
                for m in page.get("data") or []:
                    if m.get("type") == "user":
                        stop = True; break
                    if m.get("type") == "assistant":
                        t = m.get("tokens") or {}; steps += 1
                        for f in ("input", "output", "reasoning"):
                            tok[f] += int(t.get(f) or 0)
                        for f in ("read", "write"):
                            tok["cache"][f] += int((t.get("cache") or {}).get(f) or 0)
                        cost += float(m.get("cost") or 0)
                cur = (page.get("cursor") or {}).get("next")
                if not page.get("data") or not cur:
                    break
            print(json.dumps({"type": "sdf_usage", "sessionID": sid, "steps": steps, "cost": cost, "tokens": tok}))
        except Exception as exc:
            print("[sdf: usage unavailable: %s]" % type(exc).__name__)
    tail = ""
    if rc != 0:
        try:
            tail = open(err).read()
        except OSError:
            pass
    done(rc, tail)
finally:
    kill(srv)
"""




class OpenCodeRuntimeError(HerdrRuntimeError):
    """OpenCode or its sandbox returned an unusable result."""


class SandboxExecTransport(Protocol):
    def exec_argv(self, argv: Sequence[str], timeout_ms: int) -> str: ...


@dataclass(slots=True)
class OpenCodeUsage:
    """Token/cost totals for OpenCode turns.

    Taken from the driver's ``sdf_usage`` event (the server's per-message
    totals for the turn) when present, otherwise summed from ``step_finish``
    events.  OpenCode 2 reports no ``total``; it is the sum of the parts.
    """

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
        steps = part.get("steps")
        self.steps += _int(steps) if steps is not None else 1
        parts = (_int(tokens.get("input")), _int(tokens.get("output")), _int(tokens.get("reasoning")))
        total = _int(tokens.get("total"))
        self.total_tokens += total if total else sum(parts) + _int(cache.get("read")) + _int(cache.get("write"))
        self.input_tokens += parts[0]
        self.output_tokens += parts[1]
        self.reasoning_tokens += parts[2]
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
    project_trusted: bool = False


def parse_events(raw: str) -> tuple[tuple[str, ...], str | None, OpenCodeUsage, tuple[str, ...]]:
    """Parse ``opencode run --format json`` stdout.

    Returns (output lines, OpenCode session id, usage, error messages).
    Non-JSON lines are kept verbatim as output.
    """

    lines: list[str] = []
    errors: list[str] = []
    session: str | None = None
    usage = OpenCodeUsage()
    reported: OpenCodeUsage | None = None
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
        elif kind == _USAGE_EVENT:
            reported = OpenCodeUsage()
            reported.add(event)
        elif kind == "error":
            detail = event.get("error", part)
            message = detail.get("data", detail) if isinstance(detail, Mapping) else detail
            if isinstance(message, Mapping):
                message = message.get("message", json.dumps(message, sort_keys=True))
            errors.append(str(message)[:500])
            lines.append(f"[error {str(message)[:500]}]")
    return tuple(lines), session, (reported if reported is not None else usage), tuple(errors)


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
        trusted = project_trust.effective(AGENT_KIND, fresh_e2b=auto)  # ADR-0021
        with self._lock:
            self._counter += 1
            session_id = f"opencode-{attempt_id}-{self._counter}"
        self._bindings[session_id] = _Binding(attempt_id, workspace, auto, project_trusted=trusted)
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

    def project_trusted(self, session_id: str) -> bool:
        """True when this session's turns load the workspace's project config (ADR-0021)."""

        return self._known(session_id)[0].project_trusted

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
        # The prompt is a driver argument written to OpenCode's stdin, never
        # an OpenCode argument.  The driver inserts ``--server <URL>`` after ``run``.
        err = f"/tmp/sdf-opencode-{binding.attempt_id}.err"
        return [
            "python3", "-c", _DRIVER, _EXIT_MARKER, binding.workspace, str(self._turn_timeout_s),
            str(_KILL_AFTER_S), err, str(_SERVER_READY_S), str(_MCP_READY_S),
            json.dumps(_TRUSTED_FORCED_ENV if binding.project_trusted else _FORCED_ENV, sort_keys=True),
            input_text, *argv,
        ]

    def _next_turn_id(self, session_id: str) -> str:
        with self._lock:
            self._turn_seq = getattr(self, "_turn_seq", 0) + 1
            return f"{session_id}-t{self._turn_seq}"

    def send(self, session_id: str, input_text: str) -> RuntimeSession:
        binding, current = self._known(session_id)
        if current.status in (RuntimeStatus.CANCELLED, RuntimeStatus.TERMINATED):
            return current
        argv = self.command(session_id, input_text)
        self._set(session_id, RuntimeStatus.RUNNING, current.output)
        # The transport watchdog sits after server start, OpenCode's own
        # timeout + kill grace, and the usage fetch.
        budget_ms = (self._turn_timeout_s + _KILL_AFTER_S * 2 + _SERVER_READY_S + _MCP_READY_S + 30) * 1000
        # ADR-0019: own session/cgroup per turn; the guard sweeps leftovers
        # (MCP servers, tool children) even when the driver was SIGKILLed.
        raw = self._transport.exec_argv(guard_argv(self._next_turn_id(session_id), argv), budget_ms + GRACE_S * 4000)
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
        if rc == 98:
            raise OpenCodeRuntimeError("OpenCode server did not become ready")
        detail = "; ".join(errors) or (lines[-1] if lines else "")
        raise OpenCodeRuntimeError(f"OpenCode run failed (exit {rc}): {detail[-1000:]}")

    def stream(self, session_id: str) -> tuple[str, ...]:
        return self._known(session_id)[1].output

    def status(self, session_id: str) -> RuntimeSession:
        return self._known(session_id)[1]

    def cancel(self, session_id: str) -> RuntimeSession:
        _, current = self._known(session_id)
        if not self._closed:
            # ADR-0019: kill the turns' whole session/cgroup/tagged set (the
            # driver, ``opencode run``, the private ``opencode serve`` and
            # everything they spawned), TERM then KILL.
            try:
                self._transport.exec_argv(kill_argv(f"{session_id}-t"), self._control_timeout_ms)
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
