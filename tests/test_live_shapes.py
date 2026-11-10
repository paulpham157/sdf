"""Sanitized fixtures shaped like the 2026-11-10 live OpenRouter capture.

These records are hand-written. They are not a copy of the live SQLite file.
Every record is marked sdf.fixture=sample. No prompts and no credentials.
"""

from sdf.records import Record
from sdf.summary import format_summary, summarize

SAMPLE = {"sdf.fixture": "sample"}
CLAUDE = "sample-claude-live"
CODEX = "sample-codex-live"
MODEL = "recursive/flint-v1.0:free"


def _record(**kwargs) -> Record:
    resource = {"sdf.fixture": "sample", "service.name": kwargs.get("service_name")}
    return Record(resource=resource, **kwargs)


def _claude_fixture() -> list[Record]:
    common = dict(service_name="claude-code", session_id=CLAUDE, trace_id="aa" * 16)
    spans = [
        _record(
            id=1,
            signal="traces",
            name="claude_code.interaction",
            span_id="aa",
            start_unix_nano=0,
            end_unix_nano=3_571_881_593,
            attributes={"session.id": CLAUDE, "model": MODEL, **SAMPLE},
            **common,
        ),
        _record(
            id=2,
            signal="traces",
            name="claude_code.llm_request",
            span_id="l1",
            parent_span_id="aa",
            start_unix_nano=10,
            end_unix_nano=110_000_000,
            attributes={"session.id": CLAUDE, "model": MODEL, **SAMPLE},
            **common,
        ),
        _record(
            id=3,
            signal="traces",
            name="claude_code.llm_request",
            span_id="l2",
            parent_span_id="aa",
            start_unix_nano=200_000_000,
            end_unix_nano=400_000_000,
            attributes={"session.id": CLAUDE, "model": MODEL, **SAMPLE},
            **common,
        ),
        _record(
            id=4,
            signal="traces",
            name="claude_code.tool",
            span_id="tool",
            parent_span_id="aa",
            start_unix_nano=500_000_000,
            end_unix_nano=520_000_000,
            attributes={"session.id": CLAUDE, "tool_name": "Write", "tool_use_id": "call_write", **SAMPLE},
            **common,
        ),
        _record(
            id=5,
            signal="traces",
            name="claude_code.tool.blocked_on_user",
            span_id="wait",
            parent_span_id="tool",
            start_unix_nano=500_000_000,
            end_unix_nano=511_000_000,
            attributes={"session.id": CLAUDE, "tool_use_id": "call_write", **SAMPLE},
            **common,
        ),
        _record(
            id=6,
            signal="traces",
            name="claude_code.tool.execution",
            span_id="exec",
            parent_span_id="tool",
            start_unix_nano=511_000_000,
            end_unix_nano=520_000_000,
            attributes={"session.id": CLAUDE, "tool_use_id": "call_write", **SAMPLE},
            **common,
        ),
    ]
    logs = [
        _record(
            id=7,
            signal="logs",
            name="claude_code.user_prompt",
            time_unix_nano=1,
            attributes={"event.name": "user_prompt", "session.id": CLAUDE, **SAMPLE},
            **common,
        ),
        _record(
            id=8,
            signal="logs",
            name="claude_code.api_request",
            time_unix_nano=2,
            attributes={
                "event.name": "api_request",
                "session.id": CLAUDE,
                "model": MODEL,
                "cost_usd": 0.05807,
                "input_tokens": 1,
                **SAMPLE,
            },
            **common,
        ),
        _record(
            id=9,
            signal="logs",
            name="claude_code.tool_result",
            time_unix_nano=3,
            attributes={
                "event.name": "tool_result",
                "session.id": CLAUDE,
                "tool_name": "Write",
                "tool_use_id": "call_write",
                "success": "true",
                "duration_ms": 9,
                **SAMPLE,
            },
            **common,
        ),
    ]
    metrics = []
    order = 10
    for kind, value in (("input", 10739), ("output", 251), ("cacheRead", 10752), ("cacheCreation", 0)):
        metrics.append(
            _record(
                id=order,
                signal="metrics",
                name="claude_code.token.usage",
                time_unix_nano=4_000_000_000,
                value=value,
                metric_unit="tokens",
                metric_temporality="delta",
                metric_kind="sum",
                attributes={"type": kind, "model": MODEL, "session.id": CLAUDE, **SAMPLE},
                service_name="claude-code",
                session_id=CLAUDE,
            )
        )
        order += 1
    metrics.append(
        _record(
            id=order,
            signal="metrics",
            name="claude_code.cost.usage",
            time_unix_nano=4_000_000_000,
            value=0.065346,
            metric_unit="USD",
            metric_temporality="delta",
            metric_kind="sum",
            attributes={"model": MODEL, "session.id": CLAUDE, **SAMPLE},
            service_name="claude-code",
            session_id=CLAUDE,
        )
    )
    return spans + logs + metrics


