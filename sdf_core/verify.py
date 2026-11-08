"""Verify one repository with the user's own agent on this computer.

    uv run python -m sdf_core.verify /absolute/path/to/your/repository

The repository is copied into a private snapshot. Symlinks are refused.
``.git``, virtualenvs, agent config directories and secret-shaped files are
not copied. ``fixture_dir`` is not pointed at the repository and
``SDF_FIXTURES_ROOT`` is not widened.

The agent is an already installed, already logged-in ``codex`` or ``claude``
CLI. The login file is only stat-ed. It is not read and not copied. The CLI
runs under Landlock (ADR 0004): write access is the snapshot workspace.
This command refuses to start the CLI when that confinement is unavailable,
and it never falls back to the fake adapter. ``--dangerously-skip-permissions``
is not used (ADR 0008). Headless kinds (opencode, pi, qwen, goose) are not
started here.

Checks come from the snapshot's ``sdf-checks.json`` when it has one, otherwise
from the pytest files in the snapshot. They are frozen before the agent runs
and executed by the existing evaluator. A Task succeeds only on that
verification Evidence. The CLI's own exit status and stdout are not a pass.

Linux x86_64 with Landlock is required. Elsewhere the command exits before
an agent starts. Network egress is not locked by Landlock; that lock exists
on the E2B path, not here. A CLI that must write its own config directory
fails closed, because that directory is not a writable root and its files
are not copied in.
"""

from __future__ import annotations

import argparse
import ctypes
import getpass
import hashlib
import json
import os
import platform
import shutil
import sys
from collections.abc import Sequence
from dataclasses import dataclass
from pathlib import Path

from sqlalchemy import select

from .adapter import AdapterResult
from .checks import FIXTURE_MANIFEST, CheckConfigError, CheckRegistry, validate_argv
from .containment import ContainmentUnavailable
from .db import AttemptRow, EvidenceRow, GraphNodeRow, TaskRow, make_session_factory
from .execution import ExecutionService
from .local_state import (
    LocalStateError,
    _ensure_private_dir,
    data_dir,
    ensure_local_secrets,
    hermetic,
    is_local_default_url,
    resolve_database_url,
)
from .model import utcnow
from .sandbox import FixtureSandbox, ProcessResult

HOST_AGENTS = ("codex", "claude")
HEADLESS_AGENTS = ("opencode", "pi", "qwen", "goose")
DEFAULT_INSTRUCTION = (
    "Make this repository satisfy its existing project checks. "
    "Do not modify sdf-checks.json or the existing test files those checks name."
)
TURN_TIMEOUT_S = 420
MAX_INSTRUCTION_CHARS = 16_000
_LANDLOCK_CREATE_RULESET = 444
_LANDLOCK_FAILURES = (
    "landlock failed",
    "landlock is not mapped",
    "landlock unavailable",
    "sdf_landlock_root is missing",
    "landlock_create_ruleset",
)
_SKIP_DIRS = frozenset({
    ".git", ".hg", ".svn", ".venv", "venv", "node_modules", "__pycache__",
    ".tox", ".mypy_cache", ".pytest_cache", ".ruff_cache", ".codex", ".claude",
    ".pi", ".qwen", ".goose", "dist", "build",
})
_SKIP_FILES = frozenset({".netrc", "id_rsa", "id_ed25519", "id_ecdsa"})


class VerifyError(RuntimeError):
    """The user command cannot start. No agent ran, and nothing is a pass."""


@dataclass(frozen=True)
class VerifyResult:
    task_id: str
    attempt_id: str
    task_status: str
    attempt_status: str
    evidence_mode: str
    replayed: bool
    agent: str
    lines: tuple[str, ...]


def linux_x86_64() -> bool:
    machine = platform.machine().lower()
    return sys.platform == "linux" and machine in {"x86_64", "amd64"}


def landlock_available() -> bool:
    """True when this Linux x86_64 host can create a Landlock ruleset."""

    if not linux_x86_64():
        return False
    try:
        libc = ctypes.CDLL(None, use_errno=True)
        syscall = libc.syscall
        syscall.restype = ctypes.c_long
        version = syscall(_LANDLOCK_CREATE_RULESET, None, 0, 1)
    except (OSError, AttributeError):
        return False
    return version >= 1


def platform_supported() -> bool:
    return linux_x86_64() and landlock_available()


