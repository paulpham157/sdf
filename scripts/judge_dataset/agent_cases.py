"""Agent-caused failed Attempts: wrong solutions and non-attempts, induced for real.

Each case writes a tiny module and its pytest checks into a temp workspace, lets a
scripted FakeRuntime apply the "agent" edit, and runs AgentFixtureLoop so the
evidence is real evaluator output.
"""

from __future__ import annotations

import difflib
import json
import re
import sysconfig
import tempfile
from dataclasses import dataclass
from pathlib import Path

from sdf_core.fixture_loop import AgentFixtureLoop
from sdf_core.runtime import FakeRuntime, RuntimeSession, RuntimeStatus

CASES_DIR = Path(__file__).resolve().parents[2] / "tests" / "fixtures" / "judge" / "cases"
TAIL_LINES = 15


@dataclass(frozen=True)
class Spec:
    cause: str
    task: str
    files: dict[str, str]
    edits: dict[str, str]
    checks: dict[str, str]
    induced_by: str
    adapter_stderr_tail: str = ""


class EditingRuntime(FakeRuntime):
    def __init__(self, workspace: Path, edits: dict[str, str]):
        super().__init__()
        self.workspace = workspace
        self.edits = edits

    def send(self, session_id: str, input_text: str) -> RuntimeSession:
        for name, text in self.edits.items():
            path = self.workspace / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text, encoding="utf-8")
        current = super().send(session_id, input_text)
        completed = RuntimeSession(current.session_id, current.attempt_id, current.agent, RuntimeStatus.COMPLETED, current.output)
        self._sessions[session_id] = completed
        return completed


def diff_stat(before: dict[str, str], after: dict[str, str]) -> str:
    files = added = removed = 0
    for name in sorted(set(before) | set(after)):
        old, new = before.get(name, ""), after.get(name, before.get(name, ""))
        if old == new:
            continue
        files += 1
        for line in difflib.unified_diff(old.splitlines(), new.splitlines(), lineterm="", n=0):
            if line.startswith(("+++", "---", "@@")):
                continue
            added += line.startswith("+")
            removed += line.startswith("-")
    if not files:
        return "0 files changed"
    return f"{files} file{'s' if files > 1 else ''}, +{added} -{removed}"


def tail(text: str, workspace: Path) -> str:
    for root in sorted({str(workspace.resolve()), str(workspace)}, key=len, reverse=True):
        text = text.replace(root, "/workspace")
    text = text.replace(sysconfig.get_paths()["stdlib"], "/usr/lib/python3")
    text = re.sub(r" in \d+(\.\d+)?s\b", "", text)
    lines = [line.rstrip() for line in text.strip().splitlines() if line.strip()]
    return "\n".join(lines[-TAIL_LINES:])


def run_spec(spec: Spec, case_id: str) -> dict:
    with tempfile.TemporaryDirectory() as tmp:
        workspace = Path(tmp) / "workspace"
        workspace.mkdir()
        for name, text in spec.files.items():
            path = workspace / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text, encoding="utf-8")
        checks = {criterion: [["python", "-m", "pytest", "-q", "--tb=short", test]] for criterion, test in spec.checks.items()}
        result = AgentFixtureLoop(EditingRuntime(workspace, spec.edits)).run(
            attempt_id=case_id.upper(),
            agent="fake-agent",
            workspace=workspace,
            instructions=spec.task,
            criteria=tuple(checks),
            criterion_checks=checks,
        )
        evidence = [
            {
                "criterion": item.criterion or "",
                "command": item.command,
                "exit_code": item.exit_code,
                "stderr_tail": tail(item.stdout + "\n" + item.stderr, workspace),
            }
            for item in result.evaluation.evidence
            if item.status == "FAIL"
        ]
        status = result.evaluation.status
    if status != "FAIL" or not evidence:
        raise RuntimeError(f"{case_id}: expected an induced FAIL, got {status}")
    return {
        "id": case_id,
        "label": {
            "failure_cause": spec.cause,
            "higher_tier_would_help": True,
            "induced_by": spec.induced_by,
        },
        "state": {
            "task_instructions": spec.task,
            "evaluator_status": status,
            "failed_evidence": evidence,
            "adapter_stderr_tail": spec.adapter_stderr_tail,
            "diff_stat": diff_stat(spec.files, spec.edits),
        },
    }


