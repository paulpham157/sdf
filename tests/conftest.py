"""Test-wide auth setup (ADR 0015): an ephemeral pepper per test session.

Tokens created by tests live only in the in-memory test database; nothing is
written to disk or printed.

The hermetic suite does not inherit ambient ``SDF_*`` configuration or the
repo-root ``.env``. A developer shell often exports ``SDF_DATABASE_URL``, and
``load_dotenv`` would otherwise apply the rest. Gate opt-ins stay: the
Postgres file reads ``SDF_POSTGRES_TEST_URL``, and live tests read
``SDF_LIVE_*`` (plus ``SDF_DOTENV``, ``SDF_E2B_*``, and ``SDF_DAYTONA_*``).
"""

from __future__ import annotations

import os
import secrets
import tempfile

import pytest

# Kept when present so the Postgres and live gates can still opt in.
_GATE_EXACT = frozenset({"SDF_POSTGRES_TEST_URL", "SDF_DOTENV"})
_GATE_PREFIXES = ("SDF_LIVE_", "SDF_E2B_", "SDF_DAYTONA_")


def _gate_opt_in(name: str) -> bool:
    return name in _GATE_EXACT or name.startswith(_GATE_PREFIXES)


for _name in [key for key in os.environ if key.startswith("SDF_") and not _gate_opt_in(key)]:
    os.environ.pop(_name, None)

# Set after the strip so an ambient SDF_TEST_HERMETIC=0 cannot turn .env back on.
os.environ["SDF_TEST_HERMETIC"] = "1"
os.environ["SDF_AUTH_PEPPER"] = secrets.token_urlsafe(48)
# Present-but-empty wins over a repo .env (auth.setting). Popping them would
# let .env turn dev-insecure or single-user mode on.
os.environ["SDF_AUTH_DEV_INSECURE"] = ""
os.environ["SDF_AUTH_SINGLE_USER"] = ""
# Tests build fixtures under pytest's tmp dirs; the API confines fixture_dir
# to SDF_FIXTURES_ROOT (ADR 0023). Assigned, not setdefault, so an ambient
# value cannot point the suite at another tree.
os.environ["SDF_FIXTURES_ROOT"] = os.path.realpath(tempfile.gettempdir())
# Plan runs default to SDF_EVALUATOR_CONTAINMENT=require (ADR 0025) and tests have
# no containment backend; the fake agents here are not live, so run checks on
# the host.  Tests of the default delete this variable.
os.environ["SDF_EVALUATOR_CONTAINMENT"] = "off"


@pytest.fixture(autouse=True)
def _reset_auth_lockout():
    from sdf_core.api import auth_failures

    auth_failures.reset()
    yield
    auth_failures.reset()
