"""Adapter selection for the public ``POST /tasks/{id}/run`` path (ADR 0023).

``SDF_RUN_ADAPTER`` (default ``fake``):

* ``fake`` / unset - deterministic :class:`FakeNativeAdapter` (CI, offline).
* ``live`` (alias ``herdr``) - one real agent turn in a disposable sandbox,
  built from main's building blocks, not a parallel path:

  - agent ``SDF_RUN_AGENT`` (default ``codex``), any of
    :data:`~sdf_core.credentials.AGENT_KINDS`; credentials come from main's
    per-agent configuration (``SDF_CREDENTIAL_MODE_<AGENT>``,
    ``SDF_<AGENT>_API_KEY_ENV``, ``SDF_<AGENT>_BASE_URL`` / ``SDF_OPENAI_BASE_URL``
    / ``SDF_ANTHROPIC_BASE_URL``, ``SDF_<AGENT>_MODEL``) via
    :func:`~sdf_core.credential_injection.create_credentialed_transport`
    (ADR-0007).  Headless OpenAI-family kinds keep their example base URL.
    ``/run`` Codex gets that example URL only when the resolved key variable
    is ``VULTR_INFERENCE_API_KEY``; any other key with no base URL stays on
    the official OpenAI Responses API, so the key is not sent to a host the
    operator did not name (ADR 0034).  Claude uses the official Anthropic
    Messages API unless ``SDF_ANTHROPIC_BASE_URL`` is set, and the example
    key is not attached unless that base URL is set.  The example key
    *name* is used only when no ``SDF_*`` key is set; it does not select a
    protocol;
  - provider ``SDF_SANDBOX_PROVIDER`` = ``e2b`` (template
    ``SDF_E2B_AGENTS_TEMPLATE`` / :data:`~sdf_core.agent_sandbox.DEFAULT_TEMPLATE`)
    or ``daytona`` (``SDF_DAYTONA_SNAPSHOT``);
  - runtimes: :class:`HerdrRuntime` for codex/claude and the headless runtimes
    for opencode/pi/qwen/goose, so the turn guard (ADR-0019), the sub-agent
    ban (ADR-0020) and project trust (ADR-0021) apply unchanged;
  - egress: E2B sandboxes allow the host of the kind's resolved base URL
    (example OpenAI-compatible host when that is the default), everything
    else denied, no public inbound traffic, plus any extra hosts in
    ``SDF_RUN_EGRESS_ALLOW``.  Daytona cannot apply that lock at creation, so it
    is refused unless ``SDF_RUN_DAYTONA_UNRESTRICTED_EGRESS=1``;
  - teardown: the sandbox is always killed; a failed kill is retried by id and,
    if it still fails, recorded as ``cleanup_failed`` (ledger, Attempt, log,
    runtime event) and fails the Attempt instead of being swallowed;
  - usage: one usage-ledger row per turn and one per sandbox (ADR 0023).
"""

from __future__ import annotations

import os
import threading
import time
from collections.abc import Iterator, Mapping
from contextlib import contextmanager
from pathlib import Path
from typing import Any

from sqlalchemy.orm import Session

from .adapter import AdapterResult, FakeNativeAdapter, NativeAdapter, RuntimeAgentAdapter
from .agent_models import model_agent_args, resolve_agent_models
from .agent_sandbox import DEFAULT_TEMPLATE
from .credential_injection import create_credentialed_transport, resolve_sandbox_provider as _main_provider
from .credentials import (
    AGENT_KINDS,
    EXAMPLE_API_KEY_ENV,
    EXAMPLE_OPENAI_BASE_URL,
    EXAMPLE_OPENAI_MODEL,
    CredentialConfigError,
    _AGENTS,
    endpoint_host,
    inference_network,
    llm_attempt_provider,
)
from .herdr_runtime import HerdrRuntime
from .observability import classify_error, get_logger, log_context
from .plugin_bridge import PluginConnectionBridge
from .qualification import AgentRoute, explicit_route, record_route, routing_request_from_env, select_agent
from .runtime import RuntimeController, RuntimeEvent, RuntimeEventKind, RuntimeStatus, SqlAlchemyRuntimeEventSink
from . import usage as _usage
from . import tracing

log = get_logger("run")

