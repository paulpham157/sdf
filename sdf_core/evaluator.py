from __future__ import annotations

import ctypes
import logging
import os
import stat
import sys
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from .checks import ALLOWED_EXECUTABLES, python_invocation
from .containment import ContainmentUnavailable
from .sandbox import FixtureSandbox, SandboxViolation

MAX_CHECK_OUTPUT_BYTES = 256 * 1024
_log = logging.getLogger("sdf.evaluator")

# Variables that would let workspace content or the server environment steer
# the check's interpreter/test runner.  FixtureSandbox builds the child env
# from scratch, so none of these is passed; _check_env() asserts it.
CLEARED_ENV = ("PYTHONPATH", "PYTHONHOME", "PYTHONSTARTUP", "PYTHONINSPECT", "PYTHONUSERBASE",
               "PYTEST_ADDOPTS", "PYTEST_PLUGINS", "RUFF_CACHE_DIR")
CHECK_ENV = {
    # sys.path[0] is never the cwd/script dir for a bare `pytest` (python checks also get -I).
    "PYTHONSAFEPATH": "1",
    "PYTHONNOUSERSITE": "1",
    "PYTHONDONTWRITEBYTECODE": "1",
    # Entry-point plugins are discovered on sys.path, which includes the workspace.
    "PYTEST_DISABLE_PLUGIN_AUTOLOAD": "1",
}
# pytest never reads workspace config, conftest.py files or its cache, and test
# modules are appended to sys.path (a workspace file cannot shadow an installed module).
PYTEST_FLAGS = ("--noconftest", "-c", os.devnull, "--rootdir", "/", "-p", "no:cacheprovider",
                "--import-mode=append")
RUFF_FLAGS = ("--isolated", "--no-cache")
# Run a python check with -I (no PYTHON* env, no user site, no cwd/script dir
# first on sys.path); the workspace is *appended* so the code under test is
# importable but cannot shadow the stdlib or an installed package (pytest, ...).
PYTHON_BOOTSTRAP = (
    "import sys, os, runpy\n"
    "_m, _t = sys.argv[1], sys.argv[2]\n"
    "sys.argv = ['-c' if _m == '-c' else _t, *sys.argv[3:]]\n"
    "sys.path.append(os.getcwd())\n"
    "if _m == '-c':\n"
    "    exec(compile(_t, '<string>', 'exec'), {'__name__': '__main__', '__builtins__': __builtins__})\n"
    "elif _m == '-m':\n"
    "    runpy.run_module(_t, run_name='__main__', alter_sys=True)\n"
    "else:\n"
    "    runpy.run_path(_t, run_name='__main__')\n"
)
PR_SET_DUMPABLE = 4


def protect_server_process() -> bool:
    """Make this (server) process non-dumpable so a check cannot read its secrets.

    A host check runs as the server's uid; without this, any check can read
    ``/proc/<server pid>/environ`` (the server's initial environment, i.e. its
    API keys) and ``/proc/<pid>/mem``.  ``PR_SET_DUMPABLE=0`` makes those files
    root-owned.  Children are reset to dumpable on ``execve``, so checks are
    unaffected.  Idempotent; returns False where unsupported (non-Linux).
    """

    if not sys.platform.startswith("linux"):
        return False
    try:
        libc = ctypes.CDLL(None, use_errno=True)
        return libc.prctl(PR_SET_DUMPABLE, 0, 0, 0, 0) == 0
    except (OSError, AttributeError):
        return False


def harden_argv(argv: Sequence[str]) -> list[str]:
    """Rewrite an allowlisted check argv so workspace files cannot change how it runs.

    ``argv[0]`` is kept as given (the caller resolves it).  Raises ``ValueError``
    for a python invocation the evaluator cannot harden.
    """

    name = os.path.basename(argv[0])
    rest = list(argv[1:])
    if name in {"python", "python3"}:
        flags, mode, target, tail = python_invocation(argv)
        if mode == "-m" and target == "pytest":
            tail = [*PYTEST_FLAGS, *tail]
        return [argv[0], "-I", *flags, "-c", PYTHON_BOOTSTRAP, mode, target, *tail]
    if name == "pytest":
        return [argv[0], *PYTEST_FLAGS, *rest]
    if name == "ruff":
        if rest and not rest[0].startswith("-"):
            return [argv[0], rest[0], *RUFF_FLAGS, *rest[1:]]
        return [argv[0], *RUFF_FLAGS, *rest]
    return list(argv)


