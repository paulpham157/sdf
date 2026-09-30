"""Local AgentJev-0.6B Judge backend: fake process, fail-open, 2,048-token fit."""

from __future__ import annotations

import json
from urllib.error import URLError

import pytest

from sdf_core.judge import FAILURE_CAUSES, TRIAGE_QUESTIONS, validate_answers
from sdf_core.judge_agentjev import API_VERSION, TOKEN_LIMIT, to_agentjev_request
from sdf_core.judge_local import (
    DEFAULT_AGENTJEV_URL,
    DEFAULT_MODEL_NAME,
    LocalAgentJevJudge,
    LocalJudgeResult,
    estimate_tokens,
    fit_state_for_agentjev,
    make_agentjev_post,
)

FAILURE_CAUSE_LIST = list(FAILURE_CAUSES)

USAGE = {
    "questions": 2,
    "candidate_paths": 8,
    "input_path_tokens": 106,
    "generated_tokens": 0,
    "truncated_inputs": 0,
    "backbone_input_tokens": 106,
    "shared_prefix_questions": 0,
    "wall_ms": 12.0,
}

TRIAGE_STATE = {
    "task_instructions": "Make add(a, b) in calc.py return the sum.",
    "evaluator_status": "FAIL",
    "failed_evidence": [
        {
            "criterion": "add returns the sum",
            "command": "pytest -q",
            "exit_code": 1,
            "stderr_tail": "E   assert add(2, 3) == 5\n1 failed",
        }
    ],
    "adapter_stderr_tail": "",
    "diff_stat": "1 file, +1 -1",
}


def envelope(cause="environment_or_runtime", cause_p=0.41, help_p=0.12, model="AgentJev-0.6B"):
    rest = (1.0 - cause_p) / (len(FAILURE_CAUSE_LIST) - 1)
    distribution = {label: (cause_p if label == cause else rest) for label in FAILURE_CAUSE_LIST}
    return {
        "api_version": API_VERSION,
        "model": model,
        "results": [
            {
                "id": "0",
                "answers": [
                    {
                        "id": "failure_cause",
                        "type": "choice",
                        "distribution": distribution,
                        "value": cause,
                        "description": FAILURE_CAUSES[cause],
                        "top_probability": cause_p,
                        "margin": cause_p - rest,
                    },
                    {
                        "id": "higher_tier_would_help",
                        "type": "boolean",
                        "distribution": {"true": help_p, "false": 1.0 - help_p},
                        "probability": help_p,
                        "value": help_p >= 0.5,
                    },
                ],
            }
        ],
        "usage": dict(USAGE),
    }


class FakeProcess:
    """Stand-in for a local AgentJev HTTP process: records posts, returns a scripted body."""

    def __init__(self, response=None, *, error: BaseException | None = None):
        self.response = response if response is not None else envelope()
        self.error = error
        self.requests: list[dict] = []

    def __call__(self, request: dict):
        self.requests.append(request)
        if self.error is not None:
            raise self.error
        return self.response


# --- happy path through injected fake process ---


def test_local_judge_asks_fake_process_and_returns_translated_answers():
    process = FakeProcess(envelope(cause="credential_or_provider", cause_p=0.39, help_p=0.1))
    judge = LocalAgentJevJudge(process)

    result = judge.ask(TRIAGE_STATE, TRIAGE_QUESTIONS)

    assert isinstance(result, LocalJudgeResult)
    assert result.available is True
    assert result.unavailable_reason is None
    assert result.model == "AgentJev-0.6B"
    assert result.usage == USAGE
    assert process.requests == [to_agentjev_request(TRIAGE_STATE, TRIAGE_QUESTIONS)]
    assert result.answers is not None
    validate_answers(TRIAGE_QUESTIONS, result.answers)
    assert result.answers["failure_cause"]["choice"] == "credential_or_provider"
    assert result.answers["failure_cause"]["confidence"] == pytest.approx(0.39)
    assert result.answers["higher_tier_would_help"] == {"noul": pytest.approx(0.1)}


def test_local_judge_keeps_last_result():
    judge = LocalAgentJevJudge(FakeProcess())
    assert judge.last_result is None
    result = judge.ask(TRIAGE_STATE, TRIAGE_QUESTIONS)
    assert judge.last_result is result


# --- fail open ---


@pytest.mark.parametrize(
    "error",
    [
        URLError("connection refused"),
        TimeoutError("timed out"),
        OSError("broken pipe"),
        ConnectionError("reset"),
    ],
)
def test_local_judge_fails_open_on_transport_errors(error):
    judge = LocalAgentJevJudge(FakeProcess(error=error))

    result = judge.ask(TRIAGE_STATE, TRIAGE_QUESTIONS)

    assert result.available is False
    assert result.answers is None
    assert result.model is None
    assert result.unavailable_reason is not None
    assert str(error) in result.unavailable_reason
    assert judge.last_result is result


def test_local_judge_fails_open_on_service_error_body():
    judge = LocalAgentJevJudge(FakeProcess({"error": "model inference failed; consult service log"}))

    result = judge.ask(TRIAGE_STATE, TRIAGE_QUESTIONS)

    assert result.available is False
    assert result.answers is None
    assert "model inference failed" in (result.unavailable_reason or "")


def test_local_judge_fails_open_on_token_limit_rejection():
    message = "question 'failure_cause' needs 2301 tokens; limit 2048. Shorten the input; nothing was truncated."
    judge = LocalAgentJevJudge(FakeProcess({"error": message}))

    result = judge.ask(TRIAGE_STATE, TRIAGE_QUESTIONS)

    assert result.available is False
    assert "2,048" in (result.unavailable_reason or "")


