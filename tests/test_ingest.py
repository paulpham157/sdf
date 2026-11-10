"""Ingest sample OTLP payloads through the local receiver.

The JSON files in tests/samples are hand-written. They are not captures from
Codex or Claude Code. Each resource is marked sdf.fixture=sample.
"""

from __future__ import annotations

import json
import socket
import threading
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

import pytest
from opentelemetry.proto.collector.logs.v1.logs_service_pb2 import (
    ExportLogsServiceRequest,
)
from opentelemetry.proto.collector.metrics.v1.metrics_service_pb2 import (
    ExportMetricsServiceRequest,
)
from opentelemetry.proto.collector.trace.v1.trace_service_pb2 import (
    ExportTraceServiceRequest,
)
from opentelemetry.proto.common.v1.common_pb2 import AnyValue, KeyValue
from opentelemetry.proto.logs.v1.logs_pb2 import LogRecord, ResourceLogs, ScopeLogs
from opentelemetry.proto.metrics.v1.metrics_pb2 import (
    AggregationTemporality,
    Metric,
    NumberDataPoint,
    ResourceMetrics,
    ScopeMetrics,
    Sum,
)
from opentelemetry.proto.resource.v1.resource_pb2 import Resource
from opentelemetry.proto.trace.v1.trace_pb2 import ResourceSpans, ScopeSpans, Span

from sdf.cli import main
from sdf.server import make_server
from sdf.store import Store
from sdf.summary import summarize

SAMPLES = Path(__file__).parent / "samples"
POSTS = [
    ("/v1/metrics", "claude_metrics.json"),
    ("/v1/logs", "claude_logs.json"),
    ("/v1/traces", "claude_traces.json"),
    ("/v1/metrics", "codex_metrics.json"),
    ("/v1/logs", "codex_logs.json"),
]


@pytest.fixture
def receiver(tmp_path):
    store = Store(str(tmp_path / "telemetry.sqlite"))
    server = make_server("127.0.0.1", 0, store, quiet=True)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    base = f"http://127.0.0.1:{server.server_address[1]}"
    try:
        yield base, store
    finally:
        server.shutdown()
        server.server_close()
        store.close()


def _post(base: str, path: str, body: bytes, content_type: str) -> bytes:
    request = Request(base + path, data=body, method="POST")
    request.add_header("Content-Type", content_type)
    with urlopen(request) as response:
        assert response.status == 200
        return response.read()


def test_sample_files_are_marked_as_sample_data():
    readme = (SAMPLES / "README.md").read_text()
    assert "hand-written sample" in readme
    for name in [item[1] for item in POSTS]:
        payload = json.loads((SAMPLES / name).read_text())
        blobs = (
            payload.get("resourceMetrics")
            or payload.get("resourceLogs")
            or payload.get("resourceSpans")
        )
        assert blobs, name
        attributes = blobs[0]["resource"]["attributes"]
        fixture = {
            item["key"]: item["value"]["stringValue"]
            for item in attributes
            if "stringValue" in item["value"]
        }
        assert fixture["sdf.fixture"] == "sample"


