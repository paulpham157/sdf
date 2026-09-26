import ast
from pathlib import Path

import pytest

from sdf_core.judge import (
    FAILURE_CAUSES,
    TRIAGE_QUESTIONS,
    FakeJudge,
    Judge,
    JudgeScriptExhaustedError,
    validate_answers,
    validate_questions,
)

ROOT = Path(__file__).resolve().parents[1]

def triage_answer(label="environment_or_runtime", p_true=0.2):
    probabilities = {name: 0.0 for name in FAILURE_CAUSES}
    probabilities[label] = 1.0
    return {
        "failure_cause": {"choice": label, "probabilities": probabilities, "confidence": 0.9},
        "higher_tier_would_help": {"noul": p_true},
    }

def test_fake_judge_satisfies_judge_protocol():
    judge: Judge = FakeJudge([])
    assert callable(judge.ask)

def test_fake_judge_returns_scripted_answers_in_order_and_records_calls():
    first, second = triage_answer("agent_solution_wrong", 0.8), triage_answer("not_stated", 0.5)
    judge = FakeJudge([first, second])
    state_a, state_b = {"evaluator_status": "FAIL"}, {"evaluator_status": "ERROR"}
    assert judge.ask(state_a, TRIAGE_QUESTIONS) == first
    assert judge.ask(state_b, TRIAGE_QUESTIONS) == second
    assert judge.calls == [(state_a, TRIAGE_QUESTIONS), (state_b, TRIAGE_QUESTIONS)]

def test_fake_judge_raises_when_script_is_exhausted():
    judge = FakeJudge([triage_answer()])
    judge.ask({}, TRIAGE_QUESTIONS)
    with pytest.raises(JudgeScriptExhaustedError, match="exhausted"):
        judge.ask({}, TRIAGE_QUESTIONS)

def test_fake_judge_rejects_scripted_answer_that_does_not_match_questions():
    judge = FakeJudge([{"failure_cause": triage_answer()["failure_cause"]}])
    with pytest.raises(ValueError, match="answer keys"):
        judge.ask({}, TRIAGE_QUESTIONS)

def test_fake_judge_rejects_invalid_questions():
    judge = FakeJudge([triage_answer()])
    with pytest.raises(ValueError, match="unknown question type"):
        judge.ask({}, {"q": {"type": "essay", "instructions": "x"}})

def test_triage_questions_match_research_section_4_1():
    assert list(FAILURE_CAUSES) == [
        "agent_solution_wrong",
        "agent_did_not_attempt",
        "environment_or_runtime",
        "credential_or_provider",
        "task_or_fixture_defect",
        "not_stated",
    ]
    assert FAILURE_CAUSES["not_stated"] == "The state does not show enough to tell"
    assert set(TRIAGE_QUESTIONS) == {"failure_cause", "higher_tier_would_help"}
    cause = TRIAGE_QUESTIONS["failure_cause"]
    assert cause["type"] == "choice"
    assert cause["instructions"] == "What most likely caused this Attempt's verified failure?"
    assert dict(cause["criteria"]) == dict(FAILURE_CAUSES)
    tier = TRIAGE_QUESTIONS["higher_tier_would_help"]
    assert tier["type"] == "noul"
    assert tier["instructions"] == (
        "Would a more capable coding model plausibly fix this failure without changing the task or environment?"
    )
    validate_questions(TRIAGE_QUESTIONS)

def test_triage_questions_are_immutable():
    with pytest.raises(TypeError):
        TRIAGE_QUESTIONS["extra"] = {}
    with pytest.raises(TypeError):
        TRIAGE_QUESTIONS["failure_cause"]["type"] = "noul"
    with pytest.raises(TypeError):
        FAILURE_CAUSES["not_stated"] = "changed"

SCORE_QUESTION = {"q": {"type": "score", "instructions": "How good?", "levels": ["bad", "ok", "good"]}}

