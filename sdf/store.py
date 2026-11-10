from __future__ import annotations

import json
import sqlite3
import threading
from datetime import datetime, timezone

from sdf.records import Record

_SCHEMA = """
CREATE TABLE IF NOT EXISTS records (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    received_at TEXT NOT NULL,
    signal TEXT NOT NULL,
    service_name TEXT,
    session_id TEXT,
    name TEXT,
    start_unix_nano INTEGER,
    end_unix_nano INTEGER,
    time_unix_nano INTEGER,
    value REAL,
    metric_unit TEXT,
    metric_temporality TEXT,
    metric_kind TEXT,
    attributes_json TEXT NOT NULL,
    resource_json TEXT NOT NULL,
    body TEXT,
    trace_id TEXT,
    span_id TEXT,
    parent_span_id TEXT
);
CREATE INDEX IF NOT EXISTS idx_records_session ON records(session_id);
CREATE INDEX IF NOT EXISTS idx_records_signal_name ON records(signal, name);
"""


class Store:
    """SQLite file of flattened OTLP records."""

    def __init__(self, path: str):
        self.path = path
        self._lock = threading.Lock()
        self._conn = sqlite3.connect(path, check_same_thread=False)
        self._conn.row_factory = sqlite3.Row
        self._conn.executescript(_SCHEMA)
        self._migrate()
        self._conn.commit()

    def _migrate(self) -> None:
        present = {row[1] for row in self._conn.execute("PRAGMA table_info(records)")}
        if "trace_id" not in present:
            self._conn.execute("ALTER TABLE records ADD COLUMN trace_id TEXT")
        if "span_id" not in present:
            self._conn.execute("ALTER TABLE records ADD COLUMN span_id TEXT")
        if "parent_span_id" not in present:
            self._conn.execute("ALTER TABLE records ADD COLUMN parent_span_id TEXT")

    def close(self) -> None:
        with self._lock:
            self._conn.close()

    def insert(self, records: list[Record]) -> int:
        if not records:
            return 0
        received_at = datetime.now(timezone.utc).isoformat()
        rows = [
            (
                received_at,
                record.signal,
                record.service_name,
                record.session_id,
                record.name,
                record.start_unix_nano,
                record.end_unix_nano,
                record.time_unix_nano,
                record.value,
                record.metric_unit,
                record.metric_temporality,
                record.metric_kind,
                json.dumps(record.attributes, sort_keys=True),
                json.dumps(record.resource, sort_keys=True),
                record.body,
                record.trace_id,
                record.span_id,
                record.parent_span_id,
            )
            for record in records
        ]
        with self._lock:
            self._conn.executemany(
                """
                INSERT INTO records (
                    received_at, signal, service_name, session_id, name,
                    start_unix_nano, end_unix_nano, time_unix_nano, value,
                    metric_unit, metric_temporality, metric_kind,
                    attributes_json, resource_json, body,
                    trace_id, span_id, parent_span_id
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                rows,
            )
            self._conn.commit()
        return len(rows)

    def load(self) -> list[Record]:
        with self._lock:
            cursor = self._conn.execute(
                """
                SELECT id, signal, service_name, session_id, name,
                       start_unix_nano, end_unix_nano, time_unix_nano, value,
                       metric_unit, metric_temporality, metric_kind,
                       attributes_json, resource_json, body,
                       trace_id, span_id, parent_span_id
                FROM records
                ORDER BY id
                """
            )
            rows = cursor.fetchall()
        return [_row_to_record(row) for row in rows]


def _row_to_record(row: sqlite3.Row) -> Record:
    return Record(
        id=row["id"],
        signal=row["signal"],
        service_name=row["service_name"],
        session_id=row["session_id"],
        name=row["name"],
        start_unix_nano=row["start_unix_nano"],
        end_unix_nano=row["end_unix_nano"],
        time_unix_nano=row["time_unix_nano"],
        value=row["value"],
        metric_unit=row["metric_unit"],
        metric_temporality=row["metric_temporality"],
        metric_kind=row["metric_kind"],
        attributes=json.loads(row["attributes_json"]),
        resource=json.loads(row["resource_json"]),
        body=row["body"],
        trace_id=row["trace_id"],
        span_id=row["span_id"],
        parent_span_id=row["parent_span_id"],
    )
