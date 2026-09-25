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
    cleanup_attempted: bool = False
    cleanup_succeeded: bool = False


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
                # `run` boots or re-syncs the box with the requested template,
                # pulls the result home and, with --kill, destroys the box. A
                # bare `sync`/`pull` would target the plugin's default or an
                # already-paused box. The trailing `kill` is idempotent cleanup.
                ("e2b-box", "run", "-t", template, "--task", task, "--timeout-ms", str(timeout_ms), "--kill", "--json"),
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
        env = {
            "PATH": self._environ.get("PATH", ""),
            "HOME": self._environ.get("HOME", ""),
            "E2B_API_KEY": self._environ["E2B_API_KEY"],
        }
        if self._environ.get("E2B_DOMAIN"):
            env["E2B_DOMAIN"] = self._environ["E2B_DOMAIN"]
        payload: dict[str, Any] | None = None
        primary_error: Exception | None = None
        cleanup_attempted = False
        cleanup_succeeded = False
        try:
            try:
                decoded = json.loads(self._runner(plan.commands[0], plan.checkout, plan.timeout_ms, env))
            except (TypeError, json.JSONDecodeError) as exc:
                raise E2BAdapterError("e2b-box returned invalid JSON") from exc
            if not isinstance(decoded, dict):
                raise E2BAdapterError("e2b-box returned a non-object result")
            payload = decoded
            if pull and not (decoded.get("pull") or {}).get("ok"):
                raise E2BAdapterError("e2b-box did not pull the Attempt workspace back")
        except Exception as exc:
            primary_error = exc
        finally:
            if cleanup:
                cleanup_attempted = True
                try:
                    self._runner(plan.commands[1], plan.checkout, plan.timeout_ms, env)
                    cleanup_succeeded = True
                except Exception as cleanup_error:
                    if primary_error is None:
                        primary_error = cleanup_error
        if primary_error is not None:
            if isinstance(primary_error, E2BAdapterError):
                raise primary_error
            raise E2BAdapterError(str(primary_error)) from primary_error
        assert payload is not None
        agent = payload.get("agent") if isinstance(payload.get("agent"), dict) else {}
        return HerdrE2BResult(
            attempt_id=plan.attempt_id,
            sandbox_id=payload.get("sandboxId"),
            herdr_session_id=payload.get("herdrSessionId"),
            workspace_id=payload.get("workspaceId"),
            status=str(payload.get("status", "unknown")),
            raw={**payload, "agent": agent},
            cleanup_attempted=cleanup_attempted,
            cleanup_succeeded=cleanup_succeeded,
        )


    def _run(self, command: Sequence[str], cwd: Path, timeout_ms: int, env: Mapping[str, str]) -> str:
        # e2b exec streams piped stdin until EOF; an inherited pipe hangs it.
        process = subprocess.Popen(command, cwd=cwd, env=dict(env), stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
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


class HerdrE2BNativeAdapter:
    """Run one live E2B coding-agent Attempt through the ExecutionService artifact pipeline.

    The Attempt workspace is the e2b-box checkout: it is synced into a disposable
    box, the agent runs headless, results are pulled back, and the box is killed.
    Changed files are derived from local snapshots, never from agent claims.
    """

    def __init__(self, adapter: HerdrE2BAdapter | None = None, *, template: str = "codex", agent: str = "codex",
                 timeout_ms: int = 300_000, live: bool = False):
        self.adapter = adapter or HerdrE2BAdapter()
        self.template = template
        self.agent = agent
        self.timeout_ms = timeout_ms
        self.live = live
        self.last_result: HerdrE2BResult | None = None

    def run(self, *, attempt_id: str, workspace: Path, instructions: str):
        from .adapter import AdapterResult, RuntimeAgentAdapter

        before = RuntimeAgentAdapter._snapshot(workspace)
        _ensure_git_baseline(workspace)
        plan = self.adapter.plan(attempt_id=attempt_id, checkout=workspace, template=self.template,
                                 agent=self.agent, task=instructions, timeout_ms=self.timeout_ms)
        result = self.adapter.execute(plan, live=self.live)
        self.last_result = result
        after = RuntimeAgentAdapter._snapshot(workspace)
        changed = tuple(path for path in RuntimeAgentAdapter._changed(before, after) if not path.startswith(".git/"))
        agent = result.raw.get("agent") if isinstance(result.raw.get("agent"), Mapping) else {}
        completed = result.status == "done" and bool(result.raw.get("ok")) and result.cleanup_succeeded
        return AdapterResult(
            attempt_id=attempt_id,
            status="completed" if completed else result.status,
            changed_files=changed,
            stdout=str(agent.get("stdout", "")),
            stderr=str(agent.get("stderr", "")) + ("" if completed else f"\ne2b-box status: {result.status}"),
            exit_code=int(agent.get("exitCode") or 0) if completed else 1,
        )


def _ensure_git_baseline(workspace: Path) -> None:
    # e2b-box keys a box by git worktree and pulls relative to a baseline commit.
    if (workspace / ".git").exists():
        return
    env = {"PATH": os.environ.get("PATH", ""), "GIT_AUTHOR_NAME": "sdf", "GIT_AUTHOR_EMAIL": "sdf@localhost",
           "GIT_COMMITTER_NAME": "sdf", "GIT_COMMITTER_EMAIL": "sdf@localhost", "HOME": os.environ.get("HOME", "")}
    for command in (("git", "init", "-q"), ("git", "add", "-A"), ("git", "commit", "-q", "--allow-empty", "-m", "sdf attempt baseline")):
        subprocess.run(command, cwd=workspace, env=env, check=True, capture_output=True)
