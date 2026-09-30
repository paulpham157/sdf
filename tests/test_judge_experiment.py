"""Offline judge experiment (#24): schema, metrics, and fake-backend smoke.

The fake path must run in default CI without weights or a live process.
"""

from __future__ import annotations

import json
import os
from pathlib import Path

import pytest

from scripts import judge_experiment as exp
from sdf_core.judge import FAILURE_CAUSES, TRIAGE_QUESTIONS, validate_answers

ROOT = Path(__file__).resolve().parents[1]
CASES_DIR = ROOT / "tests" / "fixtures" / "judge" / "cases"


def test_non_agent_causes_match_dataset_convention():
    assert exp.AGENT_CAUSES == frozenset({"agent_solution_wrong", "agent_did_not_attempt"})
    assert exp.NON_AGENT_CAUSES == frozenset(FAILURE_CAUSES) - exp.AGENT_CAUSES
    assert "not_stated" in exp.NON_AGENT_CAUSES


def test_answers_from_label_are_valid_triage_answers():
    case = json.loads((CASES_DIR / "environment_or_runtime-001.json").read_text())
    answers = exp.answers_from_label(case["label"], confidence=0.91)
    validate_answers(TRIAGE_QUESTIONS, answers)
    assert answers["failure_cause"]["choice"] == "environment_or_runtime"
    assert answers["failure_cause"]["confidence"] == pytest.approx(0.91)
    assert answers["higher_tier_would_help"]["noul"] == pytest.approx(0.1)


def test_metrics_accuracy_calibration_and_withhold():
    predictions = [
        exp.Prediction(
            case_id="a",
            run=0,
            label_cause="environment_or_runtime",
            label_higher_tier=False,
            predicted_cause="environment_or_runtime",
            confidence=0.95,
            higher_tier_noul=0.1,
            available=True,
            model="fake",
            state_trimmed=False,
        ),
        exp.Prediction(
            case_id="b",
            run=0,
            label_cause="agent_solution_wrong",
            label_higher_tier=True,
            predicted_cause="environment_or_runtime",
            confidence=0.9,
            higher_tier_noul=0.2,
            available=True,
            model="fake",
            state_trimmed=False,
        ),
        exp.Prediction(
            case_id="c",
            run=0,
            label_cause="credential_or_provider",
            label_higher_tier=False,
            predicted_cause="credential_or_provider",
            confidence=0.5,
            higher_tier_noul=0.1,
            available=True,
            model="fake",
            state_trimmed=False,
        ),
    ]
    metrics = exp.compute_metrics(predictions, confidence_threshold=0.8)
    assert metrics["failure_cause_accuracy"] == pytest.approx(2 / 3)
    assert metrics["available_count"] == 3
    assert metrics["escalations_withheld"] == 2  # a and b (confident non-agent)
    assert metrics["escalations_withheld_wrong"] == 1  # b: agent label, withheld
    assert 0.0 <= metrics["failure_cause_calibration_ece"] <= 1.0
    assert metrics["per_cause"]["environment_or_runtime"]["support"] == 1
    assert metrics["per_cause"]["environment_or_runtime"]["correct"] == 1


def test_results_artifact_schema_for_fake_oracle():
    cases = exp.load_cases(CASES_DIR)
    assert len(cases) >= 30
    results = exp.run_experiment(
        backend="fake",
        cases=cases,
        runs=3,
        confidence_threshold=0.8,
    )
    assert results["schema_version"] == 1
    assert results["issue"] == 24
    assert results["evidence_mode"] == "synthetic"
    assert results["evidence_basis"] == "local Evidence per ADR-0006; not production impact"
    assert results["backend"] == "fake"
    assert results["runs"] == 3
    assert results["confidence_threshold"] == 0.8
    assert results["dataset"]["case_count"] == len(cases)
    assert results["agentjev"] is None
    metrics = results["metrics"]
    assert metrics["failure_cause_accuracy"] == pytest.approx(1.0)
    assert metrics["escalations_withheld_wrong"] == 0
    non_agent_n = sum(1 for c in cases if c["label"]["failure_cause"] in exp.NON_AGENT_CAUSES)
    assert metrics["escalations_withheld"] == non_agent_n * 3
    assert "token_fit" in results
    assert results["token_fit"]["cases_trimmed"] == 0
    # Oracle fake is deterministic across runs.
    assert results["metrics"]["run_accuracies"] == [1.0, 1.0, 1.0]


def test_write_results_round_trip(tmp_path: Path):
    cases = exp.load_cases(CASES_DIR)[:3]
    results = exp.run_experiment(backend="fake", cases=cases, runs=2, confidence_threshold=0.8)
    path = tmp_path / "results.json"
    exp.write_results(path, results)
    loaded = json.loads(path.read_text())
    assert loaded["backend"] == "fake"
    assert loaded["metrics"]["failure_cause_accuracy"] == pytest.approx(1.0)


def test_default_backend_is_fake(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.delenv("SDF_JUDGE_BACKEND", raising=False)
    assert exp.resolve_backend(None) == "fake"
    monkeypatch.setenv("SDF_JUDGE_BACKEND", "local")
    assert exp.resolve_backend(None) == "local"
    assert exp.resolve_backend("fake") == "fake"
    with pytest.raises(ValueError, match="cloudflare|local|fake"):
        exp.resolve_backend("cloudflare")


def test_cli_fake_writes_artifact(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    out = tmp_path / "out.json"
    monkeypatch.setenv("SDF_JUDGE_BACKEND", "fake")
    rc = exp.main(
        [
            "--cases",
            str(CASES_DIR),
            "--runs",
            "3",
            "--output",
            str(out),
            "--confidence-threshold",
            "0.8",
        ]
    )
    assert rc == 0
    assert out.is_file()
    payload = json.loads(out.read_text())
    assert payload["backend"] == "fake"
    assert payload["metrics"]["failure_cause_accuracy"] == pytest.approx(1.0)
    assert "SDF_JUDGE_BACKEND" not in os.environ or os.environ["SDF_JUDGE_BACKEND"] == "fake"
