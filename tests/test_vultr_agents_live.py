"""Live Codex, Claude Code and Pi Attempts in fresh E2B sandboxes against the
same Vultr Serverless Inference endpoint and model as OpenCode (glm-5.3).

Skipped by default.  Gated on ``SDF_LIVE_E2B=1`` and ``SDF_LIVE_VULTR_AGENTS=1``
plus ``E2B_API_KEY`` and ``VULTR_INFERENCE_API_KEY`` (env or the repo-root
``.env``, loaded into a private mapping).  ``SDF_LIVE_VULTR_AGENTS`` may also
name a comma-separated subset (``codex,claude,pi,qwen,goose``); ``1``
keeps the original three.

Each case runs the full SDF loop (Task -> Attempt -> agent run -> host-side
criterion checks -> Evidence) on the toy ``calc.py`` add bug.

Wiring (no proxy):

* codex - native: SDF api-key mode, ``SDF_OPENAI_BASE_URL`` (Vultr serves the
  Responses API), ``-m glm-5.3``; Herdr pane.
* claude - native: SDF api-key mode, ``SDF_ANTHROPIC_BASE_URL`` (Vultr serves
  Anthropic ``/v1/messages``), ``--model glm-5.3``; Herdr pane.
* pi - the ``pi`` agent kind (ADR 0012): SDF api-key mode with
  ``SDF_PI_API_KEY_ENV=VULTR_INFERENCE_API_KEY`` and the default Vultr base URL
  / ``glm-5.3``; the ``pi-models``
  seed writes ``~/.pi/agent/models.json`` naming the key variable, and
  ``sdf_core.pi_runtime.PiHeadlessRuntime`` runs ``pi -p --mode json`` turns.
* qwen / goose - the ADR-0013 kinds (Aider removed, ADR 0016): SDF api-key mode with
  ``SDF_<KIND>_API_KEY_ENV=VULTR_INFERENCE_API_KEY`` and the default Vultr base
  URL / ``glm-5.3``; their seeds record the key's NAME and the headless runtimes
  in ``sdf_core.{qwen,goose}_runtime`` run one CLI process per turn.
  Not in the published template yet: on the ``codex`` base the factory installs
  Node 22 + Qwen Code (npm) or the sha256-checked Goose musl tarball before
  egress is locked.

Sandbox source:

* ``SDF_E2B_VULTR_TEMPLATE`` set (e.g. ``sdf-herdr-agents``, built from
  ``infra/e2b/herdr-agents``): the image already carries Herdr and the CLIs, so
  the factory only adds the Claude user settings below and makes sure the
  Herdr server is up.
* unset: the factory bootstraps E2B's public ``codex`` template
  (``SDF_E2B_VULTR_BASE_TEMPLATE``): pinned Herdr (sha256-checked) and CLIs are
  installed with open egress.

Either way ``update_network`` then locks egress to the inference host before
any credential seed or agent runs.  The factory replaces
``_PRODUCTION_SANDBOX_FACTORY`` so the ADR-0008 fresh-sandbox capability still
applies to Claude.
"""

from __future__ import annotations

import json
import os
import re
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
from sdf_core.execution import ExecutionService
from sdf_core.herdr_runtime import HerdrRuntime
from sdf_core.model import utcnow
from sdf_core.goose_runtime import GOOSE_VERSION, GooseHeadlessRuntime
from sdf_core.pi_runtime import PiHeadlessRuntime
from sdf_core.qwen_runtime import QWEN_VERSION, QwenHeadlessRuntime
from sdf_core.runtime import RuntimeController
from tests import live_project_marker
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

