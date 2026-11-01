"""Host crash between an Attempt starting and finishing.

Production startup is the FastAPI lifespan on ``sdf_core.api.app`` (authentication,
logging, and tracing). ADR 0029 records that ``RuntimeController.recover`` is not
called from that path, and the lifespan does not settle an Attempt left
``running`` when the process dies after ``ExecutionService.begin`` commits.
This test drives that startup and is intentionally red until a restarted host
recovers the orphan. Do not mark it xfail or skip.
"""

from pathlib import Path

from sdf_core.adapter import FakeNativeAdapter
from sdf_core.api import SessionLocal
from sdf_core.db import AttemptRow, TaskRow
from sdf_core.execution import ExecutionService
from sdf_core.model import utcnow
from tests.auth_support import admin_client

# Terminal states an abandoned Attempt may end in. The model has no separate
# "abandoned" status; cancellation is the abandon transition from running.
_ORPHAN_TERMINAL = frozenset({"failed", "cancelled"})


class CountingAdapter:
    """Fake runtime adapter; records how many times a host asks it to run."""

    provider = "fake"
    evidence_mode = "synthetic"

    def __init__(self) -> None:
        self.calls = 0
        self._fake = FakeNativeAdapter("app.py", "print('new')\n")

    def run(self, *, attempt_id: str, workspace: Path, instructions: str):
        self.calls += 1
        return self._fake.run(attempt_id=attempt_id, workspace=workspace, instructions=instructions)


def test_host_crash_mid_run_is_recovered_and_task_is_retryable(tmp_path: Path, monkeypatch) -> None:
    """An Attempt abandoned between begin and finish is retryable after startup.

    The process is modeled as dying after ``begin`` has committed the Attempt
    and the Task ``running`` and before the adapter runs: the session and the
    service are dropped, and the durable rows are what a new process would
    read. The restarted host is this app entered through its lifespan, the
    same startup uvicorn runs. The invariant is that the orphan is then
    failed or cancelled, and a new dispatch key creates a new Attempt and
    runs it.
    """

    monkeypatch.chdir(tmp_path)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    adapter = CountingAdapter()
    # The restarted host's POST /run builds its adapter here. The crash below
    # never reaches adapter.run.
    monkeypatch.setattr("sdf_core.api.configured_run_adapter", lambda _db: adapter)

    task_id = "TASK-HOST-CRASH"
    db = SessionLocal()
    try:
        db.add(TaskRow(
            id=task_id, title="crash mid-run", status="created",
            idempotency_key="task-host-crash", acceptance_criteria=[],
            created_at=utcnow(),
        ))
        db.commit()
        service = ExecutionService(
            db,
            workspace_root=tmp_path / ".sdf-workspaces",
            artifact_root=tmp_path / ".sdf-artifacts",
            adapter=adapter,
        )
        begun = service.begin(task_id=task_id, dispatch_key="dispatch-host-crash-1", fixture=fixture)
        orphan_id = begun.attempt.id
        assert begun.attempt.status == "running"
        assert db.get(TaskRow, task_id).status == "running"
        assert adapter.calls == 0
    finally:
        db.close()
    # In-memory execution state dies with the process. Do not call finish,
    # adapter.run, or RuntimeController.recover: production startup does not.
    del service

    # uvicorn startup. The lifespan is the recovery entrypoint a restarted
    # host actually runs; there is no other one.
    with admin_client() as restarted:
        retry = restarted.post(f"/tasks/{task_id}/run", json={
            "fixture_dir": str(fixture),
            "dispatch_key": "dispatch-host-crash-2",
            "instructions": "retry after host crash",
        })

    db = SessionLocal()
    try:
        orphan = db.get(AttemptRow, orphan_id)
        task = db.get(TaskRow, task_id)
        assert orphan is not None and task is not None
        assert orphan.status in _ORPHAN_TERMINAL, (
            "a restarted host must leave the attempt abandoned between begin and finish "
            f"in a terminal failed state; after startup it is {orphan.status!r} and the task is "
            f"{task.status!r}; a new dispatch returned {retry.status_code}: {retry.text}"
        )
        assert retry.status_code == 200, retry.text
        body = retry.json()
        assert body["attempt_id"] != orphan_id
        assert adapter.calls == 1
        started = db.get(AttemptRow, body["attempt_id"])
        assert started is not None and started.task_id == task_id
        assert started.status != "running"
    finally:
        db.close()
