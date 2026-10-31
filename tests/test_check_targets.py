"""PR #64 re-review: a pytest check runs only server test files, and its result is cross-checked (ADR 0025).

Before this, ``pytest -q`` collected whatever test files the agent added; a
module-level ``pytest_plugins`` or ``os._exit(0)`` in such a file ended the
check with status 0 while the output said "1 failed".
"""

from __future__ import annotations

import logging
import os
import sys
import uuid

import pytest

from sdf_core import evaluator as evaluator_module
from sdf_core.checks import CheckConfigError, validate_argv
from sdf_core.evaluator import DeterministicEvaluator, pytest_summary
from tests.auth_support import admin_client
from tests.check_support import write_checks
from tests.test_check_hardening import FAILING_TEST, _run, _write

client = admin_client()
PASSING_TEST = "from calc import add\n\ndef test_add():\n    assert add(2, 3) == 5\n"
CALC = "def add(a, b):\n    return a + b\n"
# A plugin that rewrites the session result: exit 0 although a test failed.
EVIL_PLUGIN = "def pytest_sessionfinish(session, exitstatus):\n    session.exitstatus = 0\n"


@pytest.fixture(autouse=True)
def _venv_on_path(monkeypatch):
    monkeypatch.setenv("PATH", f"{os.path.dirname(sys.executable)}{os.pathsep}{os.environ.get('PATH', '')}")


# --- 1. explicit, server-owned test files -------------------------------------

@pytest.mark.parametrize("argv", [
    ["pytest", "-q"], ["python", "-m", "pytest", "-q"], ["pytest", "-q", "tests"], ["pytest", "-q", "tests/"],
    ["pytest", "-q", "test_*.py"], ["pytest", "-q", "../test_a.py"], ["pytest", "-qq", "test_a.py"],
    ["pytest", "-q", "-p", "evil", "test_a.py"], ["pytest", "--pyargs", "test_a.py"],
    ["pytest", "-c", "x.ini", "test_a.py"], ["pytest", "--rootdir", ".", "test_a.py"],
    ["pytest", "-o", "addopts=-p evil", "test_a.py"], ["pytest", "--no-summary", "test_a.py"],
    ["python3", "../checks/run.py"],
])
def test_pytest_checks_must_name_explicit_test_files(argv):
    with pytest.raises(CheckConfigError):
        validate_argv(argv)


@pytest.mark.parametrize("argv", [
    ["pytest", "-q", "tests/test_calc.py"], ["pytest", "-q", "tests/test_calc.py::test_add", "-k", "add"],
    ["python", "-m", "pytest", "-x", "--tb=short", "-rA", "test_calc.py"], ["pytest", "/srv/checks/test_calc.py"],
])
def test_explicit_pytest_checks_are_accepted(argv):
    assert validate_argv(argv) == tuple(argv)


@pytest.mark.parametrize("argv", [["pytest", "-q", "test_a.py"], ["python", "-m", "pytest", "-q", "test_a.py"]])
@pytest.mark.parametrize("added", {
    "pytest-plugins": {"test_zz_added.py": "pytest_plugins = ['evil_plugin']\n", "evil_plugin.py": EVIL_PLUGIN},
    "os-exit": {"test_zz_added.py": "import os\n\ndef test_bye():\n    os._exit(0)\n"},
}.items(), ids=lambda item: item[0] if isinstance(item, tuple) else None)
def test_agent_added_test_files_are_not_run(tmp_path, argv, added):
    """Reviewer cases 11/12: an added test file used to turn a failing check into PASS."""
    name, files = added
    evidence = _run(tmp_path, {"test_a.py": FAILING_TEST, **files}, argv)
    assert evidence.status == "FAIL", (name, evidence.stdout[-300:], evidence.stderr[-300:])


def test_bare_pytest_without_targets_never_runs(tmp_path):
    evidence = _run(tmp_path, {"test_a.py": FAILING_TEST, "test_zz.py": "pytest_plugins = ['p']\n",
                               "p.py": EVIL_PLUGIN}, ["pytest", "-q"])
    assert evidence.status == "INCONCLUSIVE" and "must name its test files" in evidence.stderr


