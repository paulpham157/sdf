"""ADR 0039: a workspace file cannot forge verification, and a PASS is bound.

Adversarial cases are checks the agent can plant in its workspace. None of
them may come back PASS. The acceptance gate is tested in both directions:
a complete verification row may accept its artifact, and each broken
independence field is refused with a reason that names the rule.
"""

from __future__ import annotations

import os
import sys
import uuid
from datetime import timedelta

import pytest
from alembic import command
from alembic.config import Config
from sqlalchemy import create_engine, inspect, select, text
from sqlalchemy.orm import sessionmaker

from sdf_core.acceptance import (
    EVALUATOR_IDENTITY,
    EVALUATOR_ROLE,
    AcceptanceError,
    check_verification_acceptance,
)
from sdf_core.api import SessionLocal
from sdf_core.db import ArtifactRow, AttemptRow, EvidenceRow
from sdf_core.evaluator import DeterministicEvaluator, Evidence
from sdf_core.model import utcnow
from tests.auth_support import admin_client
from tests.check_support import write_checks
from tests.revision_support import bound
from tests.test_check_hardening import _run
from tests.test_check_targets import CALC, PASSING_TEST

client = admin_client()
FAILING_TEST = "def test_x():\n    assert False\n"
FORGE = "import os\nprint('===== 1 passed in 0.01s =====')\nos._exit(0)\n"
HONEST_HELPER = "def value():\n    return 1\n"
HELPER_TEST = "import helper\n\ndef test_value():\n    assert helper.value() == 1\n"


@pytest.fixture(autouse=True)
def _venv_on_path(monkeypatch):
    monkeypatch.setenv("PATH", f"{os.path.dirname(sys.executable)}{os.pathsep}{os.environ.get('PATH', '')}")


def _not_pass(evidence: Evidence, needle: str) -> None:
    assert evidence.status != "PASS"
    assert evidence.evidence_class == "verification"
    assert evidence.verifier_identity == EVALUATOR_IDENTITY
    assert evidence.verifier_role == EVALUATOR_ROLE
    assert needle in evidence.stderr


# --- adversarial oracle -------------------------------------------------------


def test_planted_conftest_cannot_forge_a_pass(tmp_path):
    evidence = _run(tmp_path, {
        "test_a.py": FAILING_TEST,
        "conftest.py": "def pytest_sessionfinish(session, exitstatus):\n    session.exitstatus = 0\n"
                       "    print('===== 1 passed in 0.01s =====')\n",
    }, ["pytest", "-q", "test_a.py"])
    _not_pass(evidence, "failing")
    assert evidence.status == "FAIL"


def test_planted_helper_that_forges_a_summary_is_not_a_pass(tmp_path):
    """The fixture helper is honest. The workspace copy exits 0 with a fake summary."""

    evidence = _run(
        tmp_path,
        {"test_a.py": HELPER_TEST, "helper.py": FORGE},
        ["pytest", "-q", "test_a.py"],
        server={"test_a.py": HELPER_TEST, "helper.py": HONEST_HELPER},
    )
    _not_pass(evidence, "differs from the server fixture")
    assert evidence.status == "INCONCLUSIVE"
    assert evidence.exit_code is None


def test_helper_that_exists_only_in_the_workspace_is_not_a_pass(tmp_path):
    evidence = _run(
        tmp_path,
        {"test_a.py": HELPER_TEST, "helper.py": FORGE},
        ["pytest", "-q", "test_a.py"],
        server={"test_a.py": HELPER_TEST},
    )
    _not_pass(evidence, "not in the server fixture")
    assert evidence.status == "INCONCLUSIVE"


_PASSING_FRAME = (
    '{"collected":1,"testscollected":1,"passed":1,"failed":0,"skipped":0,"errors":0,"exitstatus":0}'
)
_WOULD_FAIL = "from calc import add\n\ndef test_add():\n    assert add(2, 3) == 999\n"


