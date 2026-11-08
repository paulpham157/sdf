"""Local database default shared by the API and the token CLI."""

from __future__ import annotations

import hashlib
import os
import stat
import subprocess
import sys
from pathlib import Path

import pytest

from sdf_core.local_state import (
    MEMORY_URL,
    LocalStateError,
    data_dir,
    ensure_local_database,
    resolve_database_url,
    sqlite_path,
)

REPO = Path(__file__).resolve().parents[1]


def test_hermetic_resolve_does_not_create_files(tmp_path, monkeypatch):
    monkeypatch.setenv("SDF_TEST_HERMETIC", "1")
    monkeypatch.delenv("SDF_DATABASE_URL", raising=False)
    monkeypatch.setenv("XDG_DATA_HOME", str(tmp_path / "xdg"))
    assert resolve_database_url(create=True) == MEMORY_URL
    assert not (tmp_path / "xdg").exists()


def test_explicit_url_does_not_create_local_files_or_secrets(tmp_path, monkeypatch):
    monkeypatch.delenv("SDF_TEST_HERMETIC", raising=False)
    monkeypatch.setenv("XDG_DATA_HOME", str(tmp_path / "xdg"))
    monkeypatch.setenv("SDF_DATABASE_URL", "postgresql+psycopg://user:secret@localhost/sdf")
    monkeypatch.delenv("SDF_AUTH_PEPPER", raising=False)
    monkeypatch.delenv("SDF_EVIDENCE_MAC_KEY", raising=False)
    url = resolve_database_url(create=True)
    assert url.startswith("postgresql+psycopg://")
    assert not (tmp_path / "xdg").exists()


def test_short_pepper_is_not_replaced(tmp_path, monkeypatch):
    monkeypatch.delenv("SDF_TEST_HERMETIC", raising=False)
    monkeypatch.delenv("SDF_DATABASE_URL", raising=False)
    monkeypatch.setenv("XDG_DATA_HOME", str(tmp_path / "xdg"))
    monkeypatch.setenv("SDF_AUTH_PEPPER", "too-short")
    with pytest.raises(LocalStateError, match="SDF_AUTH_PEPPER"):
        ensure_local_database()
    assert os.environ["SDF_AUTH_PEPPER"] == "too-short"
    assert not (tmp_path / "xdg").exists()


def test_local_default_permissions_and_stable_secrets(tmp_path, monkeypatch):
    monkeypatch.delenv("SDF_TEST_HERMETIC", raising=False)
    monkeypatch.delenv("SDF_DATABASE_URL", raising=False)
    monkeypatch.delenv("SDF_AUTH_PEPPER", raising=False)
    monkeypatch.delenv("SDF_EVIDENCE_MAC_KEY", raising=False)
    monkeypatch.setenv("XDG_DATA_HOME", str(tmp_path / "xdg"))
    url = ensure_local_database()
    assert url == resolve_database_url(create=False)
    assert ":memory:" not in url
    pepper = os.environ["SDF_AUTH_PEPPER"]
    mac = os.environ["SDF_EVIDENCE_MAC_KEY"]
    assert len(pepper) >= 32 and len(mac) >= 32
    assert pepper not in url and mac not in url
    directory = data_dir()
    secret = directory / "runtime-secrets"
    assert stat.S_IMODE(directory.stat().st_mode) == 0o700
    assert stat.S_IMODE(sqlite_path().stat().st_mode) == 0o600
    assert stat.S_IMODE(secret.stat().st_mode) == 0o600
    assert not secret.is_symlink() and not sqlite_path().is_symlink()
    text = secret.read_text(encoding="utf-8")
    assert pepper in text and mac in text
    monkeypatch.delenv("SDF_AUTH_PEPPER", raising=False)
    monkeypatch.delenv("SDF_EVIDENCE_MAC_KEY", raising=False)
    again = ensure_local_database()
    assert again == url
    assert os.environ["SDF_AUTH_PEPPER"] == pepper
    assert os.environ["SDF_EVIDENCE_MAC_KEY"] == mac


def test_api_and_token_cli_share_the_local_database(tmp_path):
    xdg = tmp_path / "xdg"
    home = tmp_path / "home"
    home.mkdir()
    env = {
        "PATH": os.environ.get("PATH", ""),
        "HOME": str(home),
        "XDG_DATA_HOME": str(xdg),
        "PYTHONPATH": str(REPO),
        "PYTHONNOUSERSITE": "1",
        "PYTHONDONTWRITEBYTECODE": "1",
    }
    first = subprocess.run(
        [sys.executable, "-c", _CHILD_CREATE],
        cwd=REPO,
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )
    assert first.returncode == 0, first.stderr
    second = subprocess.run(
        [sys.executable, "-c", _CHILD_LIST],
        cwd=REPO,
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )
    assert second.returncode == 0, second.stderr
    created = _facts(first.stdout)
    listed = _facts(second.stdout)
    assert created["URL"] == listed["URL"]
    assert ":memory:" not in created["URL"]
    assert created["SHA"] == listed["SHA"]
    assert created["PRINCIPAL"] == "ada"
    assert listed["COUNT"] == "1"
    secret = xdg / "sdf" / "runtime-secrets"
    database = xdg / "sdf" / "sdf.sqlite"
    assert stat.S_IMODE(secret.stat().st_mode) == 0o600
    assert stat.S_IMODE(database.stat().st_mode) == 0o600
    assert created["SHA"] == hashlib.sha256(secret.read_bytes()).hexdigest()


def _facts(stdout: str) -> dict[str, str]:
    facts = {}
    for line in stdout.splitlines():
        if " " not in line:
            continue
        key, value = line.split(" ", 1)
        if key in {"URL", "SHA", "PRINCIPAL", "COUNT"}:
            facts[key] = value
    return facts


_CHILD_CREATE = """
import hashlib, os
from pathlib import Path
import sdf_core.api as api
from sdf_core import auth_cli
from sdf_core.local_state import resolve_database_url, sqlite_path, sqlite_url

url = api.DATABASE_URL
assert url == resolve_database_url(create=False) == sqlite_url(sqlite_path())
pepper = os.environ["SDF_AUTH_PEPPER"]
mac = os.environ["SDF_EVIDENCE_MAC_KEY"]
assert len(pepper) >= 32 and len(mac) >= 32
assert pepper not in url and mac not in url
secret = Path(os.environ["XDG_DATA_HOME"]) / "sdf" / "runtime-secrets"
digest = hashlib.sha256(secret.read_bytes()).hexdigest()
rc = auth_cli.main(["create", "--principal", "ada", "--role", "operator"])
assert rc == 0
print("URL " + url)
print("SHA " + digest)
print("PRINCIPAL ada")
"""

_CHILD_LIST = """
import hashlib, json, os
from pathlib import Path
import sdf_core.api as api
from sdf_core import auth_cli
from sdf_core.local_state import resolve_database_url

assert api.DATABASE_URL == resolve_database_url(create=False)
secret = Path(os.environ["XDG_DATA_HOME"]) / "sdf" / "runtime-secrets"
digest = hashlib.sha256(secret.read_bytes()).hexdigest()
import io, contextlib
buf = io.StringIO()
with contextlib.redirect_stdout(buf):
    rc = auth_cli.main(["list"])
assert rc == 0
rows = json.loads(buf.getvalue())
assert len(rows) == 1 and rows[0]["principal"] == "ada"
assert "secret" not in rows[0] and "secret_hash" not in rows[0]
print("URL " + api.DATABASE_URL)
print("SHA " + digest)
print("COUNT 1")
"""
