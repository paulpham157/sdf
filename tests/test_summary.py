"""Summary rules over in-memory records. No agent CLI is involved."""

from sdf.records import Record
from sdf.summary import format_summary, summarize


def _metric(name, value, *, session=None, temporality="delta", kind="sum", attributes=None, when=1, order=1, service=None):
    attributes = dict(attributes or {})
    if session is not None:
        attributes["session.id"] = session
    return Record(
        id=order,
        signal="metrics",
        service_name=service or ("claude-code" if name.startswith("claude_code.") else "codex-cli"),
        session_id=session,
        name=name,
        time_unix_nano=when,
        value=value,
        metric_temporality=temporality,
        metric_kind=kind,
        attributes=attributes,
    )


def test_delta_token_points_sum_and_cumulative_points_keep_the_latest():
    delta = summarize(
        [
            _metric("claude_code.token.usage", 10, session="s", attributes={"type": "input"}, when=1, order=1),
            _metric("claude_code.token.usage", 15, session="s", attributes={"type": "input"}, when=2, order=2),
        ]
    ).sessions
    assert delta[0].tokens == {"input": 25}
    assert delta[0].tokens_source == "metrics claude_code.token.usage"

    cumulative = summarize(
        [
            _metric(
                "claude_code.token.usage",
                10,
                session="s",
                temporality="cumulative",
                attributes={"type": "input"},
                when=1,
                order=1,
            ),
            _metric(
                "claude_code.token.usage",
                40,
                session="s",
                temporality="cumulative",
                attributes={"type": "input"},
                when=2,
                order=2,
            ),
        ]
    ).sessions
    assert cumulative[0].tokens == {"input": 40}


def test_codex_sse_tokens_use_max_input_and_sum_output_and_last_event_per_turn():
    def event(order, turn, input_tokens, output_tokens, when):
        return Record(
            id=order,
            signal="logs",
            service_name="codex_exec",
            session_id="conv-1",
            name="codex.sse_event",
            time_unix_nano=when,
            attributes={
                "event.kind": "response.completed",
                "conversation.id": "conv-1",
                "model": "gpt-5.1",
                "turn.id": turn,
                "input_token_count": input_tokens,
                "output_token_count": output_tokens,
            },
        )

    session = summarize(
        [
            event(1, "turn-a", 100, 10, 1),
            event(2, "turn-a", 140, 99, 2),
            event(3, "turn-b", 200, 20, 3),
        ]
    ).sessions[0]
    assert session.tokens["input_token_count"] == 200
    assert session.tokens["output_token_count"] == 119
    assert "max" in session.tokens_method
    assert session.tokens_source.startswith("logs codex.sse_event")


def test_log_fallback_uses_codex_sse_and_turn_cost_and_does_not_invent_a_vendor_duration():
    sessions = summarize(
        [
            Record(
                id=1,
                signal="logs",
                service_name="codex-cli",
                session_id="conv-1",
                name="codex.sse_event",
                time_unix_nano=1_000_000_000,
                attributes={
                    "event.name": "codex.sse_event",
                    "event.kind": "response.completed",
                    "conversation.id": "conv-1",
                    "model": "gpt-5.1",
                    "input_token_count": 7,
                    "output_token_count": 1,
                },
            ),
            Record(
                id=2,
                signal="logs",
                service_name="codex-cli",
                session_id="conv-1",
                name="codex.turn_cost",
                time_unix_nano=6_000_000_000,
                attributes={
                    "event.name": "codex.turn_cost",
                    "conversation.id": "conv-1",
                    "usage.estimated_usd": "0.25",
                },
            ),
        ]
    ).sessions
    assert len(sessions) == 1
    session = sessions[0]
    assert session.tool == "codex"
    assert session.models == ["gpt-5.1"]
    assert session.tokens == {"input_token_count": 7, "output_token_count": 1}
    assert session.tokens_source == "logs codex.sse_event response.completed"
    assert session.cost_usd == 0.25
    assert session.cost_source == "logs codex.turn_cost usage.estimated_usd"
    assert session.duration_ms == 5000
    assert session.duration_source == "observed_window"
    assert session.tool_calls == []
    assert {item.signal for item in session.missing} == {"traces", "metrics"}


def test_unscoped_metrics_are_unattributed_instead_of_a_session():
    report = summarize(
        [
            _metric(
                "codex.turn.token_usage",
                12,
                session=None,
                kind="histogram",
                attributes={"token_type": "input"},
            ),
            _metric("codex.websocket.event", 3, session=None, attributes={"kind": "sample"}),
        ]
    )
    assert report.sessions == []
    assert len(report.unattributed) == 1
    assert report.unattributed[0].tool == "codex"
    assert report.unattributed[0].metrics == 2
    assert any(name.startswith("codex.turn.token_usage=12") for name in report.unattributed[0].names)


def test_text_marks_missing_signals():
    text = format_summary(
        summarize(
            [
                Record(
                    signal="logs",
                    service_name="claude-code",
                    session_id="only-a-prompt",
                    name="claude_code.user_prompt",
                    time_unix_nano=1,
                    attributes={"event.name": "user_prompt"},
                )
            ]
        )
    )
    assert "session only-a-prompt" in text
    assert "tool: claude-code" in text
    assert "signals: traces=0 logs=1 metrics=0" in text
    assert "cost_usd: (not reported)" in text
    assert "tokens: (not reported)" in text
    assert "(none reported)" in text
    assert "missing:" in text
    assert "traces:" in text
    assert "metrics:" in text