def test_subject_closure_cannot_call_the_report_stub(tmp_path):
    """The closure walk that minted a PASS at d216e1f must not be a pass."""

    forge = (
        "import json, os, sys, ctypes\n"
        f"frame = {_PASSING_FRAME!r}.encode()\n"
        "fn = sys.modules['sdf_oracle_plugin'].pytest_sessionfinish\n"
        "m = dict(zip(fn.__code__.co_freevars, (c.cell_contents for c in fn.__closure__)))\n"
        "m['write'](m['fd'], ctypes.create_string_buffer(frame), len(frame))\n"
        "os._exit(0)\n"
    )
    evidence = _run(tmp_path, {"calc.py": forge, "test_calc.py": _WOULD_FAIL}, ["pytest", "-q", "test_calc.py"])
    assert evidence.status == "FAIL", (evidence.status, evidence.stderr[-400:])
    assert evidence.verifier_identity == EVALUATOR_IDENTITY


def test_subject_gc_walk_cannot_call_the_report_stub(tmp_path):
    forge = (
        "import gc, json, os, ctypes\n"
        f"frame = {_PASSING_FRAME!r}.encode()\n"
        "for obj in gc.get_objects():\n"
        "    code, closure = getattr(obj, '__code__', None), getattr(obj, '__closure__', None)\n"
        "    free = getattr(code, 'co_freevars', ())\n"
        "    if closure and 'write' in free and 'fd' in free:\n"
        "        cells = dict(zip(free, (c.cell_contents for c in closure)))\n"
        "        cells['write'](cells['fd'], ctypes.create_string_buffer(frame), len(frame))\n"
        "        os._exit(0)\n"
        "os._exit(0)\n"
    )
    evidence = _run(tmp_path, {"calc.py": forge, "test_calc.py": _WOULD_FAIL}, ["pytest", "-q", "test_calc.py"])
    assert evidence.status == "FAIL", (evidence.status, evidence.stderr[-400:])


def test_subject_pluginmanager_hook_cannot_call_the_report_stub(tmp_path):
    forge = (
        "import json, os, sys, ctypes\n"
        f"frame = {_PASSING_FRAME!r}.encode()\n"
        "def _emit():\n"
        "    fn = sys.modules['sdf_oracle_plugin'].pytest_sessionfinish\n"
        "    m = dict(zip(fn.__code__.co_freevars, (c.cell_contents for c in fn.__closure__)))\n"
        "    m['write'](m['fd'], ctypes.create_string_buffer(frame), len(frame))\n"
        "def pytest_sessionfinish(session, exitstatus):\n"
        "    _emit()\n"
        "    os._exit(0)\n"
        "registered = False\n"
        "for mod in list(sys.modules.values()):\n"
        "    pm = getattr(mod, 'pluginmanager', None)\n"
        "    if pm is not None and hasattr(pm, 'register'):\n"
        "        try:\n"
        "            pm.register(sys.modules[__name__], name='sdf-subject-forge')\n"
        "        except Exception:\n"
        "            continue\n"
        "        registered = True\n"
        "        break\n"
        "if not registered:\n"
        "    try:\n"
        "        _emit()\n"
        "    except Exception:\n"
        "        pass\n"
        "    os._exit(0)\n"
    )
    evidence = _run(tmp_path, {"calc.py": forge, "test_calc.py": _WOULD_FAIL}, ["pytest", "-q", "test_calc.py"])
    assert evidence.status == "FAIL", (evidence.status, evidence.stderr[-400:])


def test_worker_cannot_ptrace_the_pytest_process(tmp_path):
    """Same-uid ptrace from the subject worker to its pytest parent is denied."""

    forge = (
        "import ctypes, os\n"
        "libc = ctypes.CDLL(None, use_errno=True)\n"
        "ppid = os.getppid()\n"
        "rc = libc.ptrace(16, ppid, None, None)\n"  # PTRACE_ATTACH
        "err = ctypes.get_errno()\n"
        "if rc == 0:\n"
        "    os._exit(0)\n"
        "raise PermissionError(f'ptrace denied {err}')\n"
    )
    evidence = _run(tmp_path, {"calc.py": forge, "test_calc.py": _WOULD_FAIL}, ["pytest", "-q", "test_calc.py"])
    assert evidence.status != "PASS"
    assert "ptrace denied" in (evidence.stdout + evidence.stderr)


