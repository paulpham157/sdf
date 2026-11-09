"""Summarize one collected session from telemetry that is actually present.

Codex and Claude Code do not emit the same signals. Each field below names
the source it used. A missing source stays unset; nothing is estimated.
"""

from __future__ import annotations

from dataclasses import asdict, dataclass, field

from sdf.records import Record

_CLAUDE_TOKEN_METRIC = "claude_code.token.usage"
_CODEX_TOKEN_METRIC = "codex.turn.token_usage"
_CLAUDE_COST_METRIC = "claude_code.cost.usage"
_CODEX_COST_METRIC = "codex.turn.cost_microusd"
_CODEX_DURATION_METRIC = "codex.turn.e2e_duration_ms"
_CLAUDE_ACTIVE_METRIC = "claude_code.active_time.total"
_TOOL_LOGS = {"claude_code.tool_result", "codex.tool_result"}
_CLAUDE_API = "claude_code.api_request"
_CODEX_SSE = "codex.sse_event"
_CODEX_TURN_COST = "codex.turn_cost"


@dataclass
class ToolCall:
    name: str | None
    call_id: str | None
    success: str | None
    duration_ms: float | None
    count: float | None
    source: str


@dataclass
class SessionSummary:
    session_id: str | None
    tool: str
    service_names: list[str] = field(default_factory=list)
    models: list[str] = field(default_factory=list)
    duration_ms: float | None = None
    duration_source: str | None = None
    tokens: dict[str, float] = field(default_factory=dict)
    tokens_source: str | None = None
    cost_usd: float | None = None
    cost_source: str | None = None
    tool_calls: list[ToolCall] = field(default_factory=list)

    def as_dict(self) -> dict:
        return asdict(self)


@dataclass
class _Point:
    name: str
    kind: str
    temporality: str
    value: float
    time: int
    order: int
    attributes: dict


def summarize(records: list[Record]) -> list[SessionSummary]:
    buckets: dict[tuple[str, str | None], list[Record]] = {}
    for record in records:
        key = (tool_of(record), record.session_id)
        buckets.setdefault(key, []).append(record)
    sessions = []
    for (tool, session_id), grouped in buckets.items():
        summary = _summarize_bucket(tool, session_id, grouped)
        if _worth_reporting(summary):
            sessions.append(summary)
    sessions.sort(key=lambda item: (item.tool, item.session_id is None, item.session_id or ""))
    return sessions


def format_summary(sessions: list[SessionSummary]) -> str:
    if not sessions:
        return "No telemetry sessions in the store.\n"
    lines: list[str] = []
    for session in sessions:
        lines.append(f"session {session.session_id or '(none)'}")
        lines.append(f"  tool: {session.tool}")
        lines.append(f"  service: {_join(session.service_names)}")
        lines.append(f"  model: {_join(session.models)}")
        lines.append(f"  duration_ms: {_field(session.duration_ms, session.duration_source)}")
        lines.append(f"  tokens: {_format_tokens(session)}")
        lines.append(f"  cost_usd: {_field(session.cost_usd, session.cost_source)}")
        if session.tool_calls:
            lines.append("  tool_calls:")
            for call in session.tool_calls:
                lines.append(f"    - {_format_call(call)}")
        else:
            lines.append("  tool_calls: (none reported)")
    return "\n".join(lines) + "\n"


def tool_of(record: Record) -> str:
    service = (record.service_name or "").lower()
    name = record.name or ""
    body = record.body or ""
    origin = str(record.attributes.get("originator") or record.resource.get("originator") or "")
    event = str(record.attributes.get("event.name") or "")
    if (
        service.startswith("claude-code")
        or name.startswith("claude_code.")
        or body.startswith("claude_code.")
        or event.startswith("claude_code.")
    ):
        return "claude-code"
    if (
        "codex" in service
        or origin.lower().startswith("codex")
        or name.startswith("codex.")
        or body.startswith("codex.")
        or event.startswith("codex.")
    ):
        return "codex"
    return "unknown"


def _summarize_bucket(tool: str, session_id: str | None, records: list[Record]) -> SessionSummary:
    points = _reduce(_points(records))
    duration_ms, duration_source = _duration(records, points)
    tokens, tokens_source = _tokens(records, points)
    cost_usd, cost_source = _cost(records, points)
    return SessionSummary(
        session_id=session_id,
        tool=tool,
        service_names=_unique(record.service_name for record in records),
        models=_models(records, points),
        duration_ms=duration_ms,
        duration_source=duration_source,
        tokens=tokens,
        tokens_source=tokens_source,
        cost_usd=cost_usd,
        cost_source=cost_source,
        tool_calls=_tool_calls(records, points),
    )


def _worth_reporting(summary: SessionSummary) -> bool:
    return bool(
        summary.session_id
        or summary.models
        or summary.tokens
        or summary.cost_usd is not None
        or summary.tool_calls
        or summary.duration_ms is not None
    )


