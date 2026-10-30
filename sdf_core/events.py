"""The normalized SDF event contract (issue #57, ADR 0029).

Every row in ``runtime_events`` is one normalized event. This module states
what an event may claim and how consumers read the stream:

* **Correlation.** Each event carries ``attempt_id``, ``session_id``,
  ``source`` and a per-stream ``sequence``. A *stream* is one
  ``(source, attempt_id, session_id)``; its identity key is unique in the
  database, so replaying an event is a no-op and reusing an identity with
  different data is a conflict.
* **Provenance.** The ``source`` names the producer. Kinds beginning with
  ``tool_`` are *action-boundary* events: only the Tool Proxy
  (``TRUSTED_ACTION_SOURCES``) may emit them, and the Tool Proxy emits
  nothing else. All other kinds are *runtime observations* from an Agent
  Runtime. The rule is checked when an event is built, again when it is
  written, and by a database CHECK constraint, so text an agent prints (or a
  runtime adapter forwards) cannot become a trusted ``tool_requested`` or
  ``tool_policy_decided`` event.
* **Not acceptance evidence.** No event certifies an Attempt's outcome.
  A runtime ``completed`` or exit observation, or an executed tool action,
  is telemetry; only Evaluator Evidence decides PASS/FAIL (ADR 0002).
  Normalized views therefore carry ``acceptance_evidence: false``.
* **Ordering.** Within a stream, ``sequence`` is authoritative. Across
  streams, events are merged by the time SDF persisted them, then stream key,
  then sequence; a stream's own order is never broken by the merge. An event
  that arrives ahead of an earlier sequence is stored as delivered and the
  missing numbers are reported as ``gaps`` until they arrive; nothing is
  synthesised to fill them.
"""

from __future__ import annotations

from collections import Counter
from dataclasses import dataclass
from datetime import datetime, timezone
from enum import StrEnum
from typing import Any, Iterable, Sequence

from sqlalchemy import select
from sqlalchemy.orm import Session

from .db import RuntimeEventRow

ACTION_BOUNDARY_PREFIX = "tool_"
TOOL_PROXY_SOURCE = "tool-proxy"
TRUSTED_ACTION_SOURCES = frozenset({TOOL_PROXY_SOURCE})

# Lifecycle kinds a runtime controller records (mirrors runtime.RuntimeEventKind).
RUNTIME_LIFECYCLE_KINDS = frozenset({
    "runtime_started",
    "runtime_input_sent",
    "runtime_output_observed",
    "runtime_cancelled",
    "runtime_terminated",
    "runtime_reconnected",
})
# Lifecycle kinds that end a runtime session.
TERMINAL_RUNTIME_KINDS = frozenset({"runtime_cancelled", "runtime_terminated"})

_EPOCH = datetime(1970, 1, 1, tzinfo=timezone.utc)


class EventCategory(StrEnum):
    RUNTIME_OBSERVATION = "runtime_observation"
    ACTION_BOUNDARY = "action_boundary"


class UntrustedEventError(ValueError):
    """An event claims a provenance its producer is not allowed to claim."""


def _kind_value(kind: Any) -> str:
    return kind.value if isinstance(kind, StrEnum) else str(kind)


def category_of(kind: Any) -> EventCategory:
    """Classify an event kind. ``tool_*`` is reserved for the action boundary."""

    return (EventCategory.ACTION_BOUNDARY if _kind_value(kind).startswith(ACTION_BOUNDARY_PREFIX)
            else EventCategory.RUNTIME_OBSERVATION)


def validate_provenance(source: str, kind: Any) -> EventCategory:
    """Raise :class:`UntrustedEventError` unless ``source`` may emit ``kind``."""

    category = category_of(kind)
    trusted = source in TRUSTED_ACTION_SOURCES
    if category is EventCategory.ACTION_BOUNDARY and not trusted:
        raise UntrustedEventError(
            f"{_kind_value(kind)!r} is an action-boundary event; only {sorted(TRUSTED_ACTION_SOURCES)} "
            f"may emit it, not {source!r}"
        )
    if trusted and category is not EventCategory.ACTION_BOUNDARY:
        raise UntrustedEventError(
            f"source {source!r} is reserved for action-boundary (tool_*) events, not {_kind_value(kind)!r}"
        )
    return category


