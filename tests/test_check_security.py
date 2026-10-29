"""Regression tests for issue #52: /run criterion checks ran as uncontained host Python.

The original repro had two halves:
  1. a caller-supplied check could read the server's environment (secrets) and echo it
     into evidence/artifacts;
  2. a caller-supplied ``/any/dir/bin/python`` passed the basename-only allowlist, so an
     arbitrary script ran on the host with the server's environment.

ADR 0025 removes caller-supplied argv from the public API (check ids only, resolved
from a server-controlled registry) and hardens the evaluator for every caller.
"""

from __future__ import annotations

import json
import os
import stat
import sys
import time
import uuid
from pathlib import Path

import pytest

from sdf_core.api import SessionLocal
from sdf_core.checks import CheckConfigError, CheckRegistry, UnknownCheckError
from sdf_core.db import ArtifactRow, AttemptRow, EvidenceRow
from sdf_core.evaluator import DeterministicEvaluator
from tests.auth_support import admin_client
from tests.check_support import write_checks

CANARY_ENV = "SDF_TEST_SERVER_CANARY"
CANARY = "canary-value-must-not-leak-7f3a"
PRINT_CANARY = f"import os; print(os.environ.get({CANARY_ENV!r}, 'absent'))"

client = admin_client()


def _fake_python(directory: Path, marker: Path) -> Path:
    """A script named ``python`` outside any trusted PATH: running it writes ``marker``."""
    bin_dir = directory / "bin"
    bin_dir.mkdir(parents=True)
    script = bin_dir / "python"
    script.write_text(f"#!/bin/sh\necho pwned > {marker}\nexit 0\n", encoding="utf-8")
    script.chmod(script.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
    return script


def _workspace(tmp_path: Path) -> Path:
    workspace = tmp_path / "ws"
    workspace.mkdir(exist_ok=True)
    return workspace


def _fixture(tmp_path: Path) -> Path:
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    return fixture


def _task(criteria: list[str]) -> str:
    task_id = f"TASK-SEC52-{uuid.uuid4().hex[:8]}"
    created = client.post("/tasks", json={"id": task_id, "title": "sec52", "idempotency_key": task_id,
                                          "acceptance_criteria": criteria})
    assert created.status_code == 201
    return task_id


def _attempt_text(task_id: str) -> str:
    """Everything persisted for the task's attempts: evidence rows and artifact bytes."""
    db = SessionLocal()
    try:
        chunks: list[str] = []
        for attempt in db.query(AttemptRow).filter_by(task_id=task_id):
            for row in db.query(EvidenceRow).filter_by(attempt_id=attempt.id):
                chunks.append(json.dumps({k: str(v) for k, v in vars(row).items() if not k.startswith("_")}))
            for artifact in db.query(ArtifactRow).filter_by(attempt_id=attempt.id):
                path = Path(artifact.uri.removeprefix("file://"))
                if path.is_file():
                    chunks.append(path.read_text(encoding="utf-8", errors="replace"))
        return "\n".join(chunks)
    finally:
        db.close()


# --- HTTP: the original repro ------------------------------------------------------------


def test_http_run_rejects_fake_absolute_python_and_never_executes_it(tmp_path):
    marker = tmp_path / "pwned.txt"
    fake = _fake_python(tmp_path / "evil", marker)
    task_id = _task(["c"])
    response = client.post(f"/tasks/{task_id}/run", json={
        "fixture_dir": str(_fixture(tmp_path)), "criterion_checks": {"c": [[str(fake), "-c", "pass"]]},
    })
    assert response.status_code == 422
    assert not marker.exists(), "caller-supplied executable ran on the host"


def test_http_run_rejects_caller_argv_that_reads_server_env(tmp_path, monkeypatch):
    monkeypatch.setenv(CANARY_ENV, CANARY)
    task_id = _task(["c"])
    response = client.post(f"/tasks/{task_id}/run", json={
        "fixture_dir": str(_fixture(tmp_path)), "criterion_checks": {"c": [["python", "-c", PRINT_CANARY]]},
    })
    assert response.status_code == 422
    assert CANARY not in response.text
    assert CANARY not in _attempt_text(task_id)


def test_http_registered_check_does_not_see_server_env(tmp_path, monkeypatch):
    monkeypatch.setenv(CANARY_ENV, CANARY)
    fixture = _fixture(tmp_path)
    write_checks(fixture, {"env-probe": ["python", "-c", PRINT_CANARY]})
    task_id = _task(["c"])
    response = client.post(f"/tasks/{task_id}/run", json={
        "fixture_dir": str(fixture), "criterion_checks": {"c": ["env-probe"]},
    })
    assert response.status_code == 200, response.text
    assert response.json()["task_status"] == "succeeded"
    persisted = _attempt_text(task_id)
    assert "absent" in persisted
    assert CANARY not in persisted


def test_http_unknown_check_id_is_rejected_before_any_attempt(tmp_path):
    task_id = _task(["c"])
    response = client.post(f"/tasks/{task_id}/run", json={
        "fixture_dir": str(_fixture(tmp_path)), "criterion_checks": {"c": ["not-registered"]},
    })
    assert response.status_code == 400
    assert "not-registered" in response.text
    db = SessionLocal()
    try:
        assert db.query(AttemptRow).filter_by(task_id=task_id).count() == 0
    finally:
        db.close()


def test_http_run_rejects_unknown_fields(tmp_path):
    task_id = _task([])
    response = client.post(f"/tasks/{task_id}/run", json={
        "fixture_dir": str(_fixture(tmp_path)), "checks_override": {"c": [["python", "-c", "pass"]]},
    })
    assert response.status_code == 422


def test_http_invalid_fixture_manifest_is_a_400_not_a_run(tmp_path):
    fixture = _fixture(tmp_path)
    write_checks(fixture, {"bad": ["/bin/sh", "-c", "true"]})
    task_id = _task(["c"])
    response = client.post(f"/tasks/{task_id}/run", json={
        "fixture_dir": str(fixture), "criterion_checks": {"c": ["bad"]},
    })
    assert response.status_code == 400
    assert "check registry error" in response.text


def test_http_evaluator_containment_required_without_backend_is_inconclusive(tmp_path, monkeypatch):
    monkeypatch.setenv("SDF_EVALUATOR_CONTAINMENT", "require")
    monkeypatch.delenv("SDF_CONTAINMENT_BACKEND", raising=False)
    fixture = _fixture(tmp_path)
    write_checks(fixture, {"ok": ["python", "-c", "print('ok')"]})
    task_id = _task(["c"])
    response = client.post(f"/tasks/{task_id}/run", json={
        "fixture_dir": str(fixture), "criterion_checks": {"c": ["ok"]},
    })
    assert response.status_code == 200, response.text
    assert response.json()["task_status"] == "inconclusive"


def test_http_bad_evaluator_containment_mode_is_rejected(tmp_path, monkeypatch):
    monkeypatch.setenv("SDF_EVALUATOR_CONTAINMENT", "sometimes")
    task_id = _task([])
    response = client.post(f"/tasks/{task_id}/run", json={"fixture_dir": str(_fixture(tmp_path))})
    assert response.status_code == 400


# --- evaluator hardening (applies to plan checks too) ------------------------------------


def test_evaluator_does_not_inherit_server_env(tmp_path, monkeypatch):
    monkeypatch.setenv(CANARY_ENV, CANARY)
    result = DeterministicEvaluator().evaluate(
        attempt_id="ATTEMPT-SEC52-ENV", workspace=tmp_path, commands=[["python", "-c", PRINT_CANARY]],
    )
    assert result.status == "PASS"
    assert result.evidence[0].stdout.strip() == "absent"
    assert all(CANARY not in (e.stdout + e.stderr) for e in result.evidence)


def test_evaluator_rejects_absolute_path_python_without_running_it(tmp_path):
    marker = tmp_path / "pwned.txt"
    fake = _fake_python(tmp_path / "evil", marker)
    result = DeterministicEvaluator().evaluate(
        attempt_id="ATTEMPT-SEC52-ABS", workspace=_workspace(tmp_path), commands=[[str(fake), "-c", "pass"]],
    )
    assert result.status == "INCONCLUSIVE"
    assert not marker.exists()


def test_evaluator_ignores_python_planted_in_workspace_on_path(tmp_path, monkeypatch):
    workspace = tmp_path / "ws"
    workspace.mkdir()
    marker = tmp_path / "pwned.txt"
    planted = _fake_python(workspace, marker)
    monkeypatch.setenv("PATH", f"{planted.parent}{os.pathsep}{os.environ.get('PATH', '')}")
    result = DeterministicEvaluator().evaluate(
        attempt_id="ATTEMPT-SEC52-PLANT", workspace=workspace, commands=[["python", "-c", "print('real')"]],
    )
    assert not marker.exists(), "evaluator executed an agent-planted interpreter"
    assert result.status == "PASS"
    assert result.evidence[0].stdout.strip() == "real"


def test_evaluator_timeout_kills_the_whole_process_group(tmp_path):
    marker = tmp_path / "child-survived.txt"
    child = f"import time; time.sleep(2); open({str(marker)!r}, 'w').write('alive')"
    parent = (f"import subprocess, sys, time; subprocess.Popen([sys.executable, '-c', {child!r}]); "
              "time.sleep(30)")
    started = time.monotonic()
    result = DeterministicEvaluator(timeout_seconds=1).evaluate(
        attempt_id="ATTEMPT-SEC52-TIMEOUT", workspace=_workspace(tmp_path), commands=[["python", "-c", parent]],
    )
    assert time.monotonic() - started < 10
    assert result.status == "INCONCLUSIVE"
    assert "timeout" in result.evidence[0].stderr
    time.sleep(2.5)
    assert not marker.exists(), "a child of the timed-out check kept running"


def test_evaluator_output_is_bounded(tmp_path):
    result = DeterministicEvaluator(max_output_bytes=1024).evaluate(
        attempt_id="ATTEMPT-SEC52-OUT", workspace=tmp_path, commands=[["python", "-c", "print('x' * 100000)"]],
    )
    assert len(result.evidence[0].stdout) <= 4096


def test_evaluator_required_containment_without_backend_is_inconclusive(tmp_path):
    marker = tmp_path / "ran.txt"
    result = DeterministicEvaluator(require_containment=True).evaluate(
        attempt_id="ATTEMPT-SEC52-REQ", workspace=_workspace(tmp_path),
        commands=[["python", "-c", f"open({str(marker)!r}, 'w')"]],
    )
    assert result.status == "INCONCLUSIVE"
    assert not marker.exists()


def test_evaluator_allowlist_can_only_be_narrowed(tmp_path):
    result = DeterministicEvaluator(allowed_executables={"python", "sh"}).evaluate(
        attempt_id="ATTEMPT-SEC52-WIDEN", workspace=tmp_path, commands=[["sh", "-c", "true"]],
    )
    assert result.status == "INCONCLUSIVE"


# --- registry ------------------------------------------------------------------------------


def test_registry_resolves_ids_from_fixture_and_server_file(tmp_path):
    fixture = _fixture(tmp_path)
    write_checks(fixture, {"unit": {"argv": ["pytest", "-q"], "timeout_seconds": 5}})
    server_file = tmp_path / "server-checks.json"
    server_file.write_text(json.dumps({"checks": {"lint": ["ruff", "check", "."]}}), encoding="utf-8")
    registry = CheckRegistry.load(fixture, environ={"SDF_EVALUATOR_CHECKS_FILE": str(server_file)})
    assert set(registry.ids()) == {"unit", "lint"}
    resolved = registry.resolve({"tests pass": ["unit", "lint"]})
    assert resolved == {"tests pass": [["pytest", "-q"], ["ruff", "check", "."]]}
    assert registry.timeout_for(["pytest", "-q"]) == 5
    with pytest.raises(UnknownCheckError):
        registry.resolve({"x": ["missing"]})


@pytest.mark.parametrize("argv", [
    ["/usr/bin/python3", "-c", "pass"],
    ["./python", "-c", "pass"],
    ["bash", "-c", "true"],
    ["python", "-c", "a\x00b"],
    [],
])
def test_registry_rejects_unsafe_argv(tmp_path, argv):
    fixture = _fixture(tmp_path)
    write_checks(fixture, {"bad": argv})
    with pytest.raises(CheckConfigError):
        CheckRegistry.load(fixture, environ={})


def test_registry_rejects_duplicate_ids_across_sources(tmp_path):
    fixture = _fixture(tmp_path)
    write_checks(fixture, {"unit": ["pytest"]})
    server_file = tmp_path / "server-checks.json"
    server_file.write_text(json.dumps({"checks": {"unit": ["pytest", "-x"]}}), encoding="utf-8")
    with pytest.raises(CheckConfigError):
        CheckRegistry.load(fixture, environ={"SDF_EVALUATOR_CHECKS_FILE": str(server_file)})


@pytest.mark.skipif(sys.platform == "win32", reason="symlinks")
def test_registry_refuses_symlinked_fixture_manifest(tmp_path):
    fixture = _fixture(tmp_path)
    real = tmp_path / "elsewhere.json"
    real.write_text(json.dumps({"checks": {"unit": ["pytest"]}}), encoding="utf-8")
    (fixture / "sdf-checks.json").symlink_to(real)
    with pytest.raises(CheckConfigError):
        CheckRegistry.load(fixture, environ={})


def test_registry_rejects_unknown_keys_and_bad_timeouts(tmp_path):
    fixture = _fixture(tmp_path)
    write_checks(fixture, {"unit": {"argv": ["pytest"], "env": {"X": "1"}}})
    with pytest.raises(CheckConfigError):
        CheckRegistry.load(fixture, environ={})
    write_checks(fixture, {"unit": {"argv": ["pytest"], "timeout_seconds": 100000}})
    with pytest.raises(CheckConfigError):
        CheckRegistry.load(fixture, environ={})
