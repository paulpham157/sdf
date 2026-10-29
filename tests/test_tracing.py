"""ADR 0024: vendor-neutral OpenTelemetry tracing (in-memory exporter, no network)."""

from __future__ import annotations

import io
import json
import logging
import subprocess
import sys
import threading
import types
import uuid

import pytest
from fastapi import FastAPI, HTTPException
from fastapi.testclient import TestClient
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter

from sdf_core import observability as obs
from sdf_core import tracing
from sdf_core.api import SessionLocal, app
from sdf_core.db import AgentRunRow, AttemptRow
from sdf_core.timeline import build_timeline, format_timeline
from tests.auth_support import admin_client
from tests.check_support import write_checks

SECRET = "sk-tracing-secret-value-0123456789"
PROMPT = "PROMPT-TEXT-must-never-be-a-span-attribute"


@pytest.fixture()
def spans(monkeypatch):
    monkeypatch.setenv("SDF_TEST_TRACING_API_KEY", SECRET)  # env-value redaction
    exporter = InMemorySpanExporter()
    assert tracing.configure_tracing({"SDF_OTEL_ENABLED": "1"}, exporter=exporter)
    try:
        yield exporter
    finally:
        tracing.shutdown()


def _by_name(exporter):
    out: dict[str, list] = {}
    for s in exporter.get_finished_spans():
        out.setdefault(s.name, []).append(s)
    return out


def _all_strings(exporter) -> str:
    chunks = []
    for s in exporter.get_finished_spans():
        chunks.append(s.name)
        chunks.append(json.dumps(dict(s.attributes or {}), default=str))
        chunks.append(str(s.status.description))
        for e in s.events:
            chunks.append(json.dumps(dict(e.attributes or {}), default=str))
        for link in s.links:
            chunks.append(json.dumps(dict(link.attributes or {}), default=str))
        chunks.append(json.dumps(dict(s.resource.attributes), default=str))
    return "\n".join(chunks)


# --- off by default: zero overhead ------------------------------------------------


def test_disabled_by_default_creates_no_provider_or_exporter(monkeypatch):
    import opentelemetry.exporter.otlp.proto.http.trace_exporter as otlp
    import opentelemetry.sdk.trace as sdk_trace
    import opentelemetry.sdk.trace.export as sdk_export

    def boom(*a, **k):
        raise AssertionError("tracing is off: nothing may be constructed")

    monkeypatch.setattr(sdk_trace.TracerProvider, "__init__", boom)
    monkeypatch.setattr(otlp.OTLPSpanExporter, "__init__", boom)
    monkeypatch.setattr(sdk_export.ConsoleSpanExporter, "__init__", boom)
    for env in ({}, {"SDF_OTEL_ENABLED": "0"}, {"SDF_OTEL_ENABLED": "", "OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:4318"}):
        assert tracing.configure_tracing(env) is False
    assert not tracing.enabled() and tracing._state.provider is None and tracing._state.exporter is None
    with tracing.span("sdf.task.run", task_id="T") as current:
        assert current is tracing.NOOP_SPAN
        tracing.set_attributes(current, attempt_id="A")
        tracing.mark_error("timeout")
    assert tracing.current_ids() is None and tracing.current_trace_id() is None
    assert tracing.instrument_fastapi(app) is False


def test_disabled_mode_never_imports_the_sdk():
    code = ("import sys, sdf_core.api, sdf_core.coordinator, sdf_core.run_adapter\n"
            "from sdf_core import tracing\n"
            "assert tracing.configure_tracing({}) is False\n"
            "with tracing.span('x', task_id='t'): pass\n"
            "print(sorted(m for m in sys.modules if m.startswith('opentelemetry')))\n")
    out = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, check=True,
                         env={"PATH": "/usr/bin:/bin", "SDF_AUTH_DEV_INSECURE": "1",
                              "SDF_DATABASE_URL": "sqlite+pysqlite:///:memory:"}).stdout
    assert out.strip() == "[]"


