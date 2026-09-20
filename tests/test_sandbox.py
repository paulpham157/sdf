from __future__ import annotations

import os
import sys
from pathlib import Path

import pytest

from sdf_core.sandbox import (
    FixtureSandbox,
    NetworkAccessDenied,
    SandboxViolation,
)
from sdf_core.containment import ContainmentUnavailable


def make_sandbox(tmp_path: Path) -> FixtureSandbox:
    return FixtureSandbox(tmp_path / "fixture")


def test_filesystem_operations_are_bound_to_fixture_even_without_tool_proxy(tmp_path: Path):
    sandbox = make_sandbox(tmp_path)
    sandbox.write_text("src/app.py", "print('fixture')\n")

    assert sandbox.read_text("src/app.py") == "print('fixture')\n"

    with pytest.raises(SandboxViolation, match="outside sandbox"):
        sandbox.write_text("../escape.txt", "must not be written")
    with pytest.raises(SandboxViolation, match="absolute"):
        sandbox.write_text(tmp_path / "escape.txt", "must not be written")
    assert not (tmp_path / "escape.txt").exists()


def test_filesystem_operations_reject_symlink_escape(tmp_path: Path):
    sandbox = make_sandbox(tmp_path)
    outside = tmp_path / "outside"
    outside.mkdir()
    (sandbox.root / "link").symlink_to(outside, target_is_directory=True)

    with pytest.raises(SandboxViolation, match="outside sandbox"):
        sandbox.write_text("link/escape.txt", "must not be written")
    assert not (outside / "escape.txt").exists()


def test_process_is_bounded_and_terminated_on_timeout(tmp_path: Path):
    sandbox = make_sandbox(tmp_path)
    result = sandbox.run(
        [sys.executable, "-c", "import os, time; print(os.getpid(), flush=True); time.sleep(30)"],
        timeout_seconds=0.1,
    )

    assert result.timed_out is True
    assert result.cleanup_completed is True
    assert result.pid is not None
    with pytest.raises(ProcessLookupError):
        os.kill(result.pid, 0)


def test_process_output_is_bounded(tmp_path: Path):
    sandbox = make_sandbox(tmp_path)
    result = sandbox.run(
        [sys.executable, "-c", "print('x' * 10000)"],
        max_output_bytes=128,
    )

    assert result.timed_out is False
    assert result.output_truncated is True
    assert len(result.stdout.encode("utf-8")) <= 128


def test_process_cwd_cannot_escape_fixture(tmp_path: Path):
    sandbox = make_sandbox(tmp_path)

    with pytest.raises(SandboxViolation, match="absolute|outside sandbox"):
        sandbox.run([sys.executable, "-c", "pass"], cwd=tmp_path)


def test_untrusted_process_fails_closed_without_os_containment(tmp_path: Path):
    sandbox = FixtureSandbox(tmp_path / "fixture", require_containment=True)

    with pytest.raises(ContainmentUnavailable, match="OS containment backend"):
        sandbox.run([sys.executable, "-c", "open('/tmp/sdf-bypass', 'w').write('escape')"])


def test_network_is_denied_by_default_without_calling_injected_executor(tmp_path: Path):
    calls: list[str] = []

    def fake_network(url: str, **_: object) -> str:
        calls.append(url)
        return "would have escaped"

    sandbox = FixtureSandbox(tmp_path / "fixture", network_executor=fake_network)

    with pytest.raises(NetworkAccessDenied, match="disabled"):
        sandbox.request_network("https://example.com")
    assert calls == []


def test_network_executor_is_an_explicit_opt_in_seam(tmp_path: Path):
    calls: list[str] = []

    def fake_network(url: str, **_: object) -> str:
        calls.append(url)
        return "fixture response"

    sandbox = FixtureSandbox(
        tmp_path / "fixture",
        allow_network=True,
        network_executor=fake_network,
    )

    assert sandbox.request_network("https://example.com") == "fixture response"
    assert calls == ["https://example.com"]
