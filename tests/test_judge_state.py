import json

import pytest

from sdf_core.evaluator import EvaluationResult, Evidence
from sdf_core.judge_state import build_triage_state, redact

DUMMY_KEY = "sk-ant-api03-DUMMYdummy0123456789abcdef"
PLAIN_SECRET = "hunter2-correct-horse-battery"
KEYS = {"task_instructions", "evaluator_status", "failed_evidence", "adapter_stderr_tail", "diff_stat"}


def evidence(status="FAIL", *, stderr="", stdout="", command="pytest -q", criterion="tests pass", exit_code=1, n=1):
    return Evidence(
        evidence_id=f"EVIDENCE-A-{n}", attempt_id="A", kind="evaluation", status=status,
        command=command, exit_code=exit_code, stdout=stdout, stderr=stderr, confidence=0.95, criterion=criterion,
    )


def evaluation(*items, status="FAIL"):
    return EvaluationResult(status=status, evidence=tuple(items))


@pytest.mark.parametrize(
    "text, leaked",
    [
        ("key sk-ant-api03-abcDEF0123456789xyz end", "abcDEF0123456789xyz"),
        ("OPENAI sk-proj-abcdefghijklmnop0123 end", "abcdefghijklmnop0123"),
        ("curl -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig'", "eyJhbGciOiJIUzI1NiJ9"),
        ("Authorization: Basic dXNlcjpwYXNzd29yZA==", "dXNlcjpwYXNzd29yZA"),
        ("x-api-key: live_abcdef0123456789", "live_abcdef0123456789"),
        ("X-Api-Key=live_abcdef0123456789", "live_abcdef0123456789"),
        ("token ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"),
        ("gho_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123"),
        ("github_pat_11ABCDEFG0123456789_abcdefghijklmnop", "11ABCDEFG0123456789"),
        ("aws AKIAIOSFODNN7EXAMPLE here", "AKIAIOSFODNN7EXAMPLE"),
        ("E2B e2b_0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef"),
        ("export ANTHROPIC_API_KEY=plainvalue123", "plainvalue123"),
        ("GITHUB_TOKEN='quoted value 1'", "quoted value 1"),
        ("DB_PASSWORD: s3cr3t!", "s3cr3t!"),
        ('{"token": "abc.def.ghi", "n": 1}', "abc.def.ghi"),
        ('{"client_secret":"zzz-top"}', "zzz-top"),
    ],
)
def test_redact_secret_shapes(text, leaked):
    out = redact(text)
    assert leaked not in out
    assert "<REDACTED>" in out


def test_redact_keeps_assignment_name():
    assert redact("FOO_API_KEY=abc123") == "FOO_API_KEY=<REDACTED>"
    assert redact('"token": "abc"') == '"token": "<REDACTED>"'


def test_redact_known_values_longest_first_and_ignores_short():
    out = redact("a hunter2-correct-horse-battery b hunter2", secret_values=("hunter2", PLAIN_SECRET, "", "ab"))
    assert "hunter2" not in out
    assert out == "a <REDACTED> b <REDACTED>"
    assert redact("ab cd", secret_values=("ab",)) == "ab cd"


def test_redact_preserves_ordinary_pytest_output():
    text = (
        "tests/test_math.py::test_add FAILED\n"
        "E       assert 2 + 2 == 5\n"
        "E        +  where 4 = add(2, 2)\n"
        "/Users/dev/project/src/math_utils.py:12: AssertionError\n"
        "FAILED tests/test_math.py::test_add - assert 4 == 5\n"
        "========= 1 failed, 12 passed, 3 skipped in 0.42s =========\n"
        "key_count = 3; token_count=7\n"
    )
    assert redact(text) == text


