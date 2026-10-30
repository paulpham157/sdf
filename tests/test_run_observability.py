"""ADR 0023: logging, redaction, usage ledger, error taxonomy, timeline, CLI, /run guards."""

from __future__ import annotations

import io
import json
import os

import pytest
from alembic import command
from alembic.config import Config
from sqlalchemy import create_engine, inspect, select

from sdf_core import observability as obs
from sdf_core import usage
from sdf_core.api import SessionLocal
from sdf_core.db import AttemptRow, RuntimeEventRow, TaskRow, UsageLedgerRow
from sdf_core.model import utcnow
from tests.auth_support import admin_client, client_as

client = admin_client()


# --- logging -------------------------------------------------------------------


def test_json_logging_carries_context_and_redacts(monkeypatch):
    monkeypatch.setenv("SDF_TEST_API_KEY", "sk-super-secret-value-123")
    stream = io.StringIO()
    obs.configure_logging({"SDF_LOG_FORMAT": "json", "SDF_LOG_LEVEL": "DEBUG"}, stream=stream)
    log = obs.get_logger("t")
    with obs.log_context(request_id="r1", attempt_id="A1"):
        log.info("hello %s", "sk-super-secret-value-123", extra={"auth": "Bearer abc.def.ghi", "n": 3})
    record = json.loads(stream.getvalue().splitlines()[-1])
    assert record["event"].startswith("hello") and record["request_id"] == "r1" and record["attempt_id"] == "A1"
    assert "sk-super-secret-value-123" not in stream.getvalue()
    assert "abc.def.ghi" not in stream.getvalue() and record["n"] == 3
    with pytest.raises(ValueError):
        with obs.log_context(bogus="x"):
            pass
    obs.configure_logging({"SDF_LOG_FORMAT": "text"}, stream=stream)


def test_text_logging_escapes_line_breaks_in_caller_values():
    stream = io.StringIO()
    obs.configure_logging({"SDF_LOG_FORMAT": "text"}, stream=stream)
    log = obs.get_logger("t")
    forged = "OBJ-1\n2026-01-01 INFO sdf.api forged_event\r"
    with obs.log_context(task_id="T-1\nfake"):
        log.info("observation %s", "a\r\nb", extra={"objective_id": forged})
    out = stream.getvalue()
    assert out.count("\n") == 1 and out.endswith("\n") and "\r" not in out
    assert "objective_id=OBJ-1\\n2026-01-01 INFO sdf.api forged_event\\r" in out
    assert "task_id=T-1\\nfake" in out and "observation a\\r\\nb" in out
    obs.configure_logging({"SDF_LOG_FORMAT": "json"}, stream=io.StringIO())


def test_redact_value_masks_secret_keys():
    out = obs.redact_value({"api_key": "x" * 20, "nested": {"password": "hunter2hunter2"}, "ok": "fine"})
    assert out["api_key"] == obs.REDACTED and out["nested"]["password"] == obs.REDACTED and out["ok"] == "fine"


@pytest.mark.parametrize("error,status,expected", [
    (None, "completed", None),
    (None, "running", "timeout"),
    (None, "terminated", "killed"),
    (RuntimeError("opencode turn exceeded 420s and was killed"), None, "timeout"),
    (RuntimeError("Error code: 429 - rate limit"), None, "provider_429"),
    (RuntimeError("upstream returned 503 Service Unavailable"), None, "provider_5xx"),
    (RuntimeError("HTTP 401 Unauthorized"), None, "provider_4xx"),
    (RuntimeError("E2B sandbox provisioning failed"), None, "sandbox_error"),
    (RuntimeError("something odd"), None, "agent_error"),
])
def test_error_taxonomy(error, status, expected):
    assert obs.classify_error(error, status=status) == expected
    assert expected is None or expected in obs.ERROR_CLASSES


def test_credential_error_is_config_error():
    from sdf_core.credentials import CredentialConfigError

    assert obs.classify_error(CredentialConfigError("x")) == "config_error"


