from __future__ import annotations

from collections.abc import Mapping, Sequence
from pathlib import Path
from uuid import uuid4

from sqlalchemy import select
from sqlalchemy.exc import IntegrityError
from sqlalchemy.orm import Session

from .adapter import FakeNativeAdapter, NativeAdapter
from .artifacts import ArtifactStore, WorkspaceManager
from .db import ArtifactRow, AttemptRow, DecisionEdgeRow, EvidenceRow, GraphNodeRow, TaskRow
from .escalation import ModelTier
from .evaluator import DeterministicEvaluator
from .model import AttemptState, TaskState, utcnow
from .state import transition_attempt, transition_task
from .policy import ActionRequest, Policy
from .tools import (
    AuditSink,
    SandboxToolExecutor,
    SqlAlchemyAuditSink,
    SqlAlchemyAttemptGuard,
    SqlAlchemyToolEventSink,
    ToolExecution,
    ToolProxy,
)
from .sandbox import FixtureSandbox


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
        parent_attempt_id: str | None = None,
        model_tier: ModelTier = ModelTier.BASIC,
        cost_usd: float = 0.0,
        provider: str | None = None,
        latency_ms: float | None = None,
        input_tokens: int | None = None,
        output_tokens: int | None = None,
        escalation_reason: str | None = None,
    ):
        existing = self.db.scalar(select(AttemptRow).where(AttemptRow.dispatch_key == dispatch_key))
        if existing:
            if existing.task_id != task_id:
                raise ValueError(f"dispatch key already belongs to task: {existing.task_id}")
            return existing
        task = self.db.get(TaskRow, task_id)
        if task is None:
            raise ValueError(f"task not found: {task_id}")
        if cost_usd < 0:
            raise ValueError("cost_usd must not be negative")
        if latency_ms is not None and latency_ms < 0:
            raise ValueError("latency_ms must not be negative")
        if input_tokens is not None and input_tokens < 0:
            raise ValueError("input_tokens must not be negative")
        if output_tokens is not None and output_tokens < 0:
            raise ValueError("output_tokens must not be negative")
        model_tier = ModelTier(model_tier)
        parent = self.db.get(AttemptRow, parent_attempt_id) if parent_attempt_id else None
        if parent_attempt_id and (parent is None or parent.task_id != task_id):
            raise ValueError("parent attempt not found for task")
        if validation_target:
            target = self.db.get(GraphNodeRow, validation_target[1])
            if target is None or target.kind != validation_target[0]:
                raise ValueError(f"validation target not found: {validation_target[0]}:{validation_target[1]}")
        attempt_id = f"ATTEMPT-{uuid4().hex[:12]}"
        attempt = AttemptRow(
            id=attempt_id, task_id=task_id, status=AttemptState.CREATED.value,
            dispatch_key=dispatch_key, agent="native", parent_attempt_id=parent_attempt_id,
            model_tier=model_tier.value, cost_usd=cost_usd, created_at=utcnow(),
            provider=provider, latency_ms=latency_ms, input_tokens=input_tokens,
            output_tokens=output_tokens, escalation_reason=escalation_reason,
        )
        self.db.add(attempt)
        self.db.add(GraphNodeRow(id=attempt_id, kind="attempt", title="Native execution attempt", source="execution", owner="sdf", confidence=1.0, created_at=utcnow()))
        self.db.add(DecisionEdgeRow(source_kind="attempt", source_id=attempt_id, target_kind="task", target_id=task_id, relation="implements", source="execution", owner="sdf", confidence=1.0, created_at=utcnow()))
        if parent is not None:
            self.db.add(DecisionEdgeRow(source_kind="attempt", source_id=attempt_id, target_kind="attempt", target_id=parent.id, relation="depends_on", source="escalation", owner="sdf", confidence=1.0, created_at=utcnow()))
        try:
            self.db.flush()
        except IntegrityError:
            # A concurrent dispatcher may have won the unique dispatch-key
            # claim between our initial read and INSERT. Roll back only this
            # losing unit of work, then return the durable winner when it is
            # for the same Task; never silently cross a task boundary.
            self.db.rollback()
            winner = self.db.scalar(select(AttemptRow).where(AttemptRow.dispatch_key == dispatch_key))
            if winner is not None:
                if winner.task_id != task_id:
                    raise ValueError(f"dispatch key already belongs to task: {winner.task_id}")
                return winner
            raise
        task.status = transition_task(TaskState(task.status), TaskState.READY).value
        task.status = transition_task(TaskState(task.status), TaskState.RUNNING).value
        attempt.status = transition_attempt(AttemptState(attempt.status), AttemptState.DISPATCHED).value
        attempt.status = transition_attempt(AttemptState(attempt.status), AttemptState.RUNNING).value
        self.db.commit()

        before = fixture
        try:
            workspace = self.workspaces.create(attempt_id, fixture)
        except Exception:
            self._fail_attempt(task, attempt)
            raise
        try:
            result = self.adapter.run(attempt_id=attempt_id, workspace=workspace, instructions=instructions)
        except Exception:
            attempt.status = transition_attempt(AttemptState(attempt.status), AttemptState.FAILED).value
            task.status = transition_task(TaskState(task.status), TaskState.EVALUATING).value
            task.status = transition_task(TaskState(task.status), TaskState.FAILED).value
            self.db.commit()
            raise
        try:
            diff = self.artifacts.capture_diff(artifact_id=f"{attempt_id}-DIFF", before=before, after=workspace, files=result.changed_files)
            process = self.artifacts.capture_process(artifact_id=f"{attempt_id}-LOG", stdout=result.stdout, stderr=result.stderr, exit_code=result.exit_code)
        except Exception:
            self._fail_attempt(task, attempt)
            raise
        for artifact in (diff, process):
            self.db.add(ArtifactRow(id=artifact.artifact_id, attempt_id=attempt_id, kind=artifact.kind, uri=str(artifact.path), sha256=artifact.sha256, size_bytes=artifact.size_bytes, created_at=utcnow()))
            self.db.add(GraphNodeRow(id=artifact.artifact_id, kind="artifact", title=artifact.kind, source=str(artifact.path), owner="execution", confidence=1.0, created_at=utcnow()))
        # PostgreSQL's endpoint-integrity trigger runs per edge row. Flush the
        # artifact graph nodes first so the trigger can observe them.
        self.db.flush()
        for artifact in (diff, process):
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
        try:
            evaluation = self.evaluator.evaluate(
                attempt_id=attempt_id,
                workspace=workspace,
                commands=commands,
                criteria=tuple(task.acceptance_criteria or ()),
                criterion_checks=criterion_checks,
            )
        except Exception:
            self._fail_attempt(task, attempt)
            raise
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
            self.db.flush()
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
                    criterion=evidence.criterion,
                    measured_at=utcnow(),
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
            self.db.flush()
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

    def execute_tool(
        self,
        *,
        attempt_id: str,
        request: ActionRequest,
        policy: Policy,
        audit: AuditSink | None = None,
        containment_backend: object | None = None,
        require_containment: bool = True,
    ) -> ToolExecution:
        """Run one structured action inside an Attempt workspace.

        This is the integration seam used by runtime adapters: it derives the
        disposable workspace from the Attempt identity, binds policy and audit
        to the same SQLAlchemy state source, and rejects cross-attempt requests
        before touching the filesystem or process executor.
        """

        if request.attempt_id != attempt_id:
            raise ValueError("tool request is not bound to the requested attempt")
        attempt = self.db.get(AttemptRow, attempt_id)
        if attempt is None:
            raise ValueError(f"attempt not found: {attempt_id}")
        workspace = self.workspaces.root / attempt_id
        if not workspace.is_dir():
            raise ValueError(f"attempt workspace not found: {attempt_id}")
        durable_audit = audit or SqlAlchemyAuditSink(self.db)
        proxy = ToolProxy(
            policy=policy,
            executor=SandboxToolExecutor(
                FixtureSandbox(
                    workspace,
                    containment_backend=containment_backend,
                    require_containment=require_containment,
                    allow_network=getattr(containment_backend, "network_egress_allowed", False),
                ),
                network_actions_allowed=getattr(containment_backend, "network_actions_allowed", True),
            ),
            audit=durable_audit,
            attempt_guard=SqlAlchemyAttemptGuard(self.db),
            event_sink=SqlAlchemyToolEventSink(self.db),
        )
        result = proxy.execute(request)
        self.db.commit()
        return result

    def _fail_attempt(self, task: TaskRow, attempt: AttemptRow) -> None:
        if attempt.status == AttemptState.RUNNING.value:
            attempt.status = transition_attempt(AttemptState.RUNNING, AttemptState.FAILED).value
        if task.status == TaskState.RUNNING.value:
            task.status = transition_task(TaskState.RUNNING, TaskState.EVALUATING).value
        if task.status == TaskState.EVALUATING.value:
            task.status = transition_task(TaskState.EVALUATING, TaskState.FAILED).value
        self.db.commit()