@pytest.fixture()
def tracing_off():
    """For tests that configure tracing themselves: always shut it down."""
    assert not tracing.enabled()
    try:
        yield
    finally:
        tracing.shutdown()


def test_exporter_selection(monkeypatch, tracing_off):
    assert tracing.configure_tracing({"SDF_OTEL_ENABLED": "1", "SDF_OTEL_EXPORTER": "console"})
    assert type(tracing._state.provider).__name__ == "TracerProvider"
    tracing.shutdown()
    assert tracing.configure_tracing({"SDF_OTEL_ENABLED": "true", "OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:9"})
    assert type(tracing._state.exporter).__name__ == "OTLPSpanExporter"
    assert tracing._state.exporter._endpoint == "http://127.0.0.1:9/v1/traces"
    tracing.shutdown()
    for unsupported in ({"SDF_OTEL_EXPORTER": "zipkin"}, {"OTEL_TRACES_EXPORTER": "none"},
                        {"OTEL_SDK_DISABLED": "true"}, {"OTEL_EXPORTER_OTLP_PROTOCOL": "grpc"}):
        assert tracing.configure_tracing({"SDF_OTEL_ENABLED": "1", **unsupported}) is False, unsupported
    monkeypatch.setenv("OTEL_SERVICE_NAME", "ignored-when-environ-given")
    exporter = InMemorySpanExporter()
    tracing.configure_tracing({"SDF_OTEL_ENABLED": "1", "OTEL_SERVICE_NAME": "sdf-test"}, exporter=exporter)
    with tracing.span("x"):
        pass
    tracing.shutdown()
    assert exporter.get_finished_spans()[0].resource.attributes["service.name"] == "sdf-test"


def test_missing_sdk_warns_and_stays_off(monkeypatch, tracing_off, caplog):
    monkeypatch.setitem(sys.modules, "opentelemetry.sdk.trace", None)  # import -> ImportError
    # configure_logging() sets propagate=False on "sdf" and alembic's fileConfig disables
    # existing loggers, so re-enable this one and listen on it directly.
    logger = logging.getLogger("sdf.tracing")
    monkeypatch.setattr(logger, "disabled", False)
    logger.addHandler(caplog.handler)
    try:
        with caplog.at_level("WARNING", logger="sdf.tracing"):
            assert tracing.configure_tracing({"SDF_OTEL_ENABLED": "1"}) is False
    finally:
        logger.removeHandler(caplog.handler)
    assert not tracing.enabled() and tracing._state.provider is None
    assert "sdf-core[otel]" in caplog.text
    with tracing.span("x") as current:
        assert current is tracing.NOOP_SPAN


# --- attributes and redaction -------------------------------------------------------


def test_attributes_are_allow_listed_and_redacted(spans):
    with tracing.span("sdf.agent.turn", attempt_id="A1", sandbox__id=f"Bearer {SECRET}", model="glm",
                      prompt=PROMPT, instructions=PROMPT, tokens__input=10, cost_usd=0.01, ok=True) as s:
        tracing.set_attributes(s, output=PROMPT, error_class="timeout", turn=2)
    (span,) = spans.get_finished_spans()
    attrs = dict(span.attributes)
    assert attrs["sdf.attempt_id"] == "A1" and attrs["sdf.model"] == "glm" and attrs["sdf.turn"] == 2
    assert attrs["sdf.tokens.input"] == 10 and attrs["sdf.cost_usd"] == 0.01 and attrs["sdf.ok"] is True
    assert "prompt" not in str(attrs) and "instructions" not in str(attrs) and "output" not in str(attrs)
    assert SECRET not in _all_strings(spans) and PROMPT not in _all_strings(spans)


def test_exceptions_record_class_only(spans):
    with pytest.raises(RuntimeError):
        with tracing.span("sdf.agent.turn"):
            raise RuntimeError(f"agent echoed {PROMPT} and {SECRET}")
    (span,) = spans.get_finished_spans()
    assert span.status.status_code.name == "ERROR" and span.status.description == "RuntimeError"
    assert span.attributes["error.type"] == "RuntimeError" and not span.events
    assert PROMPT not in _all_strings(spans) and SECRET not in _all_strings(spans)


