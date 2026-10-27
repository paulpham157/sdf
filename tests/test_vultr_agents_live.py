"""Live Codex, Claude Code and Pi Attempts in fresh E2B sandboxes against the
same Vultr Serverless Inference endpoint and model as OpenCode (glm-5.3).

Skipped by default.  Gated on ``SDF_LIVE_E2B=1`` and ``SDF_LIVE_VULTR_AGENTS=1``
plus ``E2B_API_KEY`` and ``VULTR_INFERENCE_API_KEY`` (env or the repo-root
``.env``, loaded into a private mapping).  ``SDF_LIVE_VULTR_AGENTS`` may also
name a comma-separated subset (``codex,claude,pi``).

Each case runs the full SDF loop (Task -> Attempt -> agent run -> host-side
criterion checks -> Evidence) on the toy ``calc.py`` add bug.

Wiring (no proxy):

* codex - native: SDF api-key mode, ``SDF_OPENAI_BASE_URL`` (Vultr serves the
  Responses API), ``-m glm-5.3``; Herdr pane.
* claude - native: SDF api-key mode, ``SDF_ANTHROPIC_BASE_URL`` (Vultr serves
  Anthropic ``/v1/messages``), ``--model glm-5.3``; Herdr pane.
* pi - not an SDF agent kind; a test-local headless runtime runs one
  ``pi -p --mode json`` turn with a ``models.json`` provider whose ``apiKey`` is
  the *name* of the key variable.

The account has no ``sdf-herdr-agents`` template (building one is out of
scope), so the sandbox factory bootstraps E2B's public ``codex`` template:
pinned Herdr (sha256-checked) and CLIs are installed with open egress, then
``update_network`` locks egress to the inference host before any credential
seed or agent runs.  The factory replaces ``_PRODUCTION_SANDBOX_FACTORY`` so
the ADR-0008 fresh-sandbox capability still applies to Claude.
"""

from __future__ import annotations

import json
import os
import shlex
import time
from pathlib import Path
from typing import Any

import pytest
from sqlalchemy.orm import sessionmaker

import sdf_core.e2b_herdr_transport as e2b_transport_module
from sdf_core.adapter import RuntimeAgentAdapter
from sdf_core.agent_models import model_agent_args
from sdf_core.credential_injection import create_credentialed_transport
from sdf_core.credentials import load_dotenv
from sdf_core.db import ArtifactRow, Base, DecisionEdgeRow, EvidenceRow, GraphNodeRow, TaskRow, make_engine
from sdf_core.e2b_herdr_transport import E2BHerdrTransport
from sdf_core.execution import ExecutionService
from sdf_core.herdr_runtime import HerdrRuntime
from sdf_core.model import utcnow
from sdf_core.opencode_runtime import OpenCodeHeadlessRuntime, OpenCodeRuntimeError, _split_exit
from sdf_core.runtime import RuntimeController, RuntimeSession, RuntimeStatus
from tests.test_herdr_e2b_execution import POSITIVE

ENV = dict(os.environ)
load_dotenv(environ=ENV)
KEY_VAR = "VULTR_INFERENCE_API_KEY"
_SELECTED = ENV.get("SDF_LIVE_VULTR_AGENTS", "")
AGENTS = ("codex", "claude", "pi") if _SELECTED == "1" else tuple(a for a in _SELECTED.split(",") if a)
LIVE = ENV.get("SDF_LIVE_E2B") == "1" and bool(AGENTS) and bool(ENV.get("E2B_API_KEY")) and bool(ENV.get(KEY_VAR))
pytestmark = pytest.mark.skipif(
    not LIVE, reason="live check: set SDF_LIVE_E2B=1 SDF_LIVE_VULTR_AGENTS=1 with E2B_API_KEY and VULTR_INFERENCE_API_KEY"
)