_FAKE_MODES = frozenset({"", "fake"})
_LIVE_MODES = frozenset({"live", "herdr"})
HERDR_KINDS = frozenset({"codex", "claude"})
_DEFAULT_AGENT = "codex"
_DEFAULT_TIMEOUT_MS = 240_000
_DEFAULT_TURN_TIMEOUT_S = 420
_DEFAULT_SANDBOX_TIMEOUT_S = 900
_DEFAULT_MAX_CONCURRENT = 2
KILL_RETRIES = 3
# Host variables the run path may read besides ``SDF_*`` agent configuration.
_PASSTHROUGH = ("E2B_API_KEY", "E2B_DOMAIN", "DAYTONA_API_KEY", "DAYTONA_API_URL", "DAYTONA_TARGET",
                "HOME", "PATH", "USER", "XDG_CONFIG_HOME")
# ``SDF_*`` names that are server configuration, never agent configuration.
_NEVER_FORWARD = ("SDF_DOTENV", "SDF_AUTH_", "SDF_DATABASE_URL", "SDF_RUN_", "SDF_FIXTURES_ROOT", "SDF_LOG_")


class RunAdapterConfigError(CredentialConfigError):
    """Public run-path adapter/provider configuration is invalid (HTTP 400)."""


class RunBusyError(RuntimeError):
    """Every live-run slot is taken (HTTP 429)."""


class CleanupFailedError(RuntimeError):
    """The sandbox could not be killed after retries."""


def resolve_sandbox_provider(environ: Mapping[str, str]) -> str:
    """Main's provider selection (``e2b`` default, ``daytona``); config errors are 400s."""

    try:
        return _main_provider(environ)
    except CredentialConfigError as exc:
        raise RunAdapterConfigError(str(exc)) from exc


def _int(environ: Mapping[str, str], name: str, default: int) -> int:
    raw = (environ.get(name) or "").strip()
    try:
        value = int(raw) if raw else default
    except ValueError as exc:
        raise RunAdapterConfigError(f"{name} must be an integer") from exc
    if value <= 0:
        raise RunAdapterConfigError(f"{name} must be positive")
    return value


def _codex_model_alias(environ: Mapping[str, str]) -> dict[str, str]:
    """``SDF_HERDR_CODEX_MODEL`` is a deprecated alias of ``SDF_CODEX_MODEL``."""

    legacy = (environ.get("SDF_HERDR_CODEX_MODEL") or "").strip()
    if not legacy:
        return {}
    log.warning("deprecated_setting", extra={"setting": "SDF_HERDR_CODEX_MODEL", "use": "SDF_CODEX_MODEL"})
    if (environ.get("SDF_CODEX_MODEL") or "").strip():
        return {}
    return {"SDF_CODEX_MODEL": legacy}


def run_environ(agent: str, environ: Mapping[str, str]) -> dict[str, str]:
    """The host environment the run path hands main's credential resolution.

    Only agent configuration (``SDF_*`` minus server settings), the key
    variable the agent's ``SDF_<AGENT>_API_KEY_ENV`` names, and the sandbox
    provider's own variables pass; ``SDF_DOTENV`` and server secrets never do.
    Headless OpenAI-family kinds with an unset base URL get their example
    default (the credential plan would apply it anyway).  Codex gets the
    example URL and ``glm-5.3`` only when the resolved key variable is
    :data:`~sdf_core.credentials.EXAMPLE_API_KEY_ENV`.  Any other Codex key
    leaves ``SDF_OPENAI_BASE_URL`` unset, which resolves to the official
    OpenAI API — the same host :func:`~sdf_core.credentials.resolved_llm_base_url`
    reports — and is never pointed at the example host.  Claude is left unset
    so the official Anthropic Messages API is used, and the example key name
    is not selected for Claude unless ``SDF_ANTHROPIC_BASE_URL`` is set
    (ADR 0034).  Otherwise Claude requires ``SDF_ANTHROPIC_API_KEY`` or
    another configured Claude key.
    """

    spec = _AGENTS[agent]
    env: dict[str, str] = {
        name: value for name, value in environ.items()
        if value and name.startswith("SDF_") and not name.startswith(_NEVER_FORWARD)
    }
    env |= {name: environ[name] for name in _PASSTHROUGH if environ.get(name)}
    env |= _codex_model_alias(environ)
    env.setdefault(spec.mode_variable, "api-key")
    explicit_base = (env.get(spec.base_url_source) or "").strip()
    # The example key is an OpenAI-compatible credential. Do not select it for
    # Claude unless the operator set a Claude-compatible base URL; otherwise
    # it would be sent to api.anthropic.com.
    allow_example_key = spec.protocol != "anthropic" or bool(explicit_base)
    if env[spec.mode_variable] == "api-key" and not env.get(spec.api_key_source):
        if spec.api_key_env_variable and allow_example_key:
            env.setdefault(spec.api_key_env_variable, EXAMPLE_API_KEY_ENV)
    key_variable = env.get(spec.api_key_env_variable) if spec.api_key_env_variable else None
    if spec.protocol == "anthropic" and not explicit_base and key_variable == EXAMPLE_API_KEY_ENV:
        env.pop(spec.api_key_env_variable, None)
        key_variable = None
    if key_variable and environ.get(key_variable):
        env[key_variable] = environ[key_variable]
    # api-key mode only: a base URL in subscription mode is a configuration
    # error (ADR 0007).  Claude stays on the official Messages API.
    # Codex has no kind default.  Filling the example host for every key
    # would send SDF_OPENAI_API_KEY / an arbitrary *_API_KEY_ENV to a host
    # the operator did not configure.  Do that only for the example key name,
    # matching pre-PR behavior.  Other unset Codex URLs stay unset and resolve
    # to the official OpenAI API (same as resolved_llm_base_url).
    if (
        env[spec.mode_variable] == "api-key"
        and spec.protocol == "openai"
        and not (env.get(spec.base_url_source) or "").strip()
    ):
        if spec.default_base_url:
            env[spec.base_url_source] = spec.default_base_url
        elif key_variable == EXAMPLE_API_KEY_ENV:
            env[spec.base_url_source] = EXAMPLE_OPENAI_BASE_URL
            if agent == "codex" and not (env.get("SDF_CODEX_MODEL") or "").strip():
                env["SDF_CODEX_MODEL"] = EXAMPLE_OPENAI_MODEL
    return env


