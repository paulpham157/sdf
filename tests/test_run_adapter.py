"""Unit tests for the public run adapter (no live keys, no sandboxes)."""

from __future__ import annotations

from pathlib import Path

import pytest
from sqlalchemy import select
from sqlalchemy.orm import sessionmaker

from sdf_core.adapter import FakeNativeAdapter
from sdf_core.agent_sandbox import DEFAULT_NETWORK, DEFAULT_TEMPLATE
from sdf_core.credentials import AGENT_KINDS
from sdf_core.db import AttemptRow, Base, RuntimeEventRow, TaskRow, UsageLedgerRow, make_engine
from sdf_core.model import utcnow
from sdf_core.runtime import FakeRuntime, RuntimeSession, RuntimeStatus
from sdf_core import run_adapter as ra
from sdf_core.run_adapter import (
    LiveRunAdapter,
    RunAdapterConfigError,
    RunBusyError,
    configured_run_adapter,
    resolve_sandbox_provider,
    run_environ,
    run_network,
    run_slot,
)

LIVE = {"SDF_RUN_ADAPTER": "live", "E2B_API_KEY": "e2b_unit_test_not_a_key", "VULTR_INFERENCE_API_KEY": "vk-unit-test-value"}


def _db():
    engine = make_engine()
    Base.metadata.create_all(engine)
    db = sessionmaker(engine, expire_on_commit=False)()
    db.add(TaskRow(id="T", title="t", status="running", idempotency_key="t", acceptance_criteria=[], created_at=utcnow()))
    db.add(AttemptRow(id="A", task_id="T", status="running", dispatch_key="d", agent="opencode", created_at=utcnow()))
    db.flush()
    return db


def test_defaults_to_fake():
    assert isinstance(configured_run_adapter(_db(), environ={}), FakeNativeAdapter)
    assert isinstance(configured_run_adapter(_db(), environ={"SDF_RUN_ADAPTER": "fake"}), FakeNativeAdapter)


def test_rejects_unknown_mode_and_agent():
    with pytest.raises(RunAdapterConfigError, match="SDF_RUN_ADAPTER"):
        configured_run_adapter(_db(), environ={"SDF_RUN_ADAPTER": "acp"})
    with pytest.raises(RunAdapterConfigError, match="SDF_RUN_AGENT"):
        configured_run_adapter(_db(), environ={**LIVE, "SDF_RUN_AGENT": "aider"})


def test_resolve_sandbox_provider():
    assert resolve_sandbox_provider({}) == "e2b"
    assert resolve_sandbox_provider({"SDF_SANDBOX_PROVIDER": "Daytona"}) == "daytona"
    with pytest.raises(RunAdapterConfigError, match="SDF_SANDBOX_PROVIDER"):
        resolve_sandbox_provider({"SDF_SANDBOX_PROVIDER": "fly"})


@pytest.mark.parametrize("agent", AGENT_KINDS)
def test_all_six_kinds_build_with_vultr_defaults(agent):
    adapter = configured_run_adapter(_db(), environ={**LIVE, "SDF_RUN_AGENT": agent})
    assert isinstance(adapter, LiveRunAdapter)
    assert adapter.agent == agent and adapter.image == DEFAULT_TEMPLATE
    assert adapter.network["allow_out"] == list(DEFAULT_NETWORK["allow_out"])
    assert adapter.network["deny_out"] == list(DEFAULT_NETWORK["deny_out"])
    assert adapter.model  # Vultr default model resolved for every kind


def test_run_environ_is_agent_config_only():
    env = run_environ("codex", {**LIVE, "SDF_DOTENV": "/x/.env", "SDF_AUTH_PEPPER": "p", "SDF_DATABASE_URL": "pg",
                                "OPENAI_API_KEY": "nope", "SDF_HERDR_CODEX_MODEL": "legacy-model"})
    assert "SDF_DOTENV" not in env and "SDF_AUTH_PEPPER" not in env and "SDF_DATABASE_URL" not in env
    assert "OPENAI_API_KEY" not in env
    assert env["SDF_CODEX_API_KEY_ENV"] == "VULTR_INFERENCE_API_KEY"
    assert env["VULTR_INFERENCE_API_KEY"] == LIVE["VULTR_INFERENCE_API_KEY"]
    assert env["SDF_OPENAI_BASE_URL"] == "https://api.vultrinference.com/v1"
    assert env["SDF_CODEX_MODEL"] == "legacy-model"  # deprecated alias honoured
    assert run_environ("codex", {**LIVE, "SDF_CODEX_MODEL": "m", "SDF_HERDR_CODEX_MODEL": "x"})["SDF_CODEX_MODEL"] == "m"


