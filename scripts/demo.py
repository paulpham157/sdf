#!/usr/bin/env python3
"""One-command offline evidence demo (no E2B or model keys).

Walks the public HTTP loop twice with the default fake adapter: an honest
edit that verification accepts, and a plausible-but-wrong edit the agent
claims succeeded. Prints a plain-language story plus a self-contained HTML
page. This fixture loop is not production proof. The sealed evaluator
oracle (ADR 0039) produces a verdict only on Linux x86_64 with Landlock.
"""

from __future__ import annotations

import argparse
import atexit
import ctypes
import html
import json
import os
import platform
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import webbrowser
from pathlib import Path

CODESPACES_URL = "https://codespaces.new/paulpham157/sdf"
PASS_BADGE = "PASS — accepted by evidence"
REJECT_BADGE = "REJECTED — agent said done"
PROVIDER_KEY_VARS = (
    "E2B_API_KEY",
    "OPENAI_API_KEY",
    "ANTHROPIC_API_KEY",
    "OPENCODE_API_KEY",
    "PI_API_KEY",
    "QWEN_API_KEY",
    "GOOSE_API_KEY",
)
_LIVE_ENV = (
    "SDF_LIVE_FULL_LOOP",
    "SDF_LIVE_E2B",
    "SDF_REQUIRE_PLAN_APPROVAL",
    "SDF_EVALUATOR_CONTAINMENT",
    "SDF_EVALUATOR_CHECKS_FILE",
    "SDF_DOTENV",
)
# landlock_create_ruleset; flags=1 asks for the ABI version (same probe as
# scripts/demo-evidence.sh).
_LANDLOCK_CREATE_RULESET = 444
_LANDLOCK_CREATE_RULESET_VERSION = 1
_HTML_NAME = "index.html"

CLAIM_A = "Done. app.py now prints updated."
CLAIM_B = "Done! app.py updated, all tests pass"
# Demo-only fixture so verification can treat stored MACs as authentic.
# Not a deployment secret and not the public development constant.
DEMO_EVIDENCE_MAC_KEY = "demo-evidence-mac-key-not-a-deployment-secret"
MIN_EVIDENCE_MAC_KEY_CHARS = 32
REPLACEMENT_A = "print('updated')\n"
REPLACEMENT_B = "print('updated ')\n"

TASK_A = {
    "id": "TASK-OFFLINE-001",
    "title": "Apply the honest fake edit",
    "idempotency_key": "offline-evidence-001",
    "dispatch_key": "offline-evidence-001",
    "instructions": "replace app.py so it prints updated",
    "label": "A",
    "name": "honest edit",
}
TASK_B = {
    "id": "TASK-OFFLINE-002",
    "title": "Apply a plausible but wrong edit",
    "idempotency_key": "offline-evidence-002",
    "dispatch_key": "offline-evidence-002",
    "instructions": "replace app.py so it looks updated and make tests pass",
    "label": "B",
    "name": "plausible but wrong",
}


def weak_test_source() -> str:
    return (
        "from pathlib import Path\n\n\n"
        "def test_app_mentions_updated():\n"
        "    # Naive substring check: also passes for print('updated ').\n"
        '    assert "updated" in Path("app.py").read_text(encoding="utf-8")\n'
    )


def weak_checks_source() -> str:
    return json.dumps({
        "checks": {
            "app-updated": ["python3", "-c", "print('all tests pass')"],
        }
    }, indent=2) + "\n"


def repo_root() -> Path:
    return Path(__file__).resolve().parent.parent


def linux_x86_64() -> bool:
    system = platform.system()
    machine = platform.machine().lower()
    return system == "Linux" and machine in {"x86_64", "amd64"}


def landlock_available() -> bool:
    if not linux_x86_64():
        return False
    try:
        result = ctypes.CDLL(None).syscall(
            _LANDLOCK_CREATE_RULESET, None, 0, _LANDLOCK_CREATE_RULESET_VERSION
        )
    except OSError:
        return False
    return result >= 1