def run_network(environ: Mapping[str, str], *, agent: str | None = None) -> dict[str, Any]:
    """E2B egress for /run: the kind's resolved LLM host plus ``SDF_RUN_EGRESS_ALLOW``.

    With no agent, hosts come from whichever kinds have a base URL set, or the
    example OpenAI-compatible host when none do.
    """

    if agent:
        kinds: tuple[str, ...] = (agent,)
    else:
        configured = tuple(kind for kind in AGENT_KINDS if (environ.get(_AGENTS[kind].base_url_source) or "").strip())
        kinds = configured or ("opencode",)
    network = {k: (list(v) if isinstance(v, (list, tuple)) else v) for k, v in inference_network(environ, kinds).items()}
    extra = [h.strip() for h in (environ.get("SDF_RUN_EGRESS_ALLOW") or "").split(",") if h.strip()]
    for host in extra:
        if "/" in host and host.split("/")[-1] == "0":
            raise RunAdapterConfigError("SDF_RUN_EGRESS_ALLOW must list hosts, not an open CIDR")
        if host not in network["allow_out"]:
            network["allow_out"].append(host)
    return network


# --- concurrency -------------------------------------------------------------

_SLOTS_LOCK = threading.Lock()
_SLOTS: dict[int, threading.BoundedSemaphore] = {}


def _slots(limit: int) -> threading.BoundedSemaphore:
    with _SLOTS_LOCK:
        if limit not in _SLOTS:
            _SLOTS[limit] = threading.BoundedSemaphore(limit)
        return _SLOTS[limit]


@contextmanager
def run_slot(adapter: NativeAdapter) -> Iterator[None]:
    """Hold one live-run slot for the request (fake runs are unbounded).

    The run is synchronous; at most ``SDF_RUN_MAX_CONCURRENT`` live runs (and
    so sandboxes) exist per process.  A request beyond that fails fast with
    :class:`RunBusyError` (HTTP 429) instead of queueing a sandbox.
    """

    limit = getattr(adapter, "max_concurrent", None)
    if not limit:
        yield
        return
    slots = _slots(limit)
    if not slots.acquire(blocking=False):
        raise RunBusyError(f"{limit} live run(s) already in progress (SDF_RUN_MAX_CONCURRENT); retry later")
    try:
        yield
    finally:
        slots.release()


# --- the metered runtime -----------------------------------------------------


