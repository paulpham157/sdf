"""PR #64 review: registered checks must not be forgeable from the workspace (ADR 0025).

Each forgery case below used to turn a failing check into PASS (or leak the
server environment) when the agent controlled the workspace the check runs in.
"""

from __future__ import annotations

import json
import os
import signal
import subprocess
import sys
import textwrap
import uuid
from pathlib import Path

import pytest

from sdf_core import api
from sdf_core.api import SessionLocal, app
from sdf_core.checks import CheckConfigError, CheckRegistry, check_targets, validate_argv
from sdf_core.db import ArtifactRow, AttemptRow, EvidenceRow
from sdf_core.acceptance import EVALUATOR_IDENTITY, check_verification_acceptance
from sdf_core.evaluator import CLEARED_ENV, DeterministicEvaluator, _check_env
from sdf_core.oracle import script_oracle_verdict
from sdf_core.plans import task_id_for
from tests.agent_comm_support import fake_factory, lead_behavior, make_fixture, plan_dict, worker_behavior
from tests.auth_support import admin_client, client_as
from tests.check_support import write_checks

PYVER = f"python{sys.version_info.major}.{sys.version_info.minor}"
client = admin_client()


@pytest.fixture(autouse=True)
def _venv_on_path(monkeypatch):
    """Bare `pytest` resolves on the server PATH; make sure the venv's is there."""
    monkeypatch.setenv("PATH", f"{os.path.dirname(sys.executable)}{os.pathsep}{os.environ.get('PATH', '')}")


def _write(root: Path, files: dict[str, str]) -> Path:
    root.mkdir(parents=True)
    for name, content in files.items():
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(textwrap.dedent(content), encoding="utf-8")
    return root


def _run(tmp_path: Path, files: dict[str, str], argv: list[str], *, server: dict[str, str] | None = None,
         subject_artifact_sha256: str | None = None, **kwargs):
    """Run ``argv`` in a workspace holding ``files``.

    The server fixture holds ``server`` (default: the files the check names,
    as the operator registered them) so the check's own files verify.
    """

    tag = uuid.uuid4().hex[:6]
    ws = _write(tmp_path / f"ws-{tag}", files)
    if server is None:
        try:
            named = set(check_targets(argv))
        except ValueError:
            named = set()
        server = {name: content for name, content in files.items() if name in named}
    reference = _write(tmp_path / f"fixture-{tag}", server)
    return DeterministicEvaluator(**kwargs)._run_one(
        command=argv, workspace=ws, attempt_id="A", index=1, criterion="c", reference=reference,
        subject_artifact_sha256=subject_artifact_sha256,
    )


FAILING_TEST = "def test_x():\n    assert False\n"
EXIT0 = "import os; os._exit(0)\n"

FORGERIES = {
    "stdlib-shadow": ({"fractions.py": EXIT0}, ["python3", "-c", "import fractions; raise SystemExit(1)"]),
    "conftest-exitstatus": ({"test_a.py": FAILING_TEST, "conftest.py": textwrap.dedent("""
        def pytest_sessionfinish(session, exitstatus):
            session.exitstatus = 0
        """)}, ["pytest", "-q", "test_a.py"]),
    "pytest-ini-addopts-plugin": ({"test_a.py": FAILING_TEST, "pytest.ini": "[pytest]\naddopts = -p evil\npythonpath = .\n",
                                   "evil.py": EXIT0}, ["pytest", "-q", "test_a.py"]),
    "pyproject-addopts-plugin": ({"test_a.py": FAILING_TEST, "evil.py": EXIT0,
                                  "pyproject.toml": "[tool.pytest.ini_options]\naddopts = '-p evil'\npythonpath = ['.']\n"},
                                 ["pytest", "-q", "test_a.py"]),
    "pytest-module-shadow": ({"test_a.py": FAILING_TEST, "pytest.py": EXIT0}, ["python", "-m", "pytest", "-q", "test_a.py"]),
    "user-site-pth": ({f".local/lib/{PYVER}/site-packages/evil.pth": EXIT0}, ["python3", "-c", "raise SystemExit(1)"]),
    "entry-point-plugin": ({"test_a.py": FAILING_TEST, "evil.py": EXIT0,
                            "evil-1.0.dist-info/METADATA": "Metadata-Version: 2.1\nName: evil\nVersion: 1.0\n",
                            "evil-1.0.dist-info/entry_points.txt": "[pytest11]\nevil = evil\n"},
                           ["python", "-m", "pytest", "-q", "test_a.py"]),
    "sitecustomize-shadow": ({"sitecustomize.py": EXIT0, "usercustomize.py": EXIT0},
                             ["python3", "-c", "raise SystemExit(1)"]),
}