def test_exporter_redacts_instrumentation_attributes(spans):
    # Attributes set by third-party instrumentation bypass the allow-list; the exporter still redacts.
    with tracing._state.tracer.start_as_current_span("raw") as raw:
        raw.set_attribute("http.target", f"/tasks?api_key={SECRET}")
        raw.add_event("e", {"msg": f"Bearer {SECRET}"})
    assert SECRET not in _all_strings(spans)


# --- correlation ---------------------------------------------------------------------


def test_logs_carry_trace_and_span_ids(spans):
    stream = io.StringIO()
    obs.configure_logging({"SDF_LOG_FORMAT": "json"}, stream=stream)
    log = obs.get_logger(f"tracing-test-{uuid.uuid4().hex[:6]}")
    # alembic's fileConfig (migration tests) may have disabled pre-existing loggers.
    for logger in (log, log.parent):
        logger.disabled = False
    try:
        with tracing.span("sdf.task.run", task_id="T"):
            trace_id, span_id = tracing.current_ids()
            log.info("inside")
        log.info("outside")
    finally:
        obs.configure_logging({"SDF_LOG_FORMAT": "text"}, stream=io.StringIO())
    inside, outside = (json.loads(line) for line in stream.getvalue().splitlines()[-2:])
    assert (inside["trace_id"], inside["span_id"]) == (trace_id, span_id) and len(trace_id) == 32
    assert "trace_id" not in outside


def test_context_crosses_threads(spans):
    seen = {}
    with tracing.span("sdf.run", run_id="R") as parent:
        worker = threading.Thread(target=tracing.in_current_context(lambda: seen.update(ids=tracing.current_ids())))
        worker.start()
        worker.join()
        assert seen["ids"][0] == format(parent.get_span_context().trace_id, "032x")


# --- hierarchy -------------------------------------------------------------------------


def test_http_task_attempt_hierarchy_and_trace_id_on_attempt(spans, tmp_path):
    assert tracing.instrument_fastapi(app)
    try:
        client = admin_client()
        fixture = tmp_path / "fixture"
        fixture.mkdir()
        (fixture / "app.py").write_text("print('old')\n")
        write_checks(fixture, {"prints-ok": ["python", "-c", "print('ok')"]})
        task_id = f"TASK-OTEL-{uuid.uuid4().hex[:8]}"
        client.post("/tasks", json={"id": task_id, "title": "otel", "idempotency_key": task_id,
                                    "acceptance_criteria": ["command succeeds"]})
        response = client.post(f"/tasks/{task_id}/run?api_key={SECRET}", json={
            "fixture_dir": str(fixture), "instructions": PROMPT,
            "criterion_checks": {"command succeeds": ["prints-ok"]}})
        assert response.status_code == 200, response.text
    finally:
        tracing.uninstrument_fastapi(app)
    body = response.json()
    names = _by_name(spans)
    task_span, attempt_span = names["sdf.task.run"][0], names["sdf.attempt"][0]
    server = next(s for s in spans.get_finished_spans()
                  if s.kind.name == "SERVER" and s.context.trace_id == task_span.context.trace_id)
    assert server.attributes.get("sdf.request_id")
    assert task_span.parent.span_id == server.context.span_id
    assert attempt_span.parent.span_id == task_span.context.span_id
    assert names["sdf.evaluate"][0].parent.span_id == attempt_span.context.span_id
    assert attempt_span.attributes["sdf.attempt_id"] == body["attempt_id"]
    assert task_span.attributes["sdf.task_id"] == task_id
    trace_id = format(task_span.context.trace_id, "032x")
    assert body["trace_id"] == trace_id == format(server.context.trace_id, "032x")
    with SessionLocal() as db:
        assert db.get(AttemptRow, body["attempt_id"]).trace_id == trace_id
        timeline = build_timeline(db, body["attempt_id"])
    assert timeline["trace_id"] == trace_id and f"trace_id={trace_id}" in format_timeline(timeline)
    blob = _all_strings(spans)
    assert SECRET not in blob and PROMPT not in blob


