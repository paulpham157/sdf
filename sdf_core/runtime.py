"""Small internal agent-runtime seam; real Herdr remains an unverified adapter."""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import Any, Iterable, Protocol

from sqlalchemy import select
from sqlalchemy.dialects.postgresql import insert as postgres_insert
from sqlalchemy.dialects.sqlite import insert as sqlite_insert
from sqlalchemy.orm import Session

from .credentials import CredentialMode
from .db import RuntimeEventRow
from .model import utcnow


class RuntimeStatus(StrEnum):
    CREATED = "created"
    RUNNING = "running"
    COMPLETED = "completed"
    CANCELLED = "cancelled"
    TERMINATED = "terminated"


class RuntimeEventKind(StrEnum):
    """Trusted lifecycle observations emitted by the runtime boundary."""

    STARTED = "runtime_started"
    INPUT_SENT = "runtime_input_sent"
    OUTPUT_OBSERVED = "runtime_output_observed"
    CANCELLED = "runtime_cancelled"
    TERMINATED = "runtime_terminated"
    RECONNECTED = "runtime_reconnected"
    TOOL_POLICY_DECIDED = "tool_policy_decided"
    TOOL_ACTION_EXECUTED = "tool_action_executed"
    TOOL_ACTION_FAILED = "tool_action_failed"


class RuntimeEventConflictError(ValueError):
    """Raised when a replay reuses an event identity with different data."""


@dataclass(frozen=True, slots=True)
class RuntimeEvent:
    """One attempt-correlated observation from an AgentRuntime."""

    kind: RuntimeEventKind
    session_id: str
    attempt_id: str
    sequence: int
    status: RuntimeStatus
    payload: dict[str, Any]
    source: str = "runtime"

    def __post_init__(self) -> None:
        if not isinstance(self.source, str) or not self.source.strip():
            raise ValueError("runtime event source must be a non-empty string")
        if len(self.source) > 120:
            raise ValueError("runtime event source exceeds 120 characters")
        if not isinstance(self.attempt_id, str) or not self.attempt_id.strip():
            raise ValueError("runtime event attempt_id must be a non-empty string")
        if not isinstance(self.session_id, str) or not self.session_id.strip():
            raise ValueError("runtime event session_id must be a non-empty string")
        if not isinstance(self.sequence, int) or self.sequence < 1:
            raise ValueError("runtime event sequence must be a positive integer")
        if not isinstance(self.payload, dict):
            raise TypeError("runtime event payload must be a dictionary")

    @property
    def identity(self) -> tuple[str, str, str, int]:
        """Return the durable replay key for this observation."""

        return (self.source, self.attempt_id, self.session_id, self.sequence)


class RuntimeEventSink(Protocol):
    def append(self, event: RuntimeEvent) -> None: ...


@dataclass(frozen=True, slots=True)
class CredentialMetadata:
    """Secret-free record of how a Runtime Session's agent was credentialed."""

    credential_mode: str
    connection_id: str | None = None

    def __post_init__(self) -> None:
        if self.credential_mode not in {mode.value for mode in CredentialMode}:
            # Not echoed: a misplaced secret must not reach an error message.
            raise ValueError("credential_mode is not a known Credential Mode")
        if self.connection_id is not None and (
            not isinstance(self.connection_id, str) or not self.connection_id.strip()
        ):
            raise ValueError("connection_id must be a non-empty string or None")

    def as_dict(self) -> dict[str, str | None]:
        return {"credential_mode": self.credential_mode, "connection_id": self.connection_id}

@dataclass(frozen=True, slots=True)
class RuntimeSession:
    session_id: str
    attempt_id: str
    agent: str
    status: RuntimeStatus
    output: tuple[str, ...] = ()
    credential: CredentialMetadata | None = None


class AgentRuntime(Protocol):
    def start(self, *, attempt_id: str, agent: str) -> RuntimeSession: ...
    def send(self, session_id: str, input_text: str) -> RuntimeSession: ...
    def stream(self, session_id: str) -> tuple[str, ...]: ...
    def status(self, session_id: str) -> RuntimeSession: ...
    def cancel(self, session_id: str) -> RuntimeSession: ...
    def terminate(self, session_id: str) -> RuntimeSession: ...
    def reconnect(self, session_id: str) -> RuntimeSession: ...