def test_worker_cannot_use_io_uring(tmp_path):
    """io_uring_setup in the subject worker is denied, and a failing oracle stays a non-pass."""

    forge = (
        "import ctypes, os\n"
        "libc = ctypes.CDLL(None, use_errno=True)\n"
        "libc.syscall.restype = ctypes.c_long\n"
        "params = ctypes.create_string_buffer(256)\n"
        "rc = libc.syscall(425, 8, ctypes.addressof(params))\n"  # io_uring_setup
        "err = ctypes.get_errno()\n"
        "if rc >= 0:\n"
        "    os.close(rc)\n"
        "    os._exit(0)\n"
        "raise PermissionError(f'io_uring denied {err}')\n"
    )
    evidence = _run(tmp_path, {"calc.py": forge, "test_calc.py": _WOULD_FAIL}, ["pytest", "-q", "test_calc.py"])
    assert evidence.status != "PASS"
    assert "io_uring denied" in (evidence.stdout + evidence.stderr)


def test_worker_cannot_open_pytest_process_fds(tmp_path):
    forge = (
        "import os\n"
        "ppid = os.getppid()\n"
        "try:\n"
        "    os.listdir(f'/proc/{ppid}/fd')\n"
        "except OSError as exc:\n"
        "    raise PermissionError(f'proc fd denied {exc.errno}') from exc\n"
        "os._exit(0)\n"
    )
    evidence = _run(tmp_path, {"calc.py": forge, "test_calc.py": _WOULD_FAIL}, ["pytest", "-q", "test_calc.py"])
    assert evidence.status != "PASS"
    assert "proc fd denied" in (evidence.stdout + evidence.stderr)


def test_oversized_subject_response_is_not_a_pass(tmp_path):
    """A length prefix above the 1 MiB cap is a non-pass, not a huge read."""

    forge = (
        "import os, struct\n"
        "hdr = struct.pack('>I', 2000000)\n"
        "sent = False\n"
        "for name in os.listdir('/proc/self/fd'):\n"
        "    fd = int(name)\n"
        "    if fd < 3:\n"
        "        continue\n"
        "    try:\n"
        "        target = os.readlink(f'/proc/self/fd/{fd}')\n"
        "    except OSError:\n"
        "        continue\n"
        "    if target.startswith('socket:'):\n"
        "        os.write(fd, hdr)\n"
        "        sent = True\n"
        "if not sent:\n"
        "    raise RuntimeError('no rpc socket')\n"
        "def add(a, b):\n"
        "    return 999\n"
    )
    evidence = _run(tmp_path, {"calc.py": forge, "test_calc.py": _WOULD_FAIL}, ["pytest", "-q", "test_calc.py"])
    text = evidence.stdout + evidence.stderr
    assert evidence.status != "PASS", (evidence.status, text[-500:])
    assert "exceeds 1000000" in text


def test_malformed_subject_response_is_not_a_pass(tmp_path):
    forge = (
        "import os, struct\n"
        "payload = struct.pack('>I', 4) + b'nope'\n"
        "sent = False\n"
        "for name in os.listdir('/proc/self/fd'):\n"
        "    fd = int(name)\n"
        "    if fd < 3:\n"
        "        continue\n"
        "    try:\n"
        "        target = os.readlink(f'/proc/self/fd/{fd}')\n"
        "    except OSError:\n"
        "        continue\n"
        "    if target.startswith('socket:'):\n"
        "        os.write(fd, payload)\n"
        "        sent = True\n"
        "if not sent:\n"
        "    raise RuntimeError('no rpc socket')\n"
        "def add(a, b):\n"
        "    return 999\n"
    )
    evidence = _run(tmp_path, {"calc.py": forge, "test_calc.py": _WOULD_FAIL}, ["pytest", "-q", "test_calc.py"])
    text = evidence.stdout + evidence.stderr
    assert evidence.status != "PASS", (evidence.status, text[-500:])
    assert "malformed subject response" in text