def test_state_has_exactly_the_contract_keys_and_only_failures():
    ev = evaluation(
        evidence("PASS", stderr="fine", exit_code=0, criterion="ok", n=1),
        evidence("INCONCLUSIVE", stderr="timeout", exit_code=None, n=2),
        evidence("FAIL", stderr="E assert 1 == 2", stdout="STDOUT-ONLY", n=3),
    )
    state = build_triage_state(task_instructions="Fix add()", evaluation=ev, adapter_stderr="warn", diff_stat="1 file")
    assert set(state) == KEYS
    assert state["evaluator_status"] == "FAIL"
    assert state["diff_stat"] == "1 file"
    assert state["adapter_stderr_tail"] == "warn"
    assert state["failed_evidence"] == [
        {"criterion": "tests pass", "command": "pytest -q", "exit_code": 1, "stderr_tail": "E assert 1 == 2"}
    ]
    assert "STDOUT-ONLY" not in json.dumps(state)
    assert "EVIDENCE-A" not in json.dumps(state)


def test_state_defaults():
    state = build_triage_state(task_instructions="t", evaluation=evaluation(status="INCONCLUSIVE"))
    assert state == {
        "task_instructions": "t",
        "evaluator_status": "INCONCLUSIVE",
        "failed_evidence": [],
        "adapter_stderr_tail": "",
        "diff_stat": "unknown",
    }


def test_state_caps_failures_and_tails():
    items = [evidence(stderr="x" * 5000 + f"END{i}", n=i) for i in range(5)]
    state = build_triage_state(
        task_instructions="t" * 10000, evaluation=evaluation(*items), adapter_stderr="a" * 5000 + "ADAPTER-END",
    )
    assert len(state["failed_evidence"]) == 3
    # the failed-evidence tails share one tail_chars budget
    tails = [f["stderr_tail"] for f in state["failed_evidence"]]
    assert [len(t) for t in tails] == [500, 500, 500] and tails[0].endswith("END0")
    assert len(state["adapter_stderr_tail"]) == 1500 and state["adapter_stderr_tail"].endswith("ADAPTER-END")
    assert len(state["task_instructions"]) <= 2000
    single = build_triage_state(task_instructions="t", evaluation=evaluation(items[0]))
    assert len(single["failed_evidence"][0]["stderr_tail"]) == 1500
    custom = build_triage_state(task_instructions="t", evaluation=evaluation(*items), tail_chars=10, max_failed=1)
    assert [f["stderr_tail"] for f in custom["failed_evidence"]] == ["xxxxxxEND0"]


def test_stderr_tail_falls_back_to_stdout():
    state = build_triage_state(task_instructions="t", evaluation=evaluation(evidence(stdout="out tail")))
    assert state["failed_evidence"][0]["stderr_tail"] == "out tail"


def test_huge_stderr_state_stays_small_for_agentjev_context():
    items = [evidence(stderr="E " + "y" * 200_000, command="pytest " + "z" * 10_000, n=i) for i in range(10)]
    state = build_triage_state(
        task_instructions="do " * 50_000, evaluation=evaluation(*items), adapter_stderr="w" * 200_000,
        diff_stat="d" * 50_000,
    )
    assert len(json.dumps(state)) < 6000


@pytest.mark.parametrize("secret", [DUMMY_KEY, PLAIN_SECRET])
def test_known_credential_never_appears_in_state(secret):
    # Place the secret so it straddles each tail boundary (750 per evidence with two failures, 1500 adapter).
    def straddle(tail):
        return "p" * 3000 + " " + secret + "\n" + "q" * (tail - len(secret) // 2)

    ev = evaluation(
        evidence(stderr=f"boom {secret}\n" + straddle(750), command=f"pytest --key {secret}", criterion=f"c {secret}"),
        evidence(stderr="", stdout=straddle(750), n=2),
    )
    state = build_triage_state(
        task_instructions=f"use {secret} " + "i" * 1990 + secret,
        evaluation=ev,
        adapter_stderr=straddle(1500),
        diff_stat=f"1 file {secret}",
        secret_values=(PLAIN_SECRET,),
    )
    dumped = json.dumps(state)
    assert secret not in dumped
    for i in range(0, len(secret) - 8):
        assert secret[i : i + 9] not in dumped