# --- usage -----------------------------------------------------------------------


def test_session_usage_parsing_and_delta():
    before = usage.parse_session_usage('{"input_tokens": 10, "output_tokens": 2, "cache_read_tokens": 0, "found": true}')
    after = usage.parse_session_usage('noise\n{"input_tokens": 110, "output_tokens": 12, "cache_read_tokens": 5,'
                                      ' "models": ["glm-5.3"], "found": true}')
    delta = usage.session_delta(before, after)
    assert (delta.input_tokens, delta.output_tokens, delta.cache_read_tokens) == (100, 10, 5)
    assert usage.parse_session_usage("garbage") is None
    assert not usage.session_delta(None, None).has_tokens


def test_session_usage_script_reads_claude_and_codex_files(tmp_path):
    import subprocess
    import sys

    claude = tmp_path / ".claude" / "projects" / "p"
    claude.mkdir(parents=True)
    msg = {"type": "assistant", "message": {"id": "m1", "model": "glm-5.3",
                                            "usage": {"input_tokens": 7, "output_tokens": 3}}}
    (claude / "s.jsonl").write_text(json.dumps(msg) + "\n" + json.dumps(msg) + "\n")
    codex = tmp_path / ".codex" / "sessions" / "2026"
    codex.mkdir(parents=True)
    tc = {"type": "event_msg", "payload": {"type": "token_count", "info": {"total_token_usage": {
        "input_tokens": 50, "cached_input_tokens": 20, "output_tokens": 9}}}}
    (codex / "r.jsonl").write_text(json.dumps(tc) + "\n")
    for agent, expect in (("claude", (7, 3)), ("codex", (30, 9))):
        out = subprocess.run([sys.executable, "-c", usage.SESSION_USAGE_SCRIPT, agent], capture_output=True,
                             text=True, env={**os.environ, "HOME": str(tmp_path)}, check=True).stdout
        parsed = usage.parse_session_usage(out)
        assert (parsed["input_tokens"], parsed["output_tokens"]) == expect, (agent, parsed)


def test_price_override_and_sandbox_rate():
    assert usage.price_for("glm-5.3") == (0.75, 3.0)
    assert usage.price_for("glm-5.3", {"SDF_PRICE_GLM_5_3": "1,2"}) == (1.0, 2.0)
    assert usage.sandbox_rate("e2b", {"SDF_E2B_USD_PER_SECOND": "0.001"}) == 0.001


def test_migration_creates_usage_ledger(tmp_path, monkeypatch):
    url = f"sqlite:///{tmp_path / 'm.sqlite'}"
    monkeypatch.setenv("SDF_DATABASE_URL", url)
    config = Config("alembic.ini")
    command.upgrade(config, "head")
    inspector = inspect(create_engine(url))
    assert "usage_ledger" in inspector.get_table_names()
    assert "error_class" in {c["name"] for c in inspector.get_columns("attempts")}
    assert "trace_id" in {c["name"] for c in inspector.get_columns("attempts")}
    assert "trace_id" in {c["name"] for c in inspector.get_columns("agent_runs")}
    command.downgrade(config, "0016_usage_ledger")
    inspector = inspect(create_engine(url))
    assert "trace_id" not in {c["name"] for c in inspector.get_columns("attempts")}
    assert "trace_id" not in {c["name"] for c in inspector.get_columns("agent_runs")}
    command.downgrade(config, "0015_api_auth")
    inspector = inspect(create_engine(url))
    assert "usage_ledger" not in inspector.get_table_names()


def test_ledger_rows_are_append_only():
    db = SessionLocal()
    try:
        row = usage.record_sandbox(db, attempt_id="A-LEDGER", agent="pi", sandbox_id="s", sandbox_provider="e2b",
                                   seconds=10, status="killed", error_class=None)
        db.commit()
        row.seconds = 1
        with pytest.raises(ValueError, match="append-only"):
            db.flush()
        db.rollback()
    finally:
        db.close()


