from __future__ import annotations

from collections.abc import Mapping, Sequence
from pathlib import Path
import time
from uuid import uuid4

from sqlalchemy import select
from sqlalchemy.exc import IntegrityError
from sqlalchemy.orm import Session

from dataclasses import dataclass

from .adapter import AdapterResult, FakeNativeAdapter, NativeAdapter
from .artifacts import ArtifactStore, WorkspaceManager
from .db import (
    ArtifactRow, AttemptRow, DecisionEdgeRow, EvidenceRow, GraphNodeRow, PlanGateError, PlanRow, PlanTaskRow, TaskRow,
    UsageLedgerRow,
)
from .escalation import ModelTier
from .impact import declared_evidence_mode
from . import tracing
from .evaluator import DeterministicEvaluator
from .model import AttemptState, TaskState, utcnow
from .state import transition_attempt, transition_task
from .policy import ActionRequest, Policy
from .tools import (
    AttemptBinding,
    AuditSink,
    SandboxToolExecutor,
    SqlAlchemyAuditSink,
    SqlAlchemyAttemptGuard,
    SqlAlchemyToolEventSink,
    ToolExecution,
    ToolProxy,
)
from .sandbox import FixtureSandbox


@dataclass(frozen=True)
class BegunAttempt:
    attempt: AttemptRow
    workspace: Path
    existing: bool


