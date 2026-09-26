from __future__ import annotations

import copy

import pytest

from sdf_core.judge_agentjev import (
    API_VERSION,
    AgentJevError,
    AgentJevJudge,
    AgentJevReply,
    from_agentjev_response,
    parse_agentjev_reply,
    to_agentjev_request,
)

FAILURE_CAUSES = {
    "agent_solution_wrong": "The agent changed code but the checks show its change is incorrect or incomplete",
    "agent_did_not_attempt": "The diff is empty or unrelated to the instructions",
    "environment_or_runtime": "Sandbox, network, timeout, or process failure unrelated to the code change",
    "credential_or_provider": "Model provider authentication, quota, or rate-limit error",
    "task_or_fixture_defect": "The acceptance check itself is broken or contradicts the instructions",
    "not_stated": "The state does not show enough to tell",
}

TRIAGE_QUESTIONS = {
    "failure_cause": {
        "type": "choice",
        "instructions": "What most likely caused this Attempt's verified failure?",
        "criteria": FAILURE_CAUSES,
    },
    "higher_tier_would_help": {
        "type": "noul",
        "instructions": "Would a more capable coding model plausibly fix this failure without changing the task or environment?",
    },
}

TRIAGE_STATE = {
    "task_instructions": "Make the failing test pass.",
    "evaluator_status": "FAIL",
    "failed_evidence": [{"criterion": "tests pass", "command": "pytest -q", "exit_code": 1, "stderr_tail": "AssertionError"}],
    "adapter_stderr_tail": "",
    "diff_stat": "1 file, +3 -1",
}

USAGE = {
    "questions": 1,
    "candidate_paths": 3,
    "input_path_tokens": 106,
    "generated_tokens": 0,
    "truncated_inputs": 0,
    "backbone_input_tokens": 106,
    "shared_prefix_questions": 0,
    "wall_ms": 51.58,
}

# Minimal literals copied from the prototype's recorded verification.json.
COMPLETION_ANSWER = {
    "id": "completion",
    "type": "boolean",
    "distribution": {"true": 2.014569145103451e-05, "false": 0.9999798536300659},
    "probability": 2.014569145103451e-05,
    "value": False,
}
CHOICE_ANSWER = {
    "id": "pick",
    "type": "choice",
    "distribution": {"pass": 0.7645729184150696, "fail": 0.11679393798112869, "unknown": 0.11863316595554352},
    "value": "pass",
    "description": "All required tests pass.",
    "top_probability": 0.7645729184150696,
    "margin": 0.6459397524595261,
}
SCORE_ANSWER = {
    "id": "extra",
    "type": "score",
    "distribution": {"0": 0.09739620983600616, "1": 0.1716046929359436, "2": 0.7309991121292114},
    "score": 1.6336029171943665,
    "level": 2,
    "legend": ["No solution exists.", "Work is in progress.", "All required tests pass."],
}

MIXED_QUESTIONS = {
    "pick": {
        "type": "choice",
        "instructions": "Which label fits?",
        "criteria": {"pass": "All required tests pass.", "fail": "A required test fails.", "unknown": "Not enough information."},
    },
    "extra": {"type": "score", "instructions": "How far along is the work?", "levels": list(SCORE_ANSWER["legend"])},
    "completion": {
        "type": "noul",
        "instructions": "Is the task complete?",
        "criteria": {"true": "The task is complete.", "false": "The task is not complete."},
    },
}


def envelope(*answers, model="phase4"):
    return {"api_version": API_VERSION, "model": model, "results": [{"id": "0", "answers": list(answers)}], "usage": dict(USAGE)}


MIXED_RESPONSE = envelope(CHOICE_ANSWER, SCORE_ANSWER, COMPLETION_ANSWER)


def triage_response(cause_p=0.44, help_p=0.31):
    rest = (1 - cause_p) / 5
    distribution = {label: (cause_p if label == "environment_or_runtime" else rest) for label in FAILURE_CAUSES}
    return envelope(
        {
            "id": "failure_cause",
            "type": "choice",
            "distribution": distribution,
            "value": "environment_or_runtime",
            "description": FAILURE_CAUSES["environment_or_runtime"],
            "top_probability": cause_p,
            "margin": cause_p - rest,
        },
        {
            "id": "higher_tier_would_help",
            "type": "boolean",
            "distribution": {"true": help_p, "false": 1 - help_p},
            "probability": help_p,
            "value": help_p >= 0.5,
        },
        model="AgentJev-0.6B-phase4",
    )


# --- request translation ---