def test_live_adapter_spans_turn_and_cleanup(spans, monkeypatch, tmp_path):
    from tests.test_run_adapter import _adapter, _Completing, _Transport

    runtime = _Completing([{"input_tokens": 0, "output_tokens": 0}, {"input_tokens": 1000, "output_tokens": 200}])
    db, adapter = _adapter(monkeypatch, runtime, _Transport())
    with tracing.span("sdf.attempt", attempt_id="A") as attempt:
        adapter.run(attempt_id="A", workspace=tmp_path, instructions=PROMPT)
    names = _by_name(spans)
    run = names["sdf.agent.run"][0]
    assert run.parent.span_id == attempt.get_span_context().span_id
    for child in ("sdf.agent.start", "sdf.agent.turn", "sdf.sandbox.cleanup"):
        assert names[child][0].parent.span_id == run.context.span_id, child
    turn = names["sdf.agent.turn"][0].attributes
    assert turn["sdf.agent.kind"] == "opencode" and turn["sdf.model"] == adapter.model
    assert turn["sdf.tokens.input"] == 1000 and turn["sdf.tokens.output"] == 200 and turn["sdf.cost_usd"] > 0
    assert turn["sdf.sandbox.id"] == "sbx-1" and turn["sdf.turn"] == 1
    assert names["sdf.sandbox.cleanup"][0].attributes["sdf.sandbox.id"] == "sbx-1"
    assert run.attributes["sdf.sandbox.id"] == "sbx-1" and run.status.status_code.name == "UNSET"
    assert PROMPT not in _all_strings(spans)


def test_failed_turn_and_cleanup_carry_error_class(spans, monkeypatch, tmp_path):
    from tests.test_run_adapter import _adapter, _Completing, _Transport

    def kill(_sid):
        raise RuntimeError("api down")

    runtime = _Completing(fail=RuntimeError(f"opencode turn exceeded 420s and was killed {PROMPT}"))
    db, adapter = _adapter(monkeypatch, runtime, _Transport(RuntimeError("close boom")), kill)
    with pytest.raises(RuntimeError):
        adapter.run(attempt_id="A", workspace=tmp_path, instructions=PROMPT)
    names = _by_name(spans)
    turn, cleanup, run = names["sdf.agent.turn"][0], names["sdf.sandbox.cleanup"][0], names["sdf.agent.run"][0]
    assert turn.attributes["sdf.error_class"] == "timeout" and turn.status.status_code.name == "ERROR"
    assert cleanup.attributes["sdf.error_class"] == "cleanup_failed"
    assert run.attributes["sdf.error_class"] == "cleanup_failed"
    assert PROMPT not in _all_strings(spans) and "close boom" not in _all_strings(spans)


def test_e2b_sandbox_create_span():
    from tests.test_e2b_herdr_transport import FakeFactory, _transport

    exporter = InMemorySpanExporter()
    tracing.configure_tracing({"SDF_OTEL_ENABLED": "1"}, exporter=exporter)
    try:
        transport = _transport(FakeFactory())
        transport._ensure_sandbox()
        transport._ensure_sandbox()  # reuse: no second create
    finally:
        tracing.shutdown()
    creates = [s for s in exporter.get_finished_spans() if s.name == "sdf.sandbox.create"]
    assert len(creates) == 1
    assert creates[0].attributes["sdf.sandbox.provider"] == "e2b"
    assert creates[0].attributes["sdf.sandbox.id"] == transport.sandbox_id


