"""Environment and credential failed Attempts, induced for real where the repo allows.

Evaluator timeouts, a missing test runner, a SIGKILLed or disk-full check process and
dead runtime sessions run through DeterministicEvaluator / AgentFixtureLoop. Provider
errors are the text the agent CLI prints before exiting; the evaluator then runs for
real on whatever the agent left behind (sometimes a partial edit).
"""

from __future__ import annotations

import json
import sys
import tempfile
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from pathlib import Path

from sdf_core.credentials import CredentialConfigError, _check_expiry
from sdf_core.evaluator import DeterministicEvaluator
from sdf_core.fixture_loop import AgentFixtureLoop
from sdf_core.herdr_runtime import HerdrRuntimeError
from sdf_core.runtime import FakeRuntime, RuntimeSession, RuntimeStatus

from scripts.judge_dataset.agent_cases import CASES_DIR, diff_stat, tail

ENV = "environment_or_runtime"
CRED = "credential_or_provider"
PYTEST = ["python", "-m", "pytest", "-q", "--tb=short", "-p", "no:cacheprovider"]


@dataclass(frozen=True)
class Spec:
    cause: str
    task: str
    files: dict[str, str]
    checks: dict[str, str]
    induced_by: str
    edits: dict[str, str] = field(default_factory=dict)
    adapter_stderr_tail: str = ""
    start_error: Exception | None = None
    send_error: Exception | None = None
    terminated: bool = False
    runner: tuple[str, ...] = tuple(PYTEST)
    timeout_seconds: float = 30.0


class InfraRuntime(FakeRuntime):
    def __init__(self, workspace: Path, spec: Spec):
        super().__init__()
        self.workspace = workspace
        self.spec = spec

    def start(self, *, attempt_id: str, agent: str) -> RuntimeSession:
        if self.spec.start_error is not None:
            raise self.spec.start_error
        session = super().start(attempt_id=attempt_id, agent=agent)
        return self.terminate(session.session_id) if self.spec.terminated else session

    def send(self, session_id: str, input_text: str) -> RuntimeSession:
        for name, text in self.spec.edits.items():
            (self.workspace / name).parent.mkdir(parents=True, exist_ok=True)
            (self.workspace / name).write_text(text, encoding="utf-8")
        if self.spec.send_error is not None:
            raise self.spec.send_error
        super().send(session_id, input_text)
        return self._set_status(session_id, RuntimeStatus.COMPLETED)


def clean(text: str, workspace: Path) -> str:
    for prefix in sorted({sys.prefix, sys.base_prefix}, key=len, reverse=True):
        text = text.replace(prefix, "/venv")
    return tail(text.replace(str(workspace.resolve()), "/workspace"), workspace)


def run_spec(spec: Spec, case_id: str) -> dict:
    with tempfile.TemporaryDirectory() as tmp:
        workspace = Path(tmp) / "workspace"
        workspace.mkdir()
        for name, text in spec.files.items():
            (workspace / name).parent.mkdir(parents=True, exist_ok=True)
            (workspace / name).write_text(text, encoding="utf-8")
        checks = {criterion: [[*spec.runner, test]] for criterion, test in spec.checks.items()}
        loop = AgentFixtureLoop(
            InfraRuntime(workspace, spec),
            evaluator=DeterministicEvaluator(timeout_seconds=spec.timeout_seconds, allowed_executables={"python", "pytest"}),
        )
        try:
            result = loop.run(
                attempt_id=case_id.upper(),
                agent="fake-agent",
                workspace=workspace,
                instructions=spec.task,
                criteria=tuple(checks),
                criterion_checks=checks,
            )
        except (HerdrRuntimeError, CredentialConfigError, RuntimeError) as exc:
            status = "TIMEOUT" if "timed out" in str(exc) else "ERROR"
            evidence: list[dict] = []
            adapter = f"{type(exc).__name__}: {exc}"
            diff = "unknown" if spec.send_error is not None else "0 files changed"
        else:
            evidence = [
                {
                    "criterion": item.criterion or "",
                    "command": item.command,
                    "exit_code": item.exit_code,
                    "stderr_tail": clean(item.stdout + "\n" + item.stderr, workspace),
                }
                for item in result.evaluation.evidence
                if item.status != "PASS"
            ]
            statuses = {item.status for item in result.evaluation.evidence}
            if "FAIL" in statuses:
                status = "FAIL"
            elif any(item["stderr_tail"] == "timeout" for item in evidence):
                status = "TIMEOUT"
            else:
                status = "ERROR"
            adapter = spec.adapter_stderr_tail
            diff = diff_stat(spec.files, spec.edits)
    if not evidence and not adapter:
        raise RuntimeError(f"{case_id}: induced failure left no trace")
    return {
        "id": case_id,
        "label": {"failure_cause": spec.cause, "higher_tier_would_help": False, "induced_by": spec.induced_by},
        "state": {
            "task_instructions": spec.task,
            "evaluator_status": status,
            "failed_evidence": evidence,
            "adapter_stderr_tail": adapter,
            "diff_stat": diff,
        },
    }


