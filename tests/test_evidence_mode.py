"""Evidence mode for ``/run`` and plan runs (ADR 0031).

``live`` means the run reached a real sandbox (E2B or Daytona, via the shared
live adapter); fake runs, config errors and sandbox-creation failures stay
``synthetic``.
"""

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
from sdf_core.api import SessionLocal, factory_evidence_mode
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
from sdf_core.impact import (
    EVIDENCE_MODE_VALUES,
    EvidenceMode,
    declared_evidence_mode,
    initial_evidence_mode,
)
from sdf_core.impact_repository import load_scorecard_facts
from sdf_core.model import utcnow
from sdf_core.qualification import qualify_agents
from sdf_core.run_adapter import (
    LiveRunAdapter,
    RunAdapterConfigError,
    configured_run_adapter,
)
from tests.auth_support import admin_client
from tests.check_support import write_checks
from tests.test_run_adapter import LIVE, _Completing, _Transport

CHECK = {"tests pass": [["python", "-c", "print('ok')"]]}
ALEMBIC = Config(str(Path(__file__).parents[1] / "alembic.ini"))
DAYTONA = {**LIVE, "SDF_SANDBOX_PROVIDER": "daytona", "SDF_DAYTONA_SNAPSHOT": "snap",
           "SDF_RUN_DAYTONA_UNRESTRICTED_EGRESS": "1", "DAYTONA_API_KEY": "daytona_unit_test_not_a_key"}


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


def _run(db, tmp_path, fixture, adapter, key="k"):
    return _service(db, tmp_path, adapter).run(task_id="T", dispatch_key=key, fixture=fixture, instructions="x",
                                               commands=[], criterion_checks=CHECK)


def _live(db, monkeypatch, env=LIVE, transport=None, runtime=None):
    monkeypatch.setattr(ra.time, "sleep", lambda s: None)
    adapter = configured_run_adapter(db, environ={**env, "SDF_RUN_AGENT": "pi"})
    adapter._transport_factory = lambda **kw: (transport or _Transport(), None)
    adapter._kill_by_id = lambda sid: None
    runtime = runtime or _Completing([{"input_tokens": 0, "output_tokens": 0}, {"input_tokens": 3, "output_tokens": 2}])
    monkeypatch.setattr(adapter, "_runtime", lambda t: runtime)
    return adapter


def _evidence_modes(db, attempt_id):
    return {e.evidence_mode for e in db.scalars(select(EvidenceRow).where(EvidenceRow.attempt_id == attempt_id))}


# --- one value set, declared by the run path ----------------------------------

def test_one_value_set_and_only_a_declared_live_path_is_live():
    assert set(EVIDENCE_MODE_VALUES) == {m.value for m in EvidenceMode} == set(EVIDENCE_MODES)
    assert declared_evidence_mode(LiveRunAdapter) is EvidenceMode.LIVE
    # The live adapter starts every Attempt synthetic and promotes it itself.
    assert initial_evidence_mode(LiveRunAdapter) is EvidenceMode.SYNTHETIC
    assert declared_evidence_mode(FakeNativeAdapter("a", "b")) is EvidenceMode.SYNTHETIC
    for other in (None, object(), type("X", (), {"evidence_mode": "LIVE"}), type("Y", (), {"evidence_mode": "bogus"})):
        assert declared_evidence_mode(other) is EvidenceMode.SYNTHETIC
    assert factory_evidence_mode(None) == "synthetic"


def test_begin_creates_the_row_with_its_mode_and_rejects_unknown_modes(db, tmp_path, fixture):
    service = _service(db, tmp_path, FakeNativeAdapter("app.py", "x\n"))
    begun = service.begin(task_id="T", dispatch_key="b1", fixture=fixture, evidence_mode="live")
    db.commit()
    with sessionmaker(db.get_bind())() as other:  # committed with the mode, no later flip
        assert other.get(AttemptRow, begun.attempt.id).evidence_mode == "live"
    with pytest.raises(ValueError, match="evidence_mode"):
        service.begin(task_id="T", dispatch_key="b2", fixture=fixture, evidence_mode="production")


# --- /run: fake, live (E2B and Daytona), and pre-sandbox failures ---------------

def test_fake_adapter_run_stays_synthetic(db, tmp_path, fixture):
    attempt = _run(db, tmp_path, fixture, FakeNativeAdapter("app.py", "print('new')\n"))
    assert attempt.evidence_mode == "synthetic" and _evidence_modes(db, attempt.id) == {"synthetic"}


