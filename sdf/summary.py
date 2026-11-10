"""Summarize collected sessions from telemetry that is actually present.

Claude Code, Codex, OpenCode, and Pi do not emit the same signals. Every
number names the signal it came from. A signal that did not arrive is listed
as missing, with the reason when the telemetry itself explains it.
"""

from __future__ import annotations

import re
from dataclasses import asdict, dataclass, field, replace

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
_TIME_SLACK_NS = 2_000_000_000

_CLAUDE_SPANS = {
    "claude_code.interaction",
    "claude_code.llm_request",
    "claude_code.tool",
    "claude_code.tool.blocked_on_user",
    "claude_code.tool.execution",
    "claude_code.hook",
}
_CODEX_SPANS = {
    "run_turn",
    "codex.sampling",
    "codex.turn_input",
    "exec_command",
    "unified_exec.exec_command",
    "codex.tool_blocking",
}
_TOOL_SPANS = {"claude_code.tool", "exec_command", "unified_exec.exec_command"}
_WAIT_SPANS = {"claude_code.tool.blocked_on_user", "codex.tool_blocking"}
_EXEC_SPANS = {"claude_code.tool.execution", "unified_exec.exec_command"}
_OPENCODE_SPANS = {
    "SessionRunner.drain",
    "SessionRunner.runStep",
    "SessionStep.attempt",
    "Tool.execute",
    "FileWrite.write",
    "POST",
}
_PI_SPAN_HEADS = {"invoke_agent", "pi.turn", "chat", "execute_tool"}
_PI_TOKEN_METRIC = "gen_ai.client.token.usage"
_PI_LOGS = {"pi.session.start", "pi.session.end"}
_CUMULATIVE_USAGE = {
    "input_tokens",
    "cache_read.input_tokens",
    "cache_read_input_tokens",
    "cache_write.input_tokens",
    "cache_write_input_tokens",
}
_USAGE_ORDER = (
    "input_tokens",
    "output_tokens",
    "cache_read.input_tokens",
    "cache_read_input_tokens",
    "cache_write.input_tokens",
    "cache_write_input_tokens",
    "reasoning_tokens",
)
_SESSION_CODEX_METRICS = (
    "codex.turn.token_usage",
    "codex.turn.cost_microusd",
    "codex.turn.e2e_duration_ms",
    "codex.tool.call",
)
_MODEL_LOGS = {
    "claude_code.user_prompt",
    "claude_code.assistant_response",
    "claude_code.api_request",
    "claude_code.api_error",
    "claude_code.tool_result",
    "codex.conversation_starts",
    "codex.user_prompt",
    "codex.api_request",
    "codex.sse_event",
    "codex.tool_result",
    "codex.turn_cost",
}
_MODEL_SPANS = _CLAUDE_SPANS | {"run_turn", "codex.sampling", "exec_command"}
_CUMULATIVE_TOKEN_FIELDS = ("input_token_count", "cached_token_count", "cache_write_token_count")
_ADDITIVE_TOKEN_FIELDS = ("output_token_count", "reasoning_token_count")
_METRIC_FAIL = re.compile(
    r"metrics (?:histogram|counter|gauge) \[([^\]]+)\] failed: tag value contains invalid characters: (\S+)"
)


@dataclass
class ToolCall:
    name: str | None
    call_id: str | None
    success: str | None
    duration_ms: float | None
    count: float | None
    source: str
    wait_ms: float | None = None
    wait_source: str | None = None
    execution_ms: float | None = None
    execution_source: str | None = None


@dataclass
class SpanNode:
    name: str
    duration_ms: float | None
    call_id: str | None = None
    tool_name: str | None = None
    success: str | None = None
    success_source: str | None = None
    wait_ms: float | None = None
    wait_source: str | None = None
    execution_ms: float | None = None
    execution_source: str | None = None
    children: list[SpanNode] = field(default_factory=list)


@dataclass
class LogLine:
    name: str
    count: int
    detail: str | None = None


@dataclass
class MetricLine:
    name: str
    value: float
    unit: str | None
    detail: str | None
    source: str


@dataclass
class Missing:
    signal: str
    reason: str


@dataclass
class SessionSummary:
    session_id: str | None
    tool: str
    service_names: list[str] = field(default_factory=list)
    models: list[str] = field(default_factory=list)
    traces: int = 0
    logs: int = 0
    metrics: int = 0
    duration_ms: float | None = None
    duration_source: str | None = None
    tokens: dict[str, float] = field(default_factory=dict)
    tokens_source: str | None = None
    tokens_method: str | None = None
    cost_usd: float | None = None
    cost_source: str | None = None
    cost_note: str | None = None
    spans: list[SpanNode] = field(default_factory=list)
    spans_hidden: int = 0
    log_events: list[LogLine] = field(default_factory=list)
    metric_lines: list[MetricLine] = field(default_factory=list)
    missing: list[Missing] = field(default_factory=list)
    tool_calls: list[ToolCall] = field(default_factory=list)

    def as_dict(self) -> dict:
        return asdict(self)


