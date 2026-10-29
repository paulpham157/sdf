"""Live HTTP proof: Objective→Task→POST /tasks/{id}/run→E2B agent→Evidence→/trace (#46, ADR 0023).

Exercises the **public** run endpoint (not ExecutionService directly) with the
live adapter on Vultr Inference.  Gated on ``SDF_LIVE_FULL_LOOP=1`` (or
``SDF_LIVE_E2B=1``) plus ``E2B_API_KEY`` and ``VULTR_INFERENCE_API_KEY``.
Agents: ``SDF_LIVE_RUN_AGENTS`` (default ``opencode,pi,codex``).

Teardown lists only the sandboxes *this test* created (E2B metadata
``sdf_attempt_id``), kills them, and only then asserts nothing leaked.
Secrets are never printed.
"""

from __future__ import annotations

import os
from pathlib import Path

import pytest

from sdf_core.api import SessionLocal
from sdf_core.credentials import load_dotenv
from sdf_core.db import ArtifactRow, AttemptRow, EvidenceRow, RuntimeEventRow, UsageLedgerRow
from tests.auth_support import admin_client
from tests.test_herdr_e2b_execution import NEGATIVE, POSITIVE

ENV = dict(os.environ)
if os.environ.get("SDF_DOTENV"):
    load_dotenv(os.environ["SDF_DOTENV"], ENV)

_LIVE_FLAG = os.environ.get("SDF_LIVE_FULL_LOOP") == "1" or os.environ.get("SDF_LIVE_E2B") == "1"
LIVE = _LIVE_FLAG and bool(ENV.get("E2B_API_KEY")) and bool(ENV.get("VULTR_INFERENCE_API_KEY"))
AGENTS = [a.strip() for a in os.environ.get("SDF_LIVE_RUN_AGENTS", "opencode,pi,codex").split(",") if a.strip()]

pytestmark = pytest.mark.skipif(
    not LIVE,
    reason="live /run check: set SDF_LIVE_FULL_LOOP=1 with E2B_API_KEY and VULTR_INFERENCE_API_KEY",
)

CHECK_ADD = [["python3", "-c", "from calc import add; assert add(2, 3) == 5"]]


def _sandboxes_for(attempt_id: str) -> set[str]:
    from e2b import Sandbox, SandboxQuery

    paginator = Sandbox.list(query=SandboxQuery(metadata={"sdf_attempt_id": attempt_id}), api_key=ENV["E2B_API_KEY"])
    ids: set[str] = set()
    while paginator.has_next:
        ids.update(item.sandbox_id for item in paginator.next_items())
    return ids


def _kill_own(attempt_id: str) -> set[str]:
    from e2b import Sandbox

    found = _sandboxes_for(attempt_id)
    for sandbox_id in found:
        try:
            Sandbox.kill(sandbox_id, api_key=ENV["E2B_API_KEY"])
        except Exception:  # noqa: BLE001 - re-listed below
            pass
    return found


def _agent_output(attempt_id: str | None) -> list[str]:
    """Last screen/output lines of the attempt (diagnostics; redacted by the timeline)."""

    if not attempt_id:
        return []
    out: list[str] = []
    for entry in admin_client().get(f"/runs/{attempt_id}/timeline").json().get("entries", []):
        if entry.get("kind") == "runtime_output_observed":
            payload = (entry.get("payload") or {}).get("output")
            out.extend(payload if isinstance(payload, list) else [str(payload)])
    return out[-25:]


def _apply_live_env(monkeypatch: pytest.MonkeyPatch, agent: str, tmp_path: Path) -> None:
    monkeypatch.setenv("SDF_RUN_ADAPTER", "live")
    monkeypatch.setenv("SDF_RUN_AGENT", agent)
    monkeypatch.setenv("SDF_FIXTURES_ROOT", str(tmp_path))
    monkeypatch.setenv("E2B_API_KEY", ENV["E2B_API_KEY"])
    monkeypatch.setenv("VULTR_INFERENCE_API_KEY", ENV["VULTR_INFERENCE_API_KEY"])
    if ENV.get("E2B_DOMAIN"):
        monkeypatch.setenv("E2B_DOMAIN", ENV["E2B_DOMAIN"])
    monkeypatch.delenv("SDF_DOTENV", raising=False)