BASE_TEMPLATE = ENV.get("SDF_E2B_VULTR_BASE_TEMPLATE", "codex")
INFERENCE_HOST = "api.vultrinference.com"
MODEL = "glm-5.3"
PRICE_IN, PRICE_OUT = 0.75 / 1e6, 3.00 / 1e6  # Vultr glm-5.3, USD per token
HERDR_URL = "https://github.com/herdrdev/herdr/releases/download/v0.9.1/herdr-linux-x86_64"
HERDR_SHA256 = "2a02fed16beb651ef006e1d43f048f652ca4dc58ad053cd2d44450563d5c54b7"
PACKAGES = ("@openai/codex@0.157.0", "@anthropic-ai/claude-code@2.1.282", "@mariozechner/pi-coding-agent@0.73.1")
REPO = Path(__file__).resolve().parents[1]
CHECKS = {
    "add returns the sum": [["python3", "-c", "from calc import add; assert add(2, 3) == 5"]],
    "sub still subtracts": [["python3", "-c", "from calc import sub; assert sub(5, 3) == 2"]],
}
# Background "small model" calls would otherwise send a claude-haiku id that
# Vultr silently maps to another model.
CLAUDE_USER_SETTINGS = {
    "skipDangerousModePermissionPrompt": True,
    "env": {
        "ANTHROPIC_DEFAULT_HAIKU_MODEL": MODEL, "ANTHROPIC_SMALL_FAST_MODEL": MODEL,
        "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "DISABLE_AUTOUPDATER": "1",
    },
}


def _bootstrap_script() -> str:
    claude_json = (REPO / "infra/e2b/herdr-agents/claude/claude.json").read_text()
    return "\n".join((
        "set -e",
        f"sudo curl -fsSL --retry 3 -o /usr/local/bin/herdr {HERDR_URL}",
        f"echo '{HERDR_SHA256}  /usr/local/bin/herdr' | sha256sum -c - >/dev/null",
        "sudo chmod 0755 /usr/local/bin/herdr",
        "sudo npm install -g --no-fund --no-audit --loglevel=error " + " ".join(PACKAGES) + " >/tmp/npm.log 2>&1",
        "mkdir -p ~/.claude",
        f"printf %s {shlex.quote(claude_json)} > ~/.claude.json",
        f"printf %s {shlex.quote(json.dumps(CLAUDE_USER_SETTINGS))} > ~/.claude/settings.json",
        "herdr --version; codex --version; claude --version; pi --version 2>&1",
    ))


class BootstrapFactory:
    """``Sandbox.create`` stand-in: public template + pinned tools, then lock egress."""

    def __init__(self) -> None:
        self.versions = ""
        self.timings: dict[str, float] = {}

    def __call__(self, *, template: str, timeout: int, envs: dict[str, str], network: Any = None, **opts: Any):
        from e2b import Sandbox

        started = time.monotonic()
        sandbox = Sandbox.create(template=BASE_TEMPLATE, timeout=timeout, envs=envs, **opts)
        try:
            self.versions = sandbox.commands.run(_bootstrap_script(), timeout=300).stdout.strip()
            sandbox.commands.run("herdr server >/tmp/herdr-server.log 2>&1", background=True)
            for _ in range(30):
                if sandbox.commands.run("herdr workspace list >/dev/null 2>&1 && echo ok || true", timeout=10).stdout.strip() == "ok":
                    break
                time.sleep(1)
            if network is not None:
                sandbox.update_network(dict(network))
        except BaseException:
            sandbox.kill()
            raise
        self.timings["bootstrap_s"] = round(time.monotonic() - started, 1)
        return sandbox