@pytest.mark.parametrize("env", [LIVE, DAYTONA], ids=["e2b", "daytona"])
def test_live_adapter_run_records_live_attempt_and_evidence(db, tmp_path, fixture, monkeypatch, env):
    adapter = _live(db, monkeypatch, env)
    assert adapter.provider == ("daytona" if env is DAYTONA else "e2b")
    attempt = _run(db, tmp_path, fixture, adapter)
    assert attempt.evidence_mode == "live" and _evidence_modes(db, attempt.id) == {"live"}
    # The ledger sandbox row, the authoritative live signal for routing, is there too.
    assert db.scalar(select(UsageLedgerRow).where(UsageLedgerRow.attempt_id == attempt.id,
                                                  UsageLedgerRow.kind == "sandbox")) is not None
    facts = load_scorecard_facts(db)
    assert {f.mode for f in facts.evidence if f.attempt_id == attempt.id} == {EvidenceMode.LIVE}
    assert {f.mode for f in facts.attempts if f.attempt_id == attempt.id} == {EvidenceMode.LIVE}


def test_live_is_committed_as_soon_as_the_sandbox_exists(db, tmp_path, fixture, monkeypatch):
    """A process that dies mid-run leaves a live RUNNING Attempt (the sandbox existed)."""

    seen = {}

    class Peeking(_Completing):
        def send(self, session_id, input_text):
            with sessionmaker(db.get_bind())() as other:
                row = other.scalar(select(AttemptRow).where(AttemptRow.dispatch_key == "peek"))
                seen["mode"], seen["status"] = row.evidence_mode, row.status
            return super().send(session_id, input_text)

    _run(db, tmp_path, fixture, _live(db, monkeypatch, runtime=Peeking()), key="peek")
    assert seen == {"mode": "live", "status": "running"}


def test_missing_credentials_fail_before_any_attempt_exists(db, tmp_path, fixture, monkeypatch):
    for env, key in ((LIVE, "E2B_API_KEY"), (DAYTONA, "DAYTONA_API_KEY")):
        with pytest.raises(RunAdapterConfigError, match=key):
            configured_run_adapter(db, environ={k: v for k, v in env.items() if k != key})
    # Through the public endpoint: 400 and no Attempt row at all.
    for name in ("E2B_API_KEY", "DAYTONA_API_KEY"):
        monkeypatch.delenv(name, raising=False)
    monkeypatch.setenv("SDF_RUN_ADAPTER", "live")
    client = admin_client()
    assert client.post("/tasks", json={"id": "TASK-EVMODE-NOKEY", "title": "t", "idempotency_key": "evmode-nokey",
                                       "acceptance_criteria": ["c"]}).status_code == 201
    write_checks(fixture, {"ok": ["python", "-c", "print('ok')"]})
    response = client.post("/tasks/TASK-EVMODE-NOKEY/run", json={"fixture_dir": str(fixture)})
    assert response.status_code == 400 and "E2B_API_KEY" in response.text
    with SessionLocal() as api_db:
        assert api_db.scalars(select(AttemptRow).where(AttemptRow.task_id == "TASK-EVMODE-NOKEY")).all() == []


def test_sandbox_creation_failure_is_not_live(db, tmp_path, fixture, monkeypatch):
    # (a) the transport (sandbox) cannot be created at all.
    adapter = _live(db, monkeypatch)

    def no_sandbox(**kw):
        raise RuntimeError("sandbox create failed")

    adapter._transport_factory = no_sandbox
    with pytest.raises(RuntimeError):
        _run(db, tmp_path, fixture, adapter, key="no-transport")
    # (b) the sandbox is created lazily and its creation fails on first use.
    lazy = _Transport()
    lazy.sandbox_id = None

    class FailsToStart(_Completing):
        def start(self, *, attempt_id, agent):
            raise RuntimeError("sandbox create failed")

    with pytest.raises(RuntimeError):
        _run(db, tmp_path, fixture, _live(db, monkeypatch, transport=lazy, runtime=FailsToStart()), key="lazy")
    for key in ("no-transport", "lazy"):
        attempt = db.scalar(select(AttemptRow).where(AttemptRow.dispatch_key == key))
        assert (attempt.status, attempt.evidence_mode) == ("failed", "synthetic")
        assert _evidence_modes(db, attempt.id) == set()


def test_a_failed_live_attempt_after_the_sandbox_existed_is_live(db, tmp_path, fixture, monkeypatch):
    runtime = _Completing(fail=RuntimeError("agent crashed"))
    with pytest.raises(RuntimeError):
        _run(db, tmp_path, fixture, _live(db, monkeypatch, runtime=runtime), key="crash")
    attempt = db.scalar(select(AttemptRow).where(AttemptRow.dispatch_key == "crash"))
    assert (attempt.status, attempt.evidence_mode) == ("failed", "live")


def test_idempotent_replay_does_not_change_the_stored_mode(db, tmp_path, fixture, monkeypatch):
    first = _run(db, tmp_path, fixture, FakeNativeAdapter("app.py", "print('new')\n"), key="same")
    adapter = _live(db, monkeypatch)
    adapter._transport_factory = lambda **kw: pytest.fail("a replay must not create a sandbox")
    again = _run(db, tmp_path, fixture, adapter, key="same")
    assert again.id == first.id and again.evidence_mode == "synthetic"
    assert _evidence_modes(db, first.id) == {"synthetic"}


