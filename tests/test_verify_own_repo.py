"""The one user path: own repository, own CLI, confined, real checks."""

from __future__ import annotations

import os
import sys
from pathlib import Path

import pytest

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


def test_platform_refusal_starts_nothing(tmp_path, monkeypatch):
    _isolate(tmp_path, monkeypatch)
    monkeypatch.setattr(verify, "linux_x86_64", lambda: False)
    repo = _repo(tmp_path)
    with pytest.raises(verify.VerifyError, match="No agent was started"):
        verify.run_verification(repo)
    assert not (tmp_path / "xdg" / "sdf").exists()