def test_agent_cannot_rewrite_the_registered_test_file(tmp_path):
    forged = "pytest_plugins = ['evil_plugin']\n" + FAILING_TEST
    evidence = _run(tmp_path, {"test_a.py": forged, "evil_plugin.py": EVIL_PLUGIN}, ["pytest", "-q", "test_a.py"],
                    server={"test_a.py": FAILING_TEST})
    assert evidence.status == "INCONCLUSIVE" and "differs from the server fixture" in evidence.stderr
    assert evidence.exit_code is None  # it never ran


@pytest.mark.skipif(sys.platform == "win32", reason="symlinks")
def test_registered_test_file_may_not_be_a_symlink(tmp_path):
    outside = tmp_path / "elsewhere.py"
    outside.write_text(FAILING_TEST.replace("False", "True"), encoding="utf-8")
    ws_files = {"calc.py": CALC}
    reference = _write(tmp_path / "fixture", {"test_a.py": FAILING_TEST})
    ws = _write(tmp_path / "ws", ws_files)
    (ws / "test_a.py").symlink_to(outside)
    evidence = DeterministicEvaluator()._run_one(command=["pytest", "-q", "test_a.py"], workspace=ws,
                                                 attempt_id="A", index=1, criterion="c", reference=reference)
    assert evidence.status == "INCONCLUSIVE"


def test_agent_added_file_named_by_the_check_is_not_trusted(tmp_path):
    """A test file the fixture does not have is agent-written."""
    evidence = _run(tmp_path, {"test_new.py": "def test_ok():\n    pass\n"}, ["pytest", "-q", "test_new.py"], server={})
    assert evidence.status == "INCONCLUSIVE" and "not a regular file in the server fixture" in evidence.stderr


def test_relative_check_files_need_a_server_fixture(tmp_path):
    ws = _write(tmp_path / "ws", {"test_a.py": "def test_ok():\n    pass\n"})
    evidence = DeterministicEvaluator()._run_one(command=["pytest", "-q", "test_a.py"], workspace=ws,
                                                 attempt_id="A", index=1, criterion="c")
    assert evidence.status == "INCONCLUSIVE" and "no server fixture" in evidence.stderr


def test_absolute_server_test_files_run_and_must_be_outside_the_workspace(tmp_path):
    server = _write(tmp_path / "server-tests", {"test_srv.py": PASSING_TEST})
    ws = _write(tmp_path / "ws", {"calc.py": CALC, "test_ws.py": PASSING_TEST})
    run = lambda target: DeterministicEvaluator()._run_one(command=["pytest", "-q", target], workspace=ws,  # noqa: E731
                                                           attempt_id="A", index=1, criterion="c")
    assert run(str(server / "test_srv.py")).status == "PASS"
    inside = run(str(ws / "test_ws.py"))
    assert inside.status == "INCONCLUSIVE" and "inside the workspace" in inside.stderr


# --- defense in depth: the summary must agree with the exit status -------------

@pytest.mark.parametrize("calc", [
    "import atexit, os\natexit.register(lambda: os._exit(0))\n" + CALC.replace("a + b", "a - b"),
    "import os\nos._exit(0)\n",
    "import pytest\n\ndef add(a, b):\n    pytest.skip('nope')\n",
], ids=["atexit-exit0-after-failure", "exit0-during-collection", "skip-from-imported-code"])
def test_exit_zero_that_contradicts_the_summary_is_not_a_pass(tmp_path, calc):
    """Subject code runs in another process, so it cannot turn pytest's own exit into 0.

    A failing assertion stays FAIL (pytest's exit is 1). ``os._exit`` during
    import is a collection error. A skip inside the subject is still not a pass.
    """
    evidence = _run(tmp_path, {"calc.py": calc, "test_calc.py": PASSING_TEST}, ["pytest", "-q", "test_calc.py"])
    assert evidence.status == "FAIL", (evidence.exit_code, evidence.stdout[-300:], evidence.stderr[-300:])
    assert "[evaluator]" in evidence.stderr


def test_summary_parser():
    assert pytest_summary("..\n2 passed in 0.01s\n") == {"passed": 2}
    assert pytest_summary("==== 1 failed, 2 passed, 1 error in 0.03s ====") == {"failed": 1, "passed": 2, "errors": 1}
    assert pytest_summary("1 passed, 1 warning in 0.01s (0:00:00)") == {"passed": 1, "warnings": 1}
    assert pytest_summary("F\n") is None


