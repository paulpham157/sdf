"""The one user path: own repository, own CLI, confined, real checks."""

from __future__ import annotations

import hashlib
import json
import os
import sys
import threading
from pathlib import Path

import pytest

from sdf_core.adapter import AdapterResult
from sdf_core.containment import ContainmentUnavailable
from sdf_core.fixtures import FixtureError, resolve_fixture
from sdf_core import verify

REPO = Path(__file__).resolve().parents[1]


def _isolate(tmp_path, monkeypatch) -> Path:
    xdg = tmp_path / "xdg"
    home = tmp_path / "home"
    home.mkdir()
    monkeypatch.setenv("XDG_DATA_HOME", str(xdg))
    monkeypatch.setenv("HOME", str(home))
    monkeypatch.setenv("SDF_DATABASE_URL", f"sqlite+pysqlite:///{tmp_path / 'state.sqlite'}")
    return home


def _codex(home: Path, bindir: Path, body: str) -> None:
    bindir.mkdir()
    binary = bindir / "codex"
    binary.write_text(body, encoding="utf-8")
    binary.chmod(0o755)
    auth = home / ".codex" / "auth.json"
    auth.parent.mkdir(parents=True)
    auth.write_text("login-marker-not-read\n", encoding="utf-8")


def _repo(tmp_path: Path, *, tests: bool = True) -> Path:
    repo = tmp_path / "repo"
    if tests:
        (repo / "tests").mkdir(parents=True)
        (repo / "tests" / "test_app.py").write_text(
            "def test_ok():\n    assert 1 + 1 == 2\n",
            encoding="utf-8",
        )
    else:
        repo.mkdir()
    return repo


def test_readme_documents_only_the_one_command():
    text = (REPO / "README.md").read_text(encoding="utf-8")
    assert "uv run python -m sdf_core.verify /absolute/path/to/your/repository" in text
    assert "git clone https://github.com/paulpham157/sdf.git" in text
    assert "uv sync" in text
    for forbidden in (
        "scripts/demo.py",
        "demo-evidence",
        "SDF_AUTH_DEV_INSECURE",
        "uvicorn",
        "SDF_RUN_ADAPTER",
        "offline-evidence",
    ):
        assert forbidden not in text
    assert "project.scripts" not in (REPO / "pyproject.toml").read_text(encoding="utf-8")


def test_user_repository_is_not_a_fixture_dir(tmp_path, monkeypatch):
    root = tmp_path / "fixtures"
    root.mkdir()
    repo = tmp_path / "repo"
    repo.mkdir()
    monkeypatch.setenv("SDF_FIXTURES_ROOT", str(root))
    before = os.environ["SDF_FIXTURES_ROOT"]
    with pytest.raises(FixtureError):
        resolve_fixture(str(repo))
    assert os.environ["SDF_FIXTURES_ROOT"] == before


def test_main_does_not_fall_back_to_fake(tmp_path, monkeypatch, capsys):
    home = _isolate(tmp_path, monkeypatch)
    monkeypatch.setenv("SDF_RUN_ADAPTER", "fake")
    monkeypatch.setenv("SDF_AUTH_DEV_INSECURE", "1")
    monkeypatch.setattr(verify, "platform_refusal", lambda: None)
    repo = _repo(tmp_path)
    assert verify.main([str(repo)]) == 2
    err = capsys.readouterr().err
    assert "fake agent" in err
    assert not (tmp_path / "xdg" / "sdf" / "sdf.sqlite").exists()
    assert not (home / ".codex").exists()


def test_missing_checks_do_not_start_the_agent(tmp_path, monkeypatch):
    if not verify.platform_supported():
        pytest.skip("Linux x86_64 Landlock is required")
    home = _isolate(tmp_path, monkeypatch)
    bindir = tmp_path / "bin"
    _codex(home, bindir, "#!/bin/sh\nprintf ran >> \"$PWD/agent-runs\"\n")
    monkeypatch.setenv("PATH", str(bindir))
    repo = _repo(tmp_path, tests=False)
    (repo / "README.md").write_text("no checks\n", encoding="utf-8")
    with pytest.raises(verify.VerifyError, match="no sdf-checks.json"):
        verify.run_verification(repo)
    assert list((tmp_path / "xdg").rglob("agent-runs")) == []


