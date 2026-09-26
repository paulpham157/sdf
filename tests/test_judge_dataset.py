import importlib
import json
import re
from collections import Counter
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]
JUDGE = ROOT / "tests" / "fixtures" / "judge"
CASES_DIR = JUDGE / "cases"
BUILDERS_DIR = ROOT / "scripts" / "judge_dataset"

CAUSES = (
    "agent_solution_wrong",
    "agent_did_not_attempt",
    "environment_or_runtime",
    "credential_or_provider",
    "task_or_fixture_defect",
)
AGENT_CAUSES = {"agent_solution_wrong", "agent_did_not_attempt"}
STATE_KEYS = {"task_instructions", "evaluator_status", "failed_evidence", "adapter_stderr_tail", "diff_stat"}
EVIDENCE_KEYS = {"criterion", "command", "exit_code", "stderr_tail"}
MAX_STATE_CHARS = 3000
MAX_TAIL_LINES = 15

SECRET_PATTERNS = {
    "openai/anthropic key": re.compile(r"\bsk-[A-Za-z0-9_-]{6,}"),
    "github token": re.compile(r"\bgh[pousr]_[A-Za-z0-9]{10,}"),
    "slack token": re.compile(r"\bxox[a-z]-[A-Za-z0-9-]+"),
    "aws access key": re.compile(r"\bAKIA[0-9A-Z]{16}\b"),
    "bearer token": re.compile(r"Bearer\s+(?!<REDACTED>)\S+"),
    "jwt": re.compile(r"\beyJ[\w-]{5,}\.[\w-]{5,}\.[\w-]{5,}"),
    "long hex": re.compile(r"\b[0-9a-fA-F]{32,}\b"),
    "private path": re.compile(r"/Users/|/home/|/private/var|/tmp/pytest|/var/folders"),
}
BASE64_TOKEN = re.compile(r"(?<![\w/+])[A-Za-z0-9+/_-]{32,}={0,2}")
EMAIL = re.compile(r"[\w.+-]+@([\w-]+(?:\.[\w-]+)+)")
HOSTNAME = re.compile(
    r"\b(?:[A-Za-z0-9-]+\.)+(?:com|net|org|io|dev|ai|cloud|internal|lan|corp)\b",
    re.IGNORECASE,
)
IPV4 = re.compile(r"\b(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\b")
ALLOWED_HOSTS = {"example.com", "api.anthropic.com", "api.openai.com"}
ALLOWED_IP_PREFIXES = ("127.", "0.0.0.0", "192.0.2.", "198.51.100.", "203.0.113.")

def scan(text: str) -> list[str]:
    findings = [f"{name}: {m.group(0)}" for name, pattern in SECRET_PATTERNS.items() for m in pattern.finditer(text)]
    for m in BASE64_TOKEN.finditer(text):
        token = m.group(0)
        if re.search(r"\d", token) and re.search(r"[a-z]", token) and re.search(r"[A-Z]", token):
            findings.append(f"base64-like token: {token}")
    for m in EMAIL.finditer(text):
        domain = m.group(1).lower()
        if domain != "example.com" and not domain.endswith(".example.com"):
            findings.append(f"email: {m.group(0)}")
    for m in HOSTNAME.finditer(text):
        host = m.group(0).lower()
        if host not in ALLOWED_HOSTS and host != "example.com" and not host.endswith(".example.com"):
            findings.append(f"hostname: {m.group(0)}")
    for m in IPV4.finditer(text):
        if not m.group(0).startswith(ALLOWED_IP_PREFIXES):
            findings.append(f"ip address: {m.group(0)}")
    return findings

def case_files() -> list[Path]:
    return sorted(CASES_DIR.glob("*.json"))

def load(path: Path) -> dict:
    return json.loads(path.read_text())

def builder_modules() -> list[str]:
    return sorted(p.stem for p in BUILDERS_DIR.glob("*.py") if p.stem != "__init__")

def test_cases_directory_has_cases():
    assert case_files(), f"no case files under {CASES_DIR.relative_to(ROOT)}"

