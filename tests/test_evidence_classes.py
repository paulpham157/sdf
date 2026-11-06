"""ADR 0042: execution, verification, and outcome are different Evidence.

A PASS is a verified pass only when it is independent, unexpired, and bound
to the Attempt's diff. Legacy, execution, outcome, expired, self-verified,
and wrong-digest PASS rows are reported separately and do not accept a Task.
"""

from __future__ import annotations

import importlib.util
from datetime import datetime, timedelta, timezone
from pathlib import Path
from uuid import uuid4

import pytest
from alembic import command
from alembic.config import Config
from sqlalchemy import create_engine, inspect, select, text
from sqlalchemy.orm import sessionmaker

from sdf_core.acceptance import (
    EVIDENCE_SUBJECT_SQL,
    AcceptanceError,
    check_hidden_oracle_acceptance,
    check_verification_acceptance,
)
from sdf_core.adapter import FakeNativeAdapter
from sdf_core.api import SessionLocal
from sdf_core.db import (
    ArtifactRow,
    AttemptRow,
    Base,
    DecisionEdgeRow,
    EvidenceRow,
    PlanRow,
    PlanTaskRow,
    TaskRow,
    make_engine,
)
from sdf_core.execution import ExecutionService
from sdf_core.impact import (
    AttemptFact,
    EvidenceFact,
    EvidenceMode,
    ScorecardFacts,
    TaskFact,
    build_scorecard,
)
from sdf_core.impact_repository import load_scorecard_facts
from sdf_core.measurement import build_scorecard_from_db
from sdf_core.model import utcnow
from tests.auth_support import admin_client, client_as
from tests.revision_support import bound

DIGEST = "a" * 64
ORACLE = "b" * 64
FUTURE = datetime(2099, 1, 1, tzinfo=timezone.utc)
PAST = datetime(2020, 1, 1, tzinfo=timezone.utc)
WHEN = datetime(2026, 6, 1, tzinfo=timezone.utc)


def _fact(**overrides) -> EvidenceFact:
    fields = dict(
        evidence_id="E-VERIFIED",
        attempt_id="A",
        status="PASS",
        criterion="c",
        measured_at=WHEN,
        mode=EvidenceMode.SYNTHETIC,
        evidence_class="verification",
        verifier_identity="sdf-evaluator",
        verifier_role="evaluator",
        subject_artifact_sha256=DIGEST,
        oracle_digest=ORACLE,
        expires_at=FUTURE,
    )
    fields.update(overrides)
    return EvidenceFact(**fields)


def _card(*facts: EvidenceFact):
    task = TaskFact(
        task_id="T", created_at=WHEN, status="succeeded", acceptance_criteria=("c",),
        objective_id=None, has_artifact=True,
    )
    attempt = AttemptFact(
        attempt_id="A", task_id="T", created_at=WHEN, parent_attempt_id=None, model_tier="basic",
        cost_usd=1.0, escalation_reason=None, outcome=None, mode=EvidenceMode.SYNTHETIC,
        agent="worker", artifact_sha256=DIGEST,
    )
    return build_scorecard(ScorecardFacts(tasks=(task,), attempts=(attempt,), evidence=facts))