def test_snapshot_skips_secrets_and_refuses_symlinks(tmp_path, monkeypatch):
    monkeypatch.setenv("XDG_DATA_HOME", str(tmp_path / "xdg"))
    repo = _repo(tmp_path)
    (repo / ".env").write_text("SECRET=do-not-copy\n", encoding="utf-8")
    (repo / ".git").mkdir()
    (repo / ".git" / "config").write_text("git\n", encoding="utf-8")
    (repo / "link.py").symlink_to(repo / "tests" / "test_app.py")
    with pytest.raises(verify.VerifyError, match="symlink"):
        verify.snapshot_repository(repo)
    (repo / "link.py").unlink()
    tree, skipped = verify.snapshot_repository(repo)
    assert (tree / "tests" / "test_app.py").is_file()
    assert not (tree / ".env").exists()
    assert not (tree / ".git").exists()
    assert ".env" in skipped
    copied = "\n".join(path.read_text(encoding="utf-8") for path in tree.rglob("*") if path.is_file())
    assert "do-not-copy" not in copied
    criteria, checks, _policies = verify.freeze_checks(tree)
    assert criteria == ["project-tests"]
    assert checks["project-tests"][0][0] == "pytest"
    assert "tests/test_app.py" in checks["project-tests"][0]


def test_selects_one_logged_in_cli_without_reading_it(tmp_path, monkeypatch):
    home = tmp_path / "home"
    home.mkdir()
    monkeypatch.setenv("HOME", str(home))
    bindir = tmp_path / "bin"
    _codex(home, bindir, "#!/bin/sh\nexit 0\n")
    monkeypatch.setenv("PATH", str(bindir))
    auth = home / ".codex" / "auth.json"
    assert verify.select_host_agent(None) == "codex"
    assert auth.read_text(encoding="utf-8") == "login-marker-not-read\n"
    auth.unlink()
    auth.symlink_to(tmp_path / "elsewhere")
    with pytest.raises(verify.VerifyError, match="fake agent"):
        verify.select_host_agent("codex")
    with pytest.raises(verify.VerifyError, match="opencode"):
        verify.select_host_agent("opencode")


def test_both_logins_need_an_explicit_agent(tmp_path, monkeypatch):
    home = tmp_path / "home"
    home.mkdir()
    monkeypatch.setenv("HOME", str(home))
    bindir = tmp_path / "bin"
    _codex(home, bindir, "#!/bin/sh\nexit 0\n")
    claude = bindir / "claude"
    claude.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
    claude.chmod(0o755)
    marker = home / ".claude" / ".credentials.json"
    marker.parent.mkdir()
    marker.write_text("marker\n", encoding="utf-8")
    monkeypatch.setenv("PATH", str(bindir))
    with pytest.raises(verify.VerifyError, match="--agent"):
        verify.select_host_agent(None)
    assert verify.select_host_agent("claude") == "claude"


def test_argv_does_not_skip_permissions(tmp_path, monkeypatch):
    if not verify.platform_supported():
        pytest.skip("Linux x86_64 Landlock is required")
    home = tmp_path / "home"
    home.mkdir()
    monkeypatch.setenv("HOME", str(home))
    bindir = tmp_path / "bin"
    _codex(home, bindir, "#!/bin/sh\nexit 0\n")
    monkeypatch.setenv("PATH", str(bindir))
    argv = verify.build_argv("codex", tmp_path / "workspace", "Fix the tests")
    assert "--dangerously-skip-permissions" not in argv
    assert argv[1:3] == ["exec", "--skip-git-repo-check"]
    assert "features.multi_agent=false" in argv


