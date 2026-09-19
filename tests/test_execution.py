from pathlib import Path
import json
import pytest

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
        attempt = service.run(
            task_id="TASK-001",
            dispatch_key="dispatch-001",
            fixture=fixture,
            instructions="update",
            commands=[["python", "-c", "print('ok')"]],
            criterion_checks={"tests pass": [["python", "-c", "print('ok')"]]},
            validation_target=("assumption", "ASSUMPTION-001"),
        )
        assert attempt.status == "completed"
        assert db.get(TaskRow, "TASK-001").status == "succeeded"
        evidence = db.query(GraphNodeRow).filter_by(kind="evidence").one()
        assert evidence.id.startswith("EVIDENCE-")
        edge = db.query(__import__("sdf_core.db", fromlist=["DecisionEdgeRow"]).DecisionEdgeRow).filter_by(source_id=evidence.id, relation="validates").one()
        assert edge.relation == "validates"
        evidence_row = db.get(__import__("sdf_core.db", fromlist=["EvidenceRow"]).EvidenceRow, evidence.id)
        assert evidence_row.artifact_ref is not None
        artifact = db.get(__import__("sdf_core.db", fromlist=["ArtifactRow"]).ArtifactRow, evidence_row.artifact_ref)
        assert artifact.kind == "evaluator_output"
        assert artifact.id != f"{attempt.id}-LOG"
        output = json.loads(Path(artifact.uri).read_text())
        assert output["criterion"] == "tests pass"
        assert output["status"] == "PASS"
        assert output["command"] == "python -c print('ok')"
        assert edge.evidence_ref == artifact.id


@pytest.mark.parametrize("other_checks, expected", [
    ([["python", "-c", "raise SystemExit(2)"]], "failed"),
    ([], "inconclusive"),
])
def test_partial_success_does_not_validate_target(tmp_path, other_checks, expected):
    from sdf_core.db import DecisionEdgeRow
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        db.add(GraphNodeRow(id="A", kind="assumption", title="both checks hold", source="test", owner="test", confidence=0.5, created_at=utcnow()))
        db.add(TaskRow(id="T", title="partial", status="created", idempotency_key="partial", acceptance_criteria=["a", "b"], created_at=utcnow()))
        db.commit()
        service = ExecutionService(db, workspace_root=tmp_path / "work", artifact_root=tmp_path / "artifacts")
        service.run(task_id="T", dispatch_key="partial", fixture=fixture, instructions="change", commands=[],
                    criterion_checks={"a": [["python", "-c", "print('ok')"]], "b": other_checks}, validation_target=("assumption", "A"))
        assert db.get(TaskRow, "T").status == expected
        assert not db.query(DecisionEdgeRow).filter_by(target_id="A", relation="validates").first()


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
        attempt = service.run(
            task_id="TASK-FAIL-001",
            dispatch_key="dispatch-fail-001",
            fixture=fixture,
            instructions="break it",
            commands=[["python", "-c", "raise SystemExit(2)"]],
            criterion_checks={"tests pass": [["python", "-c", "raise SystemExit(2)"]]},
            validation_target=("assumption", "ASSUMPTION-FAIL-001"),
        )
        assert attempt.status == "completed"
        assert db.get(TaskRow, "TASK-FAIL-001").status == "failed"
        edge = db.query(DecisionEdgeRow).filter_by(target_id="ASSUMPTION-FAIL-001", relation="contradicts").one()
        assert edge.source_kind == "evidence"


def test_unmapped_command_does_not_validate_assumption(tmp_path: Path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    engine = make_engine()
    Base.metadata.create_all(engine)
    Session = sessionmaker(engine, expire_on_commit=False)
    with Session() as db:
        from sdf_core.db import DecisionEdgeRow

        db.add(GraphNodeRow(id="ASSUMPTION-UNMAPPED-001", kind="assumption", title="arbitrary command proves safety", source="test", owner="product", confidence=1.0, created_at=utcnow()))
        db.add(TaskRow(id="TASK-UNMAPPED-001", title="unmapped check", status="created", idempotency_key="task-unmapped-001", acceptance_criteria=["safe change"], created_at=utcnow()))
        db.commit()
        service = ExecutionService(db, workspace_root=tmp_path / "workspaces", artifact_root=tmp_path / "artifacts", adapter=FakeNativeAdapter("app.py", "print('new')\n"))
        attempt = service.run(
            task_id="TASK-UNMAPPED-001",
            dispatch_key="dispatch-unmapped-001",
            fixture=fixture,
            instructions="update",
            commands=[["python", "-c", "print('arbitrary pass')"]],
            validation_target=("assumption", "ASSUMPTION-UNMAPPED-001"),
        )

        assert attempt.status == "completed"
        assert db.get(TaskRow, "TASK-UNMAPPED-001").status == "inconclusive"
        assert not db.query(DecisionEdgeRow).filter_by(target_id="ASSUMPTION-UNMAPPED-001", relation="validates").first()