@pytest.mark.parametrize("case", sorted(FORGERIES))
def test_workspace_cannot_forge_a_pass(tmp_path, case):
    files, argv = FORGERIES[case]
    evidence = _run(tmp_path, files, argv)
    assert evidence.status == "FAIL", (case, evidence.status, evidence.stdout[-300:], evidence.stderr[-300:])


def test_workspace_ruff_config_cannot_exclude_everything(tmp_path):
    if DeterministicEvaluator()._resolve_executable("ruff", tmp_path, DeterministicEvaluator._safe_path(tmp_path)) is None:
        pytest.skip("ruff is not installed")
    evidence = _run(tmp_path, {"bad.py": "import os\n", "ruff.toml": "exclude = ['*.py']\n"},
                    ["ruff", "check", "--select", "F401", "."])
    assert evidence.status == "FAIL"


@pytest.mark.parametrize("argv", [
    ["pytest", "-q", "test_calc.py"],
    ["python", "-m", "pytest", "-q", "test_calc.py"],
    ["python3", "-c", "from calc import add; assert add(2, 3) == 5"],
    ["python3", "test_calc.py"],
    ["python3", "-B", "-W", "ignore", "-c", "import calc, sys; assert sys.argv == ['-c', 'x'], sys.argv", "x"],
])
def test_hardened_checks_still_import_the_code_under_test(tmp_path, argv):
    files = {"calc.py": "def add(a, b):\n    return a + b\n",
             "test_calc.py": "from calc import add\n\ndef test_add():\n    assert add(2, 3) == 5\n\n"
                             "if __name__ == '__main__':\n    test_add()\n"}
    evidence = _run(tmp_path, files, argv)
    assert evidence.status == "PASS", (evidence.stdout[-400:], evidence.stderr[-400:])
    assert evidence.runner == "host"


_SCRIPT = "from calc import add\nassert add(2, 3) == 5\n"
_PYTHON_C = ["python3", "-c", "from calc import add; assert add(2, 3) == 5"]
_SCRIPT_ARGV = ["python3", "checks/run.py"]


def _subject_check(tmp_path: Path, calc: str, form: str):
    """Run ``calc`` under a python -c check or a script check. The script is the oracle."""

    if form == "python -c":
        return _run(tmp_path, {"calc.py": calc}, _PYTHON_C)
    files = {"calc.py": calc, "checks/run.py": _SCRIPT}
    return _run(tmp_path, files, _SCRIPT_ARGV, server={"checks/run.py": _SCRIPT})


def test_imported_agent_code_can_still_end_the_check_with_status_zero(tmp_path):
    """ADR 0025 residual: an import-time sys.exit(0) must not be a pass.

    The subject runs in the worker. Its SystemExit is not the check's exit.
    """

    calc = "import sys\nsys.exit(0)\n"
    for form in ("python -c", "script"):
        evidence = _subject_check(tmp_path, calc, form)
        assert evidence.status == "FAIL", (form, evidence.status, evidence.exit_code, evidence.stderr[-500:])
        assert "subject process exited" in evidence.stderr
        assert evidence.verifier_identity == EVALUATOR_IDENTITY


@pytest.mark.parametrize("form", ["python -c", "script"])
def test_subject_os_exit_zero_cannot_decide_the_check(tmp_path, form):
    evidence = _subject_check(tmp_path, "import os\nos._exit(0)\n", form)
    assert evidence.status == "FAIL", (form, evidence.status, evidence.exit_code, evidence.stderr[-500:])
    assert "subject worker exited" in evidence.stderr


