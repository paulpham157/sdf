from pathlib import Path

import pytest

from sdf_core.adapter import FakeNativeAdapter, RuntimeAgentAdapter
from sdf_core.artifacts import ArtifactStore, WorkspaceManager
from sdf_core.herdr_runtime import HerdrRuntime
from sdf_core.runtime import FakeRuntime, RuntimeSession, RuntimeStatus


class CompletingRuntime(FakeRuntime):
    def status(self, session_id: str) -> RuntimeSession:
        current = super().status(session_id)
        completed = RuntimeSession(
            current.session_id,
            current.attempt_id,
            current.agent,
            RuntimeStatus.COMPLETED,
            current.output,
        )
        self._sessions[session_id] = completed
        return completed


class WorkspaceBindingRuntime(CompletingRuntime):
    def __init__(self):
        super().__init__()
        self.bound = None

    def bind_workspace(self, attempt_id: str, workspace: Path) -> None:
        self.bound = (attempt_id, workspace)

    def send(self, session_id: str, input_text: str) -> RuntimeSession:
        assert self.bound is not None
        self.bound[1].joinpath("app.py").write_text("print('runtime')\n", encoding="utf-8")
        return super().send(session_id, input_text)


class CollectingRuntime(WorkspaceBindingRuntime):
    def __init__(self):
        super().__init__()
        self.collected = []
        self.terminated = []

    def collect_workspace(self, attempt_id: str, workspace: Path) -> None:
        self.collected.append((attempt_id, workspace))
        workspace.joinpath("app.py").write_text("print('collected')\n", encoding="utf-8")

    def terminate(self, session_id: str) -> RuntimeSession:
        self.terminated.append(session_id)
        return super().terminate(session_id)


def test_fake_native_adapter_changes_isolated_workspace_and_captures_immutable_diff(tmp_path: Path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    workspace = WorkspaceManager(tmp_path / "workspaces").create("ATTEMPT-001", fixture)

    result = FakeNativeAdapter("app.py", "print('new')\n").run(
        attempt_id="ATTEMPT-001", workspace=workspace, instructions="update greeting"
    )
    artifact = ArtifactStore(tmp_path / "artifacts").capture_diff(
        artifact_id="ARTIFACT-001", before=fixture, after=workspace, files=result.changed_files
    )

    assert result.status == "completed"
    assert "-print('old')" in artifact.path.read_text(encoding="utf-8")
    assert "+print('new')" in artifact.path.read_text(encoding="utf-8")
    assert len(artifact.sha256) == 64
    with pytest.raises(FileExistsError):
        ArtifactStore(tmp_path / "artifacts").capture_diff(
            artifact_id="ARTIFACT-001", before=fixture, after=workspace, files=result.changed_files
        )


def test_process_artifact_is_json_and_immutable(tmp_path: Path):
    store = ArtifactStore(tmp_path / "artifacts")
    artifact = store.capture_process(artifact_id="ARTIFACT-LOG-001", stdout="ok", stderr="", exit_code=0)
    assert '"exit_code": 0' in artifact.path.read_text(encoding="utf-8")
    with pytest.raises(FileExistsError):
        store.capture_process(artifact_id="ARTIFACT-LOG-001", stdout="changed", stderr="", exit_code=1)


def test_runtime_agent_adapter_maps_lifecycle_to_artifact_result(tmp_path: Path):
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    (workspace / "app.py").write_text("print('old')\n", encoding="utf-8")

    runtime = WorkspaceBindingRuntime()
    result = RuntimeAgentAdapter(runtime).run(
        attempt_id="ATTEMPT-RUNTIME-001",
        workspace=workspace,
        instructions="update greeting",
    )

    assert result.status == "completed"
    assert result.exit_code == 0
    assert result.changed_files == ("app.py",)
    assert result.stdout == "update greeting"
    assert runtime.bound == ("ATTEMPT-RUNTIME-001", workspace)


def test_runtime_agent_adapter_collects_workspace_before_diff_and_terminates_attempt_environment(tmp_path: Path):
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    (workspace / "app.py").write_text("print('old')\n", encoding="utf-8")
    runtime = CollectingRuntime()

    result = RuntimeAgentAdapter(runtime).run(
        attempt_id="ATTEMPT-RUNTIME-COLLECT", workspace=workspace, instructions="update greeting"
    )

    assert result.status == "completed"
    assert result.changed_files == ("app.py",)
    assert workspace.joinpath("app.py").read_text(encoding="utf-8") == "print('collected')\n"
    assert runtime.collected == [("ATTEMPT-RUNTIME-COLLECT", workspace)]
    assert runtime.terminated == ["SESSION-0001"]


def test_runtime_agent_adapter_stages_and_collects_attempt_workspace_through_herdr_transport(tmp_path: Path):
    class Transport:
        def __init__(self):
            self.staged = []
            self.collected = []
            self.closed = []

        def stage_workspace(self, attempt_id: str, workspace: Path) -> str:
            self.staged.append((attempt_id, workspace))
            return f"/sandbox/{attempt_id}"

        def collect_workspace(self, attempt_id: str, workspace: Path) -> None:
            self.collected.append((attempt_id, workspace))
            workspace.joinpath("app.py").write_text("print('remote')\n", encoding="utf-8")

        def close(self, timeout_ms: int) -> None:
            self.closed.append(timeout_ms)

        def run(self, command, timeout_ms):
            operation = tuple(command[1:3])
            if operation == ("workspace", "create"):
                assert tuple(command[-2:]) == ("--cwd", "/sandbox/ATTEMPT-STAGED")
                return '{"workspaceId":"ws-staged","paneId":"pane-staged"}'
            if operation == ("agent", "start"):
                assert command[3] == "codex"
                return '{"agentSessionId":"agent-staged","status":"working"}'
            if operation == ("agent", "prompt"):
                return '{"status":"working"}'
            if operation == ("agent", "read"):
                return '{"output":"remote update"}'
            if operation == ("agent", "get"):
                return '{"status":"done"}'
            if operation == ("pane", "close"):
                return '{"type":"ok"}'
            if operation == ("pane", "process-info"):
                return '{"process_info":{"shell_pid":10,"foreground_processes":[{"pid":10,"name":"sh"}]}}'
            raise AssertionError(command)

    workspace = tmp_path / "workspace"
    workspace.mkdir()
    workspace.joinpath("app.py").write_text("print('old')\n", encoding="utf-8")
    transport = Transport()
    result = RuntimeAgentAdapter(HerdrRuntime(transport=transport), agent="codex").run(
        attempt_id="ATTEMPT-STAGED", workspace=workspace, instructions="update app"
    )

    assert result.status == "completed"
    assert result.changed_files == ("app.py",)
    assert transport.staged == [("ATTEMPT-STAGED", workspace)]
    assert transport.collected == [("ATTEMPT-STAGED", workspace)]
    assert transport.closed == [30_000]
