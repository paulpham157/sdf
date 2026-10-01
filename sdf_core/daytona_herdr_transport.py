"""Daytona transport for a persistent Herdr control session.

Parallel to ``E2BHerdrTransport``: the SDF process remains the control plane.
This transport creates one long-lived Daytona sandbox through the Daytona
Python SDK, runs Herdr CLI commands inside it, and exposes an explicit
``close`` that deletes the sandbox. Attempt workspaces move over the SDK
filesystem API (upload/list/download), never through shell pipelines.

Selected beside E2B via ``SDF_SANDBOX_PROVIDER=daytona`` (default remains
``e2b``). Unit tests inject a fake factory and never require ``DAYTONA_API_KEY``.
"""

from __future__ import annotations

import math
import os
import re
import shlex
import shutil
import tempfile
import threading
from pathlib import Path, PurePosixPath
from typing import Any, Callable, Mapping, Sequence

from .herdr_runtime import HerdrRuntimeError

SandboxFactory = Callable[..., Any]
SandboxConnector = Callable[..., Any]
_TRANSFER_LIMIT = 8 * 1024 * 1024
_COLLECT_DEPTH = 64


class DaytonaNotFoundError(Exception):
    """Remote path is missing; fakes and the live adapter raise this."""


class DaytonaHerdrTransport:
    """Run Herdr control commands in one persistent Daytona sandbox."""

    def __init__(
        self,
        *,
        snapshot: str,
        herdr_binary: str = "herdr",
        timeout_seconds: int = 900,
        request_timeout_seconds: float = 30.0,
        environ: Mapping[str, str] | None = None,
        envs: Mapping[str, str] | None = None,
        sandbox_factory: SandboxFactory | None = None,
        sandbox_connector: SandboxConnector | None = None,
        sandbox_id: str | None = None,
    ) -> None:
        if not snapshot.strip():
            raise ValueError("Daytona Herdr snapshot must be non-empty")
        if timeout_seconds <= 0:
            raise ValueError("Daytona Herdr timeout must be positive")
        if request_timeout_seconds <= 0:
            raise ValueError("Daytona Herdr request timeout must be positive")
        self.snapshot = snapshot.strip()
        self.herdr_binary = herdr_binary
        self.timeout_seconds = timeout_seconds
        self.request_timeout_seconds = request_timeout_seconds
        self._environ = dict(os.environ if environ is None else environ)
        self._envs = dict(envs or {})
        self._factory = sandbox_factory
        self._connector = sandbox_connector
        self._sandbox_id = sandbox_id
        self._sandbox: Any = None
        self._unidentified_sandbox: Any = None
        self._sandbox_lock = threading.RLock()

    @property
    def sandbox_id(self) -> str | None:
        return self._sandbox_id

    def run(self, command: Sequence[str], timeout_ms: int) -> str:
        if not command:
            raise ValueError("Herdr command must not be empty")
        return self._exec(shlex.join([self.herdr_binary, *command[1:]]), timeout_ms)

    def close(self, timeout_ms: int = 30_000) -> None:
        """Delete the persistent sandbox and forget its provider identity."""

        with self._sandbox_lock:
            if self._sandbox_id is None:
                if self._unidentified_sandbox is not None:
                    self._unidentified_sandbox.delete(timeout=timeout_ms / 1000)
                    self._unidentified_sandbox = None
                return
            try:
                sandbox = self._ensure_sandbox()
                sandbox.delete(timeout=timeout_ms / 1000)
            finally:
                self._sandbox = None
                self._sandbox_id = None

    def stage_workspace(self, attempt_id: str, workspace: Path) -> str:
        """Copy a bounded local workspace into this Attempt's sandbox directory."""

        source = Path(workspace).expanduser().resolve()
        if not source.is_dir():
            raise ValueError("workspace must be an existing directory")
        remote = self._workspace_path(attempt_id)
        files: list[tuple[str, bytes]] = []
        dirs: list[str] = []
        total = 0
        for path in sorted(source.rglob("*")):
            if path.is_symlink():
                raise ValueError("workspace must not contain symlinks")
            target = f"{remote}/{path.relative_to(source).as_posix()}"
            if path.is_dir():
                dirs.append(target)
            elif path.is_file():
                data = path.read_bytes()
                total += len(data)
                if total > _TRANSFER_LIMIT:
                    raise ValueError("workspace exceeds the 8 MiB transfer limit")
                files.append((target, data))
        fs = self._ensure_sandbox().fs
        try:
            try:
                fs.delete_file(remote, recursive=True)
            except DaytonaNotFoundError:
                pass
            except Exception as exc:
                if not _looks_missing(exc):
                    raise HerdrRuntimeError(f"Daytona workspace staging failed: {exc}") from exc
            fs.create_folder(remote, "755")
            for directory in dirs:
                fs.create_folder(directory, "755")
            if files:
                uploads = [_file_upload(data, destination) for destination, data in files]
                fs.upload_files(uploads)
        except HerdrRuntimeError:
            raise
        except Exception as exc:
            raise HerdrRuntimeError(f"Daytona workspace staging failed: {exc}") from exc
        return remote

    def collect_workspace(self, attempt_id: str, workspace: Path) -> None:
        """Replace a disposable local workspace with the sandbox's Attempt state."""

        destination = Path(workspace).expanduser().resolve()
        if not destination.is_dir():
            raise ValueError("workspace must be an existing directory")
        remote = self._workspace_path(attempt_id)
        fs = self._ensure_sandbox().fs
        staging = Path(tempfile.mkdtemp(prefix="sdf-daytona-collect-", dir=destination.parent))
        try:
            try:
                entries = self._list_tree(fs, remote)
                total = 0
                for path, is_dir in entries:
                    relative = self._relative_member(remote, path)
                    local = staging / relative
                    if is_dir:
                        local.mkdir(parents=True, exist_ok=True)
                    else:
                        data = bytes(fs.download_file(path))
                        total += len(data)
                        if total > _TRANSFER_LIMIT:
                            raise HerdrRuntimeError("Daytona workspace exceeds the 8 MiB transfer limit")
                        local.parent.mkdir(parents=True, exist_ok=True)
                        local.write_bytes(data)
            except HerdrRuntimeError:
                raise
            except Exception as exc:
                raise HerdrRuntimeError(f"Daytona workspace collection failed: {exc}") from exc
            for child in destination.iterdir():
                if child.is_dir() and not child.is_symlink():
                    shutil.rmtree(child)
                else:
                    child.unlink()
            for child in staging.iterdir():
                shutil.move(str(child), destination / child.name)
        finally:
            shutil.rmtree(staging, ignore_errors=True)

    @staticmethod
    def _relative_member(remote: str, path: str) -> PurePosixPath:
        relative = PurePosixPath(path).relative_to(remote) if path.startswith(f"{remote}/") else None
        if relative is None or not relative.parts or ".." in relative.parts:
            raise HerdrRuntimeError("Daytona workspace contains an unsafe entry")
        return relative

    def _list_tree(self, fs: Any, remote: str) -> list[tuple[str, bool]]:
        """Walk the Attempt directory via SDK list_files (no shell)."""

        ordered: list[tuple[str, bool]] = []
        stack: list[tuple[str, int]] = [(remote, 0)]
        while stack:
            path, depth = stack.pop()
            try:
                entries = fs.list_files(path)
            except Exception as exc:
                if path == remote:
                    raise
                raise HerdrRuntimeError(f"Daytona workspace collection failed: {exc}") from exc
            for entry in sorted(entries, key=lambda item: getattr(item, "name", "")):
                name = getattr(entry, "name", None)
                if not name or name in {".", ".."}:
                    raise HerdrRuntimeError("Daytona workspace contains an unsafe entry")
                child = f"{path.rstrip('/')}/{name}"
                if getattr(entry, "is_symlink", False):
                    raise HerdrRuntimeError("Daytona workspace contains an unsafe entry")
                is_dir = bool(getattr(entry, "is_dir", False))
                ordered.append((child, is_dir))
                if is_dir:
                    if depth + 1 >= _COLLECT_DEPTH:
                        raise HerdrRuntimeError("Daytona workspace is too deep to collect")
                    stack.append((child, depth + 1))
        return ordered

    def _ensure_sandbox(self) -> Any:
        with self._sandbox_lock:
            if self._unidentified_sandbox is not None:
                raise HerdrRuntimeError(
                    "Daytona sandbox cleanup is pending; close the transport before provisioning again"
                )
            if self._sandbox is not None:
                return self._sandbox
            factory, connector = self._providers()
            try:
                if self._sandbox_id is not None:
                    sandbox = connector(self._sandbox_id)
                else:
                    sandbox = factory(
                        snapshot=self.snapshot,
                        env_vars=dict(self._envs),
                        timeout=self.timeout_seconds,
                    )
            except HerdrRuntimeError:
                raise
            except Exception as exc:
                raise HerdrRuntimeError(f"Daytona sandbox provisioning failed: {exc}") from exc
            try:
                sandbox_id = _sandbox_id(sandbox)
            except HerdrRuntimeError as identity_error:
                self._unidentified_sandbox = sandbox
                try:
                    sandbox.delete(timeout=self.request_timeout_seconds)
                except Exception as cleanup_error:
                    raise HerdrRuntimeError(
                        "Daytona sandbox provisioning failed: returned sandbox has no valid id; "
                        "cleanup also failed"
                    ) from cleanup_error
                self._unidentified_sandbox = None
                self._sandbox_id = None
                raise HerdrRuntimeError(
                    "Daytona sandbox provisioning failed: returned sandbox has no valid id; "
                    "the unidentifiable sandbox was deleted"
                ) from identity_error
            # Publish the provider object and identity together only after both
            # are valid. Concurrent callers then share this exact sandbox.
            self._sandbox = sandbox
            self._sandbox_id = sandbox_id
            return sandbox

    def _providers(self) -> tuple[SandboxFactory, SandboxConnector]:
        if self._factory is not None and self._connector is not None:
            return self._factory, self._connector
        if self._factory is not None:
            return self._factory, self._connector or _missing_connector
        return _live_providers(self._environ)

    def _exec(self, cmd: str, timeout_ms: int) -> str:
        sandbox = self._ensure_sandbox()
        # Host watchdog uses the caller's bound; the SDK timeout is whole seconds.
        host_timeout = timeout_ms / 1000
        sdk_timeout = max(1, math.ceil(host_timeout))
        outcome: dict[str, Any] = {}

        def call() -> None:
            try:
                outcome["result"] = sandbox.process.exec(cmd, timeout=sdk_timeout)
            except BaseException as exc:  # noqa: BLE001 - re-raised on the caller's thread
                outcome["error"] = exc

        worker = threading.Thread(target=call, name="daytona-herdr-command", daemon=True)
        worker.start()
        worker.join(host_timeout + self.request_timeout_seconds)
        if worker.is_alive() or _is_timeout(outcome.get("error")):
            self._kill_after_timeout()
            worker.join(self.request_timeout_seconds)
            raise HerdrRuntimeError("Daytona Herdr command timed out; sandbox killed") from outcome.get("error")
        if "error" in outcome:
            raise HerdrRuntimeError(f"Daytona Herdr command failed: {outcome['error']}") from outcome["error"]
        result = outcome["result"]
        exit_code = int(getattr(result, "exit_code", 0) or 0)
        if exit_code != 0:
            detail = getattr(result, "result", "") or getattr(result, "stderr", "") or f"exit {exit_code}"
            raise HerdrRuntimeError(f"Daytona Herdr command failed: {str(detail)[-1000:]}")
        return str(getattr(result, "result", "") or "")

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