def test_request_maps_noul_to_boolean_and_instructions_to_question():
    request = to_agentjev_request(TRIAGE_STATE, TRIAGE_QUESTIONS)

    assert request["state"] == TRIAGE_STATE
    assert request["questions"] == [
        {
            "id": "failure_cause",
            "type": "choice",
            "question": "What most likely caused this Attempt's verified failure?",
            "options": FAILURE_CAUSES,
        },
        {
            "id": "higher_tier_would_help",
            "type": "boolean",
            "question": "Would a more capable coding model plausibly fix this failure without changing the task or environment?",
        },
    ]


def test_request_keeps_option_order_and_noul_criteria_and_score_levels():
    request = to_agentjev_request({"x": 1}, MIXED_QUESTIONS)
    pick, extra, completion = request["questions"]

    assert list(pick["options"]) == ["pass", "fail", "unknown"]
    assert extra == {"id": "extra", "type": "score", "question": "How far along is the work?", "levels": SCORE_ANSWER["legend"]}
    assert completion["type"] == "boolean"
    assert completion["criteria"] == {"true": "The task is complete.", "false": "The task is not complete."}


def test_request_is_plain_json_even_for_mapping_proxies():
    from types import MappingProxyType

    frozen = MappingProxyType({"q": MappingProxyType({"type": "choice", "instructions": "?", "criteria": MappingProxyType({"a": "A", "b": "B"})})})
    request = to_agentjev_request(MappingProxyType({"k": "v"}), frozen)

    assert type(request["state"]) is dict
    assert type(request["questions"][0]["options"]) is dict


def test_request_rejects_unknown_question_type():
    with pytest.raises(ValueError, match="unknown question type"):
        to_agentjev_request({}, {"q": {"type": "boolean", "instructions": "?"}})


def test_request_rejects_empty_questions():
    with pytest.raises(ValueError, match="non-empty"):
        to_agentjev_request({}, {})


# --- response translation ---


def test_boolean_probability_maps_to_noul():
    answers = from_agentjev_response(envelope(COMPLETION_ANSWER), {"completion": MIXED_QUESTIONS["completion"]})

    assert answers == {"completion": {"noul": 2.014569145103451e-05}}


def test_choice_maps_distribution_value_and_top_probability_confidence():
    answers = from_agentjev_response(envelope(CHOICE_ANSWER), {"pick": MIXED_QUESTIONS["pick"]})

    assert answers["pick"] == {
        "choice": "pass",
        "probabilities": CHOICE_ANSWER["distribution"],
        "confidence": 0.7645729184150696,
    }


def test_score_maps_score_probabilities_max_confidence_and_legend():
    answers = from_agentjev_response(envelope(SCORE_ANSWER), {"extra": MIXED_QUESTIONS["extra"]})

    assert answers["extra"] == {
        "score": 1.6336029171943665,
        "probabilities": SCORE_ANSWER["distribution"],
        "confidence": 0.7309991121292114,
        "legend": SCORE_ANSWER["legend"],
    }


def test_answers_are_keyed_by_question_id_regardless_of_order():
    answers = from_agentjev_response(MIXED_RESPONSE, MIXED_QUESTIONS)

    assert list(answers) == ["pick", "extra", "completion"]


def test_reply_exposes_model_and_usage():
    reply = parse_agentjev_reply(MIXED_RESPONSE, MIXED_QUESTIONS)

    assert isinstance(reply, AgentJevReply)
    assert reply.model == "phase4"
    assert reply.usage == USAGE
    assert reply.answers == from_agentjev_response(MIXED_RESPONSE, MIXED_QUESTIONS)


def mutated(fn):
    response = copy.deepcopy(MIXED_RESPONSE)
    fn(response)
    return response


def answer_of(response, qid):
    return next(a for a in response["results"][0]["answers"] if a["id"] == qid)


