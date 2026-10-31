"""Test-wide auth setup (ADR 0015): an ephemeral pepper per test session.

Tokens created by tests live only in the in-memory test database; nothing is
written to disk or printed.
"""

from __future__ import annotations

import os
import secrets
import tempfile

import pytest

os.environ["SDF_AUTH_PEPPER"] = secrets.token_urlsafe(48)
os.environ.pop("SDF_AUTH_DEV_INSECURE", None)
os.environ.pop("SDF_AUTH_SINGLE_USER", None)
# Tests build fixtures under pytest's tmp dirs; the API confines fixture_dir
# to SDF_FIXTURES_ROOT (ADR 0023).
os.environ.setdefault("SDF_FIXTURES_ROOT", os.path.realpath(tempfile.gettempdir()))
# Plan runs default to SDF_EVALUATOR_CONTAINMENT=require (ADR 0025) and hermetic
# tests have no containment backend; the fake agents here are not live, so run
# checks on the host.  Tests of the default delete this variable.  Live-gated
# tests must not inherit it: see _live_tests_require_evaluator_containment.
os.environ["SDF_EVALUATOR_CONTAINMENT"] = "off"


@pytest.fixture(autouse=True)
def _reset_auth_lockout():
    from sdf_core.api import auth_failures

    auth_failures.reset()
    yield
    auth_failures.reset()


@pytest.fixture(autouse=True)
def _live_tests_require_evaluator_containment(request, monkeypatch):
    """Live-gated tests must not inherit the hermetic ``off`` default.

    That default runs evaluator checks on the host. A live full loop then
    executes agent-written workspace code as the pytest user, which can read
    provider keys from another same-uid process's ``/proc/*/environ`` (ADR
    0025). Hermetic tests are unchanged: the gate is off, or the test is not
    live-gated.
    """

    from tests.live_containment import (
        enforce_live_evaluator_containment,
        install_live_execution_service,
        live_gate_enabled,
        node_is_live_gated,
    )

    if not live_gate_enabled() or not node_is_live_gated(request.node):
        return
    enforce_live_evaluator_containment(monkeypatch)
    install_live_execution_service(monkeypatch)