def platform_refusal() -> str | None:
    if platform_supported():
        return None
    host = f"{platform.system()} {platform.machine()}"
    if not linux_x86_64():
        return (
            f"This command needs Linux x86_64 with Landlock so the agent stays confined "
            f"and project checks can pass (ADR 0039). This host is {host}. No agent was started."
        )
    return (
        "This command needs Linux x86_64 with Landlock. This kernel does not expose Landlock. "
        "No agent was started."
    )


def login_path(agent: str) -> Path:
    home = Path.home()
    if agent == "codex":
        return home / ".codex" / "auth.json"
    if agent == "claude":
        return home / ".claude" / ".credentials.json"
    raise VerifyError(f"{agent} is not a host CLI this command runs")


def login_present(agent: str) -> bool:
    """True when the CLI's login file is a regular file. The file is not opened."""

    path = login_path(agent)
    return path.is_file() and not path.is_symlink()


def select_host_agent(requested: str | None) -> str:
    if requested is not None:
        requested = requested.strip().lower()
        if requested in HEADLESS_AGENTS:
            raise VerifyError(
                f"{requested} is not started by this command. It needs an API key and base URL "
                "inside an E2B sandbox (docs/live-full-loop.md). This command does not start a fake agent."
            )
        if requested not in HOST_AGENTS:
            raise VerifyError(
                f"unknown agent {requested!r}. This command runs codex or claude, already logged in. "
                "It does not start a fake agent."
            )
        _require_ready(requested)
        return requested
    ready = [agent for agent in HOST_AGENTS if shutil.which(agent) and login_present(agent)]
    if len(ready) == 1:
        return ready[0]
    if len(ready) > 1:
        raise VerifyError(
            "Both codex and claude are installed and logged in. Re-run with --agent codex or "
            "--agent claude. There is no credential-mode menu, and this command does not start a fake agent."
        )
    raise VerifyError(
        "No supported host agent is logged in. Install codex (login file ~/.codex/auth.json) or "
        "claude (login file ~/.claude/.credentials.json), log in with that CLI, and re-run. "
        "SDF does not read or copy those files, and it does not start a fake agent."
    )


def _require_ready(agent: str) -> None:
    if shutil.which(agent) is None:
        raise VerifyError(f"{agent} is not on PATH. This command does not start a fake agent.")
    if not login_present(agent):
        path = login_path(agent)
        raise VerifyError(
            f"{agent} has no login file at {path}. Log in with the {agent} CLI first. "
            "SDF does not read or copy that file, and it does not start a fake agent."
        )


def _secret_file(name: str) -> bool:
    if name in _SKIP_FILES:
        return True
    if name.startswith(".env") and name not in {".env.example", ".env.sample"}:
        return True
    return name.endswith((".pem", ".key"))


def _inside(path: Path, root: Path) -> bool:
    try:
        path.resolve().relative_to(root.resolve())
    except (OSError, ValueError):
        return False
    return True


def snapshot_repository(source: Path) -> tuple[Path, tuple[str, ...]]:
    """Copy ``source`` into the private snapshot tree. Returns (tree, skipped)."""

    source = source.expanduser().resolve()
    if not source.is_dir() or source.is_symlink():
        raise VerifyError(f"{source} is not a directory")
    store = data_dir()
    _ensure_private_dir(store)
    if _inside(source, store) or _inside(store, source):
        raise VerifyError("refusing to snapshot the SDF data directory or a tree that contains it")
    staging = store / "snapshots" / f".staging-{os.getpid()}"
    if staging.exists():
        shutil.rmtree(staging)
    tree = staging / "tree"
    tree.mkdir(parents=True, exist_ok=True)
    skipped: list[str] = []
    try:
        for dirpath, dirnames, filenames in os.walk(source, followlinks=False):
            current = Path(dirpath)
            kept: list[str] = []
            for name in list(dirnames):
                child = current / name
                if child.is_symlink():
                    raise VerifyError(f"refusing to follow symlink {child}")
                if name in _SKIP_DIRS or _inside(child, store):
                    if name in _SKIP_DIRS:
                        skipped.append(child.relative_to(source).as_posix() + "/")
                    continue
                kept.append(name)
            dirnames[:] = kept
            for name in filenames:
                file = current / name
                relative = file.relative_to(source).as_posix()
                if file.is_symlink():
                    raise VerifyError(f"refusing to copy symlink {file}")
                if _secret_file(name):
                    skipped.append(relative)
                    continue
                if not file.is_file():
                    continue
                target = tree / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(file.read_bytes())
                if file.stat().st_mode & 0o100:
                    target.chmod(0o700)
        digest = _tree_digest(tree)
    except Exception:
        shutil.rmtree(staging, ignore_errors=True)
        raise
    final = store / "snapshots" / digest / "tree"
    if final.exists():
        shutil.rmtree(staging)
    else:
        final.parent.mkdir(parents=True, exist_ok=True)
        os.rename(tree, final)
        shutil.rmtree(staging, ignore_errors=True)
    return final, tuple(skipped)


