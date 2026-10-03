from pathlib import Path
import json
import pytest

from sdf_core.adapter import FakeNativeAdapter
from sdf_core.db import ArtifactRow, AttemptRow, Base, EvidenceRow, GraphNodeRow, RuntimeEventRow, TaskRow, make_engine
from sdf_core.evaluator import DeterministicEvaluator
from sdf_core.execution import ExecutionService
from sdf_core.model import utcnow
from sdf_core.policy import ActionRequest, AllowlistPolicy
from sdf_core.tools import InMemoryAuditSink, ToolExecutionStatus
from sqlalchemy.orm import sessionmaker


class RaisingEvaluator:
    def evaluate(self, **kwargs):
        raise RuntimeError("evaluator crashed")


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


@pytest.mark.parametrize(
    "first_outcome, first_checks",
    [
        ("failed", {"check": [["python", "-c", "raise SystemExit(2)"]]}),
        ("inconclusive", {}),
    ],
)
def test_terminal_duplicate_dispatch_does_not_execute_again(tmp_path: Path, first_outcome, first_checks):
    class CountingAdapter:
        def __init__(self):
            self.calls = 0
            self.fake = FakeNativeAdapter("app.py", "print('new')\n")

        def run(self, **kwargs):
            self.calls += 1
            return self.fake.run(**kwargs)

    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        db.add(TaskRow(
            id="TASK-TERMINAL-DUP", title="terminal redelivery", status="created",
            idempotency_key="terminal-duplicate", acceptance_criteria=["check"], created_at=utcnow(),
        ))
        db.commit()
        adapter = CountingAdapter()
        service = ExecutionService(
            db, workspace_root=tmp_path / "work", artifact_root=tmp_path / "artifacts", adapter=adapter,
        )
        arguments = dict(
            task_id="TASK-TERMINAL-DUP", dispatch_key="terminal-dispatch", fixture=fixture,
            instructions="change", commands=[], criterion_checks=first_checks,
        )
        first = service.run(**arguments)
        evidence_count = db.query(EvidenceRow).filter_by(attempt_id=first.id).count()
        second = service.run(**arguments)

        assert second.id == first.id
        assert db.get(TaskRow, "TASK-TERMINAL-DUP").status == first_outcome
        assert adapter.calls == 1
        assert db.query(EvidenceRow).filter_by(attempt_id=first.id).count() == evidence_count


@pytest.mark.parametrize(
    "first_outcome, first_checks",
    [
        ("failed", {"check": [["python", "-c", "raise SystemExit(2)"]]}),
        ("inconclusive", {}),
    ],
)
def test_deliberate_retry_preserves_prior_evidence_and_records_lineage(tmp_path: Path, first_outcome, first_checks):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        db.add(TaskRow(
            id="TASK-RETRY", title="retry terminal outcome", status="created",
            idempotency_key="retry-terminal", acceptance_criteria=["check"], created_at=utcnow(),
        ))
        db.commit()
        service = ExecutionService(
            db, workspace_root=tmp_path / "work", artifact_root=tmp_path / "artifacts",
            adapter=FakeNativeAdapter("app.py", "print('new')\n"),
        )
        prior = service.run(
            task_id="TASK-RETRY", dispatch_key="retry-first", fixture=fixture,
            instructions="first attempt", commands=[], criterion_checks=first_checks,
        )
        assert db.get(TaskRow, "TASK-RETRY").status == first_outcome
        attempt_before = (
            prior.id, prior.task_id, prior.status, prior.dispatch_key,
            prior.parent_attempt_id, prior.model_tier, prior.cost_usd,
        )
        evidence_before = [
            (row.id, row.attempt_id, row.status, row.command, row.artifact_ref, row.confidence, row.criterion)
            for row in db.query(EvidenceRow).filter_by(attempt_id=prior.id).order_by(EvidenceRow.id)
        ]
        assert evidence_before

        retried = service.run(
            task_id="TASK-RETRY", dispatch_key="retry-second", fixture=fixture,
            instructions="deliberate retry", commands=[],
            criterion_checks={"check": [["python", "-c", "print('ok')"]]},
            parent_attempt_id=prior.id,
        )

        assert retried.id != prior.id
        assert retried.parent_attempt_id == prior.id
        assert db.get(TaskRow, "TASK-RETRY").status == "succeeded"
        assert (
            prior.id, prior.task_id, prior.status, prior.dispatch_key,
            prior.parent_attempt_id, prior.model_tier, prior.cost_usd,
        ) == attempt_before
        evidence_after = [
            (row.id, row.attempt_id, row.status, row.command, row.artifact_ref, row.confidence, row.criterion)
            for row in db.query(EvidenceRow).filter_by(attempt_id=prior.id).order_by(EvidenceRow.id)
        ]
        assert evidence_after == evidence_before
        assert db.query(EvidenceRow).filter_by(attempt_id=retried.id).count() == 1
        diff = db.get(ArtifactRow, f"{retried.id}-DIFF")
        assert "+print('new')" in Path(diff.uri).read_text(encoding="utf-8")