@dataclass
class Unattributed:
    tool: str
    traces: int
    logs: int
    metrics: int
    names: list[str]
    reason: str

    def as_dict(self) -> dict:
        return asdict(self)


@dataclass
class SummaryReport:
    sessions: list[SessionSummary] = field(default_factory=list)
    unattributed: list[Unattributed] = field(default_factory=list)

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
    unit: str | None
    attributes: dict


def summarize(records: list[Record], *, all_spans: bool = False) -> SummaryReport:
    attributed, leftover = _attribute(records)
    sessions = []
    for (tool, session_id), grouped in attributed.items():
        sessions.append(_summarize_bucket(tool, session_id, grouped, all_spans=all_spans))
    sessions.sort(key=lambda item: (item.tool, item.session_id or ""))
    return SummaryReport(sessions=sessions, unattributed=_unattributed(leftover))


def format_summary(report: SummaryReport) -> str:
    if not report.sessions and not report.unattributed:
        return "No telemetry sessions in the store.\n"
    lines: list[str] = []
    for session in report.sessions:
        lines.append(f"session {session.session_id}")
        lines.append(f"  tool: {session.tool}")
        lines.append(f"  service: {_join(session.service_names)}")
        lines.append(f"  model: {_join(session.models)}")
        lines.append(
            f"  signals: traces={session.traces} logs={session.logs} metrics={session.metrics}"
        )
        lines.append(f"  duration_ms: {_field(session.duration_ms, session.duration_source)}")
        lines.append(f"  tokens: {_format_tokens(session)}")
        lines.append(f"  cost_usd: {_format_cost(session)}")
        lines.append("  spans:" if session.spans else "  spans: (none in the default view)")
        for node in session.spans:
            _format_span(lines, node, indent=2)
        if session.spans_hidden:
            noun = "span" if session.spans_hidden == 1 else "spans"
            lines.append(
                f"    ({session.spans_hidden} internal {noun} hidden; rerun with --all-spans)"
            )
        lines.append("  logs:")
        if session.log_events:
            for event in session.log_events:
                detail = f" {event.detail}" if event.detail else ""
                lines.append(f"    - {event.name} x{event.count}{detail}")
        else:
            lines.append("    - (none)")
        lines.append("  metrics:")
        if session.metric_lines:
            for metric in session.metric_lines:
                unit = f" {metric.unit}" if metric.unit else ""
                detail = f" {metric.detail}" if metric.detail else ""
                lines.append(
                    f"    - {metric.name}{detail} {_fmt(metric.value)}{unit} ({metric.source})"
                )
        else:
            lines.append("    - (none)")
        lines.append("  tool_calls:")
        if session.tool_calls:
            for call in session.tool_calls:
                lines.append(f"    - {_format_call(call)}")
        else:
            lines.append("    - (none reported)")
        if session.missing:
            lines.append("  missing:")
            for item in session.missing:
                lines.append(f"    - {item.signal}: {item.reason}")
        else:
            lines.append("  missing: (none)")
    if report.unattributed:
        lines.append("unattributed:")
        for group in report.unattributed:
            lines.append(
                f"  {group.tool} signals traces={group.traces} logs={group.logs} "
                f"metrics={group.metrics}"
            )
            lines.append(f"    reason: {group.reason}")
            for name in group.names:
                lines.append(f"    - {name}")
    return "\n".join(lines) + "\n"


def tool_of(record: Record) -> str:
    service = (record.service_name or "").lower()
    name = record.name or ""
    body = record.body or ""
    origin = str(record.attributes.get("originator") or record.resource.get("originator") or "")
    event = str(record.attributes.get("event.name") or "")
    system = _gen_ai_system(record)
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
    if service == "opencode":
        return "opencode"
    if service == "pi" or system == "pi" or name.startswith("pi.session.") or event.startswith("pi.session."):
        return "pi"
    return "unknown"


def _gen_ai_system(record: Record) -> str:
    for source in (record.attributes, record.resource):
        text = _text(source.get("gen_ai.system"))
        if text:
            return text.lower()
    return ""


def _attribute(
    records: list[Record],
) -> tuple[dict[tuple[str, str], list[Record]], list[Record]]:
    scoped: dict[tuple[str, str], list[Record]] = {}
    unscoped: list[Record] = []
    for record in records:
        if record.session_id:
            scoped.setdefault((tool_of(record), record.session_id), []).append(record)
        else:
            unscoped.append(record)

    trace_keys: dict[str, set[tuple[str, str]]] = {}
    span_keys: dict[str, set[tuple[str, str]]] = {}
    bounds: dict[tuple[str, str], tuple[int, int]] = {}
    for key, grouped in scoped.items():
        for record in grouped:
            if record.trace_id:
                trace_keys.setdefault(record.trace_id, set()).add(key)
            if record.span_id:
                span_keys.setdefault(record.span_id, set()).add(key)
            for stamp in _stamps(record):
                lo, hi = bounds.get(key, (stamp, stamp))
                bounds[key] = (min(lo, stamp), max(hi, stamp))

    leftover: list[Record] = []
    for record in unscoped:
        key = _match_session(record, trace_keys, span_keys, bounds)
        if key is None:
            leftover.append(record)
            continue
        scoped.setdefault(key, []).append(replace(record, session_id=key[1]))
    return scoped, leftover


