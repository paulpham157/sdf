#!/usr/bin/env python3
"""Offline failed-Attempt triage experiment (#24).

Opt-in backend via ``SDF_JUDGE_BACKEND=local|fake`` (default ``fake``).
Runs the #21 fixture dataset N times (default 3), writes a local results
artifact (ADR-0006 local Evidence — not production impact), and never wires
into ExecutionService or Escalation.

Usage::

    uv run python -m scripts.judge_experiment --output artifacts/judge_experiment/fake.json
    SDF_JUDGE_BACKEND=local uv run python -m scripts.judge_experiment \\
        --output artifacts/judge_experiment/local.json
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request
from collections import Counter, defaultdict
from collections.abc import Mapping, Sequence
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from sdf_core.judge import FAILURE_CAUSES, TRIAGE_QUESTIONS, FakeJudge, validate_answers
from sdf_core.judge_local import (
    DEFAULT_AGENTJEV_URL,
    LocalAgentJevJudge,
    fit_state_for_agentjev,
    make_agentjev_post,
)

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_CASES_DIR = ROOT / "tests" / "fixtures" / "judge" / "cases"
DEFAULT_RUNS = 3
DEFAULT_CONFIDENCE_THRESHOLD = 0.8
SCHEMA_VERSION = 1
ISSUE = 24

AGENT_CAUSES = frozenset({"agent_solution_wrong", "agent_did_not_attempt"})
NON_AGENT_CAUSES = frozenset(FAILURE_CAUSES) - AGENT_CAUSES
ALLOWED_BACKENDS = frozenset({"fake", "local"})


@dataclass(frozen=True)
class Prediction:
    case_id: str
    run: int
    label_cause: str
    label_higher_tier: bool
    predicted_cause: str | None
    confidence: float | None
    higher_tier_noul: float | None
    available: bool
    model: str | None
    state_trimmed: bool
    unavailable_reason: str | None = None


def resolve_backend(explicit: str | None) -> str:
    value = (explicit or os.environ.get("SDF_JUDGE_BACKEND") or "fake").strip().lower()
    if value not in ALLOWED_BACKENDS:
        raise ValueError(
            f"unknown SDF_JUDGE_BACKEND={value!r}; use local|fake "
            "(cloudflare lands with #22)"
        )
    return value


def load_cases(cases_dir: Path) -> list[dict[str, Any]]:
    paths = sorted(cases_dir.glob("*.json"))
    if not paths:
        raise FileNotFoundError(f"no cases under {cases_dir}")
    cases: list[dict[str, Any]] = []
    for path in paths:
        case = json.loads(path.read_text(encoding="utf-8"))
        if set(case) != {"id", "label", "state"}:
            raise ValueError(f"bad case schema: {path.name}")
        cases.append(case)
    return cases


def answers_from_label(label: Mapping[str, Any], *, confidence: float = 0.95) -> dict[str, dict[str, Any]]:
    """Deterministic oracle answers for the fake backend smoke path."""
    cause = label["failure_cause"]
    if cause not in FAILURE_CAUSES:
        raise ValueError(f"unknown failure_cause {cause!r}")
    conf = float(confidence)
    if not 0.0 <= conf <= 1.0:
        raise ValueError("confidence must be in [0, 1]")
    remainder = 1.0 - conf
    others = [name for name in FAILURE_CAUSES if name != cause]
    share = remainder / len(others) if others else 0.0
    probabilities = {name: (conf if name == cause else share) for name in FAILURE_CAUSES}
    # Tiny float drift: put leftover on the chosen label.
    probabilities[cause] = conf + (1.0 - sum(probabilities.values()))
    higher = 0.9 if label["higher_tier_would_help"] else 0.1
    answers = {
        "failure_cause": {
            "choice": cause,
            "probabilities": probabilities,
            "confidence": conf,
        },
        "higher_tier_would_help": {"noul": higher},
    }
    validate_answers(TRIAGE_QUESTIONS, answers)
    return answers


def _state_was_trimmed(state: Mapping[str, Any]) -> bool:
    fitted = fit_state_for_agentjev(state, questions=TRIAGE_QUESTIONS)
    return json.dumps(fitted, sort_keys=True) != json.dumps(dict(state), sort_keys=True)


def _ece(pairs: Sequence[tuple[float, bool]], *, bins: int = 10) -> float:
    """Expected calibration error for (confidence, correct) pairs."""
    if not pairs:
        return 0.0
    bucket_sum = [0.0] * bins
    bucket_correct = [0.0] * bins
    bucket_n = [0] * bins
    for confidence, correct in pairs:
        index = min(int(confidence * bins), bins - 1)
        bucket_sum[index] += confidence
        bucket_correct[index] += 1.0 if correct else 0.0
        bucket_n[index] += 1
    total = len(pairs)
    ece = 0.0
    for i in range(bins):
        if bucket_n[i] == 0:
            continue
        avg_conf = bucket_sum[i] / bucket_n[i]
        avg_acc = bucket_correct[i] / bucket_n[i]
        ece += (bucket_n[i] / total) * abs(avg_conf - avg_acc)
    return ece


def compute_metrics(
    predictions: Sequence[Prediction],
    *,
    confidence_threshold: float,
) -> dict[str, Any]:
    available = [p for p in predictions if p.available and p.predicted_cause is not None]
    correct_n = sum(1 for p in available if p.predicted_cause == p.label_cause)
    accuracy = (correct_n / len(available)) if available else 0.0
    cal_pairs = [
        (float(p.confidence), p.predicted_cause == p.label_cause)
        for p in available
        if p.confidence is not None
    ]
    withheld = [
        p
        for p in available
        if p.predicted_cause in NON_AGENT_CAUSES
        and p.confidence is not None
        and p.confidence >= confidence_threshold
    ]
    withheld_wrong = [p for p in withheld if p.label_higher_tier]

    per_cause: dict[str, dict[str, int]] = {}
    for cause in FAILURE_CAUSES:
        labeled = [p for p in available if p.label_cause == cause]
        per_cause[cause] = {
            "support": len(labeled),
            "correct": sum(1 for p in labeled if p.predicted_cause == cause),
        }

    by_run: dict[int, list[Prediction]] = defaultdict(list)
    for p in available:
        by_run[p.run].append(p)
    run_accuracies = []
    for run in sorted(by_run):
        items = by_run[run]
        run_accuracies.append(sum(1 for p in items if p.predicted_cause == p.label_cause) / len(items))

    return {
        "failure_cause_accuracy": accuracy,
        "failure_cause_calibration_ece": _ece(cal_pairs),
        "available_count": len(available),
        "unavailable_count": sum(1 for p in predictions if not p.available),
        "escalations_withheld": len(withheld),
        "escalations_withheld_wrong": len(withheld_wrong),
        "confidence_threshold": confidence_threshold,
        "per_cause": per_cause,
        "run_accuracies": run_accuracies,
        "confusion": {
            f"{label}->{pred}": count
            for (label, pred), count in sorted(
                Counter((p.label_cause, p.predicted_cause) for p in available).items()
            )
        },
    }


def _agentjev_info(base_url: str) -> dict[str, Any]:
    info: dict[str, Any] = {
        "url": base_url.rstrip("/"),
        "model": None,
        "commit": None,
        "health": None,
    }
    root = base_url.rstrip("/")
    try:
        with urllib.request.urlopen(f"{root}/health", timeout=3) as response:
            info["health"] = json.loads(response.read().decode("utf-8"))
    except (urllib.error.URLError, TimeoutError, OSError, json.JSONDecodeError) as exc:
        info["health_error"] = str(exc)
    try:
        with urllib.request.urlopen(f"{root}/api/info", timeout=3) as response:
            payload = json.loads(response.read().decode("utf-8"))
            if isinstance(payload, dict):
                info["api_info"] = payload
                model = payload.get("model")
                if isinstance(model, str) and model.strip():
                    info["model"] = model
    except (urllib.error.URLError, TimeoutError, OSError, json.JSONDecodeError):
        pass
    # Best-effort: common local clone used by docs/research/agentjev-local.md operators.
    candidates = [
        Path.home() / ".cache" / "agentjev-prototype" / "agent-jev",
        Path(os.environ["AGENTJEV_REPO"]) if os.environ.get("AGENTJEV_REPO") else None,
    ]
    for path in candidates:
        if path is None or not (path / ".git").exists():
            continue
        try:
            commit = subprocess.check_output(
                ["git", "-C", str(path), "rev-parse", "HEAD"],
                text=True,
                stderr=subprocess.DEVNULL,
            ).strip()
            info["commit"] = commit
            info["repo_path"] = str(path)
            break
        except (subprocess.CalledProcessError, OSError):
            continue
    return info


def _predict_fake(case: dict[str, Any], run: int) -> Prediction:
    answers = answers_from_label(case["label"])
    judge = FakeJudge([answers])
    got = judge.ask(case["state"], TRIAGE_QUESTIONS)
    cause = got["failure_cause"]["choice"]
    return Prediction(
        case_id=case["id"],
        run=run,
        label_cause=case["label"]["failure_cause"],
        label_higher_tier=bool(case["label"]["higher_tier_would_help"]),
        predicted_cause=cause,
        confidence=float(got["failure_cause"]["confidence"]),
        higher_tier_noul=float(got["higher_tier_would_help"]["noul"]),
        available=True,
        model="fake-oracle",
        state_trimmed=False,
    )


def _predict_local(judge: LocalAgentJevJudge, case: dict[str, Any], run: int) -> Prediction:
    trimmed = _state_was_trimmed(case["state"])
    result = judge.ask(case["state"], TRIAGE_QUESTIONS)
    if not result.available or result.answers is None:
        return Prediction(
            case_id=case["id"],
            run=run,
            label_cause=case["label"]["failure_cause"],
            label_higher_tier=bool(case["label"]["higher_tier_would_help"]),
            predicted_cause=None,
            confidence=None,
            higher_tier_noul=None,
            available=False,
            model=result.model,
            state_trimmed=trimmed,
            unavailable_reason=result.unavailable_reason,
        )
    answers = result.answers
    validate_answers(TRIAGE_QUESTIONS, answers)
    return Prediction(
        case_id=case["id"],
        run=run,
        label_cause=case["label"]["failure_cause"],
        label_higher_tier=bool(case["label"]["higher_tier_would_help"]),
        predicted_cause=answers["failure_cause"]["choice"],
        confidence=float(answers["failure_cause"]["confidence"]),
        higher_tier_noul=float(answers["higher_tier_would_help"]["noul"]),
        available=True,
        model=result.model,
        state_trimmed=trimmed,
    )


def run_experiment(
    *,
    backend: str,
    cases: Sequence[dict[str, Any]],
    runs: int = DEFAULT_RUNS,
    confidence_threshold: float = DEFAULT_CONFIDENCE_THRESHOLD,
    agentjev_url: str | None = None,
) -> dict[str, Any]:
    backend = resolve_backend(backend)
    if runs < 1:
        raise ValueError("runs must be >= 1")
    predictions: list[Prediction] = []
    agentjev: dict[str, Any] | None = None
    local_judge: LocalAgentJevJudge | None = None

    if backend == "local":
        url = (agentjev_url or os.environ.get("SDF_AGENTJEV_URL") or DEFAULT_AGENTJEV_URL).rstrip("/")
        agentjev = _agentjev_info(url)
        local_judge = LocalAgentJevJudge(make_agentjev_post(url, timeout_s=120.0))

    for run in range(runs):
        for case in cases:
            if backend == "fake":
                predictions.append(_predict_fake(case, run))
            else:
                assert local_judge is not None
                predictions.append(_predict_local(local_judge, case, run))

    metrics = compute_metrics(predictions, confidence_threshold=confidence_threshold)
    trimmed_ids = sorted({p.case_id for p in predictions if p.state_trimmed})
    models = sorted({p.model for p in predictions if p.model})
    if agentjev is not None and agentjev.get("model") is None and models:
        agentjev["model"] = models[0]

    return {
        "schema_version": SCHEMA_VERSION,
        "issue": ISSUE,
        "evidence_mode": "synthetic",
        "evidence_basis": "local Evidence per ADR-0006; not production impact",
        "recorded_at": datetime.now(timezone.utc).isoformat(),
        "backend": backend,
        "runs": runs,
        "confidence_threshold": confidence_threshold,
        "dataset": {
            "path": str(DEFAULT_CASES_DIR.relative_to(ROOT)),
            "case_count": len(cases),
            "case_ids": [c["id"] for c in cases],
        },
        "agentjev": agentjev,
        "models_seen": models,
        "metrics": metrics,
        "token_fit": {
            "cases_trimmed": len(trimmed_ids),
            "trimmed_case_ids": trimmed_ids,
            "note": (
                "AgentJev rejects (never truncates) input over 2,048 tokens; "
                "fit_state_for_agentjev shrinks log tails before ask."
            ),
        },
        "predictions": [asdict(p) for p in predictions],
    }


def write_results(path: Path, results: Mapping[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(results, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def render_markdown_summary(results: Mapping[str, Any]) -> str:
    metrics = results["metrics"]
    agentjev = results.get("agentjev") or {}
    lines = [
        f"# Offline judge experiment (issue #{results['issue']})",
        "",
        f"- Recorded at: `{results.get('recorded_at', '')}`",
        f"- Backend: `{results['backend']}`",
        f"- Runs: {results['runs']}",
        f"- Cases: {results['dataset']['case_count']}",
        f"- Evidence: {results['evidence_basis']}",
        f"- Confidence threshold (withhold): {results['confidence_threshold']}",
        "",
        "## Key metrics",
        "",
        f"- `failure_cause` accuracy: **{metrics['failure_cause_accuracy']:.4f}**",
        f"- `failure_cause` calibration ECE: **{metrics['failure_cause_calibration_ece']:.4f}**",
        f"- Escalations a confident non-agent label would withhold: "
        f"**{metrics['escalations_withheld']}**",
        f"- Of those, wrong (hand label says higher tier would help): "
        f"**{metrics['escalations_withheld_wrong']}**",
        f"- Available / unavailable predictions: "
        f"{metrics['available_count']} / {metrics['unavailable_count']}",
        "",
        "## Per-cause accuracy",
        "",
        "| cause | correct | support |",
        "| --- | ---: | ---: |",
    ]
    for cause, row in metrics["per_cause"].items():
        lines.append(f"| `{cause}` | {row['correct']} | {row['support']} |")
    lines.extend(
        [
            "",
            "## Token fit (2,048)",
            "",
            f"- Cases needing trim before ask: {results['token_fit']['cases_trimmed']}",
            f"- Trimmed ids: {', '.join(results['token_fit']['trimmed_case_ids']) or '(none)'}",
            "",
            "## AgentJev",
            "",
        ]
    )
    if results["backend"] == "fake":
        lines.append("- N/A (fake oracle backend)")
    else:
        lines.append(f"- Model: `{agentjev.get('model')}`")
        lines.append(f"- Commit: `{agentjev.get('commit')}`")
        lines.append(f"- URL: `{agentjev.get('url')}`")
    lines.append("")
    return "\n".join(lines)


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--backend",
        default=None,
        help="fake|local (default: SDF_JUDGE_BACKEND or fake)",
    )
    parser.add_argument("--cases", type=Path, default=DEFAULT_CASES_DIR)
    parser.add_argument("--runs", type=int, default=DEFAULT_RUNS)
    parser.add_argument("--confidence-threshold", type=float, default=DEFAULT_CONFIDENCE_THRESHOLD)
    parser.add_argument(
        "--output",
        type=Path,
        default=ROOT / "artifacts" / "judge_experiment" / "results.json",
    )
    parser.add_argument(
        "--summary",
        type=Path,
        default=None,
        help="Optional markdown summary path",
    )
    parser.add_argument("--agentjev-url", default=None)
    args = parser.parse_args(list(argv) if argv is not None else None)

    backend = resolve_backend(args.backend)
    cases = load_cases(args.cases)
    results = run_experiment(
        backend=backend,
        cases=cases,
        runs=args.runs,
        confidence_threshold=args.confidence_threshold,
        agentjev_url=args.agentjev_url,
    )
    # Point dataset.path at the cases dir actually used.
    try:
        results["dataset"]["path"] = str(args.cases.resolve().relative_to(ROOT))
    except ValueError:
        results["dataset"]["path"] = str(args.cases)

    write_results(args.output, results)
    summary_path = args.summary
    if summary_path is None and args.output.suffix == ".json":
        summary_path = args.output.with_suffix(".md")
    if summary_path is not None:
        summary_path.parent.mkdir(parents=True, exist_ok=True)
        summary_path.write_text(render_markdown_summary(results), encoding="utf-8")
    print(f"wrote {args.output}")
    if summary_path is not None:
        print(f"wrote {summary_path}")
    print(
        f"accuracy={results['metrics']['failure_cause_accuracy']:.4f} "
        f"withheld={results['metrics']['escalations_withheld']} "
        f"withheld_wrong={results['metrics']['escalations_withheld_wrong']}"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
