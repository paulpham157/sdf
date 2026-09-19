from __future__ import annotations

import shutil
import subprocess
import sys
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path


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
    criterion: str | None = None


@dataclass(frozen=True)
class EvaluationResult:
    status: str
    evidence: tuple[Evidence, ...]


class DeterministicEvaluator:
    def __init__(self, *, timeout_seconds: float = 30.0, allowed_executables: set[str] | None = None):
        self.timeout_seconds = timeout_seconds
        self.allowed_executables = allowed_executables or {"python", "python3", "pytest", "ruff"}

    def evaluate(
        self,
        *,
        attempt_id: str,
        workspace: Path,
        commands: Sequence[Sequence[str]] | None = None,
        criteria: Sequence[str] | None = None,
        criterion_checks: Mapping[str, Sequence[Sequence[str]]] | None = None,
    ) -> EvaluationResult:
        """Evaluate explicit criterion/check mappings.

        ``commands`` remains a compatibility path for callers that used the
        original evaluator API. When a task supplies ``criteria`` (including
        an empty list), those commands are unscoped observations and cannot
        make the evaluation pass. A task is successful only when every named
        criterion has at least one mapped deterministic check and all mapped
        checks pass.
        """

        # An explicit mapping is authoritative. Ignore the compatibility
        # command list when it is supplied alongside the mapping so the same
        # check is not executed twice and cannot create duplicate Evidence.
        legacy_commands = tuple(commands or ()) if criterion_checks is None else ()
        checks = {
            criterion: tuple(tuple(command) for command in mapped_commands)
            for criterion, mapped_commands in (criterion_checks or {}).items()
        }
        strict_coverage = criteria is not None or criterion_checks is not None
        expected_criteria = tuple(criteria or ()) if criteria is not None else tuple(checks)
        evidence: list[Evidence] = []
        index = 0

        def add(item: Evidence) -> None:
            nonlocal index
            index += 1
            evidence.append(
                Evidence(
                    evidence_id=f"EVIDENCE-{attempt_id}-{index}",
                    attempt_id=item.attempt_id,
                    kind=item.kind,
                    status=item.status,
                    command=item.command,
                    exit_code=item.exit_code,
                    stdout=item.stdout,
                    stderr=item.stderr,
                    confidence=item.confidence,
                    criterion=item.criterion,
                )
            )

        covered: set[str] = set()
        for criterion in expected_criteria:
            mapped_commands = checks.get(criterion, ())
            if not mapped_commands:
                add(self._coverage_evidence(attempt_id=attempt_id, criterion=criterion))
                continue
            covered.add(criterion)
            for command in mapped_commands:
                add(
                    self._run_one(
                        attempt_id=attempt_id,
                        workspace=workspace,
                        command=command,
                        criterion=criterion,
                        index=index + 1,
                    )
                )

        # A mapping for a criterion that is not on the Task is not coverage;
        # record it so an accidental extra command cannot certify the task.
        for criterion in checks:
            if criterion not in covered and criterion not in expected_criteria:
                add(self._unmapped_evidence(attempt_id=attempt_id, criterion=criterion))

        # Keep the old ``commands`` argument observable, but do not associate
        # it with a criterion. This makes arbitrary command success unable to
        # validate business intent.
        for command in legacy_commands:
            add(
                self._run_one(
                    attempt_id=attempt_id,
                    workspace=workspace,
                    command=command,
                    criterion=None,
                    index=index + 1,
                )
            )

        if not evidence:
            add(
                Evidence(
                    evidence_id="",
                    attempt_id=attempt_id,
                    kind="evaluation",
                    status="INCONCLUSIVE",
                    command="<no evaluator commands>",
                    exit_code=None,
                    stdout="",
                    stderr="no evaluator commands supplied",
                    confidence=0.2,
                    criterion=None,
                )
            )

        statuses = [item.status for item in evidence]
        if "FAIL" in statuses:
            overall = "FAIL"
        elif "INCONCLUSIVE" in statuses:
            overall = "INCONCLUSIVE"
        elif strict_coverage and (not expected_criteria or covered != set(expected_criteria)):
            overall = "INCONCLUSIVE"
        else:
            overall = "PASS"
        return EvaluationResult(status=overall, evidence=tuple(evidence))

    def _coverage_evidence(self, *, attempt_id: str, criterion: str) -> Evidence:
        return Evidence(
            evidence_id="",
            attempt_id=attempt_id,
            kind="evaluation",
            status="INCONCLUSIVE",
            command="<missing check>",
            exit_code=None,
            stdout="",
            stderr=f"no deterministic check mapped to criterion: {criterion}",
            confidence=0.2,
            criterion=criterion,
        )

    def _unmapped_evidence(self, *, attempt_id: str, criterion: str) -> Evidence:
        return Evidence(
            evidence_id="",
            attempt_id=attempt_id,
            kind="evaluation",
            status="INCONCLUSIVE",
            command="<unmapped criterion>",
            exit_code=None,
            stdout="",
            stderr=f"criterion is not declared by the task: {criterion}",
            confidence=0.2,
            criterion=criterion,
        )

    def _run_one(
        self,
        *,
        attempt_id: str,
        workspace: Path,
        command: Sequence[str],
        criterion: str | None,
        index: int,
    ) -> Evidence:
        argv = tuple(command)
        rendered = " ".join(argv)
        if not argv or Path(argv[0]).name not in self.allowed_executables:
            return Evidence(
                evidence_id=f"EVIDENCE-{attempt_id}-{index}",
                attempt_id=attempt_id,
                kind="evaluation",
                status="INCONCLUSIVE",
                command=rendered,
                exit_code=None,
                stdout="",
                stderr="executable is not allowlisted",
                confidence=0.2,
                criterion=criterion,
            )
        try:
            executable = Path(argv[0]).name
            process_argv = list(argv)
            if executable in {"python", "python3"} and shutil.which(process_argv[0]) is None:
                process_argv[0] = sys.executable
            completed = subprocess.run(
                process_argv,
                cwd=workspace,
                capture_output=True,
                text=True,
                timeout=self.timeout_seconds,
                check=False,
            )
            result_status = "PASS" if completed.returncode == 0 else "FAIL"
            return Evidence(
                evidence_id=f"EVIDENCE-{attempt_id}-{index}",
                attempt_id=attempt_id,
                kind="evaluation",
                status=result_status,
                command=rendered,
                exit_code=completed.returncode,
                stdout=completed.stdout,
                stderr=completed.stderr,
                confidence=1.0 if result_status == "PASS" else 0.95,
                criterion=criterion,
            )
        except subprocess.TimeoutExpired as exc:
            return Evidence(
                evidence_id=f"EVIDENCE-{attempt_id}-{index}",
                attempt_id=attempt_id,
                kind="evaluation",
                status="INCONCLUSIVE",
                command=rendered,
                exit_code=None,
                stdout=self._as_text(exc.stdout),
                stderr=self._as_text(exc.stderr) or "timeout",
                confidence=0.2,
                criterion=criterion,
            )
        except OSError as exc:
            return Evidence(
                evidence_id=f"EVIDENCE-{attempt_id}-{index}",
                attempt_id=attempt_id,
                kind="evaluation",
                status="INCONCLUSIVE",
                command=rendered,
                exit_code=None,
                stdout="",
                stderr=str(exc),
                confidence=0.2,
                criterion=criterion,
            )

    @staticmethod
    def _as_text(value: str | bytes | None) -> str:
        if value is None:
            return ""
        if isinstance(value, bytes):
            return value.decode(errors="replace")
        return value
