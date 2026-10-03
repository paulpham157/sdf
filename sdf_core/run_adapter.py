"""Adapter selection for the public ``POST /tasks/{id}/run`` path.

Defaults to ``FakeNativeAdapter`` so CI and local unit tests stay offline.
When ``SDF_RUN_ADAPTER=herdr`` (alias ``live``), builds a one-Attempt
``RuntimeAgentAdapter`` over ``HerdrRuntime`` with create-time credentials
(ADR-0007) in a disposable E2B sandbox, then closes/kills the box after the
Attempt. Daytona selection via ``SDF_SANDBOX_PROVIDER`` is recognized but not
implemented here (owned by issue #44).
"""

from __future__ import annotations

import os
from collections.abc import Mapping
from pathlib import Path

from sqlalchemy.orm import Session

from .adapter import AdapterResult, FakeNativeAdapter, NativeAdapter, RuntimeAgentAdapter
from .credential_injection import create_credentialed_transport
from .credentials import CredentialConfigError
from .herdr_runtime import HerdrRuntime
from .plugin_bridge import PluginConnectionBridge
from .runtime import RuntimeController, SqlAlchemyRuntimeEventSink

_FAKE_MODES = frozenset({"", "fake"})
_HERDR_MODES = frozenset({"herdr", "live"})
_SUPPORTED_PROVIDERS = frozenset({"e2b"})
_DEFAULT_TEMPLATE = "sdf-herdr-agents"
_DEFAULT_AGENT = "codex"
_DEFAULT_TIMEOUT_MS = 240_000
_DEFAULT_SANDBOX_TIMEOUT_S = 900


class RunAdapterConfigError(Exception):
    """Public run-path adapter/provider configuration is invalid."""


def resolve_sandbox_provider(environ: Mapping[str, str]) -> str:
    """Return the configured sandbox provider name (E2B only in this tree)."""

    raw = (environ.get("SDF_SANDBOX_PROVIDER") or "e2b").strip().lower()
    if not raw:
        raw = "e2b"
    if raw == "daytona":
        raise RunAdapterConfigError(
            "SDF_SANDBOX_PROVIDER=daytona is not available on this branch; "
            "Daytona wiring lives in issue #44. Use e2b (default) here."
        )
    if raw not in _SUPPORTED_PROVIDERS:
        raise RunAdapterConfigError(
            f"SDF_SANDBOX_PROVIDER must be one of {', '.join(sorted(_SUPPORTED_PROVIDERS))}; got {raw!r}"
        )
    return raw


def _agent_args(agent: str, environ: Mapping[str, str]) -> dict[str, tuple[str, ...]]:
    """Optional per-agent CLI args from operator env (tests pin models here)."""

    if agent != "codex":
        return {}
    model = (environ.get("SDF_HERDR_CODEX_MODEL") or "").strip()
    if not model:
        return {}
    return {"codex": ("-m", model)}


def _herdr_environ(environ: Mapping[str, str]) -> dict[str, str]:
    """Host env for credential resolution and E2B; never log values."""

    keys = (
        "E2B_API_KEY",
        "E2B_DOMAIN",
        "HOME",
        "PATH",
        "USER",
        "XDG_CONFIG_HOME",
        "SDF_CREDENTIAL_MODE_CODEX",
        "SDF_CREDENTIAL_MODE_CLAUDE",
        "SDF_CONNECTION_CODEX",
        "SDF_CONNECTION_CLAUDE",
        "SDF_ANTHROPIC_API_KEY",
        "SDF_OPENAI_API_KEY",
        "SDF_ANTHROPIC_BASE_URL",
        "SDF_OPENAI_BASE_URL",
        "SDF_DOTENV",
    )
    return {name: environ[name] for name in keys if name in environ and environ[name]}