def test_thaw_uses_owner_only_modes_and_freeze_stays_read_only(tmp_path):
    """Cleanup must not restore world-readable modes. The worker freeze stays 0444/0555."""

    from sdf_core.oracle import PYTEST_SEAL, _thaw_tree

    assert "os.chmod(path, 0o444)" in PYTEST_SEAL
    assert "os.chmod(dirpath, 0o555)" in PYTEST_SEAL

    root = tmp_path / "sealed"
    nested = root / "sub"
    nested.mkdir(parents=True)
    oracle = nested / "test_calc.py"
    subject = root / "calc.py"
    oracle.write_text("def test_ok():\n    pass\n", encoding="utf-8")
    subject.write_text("def add(a, b):\n    return a + b\n", encoding="utf-8")
    # Modes materialize would have set before the worker-time freeze.
    oracle.chmod(0o444)
    subject.chmod(0o644)
    nested.chmod(0o755)
    root.chmod(0o755)
    for path in (oracle, subject):
        path.chmod(0o444)
    for path in (nested, root):
        path.chmod(0o555)

    _thaw_tree(root)

    for path in (oracle, subject, nested, root):
        mode = path.stat().st_mode & 0o777
        assert mode & 0o077 == 0, oct(mode)
        assert mode == (0o700 if path.is_dir() else 0o600)


def test_worker_cannot_write_the_sealed_tree_but_can_write_temp(tmp_path):
    forge = (
        "import os, tempfile\n"
        "try:\n"
        "    open('planted-by-subject.txt', 'w').write('nope')\n"
        "except OSError:\n"
        "    denied = True\n"
        "else:\n"
        "    denied = False\n"
        "try:\n"
        "    path = os.path.join(tempfile.gettempdir(), 'sdf-subject-temp-ok')\n"
        "    open(path, 'w').write('ok')\n"
        "except OSError as exc:\n"
        "    raise RuntimeError('temp write failed: ' + str(exc))\n"
        "if not denied:\n"
        "    raise RuntimeError('sealed tree was writable')\n"
        "def add(a, b):\n"
        "    return a + b\n"
    )
    evidence = _run(tmp_path, {"calc.py": forge, "test_calc.py": _WOULD_FAIL}, ["pytest", "-q", "test_calc.py"])
    text = evidence.stdout + evidence.stderr
    assert evidence.status == "FAIL", (evidence.status, text[-500:])
    assert "sealed tree was writable" not in text
    assert "temp write failed" not in text


