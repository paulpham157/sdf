#!/usr/bin/env python3
"""One-command offline evidence demo (no E2B or model keys).

Walks the public HTTP loop with the default fake adapter and prints a
plain-language story plus a self-contained HTML page. This fixture loop is
not production proof. The sealed evaluator oracle (ADR 0039) produces a
PASS only on Linux x86_64 with Landlock.
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


def configure_env(demo_dir: Path, root: Path) -> None:
    os.environ["SDF_RUN_ADAPTER"] = "fake"
    os.environ["SDF_AUTH_DEV_INSECURE"] = "1"
    os.environ["SDF_FIXTURES_ROOT"] = str(root / "fixtures")
    os.environ["SDF_DATABASE_URL"] = f"sqlite+pysqlite:///{demo_dir / 'sdf.db'}"
    os.environ["SDF_WORKSPACE_ROOT"] = str(demo_dir / "workspaces")
    os.environ["SDF_ARTIFACT_ROOT"] = str(demo_dir / "artifacts")
    for name in _LIVE_ENV:
        os.environ.pop(name, None)


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


def run_loop(base: str) -> dict:
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
        ("/tasks", {
            "id": "TASK-OFFLINE-001",
            "title": "Apply the deterministic fake edit",
            "objective_id": "OBJ-OFFLINE-001",
            "idempotency_key": "offline-evidence-001",
            "acceptance_criteria": ["app.py prints updated"],
        }),
    ]
    for path, body in steps:
        status, payload = _json_request("POST", base + path, body)
        if status not in {200, 201}:
            raise SystemExit(f"{path} failed: {status} {payload}")
    status, run = _json_request("POST", base + "/tasks/TASK-OFFLINE-001/run", {
        "dispatch_key": "offline-evidence-001",
        "fixture_dir": "offline-evidence",
        "instructions": "replace app.py so it prints updated",
        "criterion_checks": {"app.py prints updated": ["app-updated"]},
        "validation_target_kind": "assumption",
        "validation_target_id": "ASSUMPTION-OFFLINE-001",
    })
    if status != 200:
        raise SystemExit(f"/run failed: {status} {run}")
    attempt_id = run["attempt_id"]
    _, trace = _json_request("GET", base + "/tasks/TASK-OFFLINE-001/trace")
    evidence: dict[str, dict] = {}
    for node in trace["nodes"]:
        if node.get("kind") == "evidence":
            _, row = _json_request("GET", base + f"/evidence/{node['id']}")
            evidence[row["evidence_class"]] = row
    _, revision = _json_request("GET", base + f"/attempts/{attempt_id}/revision")
    return {
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


def print_story(result: dict, before: str, after: str, html_path: Path) -> None:
    run = result["run"]
    verification = result["evidence"].get("verification", {})
    execution = result["evidence"].get("execution", {})
    edges = result["trace"].get("edges") or []
    validates = next((edge for edge in edges if edge.get("relation") == "validates"), None)
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
    print("   Task             : Apply the deterministic fake edit")
    print("   Acceptance       : app.py prints updated")
    print()
    print("2. Work  (fake adapter — no E2B, no model keys)")
    print(f"   provider={run.get('provider')}  evidence_mode={run.get('evidence_mode')}")
    print("   before app.py : " + before.strip())
    print("   after  app.py : " + after.strip())
    print("   The adapter finished. That is only an Attempt, not acceptance.")
    print()
    print("3. Independent check")
    print(
        f"   execution evidence : class={execution.get('evidence_class')} "
        f"producer={execution.get('producer_identity')} "
        f"(records that the adapter ran; cannot accept the Task)"
    )
    print(
        f"   verification       : status={verification.get('status')} "
        f"verifier={verification.get('verifier_identity')} "
        f"criterion={verification.get('criterion')!r}"
    )
    if validates:
        print(
            f"   graph edge         : {validates['source_kind']}:{validates['source_id']} "
            f"validates {validates['target_kind']}:{validates['target_id']}"
        )
    print()
    print("4. Outcome")
    passed = verification.get("status") == "PASS" and run.get("task_status") == "succeeded"
    why = (
        "verification Evidence PASS, not the agent's stdout"
        if passed
        else "see status below — this was not a clean PASS"
    )
    print(f"   task_status={run.get('task_status')}  attempt_status={run.get('attempt_status')}")
    print(f"   why: {why}")
    print(f"   attempt: {result['attempt_id']}")
    print()
    print(f"Story page: {html_path.resolve().as_uri()}")
    print()
    if not passed:
        raise SystemExit("Demo did not reach a verification PASS.")


def write_html(result: dict, before: str, after: str, path: Path) -> None:
    run = result["run"]
    verification = result["evidence"].get("verification", {})
    execution = result["evidence"].get("execution", {})
    nodes = result["trace"].get("nodes") or []
    edges = result["trace"].get("edges") or []
    node_html = "".join(
        f'<li><code>{html.escape(str(node.get("kind")))}</code> '
        f'{html.escape(str(node.get("id")))}</li>'
        for node in nodes
    )
    edge_html = "".join(
        f'<li>{html.escape(str(edge.get("source_kind")))}:'
        f'{html.escape(str(edge.get("source_id")))} '
        f'<strong>{html.escape(str(edge.get("relation")))}</strong> '
        f'{html.escape(str(edge.get("target_kind")))}:'
        f'{html.escape(str(edge.get("target_id")))}</li>'
        for edge in edges
    )
    passed = verification.get("status") == "PASS" and run.get("task_status") == "succeeded"
    badge = PASS_BADGE if passed else "Did not accept"
    page = f"""<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>SDF demo — independent evidence</title>
  <style>
    :root {{ font-family: Georgia, "Times New Roman", serif; color: #1b1b1b; background: #f6f1e8; }}
    body {{ max-width: 880px; margin: 2.5rem auto; padding: 0 1.25rem 4rem; line-height: 1.5; }}
    h1 {{ font-size: 2rem; margin-bottom: 0.25rem; }}
    .lede {{ font-size: 1.15rem; }}
    .flow {{ display: flex; flex-wrap: wrap; gap: 0.5rem; margin: 1.5rem 0; }}
    .card {{ background: white; border: 1px solid #d9cbb3; border-radius: 10px; padding: 0.85rem 1rem; min-width: 140px; flex: 1; }}
    .card h2 {{ font-size: 0.8rem; text-transform: uppercase; letter-spacing: 0.04em; margin: 0 0 0.35rem; color: #6b542e; }}
    .pass {{ background: #e7f6e7; border-color: #3d8b40; }}
    .exec {{ background: #f3f3f3; }}
    .badge {{ display: inline-block; padding: 0.2rem 0.65rem; border-radius: 999px; background: #1f6b2c; color: white; font-size: 0.85rem; }}
    pre {{ background: #221f1a; color: #f6f1e8; padding: 0.9rem 1rem; border-radius: 8px; overflow: auto; }}
    code {{ font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.92em; }}
    .note {{ color: #5a5246; font-size: 0.95rem; }}
  </style>
</head>
<body>
  <h1>An agent finishing is not success</h1>
  <p class="lede">SDF accepted this Task only after an independent evaluator
  checked the result. The fake agent’s own stdout did not count.</p>
  <p><span class="badge">{html.escape(badge)}</span></p>
  <div class="flow">
    <div class="card"><h2>Intent</h2>Business context → Objective → Assumption → Task<br>
      <em>Acceptance:</em> app.py prints updated</div>
    <div class="card"><h2>Attempt</h2>Fake adapter rewrote <code>app.py</code>.<br>
      Provider: {html.escape(str(run.get("provider")))}<br>
      Mode: {html.escape(str(run.get("evidence_mode")))}</div>
    <div class="card exec"><h2>Execution evidence</h2>The adapter ran.<br>
      Producer: {html.escape(str(execution.get("producer_identity")))}<br>
      <strong>Cannot accept the Task.</strong></div>
    <div class="card pass"><h2>Verification evidence</h2>
      Status: {html.escape(str(verification.get("status")))}<br>
      Verifier: {html.escape(str(verification.get("verifier_identity")))}<br>
      Criterion: {html.escape(str(verification.get("criterion")))}<br>
      <strong>This is what accepted the Task.</strong></div>
  </div>
  <h2>What changed</h2>
  <pre>before: {html.escape(before.strip())}
after:  {html.escape(after.strip())}</pre>
  <h2>Decision graph</h2>
  <p>Nodes</p>
  <ul>{node_html}</ul>
  <p>Edges</p>
  <ul>{edge_html}</ul>
  <p class="note">This demo used the built-in <code>FakeNativeAdapter</code>,
  fixture <code>fixtures/offline-evidence</code>, a temporary SQLite database,
  and <code>SDF_AUTH_DEV_INSECURE=1</code>. No E2B or model API key was required.
  The sealed evaluator oracle (ADR 0039) currently produces a PASS only on
  Linux x86_64. This fixture loop is not production proof.</p>
  <p class="note">task_status={html.escape(str(run.get("task_status")))}
  attempt={html.escape(str(result["attempt_id"]))}</p>
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
    return parser.parse_args(argv)


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
    configure_env(demo_dir, root)
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
        result = run_loop(base)
        before, after = workspace_app(demo_dir, root, result["attempt_id"])
        write_html(result, before, after, html_path)
        print_story(result, before, after, html_path)
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