def _fixture(tmp_path: Path) -> Path:
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "calc.py").write_text("def add(a, b):\n    return a - b\n", encoding="utf-8")
    return fixture


@pytest.mark.parametrize("agent", AGENTS)
@pytest.mark.parametrize("case", [
    {"name": "positive", "suffix": "POS", "instructions": POSITIVE, "expected_task_status": "succeeded",
     "expected_edge": "validates", "expected_evidence": "PASS"},
    {"name": "negative", "suffix": "NEG", "instructions": NEGATIVE, "expected_task_status": "failed",
     "expected_edge": "contradicts", "expected_evidence": "FAIL"},
])
def test_live_full_loop_via_public_run_endpoint(tmp_path: Path, monkeypatch: pytest.MonkeyPatch, case: dict,
                                                agent: str):
    if case["name"] == "negative" and agent != AGENTS[0]:
        pytest.skip("negative path proven once per run")
    monkeypatch.chdir(tmp_path)
    _apply_live_env(monkeypatch, agent, tmp_path)
    client = admin_client()
    suffix = f"{case['suffix']}-{agent.upper()}"
    bc_id, obj_id, assumption_id, task_id = (f"BC-FULL-{suffix}", f"OBJ-FULL-{suffix}",
                                             f"ASSUMPTION-FULL-{suffix}", f"TASK-FULL-{suffix}")
    criterion = "add returns the sum"
    assert client.post("/business-contexts", json={"id": bc_id, "title": "Live full loop", "owner": "product",
                                                   "source": "live-test"}).status_code == 201
    assert client.post("/objectives", json={"id": obj_id, "title": "Prove public run path",
                                            "business_context_id": bc_id, "owner": "product",
                                            "source": "live-test"}).status_code == 201
    assert client.post("/assumptions", json={"id": assumption_id, "title": "add() is correct", "owner": "product",
                                             "source": "live-test"}).status_code == 201
    assert client.post("/tasks", json={"id": task_id, "title": f"Live full loop {case['name']} {agent}",
                                       "objective_id": obj_id, "idempotency_key": f"live-full-loop-{suffix}",
                                       "acceptance_criteria": [criterion]}).status_code == 201

    fixture = _fixture(tmp_path)
    attempt_id = None
    try:
        run = client.post(f"/tasks/{task_id}/run", json={
            "dispatch_key": f"live-full-http-{suffix}", "fixture_dir": str(fixture),
            "instructions": case["instructions"], "criterion_checks": {criterion: CHECK_ADD},
            "validation_target_kind": "assumption", "validation_target_id": assumption_id,
        })
        body = run.json()
        attempt_id = body.get("attempt_id") or (body.get("detail") or {}).get("attempt_id")
        assert run.status_code == 200, run.text
        assert body["task_status"] == case["expected_task_status"], (body, _agent_output(attempt_id))
    finally:
        # Kill this test's own sandboxes first, then assert none had leaked.
        leaked = _kill_own(attempt_id) if attempt_id else set()
        assert not leaked, f"sandbox(es) still listed after public run: {sorted(leaked)}"
        if attempt_id:
            assert not _sandboxes_for(attempt_id)

    db = SessionLocal()
    try:
        evidence = db.query(EvidenceRow).filter_by(attempt_id=attempt_id).all()
        artifacts = db.query(ArtifactRow).filter_by(attempt_id=attempt_id).all()
        events = db.query(RuntimeEventRow).filter_by(attempt_id=attempt_id, source="run-e2b").all()
        ledger = db.query(UsageLedgerRow).filter_by(attempt_id=attempt_id).all()
        attempt = db.get(AttemptRow, attempt_id)
        assert [(row.criterion, row.status) for row in evidence] == [(criterion, case["expected_evidence"])]
        log_ids = {a.id for a in artifacts if a.id.endswith("-LOG")}
        assert all(row.artifact_ref not in log_ids for row in evidence)
        kinds = [e.kind for e in sorted(events, key=lambda e: e.sequence)]
        assert kinds[0] == "runtime_started" and kinds[-1] == "runtime_terminated", kinds
        assert {r.kind for r in ledger} == {"turn", "sandbox"}
        assert attempt.provider.startswith("e2b:") and attempt.error_class is None
        turn = next(r for r in ledger if r.kind == "turn")
        assert turn.source in {"reported", "missing"}
        if turn.source == "reported":
            assert attempt.input_tokens and attempt.cost_usd > 0
    finally:
        db.close()

    timeline = client.get(f"/runs/{attempt_id}/timeline")
    assert timeline.status_code == 200 and {"runtime", "usage", "evidence"} <= {
        e["source"] for e in timeline.json()["entries"]}
    trace = client.get(f"/tasks/{task_id}/trace").json()
    assert {bc_id, obj_id, task_id, attempt_id, assumption_id} <= {node["id"] for node in trace["nodes"]}
    assert any(edge["source_id"].startswith("EVIDENCE-") and edge["target_id"] == assumption_id
               and edge["relation"] == case["expected_edge"] for edge in trace["edges"])


