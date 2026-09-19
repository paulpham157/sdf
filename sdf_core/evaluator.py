from __future__ import annotations

import subprocess
import shutil
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Sequence


@dataclass(frozen=True)
class Evidence:
    evidence_id: str
    attempt_id: str
    kind: str
    status: str
    command: str
    exit_code: int | None
    stdout: str
    stderr: str
    confidence: float


@dataclass(frozen=True)
class EvaluationResult:
    status: str
    evidence: tuple[Evidence, ...]


class DeterministicEvaluator:
    def __init__(self, *, timeout_seconds: float = 30.0, allowed_executables: set[str] | None = None):
        self.timeout_seconds = timeout_seconds
        self.allowed_executables = allowed_executables or {"python", "python3", "pytest", "ruff"}

    def evaluate(self, *, attempt_id: str, workspace: Path, commands: Sequence[Sequence[str]]) -> EvaluationResult:
        evidence: list[Evidence] = []
        overall = "PASS"
        if not commands:
            return EvaluationResult(
                status="INCONCLUSIVE",
                evidence=(Evidence(
                    evidence_id=f"EVIDENCE-{attempt_id}-0", attempt_id=attempt_id, kind="evaluation",
                    status="INCONCLUSIVE", command="<no evaluator commands>", exit_code=None,
                    stdout="", stderr="no evaluator commands supplied", confidence=0.2,
                ),),
            )
        for index, command in enumerate(commands, start=1):
            if not command or Path(command[0]).name not in self.allowed_executables:
                evidence.append(Evidence(
                    evidence_id=f"EVIDENCE-{attempt_id}-{index}", attempt_id=attempt_id, kind="evaluation",
                    status="INCONCLUSIVE", command=" ".join(command), exit_code=None, stdout="",
                    stderr="executable is not allowlisted", confidence=0.2,
                ))
                overall = "INCONCLUSIVE"
                continue
            try:
                executable = Path(command[0]).name
                argv = list(command)
                if executable in {"python", "python3"} and shutil.which(argv[0]) is None:
                    argv[0] = sys.executable
                completed = subprocess.run(argv, cwd=workspace, capture_output=True, text=True, timeout=self.timeout_seconds, check=False)
                status = "PASS" if completed.returncode == 0 else "FAIL"
                confidence = 1.0 if status == "PASS" else 0.95
                if status == "FAIL":
                    overall = "FAIL"
                evidence.append(Evidence(
                    evidence_id=f"EVIDENCE-{attempt_id}-{index}", attempt_id=attempt_id, kind="evaluation",
                    status=status, command=" ".join(command), exit_code=completed.returncode,
                    stdout=completed.stdout, stderr=completed.stderr, confidence=confidence,
                ))
            except subprocess.TimeoutExpired as exc:
                overall = "INCONCLUSIVE"
                evidence.append(Evidence(
                    evidence_id=f"EVIDENCE-{attempt_id}-{index}", attempt_id=attempt_id, kind="evaluation",
                    status="INCONCLUSIVE", command=" ".join(command), exit_code=None,
                    stdout=exc.stdout or "", stderr=exc.stderr or "timeout", confidence=0.2,
                ))
            except OSError as exc:
                overall = "INCONCLUSIVE"
                evidence.append(Evidence(
                    evidence_id=f"EVIDENCE-{attempt_id}-{index}", attempt_id=attempt_id, kind="evaluation",
                    status="INCONCLUSIVE", command=" ".join(command), exit_code=None,
                    stdout="", stderr=str(exc), confidence=0.2,
                ))
        return EvaluationResult(status=overall, evidence=tuple(evidence))