class _MeteredRuntime:
    """Delegates to the controller; records one ledger row per turn."""

    def __init__(self, controller: RuntimeController, runtime: Any, adapter: "LiveRunAdapter",
                 transport: Any, attempt_id: str) -> None:
        self._controller = controller
        self._runtime = runtime
        self._adapter = adapter
        self._transport = transport
        self._attempt_id = attempt_id
        self.session_id: str | None = None
        self.turns = 0
        self.sandbox_id: str | None = None
        self.last_error_class: str | None = None
        self._session_usage: dict[str, Any] | None = None
        self.tokens = [0, 0]
        self.cost = 0.0
        self.cost_sources: set[str] = set()
        self.usage_sources: set[str] = set()

    def bind_workspace(self, attempt_id: str, workspace: Path) -> None:
        self._controller.bind_workspace(attempt_id, workspace)

    def start(self, *, attempt_id: str, agent: str):
        with tracing.span("sdf.agent.start", attempt_id=attempt_id, agent__kind=agent,
                          model=self._adapter.model) as current:
            try:
                session = self._controller.start(attempt_id=attempt_id, agent=agent)
            finally:
                self._seen_sandbox()
            self.session_id = session.session_id
            tracing.set_attributes(current, sandbox__id=getattr(self._transport, "sandbox_id", None))
        return session

    def _seen_sandbox(self) -> None:
        """Record the sandbox id once it exists and promote the Attempt (ADR 0031)."""

        self.sandbox_id = getattr(self._transport, "sandbox_id", None) or self.sandbox_id
        self._adapter.confirm_live(self._attempt_id, self.sandbox_id)

    def _usage_now(self) -> dict[str, Any] | None:
        usage = getattr(self._runtime, "usage", None)
        if callable(usage) and self.session_id:
            try:
                return dict(usage(self.session_id))
            except Exception:  # noqa: BLE001 - usage is diagnostics
                return None
        return None

    def _herdr_usage(self) -> dict[str, Any] | None:
        exec_argv = getattr(self._transport, "exec_argv", None)
        if not callable(exec_argv):
            return None
        try:
            raw = exec_argv(["python3", "-c", _usage.SESSION_USAGE_SCRIPT, self._adapter.agent], 60_000)
        except Exception as exc:  # noqa: BLE001
            log.warning("session_usage_unavailable", extra={"error": type(exc).__name__})
            return None
        return _usage.parse_session_usage(raw)

    def send(self, session_id: str, input_text: str):
        self.turns += 1
        self.sandbox_id = getattr(self._transport, "sandbox_id", None) or self.sandbox_id
        headless = self._adapter.agent not in HERDR_KINDS
        before = self._usage_now() if headless else None
        started = time.monotonic()
        error: BaseException | None = None
        session = None
        with tracing.span("sdf.agent.turn", attempt_id=self._attempt_id, agent__kind=self._adapter.agent,
                          model=self._adapter.model, sandbox__id=self.sandbox_id,
                          sandbox__provider=self._adapter.provider, turn=self.turns) as turn_span, \
                log_context(turn_id=str(self.turns)):
            log.info("turn_started")
            try:
                session = self._controller.send(session_id, input_text)
                return session
            except BaseException as exc:
                error = exc
                raise
            finally:
                self._seen_sandbox()
                seconds = time.monotonic() - started
                status = self._controller.status(session_id).status.value if self.session_id else "unknown"
                if headless:
                    turn_usage = _usage.headless_delta(before or {}, self._usage_now() or {})
                else:
                    after = self._herdr_usage()
                    turn_usage = _usage.session_delta(self._session_usage, after)
                    if after and after.get("found"):
                        self._session_usage = after
                klass = classify_error(error, status=None if error else status)
                settled = getattr(self._runtime, "last_turn_settled", None)
                if klass is None and callable(settled) and settled(session_id):
                    klass = "settled_grace"
                self.last_error_class = klass if klass != "settled_grace" else None
                row = _usage.record_turn(
                    self._adapter.db, attempt_id=self._attempt_id, agent=self._adapter.agent, turn=self.turns,
                    session_id=session_id, sandbox_id=self.sandbox_id,
                    sandbox_provider=self._adapter.provider, usage=turn_usage, status=status,
                    error_class=klass, seconds=round(seconds, 3), model=self._adapter.model,
                    environ=self._adapter.run_env,
                )
                self.tokens[0] += row.input_tokens or 0
                self.tokens[1] += row.output_tokens or 0
                self.cost += row.cost_usd or 0.0
                self.usage_sources.add(row.source)
                self.cost_sources.add(row.cost_source)
                self._adapter.db.flush()
                tracing.set_attributes(
                    turn_span, status=status, seconds=round(seconds, 3), usage_source=row.source,
                    tokens__input=row.input_tokens, tokens__output=row.output_tokens, cost_usd=row.cost_usd,
                    sandbox__id=getattr(self._transport, "sandbox_id", None) or self.sandbox_id,
                )
                if klass and klass != "settled_grace":
                    tracing.mark_error(klass, turn_span)
                elif klass:
                    tracing.set_attributes(turn_span, error_class=klass)
                log.info("turn_finished", extra={"status": status, "error_class": klass, "seconds": round(seconds, 3),
                                                 "usage_source": row.source, "input_tokens": row.input_tokens,
                                                 "output_tokens": row.output_tokens})

    def stream(self, session_id: str):
        return self._controller.stream(session_id)

    def status(self, session_id: str):
        return self._controller.status(session_id)

    def collect_workspace(self, attempt_id: str, workspace: Path) -> None:
        self._controller.collect_workspace(attempt_id, workspace)

    def terminate(self, session_id: str):
        self.sandbox_id = getattr(self._transport, "sandbox_id", None) or self.sandbox_id
        return self._controller.terminate(session_id)