@pytest.mark.parametrize(
    ("mutate", "message"),
    [
        (lambda r: r.update(api_version="jev.v1"), "api_version"),
        (lambda r: r.pop("api_version"), "api_version"),
        (lambda r: r.update(model=""), "model"),
        (lambda r: r.update(results=[]), "exactly one result"),
        (lambda r: r["results"].append(copy.deepcopy(r["results"][0])), "exactly one result"),
        (lambda r: r["results"][0].update(answers="nope"), "answers"),
        (lambda r: r["results"][0]["answers"].pop(), "answer ids"),
        (lambda r: answer_of(r, "pick").update(id="other"), "answer ids"),
        (lambda r: answer_of(r, "pick").update(id=1), "answer ids"),
        (lambda r: r["results"][0]["answers"].append(copy.deepcopy(CHOICE_ANSWER)), "answer ids"),
        (lambda r: answer_of(r, "pick").update(type="score"), "type"),
        (lambda r: answer_of(r, "completion").update(type="noul"), "type"),
        (lambda r: answer_of(r, "pick")["distribution"].pop("unknown"), "distribution"),
        (lambda r: answer_of(r, "pick")["distribution"].update(maybe=0.0), "distribution"),
        (lambda r: answer_of(r, "completion").update(distribution={"yes": 0.1, "no": 0.9}), "distribution"),
        (lambda r: answer_of(r, "extra")["distribution"].pop("2"), "distribution"),
        (lambda r: answer_of(r, "pick")["distribution"].update({"pass": 1.5}), "probability"),
        (lambda r: answer_of(r, "pick")["distribution"].update({"pass": True}), "probability"),
        (lambda r: answer_of(r, "completion").update(probability="high"), "probability"),
        (lambda r: answer_of(r, "pick").update(value="maybe"), "value"),
        (lambda r: answer_of(r, "pick").update(top_probability=-0.1), "top_probability"),
        (lambda r: answer_of(r, "extra").update(score=7.0), "score"),
        (lambda r: answer_of(r, "extra").update(legend=["a", "b", "c"]), "legend"),
        (lambda r: r.update(usage="lots"), "usage"),
    ],
)
def test_response_validation_errors(mutate, message):
    response = mutated(mutate)
    with pytest.raises(ValueError, match=message):
        from_agentjev_response(response, MIXED_QUESTIONS)


def test_response_must_be_a_mapping():
    with pytest.raises(ValueError, match="object"):
        from_agentjev_response([], MIXED_QUESTIONS)


def test_usage_is_optional():
    response = mutated(lambda r: r.pop("usage"))

    assert parse_agentjev_reply(response, MIXED_QUESTIONS).usage == {}


# --- judge ---


class FakePost:
    def __init__(self, response):
        self.response = response
        self.requests = []

    def __call__(self, request):
        self.requests.append(request)
        return self.response


def test_judge_round_trips_triage_questions_through_injected_post():
    post = FakePost(triage_response())
    judge = AgentJevJudge(post)

    answers = judge.ask(TRIAGE_STATE, TRIAGE_QUESTIONS)

    assert post.requests == [to_agentjev_request(TRIAGE_STATE, TRIAGE_QUESTIONS)]
    assert answers["failure_cause"]["choice"] == "environment_or_runtime"
    assert answers["failure_cause"]["confidence"] == pytest.approx(0.44)
    assert list(answers["failure_cause"]["probabilities"]) == list(FAILURE_CAUSES)
    assert answers["higher_tier_would_help"] == {"noul": pytest.approx(0.31)}
    assert judge.last_reply == AgentJevReply(answers, "AgentJev-0.6B-phase4", USAGE)


def test_judge_answers_satisfy_shared_contract_validator():
    from sdf_core.judge import validate_answers

    answers = AgentJevJudge(FakePost(triage_response())).ask(TRIAGE_STATE, TRIAGE_QUESTIONS)
    validate_answers(TRIAGE_QUESTIONS, answers)
    validate_answers(MIXED_QUESTIONS, from_agentjev_response(MIXED_RESPONSE, MIXED_QUESTIONS))


def test_judge_raises_token_limit_error_on_rejected_long_input():
    message = "question 'failure_cause' needs 2301 tokens; limit 2048. Shorten the input; nothing was truncated."
    judge = AgentJevJudge(FakePost({"error": message}))

    with pytest.raises(AgentJevError, match="2,048-token") as info:
        judge.ask(TRIAGE_STATE, TRIAGE_QUESTIONS)
    assert "nothing was truncated" in str(info.value)
    assert judge.last_reply is None


def test_judge_raises_plain_service_error():
    judge = AgentJevJudge(FakePost({"error": "model inference failed; consult service log"}))

    with pytest.raises(AgentJevError, match="model inference failed") as info:
        judge.ask(TRIAGE_STATE, TRIAGE_QUESTIONS)
    assert "2,048" not in str(info.value)


def test_judge_rejects_unknown_question_type_before_posting():
    post = FakePost(triage_response())

    with pytest.raises(ValueError):
        AgentJevJudge(post).ask(TRIAGE_STATE, {"q": {"type": "boolean", "instructions": "?"}})
    assert post.requests == []


def test_module_has_no_transport_imports():
    import sdf_core.judge_agentjev as module
    from pathlib import Path

    source = Path(module.__file__).read_text()
    for name in ("urllib", "requests", "http.client", "socket", "subprocess", "os.environ"):
        assert name not in source