@pytest.mark.parametrize("path", case_files(), ids=lambda p: p.stem)
def test_case_matches_schema(path: Path):
    text = path.read_text()
    case = json.loads(text)
    assert text == json.dumps(case, indent=2, sort_keys=True) + "\n", "write with indent=2, sort_keys, trailing newline"
    assert set(case) == {"id", "label", "state"}

    label = case["label"]
    assert set(label) == {"failure_cause", "higher_tier_would_help", "induced_by"}
    cause = label["failure_cause"]
    assert cause in CAUSES
    assert case["id"] == path.stem
    assert re.fullmatch(rf"{cause}-\d{{3}}", case["id"]), "id must be <failure_cause>-NNN"
    assert label["higher_tier_would_help"] is (cause in AGENT_CAUSES)
    assert isinstance(label["induced_by"], str) and label["induced_by"].strip()

    state = case["state"]
    assert set(state) == STATE_KEYS
    assert isinstance(state["task_instructions"], str) and state["task_instructions"].strip()
    assert state["evaluator_status"] in {"FAIL", "TIMEOUT", "ERROR"}
    assert isinstance(state["adapter_stderr_tail"], str)
    assert len(state["adapter_stderr_tail"].splitlines()) <= MAX_TAIL_LINES
    assert isinstance(state["diff_stat"], str) and state["diff_stat"].strip()
    assert isinstance(state["failed_evidence"], list)
    for item in state["failed_evidence"]:
        assert set(item) == EVIDENCE_KEYS
        assert isinstance(item["criterion"], str)
        assert isinstance(item["command"], str)
        assert item["exit_code"] is None or (isinstance(item["exit_code"], int) and not isinstance(item["exit_code"], bool))
        assert isinstance(item["stderr_tail"], str)
        assert len(item["stderr_tail"].splitlines()) <= MAX_TAIL_LINES
    assert len(json.dumps(state)) <= MAX_STATE_CHARS

@pytest.mark.parametrize("path", case_files(), ids=lambda p: p.stem)
def test_case_has_no_secret_or_private_data(path: Path):
    assert scan(path.read_text()) == []

def test_every_cause_has_enough_cases_and_total_is_in_range():
    counts = Counter(load(path)["label"]["failure_cause"] for path in case_files())
    summary = ", ".join(f"{cause}={counts.get(cause, 0)}" for cause in CAUSES)
    short = [cause for cause in CAUSES if counts.get(cause, 0) < 5]
    assert not short, f"each cause needs >= 5 cases (other builders may not have run yet): {summary}"
    total = sum(counts.values())
    assert 30 <= total <= 50, f"dataset needs 30-50 cases, has {total}: {summary}"

def test_readme_documents_every_cause():
    readme = (JUDGE / "README.md").read_text()
    for cause in (*CAUSES, "not_stated"):
        assert cause in readme

def test_scanner_flags_planted_secret_and_private_path():
    key = "sk-" + "ant-" + "api03" + "Zq7" * 8
    path = "/" + "Users" + "/someone/project/calc.py"
    assert scan(f"invalid x-api-key {key}")
    assert scan(f"File {path}, line 3")
    assert scan("Authorization: Bearer " + "abc" + "123def456")
    assert scan("mail " + "dev" + "@" + "corp-internal.net")
    assert scan("connect to build01." + "acme" + ".io failed")

def test_scanner_accepts_redacted_and_allowlisted_text():
    assert scan("API Error: 401 invalid x-api-key <REDACTED>\nAuthorization: Bearer <REDACTED>") == []
    assert scan("POST https://api.anthropic.com/v1/messages from ops@example.com") == []
    assert scan("E   assert slugify('Hello World') == 'hello-world'\n1 failed, 3 passed") == []

@pytest.mark.parametrize("module_name", builder_modules())
def test_builder_output_matches_files_on_disk(module_name: str):
    module = importlib.import_module(f"scripts.judge_dataset.{module_name}")
    cases = module.build_cases()
    assert cases, f"{module_name}.build_cases() returned nothing"
    for case in cases:
        path = CASES_DIR / f"{case['id']}.json"
        assert path.exists(), f"{path.name} missing; run: uv run python -m scripts.judge_dataset.{module_name}"
        assert path.read_text() == json.dumps(case, indent=2, sort_keys=True) + "\n", (
            f"{path.name} is stale; run: uv run python -m scripts.judge_dataset.{module_name}"
        )

def test_every_case_file_comes_from_a_builder():
    built = {
        case["id"]
        for name in builder_modules()
        for case in importlib.import_module(f"scripts.judge_dataset.{name}").build_cases()
    }
    orphans = sorted(path.stem for path in case_files() if path.stem not in built)
    assert not orphans, f"case files with no builder: {orphans}"
