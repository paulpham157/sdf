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
    _assert_trust_story(stdout)

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
    assert demo.COST_CAVEAT in plain
    assert "-EXEC" in plain
    assert "does not accept the Task" in plain
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
    _assert_trust_story(stdout)


def _assert_trust_story(stdout: str) -> None:
    assert demo.COST_CAVEAT in stdout
    assert "usage_ledger_rows=0" in stdout
    assert "cost_usd=0.0" in stdout
    assert "revision_id=REV-" in stdout
    assert "tree_sha256=" in stdout
    validates = [line for line in stdout.splitlines() if "assumption edge validates from " in line]
    assert validates
    assert all("-EXEC" not in line and "EVIDENCE-" in line for line in validates)
    contradicts = [line for line in stdout.splitlines() if "assumption edge contradicts from " in line]
    assert contradicts
    assert all("-EXEC" not in line for line in contradicts)
    assert "-EXEC" in stdout
    assert "verifier=None" in stdout


def _story_case(*, status: str = "PASS", task_status: str = "succeeded", relation: str = "validates"):
    attempt = "ATTEMPT-1"
    revision_id = "REV-1"
    tree = "ab" * 32
    verification = {
        "id": "EVIDENCE-ATTEMPT-1-1",
        "attempt_id": attempt,
        "status": status,
        "evidence_class": "verification",
        "verifier_identity": "sdf-evaluator",
        "verifier_role": "evaluator",
        "producer_identity": "sdf-evaluator",
        "producer_role": "evaluator",
        "revision_id": revision_id,
        "tree_sha256": tree,
        "criterion": "app.py prints updated",
    }
    execution = {
        "id": "EVIDENCE-ATTEMPT-1-EXEC",
        "attempt_id": attempt,
        "status": "PASS",
        "evidence_class": "execution",
        "producer_identity": "native",
        "producer_role": "agent",
        "verifier_identity": None,
        "verifier_role": None,
        "revision_id": revision_id,
        "tree_sha256": tree,
    }
    return {
        "task": demo.TASK_A,
        "run": {
            "task_status": task_status,
            "attempt_status": "completed",
            "provider": "fake",
            "evidence_mode": "synthetic",
            "cost_usd": 0.0,
        },
        "attempt_id": attempt,
        "evidence": {"verification": verification, "execution": execution},
        "evidence_rows": {"verification": [verification], "execution": [execution]},
        "revision": {
            "revision": {"id": revision_id, "result_tree_sha256": tree, "base_commit": None},
            "verification": {"status": "verified", "problems": []},
        },
        "trace": {
            "nodes": [
                {"kind": "business_context", "id": "BC-OFFLINE-001"},
                {"kind": "objective", "id": "OBJ-OFFLINE-001"},
                {"kind": "assumption", "id": demo.ASSUMPTION_ID},
                {"kind": "task", "id": demo.TASK_A["id"]},
                {"kind": "attempt", "id": attempt},
                {"kind": "evidence", "id": execution["id"]},
                {"kind": "evidence", "id": verification["id"]},
            ],
            "edges": [{
                "source_kind": "evidence",
                "source_id": verification["id"],
                "relation": relation,
                "target_kind": "assumption",
                "target_id": demo.ASSUMPTION_ID,
            }],
        },
        "usage_ledger_rows": 0,
    }


def test_two_evidence_rows_still_count_as_evaluator_pass():
    """ADR 0042 adds an execution row beside verification. The total is not the check."""

    case = _story_case()
    assert demo.trust_problems(case, True) == []
    assert demo._accepted(case) is True
    assert len(case["evidence_rows"]["verification"]) + len(case["evidence_rows"]["execution"]) == 2