PREBUILT_TEMPLATE = ENV.get("SDF_E2B_VULTR_TEMPLATE", "")
BASE_TEMPLATE = PREBUILT_TEMPLATE or ENV.get("SDF_E2B_VULTR_BASE_TEMPLATE", "codex")
INFERENCE_HOST = "api.vultrinference.com"
MODEL = "glm-5.3"
PRICE_IN, PRICE_OUT = 0.75 / 1e6, 3.00 / 1e6  # Vultr glm-5.3, USD per token
HERDR_URL = "https://github.com/herdrdev/herdr/releases/download/v0.9.5/herdr-linux-x86_64"
HERDR_SHA256 = "14d9e8bc8b4a9e0118d77c32266f76d461f09b40342eb23752960bd89f59929d"
# The bare-base path needs Node >= 22.19 for Pi 1.x.
PACKAGES = ("@openai/codex@0.165.0", "@anthropic-ai/claude-code@2.1.308", "@earendil-works/pi-coding-agent@1.4.0")
NODE22_URL = "https://nodejs.org/dist/v22.20.0/node-v22.20.0-linux-x64.tar.xz"
NODE22_SHA256 = "00bbd05e306ea68b6e13e17360d0e2f680b493ef95f2fea1c4296ff7437530bc"
GOOSE_URL = (f"https://github.com/aaif-goose/goose/releases/download/v{GOOSE_VERSION}/"
             "goose-x86_64-unknown-linux-musl.tar.gz")
GOOSE_SHA256 = "80e7716fef3dccc3fef6850e24e5b846b7340e10e13098e53b4f47769f5803ef"
# ``SDF_LIVE_DELEGATE=1`` (ADR-0020): the prompt asks the agent to hand the
# work to a sub-agent first; the run must still succeed with no child agent.
DELEGATE = ENV.get("SDF_LIVE_DELEGATE") == "1"
DELEGATE_PREFIX = (
    "First, delegate this whole task to a sub-agent using your sub-agent / Task / Agent / spawn_agent / "
    "subagent / delegate tool, and let the sub-agent make the change. If you have no such tool or it fails, "
    "do the task yourself.\n\n"
)
# Tool names that would mean a child agent ran (any kind).
SUBAGENT_TOOLS = {"Agent", "Task", "Workflow", "spawn_agent", "agent", "create_sub_session", "subagent", "task",
                  "delegate", "summon"}
HEADLESS = {"pi": PiHeadlessRuntime, "qwen": QwenHeadlessRuntime, "goose": GooseHeadlessRuntime}
# Runtime installs for the ADR-0013 kinds on the bare ``codex`` base template only.
EXTRA_INSTALL = {
    "qwen": (
        f"curl -fsSL --retry 3 -o /tmp/node22.tar.xz {NODE22_URL}",
        f"echo '{NODE22_SHA256}  /tmp/node22.tar.xz' | sha256sum -c - >/dev/null",
        "sudo mkdir -p /opt/node22 && sudo tar -xJf /tmp/node22.tar.xz --strip-components=1 -C /opt/node22",
        "sudo env PATH=/opt/node22/bin:/usr/bin:/bin npm install -g --no-fund --no-audit --loglevel=error "
        f"@qwen-code/qwen-code@{QWEN_VERSION} >/tmp/npm-qwen.log 2>&1",
        "printf '#!/bin/sh\\nexec /opt/node22/bin/node /opt/node22/lib/node_modules/@qwen-code/qwen-code/cli-entry.js \"$@\"\\n'"
        " | sudo tee /usr/local/bin/qwen >/dev/null && sudo chmod 0755 /usr/local/bin/qwen",
        "qwen --version",
    ),
    "goose": (
        f"curl -fsSL --retry 3 -o /tmp/goose.tar.gz {GOOSE_URL}",
        f"echo '{GOOSE_SHA256}  /tmp/goose.tar.gz' | sha256sum -c - >/dev/null",
        "sudo tar -xzf /tmp/goose.tar.gz -C /usr/local/bin ./goose && sudo chmod 0755 /usr/local/bin/goose",
        "goose --version",
    ),
}
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


# A prebuilt image (sdf-herdr-agents) already carries the ADR-0013 CLIs: check
# their versions only; a missing CLI fails the bootstrap (no runtime install).
IMAGE_VERSION_CHECK = {
    "qwen": (f'test "$(qwen --version)" = {QWEN_VERSION}', "qwen --version"),
    "goose": (f'test "$(goose --version | tr -d \' \')" = {GOOSE_VERSION}', "goose --version"),
}