# --- Pi: test-local headless runtime ---------------------------------------
_PI_EXIT = "SDF_OPENCODE_EXIT="  # reuse the OpenCode exit-marker parser
_PI_WRAPPER = (
    'dir=$1; secs=$2; out=$3; err=$4; shift 4; cd "$dir" || { echo ' + _PI_EXIT + '97; exit 0; }; '
    # Pi has no switch to ignore project .pi/ (settings can auto-install
    # packages, extensions run code, models.json "!cmd" keys execute): refuse.
    'if [ -e .pi ]; then echo ' + _PI_EXIT + '96; exit 0; fi; '
    'export PI_OFFLINE=1 PI_SKIP_VERSION_CHECK=1 PI_TELEMETRY=0; unset PI_CODING_AGENT_DIR; '
    'timeout --kill-after=10 "$secs" "$@" </dev/null >"$out" 2>"$err"; rc=$?; '
    'grep -E \'^\\{"type":"(message_end|tool_execution_end)"\' "$out" || true; '
    'echo ' + _PI_EXIT + '$rc; if [ "$rc" -ne 0 ]; then tail -c 2000 "$err"; fi; exit 0'
)
PI_MODELS = {
    "providers": {
        "vultr": {
            "baseUrl": f"https://{INFERENCE_HOST}/v1", "api": "openai-completions", "apiKey": KEY_VAR,
            "models": [{
                "id": MODEL, "name": "GLM 5.3 (Vultr)", "reasoning": True, "input": ["text"],
                "contextWindow": 1_000_000, "maxTokens": 32_000,
                "cost": {"input": 0.75, "output": 3.0, "cacheRead": 0, "cacheWrite": 0},
            }],
        }
    }
}


class PiHeadlessRuntime(OpenCodeHeadlessRuntime):
    def __init__(self, **kwargs: Any) -> None:
        super().__init__(binary="pi", expected_version="0.73.1", credentials={}, **kwargs)
        self.pi_usage: dict[str, Any] = {"input": 0, "output": 0, "cacheRead": 0, "total": 0, "cost": 0.0,
                                         "messages": 0, "models": [], "response_models": [], "errors": []}

    def start(self, *, attempt_id: str, agent: str) -> RuntimeSession:
        assert agent == "pi"
        workspace = self._workspaces[attempt_id]
        self.require_compatible()
        self._transport.exec_argv(
            ["python3", "-c", "import os,sys;d=os.path.expanduser('~/.pi/agent');os.makedirs(d,exist_ok=True);"
             "open(d+'/models.json','w').write(sys.argv[1])", json.dumps(PI_MODELS)], self._control_timeout_ms)
        from sdf_core.opencode_runtime import _Binding

        session_id = f"pi-{attempt_id}"
        self._bindings[session_id] = _Binding(attempt_id, workspace, False)
        session = RuntimeSession(session_id, attempt_id, agent, RuntimeStatus.CREATED)
        self._sessions[session_id] = session
        return session

    def command(self, session_id: str, input_text: str) -> list[str]:
        binding, _ = self._known(session_id)
        argv = ["pi", "-p", "--mode", "json", "--no-session", "--no-extensions", "--no-skills",
                "--no-prompt-templates", "--no-themes", "--provider", "vultr", "--model", MODEL, input_text]
        out, err = f"/tmp/sdf-pi-{binding.attempt_id}.jsonl", f"/tmp/sdf-pi-{binding.attempt_id}.err"
        return ["sh", "-c", _PI_WRAPPER, "sdf-pi", binding.workspace, str(self._turn_timeout_s), out, err, *argv]

    def send(self, session_id: str, input_text: str) -> RuntimeSession:
        _, current = self._known(session_id)
        self._set(session_id, RuntimeStatus.RUNNING, current.output)
        raw = self._transport.exec_argv(self.command(session_id, input_text), (self._turn_timeout_s + 40) * 1000)
        body, rc = _split_exit(raw)
        lines: list[str] = []
        for line in body.splitlines():
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                lines.append(line)
                continue
            if event.get("type") == "tool_execution_end":
                lines.append(f"[tool {event.get('toolName')}: {'error' if event.get('isError') else 'ok'}]")
                continue
            message = event.get("message") or {}
            if message.get("role") != "assistant":
                continue
            usage = message.get("usage") or {}
            u = self.pi_usage
            u["messages"] += 1
            for key in ("input", "output", "cacheRead"):
                u[key] += int(usage.get(key) or 0)
            u["total"] += int(usage.get("totalTokens") or 0)
            u["cost"] += float((usage.get("cost") or {}).get("total") or 0)
            for key, bucket in (("model", "models"), ("responseModel", "response_models")):
                if message.get(key) and message[key] not in u[bucket]:
                    u[bucket].append(message[key])
            if message.get("errorMessage"):
                u["errors"].append(str(message["errorMessage"])[:300])
            for part in message.get("content") or []:
                if part.get("type") == "text" and part.get("text", "").strip():
                    lines.append(part["text"])
        output = current.output + tuple(lines)
        if rc == 0 and not self.pi_usage["errors"]:
            return self._set(session_id, RuntimeStatus.COMPLETED, output)
        self._set(session_id, current.status, output)
        raise OpenCodeRuntimeError(f"pi run failed (exit {rc}): {'; '.join(self.pi_usage['errors']) or (lines[-1] if lines else '')}")

    def installed_version(self) -> str:
        # pi prints its version on stderr
        return self._transport.exec_argv(["sh", "-c", "pi --version 2>&1"], self._control_timeout_ms).strip()