def test_receiver_stores_sample_payloads_and_summary_uses_only_present_fields(receiver):
    base, store = receiver
    health = urlopen(base + "/health")
    assert health.status == 200
    assert json.loads(health.read()) == {"status": "ok"}

    for path, name in POSTS:
        body = (SAMPLES / name).read_bytes()
        assert _post(base, path, body, "application/json") == b"{}"

    rows = store.load()
    assert rows
    assert {row.resource.get("sdf.fixture") for row in rows} == {"sample"}
    # The unscoped Codex counter is stored, but it is not a session.
    assert any(row.name == "codex.websocket.event" and row.session_id is None for row in rows)

    report = summarize(rows)
    sessions = {item.session_id: item for item in report.sessions}
    assert set(sessions) == {"sample-claude-1", "sample-codex-conv-1"}
    assert report.unattributed == []

    claude = sessions["sample-claude-1"]
    assert claude.tool == "claude-code"
    assert claude.service_names == ["claude-code"]
    assert claude.models == ["claude-sonnet-5"]
    assert claude.duration_ms == 2000
    assert claude.duration_source == "traces claude_code.interaction"
    assert claude.tokens == {"input": 100, "output": 40, "cacheRead": 10, "cacheCreation": 4}
    assert claude.tokens_source == "metrics claude_code.token.usage"
    assert claude.cost_usd == 0.5
    assert claude.cost_source == "metrics claude_code.cost.usage"
    assert claude.traces and claude.logs and claude.metrics
    assert [(call.name, call.call_id, call.duration_ms) for call in claude.tool_calls] == [
        ("Bash", "toolu_bash", 250),
        ("Read", "toolu_read", 100),
    ]
    assert "logs claude_code.tool_result" in claude.tool_calls[0].source
    assert any(node.name == "claude_code.interaction" for node in claude.spans)

    codex = sessions["sample-codex-conv-1"]
    assert codex.tool == "codex"
    assert codex.models == ["gpt-5.1"]
    assert codex.duration_ms == 2500
    assert codex.duration_source == "metrics codex.turn.e2e_duration_ms"
    assert codex.tokens == {"input": 200, "output": 30}
    assert codex.tokens_source == "metrics codex.turn.token_usage"
    assert codex.cost_usd == pytest.approx(0.005)
    assert codex.cost_source == "metrics codex.turn.cost_microusd"
    assert [(call.name, call.call_id, call.success, call.duration_ms) for call in codex.tool_calls] == [
        ("shell", "call-1", "true", 90)
    ]


def test_protobuf_cumulative_counter_keeps_the_latest_point(receiver, tmp_path, capsys):
    base, store = receiver
    _post(base, "/v1/metrics", _counter(10, "1000"), "application/x-protobuf")
    assert _post(base, "/v1/metrics", _counter(25, "2000"), "application/x-protobuf") == b""
    sessions = summarize(store.load()).sessions
    assert len(sessions) == 1
    assert sessions[0].session_id == "proto-session"
    assert sessions[0].tokens == {"input": 25}

    db = tmp_path / "telemetry.sqlite"
    assert main(["summary", "--db", str(db), "--session", "proto-session", "--json"]) == 0
    printed = json.loads(capsys.readouterr().out)
    assert printed["sessions"][0]["session_id"] == "proto-session"
    assert printed["sessions"][0]["cost_usd"] is None


def test_protobuf_span_ids_are_stored_as_hex(receiver):
    base, store = receiver
    span = Span(
        trace_id=bytes.fromhex("ab" * 16),
        span_id=bytes.fromhex("cd" * 8),
        parent_span_id=bytes.fromhex("ef" * 8),
        name="claude_code.interaction",
        start_time_unix_nano=1_000_000_000,
        end_time_unix_nano=2_000_000_000,
        attributes=[
            KeyValue(key="session.id", value=AnyValue(string_value="span-session")),
            KeyValue(key="sdf.fixture", value=AnyValue(string_value="sample")),
        ],
    )
    payload = ExportTraceServiceRequest(
        resource_spans=[
            ResourceSpans(
                resource=Resource(
                    attributes=[
                        KeyValue(key="service.name", value=AnyValue(string_value="claude-code")),
                        KeyValue(key="sdf.fixture", value=AnyValue(string_value="sample")),
                    ]
                ),
                scope_spans=[ScopeSpans(spans=[span])],
            )
        ]
    )
    assert _post(base, "/v1/traces", payload.SerializeToString(), "application/x-protobuf") == b""
    stored = store.load()
    assert stored[0].trace_id == "ab" * 16
    assert stored[0].span_id == "cd" * 8
    assert stored[0].parent_span_id == "ef" * 8


def test_malformed_payload_is_rejected(receiver):
    base, store = receiver
    request = Request(base + "/v1/logs", data=b"not-otlp", method="POST")
    request.add_header("Content-Type", "application/json")
    with pytest.raises(HTTPError) as caught:
        urlopen(request)
    assert caught.value.code == 400
    assert store.load() == []