def test_only_an_independent_verification_pass_is_verified():
    genuine = _card(_fact())
    assert genuine.metric("accepted_task_rate").value == 1.0
    assert genuine.metric("verified_passes").numerator == 1
    assert genuine.metric("unverified_legacy_passes").numerator == 0

    cases = {
        "legacy": _fact(evidence_id="E-LEGACY", evidence_class=None, verifier_identity=None, verifier_role=None,
                        subject_artifact_sha256=None, oracle_digest=None, expires_at=None),
        "execution": _fact(evidence_id="E-EXEC", evidence_class="execution", verifier_identity="sdf-evaluator"),
        "expired": _fact(evidence_id="E-EXPIRED", expires_at=PAST),
        "self_verified": _fact(evidence_id="E-SELF", verifier_identity="worker", verifier_role="agent"),
        "wrong_digest": _fact(evidence_id="E-DIGEST", subject_artifact_sha256="d" * 64),
        "outcome": _fact(evidence_id="E-OUT", attempt_id=None, evidence_class="outcome",
                         verifier_identity="alex", verifier_role="stakeholder"),
    }
    for name, fact in cases.items():
        card = _card(fact)
        assert card.metric("accepted_task_rate").value == 0.0, name
        assert card.metric("verified_passes").numerator == 0, name
        assert card.metric("unverified_legacy_passes").numerator == 1, name

    mixed = _card(_fact(), *cases.values())
    assert mixed.metric("verified_passes").numerator == 1
    assert mixed.metric("unverified_legacy_passes").numerator == len(cases)
    # One verified row does not accept an Attempt that also has an unverified verdict.
    assert mixed.metric("accepted_task_rate").value == 0.0


def test_acceptance_gate_rejects_execution_that_mimics_a_verification_pass():
    row = _fact(
        evidence_class="execution",
        verifier_identity="sdf-evaluator",
        verifier_role="evaluator",
        subject_artifact_sha256=DIGEST,
        oracle_digest=ORACLE,
        expires_at=FUTURE,
    )
    with pytest.raises(AcceptanceError, match="not verification"):
        check_verification_acceptance(row, attempt_agent="worker", artifact_sha256=DIGEST)
    with pytest.raises(AcceptanceError, match="not verification"):
        check_verification_acceptance(
            _fact(evidence_class="outcome", attempt_id=None, verifier_identity="alex", verifier_role="stakeholder"),
            attempt_agent="worker", artifact_sha256=DIGEST,
        )


def _session():
    engine = make_engine()
    Base.metadata.create_all(engine)
    return sessionmaker(engine, expire_on_commit=False)()


def _task_attempt(db, suffix: str, *, agent: str = "worker"):
    task_id, attempt_id = f"T-{suffix}", f"A-{suffix}"
    now = utcnow()
    db.add(TaskRow(
        id=task_id, title=suffix, status="succeeded", idempotency_key=task_id,
        acceptance_criteria=["c"], created_at=now,
    ))
    db.add(AttemptRow(
        id=attempt_id, task_id=task_id, status="completed", dispatch_key=attempt_id,
        agent=agent, created_at=now,
    ))
    db.flush()
    binding = bound(db, attempt_id)
    db.add(ArtifactRow(
        id=f"DIFF-{suffix}", attempt_id=attempt_id, kind="diff", uri="diff", sha256=DIGEST,
        size_bytes=1, created_at=now, **binding,
    ))
    return attempt_id, binding


def _verification(db, suffix: str, binding: dict, **overrides) -> None:
    now = utcnow()
    fields = dict(
        id=f"E-{suffix}", attempt_id=f"A-{suffix}", kind="evaluation", status="PASS", command="pytest",
        exit_code=0, confidence=1.0, criterion="c", measured_at=now, created_at=now,
        evidence_class="verification", producer_identity="sdf-evaluator", producer_role="evaluator",
        verifier_identity="sdf-evaluator", verifier_role="evaluator",
        subject_artifact_sha256=DIGEST, oracle_digest=ORACLE,
        collected_at=now, expires_at=now + timedelta(hours=2),
    )
    fields.update(overrides)
    db.add(EvidenceRow(**fields, **binding))


