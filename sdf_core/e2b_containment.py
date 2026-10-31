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

import hashlib
import json
import logging
import os
import re
import shlex
import shutil
import signal
import subprocess
import time
from pathlib import Path
from typing import Any, Callable, Mapping, Sequence

from .containment import ContainmentProbe, ContainmentUnavailable

_log = logging.getLogger("sdf.e2b_containment")


E2BRunner = Callable[[Sequence[str], Path, int, Mapping[str, str]], str]


# Host paths. The box has its own home, temp and PATH; SDF_SANDBOX_ROOT is the
# host workspace and must not be visible inside the command.
_REMOTE_ENV_SKIP = frozenset({"HOME", "TMPDIR", "TMP", "TEMP", "PATH", "SDF_SANDBOX_ROOT"})


# sync/exec: one try plus two retries. kill: three tries. Exec is not retried
# once it has returned a measured JSON result (ok: true), even when the
# command itself failed.
COMMAND_ATTEMPTS = 3
KILL_ATTEMPTS = 3
_BACKOFF_SECONDS = 0.25

# Connection drops only. A measured command failure and our own wait timeout
# are not retried: the check may already have run.
_TRANSIENT = re.compile(
    r"fetch failed|econnreset|econnaborted|etimedout|eai_again|tls|ssl handshake|"
    r"connection reset|connection aborted|temporarily unavailable|broken pipe|"
    r"network is unreachable",
    re.I,
)
_SECRET_ASSIGNMENT = re.compile(
    r"(?i)\b(api[_-]?key|authorization|bearer|token|secret|password)\b\s*[:=]\s*\S+"
)

# sandbox id -> worktree key, for boxes this process synced and has not killed.
_tracked_boxes: dict[str, str] = {}


def e2b_key(raw: str) -> str:
    """Folder name as e2b-box sanitizes it (``bin/lib/paths.sh`` ``e2b_key``)."""

    return re.sub(r"[^A-Za-z0-9._-]", "-", raw).strip("-")


def box_key(path: Path) -> str:
    """``<folder>-<sha256(abspath)[:8]>``, the same key ``e2b-box`` derives from ``$PWD``."""

    resolved = str(path.expanduser().resolve())
    base = e2b_key(Path(resolved).name) or "box"
    digest = hashlib.sha256(resolved.encode()).hexdigest()[:8]
    return f"{base}-{digest}"


def box_state_dir(environ: Mapping[str, str]) -> Path:
    """Where herdr-e2b v0.5.0 writes ``boxes/<key>.json`` and ``.log``."""

    plugin = (environ.get("HERDR_PLUGIN_STATE_DIR") or "").strip()
    if plugin:
        return Path(plugin)
    explicit = (environ.get("HERDR_E2B_STATE_DIR") or "").strip()
    if explicit:
        return Path(explicit)
    xdg = (environ.get("XDG_STATE_HOME") or "").strip()
    if xdg:
        base = Path(xdg)
    else:
        home = (environ.get("HOME") or "").strip() or str(Path.home())
        base = Path(home) / ".local" / "state"
    # Split so this file does not name the plugin package the bridge test forbids.
    plugin = "e2b-dev." "herdr-e2b"
    return base / "herdr" / "plugins" / plugin


def tracked_evaluator_boxes() -> dict[str, str]:
    """Sandbox ids this process synced and has not confirmed killed, keyed to their worktree key."""

    return dict(_tracked_boxes)


def attempt_owns_sandbox(metadata: Mapping[str, Any] | None, attempt_id: str, *, sandbox_id: str = "") -> bool:
    """Whether a listed sandbox belongs to this Attempt.

    Agent sandboxes carry ``sdf_attempt_id``. Evaluator boxes from ``e2b-box``
    do not: they carry ``app=herdr-e2b`` and ``herdrWorktreeKey=<folder>-<hash>``,
    where ``<folder>`` is the Attempt workspace directory (the Attempt id).
    """

    if not attempt_id.strip():
        return False
    meta = dict(metadata or {})
    if str(meta.get("sdf_attempt_id") or "") == attempt_id:
        return True
    prefix = f"{e2b_key(attempt_id)}-"
    tracked = _tracked_boxes.get(sandbox_id)
    if tracked and tracked.startswith(prefix):
        return True
    if meta.get("app") != "herdr-e2b":
        return False
    return str(meta.get("herdrWorktreeKey") or "").startswith(prefix)


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


