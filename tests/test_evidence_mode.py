"""Evidence mode for ``/run`` (ADR 0031): live adapter -> ``live``, fake -> ``synthetic``."""

from __future__ import annotations

from pathlib import Path

import pytest
from alembic.config import Config
from sqlalchemy import create_engine, insert, select, text
from sqlalchemy.exc import IntegrityError
from sqlalchemy.orm import sessionmaker

from alembic import command
from sdf_core import run_adapter as ra
from sdf_core.adapter import FakeNativeAdapter
from sdf_core.api import factory_evidence_mode
from sdf_core.coordinator import EVIDENCE_MODES
from sdf_core.db import (
    AttemptRow,
    Base,
    EvidenceRow,
    TaskRow,
    UsageLedgerRow,
    make_engine,
)
from sdf_core.execution import ExecutionService
from sdf_core.impact import EVIDENCE_MODE_VALUES, EvidenceMode, declared_evidence_mode
from sdf_core.impact_repository import load_scorecard_facts
from sdf_core.model import utcnow
from sdf_core.qualification import qualify_agents
from sdf_core.run_adapter import LiveRunAdapter, configured_run_adapter
from tests.test_run_adapter import LIVE, _Completing, _Transport

CHECK = {"tests pass": [["python", "-c", "print('ok')"]]}
ALEMBIC = Config(str(Path(__file__).parents[1] / "alembic.ini"))


@pytest.fixture()
def db():
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as session:
        session.add(TaskRow(id="T", title="t", status="created", idempotency_key="t",
                            acceptance_criteria=["tests pass"], created_at=utcnow()))
        session.commit()
        yield session


@pytest.fixture()
def fixture(tmp_path):
    path = tmp_path / "fixture"
    path.mkdir()
    (path / "app.py").write_text("print('old')\n", encoding="utf-8")
    return path


def _service(db, tmp_path, adapter):
    return ExecutionService(db, workspace_root=tmp_path / "work", artifact_root=tmp_path / "artifacts", adapter=adapter)


# --- one value set, declared by the run path ----------------------------------

def test_one_value_set_and_only_a_declared_live_path_is_live():
    assert set(EVIDENCE_MODE_VALUES) == {m.value for m in EvidenceMode} == set(EVIDENCE_MODES)
    assert declared_evidence_mode(LiveRunAdapter) is EvidenceMode.LIVE
    assert declared_evidence_mode(FakeNativeAdapter("a", "b")) is EvidenceMode.SYNTHETIC
    for other in (None, object(), type("X", (), {"evidence_mode": "LIVE"}), type("Y", (), {"evidence_mode": "bogus"})):
        assert declared_evidence_mode(other) is EvidenceMode.SYNTHETIC
    assert factory_evidence_mode(None) == "synthetic"


# --- /run writes the declared mode on the Attempt and its Evidence -------------

def test_fake_adapter_run_stays_synthetic(db, tmp_path, fixture):
    attempt = _service(db, tmp_path, FakeNativeAdapter("app.py", "print('new')\n")).run(
        task_id="T", dispatch_key="fake", fixture=fixture, instructions="x", commands=[], criterion_checks=CHECK)
    assert attempt.evidence_mode == "synthetic"
    assert {e.evidence_mode for e in db.scalars(select(EvidenceRow).where(EvidenceRow.attempt_id == attempt.id))} \
        == {"synthetic"}


def test_live_adapter_run_records_live_attempt_and_evidence(db, tmp_path, fixture, monkeypatch):
    monkeypatch.setattr(ra.time, "sleep", lambda s: None)
    adapter = configured_run_adapter(db, environ={**LIVE, "SDF_RUN_AGENT": "pi"})
    adapter._transport_factory = lambda **kw: (_Transport(), None)
    adapter._kill_by_id = lambda sid: None
    runtime = _Completing([{"input_tokens": 0, "output_tokens": 0}, {"input_tokens": 3, "output_tokens": 2}])
    monkeypatch.setattr(adapter, "_runtime", lambda t: runtime)

    attempt = _service(db, tmp_path, adapter).run(
        task_id="T", dispatch_key="live", fixture=fixture, instructions="x", commands=[], criterion_checks=CHECK)
    evidence = db.scalars(select(EvidenceRow).where(EvidenceRow.attempt_id == attempt.id)).all()
    assert attempt.evidence_mode == "live" and evidence and {e.evidence_mode for e in evidence} == {"live"}
    # The ledger sandbox row, the authoritative live signal for routing, is there too.
    assert db.scalar(select(UsageLedgerRow).where(UsageLedgerRow.attempt_id == attempt.id,
                                                  UsageLedgerRow.kind == "sandbox")) is not None
    # The scorecard now sees this run as live.
    facts = load_scorecard_facts(db)
    assert {f.mode for f in facts.evidence if f.attempt_id == attempt.id} == {EvidenceMode.LIVE}


