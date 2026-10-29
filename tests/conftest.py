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


@pytest.fixture(autouse=True)
def _reset_auth_lockout():
    from sdf_core.api import auth_failures

    auth_failures.reset()
    yield
    auth_failures.reset()
