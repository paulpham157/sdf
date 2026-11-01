from __future__ import annotations

import ctypes
import logging
import os
import re
import stat
import sys
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass, replace
from pathlib import Path
from typing import Any

from .acceptance import (
    EVALUATOR_IDENTITY,
    EVALUATOR_ROLE,
    EVIDENCE_CLASS_VERIFICATION,
    VERIFICATION_TTL,
    environment_digest,
)
from .checks import ALLOWED_EXECUTABLES, check_targets, is_pytest, python_invocation, pytest_targets
from .containment import ContainmentUnavailable
from .model import utcnow
from .oracle import (
    OracleTamper,
    declared_test_count,
    materialize,
    PYTEST_SEAL,
    load_result_frame,
    load_script_result,
    seal_oracle_digest,
    subject_module_names,
    prepare_private_report,
    prepare_sealed_report,
    pytest_oracle_verdict,
    retarget_pytest,
    script_oracle_verdict,
)
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
# first on sys.path). The pre-workspace path is snapshotted, then the workspace
# is *appended* so the code under test is importable. A path finder resolves a
# top-level name on that snapshot whenever the name exists there, so agent code
# that inserts the workspace at sys.path[0] still cannot shadow the stdlib or
# an installed package (pytest, ...). Replacing the finder is the same inherent
# limit as os._exit(0) (ADR 0025): the imported module already controls the
# process. A script check also gets its own directory appended (before the
# cwd), as plain python would put it first: sibling helpers import, but a
# sibling ``json.py`` cannot replace the stdlib one.
# The sandbox process is the notary for pytest and for script / python -c.
# It forks the oracle child and writes the result file only after that child
# exits. The child returns here. Subject imports are proxied to the worker.
# A script or python -c check records a completion frame only after its own
# code returns (or SystemExit 0). No frame means the parent does not pass it.
PYTHON_BOOTSTRAP = (
    "import importlib.machinery, os, runpy, sys\n"
    "_m, _t = sys.argv[1], sys.argv[2]\n"
    "sys.argv = ['-c' if _m == '-c' else _t, *sys.argv[3:]]\n"
    "_seal = os.environ.pop('SDF_ORACLE_SEAL', '')\n"
    "if _seal:\n"
    "    exec(compile(_seal, '<seal>', 'exec'), {'__name__': 'sdf_seal'})\n"
    "_trusted = list(sys.path)\n"
    "if _m == 'path':\n"
    "    sys.path.append(os.path.dirname(os.path.abspath(_t)))\n"
    "sys.path.append(os.getcwd())\n"
    "_orig = importlib.machinery.PathFinder.find_spec\n"
    "def _find_spec(cls, fullname, path=None, target=None):\n"
    "    if path is None:\n"
    "        spec = _orig(fullname, _trusted, target)\n"
    "        if spec is not None:\n"
    "            return spec\n"
    "    return _orig(fullname, path, target)\n"
    "importlib.machinery.PathFinder.find_spec = classmethod(_find_spec)\n"
    "def _run():\n"
    "    if _m == '-c':\n"
    "        exec(compile(_t, '<string>', 'exec'), {'__name__': '__main__', '__builtins__': __builtins__})\n"
    "    elif _m == '-m':\n"
    "        runpy.run_module(_t, run_name='__main__', alter_sys=True)\n"
    "    else:\n"
    "        runpy.run_path(_t, run_name='__main__')\n"
    "if _m == '-m' and _t == 'pytest':\n"
    "    _run()\n"
    "else:\n"
    "    try:\n"
    "        _run()\n"
    "    except SystemExit as _exc:\n"
    "        if _exc.code not in (None, 0):\n"
    "            raise\n"
    "    _note = getattr(sys.modules.get('sdf_oracle_plugin'), 'note_completed', None)\n"
    "    if _note is None:\n"
    "        raise SystemExit(86)\n"
    "    _note(0)\n"
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


def _protect_or_warn() -> None:
    """Call :func:`protect_server_process`; log when it fails on Linux (checks then could read /proc)."""

    if not protect_server_process() and sys.platform.startswith("linux"):
        _log.warning("server_process_not_protected", extra={
            "reason": "prctl(PR_SET_DUMPABLE, 0) failed; a host check can read this process's /proc environ"})


# pytest's final summary, e.g. "1 failed, 2 passed in 0.03s" or "==== 2 passed in 0.01s ====".
_PYTEST_SUMMARY = re.compile(r"^=*\s*(?P<body>\d+ [a-z]+(?:, \d+ [a-z]+)*) in [0-9.]+s\b.*$")


def pytest_summary(stdout: str) -> dict[str, int] | None:
    """Counts from the last pytest summary line in ``stdout`` (``None`` if there is none)."""

    for line in reversed(stdout.splitlines()):
        match = _PYTEST_SUMMARY.match(line.strip())
        if match:
            counts: dict[str, int] = {}
            for item in match.group("body").split(", "):
                number, word = item.split(" ", 1)
                word = {"error": "errors", "warning": "warnings"}.get(word, word)
                counts[word] = counts.get(word, 0) + int(number)
            return counts
    return None


def pytest_verdict(stdout: str, truncated: bool = False) -> tuple[str, str] | None:
    """Parse a pytest summary. Not the accepting signal (ADR 0039).

    Kept so a caller can still read a summary. The evaluator decides pytest
    from the notary's result frame, because this text is under the control
    of any module the check imports.
    """

    counts = pytest_summary(stdout)
    if counts is None:
        if truncated:
            return "INCONCLUSIVE", "pytest output was truncated before its result summary"
        return "FAIL", "pytest exited 0 without a result summary"
    if counts.get("failed", 0) or counts.get("errors", 0):
        return "FAIL", f"pytest exited 0 but its summary reports failures: {counts}"
    if not counts.get("passed", 0):
        return "FAIL", f"pytest exited 0 but no test passed: {counts}"
    return None


def harden_argv(argv: Sequence[str], *, pytest_python: str | None = None) -> list[str]:
    """Rewrite an allowlisted check argv so workspace files cannot change how it runs.

    ``argv[0]`` is kept as given (the caller resolves it).  With
    ``pytest_python`` (the interpreter of the resolved ``pytest``), a bare
    ``pytest`` check runs as ``<python> -I -B -c BOOTSTRAP -m pytest`` so it gets
    the same ``sys.path`` as ``python -m pytest`` (workspace appended, root
    imports work).  Raises ``ValueError`` for a check form the evaluator
    cannot harden (see :func:`sdf_core.checks.validate_argv`).
    """

    name = os.path.basename(argv[0])
    rest = list(argv[1:])
    check_targets(argv)  # pytest must name its test files; no parent paths
    if name in {"python", "python3"}:
        flags, mode, target, tail = python_invocation(argv)
        if mode == "-m" and target == "pytest":
            tail = [*PYTEST_FLAGS, *tail]
        # -B: -I ignores PYTHONDONTWRITEBYTECODE, and a check must not leave
        # __pycache__ in the workspace it is judging (ADR 0032 drift check).
        return [argv[0], "-I", "-B", *flags, "-c", PYTHON_BOOTSTRAP, mode, target, *tail]
    if name == "pytest":
        if pytest_python is not None:
            return [pytest_python, "-I", "-B", "-c", PYTHON_BOOTSTRAP, "-m", "pytest", *PYTEST_FLAGS, *rest]
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
    # The check was measured, but killing its containment box failed. The
    # verdict stays PASS/FAIL; this is the cleanup error, not a new one.
    cleanup_error: str | None = None
    # ADR 0032 (#62): the workspace tree right after this check ran, when the
    # caller observed it (``after_check``); differs from the revision's result
    # tree only when the check (or anything else) changed the workspace.
    tree_after_sha256: str | None = None
    # ADR 0039: independence of a real check. Coverage gaps leave these unset
    # so kind=evaluation is not treated as verification.
    evidence_class: str | None = None
    producer_identity: str | None = None
    producer_role: str | None = None
    verifier_identity: str | None = None
    verifier_role: str | None = None
    subject_artifact_sha256: str | None = None
    oracle_digest: str | None = None
    environment_digest: str | None = None
    collected_at: Any = None
    expires_at: Any = None


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
    workspace appended to ``sys.path`` and top-level imports preferring the
    pre-workspace path; pytest ignores workspace config,
    ``conftest.py`` and plugins; ruff ignores workspace config.  On the host
    the server process is made non-dumpable first (:func:`protect_server_process`).

    Pytest, script, and ``python -c`` checks run from a sealed oracle when a
    server fixture is available (ADR 0039). Subject modules run in a worker
    process. Assertions and the completion frame stay in a child the subject
    cannot reach; the notary writes that frame only after the child exits.
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
        reference: Path | None = None,
        after_check: Callable[[Evidence], Evidence] | None = None,
        subject_artifact_sha256: str | None = None,
    ) -> EvaluationResult:
        """Evaluate explicit criterion/check mappings.

        ``after_check`` is called with the Evidence of every check that ran,
        right after it ran, and returns the Evidence to record (ADR 0032:
        the caller re-hashes the workspace and downgrades a PASS whose tree
        drifted).  The overall status is computed from what it returns.

        ``reference`` is the server fixture the workspace was copied from.  A
        check's own files (pytest test files, a python script) named by a
        relative path must be byte-identical to the fixture's copy, so the
        agent cannot rewrite the check it is judged by.

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
            evidence.append(replace(item, evidence_id=f"EVIDENCE-{attempt_id}-{index}"))

        def observed(item: Evidence) -> Evidence:
            return after_check(item) if after_check is not None else item

        covered: set[str] = set()
        for criterion in expected_criteria:
            mapped_commands = checks.get(criterion, ())
            if not mapped_commands:
                add(self._coverage_evidence(attempt_id=attempt_id, criterion=criterion))
                continue
            covered.add(criterion)
            for position, command in enumerate(mapped_commands):
                add(observed(
                    self._run_one(
                        attempt_id=attempt_id,
                        workspace=workspace,
                        command=command,
                        criterion=criterion,
                        index=index + 1,
                        position=position,
                        reference=reference,
                        subject_artifact_sha256=subject_artifact_sha256,
                    )
                ))

        # A mapping for a criterion that is not on the Task is not coverage;
        # record it so an accidental extra command cannot certify the task.
        for criterion in checks:
            if criterion not in covered and criterion not in expected_criteria:
                add(self._unmapped_evidence(attempt_id=attempt_id, criterion=criterion))

        # Keep the old ``commands`` argument observable, but do not associate
        # it with a criterion. This makes arbitrary command success unable to
        # validate business intent.
        for command in legacy_commands:
            add(observed(
                self._run_one(
                    attempt_id=attempt_id,
                    workspace=workspace,
                    command=command,
                    criterion=None,
                    index=index + 1,
                    reference=reference,
                    subject_artifact_sha256=subject_artifact_sha256,
                )
            ))

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
                      stdout: str = "", runner: str | None = None, *,
                      subject_artifact_sha256: str | None = None, oracle_digest: str | None = None) -> Evidence:
        # A check that did not run is still a verification verdict, not a
        # coverage gap (ADR 0039). It cannot accept an artifact.
        collected = utcnow()
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
            evidence_class=EVIDENCE_CLASS_VERIFICATION,
            producer_identity=EVALUATOR_IDENTITY,
            producer_role=EVALUATOR_ROLE,
            verifier_identity=EVALUATOR_IDENTITY,
            verifier_role=EVALUATOR_ROLE,
            subject_artifact_sha256=subject_artifact_sha256,
            oracle_digest=oracle_digest,
            environment_digest=environment_digest(command=rendered, runner=runner, oracle_digest=oracle_digest),
            collected_at=collected,
            expires_at=collected + VERIFICATION_TTL,
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
        reference: Path | None = None,
        subject_artifact_sha256: str | None = None,
    ) -> Evidence:
        argv = tuple(command)
        rendered = " ".join(argv)
        subject = subject_artifact_sha256

        def stop(reason: str, stdout: str = "", runner: str | None = None, *, oracle_digest: str | None = None) -> Evidence:
            return self._inconclusive(
                attempt_id, index, rendered, criterion, reason, stdout, runner,
                subject_artifact_sha256=subject, oracle_digest=oracle_digest,
            )

        if (
            not argv
            or not all(isinstance(part, str) for part in argv)
            or "/" in argv[0]
            or "\\" in argv[0]
            or argv[0] not in self.allowed_executables
        ):
            return stop("executable is not allowlisted (bare name required)")
        if any("\x00" in part for part in argv):
            return stop("argument contains a NUL byte")
        timeout = self.timeout_seconds
        if self.timeout_for is not None:
            timeout = self.timeout_for(criterion, position) or timeout
        workspace = Path(workspace)
        safe_path = self._safe_path(workspace)
        try:
            process_argv = harden_argv(argv)
            targets = check_targets(argv)
        except ValueError as exc:
            return stop(f"unsupported check form: {exc}")
        problem = _verify_targets(targets, workspace, reference)
        if problem is not None:
            return stop(problem)
        if self.containment_backend is None:
            if self.require_containment:
                return stop("evaluator containment is required but no backend is configured")
            executable = self._resolve_executable(argv[0], workspace, safe_path)
            if executable is None:
                return stop("executable not found outside the workspace")
            if os.path.basename(argv[0]) == "pytest":
                interpreter = self._pytest_interpreter(executable, workspace, safe_path)
                if interpreter is None:
                    return stop("cannot find the python interpreter of the pytest executable")
                process_argv = harden_argv(argv, pytest_python=interpreter)
            else:
                process_argv[0] = executable
            _protect_or_warn()
        sealed = None
        report = None
        try:
            absolute = [target for target in targets if os.path.isabs(target)]
            oracle_digest = None
            run_root = workspace
            python_check = _is_python_check(argv)
            # A server fixture is the oracle. Absolute test paths are already
            # outside the workspace; everything else runs from the sealed copy.
            if reference is not None and not absolute:
                try:
                    sealed = materialize(reference, workspace, targets, command=argv)
                except OracleTamper as exc:
                    return stop(exc.reason)
                oracle_digest = sealed.oracle_digest
                run_root = sealed.root
            # Pytest, scripts, and python -c all need a result file the notary
            # writes after the oracle child exits. The child's own exit code
            # is not the verdict.
            if python_check or is_pytest(argv):
                report = prepare_sealed_report(sealed) if sealed is not None else prepare_private_report()
            if report is not None and is_pytest(argv):
                process_argv = retarget_pytest(process_argv, sealed=sealed is not None)
            child_env = _check_env(safe_path)
            if report is not None and os.path.basename(process_argv[0]) not in {"pytest"}:
                # The bootstrap is the notary. It pops these before the oracle
                # child is forked, so the subject never sees them.
                child_env["SDF_ORACLE_SEAL"] = PYTEST_SEAL
                child_env["SDF_ORACLE_REPORT"] = str(report.result_path)
                child_env["SDF_ORACLE_KIND"] = "pytest" if is_pytest(argv) else "script"
                oracle_paths = set(sealed.oracle_paths) if sealed is not None else set()
                child_env["SDF_ORACLE_SUBJECTS"] = subject_module_names(run_root, oracle_paths)
            elif report is not None:
                return stop(
                    "oracle result channel is not sealed for a bare pytest executable",
                    oracle_digest=oracle_digest,
                )
            # A pure python -c check seals no files. The digest still covers
            # the registered argv, so it is not the hash of an empty input.
            if oracle_digest is None:
                oracle_digest = seal_oracle_digest(argv)
            try:
                sandbox = FixtureSandbox(
                    run_root,
                    timeout_seconds=timeout,
                    max_output_bytes=self.max_output_bytes,
                    containment_backend=self.containment_backend,
                    require_containment=self.require_containment,
                )
                completed = sandbox.run(process_argv, env=child_env)
            except (ContainmentUnavailable, SandboxViolation, OSError, ValueError) as exc:
                return stop(
                    f"check could not run: {type(exc).__name__}: {exc}"[:500],
                    runner=self.runner,
                    oracle_digest=oracle_digest,
                )
            if completed.timed_out:
                return stop((completed.stderr or "") + f"\ntimeout after {timeout:g}s", completed.stdout,
                            self.runner, oracle_digest=oracle_digest)
            result_status = "PASS" if completed.returncode == 0 else "FAIL"
            stderr = completed.stderr
            if is_pytest(argv) and report is not None:
                node_ids = _pytest_node_ids(argv)
                declared = declared_test_count(run_root if sealed is None else sealed.root, node_ids)
                result_status, reason = pytest_oracle_verdict(
                    exit_code=completed.returncode,
                    frame=load_result_frame(report.result_path),
                    declared=declared,
                    oracle_changed=bool(sealed and sealed.changed()),
                )
                if reason:
                    stderr = f"{stderr or ''}\n[evaluator] {reason}".lstrip("\n")
            elif (
                self.containment_backend is None
                and report is not None
                and python_check
                and not is_pytest(argv)
            ):
                # The notary writes the frame on this host. A containment
                # backend runs that process inside the sandbox and reports
                # the exit code; the frame is not on the host, and a non-git
                # workspace is not pulled.
                result_status, reason = script_oracle_verdict(
                    exit_code=completed.returncode,
                    frame=load_script_result(report.result_path),
                    oracle_changed=bool(sealed and sealed.changed()),
                )
                if reason:
                    stderr = f"{stderr or ''}\n[evaluator] {reason}".lstrip("\n")
            elif sealed is not None and sealed.changed():
                result_status = "INCONCLUSIVE"
                stderr = f"{stderr or ''}\noracle files changed while the check ran".lstrip("\n")
            cleanup_error = completed.cleanup_error
            if cleanup_error:
                _log.warning("evaluator_cleanup_failed", extra={"runner": self.runner, "error": cleanup_error[-500:]})
                stderr = f"{stderr or ''}\n[evaluator] cleanup failed: {cleanup_error}".lstrip("\n")
            return self._checked(
                attempt_id=attempt_id, index=index, rendered=rendered, criterion=criterion,
                status=result_status, exit_code=completed.returncode, stdout=completed.stdout, stderr=stderr,
                oracle_digest=oracle_digest, subject_artifact_sha256=subject,
                cleanup_error=cleanup_error,
            )
        finally:
            if sealed is not None:
                sealed.cleanup()
            elif report is not None:
                report.cleanup()

    def _checked(
        self, *, attempt_id: str, index: int, rendered: str, criterion: str | None, status: str,
        exit_code: int | None, stdout: str, stderr: str, oracle_digest: str | None,
        subject_artifact_sha256: str | None, cleanup_error: str | None = None,
    ) -> Evidence:
        collected = utcnow()
        if status == "PASS":
            confidence = 1.0
        elif status == "INCONCLUSIVE":
            confidence = 0.2
        else:
            confidence = 0.95
        return Evidence(
            evidence_id=f"EVIDENCE-{attempt_id}-{index}",
            attempt_id=attempt_id,
            kind="evaluation",
            status=status,
            command=rendered,
            exit_code=exit_code,
            stdout=stdout,
            stderr=stderr,
            confidence=confidence,
            criterion=criterion,
            runner=self.runner,
            cleanup_error=cleanup_error,
            evidence_class=EVIDENCE_CLASS_VERIFICATION,
            producer_identity=EVALUATOR_IDENTITY,
            producer_role=EVALUATOR_ROLE,
            verifier_identity=EVALUATOR_IDENTITY,
            verifier_role=EVALUATOR_ROLE,
            subject_artifact_sha256=subject_artifact_sha256,
            oracle_digest=oracle_digest,
            environment_digest=environment_digest(command=rendered, runner=self.runner, oracle_digest=oracle_digest),
            collected_at=collected,
            expires_at=collected + VERIFICATION_TTL,
        )

    def _pytest_interpreter(self, pytest_path: str, workspace: Path, safe_path: str) -> str | None:
        """The interpreter a ``pytest`` console script runs with (its shebang), outside the workspace."""

        try:
            with open(pytest_path, "rb") as handle:
                first = handle.readline(4096).decode("utf-8", "replace").strip()
        except OSError:
            return None
        candidate: str | None = None
        if first.startswith("#!"):
            words = first[2:].split()
            if words and os.path.basename(words[0]) == "env" and len(words) > 1:
                candidate = (self._resolve_executable(words[1], workspace, safe_path)
                             if words[1].startswith("python") else None)
            elif words:
                candidate = words[0]
        if candidate is None:
            sibling = os.path.join(os.path.dirname(pytest_path), "python")
            candidate = sibling if os.path.isfile(sibling) else None
        if candidate is None or not os.path.isabs(candidate) or not os.access(candidate, os.X_OK):
            return None
        root = workspace.resolve()
        if Path(candidate).resolve().is_relative_to(root):
            return None
        return candidate

    @staticmethod
    def _as_text(value: str | bytes | None) -> str:
        if value is None:
            return ""
        if isinstance(value, bytes):
            return value.decode(errors="replace")
        return value


def _is_python_check(argv: Sequence[str]) -> bool:
    """True for ``python`` / ``python3`` checks, including ``python -m pytest``."""

    return bool(argv) and os.path.basename(argv[0]) in {"python", "python3"}


def _pytest_node_ids(argv: Sequence[str]) -> list[str]:
    """Node ids a pytest check named, used as the oracle's expected test count."""

    name = os.path.basename(argv[0]) if argv else ""
    if name == "pytest":
        return pytest_targets(list(argv[1:]))
    if name in {"python", "python3"}:
        _, mode, target, rest = python_invocation(argv)
        if mode == "-m" and target == "pytest":
            return pytest_targets(rest)
    return []