def _match_session(record, trace_keys, span_keys, bounds):
    for candidate in (record.trace_id,):
        keys = trace_keys.get(candidate or "")
        if keys and len(keys) == 1:
            return next(iter(keys))
    for candidate in (record.span_id, record.parent_span_id):
        keys = span_keys.get(candidate or "")
        if keys and len(keys) == 1:
            return next(iter(keys))
    tool = tool_of(record)
    # OpenCode startup spans and process logs overlap a session in time but
    # are not that session. Pi metrics have no session id and still use the
    # time window below. Pi logs carry pi.session.id and are scoped at ingest.
    if tool == "opencode" and record.signal in {"logs", "traces"}:
        return None
    stamps = _stamps(record)
    if not stamps or tool == "unknown":
        return None
    stamp = stamps[0]
    matches = []
    for key, (lo, hi) in bounds.items():
        if key[0] != tool:
            continue
        if lo - _TIME_SLACK_NS <= stamp <= hi + _TIME_SLACK_NS:
            matches.append(key)
    if len(matches) == 1:
        return matches[0]
    return None


def _unattributed(records: list[Record]) -> list[Unattributed]:
    grouped: dict[str, list[Record]] = {}
    for record in records:
        grouped.setdefault(tool_of(record), []).append(record)
    groups = []
    for tool, items in grouped.items():
        names = [_unattributed_label(record) for record in items]
        names = _unique(names)
        extra = len(names) - 20
        shown = names[:20]
        if extra > 0:
            shown.append(f"({extra} more names)")
        groups.append(
            Unattributed(
                tool=tool,
                traces=sum(record.signal == "traces" for record in items),
                logs=sum(record.signal == "logs" for record in items),
                metrics=sum(record.signal == "metrics" for record in items),
                names=shown,
                reason=(
                    "not a session: no trace or span id shared with exactly one session, "
                    "and the timestamp is not inside exactly one session of this tool"
                ),
            )
        )
    groups.sort(key=lambda item: item.tool)
    return groups


def _summarize_bucket(
    tool: str, session_id: str, records: list[Record], *, all_spans: bool
) -> SessionSummary:
    points = _reduce(_points(records))
    spans, hidden = _span_tree(records, all_spans=all_spans)
    duration_ms, duration_source = _duration(records, points)
    tokens, tokens_source, tokens_method = _tokens(records, points)
    cost_usd, cost_source, cost_note = _cost(records, points, tool)
    failures = _metric_failures(records)
    return SessionSummary(
        session_id=session_id,
        tool=tool,
        service_names=_unique(record.service_name for record in records),
        models=_models(records, points),
        traces=sum(record.signal == "traces" for record in records),
        logs=sum(record.signal == "logs" for record in records),
        metrics=sum(record.signal == "metrics" for record in records),
        duration_ms=duration_ms,
        duration_source=duration_source,
        tokens=tokens,
        tokens_source=tokens_source,
        tokens_method=tokens_method,
        cost_usd=cost_usd,
        cost_source=cost_source,
        cost_note=cost_note,
        spans=spans,
        spans_hidden=hidden,
        log_events=_log_lines(records),
        metric_lines=_metric_lines(points),
        missing=_missing(tool, records, points, failures),
        tool_calls=_tool_calls(records, points),
    )


def _span_tree(records: list[Record], *, all_spans: bool) -> tuple[list[SpanNode], int]:
    spans = [record for record in records if record.signal == "traces"]
    shown = [record for record in spans if all_spans or _interesting_span(record)]
    by_id = {record.span_id: record for record in spans if record.span_id}
    shown_ids = {record.span_id for record in shown if record.span_id}
    logs = {
        call_id: record
        for record in records
        if record.signal == "logs" and record.name in _TOOL_LOGS
        for call_id in [_call_id(record)]
        if call_id
    }
    children: dict[str, list[Record]] = {span_id: [] for span_id in shown_ids}
    roots: list[Record] = []
    for record in shown:
        parent = _shown_parent(record, by_id, shown_ids)
        if parent is None:
            roots.append(record)
        else:
            children.setdefault(parent, []).append(record)
    ordered = sorted(roots, key=_span_sort)

    def build(record: Record) -> SpanNode:
        kids = [build(child) for child in sorted(children.get(record.span_id or "", []), key=_span_sort)]
        return _span_node(record, kids, logs, spans)

    return [build(record) for record in ordered], len(spans) - len(shown)