def _points(records: list[Record]) -> list[_Point]:
    points = []
    for record in records:
        if record.signal != "metrics" or record.value is None:
            continue
        points.append(
            _Point(
                name=record.name or "",
                kind=record.metric_kind or "",
                temporality=record.metric_temporality or "unspecified",
                value=record.value,
                time=record.time_unix_nano or 0,
                order=record.id,
                attributes=record.attributes,
            )
        )
    return points


def _reduce(points: list[_Point]) -> list[_Point]:
    """Sum delta points. Keep the latest cumulative or gauge point per series."""
    grouped: dict[tuple, list[_Point]] = {}
    for point in points:
        key = (
            point.name,
            point.kind,
            point.temporality,
            tuple(sorted((key, _freeze(value)) for key, value in point.attributes.items())),
        )
        grouped.setdefault(key, []).append(point)
    reduced = []
    for group in grouped.values():
        kind = group[0].kind
        temporality = group[0].temporality
        if kind == "gauge" or temporality == "cumulative":
            reduced.append(max(group, key=lambda item: (item.time, item.order)))
        else:
            reduced.append(
                _Point(
                    name=group[0].name,
                    kind=kind,
                    temporality=temporality,
                    value=sum(item.value for item in group),
                    time=max(item.time for item in group),
                    order=max(item.order for item in group),
                    attributes=group[0].attributes,
                )
            )
    return reduced


def _duration(records: list[Record], points: list[_Point]) -> tuple[float | None, str | None]:
    span_total = 0.0
    span_found = False
    for record in records:
        if record.signal != "traces" or record.name != "claude_code.interaction":
            continue
        elapsed = _span_ms(record)
        if elapsed is None:
            elapsed = _number(record.attributes.get("interaction.duration_ms"))
        if elapsed is None:
            continue
        span_total += elapsed
        span_found = True
    if span_found:
        return span_total, "claude_code.interaction"

    e2e = [point.value for point in points if point.name == _CODEX_DURATION_METRIC]
    if e2e:
        return sum(e2e), _CODEX_DURATION_METRIC

    active = [point.value for point in points if point.name == _CLAUDE_ACTIVE_METRIC]
    if active:
        return sum(active) * 1000, _CLAUDE_ACTIVE_METRIC

    stamps = [
        stamp
        for record in records
        for stamp in (record.start_unix_nano, record.end_unix_nano, record.time_unix_nano)
        if stamp is not None
    ]
    if len(stamps) >= 2:
        return (max(stamps) - min(stamps)) / 1_000_000, "observed_window"
    return None, None


def _tokens(records: list[Record], points: list[_Point]) -> tuple[dict[str, float], str | None]:
    claude = [point for point in points if point.name == _CLAUDE_TOKEN_METRIC]
    if claude:
        return _sum_by(claude, "type"), _CLAUDE_TOKEN_METRIC
    codex = [point for point in points if point.name == _CODEX_TOKEN_METRIC]
    if codex:
        return _sum_by(codex, "token_type"), _CODEX_TOKEN_METRIC

    totals: dict[str, float] = {}
    source = None
    for record in records:
        if record.signal != "logs":
            continue
        if record.name == _CLAUDE_API:
            if _add_fields(
                totals,
                record,
                ("input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens"),
            ):
                source = source or _CLAUDE_API
        elif record.name == _CODEX_SSE and str(record.attributes.get("event.kind")) == "response.completed":
            if _add_fields(
                totals,
                record,
                (
                    "input_token_count",
                    "output_token_count",
                    "cached_token_count",
                    "cache_write_token_count",
                    "reasoning_token_count",
                ),
            ):
                source = source or f"{_CODEX_SSE} response.completed"
    return totals, source


def _cost(records: list[Record], points: list[_Point]) -> tuple[float | None, str | None]:
    claude = [point.value for point in points if point.name == _CLAUDE_COST_METRIC]
    if claude:
        return sum(claude), _CLAUDE_COST_METRIC
    codex = [point.value for point in points if point.name == _CODEX_COST_METRIC]
    if codex:
        return sum(codex) / 1_000_000, _CODEX_COST_METRIC

    total = 0.0
    found = False
    source = None
    for record in records:
        if record.signal != "logs":
            continue
        if record.name == _CLAUDE_API:
            amount = _number(record.attributes.get("cost_usd"))
            label = f"{_CLAUDE_API} cost_usd"
        elif record.name == _CODEX_TURN_COST:
            amount = _number(record.attributes.get("usage.estimated_usd"))
            label = f"{_CODEX_TURN_COST} usage.estimated_usd"
        else:
            continue
        if amount is None:
            continue
        total += amount
        found = True
        source = source or label
    if found:
        return total, source
    return None, None


