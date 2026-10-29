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
the evaluator allowlist (no path), no argument contains NUL, a ``python``
check uses one of the forms the evaluator can harden (``-c``, ``-m`` or a
script path, after a small set of harmless flags), and the timeout is bounded
(1-600 s).  An id defined in both sources is a configuration error.  Plan
checks (``sdf_core.plans``) go through the same :func:`validate_argv`.
"""

from __future__ import annotations

import json
import os
import re
import stat
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any

FIXTURE_MANIFEST = "sdf-checks.json"
CHECKS_FILE_ENV = "SDF_EVALUATOR_CHECKS_FILE"
ALLOWED_EXECUTABLES = frozenset({"python", "python3", "pytest", "ruff"})
DEFAULT_TIMEOUT_SECONDS = 30.0
MIN_TIMEOUT_SECONDS = 1.0
MAX_TIMEOUT_SECONDS = 600.0
# Interpreter flags a python check may put before -c/-m/script.  Anything else
# (-E, -S, -X frozen_modules, -i, ...) could undo the evaluator's isolation.
PYTHON_FLAGS = frozenset({"-B", "-u", "-O", "-OO", "-b", "-bb", "-q"})
PYTHON_FLAGS_WITH_VALUE = frozenset({"-W"})
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
    if executable in {"python", "python3"}:
        try:
            python_invocation(out)
        except ValueError as exc:
            raise CheckConfigError(f"{where}: {exc}") from None
    return tuple(out)


def python_invocation(argv: Sequence[str]) -> tuple[list[str], str, str, list[str]]:
    """Split ``python [flags] (-c CODE | -m MODULE | SCRIPT) [args]``.

    Returns ``(flags, mode, target, rest)`` with ``mode`` one of ``-c``, ``-m``
    or ``path``; raises ``ValueError`` for any other shape.
    """

    flags: list[str] = []
    i = 1
    while i < len(argv):
        part = argv[i]
        if part in PYTHON_FLAGS:
            flags.append(part)
            i += 1
        elif part in PYTHON_FLAGS_WITH_VALUE:
            if i + 1 >= len(argv):
                raise ValueError(f"python flag {part} needs a value")
            flags += [part, argv[i + 1]]
            i += 2
        elif part in {"-c", "-m"}:
            if i + 1 >= len(argv) or not argv[i + 1]:
                raise ValueError(f"python {part} needs a value")
            return flags, part, argv[i + 1], list(argv[i + 2:])
        elif part.startswith("-"):
            raise ValueError(f"python flag {part!r} is not allowed in a check "
                             f"(allowed: {sorted(PYTHON_FLAGS | PYTHON_FLAGS_WITH_VALUE)}, then -c, -m or a script)")
        else:
            return flags, "path", part, list(argv[i + 1:])
    raise ValueError("python check needs -c CODE, -m MODULE or a script path")


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
        if isinstance(timeout, bool) or not isinstance(timeout, (int, float)) or not MIN_TIMEOUT_SECONDS <= timeout <= MAX_TIMEOUT_SECONDS:
            raise CheckConfigError(f"{source}: check {check_id!r} timeout_seconds must be in "
                                   f"[{MIN_TIMEOUT_SECONDS:g}, {MAX_TIMEOUT_SECONDS:g}]")
        checks[check_id] = RegisteredCheck(check_id, validate_argv(argv, where=f"{source}: check {check_id!r}"),
                                           float(timeout), source)
    return checks


MAX_REGISTRY_BYTES = 1024 * 1024


def _read_regular_file(path: Path, source: str) -> bytes:
    """Read ``path`` without following a final symlink, and only if it is a regular file.

    ``O_NOFOLLOW`` + ``fstat`` on the opened descriptor closes the window between
    a symlink check and the read (a swap after the check is refused too).
    """

    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0)
    try:
        fd = os.open(path, flags)
    except OSError as exc:
        raise CheckConfigError(f"{source}: cannot open check registry ({type(exc).__name__}; "
                               "it must be a regular file, not a symlink)") from exc
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode):
            raise CheckConfigError(f"{source}: check registry must be a regular file")
        if info.st_size > MAX_REGISTRY_BYTES:
            raise CheckConfigError(f"{source}: check registry is larger than {MAX_REGISTRY_BYTES} bytes")
        with os.fdopen(fd, "rb", closefd=False) as handle:
            return handle.read(MAX_REGISTRY_BYTES + 1)
    finally:
        os.close(fd)


def _load_file(path: Path, source: str) -> dict[str, RegisteredCheck]:
    raw = _read_regular_file(path, source)
    try:
        data = json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise CheckConfigError(f"{source}: cannot read check registry ({type(exc).__name__})") from exc
    return _parse(data, source)


def _refuse_under(path: Path, roots: Sequence[Path], source: str) -> None:
    """A registry the agent could write (workspace or artifact tree) is not server-controlled."""

    try:
        resolved = Path(path).resolve()
    except (OSError, RuntimeError) as exc:
        raise CheckConfigError(f"{source}: cannot resolve path ({type(exc).__name__})") from exc
    for root in roots:
        try:
            root_resolved = Path(root).resolve()
        except (OSError, RuntimeError):
            continue
        if resolved == root_resolved or resolved.is_relative_to(root_resolved):
            raise CheckConfigError(f"{source}: must not live under a workspace or artifact root")


class CheckRegistry:
    def __init__(self, checks: Mapping[str, RegisteredCheck]):
        self._checks = dict(checks)
        # (criterion, position) -> check id, filled by resolve(); timeouts are
        # looked up by id so two ids with the same argv keep their own limits.
        self._slots: dict[tuple[str, int], str] = {}

    @classmethod
    def load(cls, fixture: Path | None = None, *, environ: Mapping[str, str] | None = None,
             forbidden_roots: Sequence[Path] = ()) -> "CheckRegistry":
        env = os.environ if environ is None else environ
        checks: dict[str, RegisteredCheck] = {}
        server_file = (env.get(CHECKS_FILE_ENV) or "").strip()
        if server_file:
            _refuse_under(Path(server_file), forbidden_roots, CHECKS_FILE_ENV)
            checks.update(_load_file(Path(server_file), CHECKS_FILE_ENV))
        if fixture is not None:
            _refuse_under(Path(fixture), forbidden_roots, "fixture_dir")
            manifest = Path(fixture) / FIXTURE_MANIFEST
            # A symlinked manifest could point anywhere; only a regular file counts.
            if manifest.is_symlink():
                raise CheckConfigError(f"{FIXTURE_MANIFEST}: must be a regular file, not a symlink")
            if os.path.lexists(manifest):
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
        self._slots = {(criterion, position): cid
                       for criterion, ids in criterion_checks.items() for position, cid in enumerate(ids)}
        return {criterion: [list(self._checks[cid].argv) for cid in ids] for criterion, ids in criterion_checks.items()}

    def timeout_for(self, check_id: str) -> float | None:
        check = self._checks.get(check_id)
        return check.timeout_seconds if check is not None else None

    def slot_timeout(self, criterion: str | None, position: int) -> float | None:
        """Timeout of the check id that :meth:`resolve` put at ``criterion[position]``."""

        if criterion is None:
            return None
        check_id = self._slots.get((criterion, position))
        return self.timeout_for(check_id) if check_id is not None else None
