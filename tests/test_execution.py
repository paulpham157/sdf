from pathlib import Path

from sdf_core.adapter import FakeNativeAdapter
from sdf_core.db import Base, GraphNodeRow, TaskRow, make_engine
from sdf_core.evaluator import DeterministicEvaluator
from sdf_core.execution import ExecutionService
from sdf_core.model import utcnow
from sqlalchemy.orm import sessionmaker


def test_execution_closes_objective_to_evidence_loop(tmp_path: Path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    engine = make_engine()
    Base.metadata.create_all(engine)
    Session = sessionmaker(engine, expire_on_commit=False)
    with Session() as db:
        db.add(GraphNodeRow(id="ASSUMPTION-001", kind="assumption", title="health endpoint works", source="test", owner="product", confidence=1.0, created_at=utcnow()))
        db.add(TaskRow(id="TASK-001", title="update app", status="created", idempotency_key="task-001", acceptance_criteria=["tests pass"], created_at=utcnow()))
        db.commit()
        service = ExecutionService(db, workspace_root=tmp_path / "workspaces", artifact_root=tmp_path / "artifacts", adapter=FakeNativeAdapter("app.py", "print('new')\n"))
        attempt = service.run(task_id="TASK-001", dispatch_key="dispatch-001", fixture=fixture, instructions="update", commands=[["python", "-c", "print('ok')"]], validation_target=("assumption", "ASSUMPTION-001"))
        assert attempt.status == "completed"
        assert db.get(TaskRow, "TASK-001").status == "succeeded"
        evidence = db.query(GraphNodeRow).filter_by(kind="evidence").one()
        assert evidence.id.startswith("EVIDENCE-")
        edge = db.query(__import__("sdf_core.db", fromlist=["DecisionEdgeRow"]).DecisionEdgeRow).filter_by(source_id=evidence.id, relation="validates").one()
        assert edge.relation == "validates"


def test_duplicate_dispatch_returns_existing_attempt(tmp_path: Path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    engine = make_engine()
    Base.metadata.create_all(engine)
    Session = sessionmaker(engine, expire_on_commit=False)
    with Session() as db:
        db.add(TaskRow(id="TASK-002", title="update app", status="created", idempotency_key="task-002", acceptance_criteria=[], created_at=utcnow()))
        db.commit()
        service = ExecutionService(db, workspace_root=tmp_path / "workspaces", artifact_root=tmp_path / "artifacts", adapter=FakeNativeAdapter("app.py", "print('new')\n"))
        first = service.run(task_id="TASK-002", dispatch_key="dispatch-002", fixture=fixture, instructions="update", commands=[])
        second = service.run(task_id="TASK-002", dispatch_key="dispatch-002", fixture=fixture, instructions="update", commands=[])
        assert second.id == first.id


def test_failed_evaluation_contradicts_assumption_and_fails_task(tmp_path: Path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    engine = make_engine()
    Base.metadata.create_all(engine)
    Session = sessionmaker(engine, expire_on_commit=False)
    with Session() as db:
        from sdf_core.db import DecisionEdgeRow

        db.add(GraphNodeRow(id="ASSUMPTION-FAIL-001", kind="assumption", title="change passes tests", source="test", owner="product", confidence=1.0, created_at=utcnow()))
        db.add(TaskRow(id="TASK-FAIL-001", title="broken change", status="created", idempotency_key="task-fail-001", acceptance_criteria=["tests pass"], created_at=utcnow()))
        db.commit()
        service = ExecutionService(db, workspace_root=tmp_path / "workspaces", artifact_root=tmp_path / "artifacts", adapter=FakeNativeAdapter("app.py", "print('broken')\n"))
        attempt = service.run(task_id="TASK-FAIL-001", dispatch_key="dispatch-fail-001", fixture=fixture, instructions="break it", commands=[["python", "-c", "raise SystemExit(2)"]], validation_target=("assumption", "ASSUMPTION-FAIL-001"))
        assert attempt.status == "completed"
        assert db.get(TaskRow, "TASK-FAIL-001").status == "failed"
        edge = db.query(DecisionEdgeRow).filter_by(target_id="ASSUMPTION-FAIL-001", relation="contradicts").one()
        assert edge.source_kind == "evidence"
