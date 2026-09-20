from pathlib import Path
import subprocess

from sdf_core.containment import MacOSSandboxBackend
from sdf_core.sandbox import FixtureSandbox


def test_macos_backend_probe_and_profile_are_explicit_about_runtime_proof(tmp_path: Path, monkeypatch):
    monkeypatch.setattr("sdf_core.containment.shutil.which", lambda _: "/usr/bin/sandbox-exec")
    backend = MacOSSandboxBackend()
    probe = backend.probe()
    assert probe.available is True
    profile = backend.profile(tmp_path, allow_network=False)
    assert f'(allow file-write* (subpath "{tmp_path.resolve()}"))' in profile
    assert "(deny network*)" in profile
    assert "disposable fixture root" not in profile


def test_macos_backend_wraps_command_and_cleans_profile(tmp_path: Path, monkeypatch):
    monkeypatch.setattr("sdf_core.containment.shutil.which", lambda _: "/usr/bin/sandbox-exec")
    observed = {}

    def runner(command, cwd, env, timeout):
        observed.update(command=tuple(command), cwd=cwd, timeout=timeout)
        return subprocess.CompletedProcess(command, 0, "ok", "")

    result = MacOSSandboxBackend(runner=runner).run(
        ["python", "-c", "print('ok')"], root=tmp_path, cwd=tmp_path, timeout_seconds=2.0,
    )
    assert result.returncode == 0
    assert observed["command"][:3] == ("sandbox-exec", "-f", observed["command"][2])
    assert not Path(observed["command"][2]).exists()


def test_macos_backend_smoke_reports_disposable_write_boundary(tmp_path: Path, monkeypatch):
    monkeypatch.setattr("sdf_core.containment.shutil.which", lambda _: "/usr/bin/sandbox-exec")

    def runner(command, cwd, env, timeout):
        (cwd / ".sdf-containment-inside").write_text("inside")
        return subprocess.CompletedProcess(command, 0, "", "")

    result = MacOSSandboxBackend(runner=runner).smoke(tmp_path / "fixture")
    assert result.available is True
    assert "smoke test passed" in result.detail
    assert not (tmp_path / ".sdf-containment-inside").exists()


def test_fixture_sandbox_can_opt_into_containment_backend(tmp_path: Path):
    calls = []

    class Backend:
        def run(self, command, **kwargs):
            calls.append((tuple(command), kwargs["root"], kwargs["allow_network"]))
            return subprocess.CompletedProcess(command, 0, "contained", "")

    sandbox = FixtureSandbox(tmp_path / "fixture", containment_backend=Backend())
    result = sandbox.run(["python", "-c", "print('ignored by fake backend')"])
    assert result.stdout == "contained"
    assert result.cleanup_completed is True
    assert calls[0][1] == (tmp_path / "fixture").resolve()