def _codex_fixture() -> list[Record]:
    common = dict(service_name="codex_exec", session_id=CODEX, trace_id="bb" * 16)
    spans = [
        _record(
            id=1,
            signal="traces",
            name="run_turn",
            span_id="turn",
            start_unix_nano=0,
            end_unix_nano=6_682_000_000,
            attributes={"conversation.id": CODEX, "model": MODEL, **SAMPLE},
            **common,
        ),
        _record(
            id=2,
            signal="traces",
            name="codex.sampling",
            span_id="sample",
            parent_span_id="turn",
            start_unix_nano=1_000_000,
            end_unix_nano=2_000_000_000,
            attributes={"conversation.id": CODEX, "model": MODEL, **SAMPLE},
            **common,
        ),
        _record(
            id=3,
            signal="traces",
            name="exec_command",
            span_id="cmd-a",
            parent_span_id="turn",
            start_unix_nano=3_000_000_000,
            end_unix_nano=3_063_000_000,
            attributes={"conversation.id": CODEX, "tool_name": "exec_command", "call_id": "call_a", **SAMPLE},
            **common,
        ),
        _record(
            id=4,
            signal="traces",
            name="codex.tool_blocking",
            span_id="block-a",
            parent_span_id="cmd-a",
            start_unix_nano=3_000_000_000,
            end_unix_nano=3_012_000_000,
            attributes={"conversation.id": CODEX, "call_id": "call_a", **SAMPLE},
            **common,
        ),
        _record(
            id=5,
            signal="traces",
            name="unified_exec.exec_command",
            span_id="run-a",
            parent_span_id="cmd-a",
            start_unix_nano=3_012_000_000,
            end_unix_nano=3_063_000_000,
            attributes={"conversation.id": CODEX, "call_id": "call_a", **SAMPLE},
            **common,
        ),
        _record(
            id=6,
            signal="traces",
            name="exec_command",
            span_id="cmd-b",
            parent_span_id="turn",
            start_unix_nano=4_000_000_000,
            end_unix_nano=4_060_000_000,
            attributes={"conversation.id": CODEX, "tool_name": "exec_command", "call_id": "call_b", **SAMPLE},
            **common,
        ),
        _record(
            id=7,
            signal="traces",
            name="append_items",
            span_id="noise",
            parent_span_id="turn",
            start_unix_nano=10,
            end_unix_nano=20,
            attributes={"conversation.id": CODEX, **SAMPLE},
            **common,
        ),
    ]
    logs = [
        _record(
            id=8,
            signal="logs",
            name="codex.conversation_starts",
            time_unix_nano=1,
            attributes={"conversation.id": CODEX, "model": MODEL, **SAMPLE},
            **common,
        )
    ]
    for index, (input_tokens, output_tokens) in enumerate(((8896, 273), (9019, 180), (9108, 76)), start=9):
        logs.append(
            _record(
                id=index,
                signal="logs",
                name="codex.sse_event",
                time_unix_nano=index * 1_000_000,
                attributes={
                    "event.kind": "response.completed",
                    "conversation.id": CODEX,
                    "model": MODEL,
                    "input_token_count": input_tokens,
                    "output_token_count": output_tokens,
                    "cached_token_count": input_tokens // 2,
                    **SAMPLE,
                },
                **common,
            )
        )
    logs.extend(
        [
            _record(
                id=12,
                signal="logs",
                name="codex.tool_result",
                time_unix_nano=3_100_000_000,
                attributes={
                    "conversation.id": CODEX,
                    "tool_name": "exec_command",
                    "call_id": "call_a",
                    "success": "true",
                    "duration_ms": 63,
                    **SAMPLE,
                },
                **common,
            ),
            _record(
                id=13,
                signal="logs",
                name="codex.tool_result",
                time_unix_nano=4_100_000_000,
                attributes={
                    "conversation.id": CODEX,
                    "tool_name": "exec_command",
                    "call_id": "call_b",
                    "success": "true",
                    "duration_ms": 60,
                    **SAMPLE,
                },
                **common,
            ),
        ]
    )
    unscoped = [
        _record(
            id=20,
            signal="logs",
            service_name="codex_exec",
            name=(
                "metrics histogram [codex.turn.token_usage] failed: "
                f"tag value contains invalid characters: {MODEL}"
            ),
            time_unix_nano=1_000_000_000,
            attributes=dict(SAMPLE),
        ),
        _record(
            id=21,
            signal="logs",
            service_name="codex_exec",
            name=(
                "metrics histogram [codex.turn.e2e_duration_ms] failed: "
                f"tag value contains invalid characters: {MODEL}"
            ),
            time_unix_nano=1_000_000_000,
            attributes=dict(SAMPLE),
        ),
        _record(
            id=22,
            signal="logs",
            service_name="codex_exec",
            name=(
                "metrics counter [codex.turn.cost_microusd] failed: "
                f"tag value contains invalid characters: {MODEL}"
            ),
            time_unix_nano=1_000_000_000,
            attributes=dict(SAMPLE),
        ),
        _record(
            id=23,
            signal="traces",
            service_name="codex_exec",
            name="get_model_info",
            trace_id="cc" * 16,
            span_id="info",
            start_unix_nano=500_000_000,
            end_unix_nano=600_000_000,
            attributes={"model": "gpt-5.6-luna", **SAMPLE},
        ),
        _record(
            id=24,
            signal="metrics",
            service_name="codex_exec",
            name="codex.process.start",
            time_unix_nano=2_000_000_000,
            value=1,
            metric_temporality="delta",
            metric_kind="sum",
            attributes=dict(SAMPLE),
        ),
        _record(
            id=25,
            signal="metrics",
            service_name="codex_exec",
            name="codex.sqlite.init",
            time_unix_nano=6_682_000_000 + 3_600_000_000_000,
            value=1,
            metric_temporality="delta",
            metric_kind="sum",
            attributes=dict(SAMPLE),
        ),
    ]
    return spans + logs + unscoped


