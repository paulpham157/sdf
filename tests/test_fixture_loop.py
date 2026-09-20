from pathlib import Path

from sdf_core.db import Base, RuntimeEventRow, make_engine
from sdf_core.fixture_loop import AgentFixtureLoop
from sdf_core.runtime import FakeRuntime, RuntimeController, RuntimeSession, RuntimeStatus, SqlAlchemyRuntimeEventSink
from sqlalchemy.orm import sessionmaker


class EditingRuntime(FakeRuntime):
    def __init__(self, workspace: Path):
        super().__init__()
        self.workspace = workspace

    def send(self, session_id: str, input_text: str) -> RuntimeSession:
        self.workspace.joinpath("app.py").write_text("print('updated')\n", encoding="utf-8")
        current = super().send(session_id, input_text)
        completed = RuntimeSession(current.session_id, current.attempt_id, current.agent, RuntimeStatus.COMPLETED, current.output)
        self._sessions[session_id] = completed
        return completed


def test_fixture_loop_evaluator_acceptance_is_independent_of_runtime_status(tmp_path: Path):
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    (workspace / "app.py").write_text("print('old')\n", encoding="utf-8")
    result = AgentFixtureLoop(EditingRuntime(workspace)).run(
        attempt_id="ATTEMPT-FIXTURE-001",
        agent="fake-codex",
        workspace=workspace,
        instructions="update app",
        criteria=("tests pass",),
        criterion_checks={"tests pass": [["python", "-c", "print('pass')"]]},
    )

    assert result.runtime_completed is True
    assert result.accepted is True
    assert result.evaluation.status == "PASS"
    assert result.output == ("update app",)


def test_fixture_loop_does_not_accept_runtime_done_when_evaluator_fails(tmp_path: Path):
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    result = AgentFixtureLoop(EditingRuntime(workspace)).run(
        attempt_id="ATTEMPT-FIXTURE-002",
        agent="fake-codex",
        workspace=workspace,
        instructions="break app",
        criteria=("tests pass",),
        criterion_checks={"tests pass": [["python", "-c", "raise SystemExit(2)"]]},
    )

    assert result.runtime_completed is True
    assert result.accepted is False
    assert result.evaluation.status == "FAIL"


def test_fixture_loop_rejects_missing_workspace(tmp_path: Path):
    runtime = FakeRuntime()
    try:
        AgentFixtureLoop(runtime).run(
            attempt_id="ATTEMPT-FIXTURE-003", agent="fake", workspace=tmp_path / "missing", instructions="noop"
        )
    except ValueError as exc:
        assert "existing directory" in str(exc)
    else:  # pragma: no cover
        raise AssertionError("missing workspace should be rejected")


def test_fixture_loop_accepts_runtime_controller_and_persists_lifecycle_events(tmp_path: Path):
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        controller = RuntimeController(
            EditingRuntime(workspace),
            event_sink=SqlAlchemyRuntimeEventSink(db),
        )
        result = AgentFixtureLoop(controller).run(
            attempt_id="ATTEMPT-FIXTURE-EVENTS",
            agent="fake-codex",
            workspace=workspace,
            instructions="update app",
            criteria=("tests pass",),
            criterion_checks={"tests pass": [["python", "-c", "print('pass')"]]},
        )
        db.commit()
        assert result.accepted is True
        assert [row.kind for row in db.query(RuntimeEventRow).all()] == [
            "runtime_started", "runtime_input_sent", "runtime_output_observed"
        ]
