"""Pure wire translation between the shared Judge contract and AgentJev's agentjev.decision.v1; transport is injected."""

from __future__ import annotations

from collections.abc import Callable, Mapping
from dataclasses import dataclass, field
from typing import Any

from sdf_core.judge import probability, validate_questions

API_VERSION = "agentjev.decision.v1"
TOKEN_LIMIT = 2048

_WIRE_TYPES = {"noul": "boolean", "choice": "choice", "score": "score"}

class AgentJevError(RuntimeError):
    """Raised when the AgentJev service answers with an error body."""

@dataclass(frozen=True)
class AgentJevReply:
    answers: dict[str, dict[str, Any]]
    model: str
    usage: dict[str, Any] = field(default_factory=dict)

def _plain(value: Any) -> Any:
    if isinstance(value, Mapping):
        return {key: _plain(item) for key, item in value.items()}
    if isinstance(value, (list, tuple)):
        return [_plain(item) for item in value]
    return value

def to_agentjev_request(state: Mapping[str, Any], questions: Mapping[str, Mapping[str, Any]]) -> dict[str, Any]:
    validate_questions(questions)
    wire = []
    for qid, question in questions.items():
        kind = question["type"]
        item: dict[str, Any] = {"id": qid, "type": _WIRE_TYPES[kind], "question": question["instructions"]}
        if kind == "noul":
            if question.get("criteria") is not None:
                item["criteria"] = _plain(question["criteria"])
        elif kind == "choice":
            item["options"] = _plain(question["criteria"])
        else:
            item["levels"] = _plain(question["levels"])
        wire.append(item)
    return {"state": _plain(state), "questions": wire}

def _keys(question: Mapping[str, Any]) -> list[str]:
    kind = question["type"]
    if kind == "noul":
        return ["true", "false"]
    if kind == "choice":
        return list(question["criteria"])
    return [str(i) for i in range(len(question["levels"]))]

def _translate(qid: str, question: Mapping[str, Any], answer: Mapping[str, Any]) -> dict[str, Any]:
    kind = question["type"]
    if answer.get("type") != _WIRE_TYPES[kind]:
        raise ValueError(f"answer type for {qid!r} must be {_WIRE_TYPES[kind]!r}, got {answer.get('type')!r}")
    keys = _keys(question)
    distribution = answer.get("distribution")
    if not isinstance(distribution, Mapping) or set(distribution) != set(keys):
        raise ValueError(f"distribution keys for {qid!r} must be {keys}")
    probabilities = {key: probability(qid, "probability", distribution[key]) for key in keys}
    if kind == "noul":
        return {"noul": probability(qid, "probability", answer.get("probability"))}
    if kind == "choice":
        if answer.get("value") not in probabilities:
            raise ValueError(f"choice value for {qid!r} is not among the options")
        return {
            "choice": answer["value"],
            "probabilities": probabilities,
            "confidence": probability(qid, "top_probability", answer.get("top_probability")),
        }
    levels = list(question["levels"])
    score = answer.get("score")
    if isinstance(score, bool) or not isinstance(score, (int, float)) or not 0 <= score <= len(levels) - 1:
        raise ValueError(f"score for {qid!r} must be within the level index range")
    if list(answer.get("legend", ())) != levels:
        raise ValueError(f"legend for {qid!r} must repeat the question levels")
    return {"score": float(score), "probabilities": probabilities, "confidence": max(probabilities.values()), "legend": levels}

def _service_error(message: Any) -> AgentJevError:
    text = str(message)
    if str(TOKEN_LIMIT) in text and "token" in text:
        return AgentJevError(f"AgentJev rejected input over its 2,048-token limit (it rejects, never truncates): {text}")
    return AgentJevError(f"AgentJev service error: {text}")

def parse_agentjev_reply(response: Any, questions: Mapping[str, Mapping[str, Any]]) -> AgentJevReply:
    validate_questions(questions)
    if not isinstance(response, Mapping):
        raise ValueError("AgentJev response must be an object")
    if "error" in response:
        raise _service_error(response["error"])
    if response.get("api_version") != API_VERSION:
        raise ValueError(f"api_version must be {API_VERSION!r}, got {response.get('api_version')!r}")
    model = response.get("model")
    if not isinstance(model, str) or not model:
        raise ValueError("model must be a non-empty string")
    usage = response.get("usage", {})
    if not isinstance(usage, Mapping):
        raise ValueError("usage must be an object")
    results = response.get("results")
    if not isinstance(results, list) or len(results) != 1 or not isinstance(results[0], Mapping):
        raise ValueError("AgentJev response must contain exactly one result")
    answers = results[0].get("answers")
    if not isinstance(answers, list) or not all(isinstance(answer, Mapping) for answer in answers):
        raise ValueError("result answers must be a list of objects")
    ids = [answer.get("id") for answer in answers]
    if sorted(map(str, ids)) != sorted(questions) or len(set(ids)) != len(ids):
        raise ValueError(f"answer ids {ids} must match question ids {list(questions)}")
    by_id = {answer["id"]: answer for answer in answers}
    translated = {qid: _translate(qid, question, by_id[qid]) for qid, question in questions.items()}
    return AgentJevReply(translated, model, dict(usage))

def from_agentjev_response(response: Any, questions: Mapping[str, Mapping[str, Any]]) -> dict[str, dict[str, Any]]:
    return parse_agentjev_reply(response, questions).answers

class AgentJevJudge:
    """Judge over an injected `post(request) -> response`; keeps the last reply for model-version recording."""

    def __init__(self, post: Callable[[dict[str, Any]], Mapping[str, Any]]) -> None:
        self._post = post
        self.last_reply: AgentJevReply | None = None

    def ask(self, state: Mapping[str, Any], questions: Mapping[str, Mapping[str, Any]]) -> dict[str, dict[str, Any]]:
        self.last_reply = None
        request = to_agentjev_request(state, questions)
        self.last_reply = parse_agentjev_reply(self._post(request), questions)
        return self.last_reply.answers
