"""Private local state shared by the API and the token CLI.

Importing this module does not create directories or files. Callers that are
actually starting local mode call :func:`resolve_database_url` or
:func:`ensure_local_database`. The hermetic suite (``SDF_TEST_HERMETIC=1``)
keeps the in-memory database and never touches the data directory.

When ``SDF_DATABASE_URL`` is absent from the process environment and the
process is not hermetic, the database is ``$XDG_DATA_HOME/sdf/sdf.sqlite``
(or ``~/.local/share/sdf/sdf.sqlite``). The directory is mode 0700. The
database file and the generated runtime secret file are mode 0600. An
explicit ``SDF_DATABASE_URL`` in the process environment, including
PostgreSQL, is used as given and does not generate secrets and does not
turn authentication off. A URL that appears only in the repo-root ``.env``
is not that variable: it does not select a database and it does not make
an import run ``create_all``.

``SDF_AUTH_PEPPER`` and ``SDF_EVIDENCE_MAC_KEY`` are read from the
environment first. When local mode needs one and it is unset, a random value
is stored in the secret file and loaded into the process. A short explicit
value is rejected and is not replaced. Values are never logged.
"""

from __future__ import annotations

import os
import secrets
import stat
from pathlib import Path

MEMORY_URL = "sqlite+pysqlite:///:memory:"
DB_FILENAME = "sdf.sqlite"
SECRETS_FILENAME = "runtime-secrets"
SECRET_KEYS = ("SDF_AUTH_PEPPER", "SDF_EVIDENCE_MAC_KEY")
MIN_SECRET_CHARS = 32


class LocalStateError(RuntimeError):
    """The local data directory or its secrets cannot be used safely."""


def hermetic() -> bool:
    return os.environ.get("SDF_TEST_HERMETIC") == "1"


def data_dir() -> Path:
    """The local data directory. This does not create it."""

    base = os.environ.get("XDG_DATA_HOME")
    root = Path(base).expanduser() if base else Path.home() / ".local" / "share"
    return root / "sdf"


def sqlite_path() -> Path:
    return data_dir() / DB_FILENAME


def sqlite_url(path: Path | None = None) -> str:
    resolved = (sqlite_path() if path is None else path).resolve().as_posix()
    return "sqlite+pysqlite:///" + resolved


def is_local_default_url(url: str) -> bool:
    return url == sqlite_url()


def explicit_database_url() -> str | None:
    """The process environment's URL, or None when the variable is absent.

    An empty ``SDF_DATABASE_URL`` is returned as ``""`` so callers can reject
    it instead of treating it as the local default. The repo-root ``.env`` is
    not consulted. Hermetic tests strip ambient ``SDF_*`` before this runs,
    so a junk environment URL does not survive into the suite.
    """

    if "SDF_DATABASE_URL" in os.environ:
        return os.environ["SDF_DATABASE_URL"]
    return None


def resolve_database_url(*, create: bool) -> str:
    """The database URL for this process.

    ``create`` writes the local file only when no URL is configured and the
    process is not hermetic. A read-only caller passes ``create=False`` and
    gets the file URL when the file is already there, otherwise the in-memory
    URL, and creates nothing.
    """

    explicit = explicit_database_url()
    if explicit is not None:
        return explicit
    if hermetic():
        return MEMORY_URL
    path = sqlite_path()
    if path.is_symlink():
        raise LocalStateError("local database path must not be a symlink")
    if path.is_file():
        return sqlite_url(path)
    if not create:
        return MEMORY_URL
    return ensure_local_database()


def ensure_local_database() -> str:
    """Create the private local database and its runtime secrets."""

    if hermetic():
        raise LocalStateError("refusing to create a local database while SDF_TEST_HERMETIC=1")
    for key in SECRET_KEYS:
        current = os.environ.get(key)
        if current is not None and current != "" and len(current) < MIN_SECRET_CHARS:
            raise LocalStateError(
                f"{key} is set but shorter than {MIN_SECRET_CHARS} characters; it was not replaced"
            )
    directory = data_dir()
    _ensure_private_dir(directory)
    path = sqlite_path()
    if path.is_symlink() or (path.exists() and not path.is_file()):
        raise LocalStateError("local database path must be a regular file")
    if not path.exists():
        fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        os.close(fd)
    os.chmod(path, 0o600)
    _assert_private_file(path)
    ensure_local_secrets()
    return sqlite_url(path)