EGRESS_PROBE = r"""
import json, socket, urllib.request
out = {}
for name, url in (("vultr", "https://api.vultrinference.com/v1/models"), ("example", "https://example.com"),
                  ("pypi", "https://pypi.org/simple/")):
    try:
        urllib.request.urlopen(url, timeout=8)
        out[name] = "ok"
    except urllib.error.HTTPError as exc:
        out[name] = f"http {exc.code}"
    except Exception as exc:
        out[name] = "blocked:" + type(exc).__name__
print(json.dumps(out))
"""


def test_live_run_sandbox_egress_is_vultr_only(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """The /run sandbox reaches Vultr Inference (HTTP answer) and nothing else."""

    import json

    from sdf_core.run_adapter import configured_run_adapter

    _apply_live_env(monkeypatch, "opencode", tmp_path)
    attempt_id = "ATTEMPT-LIVE-EGRESS-" + os.urandom(4).hex()
    db = SessionLocal()
    adapter = configured_run_adapter(db, environ=os.environ)
    transport, _ = adapter._transport(attempt_id)
    try:
        raw = transport.exec_argv(["python3", "-c", EGRESS_PROBE], 60_000)
    finally:
        _sid, cleanup_error = adapter._teardown(transport)
        db.close()
        leaked = _kill_own(attempt_id)
    result = json.loads(raw.strip().splitlines()[-1])
    assert cleanup_error is None and not leaked
    assert result["vultr"] == "ok" or result["vultr"].startswith("http "), result
    assert result["example"].startswith("blocked") and result["pypi"].startswith("blocked"), result


def test_live_run_forced_timeout_is_classified(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """A turn past SDF_RUN_TURN_TIMEOUT_S is killed, classified ``timeout``, and its sandbox is gone."""

    _apply_live_env(monkeypatch, "opencode", tmp_path)
    monkeypatch.setenv("SDF_RUN_TURN_TIMEOUT_S", "5")
    client = admin_client()
    assert client.post("/tasks", json={"id": "TASK-LIVE-TIMEOUT", "title": "timeout", "acceptance_criteria": ["c"],
                                       "idempotency_key": "live-timeout-" + os.urandom(4).hex()}).status_code == 201
    fixture = _fixture(tmp_path)
    res = client.post("/tasks/TASK-LIVE-TIMEOUT/run", json={
        "fixture_dir": str(fixture), "criterion_checks": {"c": CHECK_ADD},
        "instructions": "Write a 3000-word essay about sandboxes to essay.md, then fix calc.py."})
    body = res.json()
    detail = body.get("detail") if isinstance(body.get("detail"), dict) else body
    attempt_id = detail.get("attempt_id")
    leaked = _kill_own(attempt_id) if attempt_id else set()
    assert not leaked
    assert detail.get("error_class") == "timeout", (res.status_code, body)
    db = SessionLocal()
    try:
        assert db.get(AttemptRow, attempt_id).error_class == "timeout"
        turns = db.query(UsageLedgerRow).filter_by(attempt_id=attempt_id, kind="turn").all()
        assert turns and turns[-1].error_class == "timeout"
    finally:
        db.close()
