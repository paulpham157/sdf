"""Disposable fixture containment for one SDF Attempt.

The fixture sandbox is deliberately smaller than an operating-system sandbox.
It gives SDF a trusted boundary for direct filesystem operations, bounded
process execution and an explicit network capability seam.  Callers must
provide a disposable fixture root; this module never grants access to a real
repository.  OS-level filesystem isolation for arbitrary child code is not
claimed by the portable implementation and remains a deployment concern.
"""

from __future__ import annotations

import os
import signal
import subprocess
import threading
import time
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any


class SandboxViolation(PermissionError):
    """A requested fixture operation would leave the sandbox boundary."""


class NetworkAccessDenied(SandboxViolation):
    """Network access was not explicitly enabled for this fixture."""


NetworkExecutor = Callable[..., Any]


@dataclass(frozen=True, slots=True)
class ProcessResult:
    """Bounded observation of a child process run."""

    command: tuple[str, ...]
    cwd: Path
    pid: int
    returncode: int | None
    stdout: str
    stderr: str
    timed_out: bool
    output_truncated: bool
    cleanup_completed: bool
    duration_seconds: float

    @property
    def exit_code(self) -> int | None:
        """Compatibility name used by the rest of SDF's process artifacts."""

        return self.returncode


class _BoundedOutput:
    """Drain a pipe while retaining at most ``limit`` bytes."""

    def __init__(self, limit: int):
        self.limit = limit
        self._data = bytearray()
        self._truncated = False
        self._lock = threading.Lock()

    def append(self, chunk: bytes) -> None:
        with self._lock:
            remaining = self.limit - len(self._data)
            if remaining > 0:
                self._data.extend(chunk[:remaining])
            if len(chunk) > max(remaining, 0):
                self._truncated = True

    def text(self) -> str:
        with self._lock:
            return bytes(self._data).decode("utf-8", errors="replace")

    @property
    def truncated(self) -> bool:
        with self._lock:
            return self._truncated