def _tree_digest(root: Path) -> str:
    hasher = hashlib.sha256()
    for path in sorted(item for item in root.rglob("*") if item.is_file() and not item.is_symlink()):
        relative = path.relative_to(root).as_posix()
        hasher.update(relative.encode("utf-8"))
        hasher.update(b"\0")
        hasher.update(path.read_bytes())
        hasher.update(b"\0")
    return hasher.hexdigest()


def freeze_checks(snapshot: Path) -> tuple[list[str], dict[str, list[list[str]]], dict]:
    """Checks frozen from the snapshot, before any agent runs."""

    manifest = snapshot / FIXTURE_MANIFEST
    if manifest.exists() or (snapshot / ".sdf-held-out").exists():
        try:
            registry = CheckRegistry.load(snapshot)
        except CheckConfigError as exc:
            raise VerifyError(f"project checks are not usable: {exc}") from exc
        if not registry.ids():
            raise VerifyError(
                "sdf-checks.json defines no checks. This command does not invent a pass."
            )
        criteria = registry.ids()
        try:
            resolved = registry.resolve({check_id: [check_id] for check_id in criteria})
        except CheckConfigError as exc:
            raise VerifyError(f"project checks are not usable: {exc}") from exc
        return criteria, resolved, registry.oracle_policies()
    files = _pytest_files(snapshot)
    if not files:
        raise VerifyError(
            "This repository has no sdf-checks.json and no pytest files (test_*.py or *_test.py). "
            "An agent claim is not a result, so no agent was started."
        )
    argv = ["pytest", "-q", *files]
    try:
        validate_argv(argv, where="project checks")
    except CheckConfigError as exc:
        raise VerifyError(
            f"project checks are not usable: {exc}. Name the files in sdf-checks.json."
        ) from exc
    return ["project-tests"], {"project-tests": [argv]}, {}


def _pytest_files(snapshot: Path) -> list[str]:
    found: list[str] = []
    for path in snapshot.rglob("*.py"):
        if not path.is_file() or path.is_symlink():
            continue
        relative = path.relative_to(snapshot)
        if any(part in _SKIP_DIRS for part in relative.parts):
            continue
        name = path.name
        if name == "conftest.py":
            continue
        if name.startswith("test_") or name.endswith("_test.py"):
            found.append(relative.as_posix())
    return sorted(found)


def build_argv(agent: str, workspace: Path, instructions: str) -> list[str]:
    if not instructions or len(instructions) > MAX_INSTRUCTION_CHARS:
        raise VerifyError(f"the instruction must be 1..{MAX_INSTRUCTION_CHARS} characters")
    if instructions.startswith("-") or "\x00" in instructions:
        raise VerifyError("the instruction must not start with '-' or contain NUL")
    from .herdr_runtime import _agent_start_args

    extra = _agent_start_args(
        agent, workspace, allow_dangerous_permissions=False, trust_project=False,
    )
    if "--dangerously-skip-permissions" in extra:
        raise VerifyError("refusing to launch the host CLI with --dangerously-skip-permissions")
    binary = shutil.which(agent)
    if binary is None:
        raise VerifyError(f"{agent} is not on PATH. This command does not start a fake agent.")
    if agent == "codex":
        return [binary, "exec", "--skip-git-repo-check", *extra, instructions]
    if agent == "claude":
        return [binary, "-p", instructions, *extra]
    raise VerifyError(f"{agent} is not a host CLI this command runs")


def _landlock_failed(result: ProcessResult) -> bool:
    text = f"{result.stderr}\n{result.stdout}".lower()
    return any(marker in text for marker in _LANDLOCK_FAILURES)


