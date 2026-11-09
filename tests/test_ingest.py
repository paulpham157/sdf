"""Ingest sample OTLP payloads through the local receiver.

The JSON files in tests/samples are hand-written. They are not captures from
Codex or Claude Code. Each resource is marked sdf.fixture=sample.
"""

from __future__ import annotations

import json
import threading
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

import pytest
from opentelemetry.proto.collector.metrics.v1.metrics_service_pb2 import (
    ExportMetricsServiceRequest,
)
from opentelemetry.proto.common.v1.common_pb2 import AnyValue, KeyValue
from opentelemetry.proto.metrics.v1.metrics_pb2 import (
    AggregationTemporality,
    Metric,
    NumberDataPoint,
    ResourceMetrics,
    ScopeMetrics,
    Sum,
)
from opentelemetry.proto.resource.v1.resource_pb2 import Resource

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

    sessions = {item.session_id: item for item in summarize(rows)}
    assert set(sessions) == {"sample-claude-1", "sample-codex-conv-1"}

    claude = sessions["sample-claude-1"]
    assert claude.tool == "claude-code"
    assert claude.service_names == ["claude-code"]
    assert claude.models == ["claude-sonnet-5"]
    assert claude.duration_ms == 2000
    assert claude.duration_source == "claude_code.interaction"
    assert claude.tokens == {"input": 100, "output": 40, "cacheRead": 10, "cacheCreation": 4}
    assert claude.tokens_source == "claude_code.token.usage"
    assert claude.cost_usd == 0.5
    assert claude.cost_source == "claude_code.cost.usage"
    assert [(call.name, call.call_id, call.source, call.duration_ms) for call in claude.tool_calls] == [
        ("Bash", "toolu_bash", "claude_code.tool_result", 250),
        ("Read", "toolu_read", "claude_code.tool", 100),
    ]

    codex = sessions["sample-codex-conv-1"]
    assert codex.tool == "codex"
    assert codex.models == ["gpt-5.1"]
    assert codex.duration_ms == 2500
    assert codex.duration_source == "codex.turn.e2e_duration_ms"
    assert codex.tokens == {"input": 200, "output": 30}
    assert codex.tokens_source == "codex.turn.token_usage"
    assert codex.cost_usd == pytest.approx(0.005)
    assert codex.cost_source == "codex.turn.cost_microusd"
    assert [(call.name, call.call_id, call.success, call.duration_ms) for call in codex.tool_calls] == [
        ("shell", "call-1", "true", 90)
    ]


def test_protobuf_cumulative_counter_keeps_the_latest_point(receiver, tmp_path, capsys):
    base, store = receiver
    _post(base, "/v1/metrics", _counter(10, "1000"), "application/x-protobuf")
    assert _post(base, "/v1/metrics", _counter(25, "2000"), "application/x-protobuf") == b""
    sessions = summarize(store.load())
    assert len(sessions) == 1
    assert sessions[0].session_id == "proto-session"
    assert sessions[0].tokens == {"input": 25}

    db = tmp_path / "telemetry.sqlite"
    assert main(["summary", "--db", str(db), "--session", "proto-session", "--json"]) == 0
    printed = json.loads(capsys.readouterr().out)
    assert printed[0]["session_id"] == "proto-session"
    assert printed[0]["cost_usd"] is None


def test_malformed_payload_is_rejected(receiver):
    base, store = receiver
    request = Request(base + "/v1/logs", data=b"not-otlp", method="POST")
    request.add_header("Content-Type", "application/json")
    with pytest.raises(HTTPError) as caught:
        urlopen(request)
    assert caught.value.code == 400
    assert store.load() == []


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
