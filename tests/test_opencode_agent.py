"""OpenCode agent kind and per-agent provider config (ADR-0011), no network."""

from __future__ import annotations

import json
import os
import shutil
import stat
import subprocess
import textwrap
from pathlib import Path
from types import SimpleNamespace

import pytest

from sdf_core import opencode_runtime as ocr
from sdf_core.agent_models import agent_model_args, model_agent_args, resolve_agent_models
from sdf_core.credential_injection import prepare_credential_injection
from sdf_core.credentials import (
    DEFAULT_OPENCODE_BASE_URL,
    CredentialConfigError,
    CredentialMode,
    resolve_credential_plan,
)
from sdf_core.e2b_herdr_transport import E2BHerdrTransport
from sdf_core.opencode_runtime import OpenCodeHeadlessRuntime, OpenCodeRuntimeError, parse_events
from sdf_core.runtime import CredentialMetadata, RuntimeStatus

KEY = "vk-test-0123456789abcdef-secret"
VULTR = {
    "SDF_CREDENTIAL_MODE_OPENCODE": "api-key",
    "SDF_OPENCODE_API_KEY_ENV": "VULTR_INFERENCE_API_KEY",
    "VULTR_INFERENCE_API_KEY": KEY,
}


# --- credential plan ------------------------------------------------------

def test_opencode_default_key_variable_and_vultr_defaults():
    plan = resolve_credential_plan("opencode", {"SDF_CREDENTIAL_MODE_OPENCODE": "api-key", "SDF_OPENCODE_API_KEY": KEY})
    assert plan.mode is CredentialMode.API_KEY
    assert dict(plan.set_variables) == {
        "SDF_OPENCODE_API_KEY": KEY,
        "SDF_OPENCODE_BASE_URL": DEFAULT_OPENCODE_BASE_URL,
        "SDF_OPENCODE_MODEL": "vultr/glm-5.3",
        "SDF_OPENCODE_SMALL_MODEL": "vultr/glm-5.3-flash",
    }
    assert DEFAULT_OPENCODE_BASE_URL == "https://api.vultrinference.com/v1"
    assert [(s.action, s.variable) for s in plan.seed_steps] == [("opencode-config", "SDF_OPENCODE_API_KEY")]
    assert set(plan.strip_variables) >= {
        "OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG_DIR", "OPENCODE_PERMISSION"
    }


def test_opencode_named_key_variable_keeps_its_name_in_the_sandbox():
    plan = resolve_credential_plan("opencode", {**VULTR, "SDF_OPENCODE_API_KEY": "other-default-key-value"})
    assert plan.set_variables["VULTR_INFERENCE_API_KEY"] == KEY
    # The named variable replaces the default; the default is not also injected.
    assert "SDF_OPENCODE_API_KEY" not in plan.set_variables
    assert plan.seed_steps[0].variable == "VULTR_INFERENCE_API_KEY"


def test_named_key_variable_unset_fails_closed_without_fallback():
    env = {**VULTR, "SDF_OPENCODE_API_KEY": "default-key-value-present"}
    del env["VULTR_INFERENCE_API_KEY"]
    with pytest.raises(CredentialConfigError, match="requires VULTR_INFERENCE_API_KEY") as err:
        resolve_credential_plan("opencode", env)
    assert "default-key-value-present" not in str(err.value)


@pytest.mark.parametrize("name", ["PATH", "lower_api_key", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "E2B_API_KEY",
                                  "DAYTONA_API_KEY", "SDF_CONNECTION_CODEX_API_KEY", KEY])
def test_named_key_variable_must_be_a_dedicated_key_name(name):
    env = {**VULTR, "SDF_OPENCODE_API_KEY_ENV": name, name: "value-of-the-named-var"}
    with pytest.raises(CredentialConfigError) as err:
        resolve_credential_plan("opencode", env)
    assert KEY not in str(err.value) and "value-of-the-named-var" not in str(err.value)


