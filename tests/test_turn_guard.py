"""ADR-0019: per-turn process containment (guard / kill / enter / sweep)."""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
import time
import uuid
from pathlib import Path

import pytest

from sdf_core import turn_guard as tg

pytestmark = pytest.mark.skipif(
    sys.platform != "linux" or shutil.which("setsid") is None or shutil.which("ps") is None,
    reason="turn containment needs Linux procps and util-linux setsid",
)

LEAKER = r"""
setsid sleep 300 </dev/null >/dev/null 2>&1 &
( ( while :; do date +%s%N >> "$1/leak.txt"; sleep 0.2; done ) </dev/null >/dev/null 2>&1 & ) &
sleep "$2"
"""


def _tagged(turn_id: str) -> list[int]:
    out = []
    needle = f"{tg.TAG_ENV}={turn_id}".encode()
    for entry in Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        try:
            env = (entry / "environ").read_bytes().split(b"\0")
            state = (entry / "stat").read_text().rsplit(")", 1)[1].split()[0]
        except OSError:
            continue
        if needle in env and state != "Z":
            out.append(int(entry.name))
    return out


def _quiet(path: Path, seconds: float = 1.0) -> bool:
    before = path.read_bytes() if path.exists() else b""
    time.sleep(seconds)
    after = path.read_bytes() if path.exists() else b""
    return before == after


def _id() -> str:
    return f"pytest-{uuid.uuid4().hex[:10]}-t1"


def test_argv_builders_validate_input():
    with pytest.raises(ValueError):
        tg.guard_argv("bad id;rm", ["true"])
    with pytest.raises(ValueError):
        tg.guard_argv("ok", [])
    with pytest.raises(ValueError):
        tg.kill_argv()
    with pytest.raises(ValueError):
        tg.enter_argv("ok", 1)
    with pytest.raises(ValueError):
        tg.sweep_argv("ok", 0)
    argv = tg.guard_argv("s1-t1", ["agent", "--x", "a b"])
    assert argv[:4] == ["sh", "-c", tg._GUARD, "sdf-turn-guard"]
    assert argv[4:] == ["s1-t1", str(tg.GRACE_S), "agent", "--x", "a b"]
    assert tg.kill_argv("s1-t")[3:] == ["sdf-turn-kill", str(tg.GRACE_S), "s1-t"]


def test_guard_preserves_exit_code_and_output():
    done = subprocess.run(tg.guard_argv(_id(), ["sh", "-c", "echo hi; exit 7"], grace_s=1),
                          capture_output=True, text=True, timeout=30)
    assert done.returncode == 7
    assert "hi" in done.stdout


def test_guard_sweeps_detached_and_double_forked_children(tmp_path):
    turn = _id()
    done = subprocess.run(tg.guard_argv(turn, ["sh", "-c", LEAKER, "leaker", str(tmp_path), "1"], grace_s=1),
                          stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True, timeout=60)
    assert done.returncode == 0
    assert _tagged(turn) == []
    assert (tmp_path / "leak.txt").exists()
    assert _quiet(tmp_path / "leak.txt")
    assert not (Path(tg.CGROUP_DIR) / turn).exists()


def test_sigkilled_agent_still_swept_when_guard_survives(tmp_path):
    turn = _id()
    proc = subprocess.Popen(tg.guard_argv(turn, ["sh", "-c", LEAKER, "leaker", str(tmp_path), "120"], grace_s=1),
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        sid_file = Path(tg.TURN_DIR) / turn / "sid"
        for _ in range(100):
            if sid_file.exists() and (tmp_path / "leak.txt").exists():
                break
            time.sleep(0.05)
        agent = int(sid_file.read_text())
        os.kill(agent, 9)  # the agent itself dies abruptly; its children are orphaned
        proc.wait(timeout=30)
        assert _tagged(turn) == []
        assert _quiet(tmp_path / "leak.txt")
    finally:
        subprocess.run(tg.kill_argv(turn), timeout=60, capture_output=True)
        proc.kill()


def test_kill_argv_terminates_running_turns_by_prefix(tmp_path):
    session = f"pytest-{uuid.uuid4().hex[:10]}"
    turn = f"{session}-t1"
    proc = subprocess.Popen(tg.guard_argv(turn, ["sh", "-c", LEAKER, "leaker", str(tmp_path), "120"], grace_s=1),
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        for _ in range(100):
            if (tmp_path / "leak.txt").exists():
                break
            time.sleep(0.05)
        out = subprocess.run(tg.kill_argv(f"{session}-t", grace_s=1), capture_output=True, text=True, timeout=60)
        assert "SDF_TURN_KILLED" in out.stdout
        proc.wait(timeout=30)
        assert _tagged(turn) == []
        assert _quiet(tmp_path / "leak.txt")
    finally:
        proc.kill()


def test_enter_and_sweep_keep_the_long_lived_root(tmp_path):
    turn = _id()
    root = subprocess.Popen(["setsid", "sh", "-c", "sleep 120"], stdout=subprocess.DEVNULL)
    try:
        time.sleep(0.2)
        mode = subprocess.run(tg.enter_argv(turn, root.pid), capture_output=True, text=True, timeout=30)
        assert mode.stdout.strip() in {"cgroup", "session"}
        # A leftover in the root's session that is not its descendant (reparented orphan).
        leftover = subprocess.Popen(
            ["sh", "-c", f'( ( exec env {tg.TAG_ENV}={turn} sh -c "while :; do date >> {tmp_path}/o.txt; sleep 0.2; done" ) & ) '],
        )
        leftover.wait(timeout=10)
        time.sleep(0.5)
        out = subprocess.run(tg.sweep_argv(turn, root.pid, grace_s=1), capture_output=True, text=True, timeout=60)
        assert "SDF_TURN_SWEPT" in out.stdout
        assert root.poll() is None, "the pane shell / long-lived agent must survive a sweep"
        assert _quiet(tmp_path / "o.txt")
    finally:
        subprocess.run(tg.kill_argv(turn, grace_s=1), timeout=60, capture_output=True)
        root.kill()
    assert root.wait(timeout=10) is not None