def _check_env(safe_path: str) -> dict[str, str]:
    env = {"PATH": safe_path, **CHECK_ENV}
    assert not set(CLEARED_ENV) & set(env)
    return env


@dataclass(frozen=True)
class Evidence:
    evidence_id: str
    attempt_id: str
    kind: str
    status: str
    command: str
    exit_code: int | None
    stdout: str
    stderr: str
    confidence: float
    criterion: str | None = None
    # Where the check ran: "host", the containment backend's name, or None
    # when nothing ran (coverage/config evidence).
    runner: str | None = None


@dataclass(frozen=True)
class EvaluationResult:
    status: str
    evidence: tuple[Evidence, ...]


class DeterministicEvaluator:
    """Run deterministic checks against an Attempt workspace (ADR 0025).

    A check never inherits the server environment: it gets the reduced
    environment of :class:`FixtureSandbox` (``PATH``, a workspace ``HOME`` and
    temp dirs). The executable must be a bare allowlisted name; it is resolved
    on the server ``PATH`` with relative entries and entries inside the
    workspace removed, so a file the agent wrote cannot stand in for it. A
    timeout kills the whole process group. With a ``containment_backend`` the
    check runs there instead of on the host; with ``require_containment`` and
    no backend it does not run at all (INCONCLUSIVE).

    The argv is hardened (:func:`harden_argv`): python runs isolated with the
    workspace appended to ``sys.path``; pytest ignores workspace config,
    ``conftest.py`` and plugins; ruff ignores workspace config.  On the host
    the server process is made non-dumpable first (:func:`protect_server_process`).
    What remains is inherent: a check that imports agent code runs agent code,
    which can end the process with status 0 (see ADR 0025).
    """

    def __init__(
        self,
        *,
        timeout_seconds: float = 30.0,
        allowed_executables: set[str] | None = None,
        containment_backend: Any | None = None,
        require_containment: bool = False,
        timeout_for: Callable[[str | None, int], float | None] | None = None,
        max_output_bytes: int = MAX_CHECK_OUTPUT_BYTES,
    ):
        self.timeout_seconds = timeout_seconds
        allowed = set(allowed_executables or ALLOWED_EXECUTABLES)
        # The allowlist can only be narrowed: interpreters outside it are never trusted.
        self.allowed_executables = allowed & set(ALLOWED_EXECUTABLES)
        self.containment_backend = containment_backend
        self.require_containment = require_containment
        self.timeout_for = timeout_for
        self.max_output_bytes = max_output_bytes
        self.runner = "host" if containment_backend is None else _backend_name(containment_backend)

    def evaluate(
        self,
        *,
        attempt_id: str,
        workspace: Path,
        commands: Sequence[Sequence[str]] | None = None,
        criteria: Sequence[str] | None = None,
        criterion_checks: Mapping[str, Sequence[Sequence[str]]] | None = None,
    ) -> EvaluationResult:
        """Evaluate explicit criterion/check mappings.

        ``commands`` remains a compatibility path for callers that used the
        original evaluator API. When a task supplies ``criteria`` (including
        an empty list), those commands are unscoped observations and cannot
        make the evaluation pass. A task is successful only when every named
        criterion has at least one mapped deterministic check and all mapped
        checks pass.
        """

        # An explicit mapping is authoritative. Ignore the compatibility
        # command list when it is supplied alongside the mapping so the same
        # check is not executed twice and cannot create duplicate Evidence.
        legacy_commands = tuple(commands or ()) if criterion_checks is None else ()
        checks = {
            criterion: tuple(tuple(command) for command in mapped_commands)
            for criterion, mapped_commands in (criterion_checks or {}).items()
        }
        strict_coverage = criteria is not None or criterion_checks is not None
        expected_criteria = tuple(criteria or ()) if criteria is not None else tuple(checks)
        evidence: list[Evidence] = []
        index = 0

        def add(item: Evidence) -> None:
            nonlocal index
            index += 1
            evidence.append(
                Evidence(
                    evidence_id=f"EVIDENCE-{attempt_id}-{index}",
                    attempt_id=item.attempt_id,
                    kind=item.kind,
                    status=item.status,
                    command=item.command,
                    exit_code=item.exit_code,
                    stdout=item.stdout,
                    stderr=item.stderr,
                    confidence=item.confidence,
                    criterion=item.criterion,
                    runner=item.runner,
                )
            )

        covered: set[str] = set()
        for criterion in expected_criteria:
            mapped_commands = checks.get(criterion, ())
            if not mapped_commands:
                add(self._coverage_evidence(attempt_id=attempt_id, criterion=criterion))
                continue
            covered.add(criterion)
            for position, command in enumerate(mapped_commands):
                add(
                    self._run_one(
                        attempt_id=attempt_id,
                        workspace=workspace,
                        command=command,
                        criterion=criterion,
                        index=index + 1,
                        position=position,
                    )
                )

        # A mapping for a criterion that is not on the Task is not coverage;
        # record it so an accidental extra command cannot certify the task.
        for criterion in checks:
            if criterion not in covered and criterion not in expected_criteria:
                add(self._unmapped_evidence(attempt_id=attempt_id, criterion=criterion))

        # Keep the old ``commands`` argument observable, but do not associate
        # it with a criterion. This makes arbitrary command success unable to
        # validate business intent.
        for command in legacy_commands:
            add(
                self._run_one(
                    attempt_id=attempt_id,
                    workspace=workspace,
                    command=command,
                    criterion=None,
                    index=index + 1,
                )
            )

        if not evidence:
            add(
                Evidence(
                    evidence_id="",
                    attempt_id=attempt_id,
                    kind="evaluation",
                    status="INCONCLUSIVE",
                    command="<no evaluator commands>",
                    exit_code=None,
                    stdout="",
                    stderr="no evaluator commands supplied",
                    confidence=0.2,
                    criterion=None,
                )
            )

        statuses = [item.status for item in evidence]
        if "FAIL" in statuses:
            overall = "FAIL"
        elif "INCONCLUSIVE" in statuses:
            overall = "INCONCLUSIVE"
        elif strict_coverage and (not expected_criteria or covered != set(expected_criteria)):
            overall = "INCONCLUSIVE"
        else:
            overall = "PASS"
        return EvaluationResult(status=overall, evidence=tuple(evidence))

    def _coverage_evidence(self, *, attempt_id: str, criterion: str) -> Evidence:
        return Evidence(
            evidence_id="",
            attempt_id=attempt_id,
            kind="evaluation",
            status="INCONCLUSIVE",
            command="<missing check>",
            exit_code=None,
            stdout="",
            stderr=f"no deterministic check mapped to criterion: {criterion}",
            confidence=0.2,
            criterion=criterion,
        )

    def _unmapped_evidence(self, *, attempt_id: str, criterion: str) -> Evidence:
        return Evidence(
            evidence_id="",
            attempt_id=attempt_id,
            kind="evaluation",
            status="INCONCLUSIVE",
            command="<unmapped criterion>",
            exit_code=None,
            stdout="",
            stderr=f"criterion is not declared by the task: {criterion}",
            confidence=0.2,
            criterion=criterion,
        )

    def _inconclusive(self, attempt_id: str, index: int, rendered: str, criterion: str | None, reason: str,
                      stdout: str = "", runner: str | None = None) -> Evidence:
        return Evidence(
            evidence_id=f"EVIDENCE-{attempt_id}-{index}",
            attempt_id=attempt_id,
            kind="evaluation",
            status="INCONCLUSIVE",
            command=rendered,
            exit_code=None,
            stdout=stdout,
            stderr=reason,
            confidence=0.2,
            criterion=criterion,
            runner=runner,
        )

    @staticmethod
    def _safe_path(workspace: Path) -> str:
        """The server PATH without relative entries or entries inside the workspace."""

        root = workspace.resolve()
        keep: list[str] = []
        for entry in os.environ.get("PATH", "").split(os.pathsep):
            if not entry or not os.path.isabs(entry):
                continue
            try:
                resolved = Path(entry).resolve()
            except (OSError, RuntimeError):
                continue
            if resolved == root or resolved.is_relative_to(root):
                continue
            try:
                mode = resolved.stat().st_mode
            except OSError:
                continue
            # Anyone (an agent's other process included) can drop a binary into
            # a world-writable dir such as /tmp.
            if mode & stat.S_IWOTH:
                continue
            keep.append(entry)
        return os.pathsep.join(keep)

    def _resolve_executable(self, name: str, workspace: Path, safe_path: str) -> str | None:
        found = None
        for entry in safe_path.split(os.pathsep):
            candidate = os.path.join(entry, name)
            if os.path.isfile(candidate) and os.access(candidate, os.X_OK):
                found = candidate
                break
        if found is None and name in {"python", "python3"}:
            found = sys.executable
        if found is None:
            return None
        root = workspace.resolve()
        candidate = Path(found)
        if not candidate.is_absolute():
            return None
        # Neither the PATH entry nor the symlink target may live in the workspace.
        if candidate.resolve().is_relative_to(root) or candidate.parent.resolve().is_relative_to(root):
            return None
        # Keep the unresolved path: a venv interpreter must stay a venv interpreter.
        return str(candidate)

    def _run_one(
        self,
        *,
        attempt_id: str,
        workspace: Path,
        command: Sequence[str],
        criterion: str | None,
        index: int,
        position: int = 0,
    ) -> Evidence:
        argv = tuple(command)
        rendered = " ".join(argv)
        if (
            not argv
            or not all(isinstance(part, str) for part in argv)
            or "/" in argv[0]
            or "\\" in argv[0]
            or argv[0] not in self.allowed_executables
        ):
            return self._inconclusive(attempt_id, index, rendered, criterion,
                                      "executable is not allowlisted (bare name required)")
        if any("\x00" in part for part in argv):
            return self._inconclusive(attempt_id, index, rendered, criterion, "argument contains a NUL byte")
        timeout = self.timeout_seconds
        if self.timeout_for is not None:
            timeout = self.timeout_for(criterion, position) or timeout
        workspace = Path(workspace)
        safe_path = self._safe_path(workspace)
        try:
            process_argv = harden_argv(argv)
        except ValueError as exc:
            return self._inconclusive(attempt_id, index, rendered, criterion, f"unsupported check form: {exc}")
        if self.containment_backend is None:
            if self.require_containment:
                return self._inconclusive(attempt_id, index, rendered, criterion,
                                          "evaluator containment is required but no backend is configured")
            executable = self._resolve_executable(argv[0], workspace, safe_path)
            if executable is None:
                return self._inconclusive(attempt_id, index, rendered, criterion,
                                          "executable not found outside the workspace")
            process_argv[0] = executable
            protect_server_process()
        try:
            sandbox = FixtureSandbox(
                workspace,
                timeout_seconds=timeout,
                max_output_bytes=self.max_output_bytes,
                containment_backend=self.containment_backend,
                require_containment=self.require_containment,
            )
            completed = sandbox.run(process_argv, env=_check_env(safe_path))
        except (ContainmentUnavailable, SandboxViolation, OSError, ValueError) as exc:
            return self._inconclusive(attempt_id, index, rendered, criterion,
                                      f"check could not run: {type(exc).__name__}: {exc}"[:500])
        if completed.timed_out:
            return self._inconclusive(attempt_id, index, rendered, criterion,
                                      (completed.stderr or "") + f"\ntimeout after {timeout:g}s", completed.stdout,
                                      runner=self.runner)
        result_status = "PASS" if completed.returncode == 0 else "FAIL"
        return Evidence(
            evidence_id=f"EVIDENCE-{attempt_id}-{index}",
            attempt_id=attempt_id,
            kind="evaluation",
            status=result_status,
            command=rendered,
            exit_code=completed.returncode,
            stdout=completed.stdout,
            stderr=completed.stderr,
            confidence=1.0 if result_status == "PASS" else 0.95,
            criterion=criterion,
            runner=self.runner,
        )

    @staticmethod
    def _as_text(value: str | bytes | None) -> str:
        if value is None:
            return ""
        if isinstance(value, bytes):
            return value.decode(errors="replace")
        return value


def _backend_name(backend: Any) -> str:
    name = getattr(backend, "name", None)
    return name if isinstance(name, str) and name else type(backend).__name__