WRONG = "agent_solution_wrong"
SKIPPED = "agent_did_not_attempt"

SPECS: list[Spec] = [
    Spec(
        WRONG,
        "Make slugify(title) in text_utils.py lowercase the title and join words with '-'.",
        {
            "text_utils.py": "def slugify(title):\n    raise NotImplementedError\n",
            "tests/test_slugify.py": "from text_utils import slugify\n\n\ndef test_simple():\n    assert slugify(\"Hello World\") == \"hello-world\"\n\n\ndef test_single_word():\n    assert slugify(\"python\") == \"python\"\n",
        },
        {"text_utils.py": "def slugify(title):\n    return \"-\".join(title.split())\n"},
        {"slugify lowercases and hyphenates": "tests/test_slugify.py"},
        "The agent edit joins words with '-' but never lowercases, so the mixed-case test fails.",
    ),
    Spec(
        WRONG,
        "Implement paginate(items, page, size) in pager.py; pages are 1-based.",
        {
            "pager.py": "def paginate(items, page, size):\n    raise NotImplementedError\n",
            "tests/test_pager.py": "from pager import paginate\n\nITEMS = list(range(10))\n\n\ndef test_first_page():\n    assert paginate(ITEMS, 1, 3) == [0, 1, 2]\n\n\ndef test_last_page():\n    assert paginate(ITEMS, 4, 3) == [9]\n",
        },
        {"pager.py": "def paginate(items, page, size):\n    start = page * size\n    return items[start:start + size]\n"},
        {"pages are 1-based": "tests/test_pager.py"},
        "The agent edit computes the start index as page * size (off by one page), so every page is shifted.",
    ),
    Spec(
        WRONG,
        "Implement is_leap_year(year) in calendar_utils.py using the Gregorian rules.",
        {
            "calendar_utils.py": "def is_leap_year(year):\n    raise NotImplementedError\n",
            "tests/test_leap.py": "import pytest\n\nfrom calendar_utils import is_leap_year\n\n\n@pytest.mark.parametrize(\"year,expected\", [(2024, True), (2023, False), (2000, True), (1900, False)])\ndef test_leap(year, expected):\n    assert is_leap_year(year) is expected\n",
        },
        {"calendar_utils.py": "def is_leap_year(year):\n    return year % 4 == 0\n"},
        {"Gregorian leap-year rules": "tests/test_leap.py"},
        "The agent edit only applies the divisible-by-4 rule and misses the century exception, so 1900 fails.",
    ),
    Spec(
        WRONG,
        "Implement clamp(x, lo, hi) in bounds.py so the result stays within [lo, hi].",
        {
            "bounds.py": "def clamp(x, lo, hi):\n    return x\n",
            "tests/test_bounds.py": "from bounds import clamp\n\n\ndef test_inside():\n    assert clamp(5, 0, 10) == 5\n\n\ndef test_below():\n    assert clamp(-3, 0, 10) == 0\n\n\ndef test_above():\n    assert clamp(42, 0, 10) == 10\n",
        },
        {"bounds.py": "def clamp(x, lo, hi):\n    if x < lo:\n        return lo\n    if x > hi:\n        return lo\n    return x\n"},
        {"clamp stays within bounds": "tests/test_bounds.py"},
        "The agent edit returns lo in the x > hi branch (wrong branch value), so the above-range test fails.",
    ),
    Spec(
        WRONG,
        "Make parse_duration(text) in durations.py accept strings like '1h30m' or '45m' and return minutes.",
        {
            "durations.py": "def parse_duration(text):\n    raise NotImplementedError\n",
            "tests/test_durations.py": "from durations import parse_duration\n\n\ndef test_hours():\n    assert parse_duration(\"2h\") == 120\n\n\ndef test_hours_and_minutes():\n    assert parse_duration(\"1h30m\") == 90\n\n\ndef test_minutes():\n    assert parse_duration(\"45m\") == 45\n",
        },
        {"durations.py": "def parse_duration(text):\n    hours = text.split(\"h\")[0]\n    return int(hours) * 60\n"},
        {"durations parse to minutes": "tests/test_durations.py"},
        "The agent edit is a partial fix that only handles the hours part, so inputs with minutes fail.",
    ),
    Spec(
        WRONG,
        "Change format_price(cents) in money.py to prefix the result with '$'; keep two decimal places.",
        {
            "money.py": "def format_price(cents):\n    return f\"{cents / 100:.2f}\"\n",
            "tests/test_symbol.py": "from money import format_price\n\n\ndef test_dollar_prefix():\n    assert format_price(500).startswith(\"$\")\n",
            "tests/test_decimals.py": "from money import format_price\n\n\ndef test_two_decimals():\n    assert format_price(1999) == \"$19.99\"\n\n\ndef test_whole_dollars():\n    assert format_price(700) == \"$7.00\"\n",
        },
        {"money.py": "def format_price(cents):\n    return f\"${cents // 100}\"\n"},
        {"price has dollar prefix": "tests/test_symbol.py", "price keeps two decimals": "tests/test_decimals.py"},
        "The agent edit adds the '$' prefix but switches to integer division, breaking the existing two-decimal check.",
    ),
    Spec(
        WRONG,
        "Fix word_count(text) in counting.py so it ignores repeated spaces.",
        {
            "counting.py": "def word_count(text):\n    return len(text.split(\" \"))\n",
            "tests/test_counting.py": "from counting import word_count\n\n\ndef test_repeated_spaces():\n    assert word_count(\"a  b   c\") == 3\n",
        },
        {"counting.py": "def count_words(text):\n    return len(text.split())\n"},
        {"word_count ignores repeated spaces": "tests/test_counting.py"},
        "The agent edit fixes the logic but renames word_count to count_words, so the test module fails to import (looks like a broken test).",
    ),
    Spec(
        WRONG,
        "Make evens(numbers) in sequences.py return only the even numbers, in order.",
        {
            "sequences.py": "def evens(numbers):\n    raise NotImplementedError\n",
            "tests/test_sequences.py": "from sequences import evens\n\n\ndef test_values():\n    assert list(evens([1, 2, 3, 4])) == [2, 4]\n\n\ndef test_is_sized():\n    assert len(evens([2, 4, 6])) == 3\n",
        },
        {"sequences.py": "def evens(numbers):\n    return (n for n in numbers if n % 2 == 0)\n"},
        {"evens returns the even numbers": "tests/test_sequences.py"},
        "The agent edit returns a generator instead of a list, so len() raises TypeError inside the test body (looks like a broken test).",
    ),
    Spec(
        SKIPPED,
        "Implement median(values) in stats.py; return the middle value, averaging the two middle values for even lengths.",
        {
            "stats.py": "def median(values):\n    raise NotImplementedError\n",
            "tests/test_stats.py": "from stats import median\n\n\ndef test_odd():\n    assert median([3, 1, 2]) == 2\n\n\ndef test_even():\n    assert median([4, 1, 3, 2]) == 2.5\n",
        },
        {},
        {"median of odd and even lists": "tests/test_stats.py"},
        "The agent made no edit at all, leaving the NotImplementedError stub in place.",
    ),
    Spec(
        SKIPPED,
        "Implement celsius_to_fahrenheit(c) in temperature.py.",
        {
            "README.md": "# temperature\n\nSmall conversion helpers.\n",
            "temperature.py": "def celsius_to_fahrenheit(c):\n    raise NotImplementedError\n",
            "tests/test_temperature.py": "from temperature import celsius_to_fahrenheit\n\n\ndef test_freezing():\n    assert celsius_to_fahrenheit(0) == 32\n\n\ndef test_boiling():\n    assert celsius_to_fahrenheit(100) == 212\n",
        },
        {"README.md": "# temperature\n\nSmall conversion helpers.\n\n## Usage\n\nCall `celsius_to_fahrenheit` with a value in Celsius.\n"},
        {"conversion is correct": "tests/test_temperature.py"},
        "The agent only added a usage section to README.md and left the stub untouched.",
    ),
    Spec(
        SKIPPED,
        "Implement dedupe(items) in collections_utils.py, keeping the first occurrence order.",
        {
            "collections_utils.py": "def dedupe(items):\n    raise NotImplementedError(\"TODO\")\n",
            "tests/test_dedupe.py": "from collections_utils import dedupe\n\n\ndef test_keeps_order():\n    assert dedupe([3, 1, 3, 2, 1]) == [3, 1, 2]\n",
        },
        {"collections_utils.py": "\n\ndef dedupe(items):\n    raise NotImplementedError(\"TODO\")\n\n"},
        {"dedupe keeps first occurrences": "tests/test_dedupe.py"},
        "The agent touched the target module with whitespace-only changes (blank lines) and left the TODO stub.",
    ),
    Spec(
        SKIPPED,
        "Implement chunk(items, size) in batching.py to split a list into consecutive chunks of at most size items.",
        {
            "batching.py": "def chunk(items, size):\n    pass\n",
            "tests/test_batching.py": "from batching import chunk\n\n\ndef test_even_split():\n    assert chunk([1, 2, 3, 4], 2) == [[1, 2], [3, 4]]\n\n\ndef test_remainder():\n    assert chunk([1, 2, 3], 2) == [[1, 2], [3]]\n",
        },
        {"batching.py": "def chunk(items, size):\n    \"\"\"Split items into chunks of at most size.\"\"\"\n    pass\n"},
        {"chunks are split correctly": "tests/test_batching.py"},
        "The agent only added a docstring to the target function; the body is still pass and returns None.",
    ),
    Spec(
        SKIPPED,
        "Implement validate_email(address) in validators.py returning True for addresses with one '@' and a dotted domain.",
        {
            "validators.py": "def validate_email(address):\n    raise NotImplementedError\n",
            "tests/test_validators.py": "from validators import validate_email\n\n\ndef test_valid():\n    assert validate_email(\"user@example.com\") is True\n\n\ndef test_invalid():\n    assert validate_email(\"user.example.com\") is False\n",
        },
        {"NOTES.md": "# Plan\n\n1. Read validators.py\n2. Implement validate_email\n3. Run the tests\n"},
        {"email validation": "tests/test_validators.py"},
        "The agent only created a NOTES.md plan file and never edited validators.py.",
        adapter_stderr_tail="agent: reached max turns before editing target files",
    ),
    Spec(
        SKIPPED,
        "Implement rotate(items, k) in rotation.py to rotate a list right by k positions.",
        {
            "rotation.py": "def rotate(items, k):\n    raise NotImplementedError\n",
            "helpers.py": "def identity(x):\n    return x\n",
            "tests/test_rotation.py": "from rotation import rotate\n\n\ndef test_rotate_one():\n    assert rotate([1, 2, 3], 1) == [3, 1, 2]\n\n\ndef test_rotate_full():\n    assert rotate([1, 2, 3], 3) == [1, 2, 3]\n",
        },
        {"helpers.py": "def identity(value):\n    return value\n"},
        {"rotation is correct": "tests/test_rotation.py"},
        "The agent renamed a parameter in the unrelated helpers.py and left the rotation stub untouched.",
    ),
    Spec(
        SKIPPED,
        "Implement total_weight(parcels) in shipping.py, summing the 'kg' field of each parcel dict.",
        {
            "shipping.py": "def total_weight(parcels):\n    return 0\n",
            "tests/test_shipping.py": "from shipping import total_weight\n\n\ndef test_sum():\n    assert total_weight([{\"kg\": 2}, {\"kg\": 3.5}]) == 5.5\n\n\ndef test_empty():\n    assert total_weight([]) == 0\n",
        },
        {},
        {"weights are summed": "tests/test_shipping.py"},
        "The agent made no edit, leaving the placeholder that always returns 0.",
    ),
    Spec(
        SKIPPED,
        "Implement initials(full_name) in names.py returning the uppercase first letter of each word.",
        {
            ".gitignore": "__pycache__/\n",
            "names.py": "def initials(full_name):\n    raise NotImplementedError\n",
            "tests/test_names.py": "from names import initials\n\n\ndef test_two_words():\n    assert initials(\"ada lovelace\") == \"AL\"\n",
        },
        {".gitignore": "__pycache__/\n.venv/\n*.egg-info/\n"},
        {"initials are uppercase": "tests/test_names.py"},
        "The agent only extended .gitignore and never touched names.py.",
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
