"""One agent in one sandbox, as the coordinator sees it (ADR 0014).

``AgentSandbox`` is the narrow surface the coordinator and relay need: start
(create the sandbox, seed credentials, install the ``sdf-msg`` shim, stage the
workspace), run one blocking agent turn, move mailbox files through the
provider's file API, collect the workspace, report usage, and close.

``E2BAgentSandbox`` builds it from the existing runtime stack: the credentialed
E2B transport (ADR 0007/0008, egress locked at creation) and the OpenCode or Pi
headless runtime (ADR 0011/0012).  Nothing listens on a port: the host reaches
the sandbox only through E2B's file and command API.  No messaging credential
exists; the shim holds no token, and the host authenticates a message by the
sandbox it collected it from.
"""

from __future__ import annotations

import json
import shlex
import threading
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Mapping, Protocol

from .core_mcp import CORE_DIR, CORE_SHIM_PATH, REQ_DIR, RESP_DIR, registration_script

SHIM_SOURCE = Path(__file__).with_name("sdf_msg_shim.py")
CORE_SHIM_SOURCE = Path(__file__).with_name("sdf_core_shim.py")
MAILBOX = "/home/user/.sdf-msg"
OUTBOX, INBOX = f"{MAILBOX}/outbox", f"{MAILBOX}/inbox"
SHIM_PATH = "/usr/local/bin/sdf-msg"
VULTR_HOST = "api.vultrinference.com"
DEFAULT_TEMPLATE = "sdf-herdr-agents"
DEFAULT_NETWORK = {"allow_out": [VULTR_HOST], "deny_out": ["0.0.0.0/0"], "allow_public_traffic": False}
SUPPORTED_KINDS = ("opencode", "pi")
# E2BAgentSandbox can also host a single Goose agent (sdf-core over its
# stdio MCP extension, ADR 0022); the coordinator roles stay SUPPORTED_KINDS.
SANDBOX_KINDS = (*SUPPORTED_KINDS, "goose")


@dataclass(frozen=True)
class AgentSpec:
    agent_id: str
    role: str  # lead | worker
    kind: str  # opencode | pi
    model: str | None = None


@dataclass(frozen=True)
class TurnResult:
    ok: bool
    seconds: float
    error: str | None = None
    tail: str = ""


class AgentSandbox(Protocol):
    spec: AgentSpec

    @property
    def sandbox_id(self) -> str | None: ...
    def start(self, workspace: Path) -> None: ...
    def turn(self, prompt: str) -> TurnResult: ...
    def list_outbox(self) -> list[tuple[str, int]]: ...
    def read_outbox(self, name: str) -> bytes: ...
    def remove_outbox(self, name: str) -> None: ...
    def deliver(self, name: str, data: bytes) -> None: ...
    # ADR 0022 (optional): read-only SDF Core query relay
    # def list_core_requests(self) -> list[tuple[str, int]]: ...
    # def read_core_request(self, name: str) -> bytes: ...
    # def remove_core_request(self, name: str) -> None: ...
    # def write_core_response(self, name: str, data: bytes) -> None: ...
    def inbox_count(self) -> int: ...
    def collect(self, destination: Path) -> None: ...
    def usage(self) -> dict[str, Any]: ...
    def alive_seconds(self) -> float: ...
    # ADR 0033 (optional): provider-side lifetime, kept up during a material
    # decision hold.  Without them (local fakes; Daytona, whose sandboxes are
    # created with auto-stop disabled) the sandbox has no provider timeout.
    # def lifetime_left_s(self) -> float | None: ...  # None = no provider timeout
    # def extend_lifetime(self, seconds: int) -> None: ...  # raises on failure
    def close(self) -> None: ...


AgentSandboxFactory = Callable[[AgentSpec], AgentSandbox]