def _bootstrap_script(agent: str = "") -> str:
    extra = (IMAGE_VERSION_CHECK if PREBUILT_TEMPLATE else EXTRA_INSTALL).get(agent, ())
    if PREBUILT_TEMPLATE:
        # The image's settings.json only pre-accepts the dangerous-mode prompt.
        return "\n".join((
            "set -e",
            f"printf %s {shlex.quote(json.dumps(CLAUDE_USER_SETTINGS))} > ~/.claude/settings.json",
            "herdr --version; codex --version; claude --version; pi --version 2>&1",
            *extra,
        ))
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
        *extra,
    ))


class BootstrapFactory:
    """``Sandbox.create`` stand-in: template (+ pinned tools if public), then lock egress."""

    def __init__(self, agent: str = "") -> None:
        self.agent = agent
        self.versions = ""
        self.timings: dict[str, float] = {}
        self.created: list[str] = []  # every sandbox this factory started

    def __call__(self, *, template: str, timeout: int, envs: dict[str, str], network: Any = None, **opts: Any):
        from e2b import Sandbox

        started = time.monotonic()
        # Tag the sandbox so a leftover can be traced to this test run.
        opts["metadata"] = {**(opts.get("metadata") or {}), "sdf_live_test": f"vultr-{self.agent}-{os.getpid()}"}
        sandbox = Sandbox.create(template=BASE_TEMPLATE, timeout=timeout, envs=envs, **opts)
        self.created.append(sandbox.sandbox_id)
        try:
            self.versions = sandbox.commands.run(_bootstrap_script(self.agent), timeout=420).stdout.strip()
            if not PREBUILT_TEMPLATE or sandbox.commands.run(
                "herdr workspace list >/dev/null 2>&1 && echo ok || true", timeout=10
            ).stdout.strip() != "ok":
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
    msgs, usage, sample, tools = {}, {}, None, set()
    fields = {"input": "input_tokens", "output": "output_tokens",
              "cache_read": "cache_read_input_tokens", "cache_write": "cache_creation_input_tokens"}
    for e in lines(h + "/.claude/projects/**/*.jsonl"):
        m = e.get("message") or {}
        if e.get("type") == "assistant" and m.get("id"):
            msgs[m["id"]] = m.get("model")
            for c in m.get("content") or []:
                if isinstance(c, dict) and c.get("type") == "tool_use": tools.add(c.get("name"))
            x = m.get("usage") or {}
            sample = sample or x
            slot = usage.setdefault(m["id"], dict.fromkeys(fields, 0))
            for k, f in fields.items():
                slot[k] = max(slot[k], x.get(f) or 0)
    u = {k: sum(slot[k] for slot in usage.values()) for k in fields}
    out = {"api_calls": len(msgs), "served_models": sorted({str(v) for v in msgs.values()}), "usage": u,
           "usage_sample": sample, "tools_used": sorted(map(str, tools)),
           "subagent_transcripts": len(glob.glob(h + "/.claude/projects/**/subagents/**/*.jsonl", recursive=True))}
elif agent == "codex":
    usage, models, notes, tools, children = None, set(), [], set(), 0
    for e in lines(h + "/.codex/sessions/**/*.jsonl"):
        p = e.get("payload") or {}
        if e.get("type") == "session_meta" and "subagent" in json.dumps(p.get("source")): children += 1
        if p.get("type") in ("function_call", "custom_tool_call"): tools.add(p.get("name"))
        if e.get("type") == "turn_context" and p.get("model"): models.add(p["model"])
        if p.get("type") == "token_count" and p.get("info"): usage = p["info"].get("total_token_usage")
        text = json.dumps(p)
        if p.get("type") in ("warning", "error", "stream_error") or "reroute" in text.lower():
            notes.append(text[:300])
    out = {"requested_models": sorted(models), "usage": usage, "notes": notes[:5],
           "tools_used": sorted(map(str, tools)), "subagent_sessions": children}
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