def _sandbox_id(sandbox: Any) -> str:
    identity = getattr(sandbox, "id", None) or getattr(sandbox, "sandbox_id", None)
    if (
        not isinstance(identity, str)
        or not identity.strip()
        or identity != identity.strip()
    ):
        raise HerdrRuntimeError("Daytona sandbox provisioning failed: missing or invalid sandbox id")
    return identity


def _missing_connector(sandbox_id: str, **_: Any) -> Any:
    raise HerdrRuntimeError(f"Daytona reconnect requires a sandbox_connector (id={sandbox_id})")


def _looks_missing(exc: BaseException) -> bool:
    text = str(exc).lower()
    return "not found" in text or "does not exist" in text or "no such" in text


def _is_timeout(exc: BaseException | None) -> bool:
    if exc is None:
        return False
    name = type(exc).__name__.lower()
    text = str(exc).lower()
    return "timeout" in name or "timeout" in text or "timed out" in text


def _file_upload(data: bytes, destination: str) -> Any:
    try:
        from daytona import FileUpload
    except ImportError:
        return {"source": data, "destination": destination}
    return FileUpload(source=data, destination=destination)


def _live_providers(environ: Mapping[str, str]) -> tuple[SandboxFactory, SandboxConnector]:
    api_key = environ.get("DAYTONA_API_KEY")
    if not api_key:
        raise HerdrRuntimeError("Daytona Herdr transport requires DAYTONA_API_KEY")
    try:
        from daytona import CreateSandboxFromSnapshotParams, Daytona, DaytonaConfig
    except ImportError as exc:
        raise HerdrRuntimeError(
            "Daytona Herdr transport requires the daytona package; install project dependencies"
        ) from exc

    config_kwargs: dict[str, Any] = {"api_key": api_key}
    if environ.get("DAYTONA_API_URL"):
        config_kwargs["api_url"] = environ["DAYTONA_API_URL"]
    if environ.get("DAYTONA_TARGET"):
        config_kwargs["target"] = environ["DAYTONA_TARGET"]
    client = Daytona(DaytonaConfig(**config_kwargs))

    def factory(*, snapshot: str, env_vars: Mapping[str, str], timeout: float, **_: Any) -> Any:
        params = CreateSandboxFromSnapshotParams(
            snapshot=snapshot,
            env_vars=dict(env_vars),
            auto_stop_interval=0,
        )
        return client.create(params, timeout=float(timeout))

    def connector(sandbox_id: str, **_: Any) -> Any:
        return client.get(sandbox_id)

    return factory, connector