def test_confinement_failure_does_not_run_unconfined(tmp_path, monkeypatch):
    workspace = tmp_path / "ws"
    workspace.mkdir()
    marker = tmp_path / "escaped"
    monkeypatch.setattr(verify, "platform_supported", lambda: False)
    with pytest.raises(ContainmentUnavailable):
        verify.run_confined(
            [sys.executable, "-c", f"open({str(marker)!r}, 'w').write('x')"],
            workspace=workspace,
        )
    assert not marker.exists()


@pytest.mark.skipif(not verify.platform_supported(), reason="Linux x86_64 Landlock is required")
def test_landlock_blocks_a_write_outside_the_workspace(tmp_path):
    workspace = tmp_path / "ws"
    workspace.mkdir()
    outside = tmp_path / "outside"
    code = (
        "from pathlib import Path\n"
        "Path('inside.txt').write_text('in')\n"
        f"Path({str(outside)!r}).write_text('out')\n"
    )
    result = verify.run_confined([sys.executable, "-c", code], workspace=workspace)
    assert (workspace / "inside.txt").read_text(encoding="utf-8") == "in"
    assert not outside.exists()
    assert result.returncode not in (0, None)


@pytest.mark.skipif(not verify.platform_supported(), reason="Linux x86_64 Landlock is required")
def test_own_repo_checks_are_evaluator_evidence_and_replay(tmp_path, monkeypatch):
    home = _isolate(tmp_path, monkeypatch)
    bindir = tmp_path / "bin"
    _codex(
        home,
        bindir,
        "#!/bin/sh\nprintf 'ran\\n' >> \"$PWD/agent-runs\"\necho ALL TESTS PASS\nexit 0\n",
    )
    monkeypatch.setenv("PATH", str(bindir) + os.pathsep + os.environ.get("PATH", ""))
    monkeypatch.setenv("SDF_RUN_ADAPTER", "fake")
    monkeypatch.setenv("SDF_AUTH_DEV_INSECURE", "1")
    repo = _repo(tmp_path)
    (repo / ".env").write_text("SECRET=stay-out\n", encoding="utf-8")
    first = verify.run_verification(repo, "Make the checks pass")
    assert first.task_status == "succeeded", "\n".join(first.lines)
    assert first.evidence_mode == "live"
    assert first.replayed is False
    assert first.agent == "codex"
    verification = [line for line in first.lines if line.startswith("verification:")]
    assert verification
    assert all("verifier=sdf-evaluator" in line for line in verification)
    assert any("tests/test_app.py" in line and "pytest" in line for line in verification)
    assert all("ALL TESTS PASS" not in line for line in verification)
    runs = list((tmp_path / "xdg").rglob("agent-runs"))
    assert len(runs) == 1
    assert runs[0].read_text(encoding="utf-8").count("ran") == 1
    assert (home / ".codex" / "auth.json").read_text(encoding="utf-8") == "login-marker-not-read\n"
    snapshots = tmp_path / "xdg" / "sdf" / "snapshots"
    copied = "\n".join(
        path.read_text(encoding="utf-8")
        for path in snapshots.rglob("*")
        if path.is_file() and not path.is_symlink()
    )
    assert "stay-out" not in copied
    assert not list(snapshots.rglob(".env"))
    second = verify.run_verification(repo, "Make the checks pass")
    assert second.replayed is True
    assert second.attempt_id == first.attempt_id
    assert second.task_status == "succeeded"
    assert runs[0].read_text(encoding="utf-8").count("ran") == 1


class _CountingRunner:
    def __init__(self) -> None:
        self.calls = 0
        self.evidence_mode = "synthetic"
        self.live_after_sandbox = False
        self.provider = "test-runner"

    def run(self, *, attempt_id: str, workspace: Path, instructions: str) -> AdapterResult:
        self.calls += 1
        return AdapterResult(
            attempt_id=attempt_id, status="completed", changed_files=(),
            stdout="ran", stderr="", exit_code=0,
        )