def _shown_parent(record: Record, by_id: dict[str, Record], shown_ids: set[str]) -> str | None:
    seen: set[str] = set()
    current = record.parent_span_id
    while current and current not in seen:
        seen.add(current)
        if current in shown_ids and current != record.span_id:
            return current
        parent = by_id.get(current)
        if parent is None:
            return None
        current = parent.parent_span_id
    return None


def _interesting_span(record: Record) -> bool:
    name = record.name or ""
    if name in _CLAUDE_SPANS or name in _CODEX_SPANS or name.startswith("claude_code."):
        return True
    if name in _OPENCODE_SPANS or _span_head(name) in _PI_SPAN_HEADS:
        return True
    return _call_id(record) is not None or _tool_name(record) is not None


def _span_head(name: str) -> str:
    return name.split(" ", 1)[0] if name else ""


def _span_node(record: Record, children: list[SpanNode], logs: dict[str, Record], spans: list[Record]) -> SpanNode:
    call_id = _call_id(record)
    tool_name = _tool_name(record)
    success = None
    success_source = None
    wait_ms = wait_source = execution_ms = execution_source = None
    if record.name in {"claude_code.tool", "exec_command"}:
        log = logs.get(call_id) if call_id else None
        success = _text(record.attributes.get("success"))
        success_source = "traces" if success else None
        if log is not None and _text(log.attributes.get("success")):
            success = _text(log.attributes.get("success"))
            success_source = f"logs {log.name}"
        wait_ms, wait_source = _child_duration(record, spans, _WAIT_SPANS)
        execution_ms, execution_source = _child_duration(record, spans, _EXEC_SPANS)
    elif _is_direct_tool_span(record.name or ""):
        success = _reported_success(record)
        if success and success != "unknown":
            success_source = "traces"
    return SpanNode(
        name=record.name or "(unnamed)",
        duration_ms=_span_ms(record),
        call_id=call_id,
        tool_name=tool_name,
        success=success,
        success_source=success_source,
        wait_ms=wait_ms,
        wait_source=wait_source,
        execution_ms=execution_ms,
        execution_source=execution_source,
        children=children,
    )


def _child_duration(record: Record, spans: list[Record], names: set[str]) -> tuple[float | None, str | None]:
    call_id = _call_id(record)
    if record.name not in _TOOL_SPANS:
        return None, None
    for span in spans:
        if span.name not in names:
            continue
        same_call = call_id and _call_id(span) == call_id
        child = record.span_id and span.parent_span_id == record.span_id
        if same_call or child:
            elapsed = _span_ms(span)
            if elapsed is not None:
                return elapsed, f"traces {span.name}"
    return None, None


def _duration(records: list[Record], points: list[_Point]) -> tuple[float | None, str | None]:
    interactions = [
        elapsed
        for record in records
        if record.signal == "traces" and record.name == "claude_code.interaction"
        for elapsed in [_span_ms(record) or _number(record.attributes.get("interaction.duration_ms"))]
        if elapsed is not None
    ]
    if interactions:
        return sum(interactions), "traces claude_code.interaction"
    e2e = [point.value for point in points if point.name == _CODEX_DURATION_METRIC]
    if e2e:
        return sum(e2e), f"metrics {_CODEX_DURATION_METRIC}"
    turns = [
        elapsed
        for record in records
        if record.signal == "traces" and record.name == "run_turn"
        for elapsed in [_span_ms(record)]
        if elapsed is not None
    ]
    if turns:
        return sum(turns), "traces run_turn"
    invoke = _sum_span_ms(records, lambda name: _span_head(name) == "invoke_agent")
    if invoke is not None:
        return invoke, "traces invoke_agent"
    drain = _sum_span_ms(records, lambda name: name == "SessionRunner.drain")
    if drain is not None:
        return drain, "traces SessionRunner.drain"
    active = [point.value for point in points if point.name == _CLAUDE_ACTIVE_METRIC]
    if active:
        return sum(active) * 1000, f"metrics {_CLAUDE_ACTIVE_METRIC}"
    stamps = [stamp for record in records for stamp in _stamps(record)]
    if len(stamps) >= 2:
        return (max(stamps) - min(stamps)) / 1_000_000, "observed_window"
    return None, None