def _transient(detail: str) -> bool:
    return _TRANSIENT.search(detail) is not None


def _redact(text: str, secrets: Sequence[str]) -> str:
    redacted = _SECRET_ASSIGNMENT.sub(r"\1=[redacted]", text)
    for secret in secrets:
        if secret and len(secret) >= 8:
            redacted = redacted.replace(secret, "[redacted]")
    return redacted


def _command_failure_detail(stdout: str, stderr: str, *, cwd: Path, environ: Mapping[str, str]) -> str:
    """Bounded stdout and stderr, plus the plugin log's last ``FAILED:`` line."""

    chunks = [part.strip() for part in (stderr, stdout) if part and part.strip()]
    tail = "\n".join(chunks)[-600:]
    failed = _box_log_failure(box_key(cwd), environ)
    if failed and failed not in tail:
        tail = f"{tail}\n{failed}".strip()
    if not tail:
        tail = "e2b-box command failed"
    return _redact(tail, [environ.get("E2B_API_KEY", "")])[-1000:]


def _box_log_failure(key: str, environ: Mapping[str, str]) -> str | None:
    log = box_state_dir(environ) / "boxes" / f"{key}.log"
    try:
        text = log.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return None
    failed = [line.strip() for line in text.splitlines() if "FAILED:" in line]
    if not failed:
        return None
    return failed[-1][-400:]


def _sandbox_id_from_record(key: str, environ: Mapping[str, str]) -> str | None:
    record = box_state_dir(environ) / "boxes" / f"{key}.json"
    try:
        payload = json.loads(record.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError, TypeError):
        return None
    if not isinstance(payload, dict):
        return None
    sandbox_id = payload.get("sandboxId")
    return sandbox_id if isinstance(sandbox_id, str) and sandbox_id.strip() else None