def _local_ids(repo: Path, instruction: str, *, agent: str = "codex"):
    """The task id and dispatch key ``run_verification`` derives for this tree."""

    snapshot, _skipped = verify.snapshot_repository(repo)
    criteria, checks, _policies = verify.freeze_checks(snapshot)
    material = json.dumps(
        {"tree": snapshot.parent.name, "agent": agent, "instruction": instruction, "checks": checks},
        sort_keys=True,
    )
    digest = hashlib.sha256(material.encode("utf-8")).hexdigest()
    return snapshot, criteria, checks, "TASK-LOCAL-" + digest[:16], "local-" + digest[:40], "local-" + digest[:32]


def test_same_principal_replays_without_starting_the_runner_again(tmp_path, monkeypatch):
    _isolate(tmp_path, monkeypatch)
    monkeypatch.setattr(verify, "platform_refusal", lambda: None)
    repo = _repo(tmp_path)
    instruction = "Make the checks pass"
    runner = _CountingRunner()
    first = verify.run_verification(repo, instruction, agent="codex", runner=runner)
    assert first.task_status == "succeeded", "\n".join(first.lines)
    assert first.replayed is False
    assert runner.calls == 1
    second = verify.run_verification(repo, instruction, agent="codex", runner=runner)
    assert second.replayed is True
    assert second.attempt_id == first.attempt_id
    assert second.task_status == "succeeded"
    assert runner.calls == 1


def test_other_principal_preseeded_dispatch_is_not_replayed(tmp_path, monkeypatch):
    """Principal bob cannot plant a succeeded Attempt that this command replays.

    The key is predictable from the snapshot. ``begin`` returns any row that
    already holds it. A second principal's pass must not become this command's
    result, and the stand-in runner must not be skipped in bob's favor.
    """

    _isolate(tmp_path, monkeypatch)
    monkeypatch.setattr(verify, "platform_refusal", lambda: None)
    repo = _repo(tmp_path)
    instruction = "Make the checks pass"
    snapshot, criteria, checks, task_id, dispatch_key, idempotency_key = _local_ids(
        repo, instruction,
    )
    from sdf_core.adapter import FakeNativeAdapter
    from sdf_core.db import Base, GraphNodeRow, TaskRow, make_engine, make_session_factory
    from sdf_core.execution import ExecutionService
    from sdf_core.model import utcnow

    url = os.environ["SDF_DATABASE_URL"]
    Base.metadata.create_all(make_engine(url))
    session = make_session_factory(url)()
    try:
        session.add(GraphNodeRow(
            id=task_id, kind="task", title="preseed", source="api", owner="sdf",
            confidence=1.0, created_at=utcnow(),
        ))
        session.add(TaskRow(
            id=task_id, title="preseed", status="created", idempotency_key=idempotency_key,
            acceptance_criteria=criteria, created_at=utcnow(),
        ))
        session.commit()
        planted = ExecutionService(
            session,
            workspace_root=tmp_path / "bob-workspaces",
            artifact_root=tmp_path / "bob-artifacts",
            adapter=FakeNativeAdapter("notes.txt", "bob\n"),
        ).run(
            task_id=task_id,
            dispatch_key=dispatch_key,
            fixture=snapshot,
            instructions="bob's unrelated run",
            commands=[],
            criterion_checks=checks,
            principal="bob",
        )
        session.refresh(planted)
        task = session.get(TaskRow, task_id)
        assert planted.principal == "bob"
        assert planted.agent == "native"
        assert task is not None and task.status == "succeeded"
    finally:
        session.close()

    runner = _CountingRunner()
    with pytest.raises(verify.VerifyError, match="bob"):
        verify.run_verification(repo, instruction, agent="codex", runner=runner)
    assert runner.calls == 0


