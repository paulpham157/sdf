"""E2B-backed process containment.

The local macOS ``sandbox-exec`` backend is not usable on every host.  This
backend moves arbitrary process execution into the disposable box managed by
the installed ``e2b-box`` plugin.  It intentionally keeps the provider seam
small: SDF still owns policy, audit and artifact handling while E2B owns the
remote process boundary.

E2B network egress is intentionally available to the coding agent. The
structured SDF ``network.request`` action remains disabled because it is not
yet routed through the E2B box and audited as a remote capability.
"""

from __future__ import annotations

import json
import os
import shlex
import shutil
import signal
import subprocess
from pathlib import Path
from typing import Callable, Mapping, Sequence

from .containment import ContainmentProbe, ContainmentUnavailable


E2BRunner = Callable[[Sequence[str], Path, int, Mapping[str, str]], str]


# Host paths. The box has its own home, temp and PATH; SDF_SANDBOX_ROOT is the
# host workspace and must not be visible inside the command.
_REMOTE_ENV_SKIP = frozenset({"HOME", "TMPDIR", "TMP", "TEMP", "PATH", "SDF_SANDBOX_ROOT"})


def _measured_ok(stdout: str) -> bool:
    """True when ``stdout`` is a JSON object with ``ok: true``.

    ``e2b-box exec`` exits 1 when the remote command fails, and still prints
    that object with the command's ``exitCode``. Malformed output and
    ``ok: false`` are not a measurement.
    """

    try:
        payload = json.loads(stdout)
    except (TypeError, json.JSONDecodeError):
        return False
    return isinstance(payload, dict) and payload.get("ok") is True


class E2BContainmentBackend:
    """Run a command inside a disposable E2B box.

    The workspace is synced out and the box is killed afterwards. It is not
    pulled back: ``e2b-box pull`` refuses a directory that is not a git
    checkout unless ``--force``, and a pull would copy whatever the command
    wrote (including agent code the check imported) onto the host workspace.
    """

    name = "e2b-box"
    # E2B Cloud network egress is intentionally available to the coding agent:
    # dependency installation and provider calls are part of agent work. The
    # separate structured ``network.request`` action remains disabled because
    # this adapter does not route that action through the box.
    network_egress_allowed = True
    network_actions_allowed = False

    def __init__(
        self,
        *,
        template: str = "base",
        executable: str = "e2b-box",
        runner: E2BRunner | None = None,
        environ: Mapping[str, str] | None = None,
    ):
        if not template.strip():
            raise ValueError("template must not be empty")
        self.template = template.strip()
        self.executable = executable
        self._runner = runner or self._run
        self._environ = dict(os.environ if environ is None else environ)

    def probe(self) -> ContainmentProbe:
        path = shutil.which(self.executable)
        if path is None:
            return ContainmentProbe(self.name, None, False, "e2b-box is not installed")
        if not self._environ.get("E2B_API_KEY"):
            return ContainmentProbe(self.name, path, False, "E2B_API_KEY is not configured")
        return ContainmentProbe(
            self.name,
            path,
            True,
            "e2b-box and credentials are present; disposable live smoke test required",
        )

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
        """Run one command remotely and return a CompletedProcess-shaped result.

        ``allow_network`` controls SDF's capability signal inside the command;
        E2B Cloud egress remains available to the agent. It is deliberately
        not used as evidence of network denial.

        The sequence is sync, exec, kill. There is no pull.
        """

        probe = self.probe()
        if not probe.available:
            raise ContainmentUnavailable(probe.detail)
        if not command:
            raise ValueError("command must not be empty")
        root = Path(root).expanduser().resolve()
        cwd = Path(cwd).expanduser().resolve()
        try:
            relative_cwd = cwd.relative_to(root)
        except ValueError as exc:
            raise ContainmentUnavailable("E2B process cwd must remain inside the workspace") from exc
        if timeout_seconds <= 0:
            raise ValueError("timeout_seconds must be positive")

        timeout_ms = max(1, int(timeout_seconds * 1000))
        remote = self._remote_command(command, relative_cwd, env or {})
        cli_env = {
            "PATH": self._environ.get("PATH", ""),
            "HOME": self._environ.get("HOME", ""),
            "E2B_API_KEY": self._environ["E2B_API_KEY"],
        }
        if self._environ.get("E2B_DOMAIN"):
            cli_env["E2B_DOMAIN"] = self._environ["E2B_DOMAIN"]
        primary_error: Exception | None = None
        result: subprocess.CompletedProcess[str] | None = None
        try:
            self._runner((self.executable, "-t", self.template, "sync"), root, timeout_ms, cli_env)
            raw = self._runner((self.executable, "exec", "--timeout-ms", str(timeout_ms), remote), root, timeout_ms, cli_env)
            try:
                payload = json.loads(raw)
            except (TypeError, json.JSONDecodeError) as exc:
                raise ContainmentUnavailable("e2b-box exec returned invalid JSON") from exc
            if not isinstance(payload, dict) or payload.get("ok") is not True:
                detail = payload.get("error", "command was not measured") if isinstance(payload, dict) else "invalid result"
                raise ContainmentUnavailable(f"e2b-box could not measure process: {detail}")
            exit_code = payload.get("exitCode")
            if type(exit_code) is not int:
                raise ContainmentUnavailable("e2b-box exec returned no exit code")
            result = subprocess.CompletedProcess(
                tuple(str(part) for part in command),
                exit_code,
                str(payload.get("stdout", "")),
                str(payload.get("stderr", "")),
            )
        except Exception as exc:
            primary_error = exc
        finally:
            try:
                self._runner((self.executable, "kill"), root, timeout_ms, cli_env)
            except Exception as exc:
                if primary_error is None:
                    primary_error = exc
        if primary_error is not None:
            if isinstance(primary_error, ContainmentUnavailable):
                raise primary_error
            raise ContainmentUnavailable(str(primary_error)) from primary_error
        assert result is not None
        return result

    @staticmethod
    def _remote_command(command: Sequence[str], relative_cwd: Path, env: Mapping[str, str]) -> str:
        assignments = []
        for key, value in env.items():
            key = str(key)
            if key in _REMOTE_ENV_SKIP:
                continue
            if not key or "=" in key or any(char not in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_" for char in key):
                continue
            assignments.append(f"{key}={shlex.quote(str(value))}")
        prefix = " ".join(assignments)
        command_text = shlex.join(str(part) for part in command)
        if prefix:
            command_text = f"{prefix} {command_text}"
        if str(relative_cwd) != ".":
            command_text = f"cd {shlex.quote(str(relative_cwd))} && {command_text}"
        return command_text

    def _run(self, command: Sequence[str], cwd: Path, timeout_ms: int, env: Mapping[str, str]) -> str:
        process = subprocess.Popen(
            command,
            cwd=cwd,
            env=dict(env),
            # e2b exec streams piped stdin until EOF; an inherited pipe hangs it.
            stdin=subprocess.DEVNULL,
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
            raise ContainmentUnavailable("e2b-box command timed out")
        if process.returncode != 0 and not _measured_ok(stdout):
            detail = (stderr or stdout or "")[-1000:]
            raise ContainmentUnavailable(f"e2b-box command failed: {detail}")
        return stdout