def test_run_network_extra_hosts_and_open_cidr_rejected():
    net = run_network({"SDF_RUN_EGRESS_ALLOW": "pypi.org, files.pythonhosted.org"})
    assert net["allow_out"][-2:] == ["pypi.org", "files.pythonhosted.org"]
    assert "0.0.0.0/0" in net["deny_out"]
    assert run_network({})["allow_out"] == list(DEFAULT_NETWORK["allow_out"])
    with pytest.raises(RunAdapterConfigError):
        run_network({"SDF_RUN_EGRESS_ALLOW": "0.0.0.0/0"})


def test_daytona_requires_explicit_unrestricted_egress_opt_in():
    env = {**LIVE, "SDF_SANDBOX_PROVIDER": "daytona", "SDF_DAYTONA_SNAPSHOT": "snap", "DAYTONA_API_KEY": "d"}
    with pytest.raises(RunAdapterConfigError, match="UNRESTRICTED_EGRESS"):
        configured_run_adapter(_db(), environ=env)
    adapter = configured_run_adapter(_db(), environ={**env, "SDF_RUN_DAYTONA_UNRESTRICTED_EGRESS": "1"})
    assert adapter.provider == "daytona" and adapter.image == "snap" and adapter.network is None
    with pytest.raises(RunAdapterConfigError, match="SDF_DAYTONA_SNAPSHOT"):
        configured_run_adapter(_db(), environ={**LIVE, "SDF_SANDBOX_PROVIDER": "daytona"})


def test_fails_closed_without_e2b_key(tmp_path):
    # ADR 0031: a missing key is a config error before any Attempt exists.
    with pytest.raises(RunAdapterConfigError, match="E2B_API_KEY"):
        configured_run_adapter(_db(), environ={"SDF_RUN_ADAPTER": "live"})
    with pytest.raises(RunAdapterConfigError, match="DAYTONA_API_KEY"):
        configured_run_adapter(_db(), environ={**LIVE, "SDF_SANDBOX_PROVIDER": "daytona", "SDF_DAYTONA_SNAPSHOT": "s",
                                               "SDF_RUN_DAYTONA_UNRESTRICTED_EGRESS": "1"})


def test_concurrency_slot_429(monkeypatch):
    adapter = configured_run_adapter(_db(), environ={**LIVE, "SDF_RUN_MAX_CONCURRENT": "1"})
    with run_slot(adapter):
        with pytest.raises(RunBusyError):
            with run_slot(adapter):
                pass
    with run_slot(adapter):
        pass
    with run_slot(FakeNativeAdapter("a", "b")), run_slot(FakeNativeAdapter("a", "b")):
        pass


# --- teardown / ledger with fakes ---------------------------------------------


class _Completing(FakeRuntime):
    def __init__(self, usage_steps=None, fail=None):
        super().__init__()
        self._usage = list(usage_steps or [])
        self._fail = fail

    def send(self, session_id, input_text):
        if self._fail:
            raise self._fail
        return super().send(session_id, input_text)

    def status(self, session_id):
        cur = super().status(session_id)
        if cur.status is RuntimeStatus.RUNNING:
            cur = RuntimeSession(cur.session_id, cur.attempt_id, cur.agent, RuntimeStatus.COMPLETED, cur.output)
            self._sessions[session_id] = cur
        return cur

    def usage(self, session_id):
        return self._usage.pop(0) if self._usage else {"input_tokens": 0, "output_tokens": 0}


class _Transport:
    def __init__(self, close_error=None):
        self.sandbox_id = "sbx-1"
        self.close_error = close_error
        self.closed = 0

    def close(self):
        self.closed += 1
        sid, self.sandbox_id = self.sandbox_id, None
        if self.close_error:
            raise self.close_error


def _adapter(monkeypatch, runtime, transport, kill=None, agent="opencode"):
    db = _db()
    monkeypatch.setattr(ra.time, "sleep", lambda s: None)
    adapter = configured_run_adapter(db, environ={**LIVE, "SDF_RUN_AGENT": agent})
    adapter._transport_factory = lambda **kw: (transport, None)
    adapter._kill_by_id = kill or (lambda sid: None)
    monkeypatch.setattr(adapter, "_runtime", lambda t: runtime)
    return db, adapter