def test_same_principal_different_agent_is_not_replayed(tmp_path, monkeypatch):
    _isolate(tmp_path, monkeypatch)
    monkeypatch.setattr(verify, "platform_refusal", lambda: None)
    repo = _repo(tmp_path)
    instruction = "Make the checks pass"
    _plant_attempt(repo, instruction, principal=verify._principal(), agent="claude", status="succeeded")
    runner = _CountingRunner()
    with pytest.raises(verify.VerifyError, match="refusing to replay"):
        verify.run_verification(repo, instruction, agent="codex", runner=runner)
    assert runner.calls == 0


def test_same_principal_failed_attempt_replays_without_another_runner(tmp_path, monkeypatch):
    _isolate(tmp_path, monkeypatch)
    monkeypatch.setattr(verify, "platform_refusal", lambda: None)
    repo = _repo(tmp_path)
    instruction = "Make the checks pass"
    runner = _CountingRunner()

    class _FailOnce(_CountingRunner):
        def run(self, *, attempt_id: str, workspace: Path, instructions: str) -> AdapterResult:
            self.calls += 1
            return AdapterResult(
                attempt_id=attempt_id, status="failed", changed_files=(), stdout="", stderr="no", exit_code=1,
            )

    runner_fail = _FailOnce()
    first = verify.run_verification(repo, instruction, agent="codex", runner=runner_fail)
    assert first.replayed is False
    assert first.task_status == "failed"
    assert runner_fail.calls == 1
    second = verify.run_verification(repo, instruction, agent="codex", runner=runner)
    assert second.replayed is True
    assert second.attempt_id == first.attempt_id
    assert second.task_status == "failed"
    assert runner.calls == 0


def test_begin_sees_foreign_attempt_inserted_after_preflight(tmp_path, monkeypatch):
    """The preflight read missed the row. ``begin`` then finds it and must not present it."""

    _isolate(tmp_path, monkeypatch)
    monkeypatch.setattr(verify, "platform_refusal", lambda: None)
    repo = _repo(tmp_path)
    instruction = "Make the checks pass"
    from sdf_core.db import AttemptRow, make_session_factory
    from sdf_core.execution import ExecutionService
    from sdf_core.model import utcnow

    real_begin = ExecutionService.begin
    planted = {"done": False}

    def begin(self, **kwargs):
        if not planted["done"]:
            planted["done"] = True
            side = make_session_factory(os.environ["SDF_DATABASE_URL"])()
            try:
                side.add(AttemptRow(
                    id="ATTEMPT-FOREIGN-SELECT", task_id=kwargs["task_id"], status="succeeded",
                    dispatch_key=kwargs["dispatch_key"], agent="codex", principal="bob",
                    created_at=utcnow(), evidence_mode="synthetic", model_tier="basic", cost_usd=0.0,
                ))
                side.commit()
            finally:
                side.close()
        return real_begin(self, **kwargs)

    monkeypatch.setattr(ExecutionService, "begin", begin)
    runner = _CountingRunner()
    with pytest.raises(verify.VerifyError, match="bob"):
        verify.run_verification(repo, instruction, agent="codex", runner=runner)
    assert runner.calls == 0
    assert planted["done"] is True


