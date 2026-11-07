"""Hermetic checks for the one-command offline evidence demo."""

from __future__ import annotations

import importlib.util
import os
import subprocess
import sys
from html import unescape
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parents[1]


def _load_demo():
    spec = importlib.util.spec_from_file_location("sdf_demo_script", REPO / "scripts" / "demo.py")
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


demo = _load_demo()

_PLATFORM_REASON = (
    "sealed oracle (ADR 0039) needs Linux x86_64 with Landlock; "
    f"open {demo.CODESPACES_URL}"
)


def test_unsupported_host_message_points_to_codespaces():
    message = demo.unsupported_host_message()
    if message is None:
        assert demo.linux_x86_64() and demo.landlock_available()
        return
    assert "Linux x86_64" in message
    assert demo.CODESPACES_URL in message
    assert "README" in message


@pytest.mark.skipif(demo.unsupported_host_message() is not None, reason=_PLATFORM_REASON)
def test_demo_script_accepts_task_via_verification(tmp_path):
    out_dir = tmp_path / "story"
    env = os.environ.copy()
    for name in demo.PROVIDER_KEY_VARS:
        env[name] = "test-unused-key"
    for name in demo._LIVE_ENV:
        env.pop(name, None)
    completed = subprocess.run(
        [sys.executable, str(REPO / "scripts" / "demo.py"), "--out", str(out_dir)],
        cwd=REPO,
        env=env,
        capture_output=True,
        text=True,
        timeout=60,
        check=False,
    )
    assert completed.returncode == 0, completed.stdout + completed.stderr
    stdout = completed.stdout
    assert "provider keys are set in this shell and were not used" in stdout
    assert "provider=fake" in stdout
    assert "evidence_mode=synthetic" in stdout
    assert "task_status=succeeded" in stdout
    assert "verification       : status=PASS" in stdout
    assert "verifier=sdf-evaluator" in stdout
    assert "cannot accept the Task" in stdout
    assert "verification Evidence PASS, not the agent's stdout" in stdout

    html_path = out_dir / "index.html"
    assert html_path.is_file()
    page = html_path.read_text(encoding="utf-8")
    assert demo.PASS_BADGE in page
    assert "Cannot accept the Task." in page
    assert "This is what accepted the Task." in page
    assert "sdf-evaluator" in page
    assert "print('old')" in unescape(page)
    assert "print('updated')" in unescape(page)
    assert str(html_path.resolve().as_uri()) in stdout


def test_demo_evidence_wrapper_delegates_to_demo_py():
    text = (REPO / "scripts" / "demo-evidence.sh").read_text(encoding="utf-8")
    assert "scripts/demo.py" in text
    assert "exec uv run scripts/demo.py" in text
