import json
import os
import stat
import subprocess
from pathlib import Path

import pytest

from sdf_core.containment import ContainmentUnavailable
from sdf_core.e2b_containment import E2BContainmentBackend
from sdf_core.evaluator import DeterministicEvaluator


def test_e2b_probe_requires_plugin_and_key(monkeypatch):
    monkeypatch.setattr("sdf_core.e2b_containment.shutil.which", lambda _: None)
    assert E2BContainmentBackend(environ={}).probe().available is False

    monkeypatch.setattr("sdf_core.e2b_containment.shutil.which", lambda _: "/bin/e2b-box")
    probe = E2BContainmentBackend(environ={}).probe()
    assert probe.available is False
    assert "E2B_API_KEY" in probe.detail


def test_e2b_run_syncs_execs_and_kills_without_pulling(tmp_path: Path, monkeypatch):
    monkeypatch.setattr("sdf_core.e2b_containment.shutil.which", lambda _: "/bin/e2b-box")
    calls = []

    def runner(command, cwd, timeout_ms, env):
        calls.append((tuple(command), cwd, timeout_ms, dict(env)))
        if command[1] == "exec":
            return json.dumps({"ok": True, "exitCode": 0, "stdout": "ok", "stderr": ""})
        return "{}"

    root = tmp_path / "workspace"
    cwd = root / "subdir"
    cwd.mkdir(parents=True)
    backend = E2BContainmentBackend(runner=runner, environ={"E2B_API_KEY": "<REDACTED>", "E2B_DOMAIN": "e2b.dev"})
    result = backend.run(
        ["python", "-c", "print('ok')"], root=root, cwd=cwd,
        env={"SDF_NETWORK": "denied", "SDF_SANDBOX_ROOT": "/tmp/host-workspace"}, timeout_seconds=2,
    )

    assert result.returncode == 0
    assert result.stdout == "ok"
    assert [call[0][1] for call in calls] == ["-t", "exec", "kill"]
    assert "pull" not in {part for call in calls for part in call[0]}
    assert "cd subdir" in calls[1][0][-1]
    assert "SDF_NETWORK=denied" in calls[1][0][-1]
    assert "SDF_SANDBOX_ROOT" not in calls[1][0][-1]
    assert "/tmp/host-workspace" not in calls[1][0][-1]
    assert all(call[3]["E2B_DOMAIN"] == "e2b.dev" for call in calls)


def test_e2b_allows_agent_network_egress_when_requested(tmp_path: Path, monkeypatch):
    monkeypatch.setattr("sdf_core.e2b_containment.shutil.which", lambda _: "/bin/e2b-box")
    calls = []

    def runner(command, *_):
        calls.append(tuple(command))
        return json.dumps({"ok": True, "exitCode": 0, "stdout": "network-enabled", "stderr": ""}) if command[1] == "exec" else "{}"

    backend = E2BContainmentBackend(runner=runner, environ={"E2B_API_KEY": "<REDACTED>"})
    result = backend.run(["node", "-e", "console.log('ok')"], root=tmp_path, cwd=tmp_path, allow_network=True)
    assert result.returncode == 0
    assert result.stdout == "network-enabled"
    assert backend.network_egress_allowed is True


def test_e2b_invalid_provider_result_is_fail_closed(tmp_path: Path, monkeypatch):
    monkeypatch.setattr("sdf_core.e2b_containment.shutil.which", lambda _: "/bin/e2b-box")
    calls = []

    def runner(command, *_):
        calls.append(tuple(command))
        return "{}" if command[1] != "exec" else json.dumps({"ok": False, "error": "gone"})

    backend = E2BContainmentBackend(runner=runner, environ={"E2B_API_KEY": "<REDACTED>"})
    with pytest.raises(ContainmentUnavailable, match="could not measure"):
        backend.run(["true"], root=tmp_path, cwd=tmp_path)
    assert [call[1] for call in calls] == ["-t", "exec", "kill"]