def test_coordinator_run_hierarchy(spans, tmp_path):
    from sqlalchemy.orm import sessionmaker

    from sdf_core.db import Base, make_engine
    from tests.agent_comm_support import lead_behavior, make_fixture, worker_behavior
    from tests.test_agent_comm import FAST, _approved, _coordinator

    engine = make_engine("sqlite+pysqlite:///:memory:")
    Base.metadata.create_all(engine)
    db = sessionmaker(engine, expire_on_commit=False)()
    try:
        fixture = make_fixture(tmp_path)
        plan = _approved(db, fixture, "PLAN-OTEL")
        coordinator, running, _created = _coordinator(db, tmp_path, {
            "lead": lead_behavior, "worker": worker_behavior()})
        run = coordinator.execute(coordinator.create_run(plan.id, FAST).id)
        assert run.status == "succeeded", run.error
        names = _by_name(spans)
        root = names["sdf.run"][0]
        trace_id = format(root.context.trace_id, "032x")
        assert db.get(AgentRunRow, run.id).trace_id == trace_id
        assert root.attributes["sdf.outcome"] == "succeeded" and root.attributes["sdf.run_id"] == run.id
        for name in ("sdf.agent.start", "sdf.agent.turn", "sdf.verify", "sdf.sandbox.cleanup"):
            assert names[name], name
            assert all(s.context.trace_id == root.context.trace_id for s in names[name]), name
        assert all(s.parent.span_id == root.context.span_id for s in names["sdf.agent.turn"])
        assert {s.attributes["sdf.agent.role"] for s in names["sdf.agent.start"]} == {"lead", "worker"}
        attempts = db.query(AttemptRow).all()
        assert attempts and all(a.trace_id == trace_id for a in attempts)
        assert build_timeline(db, run.id)["trace_id"] == trace_id
        assert running == set()
    finally:
        db.close()
        engine.dispose()


def test_db_spans_are_opt_in_and_carry_no_values(spans):
    from sqlalchemy import create_engine, text

    engine = create_engine("sqlite+pysqlite:///:memory:")
    assert tracing.instrument_sqlalchemy(engine, {}) is False  # SDF_OTEL_DB unset
    assert tracing.instrument_sqlalchemy([engine, engine], {"SDF_OTEL_DB": "1"})
    with engine.begin() as conn:
        conn.execute(text("create table t (v text)"))
        conn.execute(text("insert into t (v) values (:v)"), {"v": PROMPT})
    statements = [s.attributes.get("db.statement", "") for s in spans.get_finished_spans()]
    assert any("insert into t" in st for st in statements)
    assert PROMPT not in _all_strings(spans)
    engine.dispose()


# --- review fixes (PR #63): exporter hardening ------------------------------------------

PII = "PII-alice@example.com"


def _toy_app():
    toy = FastAPI()

    @toy.get("/boom")
    def boom():
        raise RuntimeError(f"agent output {PROMPT} {PII}")

    @toy.get("/ok")
    def ok():
        return {}

    return toy


def test_http_exception_event_keeps_type_only(spans):
    toy = _toy_app()
    assert tracing.instrument_fastapi(toy)
    try:
        TestClient(toy, raise_server_exceptions=False).get("/boom")
    finally:
        tracing.uninstrument_fastapi(toy)
    server = next(s for s in spans.get_finished_spans() if s.kind.name == "SERVER")
    events = [e for e in server.events if e.name == "exception"]
    assert events and all(dict(e.attributes) == {"exception.type": "RuntimeError"} for e in events)
    blob = _all_strings(spans)
    assert PROMPT not in blob and PII not in blob and "Traceback" not in blob


