"""Vendor-neutral OpenTelemetry tracing for SDF Core (ADR 0024).

Off by default.  With ``SDF_OTEL_ENABLED`` unset nothing here imports the
OpenTelemetry SDK, no provider or exporter exists, and :func:`span` returns a
shared no-op context manager.  When enabled, spans are exported over OTLP/HTTP
(or to the console for development); any OTLP backend works - Jaeger, Grafana
Tempo, Langfuse, LangSmith - through the standard ``OTEL_*`` variables.  The
API reads them from the environment *and* the repo-root ``.env``:

* ``OTEL_EXPORTER_OTLP_ENDPOINT`` / ``OTEL_EXPORTER_OTLP_TRACES_ENDPOINT``
* ``OTEL_EXPORTER_OTLP_HEADERS`` (backend auth; values never logged or attached to spans)
* ``OTEL_SERVICE_NAME`` (default ``sdf-core``)

``OTEL_TRACES_SAMPLER(_ARG)``, ``OTEL_EXPORTER_OTLP_TIMEOUT/COMPRESSION`` and
``OTEL_RESOURCE_ATTRIBUTES`` are read by the SDK from the process environment
only.  ``OTEL_SDK_DISABLED=true`` and ``OTEL_TRACES_EXPORTER=none`` keep
tracing off.  OTLP/gRPC is not supported (OTLP/HTTP protobuf only).

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
import sys
import logging
import os
import re
from urllib.parse import unquote
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
    "sdf.subtasks", "sdf.agents", "sdf.evidence_mode",
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
    fastapi_apps: list = []


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

    tracer = _state.tracer  # captured once: a concurrent shutdown() cannot null it mid-span
    if not _state.enabled or tracer is None:
        return _NOOP_CONTEXT
    return _real_span(tracer, name, clean_attributes(_attr_kwargs(fields)))


def _record_exception(current: Any, exc: BaseException) -> None:
    """Class name only: exception messages may carry prompts or output.

    Client errors (an ``HTTPException`` with a 4xx ``status_code``) are the
    caller's mistake, not a failed span: only ``error.type`` is recorded.
    """

    from opentelemetry.trace import Status, StatusCode

    current.set_attribute("error.type", type(exc).__name__)
    status_code = getattr(exc, "status_code", None)
    if isinstance(status_code, int) and 400 <= status_code < 500:
        return
    current.set_status(Status(StatusCode.ERROR, type(exc).__name__))


@contextmanager
def _real_span(tracer: Any, name: str, attributes: dict[str, Any]) -> Iterator[Any]:
    with tracer.start_as_current_span(
        name, attributes=attributes, record_exception=False, set_status_on_exception=False,
    ) as current:
        try:
            yield current
        except BaseException as exc:
            _record_exception(current, exc)
            raise


def detached_root(name: str, **fields: Any) -> Any:
    """Start (not activate) a new *root* span linked to the current span.

    For work that outlives the request that started it (background runs): the
    request span ends first, so the work gets its own trace with a span link
    back to the request.  End it with :func:`activate`.  ``NOOP_SPAN`` when off.
    """

    tracer = _state.tracer
    if not _state.enabled or tracer is None:
        return NOOP_SPAN
    from opentelemetry import trace
    from opentelemetry.context import Context

    parent = trace.get_current_span().get_span_context()
    links = [trace.Link(parent)] if parent.is_valid else []
    return tracer.start_span(name, context=Context(), links=links,
                             attributes=clean_attributes(_attr_kwargs(fields)))


@contextmanager
def activate(target: Any) -> Iterator[Any]:
    """Make a :func:`detached_root` span current for the block, then end it."""

    if target is NOOP_SPAN or target is None:
        yield NOOP_SPAN
        return
    from opentelemetry import trace

    with trace.use_span(target, end_on_exit=True, record_exception=False, set_status_on_exception=False):
        try:
            yield target
        except BaseException as exc:
            _record_exception(target, exc)
            raise


def span_trace_id(target: Any) -> str | None:
    """Hex trace id of ``target`` (``None`` for the no-op span)."""

    if target is NOOP_SPAN or target is None:
        return None
    ctx = target.get_span_context()
    return format(ctx.trace_id, "032x") if ctx.is_valid else None


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


# Attributes the HTTP/DB instrumentations add that identify people or carry
# free text: never exported.
_DROP_ATTRIBUTES = frozenset({
    "net.peer.ip", "net.peer.port", "net.sock.peer.addr", "net.sock.peer.port", "client.address", "client.port", "http.client_ip",
    "network.peer.address", "http.user_agent", "user_agent.original", "url.query",
})
_DROP_PREFIXES = ("http.request.header.", "http.response.header.")
# URL-valued attributes keep scheme/host/path only: query strings, fragments and
# userinfo can carry tokens whose names no pattern recognises.
_URL_ATTRIBUTES = frozenset({"http.url", "http.target", "url.full", "url.path"})
_USERINFO = re.compile(r"//[^/@]*@")
_SAFE_STATUS = re.compile(r"[A-Za-z_][A-Za-z0-9_.]{0,63}")
EXCEPTION_EVENT = "exception"


def _strip_url(value: str) -> str:
    return _USERINFO.sub("//", value.split("?", 1)[0].split("#", 1)[0])


def _scrub(attributes: Mapping[str, Any] | None) -> dict[str, Any]:
    out: dict[str, Any] = {}
    for key, value in (attributes or {}).items():
        if key in _DROP_ATTRIBUTES or key.startswith(_DROP_PREFIXES):
            continue
        if isinstance(value, str):
            out[key] = redact(_strip_url(value) if key in _URL_ATTRIBUTES else value)
        elif isinstance(value, (tuple, list)):
            out[key] = tuple(redact(v) if isinstance(v, str) else v for v in value)
        else:
            out[key] = value
    return out


def _scrub_event(event: Any) -> Any:
    from opentelemetry.sdk.trace import Event

    if event.name == EXCEPTION_EVENT:
        # Instrumentations record message and stacktrace: both may hold prompts or output.
        kept = {"exception.type": event.attributes["exception.type"]} \
            if event.attributes and "exception.type" in event.attributes else {}
        return Event(EXCEPTION_EVENT, kept, event.timestamp)
    return Event(event.name, _scrub(event.attributes), event.timestamp)


def _scrub_status(status: Any, attributes: Mapping[str, Any]) -> Any:
    """Keep only an identifier-like description (error class / exception class).

    The SQLAlchemy instrumentation puts ``str(exc)`` here, which for
    Postgres includes ``DETAIL: Key (col)=(value)``.
    """

    from opentelemetry.trace import Status

    description = status.description
    if not description or _SAFE_STATUS.fullmatch(description):
        return status
    fallback = attributes.get("error.type")
    safe = fallback if isinstance(fallback, str) and _SAFE_STATUS.fullmatch(fallback) else "error"
    return Status(status.status_code, safe)


def _redacting_exporter(inner: Any) -> Any:
    from opentelemetry.sdk.resources import Resource
    from opentelemetry.sdk.trace import ReadableSpan
    from opentelemetry.sdk.trace.export import SpanExporter
    from opentelemetry.trace import Link

    resources: dict[int, Any] = {}

    def resource(original: Any) -> Any:
        key = id(original)
        if key not in resources:
            resources[key] = Resource(_scrub(original.attributes), original.schema_url)
        return resources[key]

    class RedactingSpanExporter(SpanExporter):
        """Scrub spans before export.

        String attributes (span, event, link, resource) are redacted; URL
        attributes lose query/fragment/userinfo; client address, user agent and
        captured headers are dropped; ``exception`` events keep only the type;
        status descriptions keep only an identifier.
        """

        def __init__(self, wrapped: Any) -> None:
            self.wrapped = wrapped

        def export(self, spans: Any) -> Any:
            scrubbed = []
            for s in spans:
                attributes = _scrub(s.attributes)
                scrubbed.append(ReadableSpan(
                    name=s.name, context=s.context, parent=s.parent, resource=resource(s.resource),
                    attributes=attributes,
                    events=[_scrub_event(e) for e in s.events],
                    links=[Link(link.context, _scrub(link.attributes)) for link in s.links],
                    kind=s.kind, status=_scrub_status(s.status, attributes), start_time=s.start_time,
                    end_time=s.end_time, instrumentation_scope=s.instrumentation_scope,
                ))
            return self.wrapped.export(scrubbed)

        def shutdown(self) -> None:
            self.wrapped.shutdown()

        def force_flush(self, timeout_millis: int = 30000) -> bool:
            return self.wrapped.force_flush(timeout_millis)

    return RedactingSpanExporter(inner)


_HEADER_NAME = re.compile(r"[A-Za-z0-9!#$%&'*+.^_`|~-]+")


def parse_otlp_headers(raw: str | None) -> dict[str, str]:
    """Parse ``OTEL_EXPORTER_OTLP_*HEADERS`` (``k=v,k2=v2``, values URL-decoded).

    Malformed entries are skipped and only their count is logged; values are
    secrets (backend auth) and never appear in a log line.
    """

    headers: dict[str, str] = {}
    malformed = 0
    for entry in (raw or "").split(","):
        if not entry.strip():
            continue
        name, sep, value = entry.partition("=")
        name = unquote(name).strip()
        if not sep or not _HEADER_NAME.fullmatch(name) or not value.strip():
            malformed += 1
            continue
        headers[name.lower()] = unquote(value).strip()
    if malformed:
        _log.warning("ignored malformed OTLP header entries", extra={
            "malformed_entries": malformed, "header_names": ",".join(sorted(headers))})
    return headers


class _HideOtelHeaderValues(logging.Filter):
    """``opentelemetry.util.re`` logs malformed header strings verbatim; hide them."""

    def filter(self, record: logging.LogRecord) -> bool:
        record.msg, record.args = "malformed OTLP header entry ignored (value hidden)", ()
        return True


_HEADER_LOG_FILTER = _HideOtelHeaderValues()


def _otlp_kwargs(env: Mapping[str, str]) -> dict[str, Any]:
    """Endpoint/headers from ``env`` (the SDK itself would only read ``os.environ``)."""

    kwargs: dict[str, Any] = {}
    endpoint = env.get("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
    if not endpoint and env.get("OTEL_EXPORTER_OTLP_ENDPOINT"):
        endpoint = env["OTEL_EXPORTER_OTLP_ENDPOINT"].rstrip("/") + "/v1/traces"
    if endpoint:
        kwargs["endpoint"] = endpoint
    raw = env.get("OTEL_EXPORTER_OTLP_TRACES_HEADERS") or env.get("OTEL_EXPORTER_OTLP_HEADERS")
    if raw:
        kwargs["headers"] = parse_otlp_headers(raw)
    return kwargs


def configure_tracing(environ: Mapping[str, str] | None = None, *, exporter: Any = None) -> bool:
    """Enable tracing when ``SDF_OTEL_ENABLED`` is truthy; return whether it is on.

    Idempotent.  ``exporter`` (tests) replaces the env-selected exporter and is
    flushed synchronously.  A missing ``sdf-core[otel]`` install logs one warning
    and leaves tracing off.
    """

    env = os.environ if environ is None else environ
    if _state.enabled:
        return True
    if not _truthy(env.get("SDF_OTEL_ENABLED")) or _truthy(env.get("OTEL_SDK_DISABLED")):
        return False
    kind = (env.get("SDF_OTEL_EXPORTER") or env.get("OTEL_TRACES_EXPORTER") or "otlp").strip().lower()
    if kind == "none" and exporter is None:
        return False
    protocol = (env.get("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL") or env.get("OTEL_EXPORTER_OTLP_PROTOCOL") or "").lower()
    if exporter is None and protocol.startswith("grpc"):
        _log.warning("OTLP/gRPC is not supported; use http/protobuf (port 4318). Tracing stays off")
        return False
    try:
        from opentelemetry.sdk.resources import SERVICE_NAME, Resource
        from opentelemetry.sdk.trace import TracerProvider
        from opentelemetry.sdk.trace.export import BatchSpanProcessor, ConsoleSpanExporter, SimpleSpanProcessor
    except ImportError:
        _log.warning("SDF_OTEL_ENABLED is set but OpenTelemetry is not installed; install sdf-core[otel]")
        return False

    # TracerProvider reads OTEL_TRACES_SAMPLER(_ARG) from the process environment.
    resource = Resource.create({SERVICE_NAME: env.get("OTEL_SERVICE_NAME") or DEFAULT_SERVICE_NAME})
    provider = TracerProvider(resource=resource)
    label = kind
    if exporter is not None:
        label = "custom"
        processor = SimpleSpanProcessor(_redacting_exporter(exporter))
    elif kind == "console":
        # The SDK's default ``out`` is bound to sys.stdout at import time.
        processor = SimpleSpanProcessor(_redacting_exporter(ConsoleSpanExporter(out=sys.stdout)))
    elif kind in {"otlp", "otlp_proto_http", ""}:
        try:
            from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
        except ImportError:
            _log.warning("OTLP exporter is not installed; install sdf-core[otel]")
            return False
        # Endpoint and headers come from ``env``; timeout/compression from os.environ.
        logging.getLogger("opentelemetry.util.re").addFilter(_HEADER_LOG_FILTER)
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
    # An empty capture list would fall back to OTEL_INSTRUMENTATION_HTTP_CAPTURE_HEADERS_*,
    # so pass a header nobody sends and sanitise everything; the exporter also drops
    # http.*.header.* attributes.
    FastAPIInstrumentor.instrument_app(
        app, tracer_provider=_state.provider, excluded_urls="healthz",
        http_capture_headers_server_request=[_NO_HEADER], http_capture_headers_server_response=[_NO_HEADER],
        http_capture_headers_sanitize_fields=[".*"], exclude_spans=["receive", "send"],
    )
    app.middleware_stack = None  # rebuilt on the next request, now with the tracing middleware
    if app not in _state.fastapi_apps:
        _state.fastapi_apps.append(app)
    return True


_NO_HEADER = "x-sdf-no-header-capture"


def uninstrument_fastapi(app: Any) -> None:
    """Undo :func:`instrument_fastapi` (lifespan exit); safe to call twice."""

    _state.fastapi_apps[:] = [a for a in _state.fastapi_apps if a is not app]
    if not getattr(app, "_is_instrumented_by_opentelemetry", False):
        return
    from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor

    FastAPIInstrumentor.uninstrument_app(app)
    app.middleware_stack = None


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
    """Uninstrument, flush and drop the provider (lifespan exit, tests)."""

    provider = _state.provider
    _state.enabled = False
    _state.provider = _state.tracer = _state.exporter = None
    for app in list(_state.fastapi_apps):
        uninstrument_fastapi(app)
    if _state.db_instrumented:
        from opentelemetry.instrumentation.sqlalchemy import SQLAlchemyInstrumentor

        SQLAlchemyInstrumentor().uninstrument()
        _state.db_instrumented = False
    if provider is not None:
        provider.shutdown()