def test_subject_cannot_forge_the_evaluator_signal(tmp_path):
    """Import-time forge of the old marker and JUnit files must not be a PASS.

    On the first cut of ADR 0039 this was a PASS: calc.py read
    SDF_ORACLE_TOKEN and SDF_ORACLE_MARKER, wrote a matching pair, and
    os._exit(0), while the fixture test asserts ``add(2, 3) == 999``. The
    same shape is a FAIL under ADR 0025's stdout check. The seal has to
    keep it from becoming a verification PASS the acceptance gate would allow.
    """

    forge = (
        "import json, os\n"
        "from pathlib import Path\n"
        "token = os.environ['SDF_ORACLE_TOKEN']\n"
        "marker = Path(os.environ['SDF_ORACLE_MARKER'])\n"
        "junit = marker.with_name('junit.xml')\n"
        "marker.write_text(json.dumps({\n"
        "    'token': token, 'collected': 1, 'passed': 1, 'failed': 0,\n"
        "    'skipped': 0, 'errors': 0, 'exitstatus': 0,\n"
        "}), encoding='utf-8')\n"
        "junit.write_text(\n"
        "    '<?xml version=\"1.0\"?><testsuites><testsuite tests=\"1\" failures=\"0\" '\n"
        "    'errors=\"0\" skipped=\"0\"><testcase classname=\"test_calc\" name=\"test_add\" '\n"
        "    'time=\"0.01\"/></testsuite></testsuites>',\n"
        "    encoding='utf-8')\n"
        "os._exit(0)\n"
    )
    failing = "from calc import add\n\ndef test_add():\n    assert add(2, 3) == 999\n"
    evidence = _run(tmp_path, {"calc.py": forge, "test_calc.py": failing}, ["pytest", "-q", "test_calc.py"])
    assert evidence.status != "PASS"
    assert evidence.status in {"FAIL", "INCONCLUSIVE"}
    assert evidence.verifier_identity == EVALUATOR_IDENTITY
    # The acceptance gate allows a verification PASS whose verifier is the
    # evaluator and whose subject hash matches. This run must not produce one.


def test_subject_os_write_to_open_fds_cannot_forge_a_pass(tmp_path):
    """libc write of a passing frame, including every inherited fd, is not a PASS."""

    forge = (
        "import json, os\n"
        "payload = json.dumps({\n"
        "    'collected': 1, 'testscollected': 1, 'passed': 1, 'failed': 0,\n"
        "    'skipped': 0, 'errors': 0, 'exitstatus': 0,\n"
        "}).encode()\n"
        "for fd in range(0, 64):\n"
        "    try:\n"
        "        os.write(fd, payload)\n"
        "    except OSError:\n"
        "        pass\n"
        "os._exit(0)\n"
    )
    evidence = _run(tmp_path, {"calc.py": forge, "test_calc.py": _WOULD_FAIL}, ["pytest", "-q", "test_calc.py"])
    assert evidence.status == "FAIL", (evidence.status, evidence.exit_code, evidence.stderr[-400:])


def test_early_exit_with_a_forged_summary_is_not_a_pass(tmp_path):
    """The code under test prints a passing summary and os._exit(0) during import."""

    evidence = _run(
        tmp_path,
        {"calc.py": FORGE, "test_calc.py": PASSING_TEST},
        ["pytest", "-q", "test_calc.py"],
    )
    assert evidence.status == "FAIL", (evidence.status, evidence.exit_code, evidence.stderr[-400:])
    assert evidence.verifier_identity == EVALUATOR_IDENTITY


def test_modified_test_file_is_not_a_pass(tmp_path):
    forged = "def test_x():\n    print('===== 1 passed in 0.01s =====')\n    import os; os._exit(0)\n"
    evidence = _run(tmp_path, {"test_a.py": forged}, ["pytest", "-q", "test_a.py"], server={"test_a.py": FAILING_TEST})
    _not_pass(evidence, "differs from the server fixture")
    assert evidence.status == "INCONCLUSIVE"


def test_zero_collected_tests_are_not_a_pass(tmp_path):
    empty = "def not_a_test():\n    pass\n"
    evidence = _run(tmp_path, {"test_a.py": empty}, ["pytest", "-q", "test_a.py"])
    _not_pass(evidence, "zero")
    assert evidence.status == "FAIL"


def test_skipped_tests_are_not_a_pass(tmp_path):
    skipped = "import pytest\n@pytest.mark.skip(reason='nope')\ndef test_a():\n    assert False\n"
    evidence = _run(tmp_path, {"test_a.py": skipped}, ["pytest", "-q", "test_a.py"])
    _not_pass(evidence, "skipped")
    assert evidence.status == "FAIL"