def unsupported_host_message() -> str | None:
    """A user-facing reason this host cannot run the sealed-oracle demo, or None."""
    system = platform.system()
    machine = platform.machine()
    hint = (
        f"Open the repository in GitHub Codespaces (the button at the top of "
        f"the README, or {CODESPACES_URL}) to run it on Linux x86_64."
    )
    if not linux_x86_64():
        return (
            f"The sealed evaluator oracle needs Linux x86_64, so this demo "
            f"cannot produce a PASS on {system} {machine}.\n{hint}"
        )
    if not landlock_available():
        return (
            "The sealed evaluator oracle needs Linux x86_64 with Landlock, "
            "and this kernel does not expose Landlock.\n" + hint
        )
    return None


def provider_keys_present(environ: dict[str, str] | None = None) -> bool:
    env = os.environ if environ is None else environ
    return any(env.get(name) for name in PROVIDER_KEY_VARS)


def configure_evidence_mac_key() -> str:
    """Keep a user-supplied key, or install the demo fixture key.

    A configured key must be at least 32 characters; a shorter value is
    rejected rather than replaced, so the process never silently uses the
    public development constant. When the variable is unset, the labelled
    demo fixture is used so Task A can be accepted via a verified PASS.
    """

    configured = os.environ.get("SDF_EVIDENCE_MAC_KEY")
    if configured:
        if len(configured) < MIN_EVIDENCE_MAC_KEY_CHARS:
            print(
                "SDF_EVIDENCE_MAC_KEY must be at least 32 characters. "
                "The demo does not replace a short key and does not fall "
                "back to the public development constant.",
                file=sys.stderr,
            )
            raise SystemExit(2)
        return configured
    os.environ["SDF_EVIDENCE_MAC_KEY"] = DEMO_EVIDENCE_MAC_KEY
    return DEMO_EVIDENCE_MAC_KEY


def configure_env(demo_dir: Path, root: Path) -> Path:
    os.environ["SDF_RUN_ADAPTER"] = "fake"
    os.environ["SDF_AUTH_DEV_INSECURE"] = "1"
    os.environ["SDF_FIXTURES_ROOT"] = str(root / "fixtures")
    os.environ["SDF_DATABASE_URL"] = f"sqlite+pysqlite:///{demo_dir / 'sdf.db'}"
    os.environ["SDF_WORKSPACE_ROOT"] = str(demo_dir / "workspaces")
    os.environ["SDF_ARTIFACT_ROOT"] = str(demo_dir / "artifacts")
    spec_path = demo_dir / "fake-spec.json"
    os.environ["SDF_DEMO_FAKE_SPEC"] = str(spec_path)
    configure_evidence_mac_key()
    for name in _LIVE_ENV:
        os.environ.pop(name, None)
    return spec_path


def write_fake_spec(
    spec_path: Path,
    *,
    replacement: str,
    stdout: str,
    extra_files: dict[str, str] | None = None,
) -> None:
    spec_path.write_text(
        json.dumps(
            {
                "relative_file": "app.py",
                "replacement": replacement,
                "stdout": stdout,
                "extra_files": extra_files or {},
            },
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )


def _free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def _json_request(method: str, url: str, body: dict | None = None):
    data = None if body is None else json.dumps(body).encode("utf-8")
    headers = {"content-type": "application/json"} if body is not None else {}
    request = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=60) as response:
            raw = response.read()
            payload = json.loads(raw.decode("utf-8")) if raw else None
            return response.status, payload
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")
        raise SystemExit(f"{method} {url} failed: {exc.code} {detail}") from exc