def test_opencode_subscription_mode_is_rejected_not_fallen_back():
    with pytest.raises(CredentialConfigError, match="subscription is not supported for opencode"):
        resolve_credential_plan("opencode", {"SDF_CREDENTIAL_MODE_OPENCODE": "subscription", "SDF_OPENCODE_API_KEY": KEY})


def test_opencode_without_mode_fails_closed():
    with pytest.raises(CredentialConfigError, match="SDF_CREDENTIAL_MODE_OPENCODE"):
        resolve_credential_plan("opencode", {"SDF_OPENCODE_API_KEY": KEY})


@pytest.mark.parametrize(("agent", "knob", "target"), [
    ("codex", "SDF_CODEX_API_KEY_ENV", "OPENAI_API_KEY"),
    ("claude", "SDF_CLAUDE_API_KEY_ENV", "ANTHROPIC_API_KEY"),
])
def test_codex_and_claude_key_can_come_from_a_named_variable(agent, knob, target):
    env = {f"SDF_CREDENTIAL_MODE_{agent.upper()}": "api-key", knob: "GATEWAY_API_KEY", "GATEWAY_API_KEY": KEY}
    plan = resolve_credential_plan(agent, env)
    # The agent still reads its provider's fixed variable inside the box.
    assert plan.set_variables[target] == KEY
    assert "GATEWAY_API_KEY" not in plan.set_variables


def test_named_key_variable_is_an_error_in_subscription_mode():
    env = {"SDF_CREDENTIAL_MODE_CODEX": "subscription", "SDF_CONNECTION_CODEX": "c1", "SDF_CODEX_API_KEY_ENV": "X_API_KEY"}
    with pytest.raises(CredentialConfigError, match="SDF_CODEX_API_KEY_ENV is only allowed"):
        resolve_credential_plan("codex", env, read_connection=lambda *_: pytest.fail("must not read"))


def test_opencode_custom_provider_base_url_and_models():
    env = {**VULTR, "SDF_OPENCODE_BASE_URL": "https://llm.example.com/v1",
           "SDF_OPENCODE_MODEL": "gw/big-1", "SDF_OPENCODE_SMALL_MODEL": "gw/small-1"}
    plan = resolve_credential_plan("opencode", env)
    assert plan.set_variables["SDF_OPENCODE_BASE_URL"] == "https://llm.example.com/v1"
    assert plan.set_variables["SDF_OPENCODE_MODEL"] == "gw/big-1"
    assert plan.set_variables["SDF_OPENCODE_SMALL_MODEL"] == "gw/small-1"


@pytest.mark.parametrize("url", ["http://api.vultrinference.com/v1", "https://127.0.0.1/v1", "https://localhost/v1"])
def test_opencode_base_url_follows_adr_0007_rules(url):
    with pytest.raises(CredentialConfigError):
        resolve_credential_plan("opencode", {**VULTR, "SDF_OPENCODE_BASE_URL": url})


@pytest.mark.parametrize(("model", "small", "match"), [
    ("glm-5.3", None, "SDF_OPENCODE_MODEL must be"),
    ("vultr/glm-5.3", "other/glm-5.3-flash", "same provider"),
    ("vultr/glm 5.3", None, "not a valid model"),
])
def test_opencode_model_validation(model, small, match):
    env = {**VULTR, "SDF_OPENCODE_MODEL": model, **({"SDF_OPENCODE_SMALL_MODEL": small} if small else {})}
    with pytest.raises(CredentialConfigError, match=match):
        resolve_credential_plan("opencode", env)


def test_injection_strips_conflicting_opencode_config_and_guards_values():
    injection = prepare_credential_injection(
        ["opencode"], VULTR, base_envs={"OPENCODE_CONFIG": "/tmp/other.json", "KEEP": "1"}
    )
    assert "OPENCODE_CONFIG" not in injection.envs and injection.envs["KEEP"] == "1"
    assert all(KEY not in command for command in injection.seed_commands)
    assert KEY not in repr(injection)
    assert "VULTR_INFERENCE_API_KEY" in injection.credential_variables