def test_chunked_bodies_are_stored_for_json_and_protobuf(receiver, tmp_path, capsys):
    base, store = receiver
    posts = [
        ("/v1/traces", "opencode_traces.json", "application/json"),
        ("/v1/logs", "opencode_logs.json", "application/json"),
        ("/v1/traces", "pi_traces.json", "application/json"),
        ("/v1/logs", "pi_logs.json", "application/json"),
        ("/v1/metrics", "pi_metrics.json", "application/json"),
    ]
    for path, name, content_type in posts:
        status, body = _post_chunked(base, path, (SAMPLES / name).read_bytes(), content_type)
        assert status == 200
        assert body == b"{}"

    # A stated Content-Length of 0 must not hide a chunked body.
    status, body = _post_chunked(
        base,
        "/v1/traces",
        b'{"resourceSpans":[]}',
        "application/json",
        extra_headers="Content-Length: 0\r\n",
    )
    assert status == 200
    assert body == b"{}"

    rows = store.load()
    opencode = [row for row in rows if row.service_name == "opencode" and row.signal == "traces"]
    assert {row.session_id for row in opencode} == {"ses_sampleopencode0001", None}
    pi_logs = [row for row in rows if row.name == "pi.session.start"]
    assert len(pi_logs) == 1
    assert pi_logs[0].session_id == "2026-01-01T00-00-00-000Z_sample-pi-session"
    assert "session.id" not in pi_logs[0].attributes
    pi_metrics = [row for row in rows if row.name == "gen_ai.client.token.usage"]
    assert pi_metrics
    assert all(row.session_id is None for row in pi_metrics)

    report = summarize(rows)
    sessions = {item.session_id: item for item in report.sessions}
    assert sessions["ses_sampleopencode0001"].tool == "opencode"
    assert sessions["ses_sampleopencode0001"].tokens["input_tokens"] == 5822
    assert sessions["ses_sampleopencode0001"].tokens["output_tokens"] == 306
    assert sessions["ses_sampleopencode0001"].logs == 0
    pi = sessions["2026-01-01T00-00-00-000Z_sample-pi-session"]
    assert pi.tool == "pi"
    assert pi.tokens == {"input_tokens": 1245, "output_tokens": 832}
    assert pi.metrics == 6
    assert pi.logs == 2

    db = tmp_path / "telemetry.sqlite"
    assert main(["summary", "--db", str(db), "--json", "--session", pi.session_id]) == 0
    printed = json.loads(capsys.readouterr().out)
    calls = printed["sessions"][0]["tool_calls"]
    assert [call["name"] for call in calls] == ["bash", "read", "bash"]
    assert calls[0]["success"] == "true"
    assert calls[0]["call_id"] == "call_sample_bash_1"

    assert _post_chunked(base, "/v1/traces", _chunk_trace(), "application/x-protobuf") == (200, b"")
    assert _post_chunked(base, "/v1/metrics", _counter(4, "3000"), "application/x-protobuf") == (200, b"")
    assert _post_chunked(base, "/v1/logs", _pi_log(), "application/x-protobuf") == (200, b"")
    assert any(row.service_name == "pi" and row.name == "chat sample-model" for row in store.load())


def test_bad_chunked_bodies_are_rejected(receiver):
    base, store = receiver
    status, body = _post_chunked(base, "/v1/traces", b"zzzz\r\n", "application/json", frame=False)
    assert status == 400
    assert body == b"invalid chunk size"
    status, body = _post_chunked(
        base,
        "/v1/metrics",
        b"7a1201\r\n",
        "application/json",
        frame=False,
    )
    assert status == 413
    assert body == b"payload too large"
    status, body = _post_chunked(
        base,
        "/v1/logs",
        b"{}",
        "application/json",
        transfer="gzip, chunked",
        frame=False,
    )
    assert status == 400
    assert body == b"unsupported transfer encoding"
    assert store.load() == []


def _post_chunked(base, path, payload, content_type, *, extra_headers="", transfer="chunked", frame=True):
    host, port = base.removeprefix("http://").split(":")
    framed = _encode_chunked(payload) if frame else payload
    request = (
        f"POST {path} HTTP/1.1\r\n"
        f"Host: {host}:{port}\r\n"
        f"Content-Type: {content_type}\r\n"
        f"Transfer-Encoding: {transfer}\r\n"
        f"{extra_headers}"
        f"Connection: close\r\n"
        f"\r\n"
    ).encode() + framed
    with socket.create_connection((host, int(port)), timeout=5) as sock:
        sock.sendall(request)
        data = b""
        while True:
            chunk = sock.recv(8192)
            if not chunk:
                break
            data += chunk
    header, _, rest = data.partition(b"\r\n\r\n")
    status = int(header.split(b"\r\n", 1)[0].split()[1])
    length = None
    for line in header.split(b"\r\n")[1:]:
        if line.lower().startswith(b"content-length:"):
            length = int(line.split(b":", 1)[1].strip())
    body = rest if length is None else rest[:length]
    return status, body