def test_persisted_scorecard_separates_legacy_passes_from_verified_passes():
    db = _session()
    try:
        _task_attempt(db, "VERIFIED")
        _verification(db, "VERIFIED", bound(db, "A-VERIFIED"))
        for suffix in ("EXPIRED", "SELF", "DIGEST", "EXEC", "LEGACY"):
            _task_attempt(db, suffix)
        _verification(db, "EXPIRED", bound(db, "A-EXPIRED"), expires_at=utcnow() - timedelta(hours=1))
        _verification(
            db, "SELF", bound(db, "A-SELF"),
            verifier_identity="worker", verifier_role="agent", producer_identity="worker", producer_role="agent",
        )
        _verification(db, "DIGEST", bound(db, "A-DIGEST"), subject_artifact_sha256="d" * 64)
        _verification(
            db, "EXEC", bound(db, "A-EXEC"),
            kind="execution", evidence_class="execution", criterion="c",
            producer_identity="worker", producer_role="agent",
            verifier_identity="sdf-evaluator", verifier_role="evaluator",
        )
        db.flush()
        legacy_binding = bound(db, "A-LEGACY")
        db.commit()
        db.connection().execute(text(
            "INSERT INTO evidence (id, attempt_id, kind, status, command, confidence, criterion, "
            "evidence_mode, created_at, measured_at, verifier_identity, verifier_role, "
            "subject_artifact_sha256, oracle_digest, expires_at, revision_id, tree_sha256) "
            "VALUES ('E-LEGACY', 'A-LEGACY', 'evaluation', 'PASS', 'c', 1.0, 'c', 'synthetic', "
            ":now, :now, 'sdf-evaluator', 'evaluator', :sha, :oracle, :expires, :revision, :tree)"
        ), {
            "now": utcnow(), "sha": DIGEST, "oracle": ORACLE, "expires": utcnow() + timedelta(hours=2),
            "revision": legacy_binding["revision_id"], "tree": legacy_binding["tree_sha256"],
        })
        db.add(EvidenceRow(
            id="E-OUTCOME", attempt_id=None, objective_id="OBJ-SCORE", kind="outcome", status="PASS",
            command="signed off", confidence=1.0, criterion="incident_minutes", created_at=utcnow(),
            evidence_class="outcome", verifier_identity="alex", verifier_role="stakeholder",
            collected_at=utcnow(), expires_at=utcnow() + timedelta(days=1),
        ))
        db.commit()
        db.expire_all()

        measured = build_scorecard_from_db(db)
        assert measured.verified_pass_count == 1
        assert measured.unverified_legacy_pass_count == 6
        assert measured.accepted_task_rate == pytest.approx(1 / 6)

        impact = build_scorecard(load_scorecard_facts(db))
        assert impact.metric("verified_passes").numerator == 1
        assert impact.metric("unverified_legacy_passes").numerator == 6
        assert impact.metric("accepted_task_rate").value == pytest.approx(1 / 6)
        legacy = db.get(EvidenceRow, "E-LEGACY")
        assert legacy.evidence_class is None and legacy.status == "PASS"
    finally:
        db.close()