class _ClosingHerdrRunAdapter:
    """One-Attempt Herdr NativeAdapter that always closes its E2B sandbox."""

    def __init__(
        self,
        *,
        db: Session,
        environ: Mapping[str, str],
        agent: str,
        template: str,
        timeout_ms: int,
        sandbox_timeout_seconds: int,
    ) -> None:
        self._db = db
        self._environ = dict(environ)
        self._agent = agent
        self._template = template
        self._timeout_ms = timeout_ms
        self._sandbox_timeout_seconds = sandbox_timeout_seconds

    def run(self, *, attempt_id: str, workspace: Path, instructions: str) -> AdapterResult:
        resolve_sandbox_provider(self._environ)
        host_env = _herdr_environ(self._environ)
        if "E2B_API_KEY" not in host_env:
            raise RunAdapterConfigError("E2B_API_KEY is required when SDF_RUN_ADAPTER=herdr")
        bridge = PluginConnectionBridge(environ=host_env)
        transport, _injection = create_credentialed_transport(
            template=self._template,
            agents=(self._agent,),
            environ=host_env,
            read_connection=bridge,
            timeout_seconds=self._sandbox_timeout_seconds,
        )
        runtime = HerdrRuntime(
            transport=transport,
            timeout_ms=self._timeout_ms,
            agent_args=_agent_args(self._agent, self._environ),
        )
        adapter = RuntimeAgentAdapter(
            RuntimeController(
                runtime,
                event_sink=SqlAlchemyRuntimeEventSink(self._db),
                source="herdr-e2b",
            ),
            agent=self._agent,
        )
        try:
            return adapter.run(attempt_id=attempt_id, workspace=workspace, instructions=instructions)
        finally:
            sandbox_id = transport.sandbox_id
            try:
                transport.close()
            except Exception:  # noqa: BLE001 - Attempt owner must still finish
                if sandbox_id:
                    try:
                        from e2b import Sandbox

                        Sandbox.kill(sandbox_id, api_key=host_env["E2B_API_KEY"])
                    except Exception:  # noqa: BLE001 - best-effort teardown
                        pass


def configured_run_adapter(
    db: Session,
    *,
    environ: Mapping[str, str] | None = None,
) -> NativeAdapter:
    """Select the NativeAdapter for ``POST /tasks/{id}/run``.

    ``SDF_RUN_ADAPTER`` (default ``fake``):
    - ``fake`` / unset — deterministic FakeNativeAdapter (CI default)
    - ``herdr`` / ``live`` — real HerdrRuntime in an E2B sandbox
    """

    env = environ if environ is not None else os.environ
    mode = (env.get("SDF_RUN_ADAPTER") or "fake").strip().lower()
    if mode in _FAKE_MODES:
        return FakeNativeAdapter("app.py", "print('updated')\n")
    if mode not in _HERDR_MODES:
        raise RunAdapterConfigError(
            f"SDF_RUN_ADAPTER must be one of fake, herdr (or live); got {mode!r}"
        )
    agent = (env.get("SDF_RUN_AGENT") or _DEFAULT_AGENT).strip().lower() or _DEFAULT_AGENT
    template = (env.get("SDF_E2B_AGENTS_TEMPLATE") or _DEFAULT_TEMPLATE).strip() or _DEFAULT_TEMPLATE
    try:
        timeout_ms = int((env.get("SDF_HERDR_TIMEOUT_MS") or str(_DEFAULT_TIMEOUT_MS)).strip())
        sandbox_timeout = int(
            (env.get("SDF_E2B_SANDBOX_TIMEOUT_SECONDS") or str(_DEFAULT_SANDBOX_TIMEOUT_S)).strip()
        )
    except ValueError as exc:
        raise RunAdapterConfigError("Herdr/E2B timeouts must be integers") from exc
    if timeout_ms <= 0 or sandbox_timeout <= 0:
        raise RunAdapterConfigError("Herdr/E2B timeouts must be positive")
    # Fail closed on provider before the HTTP handler starts work.
    resolve_sandbox_provider(env)
    return _ClosingHerdrRunAdapter(
        db=db,
        environ=env,
        agent=agent,
        template=template,
        timeout_ms=timeout_ms,
        sandbox_timeout_seconds=sandbox_timeout,
    )


# Re-export for HTTP mapping: credential plan errors still raise CredentialConfigError.
__all__ = [
    "RunAdapterConfigError",
    "CredentialConfigError",
    "configured_run_adapter",
    "resolve_sandbox_provider",
]
