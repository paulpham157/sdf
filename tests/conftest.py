"""Test-wide auth setup (ADR 0015): an ephemeral pepper per test session.

Tokens created by tests live only in the in-memory test database; nothing is
written to disk or printed.
"""

from __future__ import annotations

import os
import secrets

import pytest

os.environ["SDF_AUTH_PEPPER"] = secrets.token_urlsafe(48)
os.environ.pop("SDF_AUTH_DEV_INSECURE", None)
os.environ.pop("SDF_AUTH_SINGLE_USER", None)


@pytest.fixture(autouse=True)
def _reset_auth_lockout():
    from sdf_core.api import auth_failures

    auth_failures.reset()
    yield
    auth_failures.reset()