def run_confined(argv: Sequence[str], *, workspace: Path, timeout_s: float = TURN_TIMEOUT_S) -> ProcessResult:
    """Run ``argv`` with Landlock write access limited to ``workspace``.

    Refuses instead of starting an unconfined process. The login file is not
    copied. ``HOME`` stays the user's home so the CLI can open its own login
    file; credential-shaped environment variables are still dropped.
    """

    if not platform_supported():
        raise ContainmentUnavailable(
            "refusing to run the agent without Linux x86_64 Landlock; that would be unsandboxed"
        )
    if not argv or any("\x00" in part for part in argv):
        raise VerifyError("refusing to run an empty command")
    sandbox = FixtureSandbox(workspace, timeout_seconds=timeout_s, max_output_bytes=1024 * 1024)
    forwarded: dict[str, str] = {"HOME": str(Path.home())}
    for key in ("USER", "LOGNAME", "LANG", "LC_ALL", "TERM"):
        value = os.environ.get(key)
        if value:
            forwarded[key] = value
    # ``cwd`` is relative to the sandbox root. The root is the workspace, so
    # "." is that directory; an absolute cwd is refused by FixtureSandbox.
    return sandbox.run(list(argv), cwd=".", env=forwarded, timeout_seconds=timeout_s)


class LocalCliAdapter:
    """One host CLI turn inside the Attempt workspace. Not the fake adapter."""

    evidence_mode = "live"
    live_after_sandbox = True
    provider = "local-landlock"

    def __init__(self, db, agent: str, *, timeout_s: float = TURN_TIMEOUT_S) -> None:
        self.db = db
        self.agent = agent
        self.timeout_s = timeout_s

    def run(self, *, attempt_id: str, workspace: Path, instructions: str) -> AdapterResult:
        before = _file_bytes(workspace)
        argv = build_argv(self.agent, workspace, instructions)
        result = run_confined(argv, workspace=workspace, timeout_s=self.timeout_s)
        if _landlock_failed(result):
            raise ContainmentUnavailable(
                "Landlock did not confine the agent. The attempt was not marked live and no pass was recorded."
            )
        self._promote(attempt_id)
        after = _file_bytes(workspace)
        changed = tuple(sorted(name for name in set(before) | set(after) if before.get(name) != after.get(name)))
        exit_code = 124 if result.timed_out else (result.returncode if result.returncode is not None else 1)
        completed = exit_code == 0 and not result.timed_out
        return AdapterResult(
            attempt_id=attempt_id,
            status="completed" if completed else "failed",
            changed_files=changed,
            stdout=result.stdout,
            stderr=result.stderr,
            exit_code=exit_code,
        )

    def _promote(self, attempt_id: str) -> None:
        from .db import AttemptRow

        attempt = self.db.get(AttemptRow, attempt_id)
        if attempt is None:
            return
        attempt.evidence_mode = self.evidence_mode
        attempt.provider = self.provider
        self.db.commit()


def _file_bytes(root: Path) -> dict[str, bytes]:
    found: dict[str, bytes] = {}
    if not root.exists():
        return found
    for path in root.rglob("*"):
        if path.is_symlink() or not path.is_file():
            continue
        found[path.relative_to(root).as_posix()] = path.read_bytes()
    return found


def _principal() -> str:
    try:
        user = getpass.getuser()
    except Exception:  # noqa: BLE001
        user = "user"
    return f"local-{user}"[:120]