def test_db_error_status_link_and_resource_are_scrubbed(monkeypatch):
    from sqlalchemy import create_engine, text

    exporter = InMemorySpanExporter()
    monkeypatch.setenv("SDF_TEST_TRACING_API_KEY", SECRET)
    assert tracing.configure_tracing({"SDF_OTEL_ENABLED": "1", "OTEL_SERVICE_NAME": f"sdf {SECRET}"}, exporter=exporter)
    engine = create_engine("sqlite+pysqlite:///:memory:")
    try:
        assert tracing.instrument_sqlalchemy(engine, {"SDF_OTEL_DB": "1"})
        with engine.begin() as conn:
            conn.execute(text("create table t (v text primary key)"))
            conn.execute(text("insert into t values (:v)"), {"v": PII})
        with pytest.raises(Exception):
            with engine.begin() as conn:
                conn.execute(text("insert into t values (:v)"), {"v": PII})
        from opentelemetry import trace

        with tracing._state.tracer.start_as_current_span("raw") as raw:
            ctx = raw.get_span_context()
        tracing._state.tracer.start_span("linked", links=[trace.Link(ctx, {"note": f"Bearer {SECRET}"})]).end()
    finally:
        tracing.shutdown()
        engine.dispose()
    failed = [s for s in exporter.get_finished_spans() if s.status.status_code.name == "ERROR"]
    assert failed and all(s.status.description in {"error", "IntegrityError"} for s in failed)
    blob = _all_strings(exporter)
    assert "UNIQUE" not in blob and PII not in blob and SECRET not in blob
    linked = next(s for s in exporter.get_finished_spans() if s.name == "linked")
    assert linked.links and SECRET not in str(dict(linked.links[0].attributes))
    assert "[REDACTED]" in linked.resource.attributes["service.name"]


def test_http_urls_lose_query_and_client_identity(spans, monkeypatch):
    # Header capture can be switched on by env: instrument_fastapi must not let it.
    monkeypatch.setenv("OTEL_INSTRUMENTATION_HTTP_CAPTURE_HEADERS_SERVER_REQUEST", ".*")
    monkeypatch.setenv("OTEL_INSTRUMENTATION_HTTP_CAPTURE_HEADERS_SERVER_RESPONSE", ".*")
    monkeypatch.setenv("OTEL_INSTRUMENTATION_HTTP_CAPTURE_HEADERS_SANITIZE_FIELDS", "")
    toy = _toy_app()
    assert tracing.instrument_fastapi(toy)
    try:
        TestClient(toy).get("/ok?key=KEYVAL123456&sig=SIGVAL123456#frag", headers={
            "User-Agent": "ua-HDRTEST", "X-Custom": "CUSTOMHDR123456", "Authorization": "Bearer abcdefghijkl"})
    finally:
        tracing.uninstrument_fastapi(toy)
    with tracing._state.tracer.start_as_current_span("raw") as raw:
        raw.set_attributes({"url.full": "https://user:pw123456@example.com/a?token=x", "client.address": "10.1.2.3",
                            "http.request.header.authorization": ("Bearer abcdefghijkl",)})
    finished = spans.get_finished_spans()
    assert [s.name for s in finished if s.kind.name == "SERVER"] == ["GET /ok"]
    assert not [s for s in finished if s.name.endswith((" http send", " http receive"))]
    server = next(s for s in finished if s.kind.name == "SERVER")
    assert server.attributes["http.url"] == "http://testserver/ok" and server.attributes["http.target"] == "/ok"
    raw_attrs = next(s for s in finished if s.name == "raw").attributes
    assert dict(raw_attrs) == {"url.full": "https://example.com/a"}
    blob = _all_strings(spans)
    for leaked in ("KEYVAL123456", "SIGVAL123456", "ua-HDRTEST", "CUSTOMHDR123456", "abcdefghijkl", "testclient",
                   "10.1.2.3", "pw123456", ".header."):
        assert leaked not in blob, leaked
    assert not {"net.peer.ip", "client.address", "http.user_agent", "user_agent.original"} & set(server.attributes)


def test_client_errors_do_not_mark_spans_failed(spans):
    with pytest.raises(HTTPException):
        with tracing.span("sdf.task.run", task_id="T4"):
            raise HTTPException(status_code=409, detail=PROMPT)
    with pytest.raises(HTTPException):
        with tracing.span("sdf.task.run", task_id="T5"):
            raise HTTPException(status_code=502, detail=PROMPT)
    client_error, server_error = spans.get_finished_spans()
    assert client_error.status.status_code.name == "UNSET" and client_error.attributes["error.type"] == "HTTPException"
    assert server_error.status.status_code.name == "ERROR" and server_error.status.description == "HTTPException"
    assert PROMPT not in _all_strings(spans)


