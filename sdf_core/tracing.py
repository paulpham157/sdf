"""Vendor-neutral OpenTelemetry tracing for SDF Core (ADR 0024).

Off by default.  With ``SDF_OTEL_ENABLED`` unset nothing here imports the
OpenTelemetry SDK, no provider or exporter exists, and :func:`span` returns a
shared no-op context manager.  When enabled, spans are exported over OTLP/HTTP
(or to the console for development); any OTLP backend works - Jaeger, Grafana
Tempo, Langfuse, LangSmith - through the standard ``OTEL_*`` variables, which
the SDK reads itself:

* ``OTEL_EXPORTER_OTLP_ENDPOINT`` / ``OTEL_EXPORTER_OTLP_TRACES_ENDPOINT``
* ``OTEL_EXPORTER_OTLP_HEADERS`` (backend auth; never logged or attached to spans)
* ``OTEL_SERVICE_NAME`` (default ``sdf-core``)
* ``OTEL_TRACES_SAMPLER`` / ``OTEL_TRACES_SAMPLER_ARG``

SDF switches: ``SDF_OTEL_ENABLED=1``; ``SDF_OTEL_EXPORTER=otlp|console``
(``OTEL_TRACES_EXPORTER=console`` is honoured too); ``SDF_OTEL_DB=1`` adds
SQLAlchemy spans (off by default: the coordinator polls the DB).

Attribute hygiene: :func:`span` and :func:`set_attributes` accept only the
allow-listed keys in :data:`ATTRIBUTE_KEYS` (ids, kinds, counters, costs, error
classes).  Prompts, instructions, agent output, code, diffs, headers and
exception messages are never attached.  Every exported string attribute -
including those added by the FastAPI/SQLAlchemy instrumentations - is passed
through :func:`sdf_core.observability.redact` by a wrapping exporter, and
exceptions are recorded as their class name only.

Tracing runs only in SDF Core on the master: nothing is installed in, sent to,
or proxied through sandboxes, whose egress stays as configured (Vultr-only).
"""

from __future__ import annotations

import contextvars
import logging
import os
from collections.abc import Callable, Iterator, Mapping
from contextlib import contextmanager
from typing import Any

from .observability import redact

_log = logging.getLogger("sdf.tracing")

TRUTHY = {"1", "true", "yes", "on"}
DEFAULT_SERVICE_NAME = "sdf-core"
MAX_ATTRIBUTE_LEN = 256

# The only attributes SDF code may set.  Keep this list free of free-text fields.
ATTRIBUTE_KEYS = frozenset({
    "sdf.task_id", "sdf.run_id", "sdf.plan_id", "sdf.attempt_id", "sdf.parent_attempt_id",
    "sdf.request_id", "sdf.principal",
    "sdf.agent.kind", "sdf.agent.id", "sdf.agent.role", "sdf.model", "sdf.model_tier",
    "sdf.sandbox.id", "sdf.sandbox.provider", "sdf.sandbox.template",
    "sdf.turn", "sdf.turns", "sdf.status", "sdf.outcome", "sdf.ok", "sdf.seconds",
    "sdf.error_class", "sdf.cleanup_error", "sdf.verdict",
    "sdf.tokens.input", "sdf.tokens.output", "sdf.cost_usd", "sdf.usage_source", "sdf.provider",
    "sdf.subtasks", "sdf.agents",
})


class _NoopSpan:
    """Stands in for a span when tracing is off; every method is a no-op."""

    __slots__ = ()

    def set_attribute(self, key: str, value: Any) -> None:  # noqa: D401 - API mirror
        return None

    def set_attributes(self, attributes: Mapping[str, Any]) -> None:
        return None

    def is_recording(self) -> bool:
        return False


NOOP_SPAN = _NoopSpan()


class _NoopContext:
    __slots__ = ()

    def __enter__(self) -> _NoopSpan:
        return NOOP_SPAN

    def __exit__(self, *exc: Any) -> bool:
        return False


_NOOP_CONTEXT = _NoopContext()


class _State:
    enabled = False
    provider: Any = None
    tracer: Any = None
    exporter: Any = None
    db_instrumented = False


_state = _State()


def enabled() -> bool:
    return _state.enabled


def _truthy(value: str | None) -> bool:
    return (value or "").strip().lower() in TRUTHY


def clean_attributes(attrs: Mapping[str, Any]) -> dict[str, Any]:
    """Allow-listed, redacted, bounded attributes; ``None`` values are dropped."""

    clean: dict[str, Any] = {}
    for key, value in attrs.items():
        if value is None:
            continue
        if key not in ATTRIBUTE_KEYS:
            _log.debug("dropping non-allow-listed span attribute %s", key)
            continue
        if isinstance(value, bool) or isinstance(value, (int, float)):
            clean[key] = value
        else:
            clean[key] = redact(str(value))[:MAX_ATTRIBUTE_LEN]
    return clean


def _attr_kwargs(fields: Mapping[str, Any]) -> dict[str, Any]:
    """``task_id=...`` style keyword names -> ``sdf.task_id`` keys (``__`` -> ``.``)."""

    return {f"sdf.{name.replace('__', '.')}": value for name, value in fields.items()}