def ensure_local_secrets() -> None:
    """Load or generate the local pepper and Evidence MAC key.

    No-op under ``SDF_TEST_HERMETIC``. Does not replace a short explicit value.
    Environment values are not written into the secret file.
    """

    if hermetic():
        return
    for key in SECRET_KEYS:
        current = os.environ.get(key)
        if current is not None and current != "" and len(current) < MIN_SECRET_CHARS:
            raise LocalStateError(
                f"{key} is set but shorter than {MIN_SECRET_CHARS} characters; it was not replaced"
            )
    _ensure_private_dir(data_dir())
    path = data_dir() / SECRETS_FILENAME
    stored: dict[str, str] = {}
    if path.is_symlink() or (path.exists() and not path.is_file()):
        raise LocalStateError("local runtime secrets path must be a regular file")
    if path.is_file():
        _assert_private_file(path)
        stored = _parse_secrets(path.read_text(encoding="utf-8"))
    write = False
    for key in SECRET_KEYS:
        current = os.environ.get(key, "")
        if len(current) >= MIN_SECRET_CHARS:
            continue
        previous = stored.get(key, "")
        if len(previous) >= MIN_SECRET_CHARS:
            os.environ[key] = previous
            continue
        if previous:
            raise LocalStateError(f"stored {key} is shorter than {MIN_SECRET_CHARS} characters")
        stored[key] = secrets.token_urlsafe(48)
        os.environ[key] = stored[key]
        write = True
    if write:
        payload = "".join(f"{key}={stored[key]}\n" for key in SECRET_KEYS if stored.get(key))
        _write_private(path, payload)
        _assert_private_file(path)


def harden_sqlite_path(path: Path) -> None:
    """Force mode 0600 on a local database and any SQLite sidecar files."""

    for candidate in (path, Path(str(path) + "-wal"), Path(str(path) + "-shm")):
        if not candidate.exists():
            continue
        if candidate.is_symlink() or not candidate.is_file():
            raise LocalStateError("local database sidecar must be a regular file")
        os.chmod(candidate, 0o600)
        _assert_private_file(candidate)


def _ensure_private_dir(path: Path) -> None:
    try:
        path.mkdir(mode=0o700, parents=True, exist_ok=True)
    except OSError as exc:
        raise LocalStateError("could not create the local data directory") from exc
    if path.is_symlink() or not path.is_dir():
        raise LocalStateError("local data path must be a directory, not a symlink")
    os.chmod(path, 0o700)
    mode = stat.S_IMODE(path.stat().st_mode)
    if mode & 0o077:
        raise LocalStateError(f"local data directory is not private (mode {mode:04o}; need 0700)")


def _assert_private_file(path: Path) -> None:
    if path.is_symlink() or not path.is_file():
        raise LocalStateError("expected a regular file")
    mode = stat.S_IMODE(path.stat().st_mode)
    if mode & 0o077:
        raise LocalStateError(f"file is not private (mode {mode:04o}; need 0600)")


def _parse_secrets(text: str) -> dict[str, str]:
    found: dict[str, str] = {}
    try:
        lines = text.splitlines()
    except UnicodeError as exc:
        raise LocalStateError("local runtime secrets file is not readable") from exc
    for line in lines:
        if not line or line.startswith("#"):
            continue
        key, sep, value = line.partition("=")
        if sep != "=" or key not in SECRET_KEYS or not value or key in found:
            raise LocalStateError("local runtime secrets file has an unexpected entry")
        found[key] = value
    return found


def _write_private(path: Path, text: str) -> None:
    flags = os.O_WRONLY | os.O_CREAT | os.O_TRUNC
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    try:
        fd = os.open(path, flags, 0o600)
    except OSError as exc:
        raise LocalStateError("could not write the local runtime secrets file") from exc
    try:
        os.write(fd, text.encode("utf-8"))
        os.fchmod(fd, 0o600)
    finally:
        os.close(fd)