def test_honest_helper_from_the_fixture_still_passes(tmp_path):
    evidence = _run(
        tmp_path,
        {"test_a.py": HELPER_TEST, "helper.py": HONEST_HELPER},
        ["pytest", "-q", "test_a.py"],
        server={"test_a.py": HELPER_TEST, "helper.py": HONEST_HELPER},
    )
    assert evidence.status == "PASS", (evidence.stdout[-400:], evidence.stderr[-400:])
    assert evidence.evidence_class == "verification"
    assert evidence.oracle_digest and len(evidence.oracle_digest) == 64
    assert evidence.environment_digest and len(evidence.environment_digest) == 64
    assert evidence.collected_at is not None and evidence.expires_at > evidence.collected_at
    assert evidence.producer_identity == EVALUATOR_IDENTITY


def test_a_coverage_gap_is_not_verification_evidence(tmp_path):
    result = DeterministicEvaluator().evaluate(
        attempt_id="ATTEMPT-GAP", workspace=tmp_path, criteria=["needs a check"], criterion_checks={},
    )
    gap = result.evidence[0]
    assert gap.status == "INCONCLUSIVE"
    assert gap.evidence_class is None
    assert gap.verifier_identity is None


# --- acceptance gate ----------------------------------------------------------


def _row(**overrides) -> Evidence:
    now = utcnow()
    fields = dict(
        evidence_id="E-1", attempt_id="A-1", kind="evaluation", status="PASS",
        command="pytest -q test_a.py", exit_code=0, stdout="", stderr="", confidence=1.0,
        evidence_class="verification",
        producer_identity=EVALUATOR_IDENTITY, producer_role=EVALUATOR_ROLE,
        verifier_identity=EVALUATOR_IDENTITY, verifier_role=EVALUATOR_ROLE,
        subject_artifact_sha256="a" * 64,
        oracle_digest="b" * 64, environment_digest="c" * 64,
        collected_at=now, expires_at=now + timedelta(hours=1),
    )
    fields.update(overrides)
    return Evidence(**fields)


def test_verification_with_independent_verifier_and_matching_artifact_is_accepted():
    check_verification_acceptance(_row(), attempt_agent="native", artifact_sha256="a" * 64)


@pytest.mark.parametrize("overrides, agent, needle", [
    ({"verifier_identity": "native"}, "native", "attempt's agent"),
    ({"verifier_role": "agent", "verifier_identity": "someone-else"}, "native", "attempt's agent"),
    ({"verifier_identity": None}, "native", "attempt's agent"),
    ({"subject_artifact_sha256": "d" * 64}, "native", "does not match"),
    ({"subject_artifact_sha256": None}, "native", "does not match"),
    ({"expires_at": utcnow() - timedelta(seconds=1)}, "native", "requires re-validation"),
    ({"expires_at": None}, "native", "requires re-validation"),
    ({"evidence_class": None}, "native", "not verification"),
    ({"evidence_class": "execution"}, "native", "not verification"),
])
def test_acceptance_gate_rejects_independence_failures(overrides, agent, needle):
    with pytest.raises(AcceptanceError, match=needle):
        check_verification_acceptance(_row(**overrides), attempt_agent=agent, artifact_sha256="a" * 64)


