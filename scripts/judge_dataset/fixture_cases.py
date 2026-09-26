"""task_or_fixture_defect cases: a correct solution judged by a broken acceptance check (#21).

Each case writes a small module with a correct agent solution and a defective
check into a temporary workspace, runs DeterministicEvaluator on it, and keeps
the real failing evidence.
"""

import difflib
import json
import re
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path

from sdf_core.evaluator import DeterministicEvaluator

CAUSE = "task_or_fixture_defect"
CASES_DIR = Path(__file__).resolve().parents[2] / "tests" / "fixtures" / "judge" / "cases"
PYTEST = ("python", "-m", "pytest", "-q", "--no-header", "-p", "no:cacheprovider")
TAIL_LINES = 15

@dataclass(frozen=True)
class Spec:
    instructions: str
    induced_by: str
    criterion: str
    module: str
    starter: str
    solution: str
    files: dict[str, str]
    command: tuple[str, ...]

SPECS = (
    Spec(
        instructions="Add slugify(text) to slugs.py: lowercase the text and join words with '-'.",
        induced_by="The acceptance check runs tests/test_slug_utils.py, but the task ships tests/test_slugs.py, so pytest finds no file (exit 4).",
        criterion="slugify joins lowercase words with dashes",
        module="slugs.py",
        starter="def slugify(text):\n    raise NotImplementedError\n",
        solution="def slugify(text):\n    return '-'.join(text.lower().split())\n",
        files={
            "tests/test_slugs.py": "from slugs import slugify\n\n\ndef test_slugify():\n    assert slugify('Hello World') == 'hello-world'\n",
        },
        command=(*PYTEST, "tests/test_slug_utils.py"),
    ),
    Spec(
        instructions="Make clamp(x, lo, hi) in bounds.py return x limited to the range [lo, hi].",
        induced_by="The test asserts clamp(15, 0, 10) == 15, the opposite of the instructions, so the correct solution fails.",
        criterion="clamp limits x to the range",
        module="bounds.py",
        starter="def clamp(x, lo, hi):\n    return x\n",
        solution="def clamp(x, lo, hi):\n    return max(lo, min(x, hi))\n",
        files={
            "tests/test_bounds.py": (
                "from bounds import clamp\n\n\ndef test_inside():\n    assert clamp(5, 0, 10) == 5\n\n\n"
                "def test_above_range():\n    assert clamp(15, 0, 10) == 15\n"
            ),
        },
        command=(*PYTEST, "tests/test_bounds.py"),
    ),
    Spec(
        instructions="Add celsius_to_fahrenheit(c) to temperature.py.",
        induced_by="The test file has a syntax error (a missing closing parenthesis), so collection fails before any test runs.",
        criterion="celsius_to_fahrenheit converts correctly",
        module="temperature.py",
        starter="",
        solution="def celsius_to_fahrenheit(c):\n    return c * 9 / 5 + 32\n",
        files={
            "tests/test_temperature.py": (
                "from temperature import celsius_to_fahrenheit\n\n\ndef test_boiling():\n"
                "    assert celsius_to_fahrenheit(100 == 212\n"
            ),
        },
        command=(*PYTEST, "tests/test_temperature.py"),
    ),
    Spec(
        instructions="Make cart_total(cart) in cart.py return the sum of price * qty over the cart's (price, qty) pairs.",
        induced_by="The test imports make_cart from a tests/helpers module that the fixture never ships, so collection fails with an import error.",
        criterion="cart_total sums price times quantity",
        module="cart.py",
        starter="def cart_total(cart):\n    return 0\n",
        solution="def cart_total(cart):\n    return sum(price * qty for price, qty in cart)\n",
        files={
            "tests/__init__.py": "",
            "tests/test_cart.py": (
                "from cart import cart_total\nfrom tests.helpers import make_cart\n\n\n"
                "def test_total():\n    assert cart_total(make_cart([(2, 3), (5, 1)])) == 11\n"
            ),
        },
        command=(*PYTEST, "tests/test_cart.py"),
    ),
    Spec(
        instructions="Add is_palindrome(s) to text_tools.py, ignoring case and spaces.",
        induced_by="The test calls text_tools.palindrome_check, a name the instructions never mention, so the correctly named function is never found.",
        criterion="is_palindrome ignores case and spaces",
        module="text_tools.py",
        starter="",
        solution="def is_palindrome(s):\n    t = s.replace(' ', '').lower()\n    return t == t[::-1]\n",
        files={
            "tests/test_text_tools.py": (
                "import text_tools\n\n\ndef test_palindrome():\n"
                "    assert text_tools.palindrome_check('Never odd or even')\n"
            ),
        },
        command=(*PYTEST, "tests/test_text_tools.py"),
    ),
    Spec(
        instructions="Make word_count(text) in words.py return the number of whitespace-separated words.",
        induced_by="The acceptance command misspells the test runner module as pytset, so it exits 1 without running any test.",
        criterion="word_count counts words",
        module="words.py",
        starter="def word_count(text):\n    return len(text)\n",
        solution="def word_count(text):\n    return len(text.split())\n",
        files={
            "tests/test_words.py": "from words import word_count\n\n\ndef test_count():\n    assert word_count('a b  c') == 3\n",
        },
        command=("python", "-m", "pytset", "-q", "tests/test_words.py"),
    ),
    Spec(
        instructions="Make average(values) in stats.py return the arithmetic mean of a non-empty list.",
        induced_by="The test asserts average([0.1, 0.2]) == 0.15 with exact float equality, so rounding makes it fail on every correct solution.",
        criterion="average returns the mean",
        module="stats.py",
        starter="def average(values):\n    return values[0]\n",
        solution="def average(values):\n    return sum(values) / len(values)\n",
        files={
            "tests/test_stats.py": (
                "from stats import average\n\n\ndef test_integers():\n    assert average([1, 2, 3]) == 2\n\n\n"
                "def test_floats():\n    assert average([0.1, 0.2]) == 0.15\n"
            ),
        },
        command=(*PYTEST, "tests/test_stats.py"),
    ),
    Spec(
        instructions="Add parse_version(s) to versions.py returning a tuple of ints, e.g. '1.2.3' -> (1, 2, 3).",
        induced_by="The test reads expected values from tests/data/versions.txt, a fixture file the task never ships, so it errors on a correct solution.",
        criterion="parse_version returns an int tuple",
        module="versions.py",
        starter="",
        solution="def parse_version(s):\n    return tuple(int(part) for part in s.split('.'))\n",
        files={
            "tests/test_versions.py": (
                "from pathlib import Path\n\nfrom versions import parse_version\n\n\ndef test_table():\n"
                "    for line in (Path(__file__).parent / 'data' / 'versions.txt').read_text().splitlines():\n"
                "        text, expected = line.split()\n"
                "        assert parse_version(text) == tuple(map(int, expected.split(',')))\n"
            ),
        },
        command=(*PYTEST, "tests/test_versions.py"),
    ),
)