@pytest.mark.parametrize("agent", ["codex", "claude", "pi", "qwen", "goose"])
def test_live_agent_on_vultr_attempt_in_e2b(agent: str, tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    if agent not in AGENTS:
        pytest.skip(f"{agent} not selected in SDF_LIVE_VULTR_AGENTS")
    factory = BootstrapFactory(agent)
    monkeypatch.setattr(e2b_transport_module, "_PRODUCTION_SANDBOX_FACTORY", factory)
    network = {"allow_out": [INFERENCE_HOST], "deny_out": ["0.0.0.0/0"]}
    secrets = {name: ENV[name] for name in ("E2B_API_KEY", KEY_VAR)}
    environ = {"E2B_API_KEY": ENV["E2B_API_KEY"], KEY_VAR: ENV[KEY_VAR]}
    injection = None
    if agent == "codex":
        environ |= {"SDF_CREDENTIAL_MODE_CODEX": "api-key", "SDF_CODEX_API_KEY_ENV": KEY_VAR,
                    "SDF_OPENAI_BASE_URL": f"https://{INFERENCE_HOST}/v1", "SDF_CODEX_MODEL": MODEL}
    elif agent == "claude":
        environ |= {"SDF_CREDENTIAL_MODE_CLAUDE": "api-key", "SDF_CLAUDE_API_KEY_ENV": KEY_VAR,
                    "SDF_ANTHROPIC_BASE_URL": f"https://{INFERENCE_HOST}", "SDF_CLAUDE_MODEL": MODEL}
    elif agent == "pi":
        # Default base URL and model (Vultr, glm-5.3); the key is named explicitly
        # because the default variable is SDF_PI_API_KEY.
        environ |= {"SDF_CREDENTIAL_MODE_PI": "api-key", "SDF_PI_API_KEY_ENV": KEY_VAR}
    elif agent in ("qwen", "goose"):
        upper = agent.upper()
        environ |= {f"SDF_CREDENTIAL_MODE_{upper}": "api-key", f"SDF_{upper}_API_KEY_ENV": KEY_VAR}
    transport, injection = create_credentialed_transport(
        agents=(agent,), environ=environ, template=BASE_TEMPLATE, timeout_seconds=900, network=network
    )
    headless = agent in HEADLESS
    if headless:
        runtime: Any = HEADLESS[agent](transport=transport, turn_timeout_s=420)
    else:
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
        # Pi's records come from the runtime's own event parsing (below).
        probe_argv = {"egress": ["sh", "-c", EGRESS_PROBE]}
        if not headless:
            probe_argv["records"] = ["python3", "-c", _RECORDS, agent]
        for name, argv in probe_argv.items():
            try:
                probes[name] = transport.exec_argv(argv, 90_000)
            except Exception as exc:  # noqa: BLE001 - diagnostics only
                probes[name] = f"probe failed: {type(exc).__name__}"
        if not headless:
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
    checks = {**CHECKS, **live_project_marker.checks(agent)} if live_project_marker.ENABLED else CHECKS
    db.add(GraphNodeRow(id="ASSUMPTION-CALC", kind="assumption", title="add() is correct", source="test",
                        owner="product", confidence=1.0, created_at=utcnow()))
    db.add(TaskRow(id=task_id, title="fix add", status="created", idempotency_key=f"task-vultr-{agent}",
                   acceptance_criteria=list(checks), created_at=utcnow()))
    db.commit()
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "calc.py").write_text(
        "def add(a, b):\n    return a - b\n\n\ndef sub(a, b):\n    return a - b\n", encoding="utf-8"
    )
    prompt = POSITIVE
    if live_project_marker.ENABLED:
        live_project_marker.write(agent, fixture)
        prompt = live_project_marker.PROMPT
    service = ExecutionService(
        db, workspace_root=tmp_path / "workspaces", artifact_root=tmp_path / "artifacts",
        adapter=RuntimeAgentAdapter(RuntimeController(runtime, source=f"{agent}-vultr-e2b"), agent=agent),
    )
    started = time.monotonic()
    error = None
    attempt = None
    try:
        attempt = service.run(
            task_id=task_id, dispatch_key=f"dispatch-vultr-{agent}", fixture=fixture,
            instructions=(DELEGATE_PREFIX + prompt) if DELEGATE else prompt,
            commands=[], criterion_checks=checks, validation_target=("assumption", "ASSUMPTION-CALC"),
        )
    except Exception as exc:  # noqa: BLE001 - reported below
        error = f"{type(exc).__name__}: {str(exc)[-600:]}"
    finally:
        elapsed = round(time.monotonic() - started, 1)
        sandbox_alive_s = elapsed
        transport.close()
        deadline = time.monotonic() + 30
        # Only this test's own sandboxes count: other runs on the same E2B
        # account (parallel live tests) may start sandboxes meanwhile.
        while ((set(created) | set(factory.created)) & _running_ids()) and time.monotonic() < deadline:
            time.sleep(1)
        leftover = sorted((set(created) | set(factory.created)) & _running_ids())

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
    if headless:
        records = runtime.usage(next(iter(runtime._bindings))) if runtime._bindings else {}
        records["fresh_e2b_claim"] = bool(runtime._bindings) and runtime.permission_skipping(next(iter(runtime._bindings)))
        if runtime._bindings:
            sid = next(iter(runtime._bindings))
            if agent == "pi":
                # ADR-0017: what this write-mode session loaded.
                records["extensions"] = list(runtime.loaded_extensions(sid))
            records["tools_used"] = sorted({m.group(1) for line in runtime.stream(sid)
                                            for m in [re.match(r"\[tool ([^\]:\s]+)", line)] if m})
        tokens_in = records.get("input_tokens", 0) + records.get("cache_read_tokens", 0)
        tokens_out = records.get("output_tokens", 0)
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
        "delegate_prompt": DELEGATE, "project_marker": live_project_marker.ENABLED, "records": records, "tokens_in": tokens_in, "tokens_out": tokens_out,
        "llm_cost_usd_est": round(tokens_in * PRICE_IN + tokens_out * PRICE_OUT, 5),
        "elapsed_s": elapsed, "bootstrap_s": factory.timings.get("bootstrap_s"),
        "e2b_cost_usd_est": round((sandbox_alive_s + 5) * 0.000056, 5),
        "sandboxes_created": len(created), "leftover_sandboxes": len(leftover),
        "egress": probes.get("egress", "").strip().splitlines(), "leaks": leaked,
        "pane_tail": probes.get("pane", "")[-1500:] if not headless else None,
        "stream_tail": list(runtime.stream(next(iter(runtime._bindings))))[-6:] if headless and runtime._bindings else None,
    }
    print(f"SDF_LIVE_VULTR_REPORT {json.dumps(report, default=str)}")
    Path("/tmp/sdf-live-reports").mkdir(exist_ok=True)
    (Path("/tmp/sdf-live-reports") / f"vultr-{agent}.json").write_text(json.dumps(report, default=str, indent=1))
    assert not leaked
    assert not leftover and len(created) == 1
    assert error is None
    assert report["task"] == "succeeded"
    assert report["evidence"] == sorted((name, "PASS") for name in checks)
    assert relations == {"validates"}
    # ADR-0020: no child agent, whether or not the prompt asked for one.
    assert not SUBAGENT_TOOLS & set(records.get("tools_used") or ()), records.get("tools_used")
    assert not records.get("subagent_sessions") and not records.get("subagent_transcripts")
    if headless:
        assert records["requested_models"] == [MODEL]
        assert records["fresh_e2b_claim"] is True
    if agent == "pi":
        from sdf_core.pi_runtime import PI_EXTENSIONS_ENV, resolve_pi_extensions

        assert records["extensions"] == list(resolve_pi_extensions(os.environ.get(PI_EXTENSIONS_ENV)))
    if agent in ("pi", "qwen"):
        # What the endpoint reported serving, not just what was requested.
        assert records["served_models"] == [MODEL]
    elif headless:
        # Goose does not surface the response's model id (caveat, ADR 0013).
        assert records["served_model_reported"] is False