def test_fixtures_are_marked_sample():
    for record in _claude_fixture() + _codex_fixture():
        assert record.resource["sdf.fixture"] == "sample"
        assert "prompt" not in record.attributes


def test_claude_summary_shows_spans_logs_and_metrics():
    session = summarize(_claude_fixture()).sessions[0]
    assert session.session_id == CLAUDE
    assert session.traces == 6 and session.logs == 3 and session.metrics == 5
    assert session.missing == []
    assert session.duration_source == "traces claude_code.interaction"
    assert session.duration_ms == 3571.881593
    assert session.tokens == {"input": 10739, "output": 251, "cacheRead": 10752, "cacheCreation": 0}
    assert session.tokens_source.startswith("metrics ")
    assert session.cost_usd == 0.065346
    assert session.cost_note == "agent estimate, not the provider price"
    call = session.tool_calls[0]
    assert call.name == "Write" and call.call_id == "call_write" and call.success == "true"
    assert call.execution_ms == 9
    assert call.wait_ms == 11
    assert "logs claude_code.tool_result" in call.source
    assert session.spans[0].name == "claude_code.interaction"
    assert [child.name for child in session.spans[0].children] == [
        "claude_code.llm_request",
        "claude_code.llm_request",
        "claude_code.tool",
    ]
    tool = session.spans[0].children[-1]
    assert tool.success == "true"
    assert [child.success for child in tool.children] == [None, None]
    text = format_summary(summarize(_claude_fixture()))
    assert "signals: traces=6 logs=3 metrics=5" in text
    assert "claude_code.tool.execution" in text
    assert "missing: (none)" in text