class FakeRuntime:
    """Deterministic runtime for local tests; no provider or process claims."""

    def __init__(self) -> None:
        self._sessions: dict[str, RuntimeSession] = {}
        self._attempts: dict[str, str] = {}
        self._counter = 0

    def start(self, *, attempt_id: str, agent: str) -> RuntimeSession:
        if attempt_id in self._attempts:
            return self._sessions[self._attempts[attempt_id]]
        self._counter += 1
        session = RuntimeSession(f"SESSION-{self._counter:04d}", attempt_id, agent, RuntimeStatus.RUNNING)
        self._sessions[session.session_id] = session
        self._attempts[attempt_id] = session.session_id
        return session

    def _get(self, session_id: str) -> RuntimeSession:
        try:
            return self._sessions[session_id]
        except KeyError as exc:
            raise KeyError(f"unknown runtime session: {session_id}") from exc

    def send(self, session_id: str, input_text: str) -> RuntimeSession:
        current = self._get(session_id)
        if current.status is not RuntimeStatus.RUNNING:
            return current
        updated = RuntimeSession(current.session_id, current.attempt_id, current.agent, current.status, current.output + (input_text,))
        self._sessions[session_id] = updated
        return updated

    def stream(self, session_id: str) -> tuple[str, ...]:
        return self._get(session_id).output

    def status(self, session_id: str) -> RuntimeSession:
        return self._get(session_id)

    def cancel(self, session_id: str) -> RuntimeSession:
        return self._set_status(session_id, RuntimeStatus.CANCELLED)

    def terminate(self, session_id: str) -> RuntimeSession:
        return self._set_status(session_id, RuntimeStatus.TERMINATED)

    def reconnect(self, session_id: str) -> RuntimeSession:
        return self._get(session_id)

    def _set_status(self, session_id: str, status: RuntimeStatus) -> RuntimeSession:
        current = self._get(session_id)
        updated = RuntimeSession(current.session_id, current.attempt_id, current.agent, status, current.output)
        self._sessions[session_id] = updated
        return updated


