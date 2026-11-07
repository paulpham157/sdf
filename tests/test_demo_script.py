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
def test_demo_script_accepts_honest_edit_and_rejects_plausible_miss(tmp_path):
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
        timeout=90,
        check=False,
    )
    assert completed.returncode == 0, completed.stdout + completed.stderr
    stdout = completed.stdout
    assert "provider keys are set in this shell and were not used" in stdout
    assert "provider=fake" in stdout
    assert "evidence_mode=synthetic" in stdout
    assert demo.CLAIM_A in stdout
    assert demo.CLAIM_B in stdout
    assert "A accepted / B rejected, though the agent said done" in stdout
    assert "Takeaway:" in stdout

    assert "Task A: task_status=succeeded" in stdout
    assert "Task A: verification status=PASS" in stdout
    assert "Task B: task_status=" in stdout
    assert "Task B: task_status=succeeded" not in stdout
    assert "Task B: verification status=PASS" not in stdout
    assert "Task B: verification status=" in stdout
    assert "verifier=sdf-evaluator" in stdout
    assert "attempt_status=completed" in stdout
    assert "cannot accept the Task" in stdout

    html_path = out_dir / "index.html"
    assert html_path.is_file()
    page = html_path.read_text(encoding="utf-8")
    assert demo.PASS_BADGE in page
    assert demo.REJECT_BADGE in page
    assert demo.CLAIM_A in unescape(page)
    assert demo.CLAIM_B in unescape(page)
    assert "sdf-evaluator" in page
    plain = unescape(page)
    assert "print('old')" in plain
    assert "print('updated')" in plain
    assert "print('updated ')" in plain
    assert str(html_path.resolve().as_uri()) in stdout


@pytest.mark.skipif(demo.unsupported_host_message() is not None, reason=_PLATFORM_REASON)
def test_demo_script_supplies_its_own_mac_key_and_accepts_honest_edit(tmp_path):
    """The user-facing command must work without the pytest fixture MAC key."""

    out_dir = tmp_path / "story"
    env = os.environ.copy()
    env.pop("SDF_EVIDENCE_MAC_KEY", None)
    env.pop("SDF_EVIDENCE_MAC_ALLOW_INSECURE_DEV_KEY", None)
    for name in demo.PROVIDER_KEY_VARS:
        env.pop(name, None)
    for name in demo._LIVE_ENV:
        env.pop(name, None)
    completed = subprocess.run(
        [sys.executable, str(REPO / "scripts" / "demo.py"), "--out", str(out_dir)],
        cwd=REPO,
        env=env,
        capture_output=True,
        text=True,
        timeout=90,
        check=False,
    )
    assert completed.returncode == 0, completed.stdout + completed.stderr
    stdout = completed.stdout
    assert "A accepted / B rejected, though the agent said done" in stdout
    assert "Task A: task_status=succeeded" in stdout
    assert "Task A: verification status=PASS" in stdout
    assert "Task B: task_status=succeeded" not in stdout
    assert "Task B: verification status=PASS" not in stdout
    assert "verifier=sdf-evaluator" in stdout


def test_configure_env_sets_a_fixture_mac_key_when_unset(tmp_path, monkeypatch):
    monkeypatch.delenv("SDF_EVIDENCE_MAC_KEY", raising=False)
    demo.configure_env(tmp_path, REPO)
    assert os.environ["SDF_EVIDENCE_MAC_KEY"] == demo.DEMO_EVIDENCE_MAC_KEY
    assert len(os.environ["SDF_EVIDENCE_MAC_KEY"]) >= demo.MIN_EVIDENCE_MAC_KEY_CHARS


def test_configure_env_keeps_a_long_user_mac_key(tmp_path, monkeypatch):
    supplied = "user-supplied-evidence-mac-key-not-the-demo-fixture"
    assert len(supplied) >= demo.MIN_EVIDENCE_MAC_KEY_CHARS
    monkeypatch.setenv("SDF_EVIDENCE_MAC_KEY", supplied)
    demo.configure_env(tmp_path, REPO)
    assert os.environ["SDF_EVIDENCE_MAC_KEY"] == supplied


def test_configure_env_rejects_a_short_user_mac_key(tmp_path, monkeypatch):
    monkeypatch.setenv("SDF_EVIDENCE_MAC_KEY", "too-short")
    with pytest.raises(SystemExit) as caught:
        demo.configure_env(tmp_path, REPO)
    assert caught.value.code == 2
    assert os.environ["SDF_EVIDENCE_MAC_KEY"] == "too-short"


def test_demo_evidence_wrapper_delegates_to_demo_py():
    text = (REPO / "scripts" / "demo-evidence.sh").read_text(encoding="utf-8")
    assert "scripts/demo.py" in text
    assert "exec uv run scripts/demo.py" in text