# --- 3. legitimate layouts keep working ---------------------------------------

def test_script_check_imports_a_sibling_helper(tmp_path):
    # The helper is part of the oracle, so it has to live in the server fixture
    # (ADR 0036). The code under test (calc.py) may differ; here it does not.
    files = {"calc.py": CALC, "checks/helper.py": "def expected():\n    return 5\n",
             "checks/run.py": "from helper import expected\nfrom calc import add\nassert add(2, 3) == expected()\n"}
    evidence = _run(tmp_path, files, ["python3", "checks/run.py"], server=files)
    assert evidence.status == "PASS", (evidence.stdout[-300:], evidence.stderr[-300:])


def test_script_dir_cannot_shadow_the_stdlib(tmp_path):
    files = {"checks/fractions.py": "import os; os._exit(0)\n", "checks/run.py": "import fractions\nraise SystemExit(1)\n"}
    assert _run(tmp_path, files, ["python3", "checks/run.py"]).status == "FAIL"


def test_agent_cannot_rewrite_a_registered_script(tmp_path):
    evidence = _run(tmp_path, {"checks/run.py": "pass\n"}, ["python3", "checks/run.py"],
                    server={"checks/run.py": "raise SystemExit(1)\n"})
    assert evidence.status == "INCONCLUSIVE" and "differs from the server fixture" in evidence.stderr


@pytest.mark.parametrize("argv", [["pytest", "-q", "tests/test_calc.py"],
                                  ["python", "-m", "pytest", "-q", "tests/test_calc.py"]])
def test_tests_in_a_subdirectory_import_root_level_code(tmp_path, argv):
    evidence = _run(tmp_path, {"calc.py": CALC, "tests/test_calc.py": PASSING_TEST}, argv)
    assert evidence.status == "PASS", (evidence.stdout[-400:], evidence.stderr[-400:])


def test_root_level_code_cannot_shadow_an_installed_module_for_bare_pytest(tmp_path):
    files = {"pytest.py": "import os; os._exit(0)\n", "_pytest.py": "import os; os._exit(0)\n",
             "tests/test_a.py": FAILING_TEST}
    assert _run(tmp_path, files, ["pytest", "-q", "tests/test_a.py"]).status == "FAIL"


def test_http_run_verifies_against_the_fixture(tmp_path):
    fixture = _write(tmp_path / "fixture", {"calc.py": CALC, "tests/test_calc.py": PASSING_TEST})
    write_checks(fixture, {"unit": ["pytest", "-q", "tests/test_calc.py"]})
    task_id = f"TASK-T64-{uuid.uuid4().hex[:8]}"
    assert client.post("/tasks", json={"id": task_id, "title": "t", "idempotency_key": task_id,
                                       "acceptance_criteria": ["c"]}).status_code == 201
    response = client.post(f"/tasks/{task_id}/run", json={"fixture_dir": str(fixture), "criterion_checks": {"c": ["unit"]}})
    assert response.status_code == 200, response.text
    assert response.json()["task_status"] == "succeeded", response.json()


# --- 2. a failed prctl is logged -----------------------------------------------

class _Collect(logging.Handler):
    def __init__(self):
        super().__init__(logging.WARNING)
        self.records: list[logging.LogRecord] = []

    def emit(self, record):
        self.records.append(record)


def test_failed_process_protection_is_logged(tmp_path, monkeypatch):
    monkeypatch.setattr(evaluator_module, "protect_server_process", lambda: False)
    monkeypatch.setattr(evaluator_module.sys, "platform", "linux")
    logger, handler = logging.getLogger("sdf.evaluator"), _Collect()
    disabled = logger.disabled
    logger.addHandler(handler)
    logger.disabled = False  # another test's dictConfig may have disabled existing loggers
    try:
        _run(tmp_path, {}, ["python3", "-c", "pass"])
    finally:
        logger.removeHandler(handler)
        logger.disabled = disabled
    assert any(r.getMessage() == "server_process_not_protected" for r in handler.records)