class RuntimeController:
    """Bind an AgentRuntime session to one Attempt and normalize observations.

    This controller is provider-neutral: the fake runtime is fully testable,
    while a future Herdr adapter can implement the same AgentRuntime seam
    without inventing a new event vocabulary.
    """

    def __init__(
        self,
        runtime: AgentRuntime,
        *,
        event_sink: RuntimeEventSink | None = None,
        source: str = "runtime",
    ):
        if not isinstance(source, str) or not source.strip():
            raise ValueError("runtime event source must be a non-empty string")
        if len(source) > 120:
            raise ValueError("runtime event source exceeds 120 characters")
        self.runtime = runtime
        self.event_sink = event_sink
        self.source = source
        self._attempts: dict[str, str] = {}
        self._attempt_sessions: dict[str, str] = {}
        self._sequences: dict[str, int] = {}
        self._events: list[RuntimeEvent] = []
        self._event_keys: set[tuple[str, str, str, int]] = set()

    @property
    def events(self) -> tuple[RuntimeEvent, ...]:
        return tuple(self._events)

    def _session(self, session_id: str) -> RuntimeSession:
        if session_id not in self._attempts:
            raise ValueError(f"session is not registered: {session_id}")
        return self.runtime.status(session_id)

    def _record(self, kind: RuntimeEventKind, session: RuntimeSession, payload: dict[str, Any] | None = None) -> None:
        sequence = self._sequences.get(session.attempt_id, 0) + 1
        event = RuntimeEvent(
            kind,
            session.session_id,
            session.attempt_id,
            sequence,
            session.status,
            payload or {},
            source=self.source,
        )
        if self.event_sink is not None:
            self.event_sink.append(event)
        self._sequences[session.attempt_id] = sequence
        self._event_keys.add(event.identity)
        self._events.append(event)

    def start(self, *, attempt_id: str, agent: str) -> RuntimeSession:
        session = self.runtime.start(attempt_id=attempt_id, agent=agent)
        if session.attempt_id != attempt_id:
            raise ValueError("runtime session is not bound to the requested attempt")
        previous_session_id = self._attempt_sessions.get(attempt_id)
        if previous_session_id is not None and previous_session_id != session.session_id:
            raise ValueError("attempt is already bound to a different runtime session")
        self._attempts[session.session_id] = attempt_id
        self._attempt_sessions[attempt_id] = session.session_id
        if previous_session_id is None:
            payload = session.credential.as_dict() if session.credential is not None else None
            self._record(RuntimeEventKind.STARTED, session, payload)
        return session

    def send(self, session_id: str, input_text: str) -> RuntimeSession:
        current = self._session(session_id)
        updated = self.runtime.send(session_id, input_text)
        self._record(RuntimeEventKind.INPUT_SENT, updated, {"input": input_text})
        return updated

    def stream(self, session_id: str) -> tuple[str, ...]:
        current = self._session(session_id)
        output = self.runtime.stream(session_id)
        self._record(RuntimeEventKind.OUTPUT_OBSERVED, current, {"output": output})
        return output

    def status(self, session_id: str) -> RuntimeSession:
        """Read the provider status without emitting a duplicate lifecycle event."""

        return self._session(session_id)

    def cancel(self, session_id: str) -> RuntimeSession:
        self._session(session_id)
        updated = self.runtime.cancel(session_id)
        self._record(RuntimeEventKind.CANCELLED, updated)
        return updated

    def terminate(self, session_id: str) -> RuntimeSession:
        self._session(session_id)
        updated = self.runtime.terminate(session_id)
        self._record(RuntimeEventKind.TERMINATED, updated)
        return updated

    def reconnect(self, session_id: str) -> RuntimeSession:
        self._session(session_id)
        updated = self.runtime.reconnect(session_id)
        self._record(RuntimeEventKind.RECONNECTED, updated)
        return updated

    def bind_workspace(self, attempt_id: str, workspace: Any) -> None:
        """Forward workspace binding to runtimes that own a remote copy."""

        bind_workspace = getattr(self.runtime, "bind_workspace", None)
        if callable(bind_workspace):
            bind_workspace(attempt_id, workspace)

    def collect_workspace(self, attempt_id: str, workspace: Any) -> None:
        """Forward workspace collection to runtimes that own a remote copy."""

        collect_workspace = getattr(self.runtime, "collect_workspace", None)
        if callable(collect_workspace):
            collect_workspace(attempt_id, workspace)

    def replay(self, events: Iterable[RuntimeEvent]) -> tuple[RuntimeEvent, ...]:
        """Restore controller observations without dispatching the runtime.

        Reconnect/restart recovery must apply trusted lifecycle observations,
        not call ``start``/``send`` again.  The durable sink performs the same
        identity check, so replaying an already persisted batch is harmless.
        """

        accepted: list[RuntimeEvent] = []
        for event in events:
            if not isinstance(event, RuntimeEvent):
                raise TypeError("runtime replay requires RuntimeEvent values")
            if event.identity in self._event_keys:
                continue
            bound_attempt = self._attempts.get(event.session_id)
            if bound_attempt is not None and bound_attempt != event.attempt_id:
                raise ValueError("runtime session is already bound to a different attempt")
            bound_session = self._attempt_sessions.get(event.attempt_id)
            if bound_session is not None and bound_session != event.session_id:
                raise ValueError("attempt is already bound to a different runtime session")
            if self.event_sink is not None:
                self.event_sink.append(event)
            self._event_keys.add(event.identity)
            self._events.append(event)
            self._sequences[event.attempt_id] = max(self._sequences.get(event.attempt_id, 0), event.sequence)
            self._attempts[event.session_id] = event.attempt_id
            self._attempt_sessions.setdefault(event.attempt_id, event.session_id)
            accepted.append(event)
        return tuple(accepted)