@pytest.mark.skipif(shutil.which("node") is None, reason="node is required to execute the seed script")
def test_opencode_config_seed_references_the_key_by_name_only(tmp_path):
    injection = prepare_credential_injection(["opencode"], {**VULTR, "SDF_OPENCODE_MODEL": "vultr/glm-5.3"})
    env = {"HOME": str(tmp_path), "PATH": os.environ["PATH"], **injection.envs}
    for command in injection.seed_commands:
        subprocess.run(["bash", "-c", command], env=env, check=True)
    path = tmp_path / ".config" / "opencode" / "opencode.json"
    text = path.read_text()
    assert KEY not in text
    assert stat.S_IMODE(path.stat().st_mode) == 0o600
    config = json.loads(text)
    assert config["model"] == "vultr/glm-5.3" and config["small_model"] == "vultr/glm-5.3-flash"
    assert config["autoupdate"] is False and config["share"] == "disabled"
    # Headless runs must not act without the ADR-0008 claim.
    assert config["permission"] == {"edit": "ask", "bash": "ask", "webfetch": "deny"}
    provider = config["provider"]["vultr"]
    assert provider["npm"] == "@ai-sdk/openai-compatible"
    assert provider["options"] == {"baseURL": DEFAULT_OPENCODE_BASE_URL, "apiKey": "{env:VULTR_INFERENCE_API_KEY}"}
    assert set(provider["models"]) == {"glm-5.3", "glm-5.3-flash"}


@pytest.mark.skipif(shutil.which("node") is None, reason="node is required to execute the seed script")
def test_opencode_config_seed_uses_custom_provider_id(tmp_path):
    env_in = {**VULTR, "SDF_OPENCODE_BASE_URL": "https://llm.example.com/v1",
              "SDF_OPENCODE_MODEL": "gw/big-1", "SDF_OPENCODE_SMALL_MODEL": "gw/small-1"}
    injection = prepare_credential_injection(["opencode"], env_in)
    env = {"HOME": str(tmp_path), "PATH": os.environ["PATH"], **injection.envs}
    subprocess.run(["bash", "-c", injection.seed_commands[0]], env=env, check=True)
    config = json.loads((tmp_path / ".config" / "opencode" / "opencode.json").read_text())
    assert set(config["provider"]) == {"gw"}
    assert config["provider"]["gw"]["options"]["baseURL"] == "https://llm.example.com/v1"


# --- model selection ----------------------------------------------------------

def test_model_overrides_per_agent_kind():
    env = {"SDF_CLAUDE_MODEL": "claude-sonnet-x", "SDF_CODEX_MODEL": "gpt-x"}
    assert agent_model_args("claude", env) == ("--model", "claude-sonnet-x")
    assert agent_model_args("codex", env) == ("-m", "gpt-x")
    assert agent_model_args("codex", {}) == ()
    assert agent_model_args("unknown-kind", env) == ()
    assert agent_model_args("opencode", {}) == ("--model", "vultr/glm-5.3")
    assert model_agent_args(["claude", "codex", "unknown-kind"], {"SDF_CODEX_MODEL": "gpt-x"}) == {"codex": ("-m", "gpt-x")}
    assert resolve_agent_models("opencode", {}).provider_id == "vultr"


# --- transport ----------------------------------------------------------------

class _Sandbox:
    sandbox_id = "sbx-1"

    def __init__(self):
        self.commands = SimpleNamespace(run=self._run)
        self.ran: list[str] = []

    def _run(self, cmd, **_):
        self.ran.append(cmd)
        return SimpleNamespace(stdout="ok\n")


