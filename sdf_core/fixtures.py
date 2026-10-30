"""Server fixture directories: where ``fixture_dir`` may point (ADR 0023).

A Task run (``POST /tasks/{id}/run``) and a Plan (``POST /plans``, then the
Coordinator at run time) both read a fixture from ``SDF_FIXTURES_ROOT``.  The
same check is used at every entry point, so a relative ``fixture_dir`` always
means "relative to the fixtures root" and never "relative to the server's
working directory".
"""

from __future__ import annotations

import os
from collections.abc import Iterable
from pathlib import Path

FIXTURES_ROOT_ENV = "SDF_FIXTURES_ROOT"


class FixtureError(ValueError):
    """``fixture_dir`` is not an existing directory strictly inside the fixtures root."""


def fixtures_root() -> Path:
    """The only directory tree ``fixture_dir`` may point into (``SDF_FIXTURES_ROOT``)."""

    return Path(os.environ.get(FIXTURES_ROOT_ENV) or Path.cwd() / "fixtures").resolve()


def server_write_roots(extra: Iterable[Path | str] = ()) -> tuple[Path, ...]:
    """Trees an agent run writes into; a fixture or check source there is not server-controlled."""

    raw = [os.getenv("SDF_WORKSPACE_ROOT") or ".sdf-workspaces", ".sdf-workspaces",
           os.getenv("SDF_ARTIFACT_ROOT") or ".sdf-artifacts", ".sdf-artifacts", *extra]
    return tuple(dict.fromkeys(Path(item).resolve() for item in raw))


def resolve_fixture(raw: str, *, write_roots: Iterable[Path] | None = None) -> Path:
    """Resolve ``raw`` (symlinks included) to a directory strictly under the fixtures root.

    A relative ``raw`` is taken relative to the fixtures root.  The root itself
    is refused (it is the container of fixtures, not a fixture), and so is
    anything inside a workspace or artifact tree, which an agent can write.
    """

    root = fixtures_root()
    try:
        candidate = Path(raw)
        resolved = (candidate if candidate.is_absolute() else root / candidate).resolve(strict=True)
    except (OSError, RuntimeError, ValueError, TypeError):
        raise FixtureError(f"fixture_dir must be an existing directory under {FIXTURES_ROOT_ENV}") from None
    if not resolved.is_dir() or not resolved.is_relative_to(root):
        raise FixtureError(f"fixture_dir must be an existing directory under {FIXTURES_ROOT_ENV}")
    if resolved == root:
        raise FixtureError(f"fixture_dir must be a directory inside {FIXTURES_ROOT_ENV}, not the root itself")
    roots = server_write_roots() if write_roots is None else tuple(write_roots)
    if any(resolved == write_root or resolved.is_relative_to(write_root) for write_root in roots):
        raise FixtureError("fixture_dir must not be under a workspace or artifact root")
    return resolved