def run_verification(
    repository: Path | str,
    instruction: str | None = None,
    *,
    agent: str | None = None,
    runner=None,
) -> VerifyResult:
    """Snapshot ``repository``, run the host agent, and evaluate frozen checks.

    ``runner`` is a test seam. The command-line entry point does not pass it
    and there is no environment variable that selects a fake runner.
    """

    refusal = platform_refusal()
    if refusal is not None:
        raise VerifyError(refusal)
    if runner is None:
        selected = select_host_agent(agent)
    else:
        selected = (agent or "codex").strip().lower()
        if selected not in HOST_AGENTS:
            raise VerifyError("the test runner still names codex or claude")
    text = DEFAULT_INSTRUCTION if instruction is None else instruction
    snapshot, skipped = snapshot_repository(Path(repository))
    criteria, checks, policies = freeze_checks(snapshot)
    if skipped:
        print(
            "skipped (not copied into the snapshot): " + ", ".join(skipped[:20]),
            file=sys.stderr,
        )
    url = resolve_database_url(create=not hermetic())
    if is_local_default_url(url):
        ensure_local_secrets()
    if not url or ":memory:" in url:
        raise VerifyError(
            "No database is configured. Unset SDF_TEST_HERMETIC for the local file, "
            "or set SDF_DATABASE_URL. This command does not use an in-memory database."
        )
    tree_id = snapshot.parent.name
    material = json.dumps(
        {"tree": tree_id, "agent": selected, "instruction": text, "checks": checks},
        sort_keys=True,
    )
    digest = hashlib.sha256(material.encode("utf-8")).hexdigest()
    task_id = "TASK-LOCAL-" + digest[:16]
    dispatch_key = "local-" + digest[:40]
    session = make_session_factory(url)()
    try:
        task = session.get(TaskRow, task_id)
        if task is None:
            session.add(GraphNodeRow(
                id=task_id, kind="task", title=f"Verify {Path(repository).name}"[:500],
                source="local", owner="sdf", confidence=1.0, created_at=utcnow(),
            ))
            task = TaskRow(
                id=task_id, title=f"Verify {Path(repository).name}"[:500], status="created",
                idempotency_key="local-" + digest[:32], acceptance_criteria=criteria,
                created_at=utcnow(),
            )
            session.add(task)
            session.commit()
        existing = session.scalar(select(AttemptRow).where(AttemptRow.dispatch_key == dispatch_key))
        if existing is not None and (existing.principal != _principal() or existing.agent != selected):
            owner = existing.principal or "an unknown principal"
            raise VerifyError(
                f"verification key already belongs to {owner}; "
                "refusing to replay it as this command's result"
            )
        replayed = existing is not None
        work = data_dir() / "workspaces"
        artifacts = data_dir() / "artifacts"
        adapter = runner if runner is not None else LocalCliAdapter(session, selected)
        service = ExecutionService(session, workspace_root=work, artifact_root=artifacts, adapter=adapter)
        attempt = service.run(
            task_id=task_id,
            dispatch_key=dispatch_key,
            fixture=snapshot,
            instructions=text,
            commands=[],
            criterion_checks=checks,
            oracle_policies=policies,
            agent=selected,
            principal=_principal(),
        )
        session.refresh(attempt)
        task = session.get(TaskRow, task_id)
        lines = _report_lines(session, attempt, task, replayed=replayed)
        return VerifyResult(
            task_id=task_id,
            attempt_id=attempt.id,
            task_status=task.status if task is not None else "failed",
            attempt_status=attempt.status,
            evidence_mode=attempt.evidence_mode,
            replayed=replayed,
            agent=selected,
            lines=lines,
        )
    finally:
        session.close()


def _report_lines(session, attempt, task, *, replayed: bool) -> tuple[str, ...]:
    lines = [
        f"agent: {attempt.agent}",
        f"task: {attempt.task_id}",
        f"attempt: {attempt.id}",
        f"task_status: {task.status if task is not None else 'missing'}",
        f"attempt_status: {attempt.status}",
        f"evidence_mode: {attempt.evidence_mode}",
        f"provider: {attempt.provider or ''}",
        f"replayed: {'yes' if replayed else 'no'}",
    ]
    rows = session.scalars(select(EvidenceRow).where(EvidenceRow.attempt_id == attempt.id)).all()
    verifications = [row for row in rows if row.evidence_class == "verification"]
    if not verifications:
        lines.append("verification: not run")
        return tuple(lines)
    for row in verifications:
        lines.append(
            "verification: "
            f"criterion={row.criterion} status={row.status} exit={row.exit_code} "
            f"verifier={row.verifier_identity} command={row.command}"
        )
    return tuple(lines)


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="python -m sdf_core.verify",
        description="Verify your own repository with your own logged-in agent on this computer.",
    )
    parser.add_argument("repository", help="path to your repository")
    parser.add_argument("instruction", nargs="?", default=None, help="optional instruction; must not start with -")
    parser.add_argument("--agent", choices=HOST_AGENTS, default=None, help="only when both codex and claude are logged in")
    args = parser.parse_args(list(argv) if argv is not None else None)
    try:
        result = run_verification(args.repository, args.instruction, agent=args.agent)
    except (VerifyError, LocalStateError, ContainmentUnavailable) as exc:
        print(str(exc), file=sys.stderr)
        return 2
    except Exception as exc:  # noqa: BLE001 - the command must not look like a pass
        print(f"verification failed: {type(exc).__name__}: {exc}", file=sys.stderr)
        return 1
    print("\n".join(result.lines))
    return 0 if result.task_status == "succeeded" else 1


if __name__ == "__main__":
    raise SystemExit(main())
