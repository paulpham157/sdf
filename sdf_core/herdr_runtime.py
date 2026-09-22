"""Provider adapter for the documented Herdr CLI surface.

This module intentionally keeps the provider boundary small and explicit. It
maps the pinned Herdr CLI lifecycle and verifies pane-level cleanup through
process-info; it does not pretend that Herdr provides SDF policy or sandbox
containment.
"""

from __future__ import annotations

import json
import re
import subprocess
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Mapping, Protocol, Sequence

from .runtime import AgentRuntime, RuntimeSession, RuntimeStatus


class HerdrRuntimeError(RuntimeError):
    """The provider boundary returned an unusable response."""


class HerdrUnsupportedOperation(HerdrRuntimeError):
    """Herdr does not provide a verified operation for this lifecycle call."""


HerdrRunner = Callable[[Sequence[str], int], str]


class HerdrTransport(Protocol):
    """Control-plane transport for a Herdr endpoint.

    The transport runs Herdr control commands in the execution environment;
    the SDF process remains the caller and never becomes the agent runtime.
    """

    def run(self, command: Sequence[str], timeout_ms: int) -> str: ...


class SshHerdrTransport:
    """Run Herdr CLI commands on a remote execution host over SSH.

    This is an explicit transport seam, not a claim that SSH itself provides
    sandboxing. The remote host must run the pinned Herdr service and enforce
    the selected sandbox boundary (for example an E2B-backed worker).
    """

    def __init__(
        self,
        *,
        host: str,
        user: str | None = None,
        port: int | None = None,
        herdr_binary: str = "herdr",
        identity_file: Path | None = None,
        executor: HerdrRunner | None = None,
    ) -> None:
        if not host.strip():
            raise ValueError("remote Herdr host must be non-empty")
        if port is not None and not 0 < port < 65536:
            raise ValueError("remote Herdr port must be between 1 and 65535")
        if not herdr_binary.strip():
            raise ValueError("remote Herdr binary must be non-empty")
        self.host = host
        self.user = user
        self.port = port
        self.herdr_binary = herdr_binary
        self.identity_file = identity_file
        self._executor = executor or self._run

    def run(self, command: Sequence[str], timeout_ms: int) -> str:
        remote = [self.herdr_binary, *command[1:]] if command else [self.herdr_binary]
        target = f"{self.user}@{self.host}" if self.user else self.host
        ssh = ["ssh"]
        if self.port is not None:
            ssh.extend(("-p", str(self.port)))
        if self.identity_file is not None:
            ssh.extend(("-i", str(self.identity_file.expanduser())))
        return self._executor([*ssh, target, *remote], timeout_ms)

    @staticmethod
    def _run(command: Sequence[str], timeout_ms: int) -> str:
        process = subprocess.run(
            command,
            capture_output=True,
            text=True,
            timeout=timeout_ms / 1000,
        )
        if process.returncode != 0:
            raise HerdrRuntimeError(f"remote Herdr command failed: {process.stderr[-1000:]}")
        return process.stdout


@dataclass(frozen=True, slots=True)
class HerdrProbeResult:
    """Local CLI capability observation; not proof of a running agent."""

    version: str | None
    sessions: tuple[Mapping[str, Any], ...]
    version_error: str | None = None
    session_error: str | None = None

    @property
    def cli_available(self) -> bool:
        return self.version is not None

    def matches(self, expected_version: str) -> bool:
        return self.version == expected_version

    def as_dict(self) -> dict[str, Any]:
        return {
            "version": self.version,
            "sessions": [dict(session) for session in self.sessions],
            "version_error": self.version_error,
            "session_error": self.session_error,
            "cli_available": self.cli_available,
        }


@dataclass(frozen=True, slots=True)
class _Binding:
    attempt_id: str
    agent: str
    workspace_id: str
    pane_id: str