def _tokens(records: list[Record], points: list[_Point]) -> tuple[dict[str, float], str | None, str | None]:
    claude = [point for point in points if point.name == _CLAUDE_TOKEN_METRIC]
    if claude:
        return (
            _sum_by(claude, "type"),
            f"metrics {_CLAUDE_TOKEN_METRIC}",
            "sum of delta points; a cumulative series keeps its latest point",
        )
    codex = [point for point in points if point.name == _CODEX_TOKEN_METRIC]
    if codex:
        return (
            _sum_by(codex, "token_type"),
            f"metrics {_CODEX_TOKEN_METRIC}",
            "sum of delta histogram points; a cumulative series keeps its latest point",
        )
    claude_logs = [record for record in records if record.signal == "logs" and record.name == _CLAUDE_API]
    totals: dict[str, float] = {}
    if any(
        _add_fields(
            totals,
            record,
            ("input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens"),
        )
        for record in claude_logs
    ):
        return totals, f"logs {_CLAUDE_API}", "sum of per-request token attributes"
    events = [
        record
        for record in records
        if record.signal == "logs"
        and record.name == _CODEX_SSE
        and str(record.attributes.get("event.kind")) == "response.completed"
    ]
    if events:
        return _codex_sse_tokens(events)
    opencode = _genai_usage(records, lambda name: name == "SessionStep.attempt", cumulative_prefix=True)
    if opencode is not None:
        totals, method = opencode
        return totals, "traces SessionStep.attempt", method
    pi = _genai_usage(
        records,
        lambda name: _span_head(name) == "chat",
        cumulative_prefix=False,
        only=("input_tokens", "output_tokens"),
    )
    if pi is not None:
        totals, method = pi
        return totals, "traces chat", method
    pi_metrics = [point for point in points if point.name == _PI_TOKEN_METRIC]
    if pi_metrics:
        return (
            _sum_by(pi_metrics, "gen_ai.token.type"),
            f"metrics {_PI_TOKEN_METRIC}",
            "sum of delta points; a cumulative series keeps its latest point",
        )
    return {}, None, None


def _genai_usage(records, match, *, cumulative_prefix: bool, only: tuple[str, ...] | None = None):
    """Aggregate gen_ai.usage.* on the spans ``match`` accepts.

    OpenCode ``SessionStep.attempt`` input and cache counts already include
    the conversation prefix, so those use the max (the same rule as Codex
    ``response.completed``). Output is new and is summed. Pi ``chat`` input
    and output are per turn and additive; they are summed, and they are not
    added again to ``gen_ai.client.token.usage``.
    """
    rows: list[dict[str, float]] = []
    for record in records:
        if record.signal != "traces" or not match(record.name or ""):
            continue
        amounts: dict[str, float] = {}
        for key, value in record.attributes.items():
            if not isinstance(key, str) or not key.startswith("gen_ai.usage."):
                continue
            label = key[len("gen_ai.usage.") :]
            if only is not None and label not in only:
                continue
            amount = _number(value)
            if amount is None:
                continue
            amounts[label] = amount
        if amounts:
            rows.append(amounts)
    if not rows:
        return None
    labels: list[str] = []
    for row in rows:
        for label in row:
            if label not in labels:
                labels.append(label)
    labels.sort(key=lambda label: (_USAGE_ORDER.index(label) if label in _USAGE_ORDER else len(_USAGE_ORDER), label))
    totals: dict[str, float] = {}
    for label in labels:
        values = [row[label] for row in rows if label in row]
        if cumulative_prefix and label in _CUMULATIVE_USAGE:
            totals[label] = max(values)
        else:
            totals[label] = sum(values)
    if cumulative_prefix:
        method = (
            "input and cache counts use the max because each request resends the "
            "conversation prefix; output is summed"
        )
    else:
        method = (
            "sum of per-turn input_tokens and output_tokens; they are additive "
            "and are not added to gen_ai.client.token.usage"
        )
    return totals, method


def _sum_span_ms(records: list[Record], match) -> float | None:
    found = []
    for record in records:
        if record.signal != "traces" or not match(record.name or ""):
            continue
        elapsed = _span_ms(record)
        if elapsed is not None:
            found.append(elapsed)
    if not found:
        return None
    return sum(found)


def _codex_sse_tokens(events: list[Record]) -> tuple[dict[str, float], str, str]:
    """Input counts on response.completed already include the resent prefix.

    Within one turn.id, only the last event is used. Across turns, output and
    reasoning are summed and input, cache read, and cache write use the max.
    Without turn.id, each event is its own request and the same rule applies.
    tool_token_count is ignored: current Codex records total_tokens there.
    """
    grouped: dict[str, list[Record]] = {}
    for index, record in enumerate(events):
        turn = _text(record.attributes.get("turn.id")) or f"request-{index}"
        grouped.setdefault(turn, []).append(record)
    chosen = []
    for group in grouped.values():
        chosen.append(max(group, key=lambda record: (record.time_unix_nano or 0, record.id)))
    totals: dict[str, float] = {}
    for key in _CUMULATIVE_TOKEN_FIELDS:
        values = [amount for record in chosen if (amount := _number(record.attributes.get(key))) is not None]
        if values:
            totals[key] = max(values)
    for key in _ADDITIVE_TOKEN_FIELDS:
        values = [amount for record in chosen if (amount := _number(record.attributes.get(key))) is not None]
        if values:
            totals[key] = sum(values)
    return (
        totals,
        f"logs {_CODEX_SSE} response.completed",
        "input, cached, and cache_write are the max (the prefix is resent); "
        "output and reasoning are summed; last event per turn.id",
    )


