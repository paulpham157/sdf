from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Protocol

from .runtime import AgentRuntime, RuntimeController, RuntimeStatus


@dataclass(frozen=True)
class AdapterResult:
    attempt_id: str
    status: str
    changed_files: tuple[str, ...]
    stdout: str = ""
    stderr: str = ""
    exit_code: int = 0


class NativeAdapter(Protocol):
    def run(self, *, attempt_id: str, workspace: Path, instructions: str) -> AdapterResult:
        """Run one coding-agent attempt inside an isolated workspace."""


class FakeNativeAdapter:
    """Deterministic adapter used by tests and local development."""

    def __init__(self, relative_file: str, replacement: str):
        self.relative_file = relative_file
        self.replacement = replacement

    def run(self, *, attempt_id: str, workspace: Path, instructions: str) -> AdapterResult:
        target = workspace / self.relative_file
        target.write_text(self.replacement, encoding="utf-8")
        return AdapterResult(
            attempt_id=attempt_id,
            status="completed",
            changed_files=(self.relative_file,),
            stdout=f"fake adapter applied: {instructions}",
        )


class RuntimeAgentAdapter:
    """Adapt the internal AgentRuntime into the existing artifact pipeline."""

    def __init__(self, runtime: AgentRuntime | RuntimeController, *, agent: str = "native-runtime"):
        if not agent.strip():
            raise ValueError("agent must be non-empty")
        self.runtime = runtime
        self.agent = agent

    def run(self, *, attempt_id: str, workspace: Path, instructions: str) -> AdapterResult:
        before = self._snapshot(workspace)
        bind_workspace = getattr(self.runtime, "bind_workspace", None)
        if callable(bind_workspace):
            bind_workspace(attempt_id, workspace)
        session = None
        try:
            session = self.runtime.start(attempt_id=attempt_id, agent=self.agent)
            self.runtime.send(session.session_id, instructions)
            output = self.runtime.stream(session.session_id)
            final = self.runtime.status(session.session_id)
            collect_workspace = getattr(self.runtime, "collect_workspace", None)
            if callable(collect_workspace):
                collect_workspace(attempt_id, workspace)
            changed = self._changed(before, self._snapshot(workspace))
            completed = final.status is RuntimeStatus.COMPLETED
            return AdapterResult(
                attempt_id=attempt_id,
                status="completed" if completed else final.status.value,
                changed_files=changed,
                stdout="\n".join(output),
                stderr="" if completed else f"runtime ended in {final.status.value}",
                exit_code=0 if completed else 1,
            )
        finally:
            if session is not None:
                self.runtime.terminate(session.session_id)

    @staticmethod
    def _snapshot(workspace: Path) -> dict[str, bytes]:
        return {
            str(path.relative_to(workspace)): path.read_bytes()
            for path in workspace.rglob("*")
            if path.is_file()
        }

    @staticmethod
    def _changed(before: dict[str, bytes], after: dict[str, bytes]) -> tuple[str, ...]:
        return tuple(sorted({*before, *after} - {
            path for path in before.keys() & after.keys() if before[path] == after[path]
        }))