# --- review fixes: OTLP headers never logged -------------------------------------------------


def test_otlp_header_values_are_never_logged(monkeypatch, tracing_off, caplog):
    from opentelemetry.util.re import parse_env_headers

    value = "HDRSECRETVALUE0123456789"
    raw = f"Authorization=Bearer%20{value},x-tenant=tenant-ABCDEFGH,malformed{value}"
    assert tracing.parse_otlp_headers(raw) == {"authorization": f"Bearer {value}", "x-tenant": "tenant-ABCDEFGH"}
    monkeypatch.setenv("OTEL_EXPORTER_OTLP_HEADERS", raw)
    import opentelemetry.exporter.otlp.proto.http.trace_exporter as otlp

    seen: dict = {}
    original_init = otlp.OTLPSpanExporter.__init__

    def recording_init(self, *args, **kwargs):
        seen.update(kwargs)
        original_init(self, *args, **kwargs)

    monkeypatch.setattr(otlp.OTLPSpanExporter, "__init__", recording_init)
    stream = io.StringIO()
    obs.configure_logging({"SDF_LOG_FORMAT": "json"}, stream=stream)
    logging.getLogger("sdf.tracing").disabled = False
    try:
        with caplog.at_level(logging.DEBUG):
            assert tracing.configure_tracing({"SDF_OTEL_ENABLED": "1", "OTEL_EXPORTER_OTLP_HEADERS": raw,
                                              "OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:9"})
            assert seen["headers"]["authorization"] == f"Bearer {value}"
            parse_env_headers(f"bad{value}", liberal=True)  # what the SDK does with os.environ
    finally:
        obs.configure_logging({"SDF_LOG_FORMAT": "text"}, stream=io.StringIO())
    logged = caplog.text + stream.getvalue() + "".join(r.getMessage() for r in caplog.records)
    assert "malformed" in logged.lower() and "authorization" in logged
    assert value not in logged and "tenant-ABCDEFGH" not in logged
    # Header values are secrets for redact() too, quoted or not.
    assert value not in obs.redact(f"x {value} y") and "tenant-ABCDEFGH" not in obs.redact("tenant-ABCDEFGH")
    assert obs._SECRET_NAME.search("OTEL_EXPORTER_OTLP_TRACES_HEADERS")


# --- review fixes: lifespan owns instrumentation, reads .env, re-enters cleanly -----------


def test_lifespan_reads_dotenv_and_reenters(monkeypatch, tmp_path, caplog):
    import opentelemetry.sdk.trace.export as sdk_export
    from sdf_core import credentials

    for name in ("SDF_OTEL_ENABLED", "SDF_OTEL_EXPORTER", "OTEL_TRACES_EXPORTER", "SDF_OTEL_DB"):
        monkeypatch.delenv(name, raising=False)
    (tmp_path / ".env").write_text("SDF_OTEL_ENABLED=1\nSDF_OTEL_EXPORTER=console\nSDF_OTEL_DB=1\n")
    monkeypatch.setattr(credentials, "REPO_ROOT", tmp_path)
    exporters: list[InMemorySpanExporter] = []

    def console(**_kw):
        exporters.append(InMemorySpanExporter())
        return exporters[-1]

    monkeypatch.setattr(sdk_export, "ConsoleSpanExporter", console)
    assert not tracing.enabled()
    with caplog.at_level(logging.WARNING):
        for _ in range(2):
            with admin_client() as client:
                assert tracing.enabled() and app._is_instrumented_by_opentelemetry
                assert client.get("/scorecard").status_code == 200
            assert not tracing.enabled() and not getattr(app, "_is_instrumented_by_opentelemetry", False)
            assert not tracing._state.db_instrumented and not tracing._state.fastapi_apps
    assert len(exporters) == 2
    for exporter in exporters:
        finished = exporter.get_finished_spans()
        assert [s for s in finished if s.kind.name == "SERVER"], "server span in every lifespan"
        assert [s for s in finished if s.attributes.get("db.system")], "db span in every lifespan"
    assert "already shutdown" not in caplog.text


