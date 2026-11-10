"""OpenCode v2 and Pi fixtures shaped like the 2026-11-10 OTLP notes.

The JSON files are hand-written. Ids are fake. There is no prompt text and
no credential. Each resource is marked sdf.fixture=sample.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from sdf.otlp import decode_export
from sdf.records import Record
from sdf.summary import format_summary, summarize, tool_of

SAMPLES = Path(__file__).parent / "samples"
OPENCODE = "ses_sampleopencode0001"
PI = "2026-01-01T00-00-00-000Z_sample-pi-session"
MODEL = "recursive/flint-v1.0:free"


def _load(signal: str, name: str) -> list[Record]:
    return decode_export(signal, (SAMPLES / name).read_bytes(), "application/json")


def _opencode() -> list[Record]:
    return _load("traces", "opencode_traces.json") + _load("logs", "opencode_logs.json")


def _pi() -> list[Record]:
    return (
        _load("traces", "pi_traces.json")
        + _load("logs", "pi_logs.json")
        + _load("metrics", "pi_metrics.json")
    )


def test_fixtures_are_marked_sample_and_have_no_secrets():
    for name in (
        "opencode_traces.json",
        "opencode_logs.json",
        "pi_traces.json",
        "pi_logs.json",
        "pi_metrics.json",
    ):
        text = (SAMPLES / name).read_text()
        assert "sdf.fixture" in text
        assert "sample" in text
        lowered = text.lower()
        assert "api_key" not in lowered
        assert "authorization" not in lowered
        assert "hello from" not in lowered
        payload = json.loads(text)
        blobs = payload.get("resourceSpans") or payload.get("resourceLogs") or payload.get("resourceMetrics")
        attributes = blobs[0]["resource"]["attributes"]
        values = {item["key"]: item["value"].get("stringValue") for item in attributes}
        assert values["sdf.fixture"] == "sample"


def test_opencode_session_uses_span_tokens_without_summing_the_prefix():
    report = summarize(_opencode())
    assert [item.session_id for item in report.sessions] == [OPENCODE]
    session = report.sessions[0]
    assert session.tool == "opencode"
    assert session.service_names == ["opencode"]
    assert session.models == [MODEL]
    assert session.traces == 13
    assert session.logs == 0
    assert session.metrics == 0
    assert session.duration_ms == pytest.approx(3437.29)
    assert session.duration_source == "traces SessionRunner.drain"
    assert session.tokens["input_tokens"] == 5822
    assert session.tokens["input_tokens"] != 5601 + 5822
    assert session.tokens["output_tokens"] == 212 + 94
    assert session.tokens["cache_read.input_tokens"] == 5760
    assert session.tokens["cache_read.input_tokens"] != 256 + 5760
    assert session.tokens_source == "traces SessionStep.attempt"
    assert "max" in session.tokens_method
    assert session.cost_usd is None
    assert [(call.name, call.call_id, call.success, call.duration_ms) for call in session.tool_calls] == [
        ("write", "call_samplewrite0001", "unknown", 3.602)
    ]
    assert session.tool_calls[0].source == "traces Tool.execute"
    assert session.spans_hidden == 4
    shown = _names(session.spans)
    assert shown >= {
        "SessionRunner.drain",
        "SessionRunner.runStep",
        "SessionStep.attempt",
        "POST",
        "Tool.execute",
        "FileWrite.write",
    }
    assert "LLM.compile" not in shown
    assert "OpenAIChat.fromRequest" not in shown
    assert "FileWrite.commit" not in shown
    assert "Plugin.load" not in shown
    assert "FileSystem.readFileStringSafe" not in shown
    attempt = session.spans[0].children[0].children[0]
    assert attempt.name == "SessionStep.attempt"
    assert [child.name for child in attempt.children] == ["POST", "Tool.execute"]
    assert attempt.children[1].tool_name == "write"
    assert attempt.children[1].children[0].name == "FileWrite.write"
    reasons = {item.signal: item.reason for item in session.missing}
    assert "session.id" in reasons["logs"]
    assert "per-session" in reasons["logs"]
    assert reasons["metrics"] == "OpenCode v2 does not export OTLP metrics."
    assert "traces" not in reasons
    assert report.unattributed[0].tool == "opencode"
    assert report.unattributed[0].traces == 1
    assert report.unattributed[0].logs == 1
    text = format_summary(report)
    assert "tool: opencode" in text
    assert "Tool.execute write call_id=call_samplewrite0001" in text
    assert "success=unknown" in text
    assert "input_tokens=5822" in text
    payload = report.as_dict()
    assert payload["sessions"][0]["tool_calls"][0]["success"] == "unknown"
    assert payload["sessions"][0]["tool_calls"][0]["name"] == "write"


def test_pi_session_sums_turn_tokens_and_does_not_add_the_metric():
    report = summarize(_pi())
    assert [item.session_id for item in report.sessions] == [PI]
    session = report.sessions[0]
    assert session.tool == "pi"
    assert session.service_names == ["pi"]
    assert session.models == [MODEL]
    assert session.traces == 13
    assert session.logs == 2
    assert session.metrics == 6
    assert session.duration_ms == pytest.approx(7252.42)
    assert session.duration_source == "traces invoke_agent"
    assert session.tokens == {"input_tokens": 1245, "output_tokens": 832}
    assert session.tokens["input_tokens"] != 975
    assert session.tokens_source == "traces chat"
    assert "additive" in session.tokens_method
    assert "gen_ai.client.token.usage" in session.tokens_method
    assert "reasoning_tokens" not in session.tokens
    assert "cache_read_input_tokens" not in session.tokens
    assert session.cost_usd == 0
    assert session.cost_source == "traces chat pi.cost.usd"
    assert [(call.name, call.call_id, call.success, call.duration_ms) for call in session.tool_calls] == [
        ("bash", "call_sample_bash_1", "true", 10.643),
        ("read", "call_sample_read_1", "true", 5.28),
        ("bash", "call_sample_bash_2", "true", 5.815),
    ]
    input_metric = next(
        line
        for line in session.metric_lines
        if line.name == "gen_ai.client.token.usage" and "input" in (line.detail or "")
    )
    output_metric = next(
        line
        for line in session.metric_lines
        if line.name == "gen_ai.client.token.usage" and "output" in (line.detail or "")
    )
    assert input_metric.value == session.tokens["input_tokens"]
    assert output_metric.value == session.tokens["output_tokens"]
    assert all(line.value != 9999 for line in session.metric_lines)
    assert session.spans_hidden == 1
    shown = _names(session.spans)
    assert "invoke_agent pi" in shown
    assert "pi.turn" in shown
    assert f"chat {MODEL}" in shown
    assert "execute_tool bash" in shown
    assert "execute_tool read" in shown
    assert "resolve_tools" not in shown
    root = session.spans[0]
    assert root.name == "invoke_agent pi"
    assert [child.name for child in root.children] == ["pi.turn", "pi.turn", "pi.turn", "pi.turn"]
    assert [child.name for child in root.children[0].children] == [f"chat {MODEL}", "execute_tool bash"]
    assert root.children[0].children[1].success == "true"
    reasons = {item.signal: item.reason for item in session.missing}
    assert "session start and end" in reasons["log catalog"]
    assert "logs" not in reasons
    assert "metrics" not in reasons
    assert report.unattributed[0].tool == "pi"
    assert any(name.startswith("gen_ai.client.token.usage=9999") for name in report.unattributed[0].names)
    text = format_summary(report)
    assert "tool: pi" in text
    assert "execute_tool bash call_id=call_sample_bash_1" in text
    assert "success=true (traces)" in text
    assert "bash call_id=call_sample_bash_1 success=true" in text
    assert "pi.session.start x1" in text
    payload = session.as_dict()
    assert payload["tool_calls"][1]["name"] == "read"
    assert payload["tool_calls"][1]["success"] == "true"
    logs = [row for row in _pi() if row.signal == "logs"]
    assert logs[0].session_id == PI
    assert "session.id" not in logs[0].attributes
    assert "conversation.id" not in logs[0].attributes
    hidden = next(row for row in _pi() if row.name == "resolve_tools")
    assert "session.id" not in hidden.attributes
    assert hidden.session_id == PI


def test_pi_tool_error_flag_inverts_and_system_name_is_enough():
    errored = Record(
        signal="traces",
        service_name="pi",
        session_id="pi-error",
        name="execute_tool bash",
        start_unix_nano=0,
        end_unix_nano=2_000_000,
        attributes={
            "gen_ai.tool.name": "bash",
            "gen_ai.tool.call.id": "call_err",
            "pi.tool.is_error": True,
        },
    )
    call = summarize([errored]).sessions[0].tool_calls[0]
    assert call.success == "false"
    assert call.name == "bash"

    only_system = Record(
        signal="logs",
        name="pi.session.start",
        session_id="from-system",
        time_unix_nano=1,
        attributes={"gen_ai.system": "pi", "event.name": "pi.session.start"},
    )
    assert tool_of(only_system) == "pi"
    assert summarize([only_system]).sessions[0].tool == "pi"


def test_pi_metrics_fall_back_when_chat_spans_omit_usage_and_stay_unattributed_outside_the_window():
    span = Record(
        id=1,
        signal="traces",
        service_name="pi",
        session_id="pi-metric-only",
        name="invoke_agent pi",
        start_unix_nano=0,
        end_unix_nano=5_000_000_000,
        attributes={"gen_ai.system": "pi"},
    )
    inside = Record(
        id=2,
        signal="metrics",
        service_name="pi",
        name="gen_ai.client.token.usage",
        time_unix_nano=1_000_000_000,
        value=1245,
        metric_temporality="delta",
        metric_kind="sum",
        attributes={"gen_ai.token.type": "input"},
    )
    outside = Record(
        id=3,
        signal="metrics",
        service_name="pi",
        name="gen_ai.client.token.usage",
        time_unix_nano=20_000_000_000,
        value=50,
        metric_temporality="delta",
        metric_kind="sum",
        attributes={"gen_ai.token.type": "input"},
    )
    report = summarize([span, inside, outside])
    session = report.sessions[0]
    assert session.tokens == {"input": 1245}
    assert session.tokens_source == "metrics gen_ai.client.token.usage"
    assert report.unattributed[0].metrics == 1
    assert any("time window" in item.reason for item in summarize([span]).sessions[0].missing)


def test_session_id_accepts_pi_and_gen_ai_conversation_keys():
    trace = decode_export(
        "traces",
        json.dumps(
            {
                "resourceSpans": [
                    {
                        "resource": {
                            "attributes": [
                                {"key": "service.name", "value": {"stringValue": "pi"}},
                                {"key": "sdf.fixture", "value": {"stringValue": "sample"}},
                            ]
                        },
                        "scopeSpans": [
                            {
                                "spans": [
                                    {
                                        "name": "invoke_agent pi",
                                        "traceId": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                                        "spanId": "bbbbbbbbbbbbbbbb",
                                        "startTimeUnixNano": "1",
                                        "endTimeUnixNano": "2",
                                        "attributes": [
                                            {
                                                "key": "gen_ai.conversation.id",
                                                "value": {"stringValue": "from-gen-ai"},
                                            }
                                        ],
                                    }
                                ]
                            }
                        ],
                    }
                ]
            }
        ).encode(),
        "application/json",
    )
    assert trace[0].session_id == "from-gen-ai"
    log = decode_export(
        "logs",
        json.dumps(
            {
                "resourceLogs": [
                    {
                        "resource": {
                            "attributes": [
                                {"key": "sdf.fixture", "value": {"stringValue": "sample"}},
                            ]
                        },
                        "scopeLogs": [
                            {
                                "logRecords": [
                                    {
                                        "timeUnixNano": "1",
                                        "body": {"stringValue": "pi session from-pi started"},
                                        "attributes": [
                                            {
                                                "key": "event.name",
                                                "value": {"stringValue": "pi.session.start"},
                                            },
                                            {"key": "pi.session.id", "value": {"stringValue": "from-pi"}},
                                            {"key": "gen_ai.system", "value": {"stringValue": "pi"}},
                                        ],
                                    }
                                ]
                            }
                        ],
                    }
                ]
            }
        ).encode(),
        "application/json",
    )
    assert log[0].session_id == "from-pi"
    assert log[0].name == "pi.session.start"
    assert tool_of(log[0]) == "pi"


def test_opencode_and_pi_sessions_stay_separate():
    report = summarize(_opencode() + _pi())
    assert [item.session_id for item in report.sessions] == [OPENCODE, PI]
    assert [item.tool for item in report.unattributed] == ["opencode", "pi"]


def _names(nodes) -> set[str]:
    found = set()
    for node in nodes:
        found.add(node.name)
        found.update(_names(node.children))
    return found