@pytest.mark.parametrize("sig", [signal.SIGKILL, signal.SIGTERM], ids=["SIGKILL", "SIGTERM"])
@pytest.mark.parametrize("form", ["python -c", "script"])
def test_subject_signal_of_the_check_is_not_a_pass(tmp_path, form, sig):
    calc = f"import os, signal\nos.kill(os.getppid(), {int(sig)})\nos._exit(0)\n"
    evidence = _subject_check(tmp_path, calc, form)
    assert evidence.status == "FAIL", (form, sig, evidence.status, evidence.exit_code, evidence.stderr[-500:])
    assert f"signal {int(sig)}" in evidence.stderr


@pytest.mark.parametrize("form", ["python -c", "script"])
def test_subject_fake_success_line_cannot_decide_the_check(tmp_path, form):
    calc = (
        "import os\n"
        "print('FAKE_PASS', flush=True)\n"
        "print('===== 1 passed in 0.01s =====', flush=True)\n"
        "os._exit(0)\n"
    )
    evidence = _subject_check(tmp_path, calc, form)
    assert evidence.status == "FAIL", (form, evidence.status, evidence.stdout, evidence.stderr[-500:])
    assert "FAKE_PASS" not in evidence.stdout
    assert "1 passed" not in evidence.stdout
    assert "subject worker exited" in evidence.stderr


@pytest.mark.parametrize("form", ["python -c", "script"])
def test_subject_exit_before_assertions_is_not_a_pass(tmp_path, form):
    calc = "import os\ndef add(a, b):\n    os._exit(0)\n    return a + b\n"
    evidence = _subject_check(tmp_path, calc, form)
    assert evidence.status == "FAIL", (form, evidence.status, evidence.exit_code, evidence.stderr[-500:])
    assert "subject worker exited" in evidence.stderr


def test_script_oracle_verdict_ignores_exit_code_signal_and_a_missing_frame():
    assert script_oracle_verdict(exit_code=0, frame={"completed": 1, "exitstatus": 0}, oracle_changed=False) == ("PASS", "")
    status, reason = script_oracle_verdict(exit_code=0, frame=None, oracle_changed=False)
    assert status == "FAIL" and "unexpected early exit" in reason
    status, reason = script_oracle_verdict(exit_code=86, frame=None, oracle_changed=False)
    assert status == "FAIL" and "without an evaluator result" in reason
    status, reason = script_oracle_verdict(
        exit_code=-9, frame={"completed": 1, "exitstatus": 0}, oracle_changed=False,
    )
    assert status == "FAIL" and "signal 9" in reason
    status, reason = script_oracle_verdict(exit_code=1, frame={"completed": 1, "exitstatus": 0}, oracle_changed=False)
    assert status == "INCONCLUSIVE" and "disagrees" in reason


def test_honest_script_and_python_c_checks_are_accepted(tmp_path):
    calc = "def add(a, b):\n    return a + b\n"
    subject = "c" * 64
    cases = (
        ("python -c", _PYTHON_C, {"calc.py": calc}),
        ("script", _SCRIPT_ARGV, {"calc.py": calc, "checks/run.py": _SCRIPT}),
    )
    for form, argv, files in cases:
        evidence = _run(tmp_path, files, argv, server=files, subject_artifact_sha256=subject)
        assert evidence.status == "PASS", (form, evidence.stdout[-400:], evidence.stderr[-400:])
        assert evidence.evidence_class == "verification"
        assert evidence.verifier_identity == EVALUATOR_IDENTITY
        assert evidence.verifier_role == "evaluator"
        assert evidence.producer_identity == EVALUATOR_IDENTITY
        assert evidence.producer_role == "evaluator"
        assert evidence.subject_artifact_sha256 == subject
        assert evidence.oracle_digest and len(evidence.oracle_digest) == 64
        assert evidence.environment_digest and len(evidence.environment_digest) == 64
        assert evidence.collected_at is not None and evidence.expires_at > evidence.collected_at
        check_verification_acceptance(evidence, attempt_agent="native", artifact_sha256=subject)