def test_a_failed_live_attempt_is_still_a_live_attempt(db, tmp_path, fixture):
    class Exploding:
        evidence_mode = "live"
        provider = "e2b"

        def run(self, **kwargs):
            raise RuntimeError("sandbox died")

    with pytest.raises(RuntimeError):
        _service(db, tmp_path, Exploding()).run(task_id="T", dispatch_key="boom", fixture=fixture,
                                                 instructions="x", commands=[], criterion_checks=CHECK)
    attempt = db.scalar(select(AttemptRow).where(AttemptRow.dispatch_key == "boom"))
    assert attempt.status == "failed" and attempt.evidence_mode == "live"
    assert db.scalars(select(EvidenceRow).where(EvidenceRow.attempt_id == attempt.id)).all() == []


def test_the_run_request_cannot_set_the_evidence_mode():
    from tests.auth_support import admin_client

    assert admin_client().post("/tasks/NOPE/run", json={"evidence_mode": "live"}).status_code in (404, 422)


# --- routing keeps the ledger sandbox row as the live signal --------------------

def test_live_evidence_alone_does_not_make_a_routing_sample(db):
    # A plan-run style Attempt: live Evidence, but no per-Attempt ledger sandbox row.
    db.add(AttemptRow(id="A-PLAN", task_id="T", status="completed", dispatch_key="d-plan", agent="pi",
                      evidence_mode="live", created_at=utcnow()))
    db.add(EvidenceRow(id="E-PLAN", attempt_id="A-PLAN", kind="evaluation", status="PASS", command="c",
                       exit_code=0, confidence=1.0, criterion="c", evidence_mode="live", created_at=utcnow()))
    db.flush()
    q = qualify_agents(db, ["pi"])["pi"]
    assert not q.qualified and q.reason == "no finished live attempt measured"


# --- the database holds only known modes ----------------------------------------

def test_create_all_schema_rejects_an_unknown_mode(db):
    for table, values in (
        (EvidenceRow.__table__, dict(id="E-X", attempt_id="A", kind="k", status="PASS", command="c",
                                     confidence=1.0, created_at=utcnow())),
        (AttemptRow.__table__, dict(id="A-X", task_id="T", status="created", dispatch_key="d-x", agent="a",
                                    created_at=utcnow())),
    ):
        with pytest.raises(IntegrityError):
            db.execute(insert(table).values(**values, evidence_mode="production"))
        db.rollback()
        db.execute(insert(table).values(**values, evidence_mode="live"))
        db.rollback()


def test_migration_0021_adds_the_check_and_round_trips(tmp_path, monkeypatch):
    url = f"sqlite:///{tmp_path / 'm.sqlite'}"
    monkeypatch.setenv("SDF_DATABASE_URL", url)
    command.upgrade(ALEMBIC, "head")
    engine = create_engine(url)
    row = ("INSERT INTO evidence (id, attempt_id, kind, status, command, confidence, evidence_mode, created_at) "
           "VALUES (:id, 'A', 'k', 'PASS', 'c', 1.0, :mode, '2026-10-30')")
    with engine.begin() as c:
        c.execute(text(row), {"id": "E-LIVE", "mode": "live"})
    with pytest.raises(IntegrityError), engine.begin() as c:
        c.execute(text(row), {"id": "E-BAD", "mode": "production"})
    command.downgrade(ALEMBIC, "0020_routing_decisions")
    with engine.begin() as c:  # constraint gone, data kept
        assert c.execute(text("SELECT evidence_mode FROM evidence WHERE id = 'E-LIVE'")).scalar() == "live"
    command.upgrade(ALEMBIC, "head")
    engine.dispose()


def test_migration_0021_refuses_rows_with_unknown_modes_and_keeps_old_synthetic_rows(tmp_path, monkeypatch):
    url = f"sqlite:///{tmp_path / 'legacy.sqlite'}"
    monkeypatch.setenv("SDF_DATABASE_URL", url)
    command.upgrade(ALEMBIC, "0020_routing_decisions")
    engine = create_engine(url)
    with engine.begin() as c:
        c.execute(text("INSERT INTO evidence (id, attempt_id, kind, status, command, confidence, evidence_mode, "
                       "created_at) VALUES ('E-OLD', 'A', 'k', 'PASS', 'c', 1.0, 'synthetic', '2026-10-01')"))
        c.execute(text("INSERT INTO evidence (id, attempt_id, kind, status, command, confidence, evidence_mode, "
                       "created_at) VALUES ('E-ODD', 'A', 'k', 'PASS', 'c', 1.0, 'staging', '2026-10-01')"))
    with pytest.raises(RuntimeError, match="evidence_mode outside"):
        command.upgrade(ALEMBIC, "head")
    with engine.begin() as c:  # nothing rewritten
        assert dict(c.execute(text("SELECT id, evidence_mode FROM evidence")).all()) == {
            "E-OLD": "synthetic", "E-ODD": "staging"}
    engine.dispose()