def check_plan_gate(db: Session, task_id: str, *, require_plan: bool = False) -> None:
    """Refuse to start a Task whose Plan is not human-approved (ADR 0014)."""

    link = db.get(PlanTaskRow, task_id)
    if link is None:
        if require_plan:
            raise PlanGateError(f"task {task_id} has no approved plan (SDF_REQUIRE_PLAN_APPROVAL)")
        return
    plan = db.get(PlanRow, link.plan_id)
    if plan is None or plan.status != "approved":
        raise PlanGateError(f"plan {link.plan_id} is not approved; task {task_id} cannot start")


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
        model_tier: ModelTier | None = None,
        require_plan_approval: bool = False,
        principal: str | None = None,
    ):
        """Run one Attempt end to end.

        Telemetry (cost, provider, latency, tokens, error class, escalation
        reason) is never taken from the caller (#50): it is measured here or
        by the adapter and summed from the usage ledger, so the scorecard
        cannot be forged through the run request.
        """
        with tracing.span("sdf.attempt", task_id=task_id, model_tier=getattr(model_tier, "value", model_tier),
                          parent_attempt_id=parent_attempt_id) as attempt_span:
            begun = self.begin(
                task_id=task_id, dispatch_key=dispatch_key, fixture=fixture, validation_target=validation_target,
                parent_attempt_id=parent_attempt_id, model_tier=model_tier,
                require_plan_approval=require_plan_approval,
                principal=principal,
            )
            tracing.set_attributes(attempt_span, attempt_id=begun.attempt.id)
            if begun.existing:
                tracing.set_attributes(attempt_span, status=begun.attempt.status, outcome="existing")
                return begun.attempt
            task = self.db.get(TaskRow, task_id)
            attempt = begun.attempt
            # The adapter, never the caller, says whether this is a real agent
            # in a real sandbox (ADR 0031); fake adapters stay synthetic.
            mode = declared_evidence_mode(self.adapter)
            attempt.evidence_mode = mode.value
            started = time.monotonic()
            try:
                result = self.adapter.run(attempt_id=attempt.id, workspace=begun.workspace, instructions=instructions)
            except Exception as exc:
                self._record_measured_telemetry(attempt, time.monotonic() - started, error=exc)
                attempt.status = transition_attempt(AttemptState(attempt.status), AttemptState.FAILED).value
                task.status = transition_task(TaskState(task.status), TaskState.EVALUATING).value
                task.status = transition_task(TaskState(task.status), TaskState.FAILED).value
                self.db.commit()
                self._trace_outcome(attempt_span, attempt)
                raise
            self._record_measured_telemetry(attempt, time.monotonic() - started)
            finished = self.finish(
                attempt_id=attempt.id, result=result, fixture=fixture, commands=commands,
                criterion_checks=criterion_checks, validation_target=validation_target,
                evidence_mode=mode.value,
            )
            self._trace_outcome(attempt_span, finished)
            return finished

    def _record_measured_telemetry(self, attempt: AttemptRow, seconds: float, *, error: Exception | None = None) -> None:
        """Fill Attempt telemetry from server-side measurement (#50).

        A metering adapter (the live run adapter) has already written the
        values from its usage ledger; those are kept. Otherwise (fake and
        in-process adapters) the provider is the adapter's identity, latency is
        the wall clock measured here, and cost/tokens are summed from any
        usage-ledger rows for this Attempt.
        """

        if attempt.provider is not None:
            return
        rows = self.db.scalars(select(UsageLedgerRow).where(UsageLedgerRow.attempt_id == attempt.id)).all()
        attempt.provider = _adapter_identity(self.adapter)
        attempt.latency_ms = round(max(seconds, 0.0) * 1000, 1)
        attempt.cost_usd = round(sum(row.cost_usd or 0.0 for row in rows), 8)
        attempt.input_tokens = _sum_or_none(row.input_tokens for row in rows)
        attempt.output_tokens = _sum_or_none(row.output_tokens for row in rows)
        if error is not None and attempt.error_class is None:
            from .observability import classify_error

            attempt.error_class = classify_error(error)

    @staticmethod
    def _trace_outcome(target, attempt: AttemptRow) -> None:
        tracing.set_attributes(
            target, status=attempt.status, outcome=attempt.outcome, cost_usd=attempt.cost_usd,
            tokens__input=attempt.input_tokens, tokens__output=attempt.output_tokens,
            provider=attempt.provider, error_class=attempt.error_class,
        )
        if attempt.status == AttemptState.FAILED.value:
            tracing.mark_error(attempt.error_class or "failed", target)

    def begin(
        self,
        *,
        task_id: str,
        dispatch_key: str,
        fixture: Path,
        validation_target: tuple[str, str] | None = None,
        parent_attempt_id: str | None = None,
        model_tier: ModelTier | None = None,
        cost_usd: float = 0.0,
        provider: str | None = None,
        latency_ms: float | None = None,
        input_tokens: int | None = None,
        output_tokens: int | None = None,
        escalation_reason: str | None = None,
        agent: str = "native",
        require_plan_approval: bool = False,
        principal: str | None = None,
    ) -> "BegunAttempt":
        """Create and dispatch an Attempt and its disposable workspace.

        The plan-approval gate (ADR 0014) is checked here, before any row is
        written, and again by the ORM/PostgreSQL fences when the Attempt is
        flushed.  ``require_plan_approval`` also refuses Tasks with no Plan.
        """

        check_plan_gate(self.db, task_id, require_plan=require_plan_approval)
        existing = self.db.scalar(select(AttemptRow).where(AttemptRow.dispatch_key == dispatch_key))
        if existing:
            if existing.task_id != task_id:
                raise ValueError(f"dispatch key already belongs to task: {existing.task_id}")
            return BegunAttempt(existing, self.workspaces.root / existing.id, existing=True)
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
        parent = self.db.get(AttemptRow, parent_attempt_id) if parent_attempt_id else None
        if parent_attempt_id and (parent is None or parent.task_id != task_id):
            raise ValueError("parent attempt not found for task")
        if parent is not None:
            check_retry_parent(self.db, parent)
        model_tier = retry_tier(parent, model_tier)
        if parent is not None and escalation_reason is None:
            escalation_reason = derived_escalation_reason(parent)
        if validation_target:
            target = self.db.get(GraphNodeRow, validation_target[1])
            if target is None or target.kind != validation_target[0]:
                raise ValueError(f"validation target not found: {validation_target[0]}:{validation_target[1]}")
        attempt_id = f"ATTEMPT-{uuid4().hex[:12]}"
        attempt = AttemptRow(
            id=attempt_id, task_id=task_id, status=AttemptState.CREATED.value,
            dispatch_key=dispatch_key, agent=agent, parent_attempt_id=parent_attempt_id,
            model_tier=model_tier.value, cost_usd=cost_usd, created_at=utcnow(),
            provider=provider, latency_ms=latency_ms, input_tokens=input_tokens,
            output_tokens=output_tokens, escalation_reason=escalation_reason,
            # ADR 0024: the trace this Attempt ran in (None when tracing is off).
            trace_id=tracing.current_trace_id(),
            # ADR 0026: who may drive this Attempt's Tool Proxy actions.
            principal=principal,
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
                return BegunAttempt(winner, self.workspaces.root / winner.id, existing=True)
            raise
        task.status = transition_task(TaskState(task.status), TaskState.READY).value
        task.status = transition_task(TaskState(task.status), TaskState.RUNNING).value
        attempt.status = transition_attempt(AttemptState(attempt.status), AttemptState.DISPATCHED).value
        attempt.status = transition_attempt(AttemptState(attempt.status), AttemptState.RUNNING).value
        self.db.commit()

        try:
            workspace = self.workspaces.create(attempt_id, fixture)
        except Exception:
            self._fail_attempt(task, attempt)
            raise
        return BegunAttempt(attempt, workspace, existing=False)

    def finish(
        self,
        *,
        attempt_id: str,
        result: AdapterResult,
        fixture: Path,
        commands: list[list[str]],
        criterion_checks: Mapping[str, Sequence[Sequence[str]]] | None = None,
        validation_target: tuple[str, str] | None = None,
        evidence_mode: str | None = None,
    ):
        """Capture artifacts and let the deterministic Evaluator decide.

        The adapter's own status is only a precondition: a Task succeeds only
        on PASS Evidence from the Evaluator, never on an agent's claim.
        """

        attempt = self.db.get(AttemptRow, attempt_id)
        if attempt is None:
            raise ValueError(f"attempt not found: {attempt_id}")
        if attempt.status != AttemptState.RUNNING.value:
            raise ValueError(f"attempt {attempt_id} is not running")
        task = self.db.get(TaskRow, attempt.task_id)
        workspace = self.workspaces.root / attempt_id
        before = fixture
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
            with tracing.span("sdf.evaluate", attempt_id=attempt_id):
                evaluation = self.evaluator.evaluate(
                    attempt_id=attempt_id,
                    workspace=workspace,
                    commands=commands,
                    criteria=tuple(task.acceptance_criteria or ()),
                    criterion_checks=criterion_checks,
                    # The check's own files must match the server fixture (ADR 0025).
                    reference=fixture,
                )
        except Exception:
            self._fail_attempt(task, attempt)
            raise
        for evidence in evaluation.evidence:
            try:
                evaluator_artifact = self.artifacts.capture_evaluator_output(
                    artifact_id=f"{evidence.evidence_id}-OUTPUT",
                    evidence=evidence,
                )
            except Exception:
                self._fail_attempt(task, attempt)
                raise
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
                    metadata_json={"evidence_id": evidence.evidence_id, "criterion": evidence.criterion,
                                   "runner": evidence.runner},
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
                    **({"evidence_mode": evidence_mode} if evidence_mode else {}),
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
        principal: str | None = None,
        binding: AttemptBinding | None = None,
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
        durable_audit = audit or SqlAlchemyAuditSink(self.db, principal=principal)
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
            event_sink=SqlAlchemyToolEventSink(self.db, principal=principal),
            binding=binding,
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


TERMINAL_ATTEMPT_STATES = frozenset({AttemptState.COMPLETED.value, AttemptState.FAILED.value,
                                     AttemptState.CANCELLED.value})
_TIER_RANK = {ModelTier.BASIC: 0, ModelTier.MEDIUM: 1, ModelTier.HIGH: 2}


def check_retry_parent(db: Session, parent: AttemptRow) -> None:
    """A retry/escalation parent must be finished and not accepted (#50 review)."""

    if parent.status not in TERMINAL_ATTEMPT_STATES:
        raise ValueError(f"parent attempt {parent.id} is {parent.status}; only a finished attempt can be retried")
    statuses = [row.status for row in db.scalars(select(EvidenceRow).where(EvidenceRow.attempt_id == parent.id))]
    if parent.status == AttemptState.COMPLETED.value and statuses and all(s == "PASS" for s in statuses):
        raise ValueError(f"parent attempt {parent.id} passed every check; there is nothing to retry")


def retry_tier(parent: AttemptRow | None, declared: ModelTier | str | None) -> ModelTier:
    """The Attempt's model tier.

    The tier is a label the caller declares for routing (it is not measured);
    a retry defaults to the parent's tier and may not go below it.
    """

    if parent is None:
        return ModelTier(declared) if declared is not None else ModelTier.BASIC
    parent_tier = ModelTier(parent.model_tier or ModelTier.BASIC.value)
    if declared is None:
        return parent_tier
    tier = ModelTier(declared)
    if _TIER_RANK[tier] < _TIER_RANK[parent_tier]:
        raise ValueError(f"model_tier {tier.value} is below the parent attempt's tier {parent_tier.value}")
    return tier


def derived_escalation_reason(parent: AttemptRow) -> str:
    """Server-side escalation reason from the parent Attempt's recorded state (#50)."""

    state = parent.status if not parent.outcome else f"{parent.status}/{parent.outcome}"
    reason = f"escalated from {parent.id} (tier {parent.model_tier}, {state}"
    if parent.error_class:
        reason += f", error {parent.error_class}"
    return (reason + ")")[:500]


def _adapter_identity(adapter: object) -> str:
    name = getattr(adapter, "provider", None) or getattr(adapter, "agent", None) or type(adapter).__name__
    return str(name)[:120]


def _sum_or_none(values) -> int | None:
    present = [value for value in values if value is not None]
    return sum(present) if present else None