def test_success_records_turn_and_sandbox_rows_on_attempt(monkeypatch, tmp_path):
    runtime = _Completing([{"input_tokens": 0, "output_tokens": 0},
                           {"input_tokens": 1000, "output_tokens": 200, "cost": 0.0}])
    db, adapter = _adapter(monkeypatch, runtime, _Transport())
    result = adapter.run(attempt_id="A", workspace=tmp_path, instructions="hi")
    assert result.status == "completed" and result.exit_code == 0
    rows = db.scalars(select(UsageLedgerRow).order_by(UsageLedgerRow.id)).all()
    assert [r.kind for r in rows] == ["turn", "sandbox"]
    assert rows[0].source == "reported" and rows[0].input_tokens == 1000 and rows[0].cost_source == "estimated"
    assert rows[1].sandbox_id == "sbx-1" and rows[1].status == "killed"
    attempt = db.get(AttemptRow, "A")
    assert attempt.input_tokens == 1000 and attempt.output_tokens == 200 and attempt.cost_usd > 0
    assert attempt.error_class is None and attempt.provider.startswith("e2b:")


def test_zero_usage_is_missing_not_zero(monkeypatch, tmp_path):
    db, adapter = _adapter(monkeypatch, _Completing(), _Transport())
    adapter.run(attempt_id="A", workspace=tmp_path, instructions="hi")
    turn = db.scalars(select(UsageLedgerRow).where(UsageLedgerRow.kind == "turn")).one()
    assert turn.source == "missing" and turn.input_tokens is None
    assert db.get(AttemptRow, "A").input_tokens is None


def test_cleanup_failure_is_retried_then_surfaced(monkeypatch, tmp_path):
    kills = []

    def kill(sid):
        kills.append(sid)
        raise RuntimeError("api down")

    db, adapter = _adapter(monkeypatch, _Completing(), _Transport(RuntimeError("close boom")), kill)
    result = adapter.run(attempt_id="A", workspace=tmp_path, instructions="hi")
    assert result.status == "cleanup_failed" and result.exit_code == 1
    assert kills == ["sbx-1"] * ra.KILL_RETRIES
    sandbox = db.scalars(select(UsageLedgerRow).where(UsageLedgerRow.kind == "sandbox")).one()
    assert sandbox.error_class == "cleanup_failed" and "close boom" in sandbox.detail["cleanup_error"]
    assert db.get(AttemptRow, "A").error_class == "cleanup_failed"
    event = db.scalars(select(RuntimeEventRow).where(RuntimeEventRow.source == "run-e2b-cleanup")).one()
    assert event.payload["cleanup_failed"] is True


def test_cleanup_retry_success_is_not_a_failure(monkeypatch, tmp_path):
    db, adapter = _adapter(monkeypatch, _Completing(), _Transport(RuntimeError("flaky")))
    assert adapter.run(attempt_id="A", workspace=tmp_path, instructions="hi").status == "completed"
    assert db.get(AttemptRow, "A").error_class is None


def test_turn_error_is_classified_and_reraised(monkeypatch, tmp_path):
    runtime = _Completing(fail=RuntimeError("opencode turn exceeded 420s and was killed"))
    transport = _Transport()
    db, adapter = _adapter(monkeypatch, runtime, transport)
    with pytest.raises(RuntimeError, match="exceeded"):
        adapter.run(attempt_id="A", workspace=tmp_path, instructions="hi")
    assert transport.closed == 1
    turn = db.scalars(select(UsageLedgerRow).where(UsageLedgerRow.kind == "turn")).one()
    assert turn.error_class == "timeout"
    assert db.get(AttemptRow, "A").error_class == "timeout"


def test_e2b_transport_gets_network_and_metadata(monkeypatch, tmp_path):
    seen = {}

    def factory(**kw):
        seen.update(kw)
        return _Transport(), None

    db, adapter = _adapter(monkeypatch, _Completing(), _Transport())
    adapter._transport_factory = factory
    adapter.run(attempt_id="A", workspace=tmp_path, instructions="hi")
    assert seen["network"]["allow_out"] == list(DEFAULT_NETWORK["allow_out"])
    assert seen["metadata"]["sdf_attempt_id"] == "A"
    assert seen["template"] == DEFAULT_TEMPLATE and seen["agents"] == ("opencode",)
    assert "SDF_DOTENV" not in seen["environ"]


def test_sandbox_id_survives_runtime_closing_the_transport(monkeypatch, tmp_path):
    """Herdr's terminate closes the transport (clearing its id) before teardown runs."""

    transport = _Transport()

    class _ClosesOnTerminate(_Completing):
        def terminate(self, session_id):
            transport.close()
            return super().terminate(session_id)

    db, adapter = _adapter(monkeypatch, _ClosesOnTerminate(), transport)
    adapter.run(attempt_id="A", workspace=tmp_path, instructions="hi")
    sandbox = db.scalars(select(UsageLedgerRow).where(UsageLedgerRow.kind == "sandbox")).one()
    turn = db.scalars(select(UsageLedgerRow).where(UsageLedgerRow.kind == "turn")).one()
    assert sandbox.sandbox_id == "sbx-1" == turn.sandbox_id
    assert adapter.last_run["sandbox_id"] == "sbx-1"