# --- agent-side records of model and usage ----------------------------------
_RECORDS = r'''
import glob, json, os, sys
agent = sys.argv[1]; h = os.path.expanduser("~"); out = {}
def lines(pattern):
    for f in sorted(glob.glob(pattern, recursive=True)):
        for line in open(f, errors="replace"):
            try: yield json.loads(line)
            except Exception: pass
if agent == "claude":
    # One API response is split over several transcript entries; take the
    # largest value per usage field per response id.
    msgs, usage, sample = {}, {}, None
    fields = {"input": "input_tokens", "output": "output_tokens",
              "cache_read": "cache_read_input_tokens", "cache_write": "cache_creation_input_tokens"}
    for e in lines(h + "/.claude/projects/**/*.jsonl"):
        m = e.get("message") or {}
        if e.get("type") == "assistant" and m.get("id"):
            msgs[m["id"]] = m.get("model")
            x = m.get("usage") or {}
            sample = sample or x
            slot = usage.setdefault(m["id"], dict.fromkeys(fields, 0))
            for k, f in fields.items():
                slot[k] = max(slot[k], x.get(f) or 0)
    u = {k: sum(slot[k] for slot in usage.values()) for k in fields}
    out = {"api_calls": len(msgs), "served_models": sorted({str(v) for v in msgs.values()}), "usage": u,
           "usage_sample": sample}
elif agent == "codex":
    usage, models, notes = None, set(), []
    for e in lines(h + "/.codex/sessions/**/*.jsonl"):
        p = e.get("payload") or {}
        if e.get("type") == "turn_context" and p.get("model"): models.add(p["model"])
        if p.get("type") == "token_count" and p.get("info"): usage = p["info"].get("total_token_usage")
        text = json.dumps(p)
        if p.get("type") in ("warning", "error", "stream_error") or "reroute" in text.lower():
            notes.append(text[:300])
    out = {"requested_models": sorted(models), "usage": usage, "notes": notes[:5]}
print(json.dumps(out))
'''


def _leaks(text: str, secrets: dict[str, str]) -> list[str]:
    return sorted(
        name for name, value in secrets.items() if value in text or any(
            value[i : i + 16] in text for i in range(0, max(len(value) - 16, 0) + 1, 8)
        )
    )


def _running_ids() -> set[str]:
    from e2b import Sandbox

    paginator = Sandbox.list(api_key=ENV["E2B_API_KEY"])
    ids: set[str] = set()
    while paginator.has_next:
        ids.update(item.sandbox_id for item in paginator.next_items())
    return ids


EGRESS_PROBE = (
    "for u in https://example.com https://registry.npmjs.org https://api.anthropic.com https://chatgpt.com "
    f"https://{INFERENCE_HOST}/v1/models; do "
    "code=$(curl -s -o /dev/null -m 8 -w '%{http_code}' \"$u\" 2>/dev/null); echo \"$u $code\"; done"
)