class LocalApi:
    """uvicorn on 127.0.0.1, torn down on exit or Ctrl-C."""

    def __init__(self, root: Path):
        self.root = root
        self.proc: subprocess.Popen[bytes] | None = None
        self.base = ""
        self._cleaned = False

    def start(self) -> str:
        port = _free_port()
        self.base = f"http://127.0.0.1:{port}"
        self.proc = subprocess.Popen(
            [sys.executable, "-m", "uvicorn", "sdf_core.api:app",
             "--host", "127.0.0.1", "--port", str(port)],
            cwd=str(self.root),
            env=os.environ.copy(),
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
        )
        atexit.register(self.stop)
        ready = False
        for _ in range(100):
            if self.proc.poll() is not None:
                out = b""
                if self.proc.stdout is not None:
                    out = self.proc.stdout.read() or b""
                raise SystemExit(
                    "API exited before becoming ready:\n"
                    + out.decode("utf-8", errors="replace")
                )
            try:
                with urllib.request.urlopen(self.base + "/healthz", timeout=1) as response:
                    if response.status == 200:
                        ready = True
                        break
            except (urllib.error.URLError, TimeoutError, OSError):
                time.sleep(0.1)
        if not ready:
            self.stop()
            raise SystemExit("API did not become ready on 127.0.0.1")
        return self.base

    def stop(self) -> None:
        if self._cleaned:
            return
        self._cleaned = True
        proc = self.proc
        if proc is None or proc.poll() is not None:
            return
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)


def seed_graph(base: str, tasks: list[dict]) -> None:
    steps = [
        ("/business-contexts", {
            "id": "BC-OFFLINE-001",
            "title": "Offline evidence demo",
            "owner": "local-dev",
            "source": "demo",
        }),
        ("/objectives", {
            "id": "OBJ-OFFLINE-001",
            "title": "Show independent fake-adapter evidence",
            "business_context_id": "BC-OFFLINE-001",
            "owner": "local-dev",
            "source": "demo",
        }),
        ("/assumptions", {
            "id": "ASSUMPTION-OFFLINE-001",
            "title": "fake adapter writes print(updated)",
            "owner": "local-dev",
            "source": "demo",
        }),
    ]
    for task in tasks:
        steps.append(("/tasks", {
            "id": task["id"],
            "title": task["title"],
            "objective_id": "OBJ-OFFLINE-001",
            "idempotency_key": task["idempotency_key"],
            "acceptance_criteria": ["app.py prints updated"],
        }))
    for path, body in steps:
        status, payload = _json_request("POST", base + path, body)
        if status not in {200, 201}:
            raise SystemExit(f"{path} failed: {status} {payload}")


def run_task(base: str, task: dict) -> dict:
    status, run = _json_request("POST", base + f"/tasks/{task['id']}/run", {
        "dispatch_key": task["dispatch_key"],
        "fixture_dir": "offline-evidence",
        "instructions": task["instructions"],
        "criterion_checks": {"app.py prints updated": ["app-updated"]},
        "validation_target_kind": "assumption",
        "validation_target_id": "ASSUMPTION-OFFLINE-001",
    })
    if status != 200:
        raise SystemExit(f"/run {task['id']} failed: {status} {run}")
    attempt_id = run["attempt_id"]
    _, trace = _json_request("GET", base + f"/tasks/{task['id']}/trace")
    evidence: dict[str, dict] = {}
    for node in trace["nodes"]:
        if node.get("kind") == "evidence":
            _, row = _json_request("GET", base + f"/evidence/{node['id']}")
            # A shared assumption's subgraph can include another task's Evidence.
            if row.get("attempt_id") != attempt_id:
                continue
            evidence[row["evidence_class"]] = row
    _, revision = _json_request("GET", base + f"/attempts/{attempt_id}/revision")
    return {
        "task": task,
        "run": run,
        "trace": trace,
        "evidence": evidence,
        "revision": revision,
        "attempt_id": attempt_id,
    }


