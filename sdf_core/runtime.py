"""Small internal agent-runtime seam; real Herdr remains an unverified adapter."""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import Protocol


class RuntimeStatus(StrEnum):
    CREATED = "created"
    RUNNING = "running"
    COMPLETED = "completed"
    CANCELLED = "cancelled"
    TERMINATED = "terminated"


@dataclass(frozen=True, slots=True)
class RuntimeSession:
    session_id: str
    attempt_id: str
    agent: str
    status: RuntimeStatus
    output: tuple[str, ...] = ()


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