@pytest.mark.parametrize("agent", ["codex", "claude", "pi"])
def test_live_agent_on_vultr_attempt_in_e2b(agent: str, tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    if agent not in AGENTS:
        pytest.skip(f"{agent} not selected in SDF_LIVE_VULTR_AGENTS")
    factory = BootstrapFactory()
    monkeypatch.setattr(e2b_transport_module, "_PRODUCTION_SANDBOX_FACTORY", factory)
    network = {"allow_out": [INFERENCE_HOST], "deny_out": ["0.0.0.0/0"]}
    secrets = {name: ENV[name] for name in ("E2B_API_KEY", KEY_VAR)}
    before = _running_ids()
    environ = {"E2B_API_KEY": ENV["E2B_API_KEY"], KEY_VAR: ENV[KEY_VAR]}
    injection = None
    if agent == "codex":
        environ |= {"SDF_CREDENTIAL_MODE_CODEX": "api-key", "SDF_CODEX_API_KEY_ENV": KEY_VAR,
                    "SDF_OPENAI_BASE_URL": f"https://{INFERENCE_HOST}/v1", "SDF_CODEX_MODEL": MODEL}
    elif agent == "claude":
        environ |= {"SDF_CREDENTIAL_MODE_CLAUDE": "api-key", "SDF_CLAUDE_API_KEY_ENV": KEY_VAR,
                    "SDF_ANTHROPIC_BASE_URL": f"https://{INFERENCE_HOST}", "SDF_CLAUDE_MODEL": MODEL}
    if agent == "pi":
        transport = E2BHerdrTransport(template=BASE_TEMPLATE, environ=environ, envs={KEY_VAR: ENV[KEY_VAR]},
                                      timeout_seconds=900, network=network)
        runtime: Any = PiHeadlessRuntime(transport=transport, turn_timeout_s=420)
    else:
        transport, injection = create_credentialed_transport(
            agents=(agent,), environ=environ, template=BASE_TEMPLATE, timeout_seconds=900, network=network
        )
        agent_args = model_agent_args((agent,), environ)
        if agent == "codex":
            # Vultr's Responses API rejects Codex's hosted ``web_search`` tool
            # (400 "Unsupported tool type"), so the first turn fails without this.
            agent_args["codex"] = (*agent_args["codex"], "-c", 'web_search="disabled"')
        runtime = HerdrRuntime(transport=transport, timeout_ms=300_000, agent_args=agent_args)

    created: list[str] = []
    probes: dict[str, str] = {}
    close = transport.close

    def recording_close(timeout_ms: int = 30_000) -> None:
        if transport.sandbox_id and transport.sandbox_id not in created:
            created.append(transport.sandbox_id)
        close(timeout_ms)

    transport.close = recording_close  # type: ignore[method-assign]
    terminate = runtime.terminate

    def probing_terminate(session_id: str):
        if transport.sandbox_id and transport.sandbox_id not in created:
            created.append(transport.sandbox_id)
        for name, argv in (("egress", ["sh", "-c", EGRESS_PROBE]), ("records", ["python3", "-c", _RECORDS, agent])):
            try:
                probes[name] = transport.exec_argv(argv, 90_000)
            except Exception as exc:  # noqa: BLE001 - diagnostics only
                probes[name] = f"probe failed: {type(exc).__name__}"
        if agent != "pi":
            try:
                probes["pane"] = runtime._raw(runtime._command("agent", "read", session_id, "--source", "recent-unwrapped"))
            except Exception as exc:  # noqa: BLE001
                probes["pane"] = f"read failed: {type(exc).__name__}"
        return terminate(session_id)

    runtime.terminate = probing_terminate  # type: ignore[method-assign]

    engine = make_engine()
    Base.metadata.create_all(engine)
    db = sessionmaker(engine, expire_on_commit=False)()
    task_id = f"TASK-VULTR-{agent.upper()}"
    db.add(GraphNodeRow(id="ASSUMPTION-CALC", kind="assumption", title="add() is correct", source="test",
                        owner="product", confidence=1.0, created_at=utcnow()))
    db.add(TaskRow(id=task_id, title="fix add", status="created", idempotency_key=f"task-vultr-{agent}",
                   acceptance_criteria=list(CHECKS), created_at=utcnow()))
    db.commit()
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "calc.py").write_text(
        "def add(a, b):\n    return a - b\n\n\ndef sub(a, b):\n    return a - b\n", encoding="utf-8"
    )
    service = ExecutionService(
        db, workspace_root=tmp_path / "workspaces", artifact_root=tmp_path / "artifacts",
        adapter=RuntimeAgentAdapter(RuntimeController(runtime, source=f"{agent}-vultr-e2b"), agent=agent),
    )
    started = time.monotonic()
    error = None
    attempt = None
    try:
        attempt = service.run(
            task_id=task_id, dispatch_key=f"dispatch-vultr-{agent}", fixture=fixture, instructions=POSITIVE,
            commands=[], criterion_checks=CHECKS, validation_target=("assumption", "ASSUMPTION-CALC"),
        )
    except Exception as exc:  # noqa: BLE001 - reported below
        error = f"{type(exc).__name__}: {str(exc)[-600:]}"
    finally:
        elapsed = round(time.monotonic() - started, 1)
        sandbox_alive_s = elapsed
        transport.close()
        deadline = time.monotonic() + 30
        while (_running_ids() - before) and time.monotonic() < deadline:
            time.sleep(1)
        leftover = sorted(_running_ids() - before)

    evidence = db.query(EvidenceRow).filter_by(attempt_id=attempt.id).all() if attempt else []
    relations = {edge.relation for edge in db.query(DecisionEdgeRow).filter_by(target_id="ASSUMPTION-CALC")}
    artifacts = db.query(ArtifactRow).filter_by(attempt_id=attempt.id).all() if attempt else []
    artifact_text = "\n".join(
        Path(a.uri).read_text(errors="replace") for a in artifacts if a.uri and Path(a.uri).is_file()
    )
    try:
        records = json.loads(probes.get("records", "").strip().splitlines()[-1])
    except (ValueError, IndexError):
        records = {"raw": probes.get("records", "")[-300:]}
    if agent == "pi":
        records = dict(runtime.pi_usage)
        tokens_in, tokens_out = records["input"] + records["cacheRead"], records["output"]
    elif agent == "claude":
        u = records.get("usage") or {}
        tokens_in, tokens_out = u.get("input", 0) + u.get("cache_read", 0) + u.get("cache_write", 0), u.get("output", 0)
    else:
        u = records.get("usage") or {}
        tokens_in, tokens_out = u.get("input_tokens", 0), u.get("output_tokens", 0)
    leaked = _leaks(
        "\n".join((artifact_text, json.dumps(probes), json.dumps(records), repr(injection),
                   *(injection.seed_commands if injection else ()), factory.versions,
                   json.dumps([(e.criterion, e.status, e.command) for e in evidence], default=str))),
        secrets,
    )
    report = {
        "agent": agent, "base_template": BASE_TEMPLATE, "versions": factory.versions.splitlines(),
        "attempt_status": attempt.status if attempt else None, "task": db.get(TaskRow, task_id).status,
        "error": error, "evidence": sorted((e.criterion, e.status) for e in evidence), "relations": sorted(relations),
        "records": records, "tokens_in": tokens_in, "tokens_out": tokens_out,
        "llm_cost_usd_est": round(tokens_in * PRICE_IN + tokens_out * PRICE_OUT, 5),
        "elapsed_s": elapsed, "bootstrap_s": factory.timings.get("bootstrap_s"),
        "e2b_cost_usd_est": round((sandbox_alive_s + 5) * 0.000056, 5),
        "sandboxes_created": len(created), "leftover_sandboxes": len(leftover),
        "egress": probes.get("egress", "").strip().splitlines(), "leaks": leaked,
        "pane_tail": probes.get("pane", "")[-1500:] if agent != "pi" else None,
        "stream_tail": list(runtime.stream(next(iter(runtime._bindings))))[-6:] if agent == "pi" and runtime._bindings else None,
    }
    print(f"SDF_LIVE_VULTR_REPORT {json.dumps(report, default=str)}")
    Path("/tmp/sdf-live-reports").mkdir(exist_ok=True)
    (Path("/tmp/sdf-live-reports") / f"vultr-{agent}.json").write_text(json.dumps(report, default=str, indent=1))
    assert not leaked
    assert not leftover and len(created) == 1
    assert error is None
    assert report["task"] == "succeeded"
    assert report["evidence"] == [("add returns the sum", "PASS"), ("sub still subtracts", "PASS")]
    assert relations == {"validates"}