def _cost(records, points, tool: str) -> tuple[float | None, str | None, str | None]:
    note = "agent estimate, not the provider price" if tool == "claude-code" else None
    claude = [point.value for point in points if point.name == _CLAUDE_COST_METRIC]
    if claude:
        return sum(claude), f"metrics {_CLAUDE_COST_METRIC}", note
    codex = [point.value for point in points if point.name == _CODEX_COST_METRIC]
    if codex:
        return sum(codex) / 1_000_000, f"metrics {_CODEX_COST_METRIC}", None
    total = 0.0
    found = False
    source = None
    for record in records:
        if record.signal != "logs":
            continue
        if record.name == _CLAUDE_API:
            amount = _number(record.attributes.get("cost_usd"))
            label = f"logs {_CLAUDE_API} cost_usd"
        elif record.name == _CODEX_TURN_COST:
            amount = _number(record.attributes.get("usage.estimated_usd"))
            label = f"logs {_CODEX_TURN_COST} usage.estimated_usd"
            note = None
        else:
            continue
        if amount is None:
            continue
        total += amount
        found = True
        source = source or label
    if found:
        return total, source, note
    pi_total = 0.0
    pi_found = False
    for record in records:
        if record.signal != "traces" or _span_head(record.name or "") != "chat":
            continue
        amount = _number(record.attributes.get("pi.cost.usd"))
        if amount is None:
            continue
        pi_total += amount
        pi_found = True
    if pi_found:
        return pi_total, "traces chat pi.cost.usd", None
    return None, None, None


def _tool_calls(records: list[Record], points: list[_Point]) -> list[ToolCall]:
    spans = [record for record in records if record.signal == "traces"]
    logs = [record for record in records if record.signal == "logs" and record.name in _TOOL_LOGS]
    log_by_id = {call_id: record for record in logs if (call_id := _call_id(record))}
    primary = []
    seen: set[str] = set()
    for record in spans:
        if not _is_direct_tool_span(record.name or ""):
            continue
        call_id = _call_id(record)
        if call_id:
            seen.add(call_id)
        primary.append(record)
    for record in spans:
        if record.name != "unified_exec.exec_command":
            continue
        call_id = _call_id(record)
        if call_id and call_id in seen:
            continue
        if call_id:
            seen.add(call_id)
        primary.append(record)
    calls = [_call_from_span(record, log_by_id.get(_call_id(record) or ""), spans) for record in primary]
    for record in logs:
        call_id = _call_id(record)
        if call_id and call_id in seen:
            continue
        calls.append(
            ToolCall(
                name=_text(record.attributes.get("tool_name")),
                call_id=call_id,
                success=_text(record.attributes.get("success")),
                duration_ms=_number(record.attributes.get("duration_ms")),
                count=None,
                source=f"logs {record.name}",
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
                source="metrics codex.tool.call",
            )
        )
    return calls


def _call_from_span(record: Record, log: Record | None, spans: list[Record]) -> ToolCall:
    wait_ms, wait_source = _child_duration(record, spans, _WAIT_SPANS)
    execution_ms, execution_source = _child_duration(record, spans, _EXEC_SPANS)
    duration = execution_ms if execution_ms is not None else _span_ms(record)
    source = f"traces {record.name}"
    success = _reported_success(record)
    if log is not None:
        source = f"{source}; logs {log.name}"
        if _text(log.attributes.get("success")):
            success = _text(log.attributes.get("success"))
        if duration is None:
            duration = _number(log.attributes.get("duration_ms"))
    return ToolCall(
        name=_tool_name(record) or record.name,
        call_id=_call_id(record),
        success=success,
        duration_ms=duration,
        count=None,
        source=source,
        wait_ms=wait_ms,
        wait_source=wait_source,
        execution_ms=execution_ms,
        execution_source=execution_source,
    )


def _log_lines(records: list[Record]) -> list[LogLine]:
    logs = [record for record in records if record.signal == "logs"]
    failures = [record for record in logs if _METRIC_FAIL.search(_log_text(record))]
    ordinary = [record for record in logs if record not in failures]
    counts: dict[str, list[Record]] = {}
    for record in ordinary:
        counts.setdefault(record.name or "(unnamed)", []).append(record)
    lines = []
    for name, group in counts.items():
        detail = None
        if name == _CODEX_SSE:
            completed = sum(str(record.attributes.get("event.kind")) == "response.completed" for record in group)
            detail = f"(response.completed x{completed})"
        elif name in _TOOL_LOGS and len(group) == 1:
            record = group[0]
            detail = " ".join(
                part
                for part in (
                    _text(record.attributes.get("tool_name")),
                    f"success={_text(record.attributes.get('success'))}"
                    if _text(record.attributes.get("success"))
                    else None,
                )
                if part
            )
        lines.append(LogLine(name=name, count=len(group), detail=detail or None))
    if failures:
        sample = _METRIC_FAIL.search(_log_text(failures[0]))
        model = sample.group(2) if sample else None
        detail = f"(model tag rejected: {model})" if model else "(model tag rejected)"
        lines.append(LogLine(name="codex.metric_export_error", count=len(failures), detail=detail))
    return lines