def test_local_judge_fails_open_on_malformed_response():
    judge = LocalAgentJevJudge(FakeProcess({"api_version": "wrong", "model": "x", "results": []}))

    result = judge.ask(TRIAGE_STATE, TRIAGE_QUESTIONS)

    assert result.available is False
    assert result.answers is None


# --- HTTP post builder (stdlib only; opener injected) ---


def test_make_agentjev_post_posts_json_to_evaluate_and_returns_object():
    calls = []

    class FakeResponse:
        def __enter__(self):
            return self

        def __exit__(self, *args):
            return False

        def read(self):
            return json.dumps(envelope()).encode()

    def opener(request, timeout=None):
        calls.append((request.full_url, request.data, dict(request.header_items()), timeout))
        return FakeResponse()

    post = make_agentjev_post("http://127.0.0.1:8149", timeout_s=12.5, urlopen=opener)
    body = to_agentjev_request(TRIAGE_STATE, TRIAGE_QUESTIONS)
    response = post(body)

    assert response["model"] == "AgentJev-0.6B"
    assert len(calls) == 1
    url, data, headers, timeout = calls[0]
    assert url == "http://127.0.0.1:8149/api/evaluate"
    assert json.loads(data) == body
    assert headers["Content-type"] == "application/json"
    assert timeout == 12.5


def test_make_agentjev_post_fills_empty_model_from_info_then_default():
    bodies = {
        "http://127.0.0.1:8149/api/evaluate": json.dumps({**envelope(), "model": ""}).encode(),
        "http://127.0.0.1:8149/api/info": json.dumps({"model": "AgentJev-0.6B-phase4"}).encode(),
    }

    class FakeResponse:
        def __init__(self, raw):
            self._raw = raw

        def __enter__(self):
            return self

        def __exit__(self, *args):
            return False

        def read(self):
            return self._raw

    def opener(request, timeout=None):
        return FakeResponse(bodies[request.full_url])

    post = make_agentjev_post(urlopen=opener)
    assert post({"state": {}, "questions": []})["model"] == "AgentJev-0.6B-phase4"

    bodies["http://127.0.0.1:8149/api/info"] = b"{"
    post2 = make_agentjev_post(urlopen=opener, default_model=DEFAULT_MODEL_NAME)
    assert post2({"state": {}, "questions": []})["model"] == DEFAULT_MODEL_NAME


def test_make_agentjev_post_default_url_is_loopback():
    assert DEFAULT_AGENTJEV_URL == "http://127.0.0.1:8149"


def test_make_agentjev_post_raises_on_non_object_json():
    class FakeResponse:
        def __enter__(self):
            return self

        def __exit__(self, *args):
            return False

        def read(self):
            return b"[1]"

    post = make_agentjev_post(urlopen=lambda *a, **k: FakeResponse())
    with pytest.raises(ValueError, match="object"):
        post({"state": {}, "questions": []})


# --- 2,048-token state fit ---


def test_estimate_tokens_is_conservative_for_ascii():
    # One character per token would under-count; we use a floor of ~3 chars/token.
    assert estimate_tokens("abc") == 1
    assert estimate_tokens("a" * 300) == 100
    assert estimate_tokens("") == 0


def test_fit_state_keeps_small_state_unchanged():
    fitted = fit_state_for_agentjev(TRIAGE_STATE, questions=TRIAGE_QUESTIONS)
    assert fitted == TRIAGE_STATE
    assert estimate_tokens(json.dumps(fitted, separators=(",", ":"))) < TOKEN_LIMIT


def test_fit_state_truncates_oversized_tails_under_token_budget():
    huge = {
        "task_instructions": "Fix the bug. " + ("detail " * 5_000),
        "evaluator_status": "FAIL",
        "failed_evidence": [
            {
                "criterion": "tests pass",
                "command": "pytest -q",
                "exit_code": 1,
                "stderr_tail": "E " + ("y" * 50_000),
            },
            {
                "criterion": "lint clean",
                "command": "ruff check",
                "exit_code": 1,
                "stderr_tail": "F " + ("z" * 50_000),
            },
        ],
        "adapter_stderr_tail": "W " + ("w" * 50_000),
        "diff_stat": "3 files, +40 -2",
    }
    request = to_agentjev_request(huge, TRIAGE_QUESTIONS)
    assert estimate_tokens(json.dumps(request, separators=(",", ":"))) > TOKEN_LIMIT

    fitted = fit_state_for_agentjev(huge, questions=TRIAGE_QUESTIONS)
    fitted_request = to_agentjev_request(fitted, TRIAGE_QUESTIONS)
    assert estimate_tokens(json.dumps(fitted_request, separators=(",", ":"))) <= TOKEN_LIMIT
    assert fitted["evaluator_status"] == "FAIL"
    assert fitted["diff_stat"] == "3 files, +40 -2"
    assert fitted["failed_evidence"][0]["criterion"] == "tests pass"
    # Prefer cutting log tails (keep the trailing failure signal), not criterion labels.
    assert fitted["failed_evidence"][0]["stderr_tail"]
    assert fitted["failed_evidence"][0]["stderr_tail"].endswith("y")
    assert len(fitted["adapter_stderr_tail"]) < len(huge["adapter_stderr_tail"])
    assert len(json.dumps(fitted)) < len(json.dumps(huge))


def test_fit_state_does_not_mutate_input():
    state = {
        "task_instructions": "t",
        "evaluator_status": "FAIL",
        "failed_evidence": [{"criterion": "c", "command": "x", "exit_code": 1, "stderr_tail": "e" * 20_000}],
        "adapter_stderr_tail": "a" * 20_000,
        "diff_stat": "unknown",
    }
    before = json.dumps(state)
    fit_state_for_agentjev(state, questions=TRIAGE_QUESTIONS)
    assert json.dumps(state) == before
