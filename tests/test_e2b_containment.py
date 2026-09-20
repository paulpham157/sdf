import json
import subprocess
from pathlib import Path

import pytest

from sdf_core.containment import ContainmentUnavailable
from sdf_core.e2b_containment import E2BContainmentBackend


def test_e2b_probe_requires_plugin_and_key(monkeypatch):
    monkeypatch.setattr("sdf_core.e2b_containment.shutil.which", lambda _: None)
    assert E2BContainmentBackend(environ={}).probe().available is False

    monkeypatch.setattr("sdf_core.e2b_containment.shutil.which", lambda _: "/bin/e2b-box")
    probe = E2BContainmentBackend(environ={}).probe()
    assert probe.available is False
    assert "E2B_API_KEY" in probe.detail


def test_e2b_run_syncs_execs_pulls_and_kills(tmp_path: Path, monkeypatch):
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
    backend = E2BContainmentBackend(runner=runner, environ={"E2B_API_KEY": "<REDACTED>"})
    result = backend.run(["python", "-c", "print('ok')"], root=root, cwd=cwd, env={"SDF_NETWORK": "denied"}, timeout_seconds=2)

    assert result.returncode == 0
    assert result.stdout == "ok"
    assert [call[0][1] for call in calls] == ["-t", "exec", "pull", "kill"]
    assert "cd subdir" in calls[1][0][-1]
    assert "SDF_NETWORK=denied" in calls[1][0][-1]


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
    assert [call[1] for call in calls] == ["-t", "exec", "pull", "kill"]