def test_e2b_transport_passes_network_only_when_configured():
    seen: list[dict] = []
    box = _Sandbox()

    def factory(**kwargs):
        seen.append(kwargs)
        return box

    network = {"allow_out": ["api.vultrinference.com"], "deny_out": ["0.0.0.0/0"]}
    t = E2BHerdrTransport(template="opencode", environ={"E2B_API_KEY": "e2b-x"}, sandbox_factory=factory, network=network)
    assert t.exec_argv(["opencode", "--version"], 1000) == "ok\n"
    assert seen[0]["network"] == network and box.ran == ["opencode --version"]
    plain = E2BHerdrTransport(template="opencode", environ={"E2B_API_KEY": "e2b-x"}, sandbox_factory=factory)
    plain.exec_argv(["true"], 1000)
    assert "network" not in seen[1]
    with pytest.raises(ValueError):
        plain.exec_argv([], 1000)


# --- headless runtime -------------------------------------------------------------

FAKE_OPENCODE = textwrap.dedent('''\
    #!/usr/bin/env python3
    import json, os, sys
    args = sys.argv[1:]
    if args == ["--version"]:
        print(os.environ.get("FAKE_OC_VERSION", "1.19.4")); sys.exit(0)
    with open(os.environ["FAKE_OC_LOG"], "a") as f:
        env = {k: os.environ.get(k) for k in ("OPENCODE_DISABLE_PROJECT_CONFIG", "OPENCODE_CONFIG",
               "OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG_DIR", "OPENCODE_PERMISSION")}
        f.write(json.dumps({"cwd": os.getcwd(), "args": args, "env": env}) + "\\n")
    mode = os.environ.get("FAKE_OC_MODE", "ok")
    if mode == "fail":
        sys.stderr.write("provider exploded\\n"); sys.exit(3)
    if mode == "sleep":
        import time; time.sleep(30)
    open("calc.py", "w").write("def add(a, b):\\n    return a + b\\n")
    sid = "ses_fake1"
    for event in (
        {"type": "step_start", "sessionID": sid, "part": {}},
        {"type": "tool_use", "sessionID": sid, "part": {"tool": "edit", "state": {"status": "completed"}}},
        {"type": "step_finish", "sessionID": sid, "part": {"tokens": {"total": 100, "input": 80, "output": 20,
         "reasoning": 5, "cache": {"read": 10, "write": 0}}, "cost": 0.0012}},
        {"type": "text", "sessionID": sid, "part": {"text": "DONE"}},
        {"type": "step_finish", "sessionID": sid, "part": {"tokens": {"total": 50, "input": 40, "output": 10,
         "reasoning": 0, "cache": {"read": 0, "write": 0}}, "cost": 0.0003}},
    ):
        print(json.dumps(event))
''')


class LocalTransport:
    """Runs argv locally; stages workspaces as plain directory copies."""

    def __init__(self, root: Path, env: dict[str, str]):
        self.root, self.env = root, env
        self.calls: list[list[str]] = []
        self.closed = 0
        self.credential_metadata = {"opencode": CredentialMetadata("api-key")}

    def exec_argv(self, argv, timeout_ms):
        self.calls.append(list(argv))
        if "pkill" in argv[-1]:
            return ""  # never signal real processes on the test host
        result = subprocess.run(list(argv), env=self.env, capture_output=True, text=True, timeout=timeout_ms / 1000)
        if result.returncode != 0:
            raise ocr.HerdrRuntimeError(result.stderr[-1000:])
        return result.stdout

    def stage_workspace(self, attempt_id, workspace):
        target = self.root / "remote" / attempt_id
        shutil.copytree(workspace, target)
        return str(target)

    def collect_workspace(self, attempt_id, workspace):
        shutil.copytree(self.root / "remote" / attempt_id, workspace, dirs_exist_ok=True)

    def close(self, timeout_ms=30_000):
        self.closed += 1