class LiveRunAdapter:
    """One-Attempt live adapter that always kills its sandbox (ADR 0023)."""

    # ADR 0031: the mode this adapter produces once a real sandbox (E2B or
    # Daytona) exists. The Attempt is created ``synthetic`` and promoted to
    # ``live`` (committed at once) when the sandbox id first appears, so config
    # or sandbox-creation failures are never ``live``. The ledger ``sandbox``
    # row stays the authoritative live signal for routing (ADR 0030).
    evidence_mode = "live"
    live_after_sandbox = True

    def __init__(self, *, db: Session, environ: Mapping[str, str], agent: str, provider: str, image: str,
                 timeout_ms: int, turn_timeout_s: int, sandbox_timeout_s: int, max_concurrent: int,
                 transport_factory: Any = None, kill_by_id: Any = None) -> None:
        self.db = db
        self.agent = agent
        self.provider = provider
        self.image = image
        self.host_environ = dict(environ)
        self.run_env = run_environ(agent, environ)
        self.model = resolve_agent_models(agent, self.run_env).model
        self.timeout_ms = timeout_ms
        self.turn_timeout_s = turn_timeout_s
        self.sandbox_timeout_s = sandbox_timeout_s
        self.max_concurrent = max_concurrent
        self.network = run_network(self.run_env, agent=agent) if provider == "e2b" else None
        self._transport_factory = transport_factory or create_credentialed_transport
        self._kill_by_id = kill_by_id or self._default_kill_by_id
        # Outcome of the last run(), for the caller (execution service).
        self.last_run: dict[str, Any] = {}
        # How this agent was chosen; recorded per Attempt (ADR 0030).
        self.route: AgentRoute = explicit_route(agent)
        # Attempt id already promoted to live (confirm_live).
        self._live_confirmed: str | None = None

    # Backwards-compatible names used by the PR's tests/docs.
    @property
    def _agent(self) -> str:
        return self.agent

    @property
    def _template(self) -> str:
        return self.image

    def _runtime(self, transport: Any) -> Any:
        if self.agent in HERDR_KINDS:
            agent_args = model_agent_args((self.agent,), self.run_env)
            if self.agent == "codex":
                # Codex's hosted web_search tool is a Responses-API feature that
                # many OpenAI-compatible hosts reject (400 Unsupported tool type),
                # including the example endpoint.  Official api.openai.com keeps it.
                host = endpoint_host(self.run_env.get("SDF_OPENAI_BASE_URL", ""))
                if host and host != "api.openai.com":
                    agent_args["codex"] = (*agent_args.get("codex", ()), "-c", 'web_search="disabled"')
            return HerdrRuntime(transport=transport, timeout_ms=self.timeout_ms, agent_args=agent_args)
        kwargs = {"transport": transport, "turn_timeout_s": self.turn_timeout_s, "model": self.model}
        if self.agent == "opencode":
            from .opencode_runtime import OpenCodeHeadlessRuntime

            return OpenCodeHeadlessRuntime(**kwargs)
        if self.agent == "pi":
            from .pi_runtime import PI_EXTENSIONS_ENV, PiHeadlessRuntime, resolve_pi_extensions

            return PiHeadlessRuntime(**kwargs, extensions=resolve_pi_extensions(self.run_env.get(PI_EXTENSIONS_ENV)))
        if self.agent == "qwen":
            from .qwen_runtime import QwenHeadlessRuntime

            return QwenHeadlessRuntime(**kwargs)
        from .goose_runtime import GooseHeadlessRuntime

        # No SDF Core query relay runs on /run, so the sdf-core MCP is off.
        return GooseHeadlessRuntime(**kwargs, core_mcp=False)

    def _transport(self, attempt_id: str) -> tuple[Any, Any]:
        kwargs: dict[str, Any] = {"agents": (self.agent,), "environ": self.run_env, "provider": self.provider,
                                  "read_connection": PluginConnectionBridge(environ=self.run_env),
                                  "timeout_seconds": self.sandbox_timeout_s}
        if self.provider == "e2b":
            kwargs |= {"template": self.image, "network": self.network,
                       "metadata": {"sdf_attempt_id": attempt_id, "sdf_run": "tasks-run", "sdf_agent": self.agent}}
        else:
            kwargs["snapshot"] = self.image
        return self._transport_factory(**kwargs)

    def _default_kill_by_id(self, sandbox_id: str) -> None:
        if self.provider == "e2b":
            from e2b import Sandbox

            opts = {"api_key": self.run_env["E2B_API_KEY"]}
            if self.run_env.get("E2B_DOMAIN"):
                opts["domain"] = self.run_env["E2B_DOMAIN"]
            Sandbox.kill(sandbox_id, **opts)  # False: already gone, which is fine
            return
        from .daytona_herdr_transport import DaytonaHerdrTransport

        DaytonaHerdrTransport(snapshot=self.image, environ=self.run_env, sandbox_id=sandbox_id).close()

    def _teardown(self, transport: Any, known_id: str | None = None) -> tuple[str | None, str | None]:
        """Kill the sandbox; returns (sandbox_id, cleanup error or None).

        ``known_id`` is the id seen during the run: a runtime may already have
        closed the transport (clearing its id) before teardown runs.
        """

        sandbox_id = getattr(transport, "sandbox_id", None) or known_id
        try:
            transport.close()
            return sandbox_id, None
        except Exception as exc:  # noqa: BLE001 - retried below, then surfaced
            first = f"{type(exc).__name__}: {exc}"
        if not sandbox_id:
            return None, first
        last = first
        for attempt in range(KILL_RETRIES):
            time.sleep(min(0.5 * 2 ** attempt, 4.0))
            try:
                self._kill_by_id(sandbox_id)
                log.warning("sandbox_kill_retried", extra={"sandbox_id": sandbox_id, "retries": attempt + 1})
                return sandbox_id, None
            except Exception as exc:  # noqa: BLE001
                last = f"{type(exc).__name__}: {exc}"
        return sandbox_id, f"close: {first}; {KILL_RETRIES} kill retries failed, last: {last}"

    def check_credentials(self) -> None:
        """Provider credentials are present (config error, 400, before any Attempt)."""

        if self.provider == "e2b" and not self.run_env.get("E2B_API_KEY"):
            raise RunAdapterConfigError("E2B_API_KEY is required when SDF_RUN_ADAPTER=live and SDF_SANDBOX_PROVIDER=e2b")
        if self.provider == "daytona" and not self.run_env.get("DAYTONA_API_KEY"):
            raise RunAdapterConfigError("DAYTONA_API_KEY is required when SDF_SANDBOX_PROVIDER=daytona")

    def confirm_live(self, attempt_id: str, sandbox_id: str | None) -> None:
        """Promote the Attempt to ``live`` once a real sandbox exists (ADR 0031).

        One-way and idempotent: ``synthetic`` -> ``live`` only, never back.
        Committed at once, so a process that dies mid-run leaves a ``live``
        RUNNING Attempt exactly when a sandbox existed.
        """

        if not sandbox_id or self._live_confirmed == attempt_id:
            return
        from .db import AttemptRow

        attempt = self.db.get(AttemptRow, attempt_id)
        if attempt is None:
            return
        attempt.evidence_mode = self.evidence_mode
        self.db.commit()
        self._live_confirmed = attempt_id

    def run(self, *, attempt_id: str, workspace: Path, instructions: str) -> AdapterResult:
        # Checked in configured_run_adapter() too; kept for direct callers.
        self.check_credentials()
        self.last_run = {}
        # Correlate the routing decision with the Attempt before the agent starts (ADR 0030).
        record_route(self.db, attempt_id, self.route)
        with log_context(attempt_id=attempt_id, agent=self.agent), \
                tracing.span("sdf.agent.run", attempt_id=attempt_id, agent__kind=self.agent, model=self.model,
                             sandbox__provider=self.provider) as run_span:
            transport, _injection = self._transport(attempt_id)
            runtime = self._runtime(transport)
            controller = RuntimeController(runtime, event_sink=SqlAlchemyRuntimeEventSink(self.db),
                                           source=f"run-{self.provider}")
            metered = _MeteredRuntime(controller, runtime, self, transport, attempt_id)
            adapter = RuntimeAgentAdapter(metered, agent=self.agent)  # type: ignore[arg-type]
            started = time.monotonic()
            result: AdapterResult | None = None
            primary: BaseException | None = None
            log.info("run_started", extra={"provider": self.provider, "image": self.image, "model": self.model,
                                           "egress_allow": (self.network or {}).get("allow_out")})
            try:
                result = adapter.run(attempt_id=attempt_id, workspace=workspace, instructions=instructions)
            except BaseException as exc:
                primary = exc
            self.confirm_live(attempt_id, getattr(transport, "sandbox_id", None) or metered.sandbox_id)
            with tracing.span("sdf.sandbox.cleanup", attempt_id=attempt_id, sandbox__provider=self.provider,
                              sandbox__id=metered.sandbox_id) as cleanup_span:
                sandbox_id, cleanup_error = self._teardown(transport, metered.sandbox_id)
                tracing.set_attributes(cleanup_span, sandbox__id=sandbox_id)
                if cleanup_error:
                    # The class only: the cleanup error text is kept on the event/ledger, not the span.
                    tracing.mark_error("cleanup_failed", cleanup_span)
            seconds = time.monotonic() - started
            with log_context(sandbox_id=sandbox_id):
                error_class = "cleanup_failed" if cleanup_error else (
                    metered.last_error_class or classify_error(primary, status=result.status if result else None))
                sandbox_row = _usage.record_sandbox(
                    self.db, attempt_id=attempt_id, agent=self.agent, sandbox_id=sandbox_id,
                    sandbox_provider=self.provider, seconds=seconds,
                    status="cleanup_failed" if cleanup_error else "killed",
                    error_class="cleanup_failed" if cleanup_error else None,
                    detail={"cleanup_error": cleanup_error[-500:]} if cleanup_error else None,
                    environ=self.run_env,
                )
                if cleanup_error:
                    log.error("cleanup_failed", extra={"error": cleanup_error[-500:]})
                    self._event(attempt_id, metered.session_id or "none", cleanup_error)
                sandbox_cost = sandbox_row.cost_usd or 0.0
                self.last_run = {
                    "attempt_id": attempt_id, "error_class": error_class, "sandbox_id": sandbox_id, "seconds": seconds,
                    "input_tokens": metered.tokens[0] if "reported" in metered.usage_sources else None,
                    "output_tokens": metered.tokens[1] if "reported" in metered.usage_sources else None,
                    "cost_usd": round(metered.cost + sandbox_cost, 8),
                    "provider": f"{self.provider}:{llm_attempt_provider(self.agent, self.run_env)}"[:120],
                    "usage_source": "reported" if "reported" in metered.usage_sources else "missing",
                    "cleanup_error": cleanup_error,
                }
                self._record_on_attempt(attempt_id)
                self.db.flush()
                log.info("run_finished", extra={"error_class": error_class, "seconds": round(seconds, 3),
                                                "status": result.status if result else "error"})
            run = self.last_run
            tracing.set_attributes(
                run_span, sandbox__id=sandbox_id, seconds=round(seconds, 3), cost_usd=run["cost_usd"],
                tokens__input=run["input_tokens"], tokens__output=run["output_tokens"],
                usage_source=run["usage_source"], turns=metered.turns,
            )
            if error_class:
                tracing.mark_error(error_class, run_span)
            if primary is not None:
                if cleanup_error:
                    primary.add_note(f"sandbox cleanup also failed: {cleanup_error[-300:]}")
                raise primary
            assert result is not None
            if cleanup_error:
                return AdapterResult(attempt_id=attempt_id, status="cleanup_failed", changed_files=result.changed_files,
                                     stdout=result.stdout, stderr=f"sandbox cleanup failed: {cleanup_error[-500:]}",
                                     exit_code=1)
            return result

    def _record_on_attempt(self, attempt_id: str) -> None:
        from .db import AttemptRow

        attempt = self.db.get(AttemptRow, attempt_id)
        if attempt is None:
            return
        run = self.last_run
        attempt.cost_usd = run["cost_usd"]
        attempt.provider = run["provider"][:120]
        attempt.latency_ms = round(run["seconds"] * 1000, 1)
        attempt.input_tokens = run["input_tokens"]
        attempt.output_tokens = run["output_tokens"]
        attempt.error_class = run["error_class"]

    def _event(self, attempt_id: str, session_id: str, detail: str) -> None:
        try:
            with self.db.begin_nested():
                SqlAlchemyRuntimeEventSink(self.db).append(RuntimeEvent(
                    attempt_id=attempt_id, session_id=session_id, sequence=1, kind=RuntimeEventKind.TERMINATED,
                    status=RuntimeStatus.TERMINATED, payload={"cleanup_failed": True, "detail": detail[-500:]},
                    source=f"run-{self.provider}-cleanup",
                ))
        except Exception as exc:  # noqa: BLE001 - the ledger/attempt still carry it
            log.error("cleanup_event_not_recorded", extra={"error": type(exc).__name__})