def span(name: str, **fields: Any):
    """Start a current span ``name`` with allow-listed attributes.

    Keyword names map to attribute keys: ``task_id`` -> ``sdf.task_id``,
    ``agent__kind`` -> ``sdf.agent.kind``.  Returns a shared no-op context
    manager when tracing is off.
    """

    if not _state.enabled:
        return _NOOP_CONTEXT
    return _real_span(name, clean_attributes(_attr_kwargs(fields)))


@contextmanager
def _real_span(name: str, attributes: dict[str, Any]) -> Iterator[Any]:
    from opentelemetry.trace import Status, StatusCode

    with _state.tracer.start_as_current_span(
        name, attributes=attributes, record_exception=False, set_status_on_exception=False,
    ) as current:
        try:
            yield current
        except BaseException as exc:
            # Class name only: exception messages may carry prompts or output.
            current.set_attribute("error.type", type(exc).__name__)
            current.set_status(Status(StatusCode.ERROR, type(exc).__name__))
            raise


def set_attributes(target: Any = None, **fields: Any) -> None:
    """Set allow-listed attributes on ``target`` (default: the current span)."""

    if not _state.enabled:
        return
    if target is None:
        from opentelemetry import trace

        target = trace.get_current_span()
    if target is NOOP_SPAN or not target.is_recording():
        return
    target.set_attributes(clean_attributes(_attr_kwargs(fields)))


def mark_error(error_class: str | None, target: Any = None) -> None:
    """Flag ``target`` (default: current span) as failed with an ADR 0023 error class."""

    if not _state.enabled or not error_class:
        return
    from opentelemetry import trace
    from opentelemetry.trace import Status, StatusCode

    target = target or trace.get_current_span()
    if target is NOOP_SPAN or not target.is_recording():
        return
    target.set_attributes(clean_attributes({"sdf.error_class": error_class}))
    target.set_status(Status(StatusCode.ERROR, str(error_class)[:64]))


def current_ids() -> tuple[str, str] | None:
    """``(trace_id, span_id)`` of the current recording span, as hex, or ``None``."""

    if not _state.enabled:
        return None
    from opentelemetry import trace

    ctx = trace.get_current_span().get_span_context()
    if not ctx.is_valid:
        return None
    return format(ctx.trace_id, "032x"), format(ctx.span_id, "016x")


def current_trace_id() -> str | None:
    ids = current_ids()
    return ids[0] if ids else None


def in_current_context(fn: Callable[..., Any]) -> Callable[..., Any]:
    """Wrap ``fn`` so each call runs in a copy of the caller's context.

    Threads (pools, background runs) do not inherit :mod:`contextvars`; this
    carries the active span (and the ADR 0023 log context) across.  Each call
    gets its own copy, so one wrapper may run concurrently.
    """

    captured = contextvars.copy_context()

    def call(*args: Any, **kwargs: Any) -> Any:
        return captured.copy().run(fn, *args, **kwargs)

    return call


# --- setup ---------------------------------------------------------------------


def _redacting_exporter(inner: Any) -> Any:
    from opentelemetry.sdk.trace import Event, ReadableSpan
    from opentelemetry.sdk.trace.export import SpanExporter

    def scrub(attributes: Mapping[str, Any] | None) -> dict[str, Any]:
        out: dict[str, Any] = {}
        for key, value in (attributes or {}).items():
            if isinstance(value, str):
                out[key] = redact(value)
            elif isinstance(value, (tuple, list)):
                out[key] = tuple(redact(v) if isinstance(v, str) else v for v in value)
            else:
                out[key] = value
        return out

    class RedactingSpanExporter(SpanExporter):
        """Redact every string attribute (and event attribute) before export."""

        def __init__(self, wrapped: Any) -> None:
            self.wrapped = wrapped

        def export(self, spans: Any) -> Any:
            scrubbed = [
                ReadableSpan(
                    name=s.name, context=s.context, parent=s.parent, resource=s.resource,
                    attributes=scrub(s.attributes),
                    events=[Event(e.name, scrub(e.attributes), e.timestamp) for e in s.events],
                    links=s.links, kind=s.kind, status=s.status, start_time=s.start_time,
                    end_time=s.end_time, instrumentation_scope=s.instrumentation_scope,
                )
                for s in spans
            ]
            return self.wrapped.export(scrubbed)

        def shutdown(self) -> None:
            self.wrapped.shutdown()

        def force_flush(self, timeout_millis: int = 30000) -> bool:
            return self.wrapped.force_flush(timeout_millis)

    return RedactingSpanExporter(inner)