def test_run_records_verification_bound_to_the_diff_artifact(tmp_path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "calc.py").write_text(CALC, encoding="utf-8")
    (fixture / "test_calc.py").write_text(PASSING_TEST, encoding="utf-8")
    write_checks(fixture, {"unit": ["pytest", "-q", "test_calc.py"]})
    task_id = f"TASK-IND-{uuid.uuid4().hex[:8]}"
    assert client.post("/tasks", json={"id": task_id, "title": "t", "idempotency_key": task_id,
                                       "acceptance_criteria": ["adds"]}).status_code == 201
    response = client.post(f"/tasks/{task_id}/run", json={"fixture_dir": str(fixture),
                                                          "criterion_checks": {"adds": ["unit"]}})
    assert response.status_code == 200, response.text
    body = response.json()
    assert body["task_status"] == "succeeded", body
    with SessionLocal() as db:
        attempt = db.get(AttemptRow, body["attempt_id"])
        rows = list(db.scalars(select(EvidenceRow).where(EvidenceRow.attempt_id == attempt.id)))
        assert len(rows) == 1
        row = rows[0]
        diff = db.scalars(select(ArtifactRow).where(
            ArtifactRow.attempt_id == attempt.id, ArtifactRow.kind == "diff")).one()
        assert row.evidence_class == "verification"
        assert row.verifier_identity == EVALUATOR_IDENTITY
        assert row.verifier_role == EVALUATOR_ROLE
        assert row.producer_role == EVALUATOR_ROLE
        assert row.subject_artifact_sha256 == diff.sha256
        assert row.oracle_digest and row.environment_digest
        assert row.collected_at is not None and row.expires_at is not None
        check_verification_acceptance(row, attempt_agent=attempt.agent, artifact_sha256=diff.sha256)
        with pytest.raises(AcceptanceError, match="does not match"):
            check_verification_acceptance(row, attempt_agent=attempt.agent, artifact_sha256="e" * 64)
        viewed = client.get(f"/evidence/{row.id}").json()
        assert viewed["evidence_class"] == "verification"
        assert viewed["subject_artifact_sha256"] == diff.sha256
        assert viewed["verifier_identity"] == EVALUATOR_IDENTITY


# --- migration ----------------------------------------------------------------


def test_migration_0025_adds_nullable_independence_columns(tmp_path, monkeypatch):
    database_url = f"sqlite:///{tmp_path / 'm0025.sqlite'}"
    monkeypatch.setenv("SDF_DATABASE_URL", database_url)
    config = Config("alembic.ini")
    engine = create_engine(database_url)
    command.upgrade(config, "0024_decision_material_fence")
    with engine.begin() as connection:
        connection.execute(text(
            "INSERT INTO attempts (id, task_id, status, dispatch_key, agent, model_tier, cost_usd, created_at, "
            "evidence_mode) VALUES ('ATTEMPT-LEGACY', 'T', 'completed', 'legacy', 'old', 'basic', 0, '2026-10-01', "
            "'synthetic')"))
        connection.execute(text(
            "INSERT INTO evidence (id, attempt_id, kind, status, command, confidence, evidence_mode, created_at) "
            "VALUES ('EV-LEGACY', 'ATTEMPT-LEGACY', 'evaluation', 'PASS', 'c', 1.0, 'synthetic', '2026-10-01')"))
    command.upgrade(config, "head")
    columns = {column["name"] for column in inspect(engine).get_columns("evidence")}
    assert {
        "evidence_class", "producer_identity", "producer_role", "verifier_identity", "verifier_role",
        "subject_artifact_sha256", "oracle_digest", "environment_digest", "collected_at", "expires_at",
    } <= columns
    with engine.connect() as connection:
        legacy = connection.execute(text(
            "SELECT evidence_class, verifier_identity, subject_artifact_sha256, expires_at FROM evidence"
        )).one()
        assert legacy == (None, None, None, None)
    command.downgrade(config, "0024_decision_material_fence")
    assert "evidence_class" not in {column["name"] for column in inspect(engine).get_columns("evidence")}
    command.upgrade(config, "head")
    assert "oracle_digest" in {column["name"] for column in inspect(engine).get_columns("evidence")}
    engine.dispose()


def test_orm_rejects_an_unknown_evidence_class():
    from sdf_core.db import Base, make_engine

    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        db.add(AttemptRow(id="ATTEMPT-CLASS", task_id="T", status="completed", dispatch_key="class-1",
                          agent="native", created_at=utcnow()))
        db.add(EvidenceRow(
            id="EV-BAD-CLASS", attempt_id="ATTEMPT-CLASS", kind="evaluation", status="PASS",
            command="c", confidence=1.0, created_at=utcnow(), evidence_class="forged",
            **bound(db, "ATTEMPT-CLASS"),
        ))
        with pytest.raises(ValueError, match="evidence_class"):
            db.commit()
