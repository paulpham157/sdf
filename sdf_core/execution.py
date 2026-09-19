from __future__ import annotations

from collections.abc import Mapping, Sequence
from pathlib import Path
from uuid import uuid4

from sqlalchemy import select
from sqlalchemy.orm import Session

from .adapter import FakeNativeAdapter, NativeAdapter
from .artifacts import ArtifactStore, WorkspaceManager
from .db import ArtifactRow, AttemptRow, DecisionEdgeRow, EvidenceRow, GraphNodeRow, TaskRow
from .evaluator import DeterministicEvaluator
from .model import AttemptState, TaskState, utcnow
from .state import transition_attempt, transition_task


class ExecutionService:
    def __init__(self, db: Session, *, workspace_root: Path, artifact_root: Path, adapter: NativeAdapter | None = None, evaluator: DeterministicEvaluator | None = None):
        self.db = db
        self.workspaces = WorkspaceManager(workspace_root)
        self.artifacts = ArtifactStore(artifact_root)
        self.adapter = adapter or FakeNativeAdapter("app.py", "print('updated')\n")
        self.evaluator = evaluator or DeterministicEvaluator()

    def run(
        self,
        *,
        task_id: str,
        dispatch_key: str,
        fixture: Path,
        instructions: str,
        commands: list[list[str]],
        criterion_checks: Mapping[str, Sequence[Sequence[str]]] | None = None,
        validation_target: tuple[str, str] | None = None,
    ):
        existing = self.db.scalar(select(AttemptRow).where(AttemptRow.dispatch_key == dispatch_key))
        if existing:
            return existing
        task = self.db.get(TaskRow, task_id)
        if task is None:
            raise ValueError(f"task not found: {task_id}")
        if validation_target:
            target = self.db.get(GraphNodeRow, validation_target[1])
            if target is None or target.kind != validation_target[0]:
                raise ValueError(f"validation target not found: {validation_target[0]}:{validation_target[1]}")
        attempt_id = f"ATTEMPT-{uuid4().hex[:12]}"
        attempt = AttemptRow(id=attempt_id, task_id=task_id, status=AttemptState.CREATED.value, dispatch_key=dispatch_key, agent="native", created_at=utcnow())
        self.db.add(attempt)
        self.db.add(GraphNodeRow(id=attempt_id, kind="attempt", title="Native execution attempt", source="execution", owner="sdf", confidence=1.0, created_at=utcnow()))
        self.db.add(DecisionEdgeRow(source_kind="attempt", source_id=attempt_id, target_kind="task", target_id=task_id, relation="implements", source="execution", owner="sdf", confidence=1.0, created_at=utcnow()))
        self.db.flush()
        task.status = transition_task(TaskState(task.status), TaskState.READY).value
        task.status = transition_task(TaskState(task.status), TaskState.RUNNING).value
        attempt.status = transition_attempt(AttemptState(attempt.status), AttemptState.DISPATCHED).value
        attempt.status = transition_attempt(AttemptState(attempt.status), AttemptState.RUNNING).value
        self.db.commit()

        before = fixture
        workspace = self.workspaces.create(attempt_id, fixture)
        try:
            result = self.adapter.run(attempt_id=attempt_id, workspace=workspace, instructions=instructions)
        except Exception:
            attempt.status = transition_attempt(AttemptState(attempt.status), AttemptState.FAILED).value
            task.status = transition_task(TaskState(task.status), TaskState.EVALUATING).value
            task.status = transition_task(TaskState(task.status), TaskState.FAILED).value
            self.db.commit()
            raise
        diff = self.artifacts.capture_diff(artifact_id=f"{attempt_id}-DIFF", before=before, after=workspace, files=result.changed_files)
        process = self.artifacts.capture_process(artifact_id=f"{attempt_id}-LOG", stdout=result.stdout, stderr=result.stderr, exit_code=result.exit_code)
        for artifact in (diff, process):
            self.db.add(ArtifactRow(id=artifact.artifact_id, attempt_id=attempt_id, kind=artifact.kind, uri=str(artifact.path), sha256=artifact.sha256, size_bytes=artifact.size_bytes, created_at=utcnow()))
            self.db.add(GraphNodeRow(id=artifact.artifact_id, kind="artifact", title=artifact.kind, source=str(artifact.path), owner="execution", confidence=1.0, created_at=utcnow()))
            self.db.add(DecisionEdgeRow(source_kind="artifact", source_id=artifact.artifact_id, target_kind="attempt", target_id=attempt_id, relation="measures", source="execution", owner="sdf", confidence=1.0, created_at=utcnow(), evidence_ref=artifact.artifact_id))

        adapter_succeeded = result.status.lower() in {"completed", "succeeded", "success"} and result.exit_code == 0
        attempt.status = transition_attempt(
            AttemptState(attempt.status), AttemptState.COMPLETED if adapter_succeeded else AttemptState.FAILED
        ).value
        task.status = transition_task(TaskState(task.status), TaskState.EVALUATING).value
        if not adapter_succeeded:
            task.status = transition_task(TaskState(task.status), TaskState.FAILED).value
            self.db.commit()
            return attempt
        evaluation = self.evaluator.evaluate(
            attempt_id=attempt_id,
            workspace=workspace,
            commands=commands,
            criteria=tuple(task.acceptance_criteria or ()),
            criterion_checks=criterion_checks,
        )
        for evidence in evaluation.evidence:
            evaluator_artifact = self.artifacts.capture_evaluator_output(
                artifact_id=f"{evidence.evidence_id}-OUTPUT",
                evidence=evidence,
            )
            self.db.add(
                ArtifactRow(
                    id=evaluator_artifact.artifact_id,
                    attempt_id=attempt_id,
                    kind=evaluator_artifact.kind,
                    uri=str(evaluator_artifact.path),
                    sha256=evaluator_artifact.sha256,
                    size_bytes=evaluator_artifact.size_bytes,
                    created_at=utcnow(),
                )
            )
            self.db.add(
                GraphNodeRow(
                    id=evaluator_artifact.artifact_id,
                    kind="artifact",
                    title=evaluator_artifact.kind,
                    source=str(evaluator_artifact.path),
                    owner="evaluator",
                    confidence=evidence.confidence,
                    created_at=utcnow(),
                    metadata_json={"evidence_id": evidence.evidence_id, "criterion": evidence.criterion},
                )
            )
            self.db.add(
                DecisionEdgeRow(
                    source_kind="artifact",
                    source_id=evaluator_artifact.artifact_id,
                    target_kind="attempt",
                    target_id=attempt_id,
                    relation="measures",
                    source="evaluator",
                    owner="evaluator",
                    confidence=evidence.confidence,
                    created_at=utcnow(),
                    evidence_ref=evaluator_artifact.artifact_id,
                )
            )
            self.db.add(
                EvidenceRow(
                    id=evidence.evidence_id,
                    attempt_id=attempt_id,
                    kind=evidence.kind,
                    status=evidence.status,
                    command=evidence.command,
                    exit_code=evidence.exit_code,
                    artifact_ref=evaluator_artifact.artifact_id,
                    confidence=evidence.confidence,
                    created_at=utcnow(),
                )
            )
            self.db.add(
                GraphNodeRow(
                    id=evidence.evidence_id,
                    kind="evidence",
                    title=evidence.status,
                    source=evidence.command,
                    owner="evaluator",
                    confidence=evidence.confidence,
                    created_at=utcnow(),
                    metadata_json={"criterion": evidence.criterion, "artifact_ref": evaluator_artifact.artifact_id},
                )
            )
            self.db.add(
                DecisionEdgeRow(
                    source_kind="evidence",
                    source_id=evidence.evidence_id,
                    target_kind="attempt",
                    target_id=attempt_id,
                    relation="measures",
                    source="evaluator",
                    owner="evaluator",
                    confidence=evidence.confidence,
                    created_at=utcnow(),
                    evidence_ref=evaluator_artifact.artifact_id,
                )
            )
            # Only an explicitly mapped criterion can create a validation
            # edge. A passing legacy command is an observation, never proof of
            # a business hypothesis.
            if validation_target and evidence.criterion is not None:
                relation = "measures"
                if evidence.status == "FAIL":
                    relation = "contradicts"
                elif evidence.status == "PASS" and evaluation.status == "PASS":
                    relation = "validates"
                self.db.add(
                    DecisionEdgeRow(
                        source_kind="evidence",
                        source_id=evidence.evidence_id,
                        target_kind=validation_target[0],
                        target_id=validation_target[1],
                        relation=relation,
                        source="evaluator",
                        owner="evaluator",
                        confidence=evidence.confidence,
                        created_at=utcnow(),
                        evidence_ref=evaluator_artifact.artifact_id,
                    )
                )
        task_state = {"PASS": TaskState.SUCCEEDED, "FAIL": TaskState.FAILED, "INCONCLUSIVE": TaskState.INCONCLUSIVE}[evaluation.status]
        task.status = transition_task(TaskState(task.status), task_state).value
        self.db.commit()
        return attempt
