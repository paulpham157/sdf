"""Provider-neutral agent fixture loop.

The loop deliberately treats runtime completion and evaluator acceptance as
different facts.  A real Herdr adapter can drive the same boundary later;
local tests use an injected runtime and never claim provider evidence.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Mapping, Sequence

from .evaluator import DeterministicEvaluator, EvaluationResult
from .runtime import AgentRuntime, RuntimeSession, RuntimeStatus


@dataclass(frozen=True, slots=True)
class FixtureLoopResult:
    attempt_id: str
    session: RuntimeSession
    output: tuple[str, ...]
    evaluation: EvaluationResult

    @property
    def runtime_completed(self) -> bool:
        return self.session.status is RuntimeStatus.COMPLETED

    @property
    def accepted(self) -> bool:
        """Independent evaluator acceptance, never agent self-report."""

        return self.evaluation.status == "PASS"


class AgentFixtureLoop:
    """Drive one Attempt through dispatch, output observation and evaluation."""

    def __init__(self, runtime: AgentRuntime, *, evaluator: DeterministicEvaluator | None = None):
        self.runtime = runtime
        self.evaluator = evaluator or DeterministicEvaluator()

    def run(
        self,
        *,
        attempt_id: str,
        agent: str,
        workspace: Path,
        instructions: str,
        commands: Sequence[Sequence[str]] = (),
        criteria: Sequence[str] = (),
        criterion_checks: Mapping[str, Sequence[Sequence[str]]] | None = None,
    ) -> FixtureLoopResult:
        workspace = Path(workspace).expanduser().resolve()
        if not workspace.is_dir():
            raise ValueError("workspace must be an existing directory")
        # A transport-owned workspace (E2B) is staged in before the agent starts
        # and collected back before evaluation, so the evaluator sees its edits.
        bind_workspace = getattr(self.runtime, "bind_workspace", None)
        if callable(bind_workspace):
            bind_workspace(attempt_id, workspace)
        session = self.runtime.start(attempt_id=attempt_id, agent=agent)
        if session.status in {RuntimeStatus.CANCELLED, RuntimeStatus.TERMINATED}:
            raise RuntimeError(f"runtime session cannot receive input: {session.status.value}")
        session = self.runtime.send(session.session_id, instructions)
        output = self.runtime.stream(session.session_id)
        session = self.runtime.status(session.session_id)
        collect_workspace = getattr(self.runtime, "collect_workspace", None)
        if callable(collect_workspace):
            collect_workspace(attempt_id, workspace)
        evaluation = self.evaluator.evaluate(
            attempt_id=attempt_id,
            workspace=workspace,
            commands=commands,
            criteria=criteria,
            criterion_checks=criterion_checks,
        )
        return FixtureLoopResult(attempt_id, session, output, evaluation)