def _metric_lines(points: list[_Point]) -> list[MetricLine]:
    lines = []
    for point in points:
        detail_parts = []
        for key in (
            "type",
            "token_type",
            "gen_ai.token.type",
            "model",
            "gen_ai.request.model",
            "tool",
            "gen_ai.tool.name",
            "gen_ai.operation.name",
            "decision",
            "success",
        ):
            text = _text(point.attributes.get(key))
            if text:
                detail_parts.append(f"{key}={text}")
        lines.append(
            MetricLine(
                name=point.name,
                value=point.value,
                unit=point.unit,
                detail=" ".join(detail_parts) or None,
                source="metrics",
            )
        )
    return lines


def _metric_failures(records: list[Record]) -> list[tuple[str, str]]:
    found = []
    for record in records:
        if record.signal != "logs":
            continue
        match = _METRIC_FAIL.search(_log_text(record))
        if match:
            found.append((match.group(1), match.group(2)))
    return found


def _missing(tool: str, records: list[Record], points: list[_Point], failures: list[tuple[str, str]]) -> list[Missing]:
    missing = []
    if not any(record.signal == "traces" for record in records):
        if tool == "claude-code":
            reason = "no spans exported. Beta traces need CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1 and OTEL_TRACES_EXPORTER=otlp."
        elif tool == "codex":
            reason = "no spans exported. Set otel.trace_exporter to an otlp-http endpoint ending in /v1/traces."
        elif tool == "opencode":
            reason = "no spans exported. OpenCode v2 exports traces when OTEL_EXPORTER_OTLP_ENDPOINT is set."
        elif tool == "pi":
            reason = (
                "no spans exported. Pi has no built-in OTLP exporter; install pi-otel "
                "and set OTEL_EXPORTER_OTLP_PROTOCOL=http/json."
            )
        else:
            reason = "no spans exported for this session."
        missing.append(Missing("traces", reason))
    if not any(record.signal == "logs" for record in records):
        if tool == "claude-code":
            reason = "no log events exported. Set OTEL_LOGS_EXPORTER=otlp."
        elif tool == "codex":
            reason = "no log events exported. otel.exporter defaults to none."
        elif tool == "opencode":
            reason = (
                "OpenCode v2 does not export per-session logs. Its OTLP logs are process "
                "events without session.id, so none are attributed to this session."
            )
        elif tool == "pi":
            reason = (
                "pi-otel logs only session start and end (pi.session.start, pi.session.end) "
                "and only when PI_OTEL_LOGS=1. None are attributed to this session."
            )
        else:
            reason = "no log events exported for this session."
        missing.append(Missing("logs", reason))
    elif tool == "pi" and all((record.name or "") in _PI_LOGS for record in records if record.signal == "logs"):
        missing.append(
            Missing(
                "log catalog",
                "pi-otel logs only session start and end, not tool results or per-turn events.",
            )
        )
    present = {point.name for point in points}
    failed = {name for name, _model in failures}
    session_failed = [name for name in _SESSION_CODEX_METRICS if name in failed and name not in present]
    if session_failed:
        model = failures[0][1] if failures else "the model id"
        missing.append(
            Missing(
                "session metrics",
                "Codex 0.167.0 records "
                + ", ".join(session_failed)
                + f" only when every metric tag is valid. The model tag {model!r} contains "
                "':', which validate_tag_value rejects, so those instruments were not exported. "
                "A slash is allowed; there is no config key that tags metrics with a different "
                "name than the wire model. Tokens and duration below fall back to logs and spans.",
            )
        )
    if not any(record.signal == "metrics" for record in records) and not session_failed:
        if tool == "claude-code":
            reason = "no metrics exported. Set OTEL_METRICS_EXPORTER=otlp."
        elif tool == "codex":
            reason = (
                "no metrics exported. otel.metrics_exporter defaults to statsig, which does not "
                "send to this receiver."
            )
        elif tool == "opencode":
            reason = "OpenCode v2 does not export OTLP metrics."
        elif tool == "pi":
            reason = (
                "pi-otel metrics have no session id and are off unless PI_OTEL_METRICS=1. "
                "None were attributed by trace, span, or this session's time window."
            )
        else:
            reason = "no metrics exported for this session."
        missing.append(Missing("metrics", reason))
    return missing


def _models(records: list[Record], points: list[_Point]) -> list[str]:
    found: list[str] = []
    for record in records:
        if record.signal == "logs" and record.name in _MODEL_LOGS:
            _add_model(found, record.attributes)
        elif record.signal == "traces" and (
            (record.name or "") in _MODEL_SPANS or _text(record.attributes.get("gen_ai.request.model"))
        ):
            _add_model(found, record.attributes)
    for point in points:
        _add_model(found, point.attributes)
    return found


def _add_model(found: list[str], attributes: dict) -> None:
    for key in ("model", "gen_ai.request.model"):
        text = _text(attributes.get(key))
        if text and text not in found:
            found.append(text)


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
                unit=record.metric_unit,
                attributes=record.attributes,
            )
        )
    return points


