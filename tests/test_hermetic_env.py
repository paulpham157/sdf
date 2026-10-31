"""The hermetic suite ignores ambient SDF_* configuration and the repo .env.

Live and Postgres gates still opt in: ``SDF_LIVE_*=1`` and
``SDF_POSTGRES_TEST_URL`` are left in the environment for the tests that
read them.
"""

from __future__ import annotations

import os
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

_HERMETIC_SLICE = [
    "tests/test_api_auth.py::test_health_is_public_and_openapi_is_not",
    "tests/test_api.py::test_configured_containment_requires_explicit_smoke_promotion",
    "tests/test_check_hardening.py::test_containment_defaults_to_require_for_live_agents",
    "tests/test_check_security.py::test_evaluator_does_not_inherit_server_env",
    "tests/test_evidence_independence.py::test_a_coverage_gap_is_not_verification_evidence",
    "tests/test_hermetic_env.py::test_repo_dotenv_is_not_loaded_for_hermetic_tests",
    "tests/test_hermetic_env.py::test_process_env_is_hermetic",
]


def _base_env() -> dict[str, str]:
    keep = ("PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "TEMP", "TMP", "VIRTUAL_ENV", "PYTHONPATH", "USER", "LOGNAME")
    return {key: os.environ[key] for key in keep if key in os.environ}


def test_process_env_is_hermetic():
    """Ambient control-plane variables do not survive conftest. Gate opt-ins may."""

    assert os.environ.get("SDF_DATABASE_URL", "") == ""
    assert os.environ["SDF_EVALUATOR_CONTAINMENT"] == "off"
    assert os.environ["SDF_AUTH_DEV_INSECURE"] == ""
    assert os.environ["SDF_AUTH_SINGLE_USER"] == ""
    assert os.environ["SDF_TEST_HERMETIC"] == "1"
    assert os.environ.get("SDF_CONTAINMENT_BACKEND", "") == ""
    assert os.environ.get("SDF_AGENT_SANDBOX", "") == ""
    assert os.environ["SDF_FIXTURES_ROOT"] == os.path.realpath(tempfile.gettempdir())


def test_repo_dotenv_is_not_loaded_for_hermetic_tests(tmp_path, monkeypatch):
    """The real repo ``.env`` is skipped. A fixture ``REPO_ROOT`` is still read.

    The server lifespan test points ``REPO_ROOT`` at a temp file and expects
    that file to load. A developer ``.env`` next to the code must not.
    """

    import sdf_core.credentials as credentials
    from sdf_core import auth

    (tmp_path / ".env").write_text(
        "SDF_OTEL_ENABLED=1\n"
        "SDF_AUTH_DEV_INSECURE=1\n",
        encoding="utf-8",
    )
    monkeypatch.setenv("SDF_TEST_HERMETIC", "1")
    for name in [key for key in os.environ if key.startswith("SDF_LIVE_")]:
        monkeypatch.delenv(name, raising=False)
    monkeypatch.setattr(credentials, "REPO_ROOT", tmp_path)
    fixture_env: dict[str, str] = {}
    assert credentials.load_dotenv(environ=fixture_env) == ("SDF_OTEL_ENABLED", "SDF_AUTH_DEV_INSECURE")
    repo = Path(__file__).resolve().parents[1]
    monkeypatch.setattr(credentials, "REPO_ROOT", repo)
    assert credentials._hermetic_skips_dotenv(repo / ".env") is True
    assert credentials.load_dotenv(environ={}) == ()
    monkeypatch.delenv("SDF_AUTH_DEV_INSECURE", raising=False)
    auth._dotenv.cache_clear()
    try:
        assert auth.dev_insecure() is False
    finally:
        auth._dotenv.cache_clear()


def test_hermetic_suite_ignores_ambient_sdf_environment():
    """Junk SDF_* values, including a database URL, do not fail hermetic tests.

    ``SDF_POSTGRES_TEST_URL`` is set here too. These modules do not read it;
    the Postgres file is the opt-in and is covered separately.
    """

    env = _base_env()
    env.update({
        "SDF_DATABASE_URL": "postgresql+psycopg://junk:junk@127.0.0.1:9/nope",
        "SDF_POSTGRES_TEST_URL": "postgresql+psycopg://junk:junk@127.0.0.1:9/nope",
        "SDF_WORKSPACE_ROOT": "/tmp/sdf-junk-workspaces",
        "SDF_ARTIFACT_ROOT": "/tmp/sdf-junk-artifacts",
        "SDF_FIXTURES_ROOT": "/tmp/sdf-junk-fixtures",
        "SDF_AUTH_PEPPER": "junk-pepper-from-the-ambient-environment",
        "SDF_AUTH_DEV_INSECURE": "1",
        "SDF_AUTH_SINGLE_USER": "1",
        "SDF_EVALUATOR_CONTAINMENT": "require",
        "SDF_CONTAINMENT_BACKEND": "e2b",
        "SDF_CONTAINMENT_SMOKE": "passed",
        "SDF_AGENT_SANDBOX": "e2b",
        "SDF_REQUIRE_PLAN_APPROVAL": "1",
        "SDF_REQUIRE_CONTAINMENT": "0",
        "SDF_TOOL_ALLOWLIST": "rm",
        "SDF_TEST_HERMETIC": "0",
        "SDF_LIVE_E2B": "0",
    })
    proc = subprocess.run(
        [sys.executable, "-m", "pytest", "-q", "--tb=line", *_HERMETIC_SLICE],
        cwd=ROOT, env=env, capture_output=True, text=True, timeout=180,
    )
    assert proc.returncode == 0, proc.stdout + proc.stderr


def test_postgres_gate_still_opts_in_when_its_url_is_set():
    """Collect-only: a set URL is not treated as 'not set', and is not connected."""

    env = _base_env()
    env["SDF_POSTGRES_TEST_URL"] = "postgresql+psycopg://postgres:postgres@127.0.0.1:9/sdf_core"
    env["SDF_DATABASE_URL"] = "postgresql+psycopg://junk:junk@127.0.0.1:9/nope"
    proc = subprocess.run(
        [sys.executable, "-m", "pytest", "--collect-only", "-q", "-rs", "tests/test_postgres_integration.py"],
        cwd=ROOT, env=env, capture_output=True, text=True, timeout=120,
    )
    text = proc.stdout + proc.stderr
    assert proc.returncode == 0, text
    assert "test_postgres_integration.py:" in text
    assert "SDF_POSTGRES_TEST_URL is not set" not in text