def _verify_targets(targets: Sequence[str], workspace: Path, reference: Path | None) -> str | None:
    """Why the check's own files cannot be trusted, or ``None``.

    An absolute target must lie outside the workspace (a server directory).
    A relative one must exist as a regular file in the server fixture and the
    workspace copy must be byte-identical (no symlink, nothing rewritten).
    """

    root = workspace.resolve()
    for target in targets:
        if os.path.isabs(target):
            try:
                resolved = Path(target).resolve()
            except (OSError, RuntimeError):
                return f"cannot resolve check file {target}"
            if resolved == root or resolved.is_relative_to(root):
                return f"check file {target} is inside the workspace; name it relative to the fixture"
            continue
        if reference is None:
            return f"check file {target} cannot be verified: no server fixture to compare it with"
        ref_root = Path(reference).resolve()
        expected = _read_plain(ref_root, target)
        if expected is None:
            return f"check file {target} is not a regular file in the server fixture"
        actual = _read_plain(root, target)
        if actual != expected:
            return f"check file {target} differs from the server fixture (the agent changed or replaced it)"
    return None


def _read_plain(root: Path, relative: str) -> bytes | None:
    """Bytes of ``root/relative`` if every component is a real directory/file inside ``root``."""

    current = root
    for part in Path(relative).parts:
        current = current / part
        if current.is_symlink():
            return None
    try:
        if not current.resolve().is_relative_to(root) or not current.is_file():
            return None
        return current.read_bytes()
    except OSError:
        return None


def _backend_name(backend: Any) -> str:
    name = getattr(backend, "name", None)
    return name if isinstance(name, str) and name else type(backend).__name__
