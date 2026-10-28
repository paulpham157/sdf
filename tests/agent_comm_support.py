"""Shared fixtures for the ADR 0014 tests: a toy repo, a plan, and a scripted
in-process ``AgentSandbox`` that drives the real ``sdf-msg`` shim."""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
from pathlib import Path
from typing import Any, Callable

from sdf_core.agent_sandbox import AgentSpec, TurnResult

SHIM = Path(__file__).resolve().parents[1] / "sdf_core" / "sdf_msg_shim.py"

CALC = "def add(a, b):\n    return a - b\n\n\ndef sub(a, b):\n    return a - b\n"
CALC2 = "def double(x):\n    return 2 * x\n"
TEST_CALC = "from calc import add, sub\n\nassert add(2, 3) == 5\nassert sub(5, 3) == 2\n"
TEST_CALC2 = "from calc2 import mul\n\nassert mul(4, 3) == 12\n"


def make_fixture(root: Path) -> Path:
    fixture = root / "toy"
    fixture.mkdir(parents=True)
    (fixture / "calc.py").write_text(CALC)
    (fixture / "calc2.py").write_text(CALC2)
    (fixture / "test_calc.py").write_text(TEST_CALC)
    (fixture / "test_calc2.py").write_text(TEST_CALC2)
    return fixture


def plan_dict(fixture: Path, plan_id: str | None = None) -> dict[str, Any]:
    return {
        **({"id": plan_id} if plan_id else {}),
        "title": "Fix add and implement mul",
        "proposed_by": "planner",
        "fixture_dir": str(fixture),
        "subtasks": [
            {"key": "add", "title": "Fix add in calc.py",
             "instructions": "In calc.py, add(a, b) must return a + b. Do not touch other files.",
             "acceptance_criteria": ["add returns the sum", "sub still subtracts", "test_calc passes"],
             "criterion_checks": {
                 "add returns the sum": [["python3", "-c", "from calc import add; assert add(2, 3) == 5"]],
                 "sub still subtracts": [["python3", "-c", "from calc import sub; assert sub(5, 3) == 2"]],
                 "test_calc passes": [["python3", "test_calc.py"]]},
             "allowed_paths": ["calc.py"], "protected_paths": ["test_calc.py"]},
            {"key": "mul", "title": "Implement mul in calc2.py",
             "instructions": "In calc2.py, add mul(a, b) returning a * b. Keep double().",
             "acceptance_criteria": ["mul multiplies", "double still doubles", "test_calc2 passes"],
             "criterion_checks": {
                 "mul multiplies": [["python3", "-c", "from calc2 import mul; assert mul(4, 3) == 12"]],
                 "double still doubles": [["python3", "-c", "from calc2 import double; assert double(4) == 8"]],
                 "test_calc2 passes": [["python3", "test_calc2.py"]]},
             "allowed_paths": ["calc2.py"], "protected_paths": ["test_calc2.py"]},
        ],
    }


Behavior = Callable[["FakeAgentSandbox", str, int], None]