def diff_stat(before: str, after: str) -> str:
    lines = list(difflib.unified_diff(before.splitlines(), after.splitlines(), lineterm="", n=0))
    added = sum(1 for line in lines if line.startswith("+") and not line.startswith("+++"))
    removed = sum(1 for line in lines if line.startswith("-") and not line.startswith("---"))
    return f"1 file, +{added} -{removed}"

def normalize(text: str, workspace: Path) -> str:
    for root in (str(workspace.resolve()), str(workspace)):
        text = text.replace(root, "/workspace")
    text = text.replace(sys.prefix, "/venv")
    text = re.sub(r"/venv/bin/python[\d.]*", "python", text)
    text = re.sub(r"\S*/lib/python3\.\d+/", "/usr/lib/python3/", text)
    text = re.sub(r" in \d+(\.\d+)?s\b", "", text)
    return text

def tail(text: str) -> str:
    lines = [line.rstrip() for line in text.strip().splitlines()]
    return "\n".join(lines[-TAIL_LINES:])

def build_case(number: int, spec: Spec) -> dict:
    with tempfile.TemporaryDirectory() as tmp:
        workspace = Path(tmp) / "workspace"
        workspace.mkdir()
        for relative, content in {spec.module: spec.solution, **spec.files}.items():
            path = workspace / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)
        result = DeterministicEvaluator().evaluate(
            attempt_id=f"{CAUSE}-{number:03d}",
            workspace=workspace,
            criteria=[spec.criterion],
            criterion_checks={spec.criterion: [spec.command]},
        )
        failed = [
            {
                "criterion": item.criterion or "",
                "command": item.command,
                "exit_code": item.exit_code,
                "stderr_tail": tail(normalize(item.stdout + item.stderr, workspace)),
            }
            for item in result.evidence
            if item.status == "FAIL"
        ]
    if result.status != "FAIL" or not failed:
        raise RuntimeError(f"{CAUSE}-{number:03d} did not fail as induced: {result.status}")
    return {
        "id": f"{CAUSE}-{number:03d}",
        "label": {"failure_cause": CAUSE, "higher_tier_would_help": False, "induced_by": spec.induced_by},
        "state": {
            "task_instructions": spec.instructions,
            "evaluator_status": result.status,
            "failed_evidence": failed,
            "adapter_stderr_tail": "",
            "diff_stat": diff_stat(spec.starter, spec.solution),
        },
    }

def build_cases() -> list[dict]:
    return [build_case(number, spec) for number, spec in enumerate(SPECS, start=1)]

def main() -> None:
    CASES_DIR.mkdir(parents=True, exist_ok=True)
    for case in build_cases():
        (CASES_DIR / f"{case['id']}.json").write_text(json.dumps(case, indent=2, sort_keys=True) + "\n")

if __name__ == "__main__":
    main()
