"""Server-registered evaluator checks (ADR 0025).

The public API never accepts an argv to run.  A caller names *check ids*; the
argv behind each id comes from a source only the server operator controls:

* the fixture manifest ``sdf-checks.json`` at the root of the resolved
  ``fixture_dir`` (under ``SDF_FIXTURES_ROOT``), read from the original
  fixture before any agent touches its workspace copy, and
* an optional deployment-wide registry at ``SDF_EVALUATOR_CHECKS_FILE``.

Both use the same JSON shape::

    {"checks": {"unit-tests": {"argv": ["pytest", "-q"], "timeout_seconds": 60},
                "add-works": ["python3", "-c", "from calc import add; assert add(2, 3) == 5"]}}

Every argv is validated when it is loaded: the executable is a bare name from
the evaluator allowlist (no path), no argument contains NUL, and the timeout
is bounded.  An id defined in both sources is a configuration error.
"""

from __future__ import annotations

import json
import os
import re
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any

FIXTURE_MANIFEST = "sdf-checks.json"
CHECKS_FILE_ENV = "SDF_EVALUATOR_CHECKS_FILE"
ALLOWED_EXECUTABLES = frozenset({"python", "python3", "pytest", "ruff"})
DEFAULT_TIMEOUT_SECONDS = 30.0
MAX_TIMEOUT_SECONDS = 600.0
MAX_ARGS = 64
MAX_ARG_LEN = 4096
CHECK_ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:-]{0,119}")


class CheckConfigError(ValueError):
    """A registry file is malformed or defines an unsafe check (operator error)."""


class UnknownCheckError(LookupError):
    """A request named a check id that no server registry defines."""


@dataclass(frozen=True)
class RegisteredCheck:
    check_id: str
    argv: tuple[str, ...]
    timeout_seconds: float
    source: str


def validate_argv(argv: Sequence[Any], *, where: str = "check") -> tuple[str, ...]:
    """Validate a check argv; raise ``CheckConfigError`` when it is unsafe."""

    if isinstance(argv, (str, bytes)) or not isinstance(argv, Sequence) or not argv:
        raise CheckConfigError(f"{where}: argv must be a non-empty list of strings")
    if len(argv) > MAX_ARGS:
        raise CheckConfigError(f"{where}: argv has more than {MAX_ARGS} arguments")
    out: list[str] = []
    for part in argv:
        if not isinstance(part, str) or "\x00" in part or len(part) > MAX_ARG_LEN:
            raise CheckConfigError(f"{where}: every argument must be a string without NUL bytes")
        out.append(part)
    executable = out[0]
    if "/" in executable or "\\" in executable or executable not in ALLOWED_EXECUTABLES:
        raise CheckConfigError(
            f"{where}: executable must be a bare name from {sorted(ALLOWED_EXECUTABLES)}, not a path"
        )
    return tuple(out)


def _parse(data: Any, source: str) -> dict[str, RegisteredCheck]:
    if not isinstance(data, Mapping) or not isinstance(data.get("checks"), Mapping):
        raise CheckConfigError(f"{source}: expected an object with a 'checks' mapping")
    checks: dict[str, RegisteredCheck] = {}
    for check_id, spec in data["checks"].items():
        if not isinstance(check_id, str) or not CHECK_ID.fullmatch(check_id):
            raise CheckConfigError(f"{source}: invalid check id {check_id!r}")
        timeout: Any = DEFAULT_TIMEOUT_SECONDS
        if isinstance(spec, Mapping):
            argv = spec.get("argv")
            timeout = spec.get("timeout_seconds", DEFAULT_TIMEOUT_SECONDS)
            unknown = set(spec) - {"argv", "timeout_seconds"}
            if unknown:
                raise CheckConfigError(f"{source}: check {check_id!r} has unknown keys {sorted(unknown)}")
        else:
            argv = spec
        if isinstance(timeout, bool) or not isinstance(timeout, (int, float)) or not 0 < timeout <= MAX_TIMEOUT_SECONDS:
            raise CheckConfigError(f"{source}: check {check_id!r} timeout_seconds must be in (0, {MAX_TIMEOUT_SECONDS:g}]")
        checks[check_id] = RegisteredCheck(check_id, validate_argv(argv, where=f"{source}: check {check_id!r}"),
                                           float(timeout), source)
    return checks


def _load_file(path: Path, source: str) -> dict[str, RegisteredCheck]:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise CheckConfigError(f"{source}: cannot read check registry ({type(exc).__name__})") from exc
    return _parse(data, source)


class CheckRegistry:
    def __init__(self, checks: Mapping[str, RegisteredCheck]):
        self._checks = dict(checks)

    @classmethod
    def load(cls, fixture: Path | None = None, *, environ: Mapping[str, str] | None = None) -> "CheckRegistry":
        env = os.environ if environ is None else environ
        checks: dict[str, RegisteredCheck] = {}
        server_file = (env.get(CHECKS_FILE_ENV) or "").strip()
        if server_file:
            checks.update(_load_file(Path(server_file), CHECKS_FILE_ENV))
        if fixture is not None:
            manifest = Path(fixture) / FIXTURE_MANIFEST
            # A symlinked manifest could point anywhere; only a regular file counts.
            if manifest.is_symlink():
                raise CheckConfigError(f"{FIXTURE_MANIFEST}: must be a regular file, not a symlink")
            if manifest.is_file():
                for check_id, check in _load_file(manifest, FIXTURE_MANIFEST).items():
                    if check_id in checks:
                        raise CheckConfigError(f"check id {check_id!r} is defined by both {CHECKS_FILE_ENV} and {FIXTURE_MANIFEST}")
                    checks[check_id] = check
        return cls(checks)

    def ids(self) -> list[str]:
        return sorted(self._checks)

    def get(self, check_id: str) -> RegisteredCheck:
        try:
            return self._checks[check_id]
        except KeyError:
            raise UnknownCheckError(f"unknown check id: {check_id}") from None

    def resolve(self, criterion_checks: Mapping[str, Sequence[str]]) -> dict[str, list[list[str]]]:
        """Map ``{criterion: [check_id, ...]}`` to ``{criterion: [argv, ...]}``."""

        unknown = sorted({cid for ids in criterion_checks.values() for cid in ids if cid not in self._checks})
        if unknown:
            raise UnknownCheckError(f"unknown check ids: {unknown}")
        return {criterion: [list(self._checks[cid].argv) for cid in ids] for criterion, ids in criterion_checks.items()}

    def timeout_for(self, argv: Sequence[str]) -> float | None:
        for check in self._checks.values():
            if check.argv == tuple(argv):
                return check.timeout_seconds
        return None