def test_execution_pass_does_not_accept_the_task():
    case = _story_case()
    case["evidence_rows"]["verification"] = []
    case["trace"]["edges"] = [{
        "source_kind": "evidence",
        "source_id": case["evidence"]["execution"]["id"],
        "relation": "validates",
        "target_kind": "assumption",
        "target_id": demo.ASSUMPTION_ID,
    }]
    problems = demo.trust_problems(case, True)
    text = "\n".join(problems)
    assert "one evidence node" not in text
    assert "one verification Evidence row" in text
    assert "execution Evidence is the source of a validates edge" in text
    assert demo._accepted(case) is False


def test_agent_self_report_cannot_accept():
    case = _story_case()
    verification = case["evidence"]["verification"]
    verification["verifier_identity"] = "native"
    verification["verifier_role"] = "agent"
    verification["producer_identity"] = "native"
    verification["producer_role"] = "agent"
    problems = demo.trust_problems(case, True)
    assert any("independent evaluator" in problem for problem in problems)
    assert demo._accepted(case) is False


def test_other_attempts_validates_edge_does_not_accept_this_task():
    case = _story_case()
    case["trace"]["edges"] = [{
        "source_kind": "evidence",
        "source_id": "EVIDENCE-OTHER-ATTEMPT-1",
        "relation": "validates",
        "target_kind": "assumption",
        "target_id": demo.ASSUMPTION_ID,
    }]
    problems = demo.trust_problems(case, True)
    assert any(demo.ASSUMPTION_ID in problem and "validates" in problem for problem in problems)


def test_verification_must_match_the_attempt_revision():
    case = _story_case()
    case["revision"]["revision"]["result_tree_sha256"] = "cd" * 32
    problems = demo.trust_problems(case, True)
    assert any("revision tree" in problem for problem in problems)


def test_unmetered_cost_is_part_of_the_demo_check():
    case = _story_case()
    case["run"]["cost_usd"] = 1.25
    case["usage_ledger_rows"] = 2
    problems = demo.trust_problems(case, True)
    assert any("cost_usd" in problem for problem in problems)
    assert any("usage_ledger_rows" in problem for problem in problems)


def test_rejected_task_must_contradict_rather_than_validate():
    case = _story_case(status="FAIL", task_status="failed", relation="contradicts")
    case["task"] = demo.TASK_B
    assert demo.trust_problems(case, False) == []
    assert demo._accepted(case) is False


def test_configure_evidence_mac_key_sets_a_fixture_when_unset(monkeypatch):
    monkeypatch.delenv("SDF_EVIDENCE_MAC_KEY", raising=False)
    assert demo.configure_evidence_mac_key() == demo.DEMO_EVIDENCE_MAC_KEY
    assert os.environ["SDF_EVIDENCE_MAC_KEY"] == demo.DEMO_EVIDENCE_MAC_KEY
    assert len(demo.DEMO_EVIDENCE_MAC_KEY) >= demo.MIN_EVIDENCE_MAC_KEY_CHARS


def test_configure_evidence_mac_key_keeps_a_long_user_key(monkeypatch):
    supplied = "user-supplied-evidence-mac-key-not-the-demo-fixture"
    assert len(supplied) >= demo.MIN_EVIDENCE_MAC_KEY_CHARS
    monkeypatch.setenv("SDF_EVIDENCE_MAC_KEY", supplied)
    assert demo.configure_evidence_mac_key() == supplied
    assert os.environ["SDF_EVIDENCE_MAC_KEY"] == supplied


def test_configure_evidence_mac_key_rejects_a_short_user_key(monkeypatch):
    monkeypatch.setenv("SDF_EVIDENCE_MAC_KEY", "too-short")
    with pytest.raises(SystemExit) as caught:
        demo.configure_evidence_mac_key()
    assert caught.value.code == 2
    assert os.environ["SDF_EVIDENCE_MAC_KEY"] == "too-short"


def test_demo_evidence_wrapper_delegates_to_demo_py():
    text = (REPO / "scripts" / "demo-evidence.sh").read_text(encoding="utf-8")
    assert "scripts/demo.py" in text
    assert "exec uv run scripts/demo.py" in text