@dataclass(frozen=True, slots=True)
class HerdrBindingSnapshot:
    """Durable identifiers required to restore a runtime after restart."""

    attempt_id: str
    session_id: str
    agent: str
    workspace_id: str
    pane_id: str

    def as_dict(self) -> dict[str, str]:
        return {
            "attempt_id": self.attempt_id,
            "session_id": self.session_id,
            "agent": self.agent,
            "workspace_id": self.workspace_id,
            "pane_id": self.pane_id,
        }

    @classmethod
    def from_mapping(cls, payload: Mapping[str, Any]) -> "HerdrBindingSnapshot":
        if not isinstance(payload, Mapping):
            raise TypeError("Herdr binding snapshot must be a mapping")
        values = {
            key: payload.get(key)
            for key in ("attempt_id", "session_id", "agent", "workspace_id", "pane_id")
        }
        if not all(isinstance(value, str) and value.strip() for value in values.values()):
            raise ValueError("Herdr binding snapshot fields must be non-empty strings")
        return cls(**values)


class HerdrRuntime(AgentRuntime):
    """Map SDF's internal runtime seam to Herdr's JSON CLI commands.

    The command forms follow the first-party CLI documentation, while the
    runner is injectable so all local tests remain provider-free.  A real
    deployment must pin and smoke-test the Herdr version before using this
    adapter for an Attempt.
    """

    def __init__(
        self,
        *,
        runner: HerdrRunner | None = None,
        transport: HerdrTransport | None = None,
        timeout_ms: int = 30_000,
        herdr_binary: str = "herdr",
        session: str | None = None,
        workspace_dir: Path | None = None,
        expected_version: str | None = None,
    ) -> None:
        if timeout_ms <= 0:
            raise ValueError("timeout_ms must be positive")
        if runner is not None and transport is not None:
            raise ValueError("configure runner or transport, not both")
        if transport is not None:
            self._runner = transport.run
        else:
            self._runner = runner or self._run
        self._timeout_ms = timeout_ms
        self._transport = transport
        self._binary = herdr_binary
        self._session = session
        self._workspace_dir = workspace_dir
        self.expected_version = expected_version
        self._bindings: dict[str, _Binding] = {}
        self._sessions: dict[str, RuntimeSession] = {}
        self._attempt_workspaces: dict[str, Path | str] = {}
        # An E2B transport owns a stronger final cleanup boundary than Herdr's
        # pane API.  Keep it optional so the deterministic local adapter does
        # not claim provider process control it does not have.
        self._environment_close = getattr(transport, "close", None) if transport is not None else None
        self._environment_closed_sessions: set[str] = set()

    def restore_binding(self, snapshot: HerdrBindingSnapshot) -> RuntimeSession:
        """Restore an Attempt/session binding without dispatching the agent."""

        for value, name in (
            (snapshot.attempt_id, "attempt_id"),
            (snapshot.session_id, "session_id"),
            (snapshot.agent, "agent"),
            (snapshot.workspace_id, "workspace_id"),
            (snapshot.pane_id, "pane_id"),
        ):
            if not isinstance(value, str) or not value.strip():
                raise ValueError(f"{name} must be non-empty")
        existing = self._bindings.get(snapshot.session_id)
        if existing is not None and (
            existing.attempt_id != snapshot.attempt_id
            or existing.workspace_id != snapshot.workspace_id
            or existing.pane_id != snapshot.pane_id
        ):
            raise HerdrRuntimeError("session binding conflicts with restored identity")
        bound_session = next(
            (session_id for session_id, binding in self._bindings.items()
             if binding.attempt_id == snapshot.attempt_id and session_id != snapshot.session_id),
            None,
        )
        if bound_session is not None:
            raise HerdrRuntimeError("attempt is already bound to a different restored session")
        session = self._sessions.get(snapshot.session_id)
        if session is None:
            session = RuntimeSession(
                snapshot.session_id,
                snapshot.attempt_id,
                snapshot.agent,
                RuntimeStatus.RUNNING,
            )
            self._sessions[snapshot.session_id] = session
        self._bindings[snapshot.session_id] = _Binding(
            snapshot.attempt_id,
            snapshot.agent,
            snapshot.workspace_id,
            snapshot.pane_id,
        )
        return session

    def bind_workspace(self, attempt_id: str, workspace: Path) -> None:
        """Bind the disposable SDF workspace before starting an Attempt."""

        if not isinstance(attempt_id, str) or not attempt_id.strip():
            raise ValueError("attempt_id must be non-empty")
        path = Path(workspace).expanduser().resolve()
        if not path.is_dir():
            raise ValueError("workspace must be an existing directory")
        existing = self._attempt_workspaces.get(attempt_id)
        if existing is not None and existing != path:
            raise HerdrRuntimeError("attempt is already bound to a different workspace")
        stage_workspace = getattr(self._transport, "stage_workspace", None)
        if callable(stage_workspace):
            self._attempt_workspaces[attempt_id] = stage_workspace(attempt_id, path)
            return
        self._attempt_workspaces[attempt_id] = path

    def collect_workspace(self, attempt_id: str, workspace: Path) -> None:
        """Copy a transport-owned Attempt workspace back for evaluation."""

        collect_workspace = getattr(self._transport, "collect_workspace", None)
        if callable(collect_workspace):
            collect_workspace(attempt_id, Path(workspace).expanduser().resolve())

    def bind_remote_workspace(self, attempt_id: str, workspace: str) -> None:
        """Bind a workspace path that exists in the execution environment.

        The control plane must not resolve or inspect this path locally. Herdr
        receives it through the configured transport and creates the agent pane
        there.
        """

        if not isinstance(attempt_id, str) or not attempt_id.strip():
            raise ValueError("attempt_id must be non-empty")
        if not isinstance(workspace, str) or not workspace.strip():
            raise ValueError("remote workspace must be non-empty")
        existing = self._attempt_workspaces.get(attempt_id)
        if existing is not None and str(existing) != workspace:
            raise HerdrRuntimeError("attempt is already bound to a different workspace")
        self._attempt_workspaces[attempt_id] = workspace

    def probe(self) -> HerdrProbeResult:
        """Probe installed binary identity and session-list JSON support."""

        version: str | None = None
        version_error: str | None = None
        try:
            raw_version = self._runner([self._binary, "--version"], self._timeout_ms)
            match = re.search(r"(?:^|\s)(\d+\.\d+\.\d+)(?:\s|$)", raw_version.strip())
            if match is None:
                raise HerdrRuntimeError("Herdr version response was not recognized")
            version = match.group(1)
        except Exception as exc:
            version_error = str(exc)

        sessions: tuple[Mapping[str, Any], ...] = ()
        session_error: str | None = None
        try:
            payload = self._object(self._command("session", "list", "--json"))
            values = payload.get("sessions", ())
            if not isinstance(values, list) or not all(isinstance(value, Mapping) for value in values):
                raise HerdrRuntimeError("Herdr session list did not contain session objects")
            sessions = tuple(values)
        except Exception as exc:
            session_error = str(exc)
        return HerdrProbeResult(version, sessions, version_error, session_error)

    def require_compatible(self) -> HerdrProbeResult:
        """Fail closed when a configured pinned version is not installed."""

        result = self.probe()
        if not result.cli_available:
            raise HerdrRuntimeError(result.version_error or "Herdr CLI is unavailable")
        if self.expected_version is not None and not result.matches(self.expected_version):
            raise HerdrRuntimeError(
                f"Herdr version mismatch: expected {self.expected_version}, found {result.version}"
            )
        return result

    def start(self, *, attempt_id: str, agent: str) -> RuntimeSession:
        if not attempt_id.strip() or not agent.strip():
            raise ValueError("attempt_id and agent must be non-empty")
        if self.expected_version is not None:
            self.require_compatible()
        existing_id = next((sid for sid, b in self._bindings.items() if b.attempt_id == attempt_id), None)
        if existing_id is not None:
            return self._sessions[existing_id]

        workspace_args = self._command("workspace", "create")
        workspace_dir = self._attempt_workspaces.get(attempt_id, self._workspace_dir)
        if workspace_dir is not None:
            workspace_args.extend(("--cwd", str(workspace_dir)))
        workspace = self._object(workspace_args)
        root_pane = workspace.get("root_pane", workspace.get("rootPane", workspace))
        if not isinstance(root_pane, Mapping):
            root_pane = workspace
        workspace_record = workspace.get("workspace", workspace)
        if not isinstance(workspace_record, Mapping):
            workspace_record = workspace
        workspace_id = self._required_string(workspace_record, "workspaceId", "workspace_id")
        pane_id = self._required_string(root_pane, "paneId", "pane_id")

        start_args = self._command("agent", "start", agent, "--kind", agent, "--pane", pane_id)
        started = self._object(start_args)
        started_agent = started.get("agent", started)
        if not isinstance(started_agent, Mapping):
            started_agent = started
        session_id = self._required_string(
            started,
            "agentSessionId",
            "sessionId",
        ) if any(key in started for key in ("agentSessionId", "sessionId")) else self._required_string(
            started_agent,
            "name",
            "agentName",
        )
        status = self._status(started_agent.get("status", started_agent.get("agent_status", started.get("status", "working"))))
        session = RuntimeSession(session_id, attempt_id, agent, status)
        self._bindings[session_id] = _Binding(attempt_id, agent, workspace_id, pane_id)
        self._sessions[session_id] = session
        return session

    def send(self, session_id: str, input_text: str) -> RuntimeSession:
        binding, current = self._known(session_id)
        args = self._command("agent", "prompt", session_id, input_text, "--wait", "--until", "idle")
        try:
            raw = self._raw(args)
        except HerdrRuntimeError as exc:
            # Herdr can time out its semantic idle observation after accepting
            # a prompt.  A foreground process is stronger evidence that work
            # was dispatched, so preserve the live binding as RUNNING and let
            # cancellation own the cleanup path instead of stranding it.
            if "agent_prompt_stalled" not in str(exc) or not self._has_foreground_child(binding):
                raise
            raw = ""
        payload: Mapping[str, Any] = {}
        if raw.strip():
            try:
                parsed = self._decode(raw)
                if isinstance(parsed, Mapping):
                    payload = parsed
            except HerdrRuntimeError:
                # The 0.9.x CLI returns an empty body for a successful prompt;
                # terminal output is read through ``agent read`` below.
                payload = {}
        status = self._status(payload.get("status", payload.get("agent_status", "working"))) if payload else RuntimeStatus.RUNNING
        output = self.stream(session_id)
        updated = RuntimeSession(current.session_id, binding.attempt_id, binding.agent, status, output or current.output)
        self._sessions[session_id] = updated
        return updated

    def stream(self, session_id: str) -> tuple[str, ...]:
        binding, current = self._known(session_id)
        args = self._command("agent", "read", session_id, "--source", "recent-unwrapped", "--lines", "200")
        raw = self._raw(args)
        try:
            payload = self._decode(raw)
        except HerdrRuntimeError:
            payload = None
        if isinstance(payload, Mapping):
            output = self._output(payload, current.output)
        else:
            output = tuple(line for line in raw.splitlines() if line.strip()) or current.output
        updated = RuntimeSession(current.session_id, binding.attempt_id, binding.agent, current.status, output)
        self._sessions[session_id] = updated
        return output

    def status(self, session_id: str) -> RuntimeSession:
        binding, current = self._known(session_id)
        args = self._command("agent", "get", session_id)
        payload = self._object(args)
        agent_payload = payload.get("agent", payload)
        if not isinstance(agent_payload, Mapping):
            agent_payload = payload
        updated = RuntimeSession(
            current.session_id,
            binding.attempt_id,
            binding.agent,
            self._status(agent_payload.get("status", agent_payload.get("agent_status", current.status.value))),
            self._output(payload, current.output),
        )
        self._sessions[session_id] = updated
        return updated

    def cancel(self, session_id: str) -> RuntimeSession:
        binding, current = self._known(session_id)
        # Herdr exposes validated key injection rather than a named cancel
        # method.  ctrl+c is therefore a cancellation request; only promote
        # it to CANCELLED after process inspection proves the agent executable
        # is no longer foreground in the pane.
        self._raw(self._command("agent", "send-keys", session_id, "ctrl+c"))
        try:
            self._wait_agent_not_foreground(binding)
        except HerdrRuntimeError:
            # ``ctrl+c`` is cooperative input, not process control.  If the
            # foreground process ignores it (or Herdr reports idle while the
            # process remains), closing the dedicated Attempt pane is the
            # documented hard-stop fallback.  Do not report CANCELLED until
            # the subsequent inspection proves the pane is gone/quiescent.
            self._close_pane(binding)
            self._wait_agent_not_foreground(binding)
        # A pane close cannot prove that a process intentionally detached from
        # the terminal has exited.  For an E2B-backed execution, tear down the
        # Attempt-owned sandbox after every cancellation: provider sandbox
        # destruction is the final descendant-cleanup boundary.
        self._close_execution_environment(session_id)
        updated = RuntimeSession(current.session_id, binding.attempt_id, binding.agent, RuntimeStatus.CANCELLED, current.output)
        self._sessions[session_id] = updated
        return updated

    def terminate(self, session_id: str) -> RuntimeSession:
        binding, current = self._known(session_id)
        # Closing the dedicated pane is the documented hard cleanup surface.
        # A successful close, or an already-closed pane, is the only evidence
        # accepted here; no terminal text is treated as proof of termination.
        if session_id not in self._environment_closed_sessions:
            self._close_pane(binding)
            try:
                self._wait_agent_not_foreground(binding)
            except HerdrRuntimeError as exc:
                if "not found" not in str(exc).lower() and "closed" not in str(exc).lower():
                    raise
            self._close_execution_environment(session_id)
        updated = RuntimeSession(current.session_id, binding.attempt_id, binding.agent, RuntimeStatus.TERMINATED, current.output)
        self._sessions[session_id] = updated
        return updated

    def _close_pane(self, binding: _Binding) -> None:
        """Close the Attempt-owned pane, accepting an idempotent close."""

        try:
            self._raw(self._command("pane", "close", binding.pane_id))
        except HerdrRuntimeError as exc:
            if "not found" not in str(exc).lower() and "closed" not in str(exc).lower():
                raise

    def _close_execution_environment(self, session_id: str) -> None:
        """Destroy an optional Attempt-owned provider environment once."""

        if session_id in self._environment_closed_sessions:
            return
        if callable(self._environment_close):
            self._environment_close(self._timeout_ms)
        self._environment_closed_sessions.add(session_id)

    def reconnect(self, session_id: str) -> RuntimeSession:
        binding, current = self._known(session_id)
        args = self._command("api", "snapshot")
        payload = self._object(args)
        snapshot = payload.get("snapshot", payload)
        if not isinstance(snapshot, Mapping):
            raise HerdrRuntimeError("Herdr snapshot did not contain a snapshot object")
        records = snapshot.get("agents", snapshot.get("sessions", []))
        if not isinstance(records, list) or not any(
            isinstance(record, Mapping)
            and (
                record.get("agentSessionId", record.get("sessionId")) == session_id
                or record.get("name") == session_id
                or record.get("pane_id", record.get("paneId")) == binding.pane_id
            )
            for record in records
        ):
            raise HerdrRuntimeError("session snapshot did not contain the requested agent session")
        return self.status(session_id)

    def _known(self, session_id: str) -> tuple[_Binding, RuntimeSession]:
        try:
            return self._bindings[session_id], self._sessions[session_id]
        except KeyError as exc:
            raise KeyError(f"unknown Herdr agent session: {session_id}") from exc

    def _wait_agent_not_foreground(self, binding: _Binding) -> None:
        deadline = time.monotonic() + self._timeout_ms / 1000
        while True:
            try:
                self._assert_agent_not_foreground(binding)
                return
            except HerdrRuntimeError as exc:
                if "not found" in str(exc).lower() or "closed" in str(exc).lower():
                    return
                if time.monotonic() >= deadline:
                    raise
                time.sleep(0.05)

    def _assert_agent_not_foreground(self, binding: _Binding) -> None:
        payload = self._object(self._command("pane", "process-info", "--pane", binding.pane_id))
        info = payload.get("process_info", payload)
        if not isinstance(info, Mapping):
            raise HerdrRuntimeError("Herdr process-info response was not an object")
        processes = info.get("foreground_processes", ())
        if not isinstance(processes, list):
            raise HerdrRuntimeError("Herdr process-info response omitted foreground_processes")
        shell_pid = info.get("shell_pid")
        if shell_pid is not None and not isinstance(shell_pid, int):
            raise HerdrRuntimeError("Herdr process-info shell_pid was not an integer")
        agent_name = binding.agent.lower()
        for process in processes:
            if not isinstance(process, Mapping):
                continue
            pid = process.get("pid")
            if shell_pid is not None and pid != shell_pid:
                raise HerdrRuntimeError("Herdr pane still has a foreground child process after cleanup")
            haystack = " ".join(
                str(process.get(key, ""))
                for key in ("name", "argv0", "cmdline")
            ).lower()
            if agent_name and agent_name in haystack:
                raise HerdrRuntimeError("Herdr agent process is still foreground after cancellation")

    def _has_foreground_child(self, binding: _Binding) -> bool:
        try:
            self._assert_agent_not_foreground(binding)
        except HerdrRuntimeError as exc:
            message = str(exc).lower()
            if "foreground child" in message or "agent process is still foreground" in message:
                return True
            raise
        return False

    def _object(self, command: Sequence[str]) -> dict[str, Any]:
        payload = self._decode(self._raw(command))
        if not isinstance(payload, dict):
            raise HerdrRuntimeError("Herdr response was not a JSON object")
        result = payload.get("result")
        if isinstance(result, dict):
            return result
        return payload

    def _raw(self, command: Sequence[str]) -> str:
        try:
            return self._runner(command, self._timeout_ms)
        except HerdrRuntimeError:
            raise
        except Exception as exc:
            raise HerdrRuntimeError(f"Herdr command failed: {exc}") from exc

    @staticmethod
    def _decode(raw: str) -> Any:
        try:
            return json.loads(raw)
        except (TypeError, json.JSONDecodeError) as exc:
            raise HerdrRuntimeError("Herdr response was not valid JSON") from exc

    def _command(self, *parts: str) -> list[str]:
        command = [self._binary]
        if self._session:
            command.extend(("--session", self._session))
        command.extend(parts)
        return command

    @staticmethod
    def _required_string(payload: Mapping[str, Any], *keys: str) -> str:
        for key in keys:
            value = payload.get(key)
            if isinstance(value, str) and value.strip():
                return value
        raise HerdrRuntimeError(f"Herdr response missing {' or '.join(keys)}")

    @staticmethod
    def _status(value: Any) -> RuntimeStatus:
        normalized = str(value).lower()
        if normalized in {"done", "completed", "complete"}:
            return RuntimeStatus.COMPLETED
        if normalized in {"cancelled", "canceled"}:
            return RuntimeStatus.CANCELLED
        if normalized in {"terminated", "closed", "exited"}:
            return RuntimeStatus.TERMINATED
        return RuntimeStatus.RUNNING

    @staticmethod
    def _output(payload: Mapping[str, Any], fallback: tuple[str, ...]) -> tuple[str, ...]:
        value = payload.get("output", payload.get("text"))
        if value is None:
            return fallback
        if isinstance(value, str):
            return (value,)
        if isinstance(value, list) and all(isinstance(item, str) for item in value):
            return tuple(value)
        raise HerdrRuntimeError("Herdr output must be a string or list of strings")

    def _run(self, command: Sequence[str], timeout_ms: int) -> str:
        process = subprocess.run(command, capture_output=True, text=True, timeout=timeout_ms / 1000)
        if process.returncode != 0:
            raise HerdrRuntimeError(f"Herdr command failed: {process.stderr[-1000:]}")
        return process.stdout