def _reduce(points: list[_Point]) -> list[_Point]:
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
        if group[0].kind == "gauge" or group[0].temporality == "cumulative":
            reduced.append(max(group, key=lambda item: (item.time, item.order)))
        else:
            reduced.append(
                replace(
                    group[0],
                    value=sum(item.value for item in group),
                    time=max(item.time for item in group),
                    order=max(item.order for item in group),
                )
            )
    return reduced


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


def _call_id(record: Record) -> str | None:
    return _first(record.attributes, ("call_id", "tool_use_id", "gen_ai.tool.call.id"))


def _tool_name(record: Record) -> str | None:
    return _first(record.attributes, ("tool_name", "gen_ai.tool.name"))


def _is_direct_tool_span(name: str) -> bool:
    return name in {"claude_code.tool", "exec_command", "Tool.execute"} or _span_head(name) == "execute_tool"


def _reported_success(record: Record) -> str | None:
    """Success for a tool span.

    Pi sets ``pi.tool.is_error``. OpenCode ``Tool.execute`` has no success
    attribute, so the result is unknown. Claude and Codex keep their own
    ``success`` attribute (and the log can still override it).
    """
    if "pi.tool.is_error" in record.attributes:
        flag = _boolish(record.attributes.get("pi.tool.is_error"))
        if flag is None:
            return "unknown"
        return "false" if flag else "true"
    text = _text(record.attributes.get("success"))
    if text:
        return text
    name = record.name or ""
    if name == "Tool.execute" or _span_head(name) == "execute_tool":
        return "unknown"
    return None


def _boolish(value) -> bool | None:
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float)):
        return value != 0
    text = _text(value)
    if text is None:
        return None
    lowered = text.lower()
    if lowered in {"true", "1"}:
        return True
    if lowered in {"false", "0"}:
        return False
    return None


def _unattributed_label(record: Record) -> str:
    name = record.name or "(unnamed)"
    if record.signal == "metrics" and record.value is not None:
        return f"{name}={_fmt(record.value)} (metrics)"
    return f"{name} ({record.signal})"


def _log_text(record: Record) -> str:
    return " ".join(part for part in (record.name, record.body) if part)


def _stamps(record: Record) -> list[int]:
    return [
        stamp
        for stamp in (record.start_unix_nano, record.end_unix_nano, record.time_unix_nano)
        if stamp is not None
    ]


def _span_sort(record: Record) -> tuple:
    return (record.start_unix_nano or record.time_unix_nano or 0, record.id)


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
    parts = " ".join(f"{key}={_fmt(value)}" for key, value in session.tokens.items())
    note = session.tokens_source or ""
    if session.tokens_method:
        note = f"{note}; {session.tokens_method}" if note else session.tokens_method
    return f"{parts} ({note})" if note else parts


def _format_cost(session: SessionSummary) -> str:
    if session.cost_usd is None:
        return "(not reported)"
    rendered = _field(session.cost_usd, session.cost_source)
    if session.cost_note:
        return f"{rendered}; {session.cost_note}"
    return rendered


def _format_call(call: ToolCall) -> str:
    parts = [call.name or "(unnamed)"]
    if call.call_id:
        parts.append(f"call_id={call.call_id}")
    if call.success:
        parts.append(f"success={call.success}")
    if call.duration_ms is not None:
        parts.append(f"duration_ms={_fmt(call.duration_ms)}")
    if call.wait_ms is not None:
        parts.append(f"wait_ms={_fmt(call.wait_ms)} ({call.wait_source})")
    if call.execution_ms is not None:
        parts.append(f"execution_ms={_fmt(call.execution_ms)} ({call.execution_source})")
    parts.append(f"source={call.source}")
    return " ".join(parts)


def _format_span(lines: list[str], node: SpanNode, indent: int) -> None:
    pad = "  " * indent
    bits = [node.name]
    if (
        node.tool_name
        and node.tool_name != node.name
        and not node.name.endswith(" " + node.tool_name)
    ):
        bits.append(node.tool_name)
    if node.call_id:
        bits.append(f"call_id={node.call_id}")
    if node.duration_ms is not None:
        bits.append(f"{_fmt(node.duration_ms)}ms")
    if node.success:
        if node.success_source:
            bits.append(f"success={node.success} ({node.success_source})")
        else:
            bits.append(f"success={node.success}")
    if node.wait_ms is not None:
        bits.append(f"wait={_fmt(node.wait_ms)}ms ({node.wait_source})")
    if node.execution_ms is not None:
        bits.append(f"execution={_fmt(node.execution_ms)}ms ({node.execution_source})")
    lines.append(pad + "- " + " ".join(bits))
    for child in node.children:
        _format_span(lines, child, indent + 1)


def _fmt(value: float) -> str:
    if abs(value - round(value)) < 1e-9:
        return str(int(round(value)))
    return f"{value:.6g}"