def _install_script(agent_id: str, mcp: bool, kind: str | None = None, core: bool = False) -> str:
    lines = [
        "set -e",
        f"sudo install -m 0755 /tmp/sdf-msg.py {SHIM_PATH}",
        "rm -f /tmp/sdf-msg.py",
        f"mkdir -p {OUTBOX} {INBOX} {MAILBOX}/read && chmod 700 {MAILBOX}",
        f"printf %s {shlex.quote(agent_id)} > {MAILBOX}/me",
    ]
    if core:
        # ADR 0022: read-only SDF Core queries, registered in user-level config only.
        lines += [
            f"sudo install -m 0755 /tmp/sdf-core.py {CORE_SHIM_PATH}",
            "rm -f /tmp/sdf-core.py",
            f"mkdir -p {REQ_DIR} {RESP_DIR} && chmod 700 {CORE_DIR}",
            f"python3 -c {shlex.quote(registration_script(kind or 'pi'))}",
        ]
    if mcp:
        merge = (
            "import json,os;p=os.path.expanduser('~/.config/opencode/opencode.json');"
            "c=json.load(open(p)) if os.path.exists(p) else {};"
            f"c.setdefault('mcp',{{}}).setdefault('servers',{{}})['sdf']={{'type':'local','command':['python3','{SHIM_PATH}','mcp'],'codemode':False}};"
            "c.setdefault('permissions',[]).append({'action':'sdf_*','resource':'*','effect':'allow'});"
            "os.makedirs(os.path.dirname(p),exist_ok=True);json.dump(c,open(p,'w'))"
        )
        lines.append(f"python3 -c {shlex.quote(merge)}")
    lines.append(f"{SHIM_PATH} whoami")
    return "\n".join(lines)


