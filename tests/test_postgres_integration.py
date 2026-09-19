import os
from pathlib import Path

import pytest
from sqlalchemy.orm import sessionmaker

from sdf_core.adapter import FakeNativeAdapter
from sdf_core.db import Base, GraphNodeRow, TaskRow, make_engine
from sdf_core.execution import ExecutionService
from sdf_core.model import utcnow


POSTGRES_URL = os.getenv("SDF_POSTGRES_TEST_URL")


@pytest.mark.skipif(not POSTGRES_URL, reason="SDF_POSTGRES_TEST_URL is not set")
def test_vertical_slice_runs_against_postgresql(tmp_path: Path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    engine = make_engine(POSTGRES_URL)
    Base.metadata.drop_all(engine)
    Base.metadata.create_all(engine)
    Session = sessionmaker(engine, expire_on_commit=False)
    with Session() as db:
        db.add(GraphNodeRow(id="ASSUMPTION-PG-001", kind="assumption", title="postgres works", source="test", owner="product", confidence=1.0, created_at=utcnow()))
        db.add(TaskRow(id="TASK-PG-001", title="postgres slice", status="created", idempotency_key="task-pg-001", acceptance_criteria=[], created_at=utcnow()))
        db.commit()
        service = ExecutionService(db, workspace_root=tmp_path / "workspaces", artifact_root=tmp_path / "artifacts", adapter=FakeNativeAdapter("app.py", "print('new')\n"))
        attempt = service.run(task_id="TASK-PG-001", dispatch_key="dispatch-pg-001", fixture=fixture, instructions="update", commands=[["python", "-c", "print('ok')"]], validation_target=("assumption", "ASSUMPTION-PG-001"))
        assert attempt.status == "completed"
        assert db.get(TaskRow, "TASK-PG-001").status == "succeeded"