def workspace_app(demo_dir: Path, root: Path, attempt_id: str) -> tuple[str, str]:
    before = (root / "fixtures" / "offline-evidence" / "app.py").read_text(encoding="utf-8")
    candidates = [
        demo_dir / "workspaces" / attempt_id / "app.py",
        root / ".sdf-workspaces" / attempt_id / "app.py",
    ]
    after = "(workspace file not found)"
    for path in candidates:
        if path.exists():
            after = path.read_text(encoding="utf-8")
            break
    return before, after


def _case_ok(case: dict, expect_accept: bool) -> bool:
    run = case["run"]
    verification = case["evidence"].get("verification", {})
    if expect_accept:
        return run.get("task_status") == "succeeded" and verification.get("status") == "PASS"
    return (
        run.get("task_status") != "succeeded"
        and verification.get("status") != "PASS"
        and verification.get("verifier_identity") == "sdf-evaluator"
        and run.get("attempt_status") == "completed"
    )


def print_story(cases: list[dict], html_path: Path) -> None:
    print()
    print("Software Decision Fabric — 60-second demo")
    print("=" * 44)
    print()
    print('The point: an agent saying "I\'m done" is not success.')
    print("A Task is accepted only when an independent evaluator")
    print("records Evidence that meets the acceptance criteria.")
    print()
    if provider_keys_present():
        print("provider keys are set in this shell and were not used")
    else:
        print("no E2B or model API keys in this shell")
    print()
    print("1. Intent")
    print("   Business context : Offline evidence demo")
    print("   Objective        : Show independent fake-adapter evidence")
    print("   Assumption       : the adapter writes print('updated')")
    print("   Acceptance       : app.py prints updated")
    if len(cases) == 2:
        print("   Two tasks, same criterion: an honest edit and a near-miss.")
    print()
    print("2. The agent's claim")
    for case in cases:
        task = case["task"]
        run = case["run"]
        print(f"   Task {task['label']} ({task['name']})")
        print(f"     claimed : {case['agent_claim']!r}")
        print(f"     before app.py : {case['before'].strip()}")
        print(f"     after  app.py : {case['after'].strip()}")
        if task["label"] == "B":
            print("     also wrote tests/test_app.py (a substring check that would pass)")
            print("     and rewrote sdf-checks.json to print('all tests pass')")
        print(
            f"     provider={run.get('provider')}  evidence_mode={run.get('evidence_mode')}  "
            f"attempt_status={run.get('attempt_status')}"
        )
        print("     The adapter finished. That is only an Attempt, not acceptance.")
    print()
    print("3. Independent check")
    for case in cases:
        task = case["task"]
        verification = case["evidence"].get("verification", {})
        execution = case["evidence"].get("execution", {})
        print(f"   Task {task['label']}")
        print(
            f"     execution evidence : class={execution.get('evidence_class')} "
            f"producer={execution.get('producer_identity')} "
            f"(records that the adapter ran; cannot accept the Task)"
        )
        print(
            f"     verification       : status={verification.get('status')} "
            f"verifier={verification.get('verifier_identity')} "
            f"criterion={verification.get('criterion')!r}"
        )
        if task["label"] == "B":
            print("     A naive workspace test would pass; the sealed check ignored it.")
    print()
    print("4. Outcome")
    by_label = {case["task"]["label"]: case for case in cases}
    for case in cases:
        task = case["task"]
        run = case["run"]
        verification = case["evidence"].get("verification", {})
        accepted = run.get("task_status") == "succeeded" and verification.get("status") == "PASS"
        label = "accepted" if accepted else "rejected"
        print(
            f"   Task {task['label']}: task_status={run.get('task_status')}  "
            f"attempt_status={run.get('attempt_status')}  ({label})"
        )
        print(
            f"   Task {task['label']}: verification status={verification.get('status')} "
            f"verifier={verification.get('verifier_identity')}"
        )
        print(f"   Task {task['label']}: attempt {case['attempt_id']}")
    if "A" in by_label and "B" in by_label:
        print("   A accepted / B rejected, though the agent said done")
    print()
    takeaway = (
        "An agent can say it is done and even make a weak test go green; "
        "SDF accepts a Task only when independent verification Evidence passes."
    )
    print(f"Takeaway: {takeaway}")
    print()
    print(f"Story page: {html_path.resolve().as_uri()}")
    print()

    problems = []
    if "A" in by_label and not _case_ok(by_label["A"], True):
        problems.append("Task A did not reach a verification PASS.")
    if "B" in by_label and not _case_ok(by_label["B"], False):
        problems.append("Task B was not rejected by independent verification.")
    if problems:
        raise SystemExit("\n".join(problems))