class E2BAgentSandbox:
    """OpenCode or Pi in a fresh, egress-locked E2B sandbox."""

    def __init__(
        self, spec: AgentSpec, *, environ: Mapping[str, str], template: str = DEFAULT_TEMPLATE,
        sandbox_timeout_s: int = 1800, turn_timeout_s: int = 420, network: Mapping[str, Any] | None = None,
        mcp_for_opencode: bool = True, core_access: bool = True,
    ) -> None:
        if spec.kind not in SANDBOX_KINDS:
            raise ValueError(f"agent kind {spec.kind!r} is not supported by the coordinator (use {SUPPORTED_KINDS})")
        self.spec = spec
        self._environ = dict(environ)
        self._template = template
        self._sandbox_timeout_s = sandbox_timeout_s
        self._turn_timeout_s = turn_timeout_s
        self._network = dict(network or DEFAULT_NETWORK)
        self._mcp = mcp_for_opencode and spec.kind == "opencode"
        self._core = core_access
        self._transport: Any = None
        self._runtime: Any = None
        self._session: str | None = None
        self._binding = spec.agent_id.replace("-", "_").upper()
        self._born: float | None = None
        self._died: float | None = None
        # ADR 0033: when the provider ends the sandbox (monotonic), if it has
        # a timeout.  Set conservatively before the sandbox is created.
        self._expires: float | None = None
        self._lock = threading.Lock()
        self._sandbox_id: str | None = None

    @property
    def sandbox_id(self) -> str | None:
        return self._sandbox_id

    def _fs(self) -> Any:
        return self._transport._ensure_sandbox().files

    def start(self, workspace: Path) -> None:
        from .credential_injection import create_credentialed_transport

        pi_extensions: tuple[str, ...] = ()
        if self.spec.kind == "pi":
            from .pi_runtime import PI_EXTENSIONS_ENV, resolve_pi_extensions

            # ADR-0017: validated before any sandbox exists.
            pi_extensions = resolve_pi_extensions(self._environ.get(PI_EXTENSIONS_ENV))
        self._transport, _ = create_credentialed_transport(
            agents=(self.spec.kind,), environ=self._environ, template=self._template,
            timeout_seconds=self._sandbox_timeout_s, network=self._network,
        )
        if self.spec.kind == "opencode":
            from .opencode_runtime import OpenCodeHeadlessRuntime

            self._runtime = OpenCodeHeadlessRuntime(
                transport=self._transport, turn_timeout_s=self._turn_timeout_s, model=self.spec.model)
        elif self.spec.kind == "goose":
            from .goose_runtime import GooseHeadlessRuntime

            self._runtime = GooseHeadlessRuntime(
                transport=self._transport, turn_timeout_s=self._turn_timeout_s, model=self.spec.model,
                core_mcp=self._core)
        else:
            from .pi_runtime import PiHeadlessRuntime

            self._runtime = PiHeadlessRuntime(
                transport=self._transport, turn_timeout_s=self._turn_timeout_s, model=self.spec.model,
                extensions=pi_extensions)
        self._born = time.monotonic()
        if callable(getattr(self._transport, "extend_lifetime", None)):
            self._expires = self._born + self._sandbox_timeout_s  # E2B: timeout from creation
        # Creates the sandbox (egress locked at creation), seeds credentials.
        self._runtime.bind_workspace(self._binding, workspace)
        self._sandbox_id = self._transport.sandbox_id
        self._fs().write("/tmp/sdf-msg.py", SHIM_SOURCE.read_text())
        if self._core:
            self._fs().write("/tmp/sdf-core.py", CORE_SHIM_SOURCE.read_text())
        script = _install_script(self.spec.agent_id, self._mcp, self.spec.kind, self._core)
        out = self._transport.exec_argv(["sh", "-c", script], 60_000)
        if out.strip().splitlines()[-1:] != [self.spec.agent_id]:
            raise RuntimeError("sdf-msg shim install could not be verified")
        self._session = self._runtime.start(attempt_id=self._binding, agent=self.spec.kind).session_id

    @property
    def workspace_path(self) -> str:
        return f"/tmp/sdf/{self._binding}"

    def turn(self, prompt: str) -> TurnResult:
        started = time.monotonic()
        try:
            session = self._runtime.send(self._session, prompt)
            tail = "\n".join(session.output[-5:])[-1500:]
            return TurnResult(True, time.monotonic() - started, None, tail)
        except Exception as exc:  # noqa: BLE001 - a failed turn is data for the coordinator
            return TurnResult(False, time.monotonic() - started, f"{type(exc).__name__}: {str(exc)[-600:]}")

    def list_outbox(self) -> list[tuple[str, int]]:
        return [(e.name, int(e.size or 0)) for e in self._fs().list(OUTBOX)
                if e.type is not None and str(e.type).lower().endswith("file")]

    def read_outbox(self, name: str) -> bytes:
        return bytes(self._fs().read(f"{OUTBOX}/{name}", format="bytes"))

    def remove_outbox(self, name: str) -> None:
        self._fs().remove(f"{OUTBOX}/{name}")

    def deliver(self, name: str, data: bytes) -> None:
        fs = self._fs()
        tmp = f"{INBOX}/.{name}.part"
        fs.write(tmp, data)
        fs.rename(tmp, f"{INBOX}/{name}")  # the shim ignores dot-files: no partial reads

    # -- ADR 0022: SDF Core query relay ---------------------------------------
    def list_core_requests(self) -> list[tuple[str, int]]:
        if not self._core:
            return []
        return [(e.name, int(e.size or 0)) for e in self._fs().list(REQ_DIR)
                if e.type is not None and str(e.type).lower().endswith("file")]

    def read_core_request(self, name: str) -> bytes:
        return bytes(self._fs().read(f"{REQ_DIR}/{name}", format="bytes"))

    def remove_core_request(self, name: str) -> None:
        self._fs().remove(f"{REQ_DIR}/{name}")

    def write_core_response(self, name: str, data: bytes) -> None:
        fs = self._fs()
        tmp = f"{RESP_DIR}/.{name}.part"
        fs.write(tmp, data)
        fs.rename(tmp, f"{RESP_DIR}/{name}")

    def inbox_count(self) -> int:
        return sum(1 for e in self._fs().list(INBOX) if e.name.endswith(".json") and not e.name.startswith("."))

    def collect(self, destination: Path) -> None:
        self._runtime.collect_workspace(self._binding, destination)

    def usage(self) -> dict[str, Any]:
        if self._runtime is None or self._session is None:
            return {}
        try:
            return dict(self._runtime.usage(self._session))
        except Exception:  # noqa: BLE001
            return {}

    def alive_seconds(self) -> float:
        if self._born is None:
            return 0.0
        return (self._died or time.monotonic()) - self._born

    def lifetime_left_s(self) -> float | None:
        """Seconds until the provider's sandbox timeout (E2B); ``None`` when there is none (Daytona)."""

        if self._expires is None or self._died is not None:
            return None
        return max(0.0, self._expires - time.monotonic())

    def extend_lifetime(self, seconds: int) -> None:
        """Reset the provider timeout to ``seconds`` from now (E2B ``set_timeout``); raises on failure."""

        if self._expires is None or self._died is not None:
            raise RuntimeError("this sandbox has no provider lifetime to extend")
        asked = time.monotonic()
        self._transport.extend_lifetime(int(seconds))
        self._expires = asked + int(seconds)

    def close(self) -> None:
        with self._lock:
            if self._transport is not None and self._died is None:
                try:
                    self._transport.close()
                finally:
                    self._died = time.monotonic()