def configured_run_adapter(db: Session, *, environ: Mapping[str, str] | None = None) -> NativeAdapter:
    """Select the NativeAdapter for ``POST /tasks/{id}/run`` (fail closed on config)."""

    env = environ if environ is not None else os.environ
    mode = (env.get("SDF_RUN_ADAPTER") or "fake").strip().lower()
    if mode in _FAKE_MODES:
        return FakeNativeAdapter("app.py", "print('updated')\n")
    if mode not in _LIVE_MODES:
        raise RunAdapterConfigError(f"SDF_RUN_ADAPTER must be fake or live (alias herdr); got {mode!r}")
    agent = (env.get("SDF_RUN_AGENT") or _DEFAULT_AGENT).strip().lower() or _DEFAULT_AGENT
    max_concurrent = _int(env, "SDF_RUN_MAX_CONCURRENT", _DEFAULT_MAX_CONCURRENT)
    if agent == "auto":
        route = _measured_route(db, env, capacity=max_concurrent)
        agent = route.agent or ""
    elif agent not in AGENT_KINDS:
        raise RunAdapterConfigError(f"SDF_RUN_AGENT must be auto or one of {', '.join(AGENT_KINDS)}; got {agent!r}")
    else:
        route = explicit_route(agent)
    provider = resolve_sandbox_provider(env)
    if provider == "e2b":
        image = (env.get("SDF_E2B_AGENTS_TEMPLATE") or DEFAULT_TEMPLATE).strip() or DEFAULT_TEMPLATE
    else:
        image = (env.get("SDF_DAYTONA_SNAPSHOT") or "").strip()
        if not image:
            raise RunAdapterConfigError("SDF_SANDBOX_PROVIDER=daytona requires SDF_DAYTONA_SNAPSHOT")
        if (env.get("SDF_RUN_DAYTONA_UNRESTRICTED_EGRESS") or "").strip() not in ("1", "true", "yes"):
            raise RunAdapterConfigError(
                "Daytona sandboxes cannot be egress-locked at creation by SDF; set "
                "SDF_RUN_DAYTONA_UNRESTRICTED_EGRESS=1 to accept unrestricted egress for /run (ADR 0023)")
    if agent == "pi":
        from .pi_runtime import PI_EXTENSIONS_ENV, resolve_pi_extensions

        try:
            resolve_pi_extensions(env.get(PI_EXTENSIONS_ENV))
        except ValueError as exc:
            raise RunAdapterConfigError(str(exc)) from exc
    adapter = LiveRunAdapter(
        db=db, environ=env, agent=agent, provider=provider, image=image,
        timeout_ms=_int(env, "SDF_HERDR_TIMEOUT_MS", _DEFAULT_TIMEOUT_MS),
        turn_timeout_s=_int(env, "SDF_RUN_TURN_TIMEOUT_S", _DEFAULT_TURN_TIMEOUT_S),
        sandbox_timeout_s=_int(env, "SDF_E2B_SANDBOX_TIMEOUT_SECONDS", _DEFAULT_SANDBOX_TIMEOUT_S),
        max_concurrent=max_concurrent,
    )
    # Missing provider credentials are a config error before any Attempt exists.
    adapter.check_credentials()
    # Routing only picks the kind: the run path, Tool Proxy, egress lock and
    # sandbox below are the same for every agent (ADR 0030).
    adapter.route = route
    return adapter