def _case_html(case: dict) -> str:
    task = case["task"]
    run = case["run"]
    verification = case["evidence"].get("verification", {})
    accepted = run.get("task_status") == "succeeded" and verification.get("status") == "PASS"
    badge = PASS_BADGE if accepted else REJECT_BADGE
    tone = "pass" if accepted else "fail"
    extra = ""
    if task["label"] == "B":
        extra = (
            "<p>The agent also wrote <code>tests/test_app.py</code> (a substring "
            "check that would pass) and rewrote <code>sdf-checks.json</code>. "
            "The sealed evaluator ignored both.</p>"
        )
    return f"""
    <section class="col {tone}">
      <h2>Task {html.escape(task['label'])} — {html.escape(task['name'])}</h2>
      <p><span class="badge {tone}">{html.escape(badge)}</span></p>
      <p><strong>Agent claimed:</strong> {html.escape(case['agent_claim'])}</p>
      <p>Attempt status: {html.escape(str(run.get('attempt_status')))}<br>
         Task status: {html.escape(str(run.get('task_status')))}</p>
      <p><strong>Evaluator:</strong> {html.escape(str(verification.get('status')))}
         from {html.escape(str(verification.get('verifier_identity')))}
         on {html.escape(str(verification.get('criterion')))}</p>
      {extra}
      <pre>before: {html.escape(case['before'].strip())}
after:  {html.escape(case['after'].strip())}</pre>
    </section>
    """


def write_html(cases: list[dict], path: Path) -> None:
    columns = "".join(_case_html(case) for case in cases)
    both = len(cases) == 2
    lede = (
        "The same acceptance criterion, two agent claims. SDF accepted only the "
        "edit the sealed evaluator could verify — not the one that merely looked done."
        if both else
        "SDF accepted or rejected this Task based on independent verification "
        "Evidence, not the fake agent’s stdout."
    )
    page = f"""<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>SDF demo — independent evidence</title>
  <style>
    :root {{ font-family: Georgia, "Times New Roman", serif; color: #1b1b1b; background: #f6f1e8; }}
    body {{ max-width: 980px; margin: 2.5rem auto; padding: 0 1.25rem 4rem; line-height: 1.5; }}
    h1 {{ font-size: 2rem; margin-bottom: 0.25rem; }}
    .lede {{ font-size: 1.15rem; }}
    .columns {{ display: flex; flex-wrap: wrap; gap: 1rem; margin: 1.5rem 0; }}
    .col {{ background: white; border: 1px solid #d9cbb3; border-radius: 10px; padding: 0.85rem 1rem; flex: 1; min-width: 280px; }}
    .col.pass {{ background: #e7f6e7; border-color: #3d8b40; }}
    .col.fail {{ background: #fdecea; border-color: #a12622; }}
    .badge {{ display: inline-block; padding: 0.2rem 0.65rem; border-radius: 999px; color: white; font-size: 0.85rem; }}
    .badge.pass {{ background: #1f6b2c; }}
    .badge.fail {{ background: #a12622; }}
    pre {{ background: #221f1a; color: #f6f1e8; padding: 0.9rem 1rem; border-radius: 8px; overflow: auto; }}
    code {{ font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.92em; }}
    .note {{ color: #5a5246; font-size: 0.95rem; }}
  </style>
</head>
<body>
  <h1>An agent finishing is not success</h1>
  <p class="lede">{html.escape(lede)}</p>
  <div class="columns">{columns}</div>
  <p class="note">This demo used the built-in <code>FakeNativeAdapter</code>,
  fixture <code>fixtures/offline-evidence</code>, a temporary SQLite database,
  <code>SDF_AUTH_DEV_INSECURE=1</code>, and a demo-only
  <code>SDF_EVIDENCE_MAC_KEY</code> fixture (not a deployment secret).
  No E2B or model API key was required.
  The sealed evaluator oracle (ADR 0039) currently produces a verdict only on
  Linux x86_64. This fixture loop is not production proof.</p>
</body>
</html>
"""
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(page, encoding="utf-8")


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Run the offline SDF evidence demo and write a local story page."
    )
    parser.add_argument(
        "--out",
        type=Path,
        default=None,
        help="directory for the HTML story page (default: a temporary directory)",
    )
    parser.add_argument(
        "--open",
        action="store_true",
        help="open the HTML story page in a browser",
    )
    parser.add_argument(
        "--only",
        choices=("accepted", "rejected"),
        default=None,
        help="run only the honest (accepted) task or only the rejected task",
    )
    return parser.parse_args(argv)


