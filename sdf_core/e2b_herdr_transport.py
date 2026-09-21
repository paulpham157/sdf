"""E2B transport for a persistent Herdr control session.

The SDF process remains the control plane. This transport creates one
long-lived E2B sandbox, runs Herdr CLI commands inside it, and exposes an
explicit ``close`` operation for the Attempt owner. It is deliberately
separate from ``E2BContainmentBackend.run()``, whose sync/exec/pull/kill loop
is intentionally one-shot and cannot support prompt/reconnect semantics.
"""

from __future__ import annotations

import os
import re
import signal
import subprocess
from pathlib import Path
from typing import Callable, Mapping, Sequence

from .herdr_runtime import HerdrRuntimeError


E2BHerdrRunner = Callable[[Sequence[str], int, Mapping[str, str]], str]


class E2BHerdrTransport:
    """Run Herdr control commands in one persistent E2B sandbox."""

    def __init__(
        self,
        *,
        template: str,
        e2b_binary: str = "e2b",
        herdr_binary: str = "herdr",
        timeout_seconds: int = 900,
        runner: E2BHerdrRunner | None = None,
        environ: Mapping[str, str] | None = None,
        sandbox_id: str | None = None,
    ) -> None:
        if not template.strip():
            raise ValueError("E2B Herdr template must be non-empty")
        if timeout_seconds <= 0:
            raise ValueError("E2B Herdr timeout must be positive")
        self.template = template.strip()
        self.e2b_binary = e2b_binary
        self.herdr_binary = herdr_binary
        self.timeout_seconds = timeout_seconds
        self._runner = runner or self._run
        self._environ = dict(os.environ if environ is None else environ)
        self._sandbox_id = sandbox_id

    @property
    def sandbox_id(self) -> str | None:
        return self._sandbox_id

    def run(self, command: Sequence[str], timeout_ms: int) -> str:
        sandbox_id = self._ensure_sandbox(timeout_ms)
        if not command:
            raise ValueError("Herdr command must not be empty")
        remote = [self.herdr_binary, *command[1:]]
        return self._runner(
            (self.e2b_binary, "sandbox", "exec", sandbox_id, "--", *remote),
            timeout_ms,
            self._cli_env(),
        )

    def close(self, timeout_ms: int = 30_000) -> None:
        """Kill the persistent sandbox and forget its provider identity."""

        if self._sandbox_id is None:
            return
        sandbox_id = self._sandbox_id
        try:
            self._runner(
                (self.e2b_binary, "sandbox", "kill", sandbox_id),
                timeout_ms,
                self._cli_env(),
            )
        finally:
            self._sandbox_id = None

    def _ensure_sandbox(self, timeout_ms: int) -> str:
        if self._sandbox_id is not None:
            return self._sandbox_id
        raw = self._runner(
            (
                self.e2b_binary,
                "sandbox",
                "create",
                "--detach",
                "--timeout",
                str(self.timeout_seconds),
                self.template,
            ),
            timeout_ms,
            self._cli_env(),
        )
        match = re.search(r"\b([a-z0-9]{16,32})\b", raw.lower())
        if match is None:
            raise HerdrRuntimeError("E2B sandbox create did not return a sandbox ID")
        self._sandbox_id = match.group(1)
        return self._sandbox_id

    def _cli_env(self) -> dict[str, str]:
        api_key = self._environ.get("E2B_API_KEY")
        if not api_key:
            raise HerdrRuntimeError("E2B Herdr transport requires E2B_API_KEY")
        env = {
            "PATH": self._environ.get("PATH", ""),
            "HOME": self._environ.get("HOME", str(Path.home())),
            "E2B_API_KEY": api_key,
        }
        domain = self._environ.get("E2B_DOMAIN")
        if domain:
            env["E2B_DOMAIN"] = domain
        return env

    @staticmethod
    def _run(command: Sequence[str], timeout_ms: int, env: Mapping[str, str]) -> str:
        process = subprocess.Popen(
            command,
            env=dict(env),
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            start_new_session=(os.name == "posix"),
        )
        try:
            stdout, stderr = process.communicate(timeout=timeout_ms / 1000)
        except subprocess.TimeoutExpired:
            if os.name == "posix":
                os.killpg(process.pid, signal.SIGKILL)
            else:  # pragma: no cover
                process.kill()
            process.wait()
            raise HerdrRuntimeError("E2B Herdr command timed out")
        if process.returncode != 0:
            raise HerdrRuntimeError(f"E2B Herdr command failed: {stderr[-1000:]}")
        return stdout
