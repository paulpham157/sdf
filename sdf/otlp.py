"""Decode OTLP/HTTP JSON and protobuf into flat records.

Both Codex and Claude Code can post ``application/x-protobuf`` or
``application/json``. Decoding goes through the official protobuf messages
so the two encodings share one shape.
"""

from __future__ import annotations

import base64
import binascii

from google.protobuf.json_format import MessageToDict, Parse, ParseError
from google.protobuf.message import DecodeError
from opentelemetry.proto.collector.logs.v1.logs_service_pb2 import (
    ExportLogsServiceRequest,
)
from opentelemetry.proto.collector.metrics.v1.metrics_service_pb2 import (
    ExportMetricsServiceRequest,
)
from opentelemetry.proto.collector.trace.v1.trace_service_pb2 import (
    ExportTraceServiceRequest,
)

from sdf.records import Record

_REQUESTS = {
    "traces": ExportTraceServiceRequest,
    "metrics": ExportMetricsServiceRequest,
    "logs": ExportLogsServiceRequest,
}

MAX_BODY_BYTES = 8_000_000


class OtlpError(ValueError):
    """The HTTP body was not an OTLP export request."""


def decode_export(signal: str, body: bytes, content_type: str | None) -> list[Record]:
    if signal not in _REQUESTS:
        raise OtlpError(f"unknown signal {signal}")
    if len(body) > MAX_BODY_BYTES:
        raise OtlpError("payload too large")
    message = _REQUESTS[signal]()
    media = (content_type or "").split(";", 1)[0].strip().lower()
    try:
        if media == "application/json":
            Parse(body.decode("utf-8"), message, ignore_unknown_fields=True)
        else:
            message.ParseFromString(body)
    except (DecodeError, ParseError, UnicodeDecodeError, ValueError) as exc:
        raise OtlpError("could not parse OTLP payload") from exc
    payload = MessageToDict(message, preserving_proto_field_name=True)
    return normalize(signal, payload)


def normalize(signal: str, payload: dict) -> list[Record]:
    if signal == "traces":
        return _traces(payload)
    if signal == "metrics":
        return _metrics(payload)
    if signal == "logs":
        return _logs(payload)
    raise OtlpError(f"unknown signal {signal}")


def _traces(payload: dict) -> list[Record]:
    records: list[Record] = []
    for resource_spans in payload.get("resource_spans") or []:
        resource = _attributes(resource_spans.get("resource"))
        for scope_spans in resource_spans.get("scope_spans") or []:
            for span in scope_spans.get("spans") or []:
                attributes = _attribute_list(span.get("attributes"))
                records.append(
                    _record(
                        signal="traces",
                        resource=resource,
                        attributes=attributes,
                        name=span.get("name") or None,
                        start=_nano(span.get("start_time_unix_nano")),
                        end=_nano(span.get("end_time_unix_nano")),
                        body=None,
                        trace_id=_otel_id(span.get("trace_id")),
                        span_id=_otel_id(span.get("span_id")),
                        parent_span_id=_otel_id(span.get("parent_span_id")),
                    )
                )
    return records


def _metrics(payload: dict) -> list[Record]:
    records: list[Record] = []
    for resource_metrics in payload.get("resource_metrics") or []:
        resource = _attributes(resource_metrics.get("resource"))
        for scope_metrics in resource_metrics.get("scope_metrics") or []:
            for metric in scope_metrics.get("metrics") or []:
                name = metric.get("name") or None
                unit = metric.get("unit") or None
                for kind in ("sum", "gauge", "histogram"):
                    block = metric.get(kind)
                    if not isinstance(block, dict):
                        continue
                    temporality = _temporality(block.get("aggregation_temporality"))
                    for point in block.get("data_points") or []:
                        records.append(
                            _record(
                                signal="metrics",
                                resource=resource,
                                attributes=_attribute_list(point.get("attributes")),
                                name=name,
                                start=_nano(point.get("start_time_unix_nano")),
                                end=None,
                                when=_nano(point.get("time_unix_nano")),
                                value=_metric_value(kind, point),
                                unit=unit,
                                temporality=temporality,
                                kind=kind,
                            )
                        )
    return records


def _logs(payload: dict) -> list[Record]:
    records: list[Record] = []
    for resource_logs in payload.get("resource_logs") or []:
        resource = _attributes(resource_logs.get("resource"))
        for scope_logs in resource_logs.get("scope_logs") or []:
            for record in scope_logs.get("log_records") or []:
                attributes = _attribute_list(record.get("attributes"))
                body = _body(record.get("body"))
                name = _event_name(body, attributes, resource)
                records.append(
                    _record(
                        signal="logs",
                        resource=resource,
                        attributes=attributes,
                        name=name,
                        start=None,
                        end=None,
                        when=_nano(record.get("time_unix_nano")),
                        body=body,
                        trace_id=_otel_id(record.get("trace_id")),
                        span_id=_otel_id(record.get("span_id")),
                    )
                )
    return records


