from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Protocol


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