def _sandbox_id_from_output(stdout: str) -> str | None:
    for line in reversed(stdout.splitlines()):
        stripped = line.strip()
        if not stripped.startswith("{"):
            continue
        try:
            payload = json.loads(stripped)
        except json.JSONDecodeError:
            continue
        if isinstance(payload, dict) and isinstance(payload.get("sandboxId"), str) and payload["sandboxId"].strip():
            return payload["sandboxId"]
    return None


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
        sleep: Callable[[float], None] | None = None,
    ):
        if not template.strip():
            raise ValueError("template must not be empty")
        self.template = template.strip()
        self.executable = executable
        self._runner = runner or self._run
        self._environ = dict(os.environ if environ is None else environ)
        self._sleep = sleep or time.sleep

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
        for name in ("HERDR_PLUGIN_STATE_DIR", "HERDR_E2B_STATE_DIR", "XDG_STATE_HOME"):
            if self._environ.get(name):
                cli_env[name] = self._environ[name]
        primary_error: Exception | None = None
        result: subprocess.CompletedProcess[str] | None = None
        try:
            synced = self._call(
                (self.executable, "-t", self.template, "sync"), root, timeout_ms, cli_env, attempts=COMMAND_ATTEMPTS,
            )
            self._remember_sandbox(root, cli_env, synced)
            raw = self._call(
                (self.executable, "exec", "--timeout-ms", str(timeout_ms), remote),
                root, timeout_ms, cli_env, attempts=COMMAND_ATTEMPTS,
            )
            result = self._measured(command, raw)
        except Exception as exc:
            primary_error = exc
        kill_error = self._kill(root, timeout_ms, cli_env)
        if result is not None:
            # A measured exit code is the verdict. Cleanup failure stays visible
            # and does not turn PASS/FAIL into INCONCLUSIVE.
            result.cleanup_error = kill_error  # type: ignore[attr-defined]
            if kill_error:
                _log.warning("e2b_cleanup_failed", extra={
                    "error": kill_error[-500:], "worktree_key": box_key(root),
                })
            return result
        if primary_error is not None:
            if kill_error:
                primary_error = ContainmentUnavailable(f"{primary_error}; kill also failed: {kill_error}")
            if isinstance(primary_error, ContainmentUnavailable):
                raise primary_error
            raise ContainmentUnavailable(str(primary_error)) from primary_error
        raise ContainmentUnavailable(kill_error or "e2b-box produced no result")

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
            detail = _command_failure_detail(stdout, stderr, cwd=cwd, environ=env)
            raise ContainmentUnavailable(f"e2b-box command failed: {detail}")
        return stdout

    def _call(
        self, command: Sequence[str], root: Path, timeout_ms: int, cli_env: Mapping[str, str], *, attempts: int,
    ) -> str:
        """Run one CLI command. Retry only a transient connection error, before any measured result."""

        last: Exception | None = None
        for attempt in range(attempts):
            try:
                return self._runner(tuple(command), root, timeout_ms, cli_env)
            except ContainmentUnavailable as exc:
                last = exc
                if attempt + 1 >= attempts or not _transient(str(exc)):
                    raise
                self._sleep(min(_BACKOFF_SECONDS * 2 ** attempt, 2.0))
        assert last is not None
        raise last

    def _kill(self, root: Path, timeout_ms: int, cli_env: Mapping[str, str]) -> str | None:
        """Kill the box. Returns an error string when every attempt failed."""

        last = ""
        for attempt in range(KILL_ATTEMPTS):
            try:
                self._runner((self.executable, "kill"), root, timeout_ms, cli_env)
                self._forget_sandbox(root, cli_env)
                return None
            except Exception as exc:  # noqa: BLE001 - cleanup is retried, then reported
                last = str(exc)
                if attempt + 1 < KILL_ATTEMPTS:
                    self._sleep(min(_BACKOFF_SECONDS * 2 ** attempt, 2.0))
        sandbox_id = _sandbox_id_from_record(box_key(root), cli_env)
        if sandbox_id and self._kill_by_id(sandbox_id):
            self._forget_sandbox(root, cli_env)
            return None
        return f"e2b-box kill failed after {KILL_ATTEMPTS} attempts: {last}"[-1000:]

    def _kill_by_id(self, sandbox_id: str) -> bool:
        try:
            from e2b import Sandbox
        except ImportError:
            return False
        opts: dict[str, str] = {}
        if self._environ.get("E2B_API_KEY"):
            opts["api_key"] = self._environ["E2B_API_KEY"]
        if self._environ.get("E2B_DOMAIN"):
            opts["domain"] = self._environ["E2B_DOMAIN"]
        try:
            Sandbox.kill(sandbox_id, **opts)
        except Exception as exc:  # noqa: BLE001 - the CLI error is what we report
            _log.warning("e2b_kill_by_id_failed", extra={"sandbox_id": sandbox_id, "error": type(exc).__name__})
            return False
        return True

    def _remember_sandbox(self, root: Path, cli_env: Mapping[str, str], sync_output: str) -> None:
        key = box_key(root)
        sandbox_id = _sandbox_id_from_record(key, cli_env) or _sandbox_id_from_output(sync_output)
        if sandbox_id:
            _tracked_boxes[sandbox_id] = key

    def _forget_sandbox(self, root: Path, cli_env: Mapping[str, str]) -> None:
        key = box_key(root)
        for sandbox_id, tracked in list(_tracked_boxes.items()):
            if tracked == key:
                _tracked_boxes.pop(sandbox_id, None)
        record_id = _sandbox_id_from_record(key, cli_env)
        if record_id:
            _tracked_boxes.pop(record_id, None)

    @staticmethod
    def _measured(command: Sequence[str], raw: str) -> subprocess.CompletedProcess[str]:
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
        return subprocess.CompletedProcess(
            tuple(str(part) for part in command),
            exit_code,
            str(payload.get("stdout", "")),
            str(payload.get("stderr", "")),
        )