def test_run_response_and_timeline_carry_the_attempt_mode(fixture):
    client = admin_client()
    assert client.post("/tasks", json={"id": "TASK-EVMODE-API", "title": "t", "idempotency_key": "evmode-api",
                                       "acceptance_criteria": ["c"]}).status_code == 201
    write_checks(fixture, {"ok": ["python", "-c", "print('ok')"]})
    body = client.post("/tasks/TASK-EVMODE-API/run",
                       json={"fixture_dir": str(fixture), "criterion_checks": {"c": ["ok"]}}).json()
    assert body["evidence_mode"] == "synthetic"
    entries = client.get(f"/runs/{body['attempt_id']}/timeline").json()["entries"]
    assert [e["evidence_mode"] for e in entries if e.get("kind") == "attempt_created"] == ["synthetic"]


def test_the_run_request_cannot_set_the_evidence_mode():
    client = admin_client()
    assert client.post("/tasks", json={"id": "TASK-EVMODE-422", "title": "t", "idempotency_key": "evmode-422",
                                       "acceptance_criteria": ["c"]}).status_code == 201
    response = client.post("/tasks/TASK-EVMODE-422/run", json={"evidence_mode": "live"})
    assert response.status_code == 422
    assert any("evidence_mode" in map(str, err.get("loc", ())) for err in response.json()["detail"])


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
        (EvidenceRow.__table__, {"id": "E-X", "attempt_id": "A", "kind": "k", "status": "PASS", "command": "c",
                                 "confidence": 1.0, "created_at": utcnow()}),
        (AttemptRow.__table__, {"id": "A-X", "task_id": "T", "status": "created", "dispatch_key": "d-x",
                                "agent": "a", "created_at": utcnow()}),
    ):
        with pytest.raises(IntegrityError):
            db.execute(insert(table).values(**values, evidence_mode="production"))
        db.rollback()
        db.execute(insert(table).values(**values, evidence_mode="live"))
        db.rollback()


EVIDENCE_SQL = ("INSERT INTO evidence (id, attempt_id, kind, status, command, confidence, evidence_mode, created_at) "
                "VALUES (:id, 'A', 'k', 'PASS', 'c', 1.0, :mode, '2026-10-30')")
ATTEMPT_SQL = ("INSERT INTO attempts (id, task_id, status, dispatch_key, agent, evidence_mode, created_at) "
               "VALUES (:id, 'T', 'created', :id, 'pi', :mode, '2026-10-30')")


def test_migration_0021_adds_the_check_and_round_trips(tmp_path, monkeypatch):
    url = f"sqlite:///{tmp_path / 'm.sqlite'}"
    monkeypatch.setenv("SDF_DATABASE_URL", url)
    command.upgrade(ALEMBIC, "head")
    engine = create_engine(url)
    with engine.begin() as c:
        c.execute(text(EVIDENCE_SQL), {"id": "E-LIVE", "mode": "live"})
        c.execute(text(ATTEMPT_SQL), {"id": "A-LIVE", "mode": "live"})
    for sql in (EVIDENCE_SQL, ATTEMPT_SQL):
        with pytest.raises(IntegrityError), engine.begin() as c:
            c.execute(text(sql), {"id": "X-BAD", "mode": "production"})
    command.downgrade(ALEMBIC, "0020_routing_decisions")
    with engine.begin() as c:  # constraint gone, data kept
        assert c.execute(text("SELECT evidence_mode FROM evidence WHERE id = 'E-LIVE'")).scalar() == "live"
        assert c.execute(text("SELECT evidence_mode FROM attempts WHERE id = 'A-LIVE'")).scalar() == "live"
    command.upgrade(ALEMBIC, "head")
    engine.dispose()


@pytest.mark.parametrize("table, sql", [("evidence", EVIDENCE_SQL), ("attempts", ATTEMPT_SQL)])
def test_migration_0021_refuses_rows_with_unknown_modes_and_keeps_them(tmp_path, monkeypatch, table, sql):
    url = f"sqlite:///{tmp_path / 'legacy.sqlite'}"
    monkeypatch.setenv("SDF_DATABASE_URL", url)
    command.upgrade(ALEMBIC, "0020_routing_decisions")
    engine = create_engine(url)
    with engine.begin() as c:
        c.execute(text(sql), {"id": "OLD", "mode": "synthetic"})
        c.execute(text(sql), {"id": "ODD", "mode": "staging"})
    with pytest.raises(RuntimeError, match=f"1 {table} row"):
        command.upgrade(ALEMBIC, "head")
    with engine.begin() as c:  # nothing rewritten
        assert dict(c.execute(text(f"SELECT id, evidence_mode FROM {table}")).all()) == {
            "OLD": "synthetic", "ODD": "staging"}
    engine.dispose()