def test_codex_openrouter_shape_uses_spans_and_does_not_sum_prefix_tokens():
    report = summarize(_codex_fixture())
    assert [item.session_id for item in report.sessions] == [CODEX]
    session = report.sessions[0]
    assert session.service_names == ["codex_exec"]
    assert session.models == [MODEL]
    assert "gpt-5.6-luna" not in session.models
    assert session.duration_source == "traces run_turn"
    assert session.duration_ms == 6682
    assert session.tokens["input_token_count"] == 9108
    assert session.tokens["output_token_count"] == 273 + 180 + 76
    assert session.cost_usd is None
    assert session.metrics == 1
    assert any(line.name == "codex.process.start" for line in session.metric_lines)
    assert any(item.signal == "session metrics" and ":" in item.reason for item in session.missing)
    calls = {(call.call_id, call.success, call.duration_ms, call.wait_ms, call.execution_ms) for call in session.tool_calls}
    assert calls == {("call_a", "true", 51, 12, 51), ("call_b", "true", 60, None, None)}
    assert "logs codex.tool_result" in session.tool_calls[0].source
    shown = _names(session.spans)
    assert "exec_command" in shown and "run_turn" in shown
    assert "append_items" not in shown and "get_model_info" not in shown
    assert session.spans_hidden == 2
    assert any(event.name == "codex.metric_export_error" and event.count == 3 for event in session.log_events)
    assert report.unattributed[0].tool == "codex"
    assert any(name.startswith("codex.sqlite.init=") for name in report.unattributed[0].names)
    assert "not a session" in report.unattributed[0].reason

    shown_all = _names(summarize(_codex_fixture(), all_spans=True).sessions[0].spans)
    assert "append_items" in shown_all


def test_overlapping_sessions_do_not_absorb_an_ambiguous_span():
    def session(name, start):
        return _record(
            signal="traces",
            service_name="codex_exec",
            session_id=name,
            name="run_turn",
            span_id=name,
            trace_id=name,
            start_unix_nano=start,
            end_unix_nano=start + 5_000_000_000,
            attributes={"conversation.id": name, **SAMPLE},
        )

    ambiguous = _record(
        signal="traces",
        service_name="codex_exec",
        name="append_items",
        start_unix_nano=4_000_000_000,
        end_unix_nano=4_100_000_000,
        attributes=dict(SAMPLE),
    )
    traced = _record(
        signal="traces",
        service_name="codex_exec",
        name="exec_command",
        trace_id="left",
        span_id="cmd",
        start_unix_nano=9_000_000_000,
        end_unix_nano=9_010_000_000,
        attributes={"call_id": "call_z", "tool_name": "exec_command", **SAMPLE},
    )
    report = summarize([session("left", 0), session("right", 3_000_000_000), ambiguous, traced])
    assert {item.session_id for item in report.sessions} == {"left", "right"}
    left = next(item for item in report.sessions if item.session_id == "left")
    assert any(node.name == "exec_command" for node in left.spans)
    assert any(name.startswith("append_items") for name in report.unattributed[0].names)


def _names(nodes) -> set[str]:
    found = set()
    for node in nodes:
        found.add(node.name)
        found.update(_names(node.children))
    return found