class SqlAlchemyRuntimeEventSink:
    """Persist normalized runtime observations for replay and audit."""

    def __init__(self, db: Session):
        self.db = db

    @staticmethod
    def _value(value: Any) -> Any:
        return value.value if isinstance(value, StrEnum) else value

    @classmethod
    def _payload(cls, payload: dict[str, Any]) -> dict[str, Any]:
        """Convert tuple/enum values to the JSON representation used by DBs."""

        if isinstance(payload, dict):
            return {str(key): cls._payload(value) if isinstance(value, dict) else cls._json_value(value) for key, value in payload.items()}
        return cls._json_value(payload)

    @classmethod
    def _json_value(cls, value: Any) -> Any:
        if isinstance(value, StrEnum):
            return value.value
        if isinstance(value, dict):
            return {str(key): cls._json_value(item) for key, item in value.items()}
        if isinstance(value, (tuple, list)):
            return [cls._json_value(item) for item in value]
        return value

    @classmethod
    def _compatible(cls, row: RuntimeEventRow, event: RuntimeEvent) -> bool:
        return (
            row.kind == cls._value(event.kind)
            and row.status == cls._value(event.status)
            and cls._payload(row.payload) == cls._payload(event.payload)
        )

    @staticmethod
    def _where(event: RuntimeEvent):
        return (
            (RuntimeEventRow.source == event.source)
            & (RuntimeEventRow.attempt_id == event.attempt_id)
            & (RuntimeEventRow.session_id == event.session_id)
            & (RuntimeEventRow.sequence == event.sequence)
        )

    def append(self, event: RuntimeEvent) -> RuntimeEventRow:
        """Insert one event or return its existing replay-equivalent row.

        The unique identity is enforced in the database as well as checked in
        application code.  Dialect-specific ``ON CONFLICT DO NOTHING`` keeps
        concurrent redelivery idempotent for the supported PostgreSQL and
        SQLite state stores.
        """

        if not isinstance(event, RuntimeEvent):
            raise TypeError("runtime event sink requires RuntimeEvent values")
        values = {
            "source": event.source,
            "attempt_id": event.attempt_id,
            "session_id": event.session_id,
            "sequence": event.sequence,
            "kind": self._value(event.kind),
            "status": self._value(event.status),
            "payload": self._payload(event.payload),
            "created_at": utcnow(),
        }
        bind = self.db.get_bind()
        dialect = bind.dialect.name
        if dialect == "sqlite":
            statement = sqlite_insert(RuntimeEventRow).values(**values).on_conflict_do_nothing(
                index_elements=["source", "attempt_id", "session_id", "sequence"]
            )
            self.db.execute(statement)
        elif dialect == "postgresql":
            statement = postgres_insert(RuntimeEventRow).values(**values).on_conflict_do_nothing(
                index_elements=["source", "attempt_id", "session_id", "sequence"]
            )
            self.db.execute(statement)
        else:
            existing = self.db.scalar(select(RuntimeEventRow).where(self._where(event)))
            if existing is None:
                self.db.add(RuntimeEventRow(**values))
                self.db.flush()

        row = self.db.scalar(select(RuntimeEventRow).where(self._where(event)))
        if row is None:  # pragma: no cover - defensive: INSERT and SELECT share a transaction
            raise RuntimeError("runtime event insert did not produce a durable row")
        if not self._compatible(row, event):
            raise RuntimeEventConflictError(
                "runtime event replay identity already exists with different event data: "
                f"{event.identity!r}"
            )
        return row

    def replay(self, events: RuntimeEvent | Iterable[RuntimeEvent]):
        """Replay one event or a batch through the idempotent append path."""

        if isinstance(events, RuntimeEvent):
            return self.append(events)
        return tuple(self.append(event) for event in events)

    def events_for(
        self,
        attempt_id: str,
        *,
        source: str | None = None,
        session_id: str | None = None,
    ) -> tuple[RuntimeEvent, ...]:
        """Read trusted observations in sequence order for controller replay."""

        filters = [RuntimeEventRow.attempt_id == attempt_id]
        if source is not None:
            filters.append(RuntimeEventRow.source == source)
        if session_id is not None:
            filters.append(RuntimeEventRow.session_id == session_id)
        rows = self.db.scalars(
            select(RuntimeEventRow)
            .where(*filters)
            .order_by(RuntimeEventRow.sequence, RuntimeEventRow.id)
        ).all()

        def event_kind(value: str):
            try:
                return RuntimeEventKind(value)
            except ValueError:
                # Forward-compatible replay: unknown provider event kinds are
                # retained as strings rather than dropped.
                return value

        def event_status(value: str):
            try:
                return RuntimeStatus(value)
            except ValueError:
                return value

        return tuple(
            RuntimeEvent(
                kind=event_kind(row.kind),
                session_id=row.session_id,
                attempt_id=row.attempt_id,
                sequence=row.sequence,
                status=event_status(row.status),
                payload=dict(row.payload),
                source=row.source,
            )
            for row in rows
        )