def _otlp_kwargs(env: Mapping[str, str]) -> dict[str, Any]:
    """Endpoint/headers from an explicit mapping (the SDK itself only reads ``os.environ``)."""

    if env is os.environ:
        return {}
    kwargs: dict[str, Any] = {}
    endpoint = env.get("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
    if not endpoint and env.get("OTEL_EXPORTER_OTLP_ENDPOINT"):
        endpoint = env["OTEL_EXPORTER_OTLP_ENDPOINT"].rstrip("/") + "/v1/traces"
    if endpoint:
        kwargs["endpoint"] = endpoint
    headers = env.get("OTEL_EXPORTER_OTLP_TRACES_HEADERS") or env.get("OTEL_EXPORTER_OTLP_HEADERS")
    if headers:
        from opentelemetry.util.re import parse_env_headers

        kwargs["headers"] = parse_env_headers(headers, liberal=True)
    return kwargs


def configure_tracing(environ: Mapping[str, str] | None = None, *, exporter: Any = None) -> bool:
    """Enable tracing when ``SDF_OTEL_ENABLED`` is truthy; return whether it is on.

    Idempotent.  ``exporter`` (tests) replaces the env-selected exporter and is
    flushed synchronously.  A missing ``sdf[otel]`` install logs one warning
    and leaves tracing off.
    """

    env = os.environ if environ is None else environ
    if _state.enabled:
        return True
    if not _truthy(env.get("SDF_OTEL_ENABLED")):
        return False
    try:
        from opentelemetry.sdk.resources import SERVICE_NAME, Resource
        from opentelemetry.sdk.trace import TracerProvider
        from opentelemetry.sdk.trace.export import BatchSpanProcessor, ConsoleSpanExporter, SimpleSpanProcessor
    except ImportError:
        _log.warning("SDF_OTEL_ENABLED is set but OpenTelemetry is not installed; install sdf[otel]")
        return False

    # TracerProvider reads OTEL_TRACES_SAMPLER(_ARG) from the process environment.
    resource = Resource.create({SERVICE_NAME: env.get("OTEL_SERVICE_NAME") or DEFAULT_SERVICE_NAME})
    provider = TracerProvider(resource=resource)
    kind = (env.get("SDF_OTEL_EXPORTER") or env.get("OTEL_TRACES_EXPORTER") or "otlp").strip().lower()
    label = kind
    if exporter is not None:
        label = "custom"
        processor = SimpleSpanProcessor(_redacting_exporter(exporter))
    elif kind == "console":
        processor = SimpleSpanProcessor(_redacting_exporter(ConsoleSpanExporter()))
    elif kind in {"otlp", "otlp_proto_http", ""}:
        try:
            from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
        except ImportError:
            _log.warning("OTLP exporter is not installed; install sdf[otel]")
            return False
        # Endpoint, headers, timeout and compression come from OTEL_EXPORTER_OTLP_*.
        exporter = OTLPSpanExporter(**_otlp_kwargs(env))
        processor = BatchSpanProcessor(_redacting_exporter(exporter))
    else:
        _log.warning("unsupported SDF_OTEL_EXPORTER %r; tracing stays off", kind)
        return False
    provider.add_span_processor(processor)
    _state.provider = provider
    _state.tracer = provider.get_tracer("sdf_core")
    _state.exporter = exporter
    _state.enabled = True
    # Names only - never endpoints' credentials or header values.
    _log.info("tracing enabled", extra={"exporter": label})
    return True


def instrument_fastapi(app: Any) -> bool:
    """Add FastAPI server spans (no headers or bodies captured)."""

    if not _state.enabled:
        return False
    try:
        from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor
    except ImportError:
        _log.warning("opentelemetry-instrumentation-fastapi is not installed; no request spans")
        return False
    if getattr(app, "_is_instrumented_by_opentelemetry", False):
        return True
    FastAPIInstrumentor.instrument_app(app, tracer_provider=_state.provider, excluded_urls="healthz")
    app.middleware_stack = None  # rebuilt on the next request, now with the tracing middleware
    return True


def instrument_sqlalchemy(engines: Any, environ: Mapping[str, str] | None = None) -> bool:
    """Add DB spans for ``engines`` when ``SDF_OTEL_DB`` is truthy (statements carry no values).

    The instrumentor is a singleton, so every engine must be passed in one call.
    """

    env = os.environ if environ is None else environ
    if not _state.enabled or not _truthy(env.get("SDF_OTEL_DB")):
        return False
    try:
        from opentelemetry.instrumentation.sqlalchemy import SQLAlchemyInstrumentor
    except ImportError:
        _log.warning("opentelemetry-instrumentation-sqlalchemy is not installed; no DB spans")
        return False
    if not isinstance(engines, (list, tuple)):
        engines = [engines]
    unique = list({id(e): e for e in engines if e is not None}.values())
    SQLAlchemyInstrumentor().instrument(engines=unique, tracer_provider=_state.provider, enable_commenter=False)
    _state.db_instrumented = True
    return True


def shutdown() -> None:
    """Flush and drop the provider (tests and process exit)."""

    provider = _state.provider
    _state.enabled = False
    _state.provider = _state.tracer = _state.exporter = None
    if _state.db_instrumented:
        from opentelemetry.instrumentation.sqlalchemy import SQLAlchemyInstrumentor

        SQLAlchemyInstrumentor().uninstrument()
        _state.db_instrumented = False
    if provider is not None:
        provider.shutdown()