# --- review fixes: coordinator and background runs ------------------------------------------


def test_trace_id_not_written_for_a_run_that_is_not_running(spans, tmp_path):
    from sqlalchemy.orm import sessionmaker

    from sdf_core.db import Base, make_engine
    from sdf_core.plans import PlanGateError
    from tests.agent_comm_support import lead_behavior, make_fixture, worker_behavior
    from tests.test_agent_comm import FAST, _approved, _coordinator

    engine = make_engine("sqlite+pysqlite:///:memory:")
    Base.metadata.create_all(engine)
    db = sessionmaker(engine, expire_on_commit=False)()
    try:
        plan = _approved(db, make_fixture(tmp_path), "PLAN-OTEL-GATE")
        coordinator, _running, _created = _coordinator(db, tmp_path, {
            "lead": lead_behavior, "worker": worker_behavior()})
        run = coordinator.create_run(plan.id, FAST)
        run.status, run.trace_id = "succeeded", "a" * 32
        db.commit()
        with pytest.raises(PlanGateError):
            coordinator.execute(run.id)
        db.expire_all()
        assert db.get(AgentRunRow, run.id).trace_id == "a" * 32
    finally:
        db.close()
        engine.dispose()


def test_background_run_gets_its_own_linked_root_trace(spans, tmp_path, monkeypatch):
    from tests.agent_comm_support import fake_factory, lead_behavior, make_fixture, plan_dict, worker_behavior
    from tests.auth_support import client_as
    from tests.test_agent_comm_api import FAST

    import sdf_core.api as api_module

    deferred: list[threading.Thread] = []

    class DeferredThread(threading.Thread):
        def start(self):
            if self not in deferred:
                deferred.append(self)
            else:
                super().start()

    monkeypatch.setattr(api_module, "_threading", types.SimpleNamespace(Thread=DeferredThread))
    monkeypatch.chdir(tmp_path)
    running: set[str] = set()
    app.state.agent_sandbox_factory = fake_factory(tmp_path / "boxes", running, {
        "lead": lead_behavior, "worker": worker_behavior()}, [])
    app.state.agent_cleanup_check = lambda ids: ids & running
    operator, approver = client_as("operator", principal="planner"), client_as("approver", principal="hung")
    assert tracing.instrument_fastapi(app)
    try:
        plan_id = f"PLAN-OTEL-BG-{uuid.uuid4().hex[:8]}"
        body = operator.post("/plans", json=plan_dict(make_fixture(tmp_path), plan_id)).json()
        assert approver.post(f"/plans/{plan_id}/approve", json={
            "expected_sha256": body["content_sha256"]}).status_code == 200
        started = operator.post(f"/plans/{plan_id}/runs", json={"config": FAST, "wait": False})
        assert started.status_code == 202, started.text
        run_id, response_trace = started.json()["id"], started.json()["trace_id"]
        # The test DB is one shared in-memory SQLite connection (StaticPool), so the run
        # thread must not overlap the request's session: start it after the 202, then join.
        (worker,) = deferred
        worker.start()
        worker.join(timeout=60)
        assert not worker.is_alive()
        stored = operator.get(f"/runs/{run_id}").json()
    finally:
        tracing.uninstrument_fastapi(app)
        app.state.agent_sandbox_factory = None
        app.state.agent_cleanup_check = None
    assert stored["status"] == "succeeded", stored["error"]
    names = _by_name(spans)
    (root,) = names["sdf.run.background"]
    run_span = names["sdf.run"][0]
    request = next(s for s in spans.get_finished_spans()
                   if s.kind.name == "SERVER" and s.name == "POST /plans/{plan_id}/runs")
    assert root.parent is None and root.attributes["sdf.run_id"] == run_id
    assert root.context.trace_id != request.context.trace_id
    assert [link.context.span_id for link in root.links] == [request.context.span_id]
    assert run_span.parent.span_id == root.context.span_id
    root_trace = format(root.context.trace_id, "032x")
    assert response_trace == stored["trace_id"] == root_trace