@pytest.fixture
def local(tmp_path):
    bindir = tmp_path / "bin"
    bindir.mkdir()
    fake = bindir / "opencode"
    fake.write_text(FAKE_OPENCODE)
    fake.chmod(0o755)
    env = {"PATH": f"{bindir}:{os.environ['PATH']}", "FAKE_OC_LOG": str(tmp_path / "calls.jsonl"), "HOME": str(tmp_path)}
    workspace = tmp_path / "ws"
    workspace.mkdir()
    (workspace / "calc.py").write_text("def add(a, b):\n    return a - b\n")
    return LocalTransport(tmp_path, env), workspace, tmp_path / "calls.jsonl"


def _calls(log: Path) -> list[dict]:
    return [json.loads(line) for line in log.read_text().splitlines()]


def test_headless_turn_runs_in_staged_workspace_and_reports_usage(local):
    transport, workspace, log = local
    runtime = OpenCodeHeadlessRuntime(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-1", workspace)
    session = runtime.start(attempt_id="ATTEMPT-1", agent="opencode")
    assert session.status is RuntimeStatus.CREATED and session.credential == CredentialMetadata("api-key")
    # Not a production E2B transport: no permission skipping.
    assert runtime.permission_skipping(session.session_id) is False
    done = runtime.send(session.session_id, "-starts with a dash; fix add")
    assert done.status is RuntimeStatus.COMPLETED
    assert runtime.stream(session.session_id) == ("[tool edit: completed]", "DONE")
    call = _calls(log)[0]
    assert call["cwd"] == str(transport.root / "remote" / "ATTEMPT-1")
    assert call["args"] == ["run", "--format", "json", "--", "-starts with a dash; fix add"]
    assert runtime.usage(session.session_id) == {
        "steps": 2, "total_tokens": 150, "input_tokens": 120, "output_tokens": 30,
        "reasoning_tokens": 5, "cache_read_tokens": 10, "cost": 0.0015,
    }
    # A second turn continues the same OpenCode session.
    runtime.send(session.session_id, "again")
    assert _calls(log)[1]["args"][-4:] == ["--session", "ses_fake1", "--", "again"]
    runtime.collect_workspace("ATTEMPT-1", workspace)
    assert "a + b" in (workspace / "calc.py").read_text()
    assert runtime.terminate(session.session_id).status is RuntimeStatus.TERMINATED
    runtime.terminate(session.session_id)
    assert transport.closed == 1


def test_turn_ignores_workspace_config_and_config_override_variables(local):
    # The Attempt workspace is untrusted: its opencode.json / .opencode/ could
    # allow bash/edit, redirect the key's baseURL or load plugins (ADR-0011).
    transport, workspace, log = local
    (workspace / "opencode.json").write_text('{"permission": {"bash": "allow", "edit": "allow"}}')
    transport.env.update({
        "OPENCODE_PERMISSION": '{"bash": "allow"}', "OPENCODE_CONFIG": "/tmp/x.json",
        "OPENCODE_CONFIG_CONTENT": "{}", "OPENCODE_CONFIG_DIR": "/tmp/x",
    })
    runtime = OpenCodeHeadlessRuntime(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-7", workspace)
    session = runtime.start(attempt_id="ATTEMPT-7", agent="opencode")
    runtime.send(session.session_id, "fix")
    assert _calls(log)[0]["env"] == {
        "OPENCODE_DISABLE_PROJECT_CONFIG": "1", "OPENCODE_CONFIG": None, "OPENCODE_CONFIG_CONTENT": None,
        "OPENCODE_CONFIG_DIR": None, "OPENCODE_PERMISSION": None,
    }


def test_auto_is_passed_only_with_the_fresh_e2b_claim(local, monkeypatch):
    transport, workspace, log = local
    monkeypatch.setattr(ocr, "_claim_fresh_e2b_permission_bypass", lambda t: t is transport)
    runtime = OpenCodeHeadlessRuntime(transport=transport, turn_timeout_s=20, model="gw/big-1")
    runtime.bind_workspace("ATTEMPT-2", workspace)
    session = runtime.start(attempt_id="ATTEMPT-2", agent="opencode")
    assert runtime.permission_skipping(session.session_id) is True
    runtime.send(session.session_id, "fix")
    assert _calls(log)[0]["args"] == ["run", "--format", "json", "--model", "gw/big-1", "--auto", "--", "fix"]


def test_real_claim_is_refused_for_non_production_transports(local):
    transport, *_ = local
    assert ocr._claim_fresh_e2b_permission_bypass(transport) is False
    injected = E2BHerdrTransport(template="opencode", environ={"E2B_API_KEY": "x"}, sandbox_factory=lambda **_: _Sandbox())
    assert ocr._claim_fresh_e2b_permission_bypass(injected) is False


def test_version_pin_is_enforced(local):
    transport, workspace, _ = local
    transport.env["FAKE_OC_VERSION"] = "2.0.0"
    runtime = OpenCodeHeadlessRuntime(transport=transport)
    runtime.bind_workspace("ATTEMPT-3", workspace)
    with pytest.raises(OpenCodeRuntimeError, match="OpenCode 2.0.0 is installed but SDF is pinned to 1.19.4"):
        runtime.start(attempt_id="ATTEMPT-3", agent="opencode")


def test_start_requires_opencode_kind_credentials_and_staged_workspace(local):
    transport, workspace, _ = local
    runtime = OpenCodeHeadlessRuntime(transport=transport)
    with pytest.raises(OpenCodeRuntimeError, match="cannot start agent kind 'codex'"):
        runtime.start(attempt_id="A", agent="codex")
    with pytest.raises(OpenCodeRuntimeError, match="bind_workspace"):
        runtime.start(attempt_id="A", agent="opencode")
    transport.credential_metadata = {"codex": CredentialMetadata("api-key")}
    runtime = OpenCodeHeadlessRuntime(transport=transport)
    with pytest.raises(OpenCodeRuntimeError, match="SDF_CREDENTIAL_MODE_OPENCODE"):
        runtime.start(attempt_id="A", agent="opencode")


def test_failed_turn_raises_with_stderr_tail(local):
    transport, workspace, _ = local
    transport.env["FAKE_OC_MODE"] = "fail"
    runtime = OpenCodeHeadlessRuntime(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-4", workspace)
    session = runtime.start(attempt_id="ATTEMPT-4", agent="opencode")
    with pytest.raises(OpenCodeRuntimeError, match=r"exit 3\): provider exploded"):
        runtime.send(session.session_id, "fix")


@pytest.mark.skipif(shutil.which("timeout") is None, reason="coreutils timeout is required")
def test_turn_timeout_is_enforced_inside_the_sandbox(local, monkeypatch):
    monkeypatch.setattr(ocr, "_KILL_AFTER_S", 1)
    transport, workspace, _ = local
    transport.env["FAKE_OC_MODE"] = "sleep"
    runtime = OpenCodeHeadlessRuntime(transport=transport, turn_timeout_s=1)
    runtime.bind_workspace("ATTEMPT-5", workspace)
    session = runtime.start(attempt_id="ATTEMPT-5", agent="opencode")
    with pytest.raises(OpenCodeRuntimeError, match="exceeded 1s"):
        runtime.send(session.session_id, "fix")
    assert runtime.status(session.session_id).status is RuntimeStatus.TERMINATED


def test_cancel_closes_the_environment(local):
    transport, workspace, _ = local
    runtime = OpenCodeHeadlessRuntime(transport=transport)
    runtime.bind_workspace("ATTEMPT-6", workspace)
    session = runtime.start(attempt_id="ATTEMPT-6", agent="opencode")
    assert runtime.cancel(session.session_id).status is RuntimeStatus.CANCELLED
    assert transport.calls[-1] == ["sh", "-c", "pkill -f '[o]pencode run' || true"]
    assert transport.closed == 1
    assert runtime.send(session.session_id, "x").status is RuntimeStatus.CANCELLED
    with pytest.raises(ocr.HerdrUnsupportedOperation):
        runtime.reconnect(session.session_id)


def test_parse_events_reports_errors_and_keeps_raw_lines():
    raw = "\n".join([
        "not json",
        json.dumps({"type": "error", "sessionID": "s1", "error": {"name": "APIError", "data": {"message": "401 Unauthorized"}}}),
    ])
    lines, session, usage, errors = parse_events(raw)
    assert lines == ("not json", "[error 401 Unauthorized]") and session == "s1"
    assert errors == ("401 Unauthorized",) and usage.steps == 0


def test_runtime_drives_the_sdf_attempt_loop(local, tmp_path):
    """Task -> Attempt -> evaluator Evidence through the public ExecutionService."""

    from sqlalchemy.orm import sessionmaker

    from sdf_core.adapter import RuntimeAgentAdapter
    from sdf_core.db import Base, DecisionEdgeRow, EvidenceRow, GraphNodeRow, TaskRow, make_engine
    from sdf_core.execution import ExecutionService
    from sdf_core.model import utcnow
    from sdf_core.runtime import RuntimeController

    transport, workspace, _ = local
    engine = make_engine()
    Base.metadata.create_all(engine)
    db = sessionmaker(engine, expire_on_commit=False)()
    db.add(GraphNodeRow(id="ASSUMPTION-CALC", kind="assumption", title="add is correct", source="test",
                        owner="product", confidence=1.0, created_at=utcnow()))
    db.add(TaskRow(id="TASK-OC", title="fix add", status="created", idempotency_key="task-oc",
                   acceptance_criteria=["add returns the sum"], created_at=utcnow()))
    db.commit()
    runtime = OpenCodeHeadlessRuntime(transport=transport, turn_timeout_s=20)
    service = ExecutionService(
        db, workspace_root=tmp_path / "workspaces", artifact_root=tmp_path / "artifacts",
        adapter=RuntimeAgentAdapter(RuntimeController(runtime, source="opencode"), agent="opencode"),
    )
    attempt = service.run(
        task_id="TASK-OC", dispatch_key="dispatch-oc", fixture=workspace, instructions="Fix add.",
        commands=[], criterion_checks={"add returns the sum": [["python3", "-c", "from calc import add; assert add(2, 3) == 5"]]},
        validation_target=("assumption", "ASSUMPTION-CALC"),
    )
    evidence = db.query(EvidenceRow).filter_by(attempt_id=attempt.id).all()
    assert [e.status for e in evidence] == ["PASS"]
    assert db.get(TaskRow, "TASK-OC").status == "succeeded"
    assert {e.relation for e in db.query(DecisionEdgeRow).filter_by(target_id="ASSUMPTION-CALC")} == {"validates"}
    assert transport.closed == 1


def test_model_override_reaches_the_herdr_agent_start_argv(tmp_path):
    from sdf_core.herdr_runtime import HerdrRuntime
    from tests.test_herdr_e2b_execution import _InteractiveHerdr

    seen: list[list[str]] = []
    fake = _InteractiveHerdr("codex", "")

    def runner(command, timeout_ms):
        seen.append(list(command))
        return fake(command, timeout_ms)

    runtime = HerdrRuntime(runner=runner, agent_args=model_agent_args(["codex"], {"SDF_CODEX_MODEL": "gpt-x"}))
    runtime.bind_workspace("ATTEMPT-M", tmp_path)
    runtime.start(attempt_id="ATTEMPT-M", agent="codex")
    start = next(c for c in seen if c[1:3] == ["agent", "start"])
    assert start[-2:] == ["-m", "gpt-x"] and "--" in start