def expired_connection() -> CredentialConfigError:
    now = datetime(2026, 1, 15, 9, 0, tzinfo=timezone.utc)
    try:
        _check_expiry("codex", "conn_000", now + timedelta(minutes=20), 3600, now)
    except CredentialConfigError as exc:
        return exc
    raise AssertionError("expected an expiry error")


def stub_module(name: str, signature: str) -> str:
    return f"def {name}({signature}):\n    raise NotImplementedError\n"


SPECS: list[Spec] = [
    Spec(
        ENV,
        "Implement word_frequencies(text) in wordfreq.py returning a dict of lowercase word counts.",
        {
            "wordfreq.py": stub_module("word_frequencies", "text"),
            "conftest.py": "import time\n\n\ndef pytest_configure(config):\n    time.sleep(5)\n",
            "tests/test_wordfreq.py": "from wordfreq import word_frequencies\n\n\ndef test_counts():\n    assert word_frequencies(\"a A b\") == {\"a\": 2, \"b\": 1}\n",
        },
        {"counts are case-insensitive": "tests/test_wordfreq.py"},
        "A conftest.py standing in for a stalled sandbox sleeps 5s at pytest startup while the evaluator timeout is 0.5s, so the check times out.",
        edits={"wordfreq.py": "from collections import Counter\n\n\ndef word_frequencies(text):\n    return dict(Counter(text.lower().split()))\n"},
        timeout_seconds=0.5,
    ),
    Spec(
        ENV,
        "Implement to_roman(n) in roman.py for 1 <= n <= 3999.",
        {
            "roman.py": stub_module("to_roman", "n"),
            "tests/test_roman.py": "from roman import to_roman\n\n\ndef test_small():\n    assert to_roman(4) == \"IV\"\n",
        },
        {"roman numerals": "tests/test_roman.py"},
        "The runtime raised the E2B transport's real 'command timed out; sandbox killed' error after the agent had edited roman.py, so nothing was collected or evaluated.",
        edits={"roman.py": "NUMERALS = [(1000, \"M\"), (900, \"CM\"), (500, \"D\"), (400, \"CD\")]\n\n\ndef to_roman(n):\n    out = \"\"\n"},
        send_error=HerdrRuntimeError("E2B Herdr command timed out; sandbox killed"),
    ),
    Spec(
        ENV,
        "Make percent(part, whole) in ratios.py return the rounded integer percentage.",
        {
            "ratios.py": stub_module("percent", "part, whole"),
            "tests/test_ratios.py": "from ratios import percent\n\n\ndef test_half():\n    assert percent(1, 2) == 50\n",
        },
        {"percentage is rounded": "tests/test_ratios.py"},
        "The check invokes /opt/venv/bin/pytest, a runner path that does not exist on the host, so the evaluator gets FileNotFoundError before any test runs.",
        edits={"ratios.py": "def percent(part, whole):\n    return round(100 * part / whole)\n"},
        runner=("/opt/venv/bin/pytest", "-q"),
    ),
    Spec(
        ENV,
        "Implement flatten(nested) in flat.py to flatten one level of nested lists.",
        {
            "flat.py": stub_module("flatten", "nested"),
            "conftest.py": "import os\nimport signal\n\n\ndef pytest_sessionstart(session):\n    os.kill(os.getpid(), signal.SIGKILL)\n",
            "tests/test_flat.py": "from flat import flatten\n\n\ndef test_one_level():\n    assert flatten([[1], [2, 3]]) == [1, 2, 3]\n",
        },
        {"one level is flattened": "tests/test_flat.py"},
        "A conftest.py SIGKILLs the pytest process at session start, as an OOM killer would, so the check exits -9 with no output.",
        edits={"flat.py": "def flatten(nested):\n    return [x for inner in nested for x in inner]\n"},
    ),
    Spec(
        ENV,
        "Implement titlecase(text) in titles.py capitalising every word.",
        {
            "titles.py": stub_module("titlecase", "text"),
            "conftest.py": "import errno\nimport os\n\n\ndef pytest_configure(config):\n    raise OSError(errno.ENOSPC, os.strerror(errno.ENOSPC), \"/workspace/.pytest-tmp\")\n",
            "tests/test_titles.py": "from titles import titlecase\n\n\ndef test_words():\n    assert titlecase(\"a tale\") == \"A Tale\"\n",
        },
        {"every word is capitalised": "tests/test_titles.py"},
        "A conftest.py raises ENOSPC (no space left on device) at pytest configure time, simulating a full sandbox disk.",
        edits={"titles.py": "def titlecase(text):\n    return \" \".join(w.capitalize() for w in text.split())\n"},
    ),
    Spec(
        ENV,
        "Implement mask_card(number) in cards.py keeping only the last four digits visible.",
        {
            "cards.py": stub_module("mask_card", "number"),
            "tests/test_cards.py": "from cards import mask_card\n\n\ndef test_mask():\n    assert mask_card(\"4111111111111111\") == \"************1111\"\n",
        },
        {"only last four digits visible": "tests/test_cards.py"},
        "The FakeRuntime session is terminated before any input, so AgentFixtureLoop refuses to dispatch the Attempt.",
        terminated=True,
    ),
    Spec(
        ENV,
        "Implement next_weekday(day) in weekdays.py returning the following weekday name.",
        {
            "weekdays.py": stub_module("next_weekday", "day"),
            "tests/test_weekdays.py": "from weekdays import next_weekday\n\n\ndef test_friday():\n    assert next_weekday(\"Friday\") == \"Monday\"\n",
        },
        {"weekends are skipped": "tests/test_weekdays.py"},
        "The runtime raised the Herdr endpoint transport's connection-failed error with connection refused while dispatching the prompt.",
        send_error=HerdrRuntimeError(f"Herdr endpoint connection failed: {ConnectionRefusedError(111, 'Connection refused')}"),
    ),
    Spec(
        ENV,
        "Implement parse_semver(version) in semver.py returning a (major, minor, patch) tuple of ints.",
        {
            "semver.py": stub_module("parse_semver", "version"),
            "tests/test_semver.py": "from semver import parse_semver\n\n\ndef test_parse():\n    assert parse_semver(\"1.2.3\") == (1, 2, 3)\n",
        },
        {"versions parse to int tuples": "tests/test_semver.py"},
        "The runtime raised Herdr's real 'agent never became interactive' error at start, so the agent never received the prompt.",
        start_error=HerdrRuntimeError("Herdr agent never became interactive: unknown"),
    ),
    Spec(
        CRED,
        "Implement is_palindrome(text) in palindromes.py ignoring case and spaces.",
        {
            "palindromes.py": stub_module("is_palindrome", "text"),
            "tests/test_palindromes.py": "from palindromes import is_palindrome\n\n\ndef test_phrase():\n    assert is_palindrome(\"Never odd or even\") is True\n",
        },
        {"phrases are palindromes": "tests/test_palindromes.py"},
        "The agent CLI was given a revoked API key, printed Claude Code's 401 authentication_error and exited without editing; the evaluator then ran on the untouched stub.",
        adapter_stderr_tail='API Error: 401 {"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}} · Please run /login',
    ),
    Spec(
        CRED,
        "Implement fizzbuzz(n) in fizz.py returning the FizzBuzz string for n.",
        {
            "fizz.py": stub_module("fizzbuzz", "n"),
            "tests/test_fizz.py": "from fizz import fizzbuzz\n\n\ndef test_values():\n    assert [fizzbuzz(i) for i in (3, 5, 15, 7)] == [\"Fizz\", \"Buzz\", \"FizzBuzz\", \"7\"]\n",
        },
        {"fizzbuzz values": "tests/test_fizz.py"},
        "The API account ran out of prepaid credit mid-session: the agent had written only the Fizz branch before Claude Code reported 'Credit balance is too low' and stopped.",
        edits={"fizz.py": "def fizzbuzz(n):\n    if n % 3 == 0:\n        return \"Fizz\"\n"},
        adapter_stderr_tail="Credit balance is too low",
    ),
    Spec(
        CRED,
        "Implement merge_intervals(intervals) in intervals.py merging overlapping [start, end] pairs.",
        {
            "intervals.py": stub_module("merge_intervals", "intervals"),
            "tests/test_intervals.py": "from intervals import merge_intervals\n\n\ndef test_overlap():\n    assert merge_intervals([[1, 3], [2, 6], [8, 9]]) == [[1, 6], [8, 9]]\n",
        },
        {"overlaps are merged": "tests/test_intervals.py"},
        "The organisation's token-per-minute limit was hit after the agent's first edit, so Claude Code exhausted its retries on 429 rate_limit_error and exited with a half-written function.",
        edits={"intervals.py": "def merge_intervals(intervals):\n    intervals = sorted(intervals)\n    merged = []\n    return merged\n"},
        adapter_stderr_tail=(
            "⎿ API Error (429 rate_limit_error) · Retrying in 8 seconds… (attempt 8/10)\n"
            "⎿ API Error (429 rate_limit_error) · Retrying in 16 seconds… (attempt 9/10)\n"
            'API Error: 429 {"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed the rate limit '
            'for your organization (<REDACTED>) of 50,000 input tokens per minute."}}'
        ),
    ),
    Spec(
        CRED,
        "Implement celsius_range(values) in weather.py returning (min, max) of a list of readings.",
        {
            "weather.py": stub_module("celsius_range", "values"),
            "tests/test_weather.py": "from weather import celsius_range\n\n\ndef test_range():\n    assert celsius_range([3, -2, 9]) == (-2, 9)\n",
        },
        {"min and max readings": "tests/test_weather.py"},
        "The subscription OAuth token forwarded into the sandbox had expired, so Claude Code's first request failed with a 401 and it asked to /login.",
        adapter_stderr_tail=(
            'API Error: 401 {"type":"error","error":{"type":"authentication_error","message":"OAuth token has expired. '
            'Please obtain a new token or refresh your existing token."}} · Please run /login'
        ),
    ),
    Spec(
        CRED,
        "Implement caesar(text, shift) in cipher.py shifting ASCII letters and keeping case.",
        {
            "cipher.py": stub_module("caesar", "text, shift"),
            "tests/test_cipher.py": "from cipher import caesar\n\n\ndef test_shift():\n    assert caesar(\"Abz\", 1) == \"Bca\"\n",
        },
        {"letters shift with case": "tests/test_cipher.py"},
        "The workspace-scoped API key lacked access to the requested model, so the first request returned 403 permission_error.",
        adapter_stderr_tail=(
            'API Error: 403 {"type":"error","error":{"type":"permission_error","message":"Your API key does not have '
            'permission to use the specified resource."}}'
        ),
    ),
    Spec(
        CRED,
        "Implement group_by_length(words) in grouping.py returning a dict from length to words.",
        {
            "grouping.py": stub_module("group_by_length", "words"),
            "tests/test_grouping.py": "from grouping import group_by_length\n\n\ndef test_groups():\n    assert group_by_length([\"a\", \"bb\", \"c\"]) == {1: [\"a\", \"c\"], 2: [\"bb\"]}\n",
        },
        {"words grouped by length": "tests/test_grouping.py"},
        "The OpenAI project behind the Codex API key had no remaining quota, so every Codex turn failed with 429 insufficient_quota before any edit.",
        adapter_stderr_tail=(
            "ERROR: exceeded retry limit, last status: 429 Too Many Requests, request id: req_000\n"
            'ERROR: {"error":{"message":"You exceeded your current quota, please check your plan and billing details.",'
            '"type":"insufficient_quota","param":null,"code":"insufficient_quota"}}'
        ),
    ),
    Spec(
        CRED,
        "Implement safe_divide(a, b) in safemath.py returning None when b is zero.",
        {
            "safemath.py": stub_module("safe_divide", "a, b"),
            "tests/test_safemath.py": "from safemath import safe_divide\n\n\ndef test_zero():\n    assert safe_divide(1, 0) is None\n\n\ndef test_normal():\n    assert safe_divide(6, 3) == 2\n",
        },
        {"division is safe": "tests/test_safemath.py"},
        "openai_base_url in the seeded Codex config.toml pointed at a proxy path without /v1, so every Responses request got 404 and Codex gave up.",
        adapter_stderr_tail=(
            "ERROR: unexpected status 404 Not Found: 404 page not found, url: https://llm-proxy.example.com/responses\n"
            "ERROR: stream disconnected before completion: exceeded retry limit"
        ),
    ),
    Spec(
        CRED,
        "Implement count_vowels(text) in vowels.py counting a, e, i, o, u in any case.",
        {
            "vowels.py": stub_module("count_vowels", "text"),
            "tests/test_vowels.py": "from vowels import count_vowels\n\n\ndef test_mixed_case():\n    assert count_vowels(\"AbcdE\") == 2\n",
        },
        {"vowels counted in any case": "tests/test_vowels.py"},
        "The ChatGPT login forwarded into Codex's auth.json had an expired refresh token, so Codex stopped after one partial edit and asked to sign in again.",
        edits={"vowels.py": "VOWELS = set(\"aeiou\")\n\n\ndef count_vowels(text):\n    return sum(ch in VOWELS for ch in text)\n"},
        adapter_stderr_tail=(
            "ERROR: Your access token could not be refreshed because your refresh token has expired. "
            "Please log out and sign in again."
        ),
    ),
    Spec(
        CRED,
        "Implement unique_emails(addresses) in mailing.py deduplicating case-insensitively, keeping first spelling.",
        {
            "mailing.py": stub_module("unique_emails", "addresses"),
            "tests/test_mailing.py": "from mailing import unique_emails\n\n\ndef test_dedupe():\n    assert unique_emails([\"A@example.com\", \"a@example.com\"]) == [\"A@example.com\"]\n",
        },
        {"emails deduplicated": "tests/test_mailing.py"},
        "The host-side Codex connection conn_000 expired 20 minutes out against a one-hour sandbox, so sdf_core.credentials refused to inject it before the runtime started.",
        start_error=expired_connection(),
    ),
]


def build_cases() -> list[dict]:
    counters: dict[str, int] = {}
    cases = []
    for spec in SPECS:
        counters[spec.cause] = counters.get(spec.cause, 0) + 1
        cases.append(run_spec(spec, f"{spec.cause}-{counters[spec.cause]:03d}"))
    return cases


def main() -> None:
    CASES_DIR.mkdir(parents=True, exist_ok=True)
    for case in build_cases():
        path = CASES_DIR / f"{case['id']}.json"
        path.write_text(json.dumps(case, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(path.relative_to(CASES_DIR.parents[3]))


if __name__ == "__main__":
    main()