# --- timeline / CLI ----------------------------------------------------------------


def _seed_attempt(attempt_id: str) -> None:
    db = SessionLocal()
    try:
        db.add(TaskRow(id=f"T-{attempt_id}", title="t", status="failed", idempotency_key=f"k-{attempt_id}",
                       acceptance_criteria=[], created_at=utcnow()))
        db.add(AttemptRow(id=attempt_id, task_id=f"T-{attempt_id}", status="failed", dispatch_key=f"d-{attempt_id}",
                          agent="pi", error_class="timeout", created_at=utcnow()))
        db.add(RuntimeEventRow(source="run-e2b", attempt_id=attempt_id, session_id="S", sequence=1,
                               kind="runtime_started", status="running", payload={"token": "sk-abcdefghijklmnop"},
                               created_at=utcnow()))
        usage.record_turn(db, attempt_id=attempt_id, agent="pi", turn=1, session_id="S", sandbox_id="sbx",
                          sandbox_provider="e2b", usage=usage.TurnUsage(input_tokens=5, output_tokens=1),
                          status="running", error_class="timeout", model="glm-5.3")
        db.commit()
    finally:
        db.close()


def test_timeline_endpoint_merges_sources_and_is_viewer_readable():
    _seed_attempt("A-TL-1")
    viewer = client_as("viewer")
    res = viewer.get("/runs/A-TL-1/timeline")
    assert res.status_code == 200, res.text
    body = res.json()
    assert body["kind"] == "attempt" and body["error_class"] == "timeout"
    sources = {e["source"] for e in body["entries"]}
    assert {"attempt", "runtime", "usage"} <= sources
    assert "sk-abcdefghijklmnop" not in res.text
    turn = next(e for e in body["entries"] if e["kind"] == "usage_turn")
    assert turn["error_class"] == "timeout" and turn["usage_source"] == "reported"
    assert viewer.get("/runs/NOPE/timeline").status_code == 404


def test_cli_runs_show(tmp_path, monkeypatch, capsys):
    from sdf_core import cli
    from sdf_core.db import Base, make_engine
    from sqlalchemy.orm import sessionmaker

    url = f"sqlite:///{tmp_path / 'cli.sqlite'}"
    engine = make_engine(url)
    Base.metadata.create_all(engine)
    with sessionmaker(engine)() as db:
        db.add(AttemptRow(id="A-CLI", task_id="T", status="failed", dispatch_key="d", agent="pi",
                          error_class="cleanup_failed", created_at=utcnow()))
        db.commit()
    assert cli.main(["runs", "show", "A-CLI", "--database-url", url]) == 0
    assert "error_class=cleanup_failed" in capsys.readouterr().out
    assert cli.main(["runs", "show", "A-CLI", "--json", "--database-url", url]) == 0
    assert json.loads(capsys.readouterr().out)["id"] == "A-CLI"
    assert cli.main(["runs", "show", "missing", "--database-url", url]) == 1
    monkeypatch.delenv("SDF_DATABASE_URL", raising=False)
    assert cli.main(["runs", "show", "x"]) == 2


# --- /run guards ---------------------------------------------------------------------


def test_fixture_dir_confined_to_fixtures_root(tmp_path, monkeypatch):
    root = tmp_path / "fixtures"
    (root / "ok").mkdir(parents=True)
    (root / "ok" / "app.py").write_text("x\n")
    outside = tmp_path / "secret"
    outside.mkdir()
    (root / "link").symlink_to(outside)
    monkeypatch.setenv("SDF_FIXTURES_ROOT", str(root))
    client.post("/tasks", json={"id": "TASK-FIXROOT", "title": "f", "idempotency_key": "fixroot"})
    for bad in (str(outside), str(root / "ok" / ".." / ".." / "secret"), str(root / "link"), "../secret",
                str(root / "missing")):
        res = client.post("/tasks/TASK-FIXROOT/run", json={"fixture_dir": bad})
        assert res.status_code == 400, (bad, res.text)
        assert client.post("/plans", json={"title": "p", "fixture_dir": bad, "tasks": []}).status_code in (400, 422)
    assert client.post("/tasks/TASK-FIXROOT/run", json={"fixture_dir": "ok"}).status_code == 200
    # The fixtures root itself is not a fixture (it resolved to the cwd for Plans before).
    assert client.post("/tasks/TASK-FIXROOT/run", json={"fixture_dir": "."}).status_code == 400