def _encode_chunked(payload: bytes) -> bytes:
    """Small chunks, with an extension and leading zeros, then a trailer."""
    out = bytearray()
    step = 9
    first = True
    for offset in range(0, len(payload), step):
        part = payload[offset : offset + step]
        size = f"{len(part):04x}" if first else f"{len(part):x}"
        extension = ";sample=1" if first else ""
        first = False
        out += f"{size}{extension}\r\n".encode() + part + b"\r\n"
    out += b"0\r\nfixture: sample\r\n\r\n"
    return bytes(out)


def _chunk_trace() -> bytes:
    span = Span(
        trace_id=bytes.fromhex("12" * 16),
        span_id=bytes.fromhex("34" * 8),
        name="chat sample-model",
        start_time_unix_nano=10,
        end_time_unix_nano=20,
        attributes=[
            KeyValue(key="session.id", value=AnyValue(string_value="proto-chunk-session")),
            KeyValue(key="gen_ai.request.model", value=AnyValue(string_value="sample-model")),
            KeyValue(key="sdf.fixture", value=AnyValue(string_value="sample")),
        ],
    )
    payload = ExportTraceServiceRequest(
        resource_spans=[
            ResourceSpans(
                resource=Resource(
                    attributes=[
                        KeyValue(key="service.name", value=AnyValue(string_value="pi")),
                        KeyValue(key="sdf.fixture", value=AnyValue(string_value="sample")),
                    ]
                ),
                scope_spans=[ScopeSpans(spans=[span])],
            )
        ]
    )
    return payload.SerializeToString()


def _pi_log() -> bytes:
    record = LogRecord(
        time_unix_nano=10,
        body=AnyValue(string_value="pi session proto-chunk-session started"),
        attributes=[
            KeyValue(key="event.name", value=AnyValue(string_value="pi.session.start")),
            KeyValue(key="pi.session.id", value=AnyValue(string_value="proto-chunk-session")),
            KeyValue(key="gen_ai.system", value=AnyValue(string_value="pi")),
            KeyValue(key="sdf.fixture", value=AnyValue(string_value="sample")),
        ],
    )
    payload = ExportLogsServiceRequest(
        resource_logs=[
            ResourceLogs(
                resource=Resource(
                    attributes=[
                        KeyValue(key="service.name", value=AnyValue(string_value="pi")),
                        KeyValue(key="sdf.fixture", value=AnyValue(string_value="sample")),
                    ]
                ),
                scope_logs=[ScopeLogs(log_records=[record])],
            )
        ]
    )
    return payload.SerializeToString()


def _counter(value: int, time_nano: str) -> bytes:
    point = NumberDataPoint(
        time_unix_nano=int(time_nano),
        as_int=value,
        attributes=[
            KeyValue(key="type", value=AnyValue(string_value="input")),
            KeyValue(key="session.id", value=AnyValue(string_value="proto-session")),
            KeyValue(key="sdf.fixture", value=AnyValue(string_value="sample")),
        ],
    )
    metric = Metric(
        name="claude_code.token.usage",
        unit="tokens",
        sum=Sum(
            data_points=[point],
            aggregation_temporality=AggregationTemporality.AGGREGATION_TEMPORALITY_CUMULATIVE,
            is_monotonic=True,
        ),
    )
    request = ExportMetricsServiceRequest(
        resource_metrics=[
            ResourceMetrics(
                resource=Resource(
                    attributes=[
                        KeyValue(key="service.name", value=AnyValue(string_value="claude-code")),
                        KeyValue(key="session.id", value=AnyValue(string_value="proto-session")),
                        KeyValue(key="sdf.fixture", value=AnyValue(string_value="sample")),
                    ]
                ),
                scope_metrics=[ScopeMetrics(metrics=[metric])],
            )
        ]
    )
    return request.SerializeToString()