@pytest.mark.parametrize(
    ("questions", "message"),
    [
        ({"q": {"type": "essay", "instructions": "x"}}, "unknown question type"),
        ({"q": {"type": "noul"}}, "instructions"),
        ({"q": {"type": "choice", "instructions": "x", "criteria": {"only": "one"}}}, "2..255"),
        (
            {"q": {"type": "choice", "instructions": "x", "criteria": {f"l{i}": "d" for i in range(256)}}},
            "2..255",
        ),
        ({"q": {"type": "score", "instructions": "x", "levels": ["one"]}}, "2..10"),
        ({"q": {"type": "score", "instructions": "x", "levels": [str(i) for i in range(11)]}}, "2..10"),
        ({"q": {"type": "noul", "instructions": "x", "criteria": {"yes": "a", "no": "b"}}}, "true"),
    ],
)
def test_validate_questions_rejects_shape_violations(questions, message):
    with pytest.raises(ValueError, match=message):
        validate_questions(questions)

def test_validate_answers_accepts_score_shape():
    validate_answers(
        SCORE_QUESTION,
        {"q": {"score": 1.4, "probabilities": {"0": 0.1, "1": 0.4, "2": 0.5}, "confidence": 0.7,
               "legend": ["bad", "ok", "good"]}},
    )

@pytest.mark.parametrize(
    ("questions", "answers", "message"),
    [
        (TRIAGE_QUESTIONS, {"failure_cause": triage_answer()["failure_cause"]}, "answer keys"),
        (TRIAGE_QUESTIONS, {**triage_answer(), "extra": {"noul": 0.5}}, "answer keys"),
        (
            TRIAGE_QUESTIONS,
            {**triage_answer(), "failure_cause": {"choice": "made_up", "probabilities": {}, "confidence": 0.5}},
            "not among criteria",
        ),
        (TRIAGE_QUESTIONS, {**triage_answer(), "higher_tier_would_help": {"noul": 1.5}}, r"\[0, 1\]"),
        (
            TRIAGE_QUESTIONS,
            {**triage_answer(), "failure_cause": {**triage_answer()["failure_cause"], "confidence": -0.1}},
            r"\[0, 1\]",
        ),
        (
            TRIAGE_QUESTIONS,
            {**triage_answer(), "failure_cause": {**triage_answer()["failure_cause"],
                                                  "probabilities": {**{label: 0.0 for label in FAILURE_CAUSES}, "not_stated": 2.0}}},
            r"\[0, 1\]",
        ),
        (
            TRIAGE_QUESTIONS,
            {**triage_answer(), "failure_cause": {**triage_answer()["failure_cause"],
                                                  "probabilities": {"not_stated": 1.0}}},
            "probability labels",
        ),
        (
            SCORE_QUESTION,
            {"q": {"score": 1.0, "probabilities": {"0": 0.2}, "confidence": 0.5, "legend": ["bad", "ok", "good"]}},
            "probability labels",
        ),
        (
            SCORE_QUESTION,
            {"q": {"score": 1.0, "probabilities": {"0": -0.2, "1": 0.7, "2": 0.5}, "confidence": 0.5, "legend": ["bad", "ok", "good"]}},
            r"\[0, 1\]",
        ),
    ],
)
def test_validate_answers_rejects_shape_violations(questions, answers, message):
    with pytest.raises(ValueError, match=message):
        validate_answers(questions, answers)

@pytest.mark.parametrize("module", ["execution.py", "escalation.py"])
def test_execution_and_escalation_do_not_import_judge_modules(module):
    forbidden = {"judge", "judge_state", "judge_agentjev"}
    tree = ast.parse((ROOT / "sdf_core" / module).read_text())
    imported: set[str] = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            imported.update(alias.name.split(".")[-1] for alias in node.names)
        elif isinstance(node, ast.ImportFrom):
            if node.module:
                imported.add(node.module.split(".")[-1])
            imported.update(alias.name for alias in node.names)
    assert not imported & forbidden