def test_plan_stores_the_resolved_confined_fixture(tmp_path, monkeypatch):
    import uuid

    from sdf_core.db import PlanRow
    from tests.agent_comm_support import make_fixture, plan_dict

    root = tmp_path / "fixtures"
    fixture = make_fixture(root)
    # A same-named directory next to the cwd must never be what a Plan runs on.
    monkeypatch.chdir(tmp_path)
    (tmp_path / fixture.name).mkdir()
    monkeypatch.setenv("SDF_FIXTURES_ROOT", str(root))
    for bad in (".", str(root), "..", str(tmp_path / fixture.name)):
        body = {**plan_dict(fixture, f"PLAN-FX-{uuid.uuid4().hex[:8]}"), "fixture_dir": bad}
        res = client.post("/plans", json=body)
        assert res.status_code == 400, (bad, res.text)
    for raw in (fixture.name, str(fixture)):
        plan_id = f"PLAN-FX-{uuid.uuid4().hex[:8]}"
        res = client.post("/plans", json={**plan_dict(fixture, plan_id), "fixture_dir": raw})
        assert res.status_code == 201, (raw, res.text)
        with SessionLocal() as session:
            stored = session.get(PlanRow, plan_id).content["fixture_dir"]
        assert stored == str(fixture.resolve()) and os.path.isabs(stored), (raw, stored)


def test_run_config_error_is_400_and_busy_is_429(tmp_path, monkeypatch):
    from sdf_core import run_adapter

    fixture = tmp_path / "fx"
    fixture.mkdir()
    client.post("/tasks", json={"id": "TASK-RUNCFG", "title": "f", "idempotency_key": "runcfg"})
    monkeypatch.setenv("SDF_RUN_ADAPTER", "live")
    monkeypatch.setenv("SDF_RUN_AGENT", "nope")
    res = client.post("/tasks/TASK-RUNCFG/run", json={"fixture_dir": str(fixture)})
    assert res.status_code == 400 and "SDF_RUN_AGENT" in res.text
    monkeypatch.setenv("SDF_SANDBOX_PROVIDER", "fly")
    monkeypatch.delenv("SDF_RUN_AGENT")
    assert client.post("/tasks/TASK-RUNCFG/run", json={"fixture_dir": str(fixture)}).status_code == 400
    monkeypatch.delenv("SDF_SANDBOX_PROVIDER")
    # Since #70, configured_run_adapter() validates provider keys; use dummies so
    # the busy path is reached without depending on the real environment.
    monkeypatch.setenv("E2B_API_KEY", "dummy-e2b-key")
    monkeypatch.setenv("VULTR_INFERENCE_API_KEY", "dummy-vultr-key")
    monkeypatch.setenv("DAYTONA_API_KEY", "dummy-daytona-key")

    def busy(adapter):
        raise run_adapter.RunBusyError("full")

    import sdf_core.api as api
    from contextlib import contextmanager

    @contextmanager
    def full(adapter):
        busy(adapter)
        yield

    monkeypatch.setattr(api, "run_slot", full)
    res = client.post("/tasks/TASK-RUNCFG/run", json={"fixture_dir": str(fixture)})
    assert res.status_code == 429 and res.headers["retry-after"] == "30"


def test_request_id_header_round_trips():
    res = client.get("/healthz", headers={"X-Request-ID": "abc-123"})
    assert res.headers["x-request-id"] == "abc-123"
    assert len(client.get("/healthz", headers={"X-Request-ID": "bad id!"}).headers["x-request-id"]) == 32