def e2b_factory(environ: Mapping[str, str], **kwargs: Any) -> AgentSandboxFactory:
    def build(spec: AgentSpec) -> AgentSandbox:
        return E2BAgentSandbox(spec, environ=environ, **kwargs)

    build.evidence_mode = "live"  # type: ignore[attr-defined]  # real agents (ADR 0027 §4)
    return build


def running_sandbox_ids(api_key: str) -> set[str]:
    from e2b import Sandbox

    paginator = Sandbox.list(api_key=api_key)
    ids: set[str] = set()
    while paginator.has_next:
        ids.update(item.sandbox_id for item in paginator.next_items())
    return ids


def agent_environ(environ: Mapping[str, str], kinds: set[str]) -> dict[str, str]:
    """The minimal host-side mapping the credentialed transport reads.

    Only variable *names* are configured here; values are passed through from
    the operator environment and never logged.  Defaults follow the per-agent
    overrides (ADR 0011/0012): OpenCode and Pi on Vultr glm-5.3 with the key in
    ``VULTR_INFERENCE_API_KEY`` unless ``SDF_*_API_KEY_ENV`` says otherwise.
    """

    out: dict[str, str] = {}
    for name in ("E2B_API_KEY", "E2B_DOMAIN"):
        if environ.get(name):
            out[name] = environ[name]
    for kind in kinds:
        upper = kind.upper()
        out[f"SDF_CREDENTIAL_MODE_{upper}"] = environ.get(f"SDF_CREDENTIAL_MODE_{upper}", "api-key")
        key_env = environ.get(f"SDF_{upper}_API_KEY_ENV", "VULTR_INFERENCE_API_KEY")
        out[f"SDF_{upper}_API_KEY_ENV"] = key_env
        if environ.get(key_env):
            out[key_env] = environ[key_env]
        for suffix in ("BASE_URL", "MODEL"):
            name = f"SDF_{upper}_{suffix}"
            if environ.get(name):
                out[name] = environ[name]
    # ADR-0017: passed through as set; an empty value means "no extensions".
    if "pi" in kinds and "SDF_PI_EXTENSIONS" in environ:
        out["SDF_PI_EXTENSIONS"] = environ["SDF_PI_EXTENSIONS"]
    return out


def describe_env(environ: Mapping[str, str]) -> str:
    """Presence-only description (never values) for logs and reports."""

    return json.dumps({k: bool(v) for k, v in sorted(environ.items())})