def test_dispatch_key_cannot_be_reused_for_another_task(tmp_path: Path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        db.add_all([
            TaskRow(id="TASK-DISPATCH-A", title="a", status="created", idempotency_key="dispatch-task-a", acceptance_criteria=[], created_at=utcnow()),
            TaskRow(id="TASK-DISPATCH-B", title="b", status="created", idempotency_key="dispatch-task-b", acceptance_criteria=[], created_at=utcnow()),
        ])
        db.commit()
        service = ExecutionService(db, workspace_root=tmp_path / "work", artifact_root=tmp_path / "artifacts")
        service.run(task_id="TASK-DISPATCH-A", dispatch_key="shared-dispatch", fixture=fixture, instructions="a", commands=[])
        with pytest.raises(ValueError, match="belongs to task"):
            service.run(task_id="TASK-DISPATCH-B", dispatch_key="shared-dispatch", fixture=fixture, instructions="b", commands=[])


def test_evaluator_failure_settles_attempt_and_task(tmp_path: Path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        db.add(TaskRow(id="TASK-EVAL-CRASH", title="crash", status="created", idempotency_key="eval-crash", acceptance_criteria=["check"], created_at=utcnow()))
        db.commit()
        service = ExecutionService(db, workspace_root=tmp_path / "work", artifact_root=tmp_path / "artifacts", evaluator=RaisingEvaluator())
        with pytest.raises(RuntimeError, match="evaluator crashed"):
            service.run(task_id="TASK-EVAL-CRASH", dispatch_key="eval-crash", fixture=fixture, instructions="change", commands=[])
        assert db.get(TaskRow, "TASK-EVAL-CRASH").status == "failed"
        attempt = db.query(__import__("sdf_core.db", fromlist=["AttemptRow"]).AttemptRow).one()
        assert attempt.status == "completed"
        duplicate = service.run(
            task_id="TASK-EVAL-CRASH", dispatch_key="eval-crash", fixture=fixture,
            instructions="redelivered", commands=[],
        )
        assert duplicate.id == attempt.id

        service.evaluator = DeterministicEvaluator()
        retry = service.run(
            task_id="TASK-EVAL-CRASH", dispatch_key="eval-crash-retry", fixture=fixture,
            instructions="recover", commands=[],
            criterion_checks={"check": [["python", "-c", "print('ok')"]]},
            parent_attempt_id=attempt.id,
        )
        assert retry.id != attempt.id
        assert retry.parent_attempt_id == attempt.id
        assert db.get(TaskRow, "TASK-EVAL-CRASH").status == "succeeded"


def test_evaluator_artifact_failure_settles_task_and_allows_deliberate_retry(tmp_path: Path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        db.add(TaskRow(
            id="TASK-ARTIFACT-CRASH", title="recover artifact failure", status="created",
            idempotency_key="artifact-crash", acceptance_criteria=["check passes"], created_at=utcnow(),
        ))
        db.commit()
        service = ExecutionService(
            db, workspace_root=tmp_path / "work", artifact_root=tmp_path / "artifacts",
            adapter=FakeNativeAdapter("app.py", "print('new')\n"),
        )
        capture_output = service.artifacts.capture_evaluator_output

        def fail_once(**kwargs):
            service.artifacts.capture_evaluator_output = capture_output
            raise OSError("artifact store unavailable")

        service.artifacts.capture_evaluator_output = fail_once

        with pytest.raises(OSError, match="artifact store unavailable"):
            service.run(
                task_id="TASK-ARTIFACT-CRASH", dispatch_key="artifact-crash-first",
                fixture=fixture, instructions="change", commands=[],
                criterion_checks={"check passes": [["python", "-c", "print('ok')"]]},
            )

        prior = db.query(AttemptRow).one()
        assert prior.status == "completed"
        assert db.get(TaskRow, "TASK-ARTIFACT-CRASH").status == "failed"
        prior_evidence = db.query(EvidenceRow).filter_by(attempt_id=prior.id).all()
        assert prior_evidence == []

        retried = service.run(
            task_id="TASK-ARTIFACT-CRASH", dispatch_key="artifact-crash-retry",
            fixture=fixture, instructions="retry", commands=[],
            criterion_checks={"check passes": [["python", "-c", "print('ok')"]]},
            parent_attempt_id=prior.id,
        )

        assert retried.id != prior.id
        assert retried.parent_attempt_id == prior.id
        assert db.get(TaskRow, "TASK-ARTIFACT-CRASH").status == "succeeded"


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


def test_execution_service_routes_structured_tool_action_through_attempt_workspace(tmp_path: Path):
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        db.add(TaskRow(id="TASK-TOOL-INTEGRATION", title="tool", status="created", idempotency_key="tool-integration", acceptance_criteria=[], created_at=utcnow()))
        db.add(__import__("sdf_core.db", fromlist=["AttemptRow"]).AttemptRow(
            id="ATTEMPT-TOOL-INTEGRATION", task_id="TASK-TOOL-INTEGRATION", status="running",
            dispatch_key="tool-integration-dispatch", agent="codex", created_at=utcnow(),
        ))
        db.commit()
        workspace = tmp_path / "workspaces" / "ATTEMPT-TOOL-INTEGRATION"
        workspace.mkdir(parents=True)
        service = ExecutionService(db, workspace_root=tmp_path / "workspaces", artifact_root=tmp_path / "artifacts")
        audit = InMemoryAuditSink()
        result = service.execute_tool(
            attempt_id="ATTEMPT-TOOL-INTEGRATION",
            request=ActionRequest(
                attempt_id="ATTEMPT-TOOL-INTEGRATION", actor="agent:codex",
                tool="filesystem", action="write", resource="result.txt",
                context={"data": "from-tool"},
            ),
            policy=AllowlistPolicy({("filesystem", "write")}),
            audit=audit,
        )
        assert result.status is ToolExecutionStatus.EXECUTED
        assert (workspace / "result.txt").read_text() == "from-tool"
        assert len(audit.records) == 2
        events = db.query(RuntimeEventRow).filter_by(
            source="tool-proxy", attempt_id="ATTEMPT-TOOL-INTEGRATION"
        ).all()
        assert [event.kind for event in events] == ["tool_policy_decided", "tool_action_executed"]

        process_result = service.execute_tool(
            attempt_id="ATTEMPT-TOOL-INTEGRATION",
            request=ActionRequest(
                attempt_id="ATTEMPT-TOOL-INTEGRATION", actor="agent:codex",
                tool="process", action="run", resource=".",
                context={"command": ["python", "-c", "print('must be contained')"]},
            ),
            policy=AllowlistPolicy({("process", "run")}),
        )
        assert process_result.status is ToolExecutionStatus.FAILED
        assert "containment" in (process_result.error or "").lower()
