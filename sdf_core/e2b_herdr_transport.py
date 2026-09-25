"""E2B transport for a persistent Herdr control session.

The SDF process remains the control plane. This transport creates one
long-lived E2B sandbox through the E2B Python SDK, runs Herdr CLI commands
inside it, and exposes an explicit ``close`` operation for the Attempt owner.
It is deliberately separate from ``E2BContainmentBackend.run()``, whose
sync/exec/pull/kill loop is intentionally one-shot and cannot support
prompt/reconnect semantics.

Every command carries an explicit command timeout and request timeout. envd
runs a command on a context decoupled from its stream, so cancelling the
stream does not stop the remote process (upstream e2b #1877); a timed-out
command therefore kills the whole sandbox.
"""

from __future__ import annotations

import base64
import io
import os
import re
import shlex
import shutil
import tarfile
import tempfile
from concurrent.futures import ThreadPoolExecutor
from concurrent.futures import TimeoutError as FutureTimeout
from pathlib import Path
from typing import Any, Callable, Mapping, Sequence

from e2b import CommandExitException, Sandbox, SandboxException, TimeoutException

from .herdr_runtime import HerdrRuntimeError

SandboxFactory = Callable[..., Any]
SandboxConnector = Callable[..., Any]
_ARCHIVE_LIMIT = 8 * 1024 * 1024

