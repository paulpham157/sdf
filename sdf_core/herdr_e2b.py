"""Live-gated Herdr/E2B adapter seam.

The adapter builds an auditable plan without network calls. Live execution is
an explicit opt-in and delegates to the installed ``e2b-box`` plugin command.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Mapping, Sequence


class E2BAdapterError(RuntimeError):
    pass


@dataclass(frozen=True, slots=True)
class HerdrE2BPlan:
    attempt_id: str
    checkout: Path
    template: str
    agent: str
    timeout_ms: int
    commands: tuple[tuple[str, ...], ...]


@dataclass(frozen=True, slots=True)
class HerdrE2BResult:
    attempt_id: str
    sandbox_id: str | None
    herdr_session_id: str | None
    status: str
    raw: Mapping[str, Any]


Runner = Callable[[Sequence[str]], str]


class HerdrE2BAdapter:
    def __init__(self, *, runner: Runner | None = None, environ: Mapping[str, str] | None = None):
        self._runner = runner or self._run
        self._environ = dict(os.environ if environ is None else environ)

    def plan(
        self,
        *,
        attempt_id: str,
        checkout: Path,
        template: str,
        agent: str,
        task: str = "-",
        timeout_ms: int = 900_000,
    ) -> HerdrE2BPlan:
        checkout = Path(checkout).expanduser().resolve()
        if not attempt_id.strip() or not template.strip() or not agent.strip():
            raise ValueError("attempt_id, template and agent must be non-empty")
        if not checkout.is_dir():
            raise ValueError("checkout must be an existing directory")
        if timeout_ms <= 0:
            raise ValueError("timeout_ms must be positive")
        return HerdrE2BPlan(
            attempt_id=attempt_id,
            checkout=checkout,
            template=template,
            agent=agent,
            timeout_ms=timeout_ms,
            commands=(
                ("e2b-box", "run", "-t", template, "--task", task, "--timeout-ms", str(timeout_ms), "--json"),
                ("e2b-box", "pull"),
                ("e2b-box", "kill"),
            ),
        )

    def execute(self, plan: HerdrE2BPlan, *, live: bool = False) -> HerdrE2BResult:
        if not live:
            return HerdrE2BResult(plan.attempt_id, None, None, "dry-run", {"plan": plan.commands})
        if not self._environ.get("E2B_API_KEY"):
            raise E2BAdapterError("live E2B execution requires E2B_API_KEY")
        if shutil.which("e2b-box") is None:
            raise E2BAdapterError("live E2B execution requires the e2b-box plugin command")
        payload = json.loads(self._runner(plan.commands[0]))
        if not isinstance(payload, dict):
            raise E2BAdapterError("e2b-box returned a non-object result")
        agent = payload.get("agent") if isinstance(payload.get("agent"), dict) else {}
        return HerdrE2BResult(
            attempt_id=plan.attempt_id,
            sandbox_id=payload.get("sandboxId"),
            herdr_session_id=payload.get("herdrSessionId"),
            status=str(payload.get("status", "unknown")),
            raw={**payload, "agent": agent},
        )

    @staticmethod
    def _run(command: Sequence[str]) -> str:
        return subprocess.check_output(command, text=True)