def _tool_calls(records: list[Record], points: list[_Point]) -> list[ToolCall]:
    calls: list[ToolCall] = []
    seen: set[str] = set()
    for record in records:
        if record.signal != "logs" or record.name not in _TOOL_LOGS:
            continue
        call_id = _first(record.attributes, ("call_id", "tool_use_id"))
        if call_id:
            seen.add(call_id)
        calls.append(
            ToolCall(
                name=_text(record.attributes.get("tool_name")),
                call_id=call_id,
                success=_text(record.attributes.get("success")),
                duration_ms=_number(record.attributes.get("duration_ms")),
                count=None,
                source=record.name or "",
            )
        )
    for record in records:
        if record.signal != "traces" or record.name != "claude_code.tool":
            continue
        call_id = _first(record.attributes, ("tool_use_id", "call_id", "gen_ai.tool.call.id"))
        if call_id and call_id in seen:
            continue
        if call_id:
            seen.add(call_id)
        duration = _number(record.attributes.get("duration_ms"))
        if duration is None:
            duration = _span_ms(record)
        calls.append(
            ToolCall(
                name=_text(record.attributes.get("tool_name")),
                call_id=call_id,
                success=None,
                duration_ms=duration,
                count=None,
                source="claude_code.tool",
            )
        )
    if calls:
        return calls
    for point in points:
        if point.name != "codex.tool.call":
            continue
        calls.append(
            ToolCall(
                name=_text(point.attributes.get("tool")),
                call_id=None,
                success=_text(point.attributes.get("success")),
                duration_ms=None,
                count=point.value,
                source="codex.tool.call",
            )
        )
    return calls


def _models(records: list[Record], points: list[_Point]) -> list[str]:
    found: list[str] = []
    sources = [record.attributes for record in records]
    sources.extend(point.attributes for point in points)
    for attributes in sources:
        for key in ("model", "gen_ai.request.model"):
            text = _text(attributes.get(key))
            if text and text not in found:
                found.append(text)
    return found


def _sum_by(points: list[_Point], key: str) -> dict[str, float]:
    totals: dict[str, float] = {}
    for point in points:
        label = _text(point.attributes.get(key)) or "value"
        totals[label] = totals.get(label, 0.0) + point.value
    return totals


def _add_fields(totals: dict[str, float], record: Record, keys: tuple[str, ...]) -> bool:
    found = False
    for key in keys:
        amount = _number(record.attributes.get(key))
        if amount is None:
            continue
        totals[key] = totals.get(key, 0.0) + amount
        found = True
    return found


def _span_ms(record: Record) -> float | None:
    if record.start_unix_nano is None or record.end_unix_nano is None:
        return None
    if record.end_unix_nano < record.start_unix_nano:
        return None
    return (record.end_unix_nano - record.start_unix_nano) / 1_000_000


def _first(attributes: dict, keys: tuple[str, ...]) -> str | None:
    for key in keys:
        text = _text(attributes.get(key))
        if text:
            return text
    return None


def _unique(values) -> list[str]:
    found: list[str] = []
    for value in values:
        if value and value not in found:
            found.append(value)
    return found


def _number(value) -> float | None:
    if isinstance(value, bool) or value is None or value == "":
        return None
    if isinstance(value, (int, float)):
        return float(value)
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def _text(value) -> str | None:
    if isinstance(value, bool):
        return "true" if value else "false"
    if value is None:
        return None
    text = str(value).strip()
    return text or None


def _freeze(value):
    if isinstance(value, dict):
        return tuple(sorted((key, _freeze(item)) for key, item in value.items()))
    if isinstance(value, list):
        return tuple(_freeze(item) for item in value)
    return value


def _join(values: list[str]) -> str:
    return ", ".join(values) if values else "(not reported)"


def _field(value: float | None, source: str | None) -> str:
    if value is None:
        return "(not reported)"
    rendered = _fmt(value)
    if source:
        return f"{rendered} ({source})"
    return rendered


def _format_tokens(session: SessionSummary) -> str:
    if not session.tokens:
        return "(not reported)"
    parts = [f"{key}={_fmt(value)}" for key, value in session.tokens.items()]
    rendered = " ".join(parts)
    if session.tokens_source:
        return f"{rendered} ({session.tokens_source})"
    return rendered


def _format_call(call: ToolCall) -> str:
    parts = [call.name or "(unnamed)"]
    if call.call_id:
        parts.append(f"call_id={call.call_id}")
    if call.success:
        parts.append(f"success={call.success}")
    if call.duration_ms is not None:
        parts.append(f"duration_ms={_fmt(call.duration_ms)}")
    if call.count is not None:
        parts.append(f"count={_fmt(call.count)}")
    parts.append(f"source={call.source}")
    return " ".join(parts)


def _fmt(value: float) -> str:
    if abs(value - round(value)) < 1e-9:
        return str(int(round(value)))
    return f"{value:.6g}"