class FixtureSandbox:
    """Contain direct fixture actions for a single disposable Attempt.

    Filesystem methods resolve paths under ``root`` and reject absolute paths,
    traversal outside the root and symlink escapes.  ``run`` always starts in
    a validated fixture directory, drains output with a byte limit and kills
    a timed-out process group.  Network access is denied unless a caller opts
    in with both ``allow_network=True`` and an injected executor.

    The child process boundary is intentionally explicit: portable Python can
    bound lifecycle and output, but cannot provide OS-level filesystem or
    network isolation for arbitrary native child code.  Deployments requiring
    that stronger guarantee must provide an OS sandbox backend before running
    an untrusted agent.
    """

    def __init__(
        self,
        root: Path,
        *,
        timeout_seconds: float = 5.0,
        max_output_bytes: int = 64 * 1024,
        allow_network: bool = False,
        network_executor: NetworkExecutor | None = None,
    ) -> None:
        if timeout_seconds <= 0:
            raise ValueError("timeout_seconds must be positive")
        if max_output_bytes <= 0:
            raise ValueError("max_output_bytes must be positive")

        self.root = Path(root).expanduser().resolve()
        self.root.mkdir(parents=True, exist_ok=True)
        if not self.root.is_dir():
            raise ValueError("sandbox root must be a directory")
        self.timeout_seconds = timeout_seconds
        self.max_output_bytes = max_output_bytes
        self.allow_network = allow_network
        self.network_executor = network_executor

    def resolve(self, relative: str | Path, *, allow_root: bool = True) -> Path:
        """Resolve a fixture path, rejecting traversal and symlink escapes."""

        candidate_input = Path(relative)
        if candidate_input.is_absolute():
            raise SandboxViolation(f"absolute path is not allowed: {candidate_input}")
        try:
            candidate = (self.root / candidate_input).resolve(strict=False)
            candidate.relative_to(self.root)
        except (OSError, ValueError) as exc:
            raise SandboxViolation(f"path is outside sandbox: {relative}") from exc
        if not allow_root and candidate == self.root:
            raise SandboxViolation("sandbox root is not a file target")
        return candidate

    def read_bytes(self, relative: str | Path) -> bytes:
        return self.resolve(relative, allow_root=False).read_bytes()

    def read_text(self, relative: str | Path, *, encoding: str = "utf-8") -> str:
        return self.resolve(relative, allow_root=False).read_text(encoding=encoding)

    def write_bytes(self, relative: str | Path, data: bytes) -> Path:
        target = self.resolve(relative, allow_root=False)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
        return target

    def write_text(
        self,
        relative: str | Path,
        data: str,
        *,
        encoding: str = "utf-8",
    ) -> Path:
        target = self.resolve(relative, allow_root=False)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(data, encoding=encoding)
        return target

    def mkdir(self, relative: str | Path) -> Path:
        target = self.resolve(relative, allow_root=False)
        target.mkdir(parents=True, exist_ok=False)
        return target

    def exists(self, relative: str | Path) -> bool:
        return self.resolve(relative).exists()

    def request_network(
        self,
        url: str,
        *,
        method: str = "GET",
        headers: Mapping[str, str] | None = None,
        body: bytes = b"",
    ) -> Any:
        """Call an explicitly injected network fixture, or fail closed."""

        if not self.allow_network:
            raise NetworkAccessDenied("network is disabled for this sandbox")
        if self.network_executor is None:
            raise NetworkAccessDenied("network has no injected executor")
        return self.network_executor(
            url,
            method=method,
            headers=dict(headers or {}),
            body=body,
        )

    def run(
        self,
        command: Sequence[str],
        *,
        cwd: str | Path = ".",
        env: Mapping[str, str] | None = None,
        timeout_seconds: float | None = None,
        max_output_bytes: int | None = None,
    ) -> ProcessResult:
        """Run a bounded child process in a validated fixture directory.

        ``stdin`` is closed and the inherited environment is reduced to a
        small deterministic set.  A timeout terminates the process group and
        waits for the parent to be reaped before returning.
        """

        args = tuple(str(part) for part in command)
        if not args:
            raise ValueError("command must not be empty")
        if any("\x00" in part for part in args):
            raise ValueError("command arguments must not contain NUL bytes")
        run_cwd = self.resolve(cwd)
        if not run_cwd.is_dir():
            raise SandboxViolation(f"process cwd is not a directory: {cwd}")
        timeout = self.timeout_seconds if timeout_seconds is None else timeout_seconds
        output_limit = self.max_output_bytes if max_output_bytes is None else max_output_bytes
        if timeout <= 0:
            raise ValueError("timeout_seconds must be positive")
        if output_limit <= 0:
            raise ValueError("max_output_bytes must be positive")

        child_env = {
            "PATH": os.environ.get("PATH", ""),
            "HOME": str(self.root),
            "TMPDIR": str(self.root),
            "TMP": str(self.root),
            "TEMP": str(self.root),
            "PYTHONNOUSERSITE": "1",
            "SDF_SANDBOX_ROOT": str(self.root),
            "SDF_NETWORK": "allowed" if self.allow_network else "denied",
        }
        if env:
            child_env.update({str(key): str(value) for key, value in env.items()})
        # A caller-provided environment cannot silently turn network access on.
        child_env["SDF_NETWORK"] = "allowed" if self.allow_network else "denied"

        started_at = time.monotonic()
        process = subprocess.Popen(
            args,
            cwd=run_cwd,
            env=child_env,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=(os.name == "posix"),
            text=False,
        )
        assert process.stdout is not None
        assert process.stderr is not None
        stdout_capture = _BoundedOutput(output_limit)
        stderr_capture = _BoundedOutput(output_limit)
        readers = (
            threading.Thread(target=self._drain, args=(process.stdout, stdout_capture), daemon=True),
            threading.Thread(target=self._drain, args=(process.stderr, stderr_capture), daemon=True),
        )
        for reader in readers:
            reader.start()

        timed_out = False
        cleanup_completed = False
        try:
            process.wait(timeout=timeout)
            cleanup_completed = self._finish_readers(process, readers)
        except subprocess.TimeoutExpired:
            timed_out = True
            self._terminate_process(process)
            cleanup_completed = self._finish_readers(process, readers)
        finally:
            for stream in (process.stdout, process.stderr):
                try:
                    stream.close()
                except OSError:
                    pass
            for reader in readers:
                reader.join(timeout=0.5)
            cleanup_completed = cleanup_completed and not any(reader.is_alive() for reader in readers)

        return ProcessResult(
            command=args,
            cwd=run_cwd,
            pid=process.pid,
            returncode=process.returncode,
            stdout=stdout_capture.text(),
            stderr=stderr_capture.text(),
            timed_out=timed_out,
            output_truncated=stdout_capture.truncated or stderr_capture.truncated,
            cleanup_completed=cleanup_completed and process.poll() is not None,
            duration_seconds=time.monotonic() - started_at,
        )

    @staticmethod
    def _drain(stream: Any, capture: _BoundedOutput) -> None:
        try:
            while True:
                chunk = stream.read(8192)
                if not chunk:
                    return
                capture.append(chunk)
        except (OSError, ValueError):
            return

    def _finish_readers(
        self,
        process: subprocess.Popen[bytes],
        readers: tuple[threading.Thread, threading.Thread],
    ) -> bool:
        for reader in readers:
            reader.join(timeout=0.2)
        if not any(reader.is_alive() for reader in readers):
            return process.poll() is not None
        # A child may have inherited the pipe.  Terminate the private process
        # group before closing the streams and declaring cleanup complete.
        self._terminate_process(process)
        for reader in readers:
            reader.join(timeout=0.5)
        return process.poll() is not None and not any(reader.is_alive() for reader in readers)

    @staticmethod
    def _terminate_process(process: subprocess.Popen[bytes]) -> None:
        if process.poll() is not None and os.name != "posix":
            return
        if os.name == "posix":
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=0.2)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
        else:  # pragma: no cover - CI currently runs on POSIX
            process.terminate()
        try:
            process.wait(timeout=0.5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=0.5)
