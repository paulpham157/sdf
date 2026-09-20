"""Live-gated Herdr/E2B adapter seam.

The adapter builds an auditable plan without network calls. Live execution is
an explicit opt-in and delegates to the installed ``e2b-box`` plugin command.
"""

from __future__ import annotations

import json
import os
import shutil
import signal
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
    workspace_id: str | None
    status: str
    raw: Mapping[str, Any]


Runner = Callable[[Sequence[str], Path, int, Mapping[str, str]], str]


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
                ("e2b-box", "sync"),
                ("e2b-box", "run", "-t", template, "--task", task, "--timeout-ms", str(timeout_ms), "--json"),
                ("e2b-box", "pull"),
                ("e2b-box", "kill"),
            ),
        )

    def execute(self, plan: HerdrE2BPlan, *, live: bool = False, pull: bool = True, cleanup: bool = True) -> HerdrE2BResult:
        if not live:
            return HerdrE2BResult(plan.attempt_id, None, None, None, "dry-run", {"plan": plan.commands})
        if not self._environ.get("E2B_API_KEY"):
            raise E2BAdapterError("live E2B execution requires E2B_API_KEY")
        if shutil.which("e2b-box") is None:
            raise E2BAdapterError("live E2B execution requires the e2b-box plugin command")
        env = {"PATH": self._environ.get("PATH", ""), "HOME": self._environ.get("HOME", ""), "E2B_API_KEY": self._environ["E2B_API_KEY"]}
        self._runner(plan.commands[0], plan.checkout, plan.timeout_ms, env)
        payload = json.loads(self._runner(plan.commands[1], plan.checkout, plan.timeout_ms, env))
        if not isinstance(payload, dict):
            raise E2BAdapterError("e2b-box returned a non-object result")
        agent = payload.get("agent") if isinstance(payload.get("agent"), dict) else {}
        if pull:
            self._runner(plan.commands[2], plan.checkout, plan.timeout_ms, env)
        if cleanup:
            self._runner(plan.commands[3], plan.checkout, plan.timeout_ms, env)
        return HerdrE2BResult(
            attempt_id=plan.attempt_id,
            sandbox_id=payload.get("sandboxId"),
            herdr_session_id=payload.get("herdrSessionId"),
            workspace_id=payload.get("workspaceId"),
            status=str(payload.get("status", "unknown")),
            raw={**payload, "agent": agent},
        )


    def _run(self, command: Sequence[str], cwd: Path, timeout_ms: int, env: Mapping[str, str]) -> str:
        process = subprocess.Popen(command, cwd=cwd, env=dict(env), stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                   text=True, start_new_session=(os.name == "posix"))
        try:
            stdout, stderr = process.communicate(timeout=timeout_ms / 1000)
        except subprocess.TimeoutExpired:
            if os.name == "posix":
                os.killpg(process.pid, signal.SIGKILL)
            else:  # pragma: no cover
                process.kill()
            process.wait()
            raise E2BAdapterError("e2b-box command timed out")
        if process.returncode != 0:
            raise E2BAdapterError(f"e2b-box command failed: {stderr[-1000:]}")
        return stdout