class FakeAgentSandbox:
    """A local stand-in: per-agent HOME with the real shim, a workspace dir,
    scripted turns.  ``running`` is shared so tests can check cleanup."""

    def __init__(self, spec: AgentSpec, root: Path, behavior: Behavior, running: set[str]) -> None:
        self.spec = spec
        self.root = root / spec.agent_id
        self.home = self.root / "home"
        self.ws = self.root / "ws"
        self.behavior = behavior
        self.running = running
        self.turns = 0
        self._sandbox_id: str | None = None
        self.closed = False

    @property
    def sandbox_id(self) -> str | None:
        return self._sandbox_id

    @property
    def mailbox(self) -> Path:
        return self.home / ".sdf-msg"

    def start(self, workspace: Path) -> None:
        shutil.copytree(workspace, self.ws)
        for d in ("outbox", "inbox", "read"):
            (self.mailbox / d).mkdir(parents=True, exist_ok=True)
        (self.mailbox / "me").write_text(self.spec.agent_id)
        self._sandbox_id = f"fake-{self.spec.agent_id}-{id(self) % 10000}"
        self.running.add(self._sandbox_id)

    def msg(self, *args: str, stdin: str | None = None) -> str:
        env = {"HOME": str(self.home), "PATH": os.environ.get("PATH", "")}
        out = subprocess.run([sys.executable, str(SHIM), *args], cwd=self.ws, env=env, input=stdin,
                             capture_output=True, text=True, timeout=30)
        return out.stdout + out.stderr

    def read_json(self) -> list[dict[str, Any]]:
        msgs = []
        for path in sorted((self.mailbox / "inbox").glob("*.json")):
            msgs.append(json.loads(path.read_text()))
            path.rename(self.mailbox / "read" / path.name)
        return msgs

    def turn(self, prompt: str) -> TurnResult:
        self.turns += 1
        try:
            self.behavior(self, prompt, self.turns)
            return TurnResult(True, 0.01)
        except Exception as exc:  # noqa: BLE001
            return TurnResult(False, 0.01, f"{type(exc).__name__}: {exc}")

    def list_outbox(self) -> list[tuple[str, int]]:
        return [(p.name, p.stat().st_size) for p in (self.mailbox / "outbox").iterdir() if p.is_file()]

    def read_outbox(self, name: str) -> bytes:
        return (self.mailbox / "outbox" / name).read_bytes()

    def remove_outbox(self, name: str) -> None:
        (self.mailbox / "outbox" / name).unlink()

    def deliver(self, name: str, data: bytes) -> None:
        tmp = self.mailbox / "inbox" / f".{name}.part"
        tmp.write_bytes(data)
        tmp.rename(self.mailbox / "inbox" / name)

    def inbox_count(self) -> int:
        return sum(1 for p in (self.mailbox / "inbox").glob("*.json"))

    def collect(self, destination: Path) -> None:
        for child in destination.iterdir():
            shutil.rmtree(child) if child.is_dir() else child.unlink()
        for path in self.ws.rglob("*"):
            target = destination / path.relative_to(self.ws)
            if path.is_dir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(path.read_bytes())

    def usage(self) -> dict[str, Any]:
        return {"input_tokens": 1000 * self.turns, "output_tokens": 100 * self.turns}

    def alive_seconds(self) -> float:
        return 0.0

    def close(self) -> None:
        self.closed = True
        if self._sandbox_id:
            self.running.discard(self._sandbox_id)


# --- scripted behaviours ---------------------------------------------------

def lead_behavior(fake: FakeAgentSandbox, prompt: str, turn: int) -> None:
    for message in fake.read_json():
        body = message["body"]
        if message["from"] == "sdf" and body.startswith("APPROVED PLAN"):
            for line in body.splitlines():
                if line.startswith("- worker-"):
                    worker = line[2:].split(" ", 1)[0]
                    key = line.split("`")[1]
                    fake.msg("send", worker, f"Please do subtask {key}. Report done when finished.")
        elif message["from"] == "sdf" and "every subtask is verified PASS" in body:
            fake.msg("report", "--status", "done", "All subtasks verified by the host.")


FIXES = {"add": ("calc.py", CALC.replace("return a - b\n\n\ndef sub", "return a + b\n\n\ndef sub")),
         "mul": ("calc2.py", CALC2 + "\n\ndef mul(a, b):\n    return a * b\n")}


def worker_behavior(*, honest: bool = True, tamper_tests: bool = False, ask_first: bool = False) -> Behavior:
    def behave(fake: FakeAgentSandbox, prompt: str, turn: int) -> None:
        for message in fake.read_json():
            if message.get("from_role") != "lead":
                continue
            key = message["body"].split("subtask ", 1)[1].split(".", 1)[0]
            if ask_first:
                fake.msg("request-decision", "May I also refactor calc.py?", "--option", "yes", "--option", "no")
            if honest:
                name, content = FIXES[key]
                (fake.ws / name).write_text(content)
            if tamper_tests:
                (fake.ws / "test_calc.py").write_text("print('tests disabled')\n")
                (fake.ws / "test_calc2.py").write_text("print('tests disabled')\n")
                (fake.ws / "evil.py").write_text("print('outside scope')\n")
            fake.msg("report", "--status", "done", f"Subtask {key} is complete and all tests pass.")
    return behave


def fake_factory(root: Path, running: set[str], behaviors: dict[str, Behavior], created: list) -> Callable:
    def build(spec: AgentSpec) -> FakeAgentSandbox:
        behavior = behaviors.get(spec.agent_id) or behaviors[spec.role]
        sandbox = FakeAgentSandbox(spec, root, behavior, running)
        created.append(sandbox)
        return sandbox
    return build
