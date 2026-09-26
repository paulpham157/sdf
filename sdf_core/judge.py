"""Advisory typed-judgment seam (ADR-0010); judge output never decides PASS or escalation."""

from __future__ import annotations

from collections.abc import Iterable, Mapping
from types import MappingProxyType
from typing import Any, Protocol

QUESTION_TYPES = ("noul", "choice", "score")


class Judge(Protocol):
    def ask(self, state: Mapping[str, Any], questions: Mapping[str, Mapping[str, Any]]) -> dict[str, dict[str, Any]]: ...


class JudgeScriptExhaustedError(RuntimeError):
    """Raised when a FakeJudge is asked more often than it was scripted."""


def validate_questions(questions: Mapping[str, Mapping[str, Any]]) -> None:
    if not isinstance(questions, Mapping) or not questions:
        raise ValueError("questions must be a non-empty mapping")
    for key, question in questions.items():
        if not isinstance(key, str) or not key:
            raise ValueError("question ids must be non-empty strings")
        if not isinstance(question, Mapping):
            raise ValueError(f"question {key!r} must be a mapping")
        kind = question.get("type")
        if kind not in QUESTION_TYPES:
            raise ValueError(f"unknown question type for {key!r}: {kind!r}")
        instructions = question.get("instructions")
        if not isinstance(instructions, str) or not instructions.strip():
            raise ValueError(f"question {key!r} needs non-empty instructions")
        if kind == "noul":
            criteria = question.get("criteria")
            if criteria is not None and (not isinstance(criteria, Mapping) or set(criteria) != {"true", "false"}):
                raise ValueError(f"noul criteria for {key!r} must have exactly 'true' and 'false'")
        elif kind == "choice":
            criteria = question.get("criteria")
            if not isinstance(criteria, Mapping) or not 2 <= len(criteria) <= 255:
                raise ValueError(f"choice {key!r} needs 2..255 criteria labels")
            if not all(isinstance(label, str) and label for label in criteria):
                raise ValueError(f"choice {key!r} labels must be non-empty strings")
        else:
            levels = question.get("levels")
            if not isinstance(levels, (list, tuple)) or not 2 <= len(levels) <= 10:
                raise ValueError(f"score {key!r} needs 2..10 levels")
            if not all(isinstance(level, str) and level for level in levels):
                raise ValueError(f"score {key!r} levels must be non-empty strings")


def probability(key: str, field: str, value: Any) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not 0.0 <= value <= 1.0:
        raise ValueError(f"{field} for {key!r} must be a number in [0, 1]")
    return float(value)


def _probabilities(key: str, probabilities: Any, allowed: Iterable[str]) -> None:
    if not isinstance(probabilities, Mapping):
        raise ValueError(f"probabilities for {key!r} must be a mapping")
    allowed = set(allowed)
    if set(probabilities) != allowed:
        raise ValueError(f"probability labels for {key!r} must match criteria")
    for label, p in probabilities.items():
        if label not in allowed:
            raise ValueError(f"probability label {label!r} for {key!r} is not among criteria")
        probability(key, "probability", p)


def validate_answers(questions: Mapping[str, Mapping[str, Any]], answers: Mapping[str, Mapping[str, Any]]) -> None:
    validate_questions(questions)
    if not isinstance(answers, Mapping) or set(answers) != set(questions):
        raise ValueError("answer keys must match question keys")
    for key, question in questions.items():
        answer = answers[key]
        if not isinstance(answer, Mapping):
            raise ValueError(f"answer {key!r} must be a mapping")
        kind = question["type"]
        if kind == "noul":
            probability(key, "noul", answer.get("noul"))
            continue
        probability(key, "confidence", answer.get("confidence"))
        if kind == "choice":
            if answer.get("choice") not in question["criteria"]:
                raise ValueError(f"choice for {key!r} is not among criteria")
            _probabilities(key, answer.get("probabilities"), question["criteria"])
        else:
            levels = list(question["levels"])
            score = answer.get("score")
            if isinstance(score, bool) or not isinstance(score, (int, float)) or not 0 <= score <= len(levels) - 1:
                raise ValueError(f"score for {key!r} must be within the level index range")
            _probabilities(key, answer.get("probabilities"), (str(i) for i in range(len(levels))))
            if list(answer.get("legend", ())) != levels:
                raise ValueError(f"legend for {key!r} must repeat the question levels")


class FakeJudge:
    """Deterministic judge for local tests; returns scripted answers in order, no network."""

    def __init__(self, script: Iterable[dict[str, dict[str, Any]]]) -> None:
        self._script = list(script)
        self.calls: list[tuple[Mapping[str, Any], Mapping[str, Mapping[str, Any]]]] = []

    def ask(self, state: Mapping[str, Any], questions: Mapping[str, Mapping[str, Any]]) -> dict[str, dict[str, Any]]:
        validate_questions(questions)
        self.calls.append((state, questions))
        if len(self.calls) > len(self._script):
            raise JudgeScriptExhaustedError(f"FakeJudge script exhausted after {len(self._script)} answer(s)")
        answers = self._script[len(self.calls) - 1]
        validate_answers(questions, answers)
        return answers


FAILURE_CAUSES: Mapping[str, str] = MappingProxyType(
    {
        "agent_solution_wrong": "The agent changed code but the checks show its change is incorrect or incomplete",
        "agent_did_not_attempt": "The diff is empty or unrelated to the instructions",
        "environment_or_runtime": "Sandbox, network, timeout, or process failure unrelated to the code change",
        "credential_or_provider": "Model provider authentication, quota, or rate-limit error",
        "task_or_fixture_defect": "The acceptance check itself is broken or contradicts the instructions",
        "not_stated": "The state does not show enough to tell",
    }
)

TRIAGE_QUESTIONS: Mapping[str, Mapping[str, Any]] = MappingProxyType(
    {
        "failure_cause": MappingProxyType(
            {
                "type": "choice",
                "instructions": "What most likely caused this Attempt's verified failure?",
                "criteria": FAILURE_CAUSES,
            }
        ),
        "higher_tier_would_help": MappingProxyType(
            {
                "type": "noul",
                "instructions": "Would a more capable coding model plausibly fix this failure without changing the task or environment?",
            }
        ),
    }
)