def test_unique_dispatch_loser_does_not_present_the_other_principal(tmp_path, monkeypatch):
    """Both callers pass the preflight read. The unique-key loser must not adopt the winner."""

    _isolate(tmp_path, monkeypatch)
    monkeypatch.setattr(verify, "platform_refusal", lambda: None)
    repo = _repo(tmp_path)
    instruction = "Make the checks pass"
    snapshot, criteria, _checks, task_id, _dispatch_key, idempotency_key = _local_ids(repo, instruction)
    monkeypatch.setattr(verify, "snapshot_repository", lambda _source: (snapshot, ()))
    from sdf_core.db import AttemptRow, GraphNodeRow, TaskRow, make_session_factory
    from sdf_core.model import utcnow
    from sqlalchemy.orm import Session

    url = os.environ["SDF_DATABASE_URL"]
    session = make_session_factory(url)()
    try:
        session.add(GraphNodeRow(
            id=task_id, kind="task", title="shared", source="local", owner="sdf",
            confidence=1.0, created_at=utcnow(),
        ))
        session.add(TaskRow(
            id=task_id, title="shared", status="created", idempotency_key=idempotency_key,
            acceptance_criteria=criteria, created_at=utcnow(),
        ))
        session.commit()
    finally:
        session.close()

    barrier = threading.Barrier(2)
    identities = threading.local()
    original_flush = Session.flush

    def flush(self, objects=None):
        attempts = [obj for obj in self.new if isinstance(obj, AttemptRow)]
        if attempts and not getattr(identities, "gated", False):
            identities.gated = True
            barrier.wait(timeout=20)
        return original_flush(self, objects)

    monkeypatch.setattr(Session, "flush", flush)
    names = {"alice": "local-alice", "bob": "local-bob"}

    def principal():
        return names[identities.who]

    monkeypatch.setattr(verify, "_principal", principal)
    outcomes: dict[str, tuple] = {}

    def run_as(who: str) -> None:
        identities.who = who
        identities.gated = False
        runner = _CountingRunner()
        try:
            result = verify.run_verification(repo, instruction, agent="codex", runner=runner)
        except verify.VerifyError as exc:
            outcomes[who] = ("refused", str(exc), runner.calls)
        except Exception as exc:  # noqa: BLE001 - the test reports the real failure
            outcomes[who] = ("error", f"{type(exc).__name__}: {exc}", runner.calls)
        else:
            outcomes[who] = ("ok", result.attempt_id, runner.calls, result.task_status)

    threads = [threading.Thread(target=run_as, args=(who,)) for who in ("alice", "bob")]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join(timeout=60)
    assert all(not thread.is_alive() for thread in threads), outcomes
    assert set(outcomes) == {"alice", "bob"}, outcomes
    oks = [who for who, item in outcomes.items() if item[0] == "ok"]
    refused = [who for who, item in outcomes.items() if item[0] == "refused"]
    assert len(oks) == 1 and len(refused) == 1, outcomes
    winner, loser = oks[0], refused[0]
    assert outcomes[winner][2] == 1
    assert outcomes[loser][2] == 0
    assert names[winner] in outcomes[loser][1]
    side = make_session_factory(url)()
    try:
        row = side.get(AttemptRow, outcomes[winner][1])
        assert row is not None
        assert row.principal == names[winner]
        assert row.agent == "codex"
    finally:
        side.close()


def _plant_attempt(repo: Path, instruction: str, *, principal: str, agent: str, status: str) -> None:
    from sdf_core.db import AttemptRow, GraphNodeRow, TaskRow, make_session_factory
    from sdf_core.model import utcnow

    snapshot, criteria, _checks, task_id, dispatch_key, idempotency_key = _local_ids(repo, instruction)
    del snapshot
    session = make_session_factory(os.environ["SDF_DATABASE_URL"])()
    try:
        session.add(GraphNodeRow(
            id=task_id, kind="task", title="planted", source="local", owner="sdf",
            confidence=1.0, created_at=utcnow(),
        ))
        session.add(TaskRow(
            id=task_id, title="planted", status="created", idempotency_key=idempotency_key,
            acceptance_criteria=criteria, created_at=utcnow(),
        ))
        session.add(AttemptRow(
            id=f"ATTEMPT-PLANTED-{agent}", task_id=task_id, status=status, dispatch_key=dispatch_key,
            agent=agent, principal=principal, created_at=utcnow(), evidence_mode="synthetic",
            model_tier="basic", cost_usd=0.0,
        ))
        session.commit()
    finally:
        session.close()


def test_platform_refusal_starts_nothing(tmp_path, monkeypatch):
    _isolate(tmp_path, monkeypatch)
    monkeypatch.setattr(verify, "linux_x86_64", lambda: False)
    repo = _repo(tmp_path)
    with pytest.raises(verify.VerifyError, match="No agent was started"):
        verify.run_verification(repo)
    assert not (tmp_path / "xdg" / "sdf").exists()