def validate_runtime_source(source: str) -> None:
    """A runtime controller may not borrow the trusted action-boundary source."""

    if source in TRUSTED_ACTION_SOURCES:
        raise UntrustedEventError(f"source {source!r} is reserved for the Tool Proxy")


@dataclass(frozen=True, slots=True)
class StreamSummary:
    source: str
    attempt_id: str
    session_id: str
    category: str
    count: int
    first_sequence: int
    last_sequence: int
    gaps: tuple[int, ...]
    last_kind: str
    last_status: str

    def as_dict(self) -> dict[str, Any]:
        return {
            "source": self.source, "attempt_id": self.attempt_id, "session_id": self.session_id,
            "category": self.category, "count": self.count, "first_sequence": self.first_sequence,
            "last_sequence": self.last_sequence, "gaps": list(self.gaps), "last_kind": self.last_kind,
            "last_status": self.last_status,
        }


def _at(row: RuntimeEventRow) -> datetime:
    value = row.created_at
    if value is None:
        return _EPOCH
    return value if value.tzinfo else value.replace(tzinfo=timezone.utc)


def stream_key(row: RuntimeEventRow) -> tuple[str, str, str]:
    return (row.source, row.attempt_id, row.session_id)


def order_events(rows: Iterable[RuntimeEventRow]) -> list[RuntimeEventRow]:
    """Deterministic merge: per-stream ``sequence`` order is never broken.

    Streams are merged by the head event's persisted time, then stream key,
    then sequence, then row id, so the same rows always give the same order.
    """

    streams: dict[tuple[str, str, str], list[RuntimeEventRow]] = {}
    for row in rows:
        streams.setdefault(stream_key(row), []).append(row)
    for items in streams.values():
        items.sort(key=lambda r: (r.sequence, r.id or 0))
    heads = {key: 0 for key in streams}
    ordered: list[RuntimeEventRow] = []
    while heads:
        key = min(heads, key=lambda k: (_at(streams[k][heads[k]]), k, streams[k][heads[k]].sequence))
        ordered.append(streams[key][heads[key]])
        heads[key] += 1
        if heads[key] == len(streams[key]):
            del heads[key]
    return ordered


def normalized(row: RuntimeEventRow) -> dict[str, Any]:
    """The public, provenance-bearing shape of one stored event."""

    category = category_of(row.kind)
    return {
        "source": row.source,
        "attempt_id": row.attempt_id,
        "session_id": row.session_id,
        "sequence": row.sequence,
        "kind": row.kind,
        "status": row.status,
        "payload": row.payload,
        "created_at": row.created_at,
        "category": category.value,
        "trusted_boundary": row.source in TRUSTED_ACTION_SOURCES and category is EventCategory.ACTION_BOUNDARY,
        "acceptance_evidence": False,
    }


def events_for_attempts(db: Session, attempt_ids: Sequence[str]) -> list[RuntimeEventRow]:
    if not attempt_ids:
        return []
    rows = db.scalars(select(RuntimeEventRow).where(RuntimeEventRow.attempt_id.in_(list(attempt_ids)))).all()
    return order_events(rows)


def summarize(rows: Iterable[RuntimeEventRow]) -> list[StreamSummary]:
    """One summary per stream: counts, sequence range, gaps and last state."""

    streams: dict[tuple[str, str, str], list[RuntimeEventRow]] = {}
    for row in rows:
        streams.setdefault(stream_key(row), []).append(row)
    out: list[StreamSummary] = []
    for key in sorted(streams):
        items = sorted(streams[key], key=lambda r: r.sequence)
        sequences = [r.sequence for r in items]
        present = set(sequences)
        gaps = tuple(n for n in range(1, sequences[-1]) if n not in present)
        last = items[-1]
        out.append(StreamSummary(
            source=key[0], attempt_id=key[1], session_id=key[2], category=category_of(last.kind).value,
            count=len(items), first_sequence=sequences[0], last_sequence=sequences[-1], gaps=gaps,
            last_kind=last.kind, last_status=last.status,
        ))
    return out


def kind_counts(rows: Iterable[RuntimeEventRow]) -> dict[str, int]:
    return dict(sorted(Counter(r.kind for r in rows).items()))
