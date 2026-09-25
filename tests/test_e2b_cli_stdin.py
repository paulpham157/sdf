"""E2B CLI runners must never hand the child an inherited stdin.

`e2b sandbox exec` streams piped stdin until EOF before waiting for the remote
command, so an inherited, never-closed pipe hangs the call forever
(docs/research/e2b-exec-reliability.md).
"""

import subprocess
from pathlib import Path

import pytest

from sdf_core.e2b_containment import E2BContainmentBackend
from sdf_core.herdr_e2b import HerdrE2BAdapter

class _Done:
    returncode = 0
    pid = 1

    def communicate(self, timeout=None):
        return "", ""

@pytest.fixture
def popen_kwargs(monkeypatch: pytest.MonkeyPatch):
    seen: list[dict] = []

    def fake_popen(command, **kwargs):
        seen.append(kwargs)
        return _Done()

    monkeypatch.setattr(subprocess, "Popen", fake_popen)
    return seen

def test_containment_runner_closes_stdin(popen_kwargs, tmp_path: Path):
    E2BContainmentBackend._run(object.__new__(E2BContainmentBackend), ("e2b-box", "exec", "true"), tmp_path, 1000, {})
    assert popen_kwargs[-1]["stdin"] is subprocess.DEVNULL

def test_headless_adapter_runner_closes_stdin(popen_kwargs, tmp_path: Path):
    HerdrE2BAdapter(environ={})._run(("e2b-box", "kill"), tmp_path, 1000, {})
    assert popen_kwargs[-1]["stdin"] is subprocess.DEVNULL
