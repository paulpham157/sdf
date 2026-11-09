from __future__ import annotations

from dataclasses import dataclass, field


@dataclass
class Record:
    """One stored span, metric point, or log record."""

    signal: str
    service_name: str | None = None
    session_id: str | None = None
    name: str | None = None
    start_unix_nano: int | None = None
    end_unix_nano: int | None = None
    time_unix_nano: int | None = None
    value: float | None = None
    metric_unit: str | None = None
    metric_temporality: str | None = None
    metric_kind: str | None = None
    attributes: dict = field(default_factory=dict)
    resource: dict = field(default_factory=dict)
    body: str | None = None
    id: int = 0
