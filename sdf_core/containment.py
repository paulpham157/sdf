"""Optional OS-level process containment backends.

The portable fixture sandbox cannot constrain arbitrary child code by itself.
This module provides an explicit subprocess boundary for deployments that
have an OS sandbox tool, while keeping availability and runtime proof visible.
"""

from __future__ import annotations

import os
import shutil
import signal
import subprocess
import tempfile
import sys
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, Mapping, Sequence


class ContainmentUnavailable(RuntimeError):
    """The requested OS containment backend is not installed or usable."""


@dataclass(frozen=True, slots=True)
class ContainmentProbe:
    backend: str
    executable: str | None
    available: bool
    detail: str


CommandRunner = Callable[[Sequence[str], Path, Mapping[str, str], float], subprocess.CompletedProcess[str]]


class MacOSSandboxBackend:
    """Use Apple's sandbox-exec profile as an explicit opt-in boundary.

    ``sandbox-exec`` is deprecated on some macOS releases and may be blocked
    by host policy. Therefore construction/probing never implies proof; callers
    must run a disposable smoke test and retain its result as deployment
    evidence before routing real agents through this backend.
    """

    name = "macos-sandbox-exec"

    def __init__(self, *, executable: str = "sandbox-exec", runner: CommandRunner | None = None):
        self.executable = executable
        self._runner = runner or self._run

    def probe(self) -> ContainmentProbe:
        path = shutil.which(self.executable)
        if path is None:
            return ContainmentProbe(self.name, None, False, "sandbox-exec is not installed")
        return ContainmentProbe(self.name, path, True, "binary is present; disposable enforcement smoke test required")

    def profile(self, root: Path, *, allow_network: bool = False) -> str:
        root = Path(root).resolve()
        network_rule = "(allow network*)" if allow_network else "(deny network*)"
        # Read-only system paths permit an interpreter to start; mutable access
        # is limited to the disposable fixture root.
        return "\n".join(
            (
                "(version 1)",
                "(deny default)",
                "(allow process-exec)",
                "(allow process-fork)",
                "(allow file-read* (subpath \"/System\"))",
                "(allow file-read* (subpath \"/usr\"))",
                "(allow file-read* (subpath \"/bin\"))",
                "(allow file-read* (subpath \"/private/var\"))",
                f"(allow file-read* (subpath \"{root}\"))",
                f"(allow file-write* (subpath \"{root}\"))",
                network_rule,
            )
        )

    def smoke(self, root: Path, *, timeout_seconds: float = 5.0) -> ContainmentProbe:
        """Run a disposable write-boundary check and return explicit evidence."""

        probe = self.probe()
        if not probe.available:
            return probe
        root = Path(root).expanduser().resolve()
        root.mkdir(parents=True, exist_ok=True)
        outside = root.parent / f".sdf-containment-outside-{uuid.uuid4().hex}"
        inside = root / ".sdf-containment-inside"
        code = (
            "from pathlib import Path; "
            f"Path({str(inside)!r}).write_text('inside'); "
            f"Path({str(outside)!r}).write_text('outside')"
        )
        try:
            result = self.run(
                (sys.executable, "-c", code),
                root=root,
                cwd=root,
                timeout_seconds=timeout_seconds,
            )
            if result.returncode != 0 or outside.exists() or not inside.exists():
                return ContainmentProbe(
                    self.name,
                    probe.executable,
                    False,
                    "disposable write-boundary smoke test failed",
                )
            return ContainmentProbe(
                self.name,
                probe.executable,
                True,
                "disposable write-boundary smoke test passed",
            )
        except Exception as exc:
            return ContainmentProbe(self.name, probe.executable, False, f"smoke test failed: {exc}")
        finally:
            inside.unlink(missing_ok=True)
            outside.unlink(missing_ok=True)

    def run(
        self,
        command: Sequence[str],
        *,
        root: Path,
        cwd: Path,
        env: Mapping[str, str] | None = None,
        timeout_seconds: float = 5.0,
        allow_network: bool = False,
    ) -> subprocess.CompletedProcess[str]:
        probe = self.probe()
        if not probe.available:
            raise ContainmentUnavailable(probe.detail)
        if not command:
            raise ValueError("command must not be empty")
        if timeout_seconds <= 0:
            raise ValueError("timeout_seconds must be positive")
        with tempfile.NamedTemporaryFile("w", suffix=".sb", delete=False) as profile_file:
            profile_file.write(self.profile(root, allow_network=allow_network))
            profile_path = Path(profile_file.name)
        try:
            wrapped = (self.executable, "-f", str(profile_path), *map(str, command))
            return self._runner(wrapped, Path(cwd), dict(env or {}), timeout_seconds)
        finally:
            profile_path.unlink(missing_ok=True)

    @staticmethod
    def _run(command: Sequence[str], cwd: Path, env: Mapping[str, str], timeout_seconds: float) -> subprocess.CompletedProcess[str]:
        child_env = dict(env)
        child_env.setdefault("PATH", os.environ.get("PATH", ""))
        try:
            return subprocess.run(command, cwd=cwd, env=child_env, capture_output=True, text=True, timeout=timeout_seconds)
        except subprocess.TimeoutExpired as exc:
            raise ContainmentUnavailable("contained process timed out") from exc