def test_steering_variables_are_not_passed_to_checks(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTEST_ADDOPTS", "-p evil")
    monkeypatch.setenv("PYTEST_PLUGINS", "evil")
    monkeypatch.setenv("PYTHONPATH", str(tmp_path))
    monkeypatch.setenv("PYTHONSTARTUP", str(tmp_path / "evil.py"))
    names = list(CLEARED_ENV)
    probe = f"import os, sys; leaked = [n for n in {names!r} if n in os.environ]; print(leaked); sys.exit(1 if leaked else 0)"
    evidence = _run(tmp_path, {"evil.py": EXIT0}, ["python3", "-c", probe])
    assert evidence.status == "PASS", evidence.stdout
    env = _check_env("/usr/bin")
    assert env["PYTHONSAFEPATH"] == "1" and env["PYTEST_DISABLE_PLUGIN_AUTOLOAD"] == "1"
    assert not set(CLEARED_ENV) & set(env)


@pytest.mark.skipif(not sys.platform.startswith("linux") or os.geteuid() == 0,
                    reason="needs Linux /proc and a non-root uid")
def test_check_cannot_read_the_server_environment_through_proc(tmp_path):
    """e64b: a check read /proc/$PPID/environ, i.e. the server's initial env (its API keys)."""
    ws = tmp_path / "ws"
    ws.mkdir()
    ref = tmp_path / "fixture"
    ref.mkdir()
    (ws / "test_leak.py").write_text(textwrap.dedent("""
        import os
        def test_leak():
            try:
                data = open(f"/proc/{os.getppid()}/environ", "rb").read()
            except PermissionError:
                data = b""
            print("LEAK" if b"SDF_TEST_PROC_CANARY=proc-canary-31" in data else "noleak")
        """), encoding="utf-8")
    (ref / "test_leak.py").write_bytes((ws / "test_leak.py").read_bytes())
    script = textwrap.dedent(f"""
        from pathlib import Path
        from sdf_core.evaluator import DeterministicEvaluator
        e = DeterministicEvaluator()._run_one(command=["pytest", "-q", "-s", "test_leak.py"],
                                              workspace=Path({str(ws)!r}), reference=Path({str(ref)!r}),
                                              attempt_id="A", index=1, criterion="c")
        print("STATUS", e.status)
        print(e.stdout)
        """)
    env = {**os.environ, "SDF_TEST_PROC_CANARY": "proc-canary-31",
           "PYTHONPATH": str(Path(__file__).resolve().parents[1])}
    out = subprocess.run([sys.executable, "-c", script], env=env, capture_output=True, text=True, timeout=60)
    assert "STATUS PASS" in out.stdout, out.stdout + out.stderr
    assert "noleak" in out.stdout and "LEAK" not in out.stdout


def test_world_writable_path_dir_is_not_trusted(tmp_path, monkeypatch):
    shared = tmp_path / "shared-bin"
    shared.mkdir()
    shared.chmod(0o777)
    marker = tmp_path / "pwned.txt"
    fake = shared / "pytest"
    fake.write_text(f"#!/bin/sh\necho pwned > {marker}\nexit 0\n", encoding="utf-8")
    fake.chmod(0o755)
    monkeypatch.setenv("PATH", f"{shared}{os.pathsep}{os.environ['PATH']}")
    _run(tmp_path, {"test_a.py": FAILING_TEST}, ["pytest", "-q", "test_a.py"])
    assert not marker.exists(), "a binary in a world-writable PATH dir ran as the check"


# --- validation (plans and registries) -------------------------------------------------------


@pytest.mark.parametrize("argv", [
    ["/tmp/evil/bin/python3", "-c", "pass"],
    ["python3", "-E", "-c", "pass"],
    ["python3", "-S", "-c", "pass"],
    ["python3", "-i"],
    ["python3"],
])
def test_validate_argv_rejects_paths_and_unhardenable_python(argv):
    with pytest.raises(CheckConfigError):
        validate_argv(argv)


def _plan_with_check(fixture: Path, argv: list[str]) -> dict:
    plan = plan_dict(fixture, f"PLAN-S5-{uuid.uuid4().hex[:8]}")
    plan["subtasks"][0]["criterion_checks"]["add returns the sum"] = [argv]
    return plan


@pytest.mark.parametrize("argv", [["/tmp/evil/bin/python3", "-c", "pass"], ["python3", "-E", "-c", "pass"]])
def test_plan_proposal_validates_check_argv_like_the_registry(tmp_path, argv):
    operator = client_as("operator", principal="planner")
    response = operator.post("/plans", json=_plan_with_check(make_fixture(tmp_path), argv))
    assert response.status_code == 422, response.text


def test_registry_refuses_sources_under_workspace_or_artifact_roots(tmp_path):
    work = tmp_path / "work"
    (work / "ATTEMPT-1").mkdir(parents=True)
    server_file = work / "ATTEMPT-1" / "checks.json"
    server_file.write_text(json.dumps({"checks": {"unit": ["pytest", "test_a.py"]}}), encoding="utf-8")
    with pytest.raises(CheckConfigError, match="workspace or artifact"):
        CheckRegistry.load(None, environ={"SDF_EVALUATOR_CHECKS_FILE": str(server_file)}, forbidden_roots=[work])
    with pytest.raises(CheckConfigError, match="workspace or artifact"):
        CheckRegistry.load(work / "ATTEMPT-1", environ={}, forbidden_roots=[work])


def test_http_fixture_under_workspace_root_is_refused(tmp_path, monkeypatch):
    work = tmp_path / "workspaces"
    fixture = work / "ATTEMPT-X"
    fixture.mkdir(parents=True)
    write_checks(fixture, {"ok": ["python3", "-c", "pass"]})
    monkeypatch.setenv("SDF_WORKSPACE_ROOT", str(work))
    task_id = f"TASK-S6-{uuid.uuid4().hex[:8]}"
    assert client.post("/tasks", json={"id": task_id, "title": "s6", "idempotency_key": task_id,
                                       "acceptance_criteria": ["c"]}).status_code == 201
    response = client.post(f"/tasks/{task_id}/run", json={"fixture_dir": str(fixture),
                                                          "criterion_checks": {"c": ["ok"]}})
    assert response.status_code == 400
    assert "workspace or artifact root" in response.text


@pytest.mark.skipif(not hasattr(os, "mkfifo"), reason="fifo")
def test_registry_refuses_symlinked_server_file_and_non_regular_manifest(tmp_path):
    real = tmp_path / "real.json"
    real.write_text(json.dumps({"checks": {"unit": ["pytest", "test_a.py"]}}), encoding="utf-8")
    link = tmp_path / "link.json"
    link.symlink_to(real)
    with pytest.raises(CheckConfigError):
        CheckRegistry.load(None, environ={"SDF_EVALUATOR_CHECKS_FILE": str(link)})
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    os.mkfifo(fixture / "sdf-checks.json")
    with pytest.raises(CheckConfigError):
        CheckRegistry.load(fixture, environ={})


def test_timeouts_are_keyed_by_check_id_not_argv(tmp_path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    write_checks(fixture, {"quick": {"argv": ["pytest", "-q", "test_a.py"], "timeout_seconds": 2},
                           "slow": {"argv": ["pytest", "-q", "test_a.py"], "timeout_seconds": 300}})
    registry = CheckRegistry.load(fixture, environ={})
    registry.resolve({"a": ["slow"], "b": ["quick", "slow"]})
    assert registry.slot_timeout("a", 0) == 300
    assert registry.slot_timeout("b", 0) == 2 and registry.slot_timeout("b", 1) == 300


# --- containment mode: plan path, defaults, prefer fallback ------------------------------------


def test_containment_defaults_to_require_for_live_agents(monkeypatch):
    monkeypatch.delenv("SDF_EVALUATOR_CONTAINMENT", raising=False)
    monkeypatch.delenv("SDF_CONTAINMENT_BACKEND", raising=False)
    assert api.evaluator_mode(live=True) == "require"
    assert api.evaluator_mode(live=False) == "off"
    assert api.configured_evaluator(live=True).require_containment is True
    db = SessionLocal()
    app.state.agent_sandbox_factory = lambda *a, **k: None
    try:
        assert api._coordinator(db).evaluator.require_containment is True
    finally:
        app.state.agent_sandbox_factory = None
        db.close()


def test_prefer_without_backend_warns_and_records_host(tmp_path, monkeypatch):
    monkeypatch.setenv("SDF_EVALUATOR_CONTAINMENT", "prefer")
    monkeypatch.delenv("SDF_CONTAINMENT_BACKEND", raising=False)
    warnings: list = []
    monkeypatch.setattr(api._log, "warning", lambda msg, *a, **k: warnings.append((msg, k.get("extra"))))
    evaluator = api.configured_evaluator(live=True)
    assert evaluator.runner == "host" and evaluator.containment_backend is None
    assert warnings and warnings[0][0] == "evaluator_runs_on_host" and warnings[0][1]["mode"] == "prefer"


def test_http_run_records_where_each_check_ran(tmp_path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    write_checks(fixture, {"ok": ["python3", "-c", "pass"]})
    task_id = f"TASK-S4-{uuid.uuid4().hex[:8]}"
    assert client.post("/tasks", json={"id": task_id, "title": "s4", "idempotency_key": task_id,
                                       "acceptance_criteria": ["c"]}).status_code == 201
    response = client.post(f"/tasks/{task_id}/run", json={"fixture_dir": str(fixture),
                                                          "criterion_checks": {"c": ["ok"]}})
    assert response.status_code == 200, response.text
    db = SessionLocal()
    try:
        attempt = db.query(AttemptRow).filter_by(task_id=task_id).one()
        evidence = db.query(EvidenceRow).filter_by(attempt_id=attempt.id).one()
        artifact = db.get(ArtifactRow, f"{evidence.id}-OUTPUT")
        assert json.loads(Path(artifact.uri.removeprefix("file://")).read_text())["runner"] == "host"
    finally:
        db.close()


FAST = {"poll_interval_s": 0.01, "stall_s": 0.5, "max_wall_s": 20, "max_verify_rounds": 1}


def test_plan_run_honours_required_containment(tmp_path, monkeypatch):
    """B1: plan checks ran on the host even with SDF_EVALUATOR_CONTAINMENT=require."""
    monkeypatch.chdir(tmp_path)
    monkeypatch.setenv("SDF_EVALUATOR_CONTAINMENT", "require")
    monkeypatch.delenv("SDF_CONTAINMENT_BACKEND", raising=False)
    running: set[str] = set()
    created: list = []
    app.state.agent_sandbox_factory = fake_factory(tmp_path / "boxes", running, {
        "lead": lead_behavior, "worker": worker_behavior(), "worker-2": worker_behavior()}, created)
    app.state.agent_cleanup_check = lambda ids: ids & running
    operator = client_as("operator", principal="planner")
    approver = client_as("approver", principal="hung")
    try:
        plan_id = f"PLAN-B1-{uuid.uuid4().hex[:8]}"
        marker = tmp_path / "check-ran-on-host.txt"
        plan = _plan_with_check(make_fixture(tmp_path),
                                ["python3", "-c", f"open({str(marker)!r}, 'w').write('ran')"])
        plan["id"] = plan_id
        proposed = operator.post("/plans", json=plan)
        assert proposed.status_code == 201, proposed.text
        assert approver.post(f"/plans/{plan_id}/approve", json={
            "expected_sha256": proposed.json()["content_sha256"]}).status_code == 200
        started = operator.post(f"/plans/{plan_id}/runs", json={"config": FAST, "wait": True})
        assert started.status_code == 202, started.text
        run = started.json()
        assert run["status"] != "succeeded"
        assert not marker.exists(), "a plan check ran on the host under SDF_EVALUATOR_CONTAINMENT=require"
        db = SessionLocal()
        try:
            attempts = db.query(AttemptRow).filter_by(task_id=task_id_for(plan_id, "add")).all()
            statuses = {row.status for a in attempts for row in db.query(EvidenceRow).filter_by(attempt_id=a.id)}
            assert statuses == {"INCONCLUSIVE"}, statuses
        finally:
            db.close()
    finally:
        app.state.agent_sandbox_factory = None
        app.state.agent_cleanup_check = None