def selected_tasks(only: str | None) -> list[dict]:
    if only == "accepted":
        return [TASK_A]
    if only == "rejected":
        return [TASK_B]
    return [TASK_A, TASK_B]


def run_selected(
    base: str, spec_path: Path, demo_dir: Path, root: Path, tasks: list[dict]
) -> list[dict]:
    seed_graph(base, tasks)
    cases = []
    for task in tasks:
        if task["id"] == TASK_A["id"]:
            write_fake_spec(spec_path, replacement=REPLACEMENT_A, stdout=CLAIM_A)
            claim = CLAIM_A
        else:
            write_fake_spec(
                spec_path,
                replacement=REPLACEMENT_B,
                stdout=CLAIM_B,
                extra_files={
                    "tests/test_app.py": weak_test_source(),
                    "sdf-checks.json": weak_checks_source(),
                },
            )
            claim = CLAIM_B
        case = run_task(base, task)
        before, after = workspace_app(demo_dir, root, case["attempt_id"])
        case["before"] = before
        case["after"] = after
        case["agent_claim"] = claim
        cases.append(case)
    return cases


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    problem = unsupported_host_message()
    if problem:
        print(problem, file=sys.stderr)
        return 1
    root = repo_root()
    if not (root / "sdf_core" / "api.py").exists():
        print(f"repo not found at {root}", file=sys.stderr)
        return 2
    os.chdir(root)
    demo_dir = Path(tempfile.mkdtemp(prefix="sdf-demo-"))
    spec_path = configure_env(demo_dir, root)
    write_fake_spec(spec_path, replacement=REPLACEMENT_A, stdout=CLAIM_A)
    out_dir = args.out.resolve() if args.out is not None else demo_dir / "story"
    html_path = out_dir / _HTML_NAME
    server = LocalApi(root)

    def _on_signal(signum, _frame):
        server.stop()
        raise SystemExit(128 + int(signum))

    signal.signal(signal.SIGINT, _on_signal)
    signal.signal(signal.SIGTERM, _on_signal)
    try:
        base = server.start()
        cases = run_selected(base, spec_path, demo_dir, root, selected_tasks(args.only))
        write_html(cases, html_path)
        print_story(cases, html_path)
        if args.open:
            webbrowser.open(html_path.resolve().as_uri())
        return 0
    except KeyboardInterrupt:
        print("demo interrupted", file=sys.stderr)
        return 130
    finally:
        server.stop()


if __name__ == "__main__":
    raise SystemExit(main())