class E2BHerdrTransport:
    """Run Herdr control commands in one persistent E2B sandbox."""

    def __init__(
        self,
        *,
        template: str,
        herdr_binary: str = "herdr",
        timeout_seconds: int = 900,
        request_timeout_seconds: float = 30.0,
        environ: Mapping[str, str] | None = None,
        envs: Mapping[str, str] | None = None,
        sandbox_factory: SandboxFactory | None = None,
        sandbox_connector: SandboxConnector | None = None,
        sandbox_id: str | None = None,
    ) -> None:
        if not template.strip():
            raise ValueError("E2B Herdr template must be non-empty")
        if timeout_seconds <= 0:
            raise ValueError("E2B Herdr timeout must be positive")
        if request_timeout_seconds <= 0:
            raise ValueError("E2B Herdr request timeout must be positive")
        self.template = template.strip()
        self.herdr_binary = herdr_binary
        self.timeout_seconds = timeout_seconds
        self.request_timeout_seconds = request_timeout_seconds
        self._environ = dict(os.environ if environ is None else environ)
        self._envs = dict(envs or {})
        self._factory = sandbox_factory or Sandbox.create
        self._connector = sandbox_connector or Sandbox.connect
        self._sandbox_id = sandbox_id
        self._sandbox: Any = None

    @property
    def sandbox_id(self) -> str | None:
        return self._sandbox_id

    def run(self, command: Sequence[str], timeout_ms: int) -> str:
        if not command:
            raise ValueError("Herdr command must not be empty")
        return self._exec(shlex.join([self.herdr_binary, *command[1:]]), timeout_ms)

    def close(self, timeout_ms: int = 30_000) -> None:
        """Kill the persistent sandbox and forget its provider identity."""

        if self._sandbox_id is None:
            return
        try:
            sandbox = self._ensure_sandbox()
            sandbox.kill(request_timeout=timeout_ms / 1000)
        finally:
            self._sandbox = None
            self._sandbox_id = None

    def stage_workspace(self, attempt_id: str, workspace: Path) -> str:
        """Copy a bounded local fixture into this Attempt's sandbox path."""

        source = Path(workspace).expanduser().resolve()
        if not source.is_dir():
            raise ValueError("workspace must be an existing directory")
        remote = self._workspace_path(attempt_id)
        archive = io.BytesIO()
        with tarfile.open(fileobj=archive, mode="w:gz") as tar:
            for path in sorted(source.rglob("*")):
                if path.is_symlink():
                    raise ValueError("workspace must not contain symlinks")
                if path.is_file():
                    tar.add(path, arcname=str(path.relative_to(source)), recursive=False)
        payload = archive.getvalue()
        if len(payload) > _ARCHIVE_LIMIT:
            raise ValueError("workspace archive exceeds the 8 MiB transport limit")
        timeout_ms = self.timeout_seconds * 1000
        remote_archive = f"{remote}.stage.tar.gz"
        sandbox = self._ensure_sandbox()
        try:
            sandbox.files.write(remote_archive, payload, request_timeout=self.request_timeout_seconds)
        except SandboxException as exc:
            raise HerdrRuntimeError(f"E2B workspace staging failed: {exc}") from exc
        self._exec(
            'mkdir -p {0} && tar -xzf {1} -C {0} && rm -f {1}'.format(
                shlex.quote(remote), shlex.quote(remote_archive)
            ),
            timeout_ms,
        )
        return remote

    def collect_workspace(self, attempt_id: str, workspace: Path) -> None:
        """Replace a disposable local workspace with the sandbox result."""

        destination = Path(workspace).expanduser().resolve()
        if not destination.is_dir():
            raise ValueError("workspace must be an existing directory")
        remote = self._workspace_path(attempt_id)
        encoded = self._exec(
            f"tar -C {shlex.quote(remote)} -czf - . | base64 -w 0", self.timeout_seconds * 1000
        )
        try:
            payload = base64.b64decode(encoded.strip(), validate=True)
        except ValueError as exc:
            raise HerdrRuntimeError("E2B workspace collection returned invalid base64") from exc
        staging = Path(tempfile.mkdtemp(prefix="sdf-e2b-collect-", dir=destination.parent))
        try:
            with tarfile.open(fileobj=io.BytesIO(payload), mode="r:gz") as tar:
                members = tar.getmembers()
                for member in members:
                    relative = Path(member.name)
                    if relative.is_absolute() or ".." in relative.parts or not (member.isdir() or member.isfile()):
                        raise HerdrRuntimeError("E2B workspace archive contains an unsafe member")
                tar.extractall(staging, members=members, filter="data")
            for child in destination.iterdir():
                if child.is_dir():
                    shutil.rmtree(child)
                else:
                    child.unlink()
            for child in staging.iterdir():
                shutil.move(str(child), destination / child.name)
        finally:
            shutil.rmtree(staging, ignore_errors=True)

    def _ensure_sandbox(self) -> Any:
        if self._sandbox is not None:
            return self._sandbox
        opts = self._api_opts()
        try:
            if self._sandbox_id is not None:
                self._sandbox = self._connector(self._sandbox_id, **opts)
            else:
                self._sandbox = self._factory(
                    template=self.template, timeout=self.timeout_seconds, envs=dict(self._envs), **opts
                )
        except SandboxException as exc:
            raise HerdrRuntimeError(f"E2B sandbox provisioning failed: {exc}") from exc
        self._sandbox_id = self._sandbox.sandbox_id
        return self._sandbox

    def _exec(self, cmd: str, timeout_ms: int) -> str:
        sandbox = self._ensure_sandbox()
        timeout = timeout_ms / 1000
        # The SDK enforces ``timeout`` on the stream; the host watchdog also
        # bounds a stalled request so neither can hang an Attempt.
        pool = ThreadPoolExecutor(max_workers=1)
        future = pool.submit(
            sandbox.commands.run, cmd, timeout=timeout, request_timeout=self.request_timeout_seconds
        )
        try:
            result = future.result(timeout=timeout + self.request_timeout_seconds)
        except (TimeoutException, FutureTimeout) as exc:
            self._kill_after_timeout()
            raise HerdrRuntimeError("E2B Herdr command timed out; sandbox killed") from exc
        except CommandExitException as exc:
            raise HerdrRuntimeError(f"E2B Herdr command failed: {(exc.stderr or '')[-1000:]}") from exc
        except SandboxException as exc:
            raise HerdrRuntimeError(f"E2B Herdr command failed: {exc}") from exc
        finally:
            pool.shutdown(wait=False)
        return result.stdout

    def _kill_after_timeout(self) -> None:
        try:
            self.close(int(self.request_timeout_seconds * 1000))
        except Exception:  # noqa: BLE001 - the timeout is the error to report
            pass

    @staticmethod
    def _workspace_path(attempt_id: str) -> str:
        if not re.fullmatch(r"[A-Za-z0-9_-]+", attempt_id):
            raise ValueError("attempt_id must contain only letters, digits, underscores, or hyphens")
        return f"/tmp/sdf/{attempt_id}"

    def _api_opts(self) -> dict[str, Any]:
        api_key = self._environ.get("E2B_API_KEY")
        if not api_key:
            raise HerdrRuntimeError("E2B Herdr transport requires E2B_API_KEY")
        opts: dict[str, Any] = {"api_key": api_key, "request_timeout": self.request_timeout_seconds}
        domain = self._environ.get("E2B_DOMAIN")
        if domain:
            opts["domain"] = domain
        return opts