def _measured_route(db: Session, env: Mapping[str, str], *, capacity: int) -> AgentRoute:
    """``SDF_RUN_AGENT=auto``: choose among ``SDF_RUN_ROUTE_AGENTS`` from trusted measurements."""

    candidates = [a.strip().lower() for a in (env.get("SDF_RUN_ROUTE_AGENTS") or "").split(",") if a.strip()]
    if not candidates:
        raise RunAdapterConfigError("SDF_RUN_AGENT=auto requires SDF_RUN_ROUTE_AGENTS (comma-separated agent kinds)")
    unknown = [a for a in candidates if a not in AGENT_KINDS]
    if unknown:
        raise RunAdapterConfigError(f"SDF_RUN_ROUTE_AGENTS has unknown agent kind(s): {', '.join(unknown)}")
    try:
        request = routing_request_from_env(env)
    except ValueError as exc:
        raise RunAdapterConfigError(str(exc)) from exc
    route = select_agent(db, candidates, request, capacity=capacity)
    if route.agent is None:
        # Fail closed: no measurement, no route (never a configured fallback).
        raise RunAdapterConfigError(f"measured routing: {route.reason}")
    log.info("run_routed", extra={"agent": route.agent, "reason": route.reason})
    return route


# Back-compat name from the first version of this PR.
_ClosingHerdrRunAdapter = LiveRunAdapter

__all__ = [
    "CleanupFailedError", "CredentialConfigError", "LiveRunAdapter", "RunAdapterConfigError", "RunBusyError",
    "configured_run_adapter", "resolve_sandbox_provider", "run_environ", "run_network", "run_slot",
]