# A stand-in for herdr-e2b's e2b-box. ``pull`` writes a host file and fails
# the way the real plugin does on a non-git workspace. ``exec`` exits 1 when
# the remote command fails, with the measurement still on stdout.
_FAKE_E2B_BOX = """#!/usr/bin/env python3
import json, sys
from pathlib import Path

here = Path(__file__).resolve().parent
mode = (here / "mode").read_text(encoding="utf-8").strip() if (here / "mode").exists() else "pass"
with (here / "calls").open("a", encoding="utf-8") as handle:
    handle.write(" ".join(sys.argv[1:]) + "\\n")
args = sys.argv[1:]
if "pull" in args:
    (Path.cwd() / "pulled-back.txt").write_text("agent-wrote-this\\n", encoding="utf-8")
    sys.stderr.write("not a git repository; pass --force\\n")
    sys.exit(2)
if "exec" in args:
    if mode == "fail":
        json.dump({"ok": True, "exitCode": 1, "stdout": "", "stderr": "add(-10,-10) -> 0"}, sys.stdout)
        sys.exit(1)
    if mode == "malformed":
        sys.stdout.write("not-json")
        sys.exit(1)
    if mode == "not-ok":
        json.dump({"ok": False, "error": "gone"}, sys.stdout)
        sys.exit(1)
    json.dump({"ok": True, "exitCode": 0, "stdout": "ok", "stderr": ""}, sys.stdout)
    sys.exit(0)
sys.exit(0)
"""


def _install_fake_e2b_box(directory: Path, mode: str) -> Path:
    directory.mkdir(parents=True, exist_ok=True)
    script = directory / "e2b-box"
    script.write_text(_FAKE_E2B_BOX, encoding="utf-8")
    script.chmod(script.stat().st_mode | stat.S_IEXEC)
    (directory / "mode").write_text(mode, encoding="utf-8")
    (directory / "calls").write_text("", encoding="utf-8")
    return script


def _backend(script: Path) -> E2BContainmentBackend:
    return E2BContainmentBackend(
        executable=str(script),
        environ={"E2B_API_KEY": "<REDACTED>", "PATH": os.environ.get("PATH", "")},
    )


def _check(tmp_path: Path, script: Path, *, body: str = "def add(a, b):\n    return a + b\n"):
    workspace = tmp_path / f"workspace-{script.parent.name}"
    workspace.mkdir()
    (workspace / "calc.py").write_text(body, encoding="utf-8")
    assert not (workspace / ".git").exists()
    return DeterministicEvaluator(containment_backend=_backend(script), require_containment=True)._run_one(
        command=["python3", "-c", "from calc import add; assert add(2, 3) == 5"],
        workspace=workspace, attempt_id="A", index=1, criterion="add returns the sum", reference=None,
    ), workspace


def test_non_git_workspace_check_passes_without_a_pull(tmp_path: Path, monkeypatch):
    monkeypatch.setattr("sdf_core.e2b_containment.shutil.which", lambda cmd: cmd)
    script = _install_fake_e2b_box(tmp_path / "bin", "pass")
    evidence, workspace = _check(tmp_path, script)
    calls = (script.parent / "calls").read_text(encoding="utf-8")
    assert evidence.status == "PASS", (evidence.status, evidence.stderr)
    assert evidence.runner == "e2b-box"
    assert "pull" not in calls
    assert not (workspace / "pulled-back.txt").exists()
    assert "SDF_SANDBOX_ROOT" not in calls


def test_failing_contained_check_is_fail_not_inconclusive(tmp_path: Path, monkeypatch):
    monkeypatch.setattr("sdf_core.e2b_containment.shutil.which", lambda cmd: cmd)
    script = _install_fake_e2b_box(tmp_path / "bin", "fail")
    evidence, _workspace = _check(tmp_path, script, body="def add(a, b):\n    return a - b\n")
    assert evidence.status == "FAIL"
    assert evidence.exit_code == 1
    assert evidence.runner == "e2b-box"
    assert "add(-10,-10)" in (evidence.stderr or "")


def test_malformed_e2b_exec_output_is_inconclusive_and_names_the_backend(tmp_path: Path, monkeypatch):
    monkeypatch.setattr("sdf_core.e2b_containment.shutil.which", lambda cmd: cmd)
    script = _install_fake_e2b_box(tmp_path / "bin", "malformed")
    evidence, _workspace = _check(tmp_path, script)
    assert evidence.status == "INCONCLUSIVE"
    assert evidence.runner == "e2b-box"

    script = _install_fake_e2b_box(tmp_path / "bin-not-ok", "not-ok")
    evidence, _workspace = _check(tmp_path, script)
    assert evidence.status == "INCONCLUSIVE"
    assert evidence.runner == "e2b-box"