def _record(
    *,
    signal: str,
    resource: dict,
    attributes: dict,
    name: str | None,
    start: int | None,
    end: int | None,
    body: str | None = None,
    when: int | None = None,
    value: float | None = None,
    unit: str | None = None,
    temporality: str | None = None,
    kind: str | None = None,
    trace_id: str | None = None,
    span_id: str | None = None,
    parent_span_id: str | None = None,
) -> Record:
    service = _text(resource.get("service.name"))
    return Record(
        signal=signal,
        service_name=service,
        session_id=_session_id(attributes, resource),
        name=name,
        start_unix_nano=start,
        end_unix_nano=end,
        time_unix_nano=when,
        value=value,
        metric_unit=unit,
        metric_temporality=temporality,
        metric_kind=kind,
        attributes=attributes,
        resource=resource,
        body=body,
        trace_id=trace_id,
        span_id=span_id,
        parent_span_id=parent_span_id,
    )


def _attributes(resource: dict | None) -> dict:
    if not isinstance(resource, dict):
        return {}
    return _attribute_list(resource.get("attributes"))


def _attribute_list(items) -> dict:
    parsed: dict = {}
    for item in items or []:
        if not isinstance(item, dict) or "key" not in item:
            continue
        parsed[str(item["key"])] = _any_value(item.get("value"))
    return parsed


def _any_value(value):
    if not isinstance(value, dict):
        return value
    if "string_value" in value:
        return value["string_value"]
    if "int_value" in value:
        return _int_or_raw(value["int_value"])
    if "double_value" in value:
        return float(value["double_value"])
    if "bool_value" in value:
        return bool(value["bool_value"])
    if "array_value" in value:
        values = (value.get("array_value") or {}).get("values") or []
        return [_any_value(item) for item in values]
    if "kvlist_value" in value:
        values = (value.get("kvlist_value") or {}).get("values") or []
        return _attribute_list(values)
    if "bytes_value" in value:
        return value["bytes_value"]
    return None


def _body(value) -> str | None:
    parsed = _any_value(value)
    if parsed is None:
        return None
    if isinstance(parsed, str):
        return parsed
    return str(parsed)


def _event_name(body: str | None, attributes: dict, resource: dict) -> str | None:
    attr = _text(attributes.get("event.name"))
    if body and body.startswith(("claude_code.", "codex.")):
        return body
    if attr and attr.startswith(("claude_code.", "codex.")):
        return attr
    service = _text(resource.get("service.name")) or ""
    if attr and service.startswith("claude-code") and "." not in attr:
        return f"claude_code.{attr}"
    return attr or body


def _session_id(attributes: dict, resource: dict) -> str | None:
    for source in (attributes, resource):
        for key in ("session.id", "conversation.id"):
            text = _text(source.get(key))
            if text:
                return text
    return None


def _metric_value(kind: str, point: dict) -> float | None:
    if kind == "histogram":
        return _number(point.get("sum"))
    if "as_int" in point:
        return _number(point.get("as_int"))
    if "as_double" in point:
        return _number(point.get("as_double"))
    return None


def _temporality(value) -> str:
    text = str(value or "").upper()
    if "DELTA" in text or text == "1":
        return "delta"
    if "CUMULATIVE" in text or text == "2":
        return "cumulative"
    return "unspecified"


def _nano(value) -> int | None:
    if value is None or value == "":
        return None
    try:
        return int(value)
    except (TypeError, ValueError):
        return None


def _number(value) -> float | None:
    if isinstance(value, bool) or value is None or value == "":
        return None
    if isinstance(value, (int, float)):
        return float(value)
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def _int_or_raw(value):
    if isinstance(value, bool):
        return value
    try:
        return int(value)
    except (TypeError, ValueError):
        return value


def _otel_id(value) -> str | None:
    """Normalize an OTLP trace or span id to lowercase hex.

    Protobuf JSON encodes these bytes as base64. A 16 or 32 character hex
    string is kept as hex so it is not mistaken for base64.
    """
    if not isinstance(value, str):
        return None
    text = value.strip()
    if not text:
        return None
    if len(text) in {16, 32} and all(char in "0123456789abcdefABCDEF" for char in text):
        return text.lower()
    try:
        raw = base64.b64decode(text, validate=True)
    except (ValueError, binascii.Error):
        return None
    if not raw:
        return None
    return raw.hex()


def _text(value) -> str | None:
    if value is None:
        return None
    text = str(value).strip()
    return text or None