def test_a_run_records_execution_evidence_that_cannot_accept(tmp_path: Path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    db = _session()
    try:
        db.add(TaskRow(
            id="TASK-EXEC-RUN", title="run", status="created", idempotency_key="task-exec-run",
            acceptance_criteria=["command succeeds"], created_at=utcnow(),
        ))
        db.commit()
        attempt = ExecutionService(
            db, workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art",
            adapter=FakeNativeAdapter("app.py", "print('new')\n"),
        ).run(
            task_id="TASK-EXEC-RUN", dispatch_key="exec-run", fixture=fixture, instructions="go",
            commands=[], criterion_checks={"command succeeds": [["python", "-c", "print('ok')"]]},
        )
        rows = list(db.scalars(select(EvidenceRow).where(EvidenceRow.attempt_id == attempt.id)))
        execution = next(row for row in rows if row.evidence_class == "execution")
        verification = next(row for row in rows if row.evidence_class == "verification")
        diff = db.scalars(select(ArtifactRow).where(
            ArtifactRow.attempt_id == attempt.id, ArtifactRow.kind == "diff")).one()
        assert execution.producer_identity == attempt.agent
        assert execution.producer_role == "agent"
        assert execution.status == "PASS"
        assert execution.kind == "execution"
        assert execution.criterion is None
        assert execution.subject_artifact_sha256 == diff.sha256
        assert not db.scalars(select(DecisionEdgeRow).where(
            DecisionEdgeRow.source_id == execution.id, DecisionEdgeRow.relation == "validates")).first()
        with pytest.raises(AcceptanceError, match="not verification"):
            check_verification_acceptance(execution, attempt_agent=attempt.agent, artifact_sha256=diff.sha256)
        check_verification_acceptance(verification, attempt_agent=attempt.agent, artifact_sha256=diff.sha256)
        card = build_scorecard_from_db(db)
        assert card.verified_pass_count == 1
        assert card.unverified_legacy_pass_count == 1
        assert db.get(TaskRow, "TASK-EXEC-RUN").status == "succeeded"
    finally:
        db.close()


def test_outcome_evidence_is_recorded_on_the_objective_and_does_not_accept_a_task():
    client = admin_client()
    suffix = uuid4().hex[:8]
    context, objective, task = f"BC-OUT-{suffix}", f"OBJ-OUT-{suffix}", f"TASK-OUT-{suffix}"
    assert client.post("/business-contexts", json={
        "id": context, "title": "context", "owner": "product", "source": "test",
    }).status_code == 201
    assert client.post("/objectives", json={
        "id": objective, "title": "objective", "business_context_id": context,
        "owner": "product", "source": "test",
    }).status_code == 201
    assert client.post("/tasks", json={
        "id": task, "title": "task", "idempotency_key": task, "objective_id": objective,
        "acceptance_criteria": ["met"],
    }).status_code == 201
    expires = (utcnow() + timedelta(days=3)).isoformat()
    created = client.post(f"/objectives/{objective}/evidence", json={
        "verifier_identity": "alex",
        "verifier_role": "stakeholder",
        "status": "PASS",
        "summary": "incident minutes fell to 40",
        "expires_at": expires,
        "criterion": "incident_minutes",
        "confidence": 1.0,
        "mode": "synthetic",
    })
    assert created.status_code == 201, created.text
    body = created.json()
    assert body["evidence_class"] == "outcome"
    assert body["evidence_mode"] == "synthetic"
    assert body["attempt_id"] is None
    assert body["objective_id"] == objective
    assert body["verifier_identity"] == "alex"
    assert body["verifier_role"] == "stakeholder"
    assert body["producer_identity"] == "test-admin"
    assert body["producer_role"] == "approver"
    assert body["expires_at"] is not None
    listed = client.get(f"/objectives/{objective}/evidence")
    assert listed.status_code == 200
    assert [row["id"] for row in listed.json()] == [body["id"]]
    viewed = client.get(f"/evidence/{body['id']}")
    assert viewed.status_code == 200
    assert viewed.json()["evidence_class"] == "outcome"
    refused = client.post(f"/objectives/{objective}/evidence", json={
        "verifier_identity": "native", "verifier_role": "agent", "summary": "the agent says it is done",
        "mode": "synthetic",
    })
    assert refused.status_code == 422
    trace = client.get(f"/tasks/{task}/trace").json()
    assert any(node["id"] == body["id"] for node in trace["nodes"])
    assert any(
        edge["relation"] == "measures" and edge["source_id"] == body["id"] and edge["target_id"] == objective
        for edge in trace["edges"]
    )
    assert not any(edge["relation"] == "validates" and edge["target_id"] == task for edge in trace["edges"])
    with SessionLocal() as db:
        assert db.get(TaskRow, task).status == "created"
    with pytest.raises(AcceptanceError, match="not verification"):
        check_verification_acceptance(body, attempt_agent="native", artifact_sha256=DIGEST)


def test_orm_rejects_a_missing_class_and_a_mislinked_outcome():
    db = _session()
    try:
        _task_attempt(db, "ORM")
        binding = bound(db, "A-ORM")
        db.add(EvidenceRow(
            id="E-NULL", attempt_id="A-ORM", kind="evaluation", status="PASS", command="c",
            confidence=1.0, created_at=utcnow(), **binding,
        ))
        with pytest.raises(ValueError, match="evidence_class"):
            db.commit()
        db.rollback()
        db.add(EvidenceRow(
            id="E-BAD-OUT", attempt_id="A-ORM", objective_id="OBJ", kind="outcome", status="PASS",
            command="c", confidence=1.0, created_at=utcnow(), evidence_class="outcome", **binding,
        ))
        with pytest.raises(ValueError, match="outcome evidence"):
            db.commit()
        db.rollback()
        db.add(EvidenceRow(
            id="E-OK-OUT", attempt_id=None, objective_id="OBJ-OK", kind="outcome", status="PASS",
            command="signed off", confidence=1.0, created_at=utcnow(), evidence_class="outcome",
            verifier_identity="alex", verifier_role="stakeholder",
        ))
        db.commit()
        stored = db.get(EvidenceRow, "E-OK-OUT")
        assert stored.attempt_id is None and stored.objective_id == "OBJ-OK"
        assert stored.evidence_class == "outcome"
    finally:
        db.close()


def test_migration_0028_keeps_legacy_null_class_and_round_trips(tmp_path, monkeypatch):
    spec = importlib.util.spec_from_file_location(
        "m0028", Path("alembic/versions/0028_evidence_classes.py"),
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    assert module._SUBJECT_RULE == EVIDENCE_SUBJECT_SQL

    database_url = f"sqlite:///{tmp_path / 'm0028.sqlite'}"
    monkeypatch.setenv("SDF_DATABASE_URL", database_url)
    config = Config("alembic.ini")
    engine = create_engine(database_url)
    command.upgrade(config, "0026_oracle_visibility")
    with engine.begin() as connection:
        connection.execute(text(
            "INSERT INTO attempts (id, task_id, status, dispatch_key, agent, model_tier, cost_usd, created_at, "
            "evidence_mode) VALUES ('ATTEMPT-LEGACY', 'T', 'completed', 'legacy', 'old', 'basic', 0, '2026-10-01', "
            "'synthetic')"
        ))
        connection.execute(text(
            "INSERT INTO evidence (id, attempt_id, kind, status, command, confidence, evidence_mode, created_at) "
            "VALUES ('EV-LEGACY', 'ATTEMPT-LEGACY', 'evaluation', 'PASS', 'c', 1.0, 'synthetic', '2026-10-01')"
        ))
    command.upgrade(config, "head")

    def table_sql() -> str:
        with engine.connect() as connection:
            return connection.execute(text("SELECT sql FROM sqlite_master WHERE name = 'evidence'")).scalar()

    ddl = table_sql()
    assert "ck_evidence_evidence_mode" in ddl
    assert "ck_evidence_class_subject" in ddl
    assert "objective_id" in {column["name"] for column in inspect(engine).get_columns("evidence")}
    with engine.begin() as connection:
        legacy = connection.execute(text(
            "SELECT evidence_class, objective_id FROM evidence WHERE id = 'EV-LEGACY'"
        )).one()
        assert legacy == (None, None)
        connection.execute(text(
            "INSERT INTO evidence (id, attempt_id, objective_id, kind, status, command, confidence, "
            "evidence_mode, created_at, evidence_class, verifier_identity) "
            "VALUES ('EV-OUT', NULL, 'OBJ-1', 'outcome', 'PASS', 'signed off', 1.0, 'synthetic', "
            "'2026-11-01', 'outcome', 'alex')"
        ))
    with pytest.raises(RuntimeError, match="attempt_id"):
        command.downgrade(config, "0026_oracle_visibility")
    with engine.begin() as connection:
        connection.execute(text("DELETE FROM evidence WHERE id = 'EV-OUT'"))
    command.downgrade(config, "0026_oracle_visibility")
    assert "objective_id" not in {column["name"] for column in inspect(engine).get_columns("evidence")}
    command.upgrade(config, "head")
    with engine.connect() as connection:
        assert connection.execute(text(
            "SELECT evidence_class FROM evidence WHERE id = 'EV-LEGACY'"
        )).scalar() is None
    engine.dispose()


def _graph(client, suffix: str) -> str:
    context, objective = f"BC-IND-{suffix}", f"OBJ-IND-{suffix}"
    assert client.post("/business-contexts", json={
        "id": context, "title": "context", "owner": "product", "source": "test",
    }).status_code == 201
    assert client.post("/objectives", json={
        "id": objective, "title": "objective", "business_context_id": context,
        "owner": "product", "source": "test",
    }).status_code == 201
    return objective


def _task(client, objective: str | None, task_id: str) -> None:
    body = {"id": task_id, "title": task_id, "idempotency_key": task_id, "acceptance_criteria": ["met"]}
    if objective is not None:
        body["objective_id"] = objective
    assert client.post("/tasks", json=body).status_code == 201


def _attempt(db, *, attempt_id: str, task_id: str, agent: str, parent_attempt_id: str | None = None) -> None:
    db.add(AttemptRow(
        id=attempt_id, task_id=task_id, status="completed", dispatch_key=attempt_id,
        agent=agent, parent_attempt_id=parent_attempt_id, created_at=utcnow(),
    ))


def _outcome(client, objective: str, **overrides):
    body = {
        "verifier_identity": "alex",
        "verifier_role": "stakeholder",
        "status": "PASS",
        "summary": "incident minutes fell",
        "mode": "synthetic",
    }
    body.update(overrides)
    return client.post(f"/objectives/{objective}/evidence", json=body)


def test_outcome_evidence_rejects_the_producer_and_attempt_agents():
    """The verifier is neither the recorder nor an agent that worked the Objective.

    Attempts are not children of an Objective. They belong to a Task. That Task
    is under the Objective through an implements edge, or through a Plan whose
    objective_id is the Objective. A retry is another Attempt of the same Task.
    """

    setup = client_as("operator", principal="op-setup")
    suffix = uuid4().hex[:8]
    direct = _graph(setup, f"direct-{suffix}")
    planned = _graph(setup, f"plan-{suffix}")
    other = _graph(setup, f"other-{suffix}")
    direct_task, plan_task, other_task = (f"TASK-{kind}-{suffix}" for kind in ("direct", "plan", "other"))
    _task(setup, direct, direct_task)
    _task(setup, None, plan_task)
    _task(setup, other, other_task)
    plan_id = f"PLAN-IND-{suffix}"
    with SessionLocal() as db:
        db.add(PlanRow(
            id=plan_id, title="plan", objective_id=planned, status="approved", content={},
            content_sha256="ab" * 32, proposed_by="planner", created_at=utcnow(),
            decision_request_id=f"DREQ-IND-{suffix}",
        ))
        db.add(PlanTaskRow(task_id=plan_task, plan_id=plan_id, subtask_key="work"))
        # The plan gate reads a persisted Plan. An Attempt added in the same
        # flush cannot see an unflushed PlanRow.
        db.flush()
        _attempt(db, attempt_id=f"A-DIRECT-{suffix}", task_id=direct_task, agent="pi:worker-1")
        _attempt(
            db, attempt_id=f"A-RETRY-{suffix}", task_id=direct_task, agent="opencode:retry-1",
            parent_attempt_id=f"A-DIRECT-{suffix}",
        )
        _attempt(db, attempt_id=f"A-PLAN-{suffix}", task_id=plan_task, agent="codex:plan-worker")
        _attempt(db, attempt_id=f"A-OTHER-{suffix}", task_id=other_task, agent="goose:elsewhere")
        db.commit()

    recorder = client_as("approver", principal="casey")
    operator = client_as("operator", principal="op-denied")
    denied = _outcome(operator, direct, verifier_identity="alex")
    assert denied.status_code == 403
    assert "approver" in denied.json()["detail"]

    def rejected(objective: str, verifier: str, needle: str) -> None:
        response = _outcome(recorder, objective, verifier_identity=verifier)
        assert response.status_code == 422, response.text
        assert needle in response.json()["detail"]

    rejected(direct, "casey", "producer_identity")
    rejected(direct, "pi:worker-1", "attempt agent")
    rejected(direct, "  opencode:retry-1  ", "attempt agent")
    rejected(planned, "codex:plan-worker", "attempt agent")
    # An agent that worked a different Objective is not this Objective's verifier.
    elsewhere = _outcome(recorder, direct, verifier_identity="goose:elsewhere", mode="synthetic")
    assert elsewhere.status_code == 201, elsewhere.text
    with SessionLocal() as db:
        stored = list(db.scalars(select(EvidenceRow).where(EvidenceRow.objective_id == direct)))
        assert [row.verifier_identity for row in stored] == ["goose:elsewhere"]
        assert not db.scalars(select(DecisionEdgeRow).where(
            DecisionEdgeRow.source_id == stored[0].id, DecisionEdgeRow.relation == "validates",
        )).first()
        assert db.get(TaskRow, direct_task).status == "created"
    # The direct Task's agent is not an agent of the Plan's Objective, and the
    # Plan Task's agent is not an agent of the directly linked Objective.
    assert _outcome(recorder, planned, verifier_identity="pi:worker-1").status_code == 201
    assert _outcome(recorder, direct, verifier_identity="codex:plan-worker").status_code == 201


@pytest.mark.parametrize("mode", ["synthetic", "live"])
def test_independent_stakeholder_outcome_evidence_persists_mode_and_stays_fail_closed(mode: str):
    setup = client_as("operator", principal="op-mode")
    suffix = uuid4().hex[:8]
    objective = _graph(setup, f"mode-{mode}-{suffix}")
    task = f"TASK-MODE-{mode}-{suffix}"
    _task(setup, objective, task)
    recorder = client_as("approver", principal=f"casey-{mode}")
    missing = recorder.post(f"/objectives/{objective}/evidence", json={
        "verifier_identity": "alex", "verifier_role": "stakeholder", "summary": "signed off",
    })
    assert missing.status_code == 422
    bogus = _outcome(recorder, objective, mode="bogus")
    assert bogus.status_code == 422
    with SessionLocal() as db:
        before = build_scorecard_from_db(db).verified_pass_count
    created = _outcome(
        recorder, objective, verifier_identity="alex", verifier_role="stakeholder", mode=mode,
        summary=f"signed off ({mode})",
    )
    assert created.status_code == 201, created.text
    body = created.json()
    assert body["evidence_mode"] == mode
    assert body["evidence_class"] == "outcome"
    assert body["producer_identity"] == f"casey-{mode}"
    assert body["producer_role"] == "approver"
    assert body["verifier_identity"] == "alex"
    assert body["verifier_role"] == "stakeholder"
    assert body["attempt_id"] is None
    with SessionLocal() as db:
        row = db.get(EvidenceRow, body["id"])
        assert row.evidence_mode == mode
        assert row.objective_id == objective
        assert row.attempt_id is None
        assert not db.scalars(select(DecisionEdgeRow).where(
            DecisionEdgeRow.source_id == body["id"], DecisionEdgeRow.relation == "validates",
        )).first()
        assert db.scalars(select(DecisionEdgeRow).where(
            DecisionEdgeRow.source_id == body["id"],
            DecisionEdgeRow.target_id == objective,
            DecisionEdgeRow.relation == "measures",
        )).first()
        assert db.get(TaskRow, task).status == "created"
        assert build_scorecard_from_db(db).verified_pass_count == before
    with pytest.raises(AcceptanceError, match="not verification"):
        check_verification_acceptance(body, attempt_agent="pi:worker-1", artifact_sha256=DIGEST)
    with pytest.raises(AcceptanceError, match="held-out or randomized"):
        check_hidden_oracle_acceptance(
            [body], required=True, attempt_agent="pi:worker-1", artifact_sha256=DIGEST,
        )
