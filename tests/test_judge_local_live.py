"""Opt-in live call against a local AgentJev-0.6B process.

Skipped unless SDF_LIVE_AGENTJEV=1. Needs a server on SDF_AGENTJEV_URL
(default http://127.0.0.1:8149). See docs/research/agentjev-local.md.
"""

from __future__ import annotations

import json
import os
import urllib.error
import urllib.request
from pathlib import Path

import pytest

from sdf_core.judge import TRIAGE_QUESTIONS, validate_answers
from sdf_core.judge_local import DEFAULT_AGENTJEV_URL, LocalAgentJevJudge, make_agentjev_post

LIVE = os.environ.get("SDF_LIVE_AGENTJEV") == "1"
URL = os.environ.get("SDF_AGENTJEV_URL", DEFAULT_AGENTJEV_URL).rstrip("/")
ROOT = Path(__file__).resolve().parents[1]
FIXTURE = ROOT / "tests/fixtures/judge/cases/environment_or_runtime-001.json"

pytestmark = pytest.mark.skipif(not LIVE, reason="set SDF_LIVE_AGENTJEV=1 with a local AgentJev process on SDF_AGENTJEV_URL")


def _load_case() -> dict:
    assert FIXTURE.is_file(), f"missing #21 fixture at {FIXTURE}"
    return json.loads(FIXTURE.read_text(encoding="utf-8"))


def _server_up() -> bool:
    try:
        with urllib.request.urlopen(f"{URL}/health", timeout=2) as response:
            return 200 <= response.status < 300
    except (urllib.error.URLError, TimeoutError, OSError):
        return False


def test_live_local_agentjev_answers_one_synthetic_triage_case():
    if not _server_up():
        pytest.skip(f"no AgentJev process at {URL}/health")
    case = _load_case()
    assert case["label"]["failure_cause"] == "environment_or_runtime"
    state = case["state"]
    judge = LocalAgentJevJudge(make_agentjev_post(URL, timeout_s=120.0))

    result = judge.ask(state, TRIAGE_QUESTIONS)

    assert result.available, result.unavailable_reason
    assert result.answers is not None
    validate_answers(TRIAGE_QUESTIONS, result.answers)
    assert result.model
    assert "failure_cause" in result.answers
    assert result.answers["failure_cause"]["choice"] in TRIAGE_QUESTIONS["failure_cause"]["criteria"]
    assert "higher_tier_would_help" in result.answers
    assert 0.0 <= result.answers["higher_tier_would_help"]["noul"] <= 1.0
