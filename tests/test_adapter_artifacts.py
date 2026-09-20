from pathlib import Path

import pytest

from sdf_core.adapter import FakeNativeAdapter, RuntimeAgentAdapter
from sdf_core.artifacts import ArtifactStore, WorkspaceManager
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
