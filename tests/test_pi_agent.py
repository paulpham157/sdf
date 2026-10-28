"""Pi agent kind (ADR-0012), no network."""

from __future__ import annotations

import json
import os
import shutil
import stat
import subprocess
import textwrap
from pathlib import Path

import pytest

from sdf_core import pi_runtime as pir
from sdf_core.agent_models import agent_model_args
from sdf_core.credential_injection import prepare_credential_injection
from sdf_core.credentials import (
    AGENT_KINDS,
    DEFAULT_PI_BASE_URL,
    CredentialConfigError,
    CredentialMode,
    resolve_credential_plan,
)
from sdf_core.e2b_herdr_transport import E2BHerdrTransport
from sdf_core.pi_runtime import PiHeadlessRuntime, PiRuntimeError, parse_events
from sdf_core.runtime import CredentialMetadata, RuntimeStatus

KEY = "vk-test-0123456789abcdef-secret"
PI = {"SDF_CREDENTIAL_MODE_PI": "api-key", "SDF_PI_API_KEY": KEY}


# --- credential plan ------------------------------------------------------

def test_pi_is_an_agent_kind_with_vultr_defaults():
    assert "pi" in AGENT_KINDS
    plan = resolve_credential_plan("pi", PI)
    assert plan.mode is CredentialMode.API_KEY
    assert dict(plan.set_variables) == {
        "SDF_PI_API_KEY": KEY,
        "SDF_PI_BASE_URL": "https://api.vultrinference.com/v1",
        "SDF_PI_MODEL": "glm-5.3",
    }
    assert DEFAULT_PI_BASE_URL == "https://api.vultrinference.com/v1"
    assert [(s.action, s.variable) for s in plan.seed_steps] == [("pi-models", "SDF_PI_API_KEY")]
    assert set(plan.strip_variables) >= {
        "PI_CODING_AGENT_DIR", "PI_CODING_AGENT_SESSION_DIR", "PI_PACKAGE_DIR",
        "PI_SHARE_VIEWER_URL", "PI_OAUTH_CALLBACK_HOST",
    }


def test_pi_named_key_variable_replaces_the_default():
    env = {**PI, "SDF_PI_API_KEY_ENV": "GATEWAY_API_KEY", "GATEWAY_API_KEY": "gw-key-value"}
    plan = resolve_credential_plan("pi", env)
    assert plan.set_variables["GATEWAY_API_KEY"] == "gw-key-value"
    assert "SDF_PI_API_KEY" not in plan.set_variables
    assert plan.seed_steps[0].variable == "GATEWAY_API_KEY"


def test_pi_named_key_unset_fails_closed_without_fallback():
    env = {**PI, "SDF_PI_API_KEY_ENV": "GATEWAY_API_KEY"}
    with pytest.raises(CredentialConfigError, match="requires GATEWAY_API_KEY") as err:
        resolve_credential_plan("pi", env)
    assert KEY not in str(err.value)


def test_pi_default_key_follows_the_sdf_convention_and_ignores_the_vultr_variable():
    """SDF_PI_API_KEY like SDF_OPENCODE_API_KEY; a bare VULTR_INFERENCE_API_KEY
    is only read when SDF_PI_API_KEY_ENV names it (ADR-0007, no fallback)."""

    with pytest.raises(CredentialConfigError, match="SDF_PI_API_KEY") as err:
        resolve_credential_plan("pi", {"SDF_CREDENTIAL_MODE_PI": "api-key", "VULTR_INFERENCE_API_KEY": KEY})
    assert KEY not in str(err.value)
    plan = resolve_credential_plan("pi", {
        "SDF_CREDENTIAL_MODE_PI": "api-key", "SDF_PI_API_KEY_ENV": "VULTR_INFERENCE_API_KEY",
        "VULTR_INFERENCE_API_KEY": KEY,
    })
    assert dict(plan.set_variables)["VULTR_INFERENCE_API_KEY"] == KEY
    assert "SDF_PI_API_KEY" not in plan.set_variables


def test_pi_default_key_unset_fails_closed():
    with pytest.raises(CredentialConfigError, match="SDF_PI_API_KEY"):
        resolve_credential_plan("pi", {"SDF_CREDENTIAL_MODE_PI": "api-key"})


@pytest.mark.parametrize("name", ["PATH", "lower_api_key", "OPENAI_API_KEY", "E2B_API_KEY", "DAYTONA_API_KEY",
                                  "SDF_CONNECTION_PI_API_KEY", "PI_API_KEY", KEY])
def test_pi_named_key_variable_must_be_a_dedicated_key_name(name):
    env = {**PI, "SDF_PI_API_KEY_ENV": name, name: "value-of-the-named-var"}
    with pytest.raises(CredentialConfigError) as err:
        resolve_credential_plan("pi", env)
    assert KEY not in str(err.value) and "value-of-the-named-var" not in str(err.value)


def test_pi_subscription_mode_is_rejected_and_mode_is_required():
    with pytest.raises(CredentialConfigError, match="subscription is not supported for pi"):
        resolve_credential_plan("pi", {**PI, "SDF_CREDENTIAL_MODE_PI": "subscription"})
    with pytest.raises(CredentialConfigError, match="SDF_CREDENTIAL_MODE_PI"):
        resolve_credential_plan("pi", {"SDF_PI_API_KEY": KEY})


def test_pi_custom_base_url_and_model():
    plan = resolve_credential_plan("pi", {**PI, "SDF_PI_BASE_URL": "https://llm.example.com/v1", "SDF_PI_MODEL": "org/big-1"})
    assert plan.set_variables["SDF_PI_BASE_URL"] == "https://llm.example.com/v1"
    assert plan.set_variables["SDF_PI_MODEL"] == "org/big-1"
    assert agent_model_args("pi", {}) == ("--model", "glm-5.3")


@pytest.mark.parametrize("url", ["http://api.vultrinference.com/v1", "https://127.0.0.1/v1", "https://localhost/v1"])
def test_pi_base_url_follows_adr_0007_rules(url):
    with pytest.raises(CredentialConfigError):
        resolve_credential_plan("pi", {**PI, "SDF_PI_BASE_URL": url})


@pytest.mark.parametrize("model", ["glm-5.3:high", "glm 5.3", "-x"])
def test_pi_model_must_be_a_bare_id(model):
    with pytest.raises(CredentialConfigError):
        resolve_credential_plan("pi", {**PI, "SDF_PI_MODEL": model})


def test_injection_strips_pi_variables_and_never_puts_the_key_in_commands():
    injection = prepare_credential_injection(
        ["pi"], PI, base_envs={"PI_CODING_AGENT_DIR": "/tmp/evil", "KEEP": "1"}
    )
    assert "PI_CODING_AGENT_DIR" not in injection.envs and injection.envs["KEEP"] == "1"
    assert all(KEY not in command for command in injection.seed_commands)
    assert KEY not in repr(injection)


@pytest.mark.skipif(shutil.which("node") is None, reason="node is required to execute the seed script")
def test_pi_models_seed_references_the_key_by_name_only(tmp_path):
    injection = prepare_credential_injection(["pi"], PI)
    env = {"HOME": str(tmp_path), "PATH": os.environ["PATH"], **injection.envs}
    for command in injection.seed_commands:
        subprocess.run(["bash", "-c", command], env=env, check=True)
    path = tmp_path / ".pi" / "agent" / "models.json"
    text = path.read_text()
    assert KEY not in text
    assert stat.S_IMODE(path.stat().st_mode) == 0o600
    assert stat.S_IMODE(path.parent.stat().st_mode) == 0o700
    provider = json.loads(text)["providers"]["sdf"]
    assert provider["baseUrl"] == DEFAULT_PI_BASE_URL
    assert provider["api"] == "openai-completions"
    # A NAME, not a `!command` and not the value.
    # Pi 1.x: ``$NAME`` reads the variable (a bare name would be the key itself).
    assert provider["apiKey"] == "$SDF_PI_API_KEY"
    [model] = provider["models"]
    assert model["id"] == "glm-5.3"
    assert model["cost"] == {"input": 0.75, "output": 3, "cacheRead": 0, "cacheWrite": 0}


@pytest.mark.skipif(shutil.which("node") is None, reason="node is required to execute the seed script")
def test_pi_models_seed_custom_endpoint_reports_zero_price(tmp_path):
    injection = prepare_credential_injection(
        ["pi"], {**PI, "SDF_PI_BASE_URL": "https://llm.example.com/v1", "SDF_PI_MODEL": "big-1"}
    )
    env = {"HOME": str(tmp_path), "PATH": os.environ["PATH"], **injection.envs}
    subprocess.run(["bash", "-c", injection.seed_commands[0]], env=env, check=True)
    provider = json.loads((tmp_path / ".pi" / "agent" / "models.json").read_text())["providers"]["sdf"]
    assert provider["baseUrl"] == "https://llm.example.com/v1"
    assert provider["models"][0]["id"] == "big-1"
    assert provider["models"][0]["cost"]["input"] == 0


# --- headless runtime -------------------------------------------------------------

FAKE_PI = textwrap.dedent('''\
    #!/usr/bin/env python3
    import json, os, sys
    args = sys.argv[1:]
    if args == ["--version"]:
        # Pi 1.x prints its version on stdout; PI_PACKAGE_DIR would change it.
        v = "0.0.0" if "PI_PACKAGE_DIR" in os.environ else os.environ.get("FAKE_PI_VERSION", "1.4.0")
        print(v); sys.exit(0)
    prompt = sys.stdin.read()
    with open(os.environ["FAKE_PI_LOG"], "a") as f:
        env = {k: v for k, v in os.environ.items() if k.startswith(("PI_", "PILENS_"))}
        f.write(json.dumps({"cwd": os.getcwd(), "args": args, "prompt": prompt, "env": env}) + "\\n")
    mode = os.environ.get("FAKE_PI_MODE", "ok")
    if mode == "fail":
        sys.stderr.write("provider exploded\\n"); sys.exit(3)
    if mode == "sleep":
        import time; time.sleep(30)
    if mode == "mkpi":
        os.mkdir(".pi")
    if "--tools" not in args:
        open("calc.py", "w").write("def add(a, b):\\n    return a + b\\n")
    usage = {"input": 80, "output": 20, "cacheRead": 10, "cacheWrite": 0, "totalTokens": 110,
             "cost": {"total": 0.0012}}
    model = args[args.index("--model") + 1]
    events = [
        {"type": "message_update", "message": {"role": "assistant"}},
        {"type": "message_end", "message": {"role": "user", "content": [{"type": "text", "text": prompt}]}},
        {"type": "tool_execution_end", "toolName": "edit", "isError": False},
        {"type": "message_end", "message": {"role": "assistant", "model": model, "usage": usage,
         "stopReason": "stop", "content": [{"type": "text", "text": "DONE"}]}},
    ]
    if mode == "error":
        events.append({"type": "message_end", "message": {"role": "assistant", "model": model,
                       "stopReason": "error", "errorMessage": "401 Unauthorized", "content": []}})
    for event in events:
        print(json.dumps(event, separators=(",", ":")))
''')


class LocalTransport:
    """Runs argv locally; stages workspaces as plain directory copies."""

    def __init__(self, root: Path, env: dict[str, str]):
        self.root, self.env = root, env
        self.calls: list[list[str]] = []
        self.closed = 0
        self.credential_metadata = {"pi": CredentialMetadata("api-key")}

    def exec_argv(self, argv, timeout_ms):
        self.calls.append(list(argv))
        if "pkill" in argv[-1]:
            return ""  # never signal real processes on the test host
        result = subprocess.run(list(argv), env=self.env, capture_output=True, text=True, timeout=timeout_ms / 1000)
        if result.returncode != 0:
            raise pir.HerdrRuntimeError(result.stderr[-1000:])
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
def local(tmp_path, monkeypatch):
    bindir = tmp_path / "bin"
    bindir.mkdir()
    fake = bindir / "pi"
    fake.write_text(FAKE_PI)
    fake.chmod(0o755)
    env = {"PATH": f"{bindir}:{os.environ['PATH']}", "FAKE_PI_LOG": str(tmp_path / "calls.jsonl"),
           "HOME": str(tmp_path), "SDF_PI_MODEL": "glm-5.3"}
    workspace = tmp_path / "ws"
    workspace.mkdir()
    (workspace / "calc.py").write_text("def add(a, b):\n    return a - b\n")
    # Keep per-attempt session dirs inside tmp_path.
    real = PiHeadlessRuntime.command

    def command(self, session_id, input_text):
        argv = real(self, session_id, input_text)
        return [a.replace("/tmp/sdf-pi-", f"{tmp_path}/sdf-pi-") for a in argv[:4]] + [
            a.replace("/tmp/sdf-pi-", f"{tmp_path}/sdf-pi-") if i not in (6,) else a
            for i, a in enumerate(argv[4:])
        ]

    monkeypatch.setattr(PiHeadlessRuntime, "command", command)
    # The image-baked extensions (ADR-0017), at their pins.
    monkeypatch.delenv("SDF_PI_EXTENSIONS", raising=False)
    monkeypatch.setattr(pir, "PI_EXTENSIONS_DIR", str(_bake_extensions(tmp_path / "ext")))
    return LocalTransport(tmp_path, env), workspace, tmp_path / "calls.jsonl"


def _bake_extensions(root: Path, **versions: str) -> Path:
    for name, ext in pir.PI_EXTENSIONS.items():
        package = root / "node_modules" / name
        package.mkdir(parents=True)
        version = versions.get(name, ext.version)
        (package / "package.json").write_text(json.dumps({"name": name, "version": version}, indent=2))
    return root


def _calls(log: Path) -> list[dict]:
    return [json.loads(line) for line in log.read_text().splitlines()]


def _pi_args(argv: list[str]) -> list[str]:
    return argv[argv.index("pi") + 1:] if "pi" in argv else argv


def test_read_only_turn_uses_stdin_prompt_and_reports_usage(local, tmp_path):
    transport, workspace, log = local
    runtime = PiHeadlessRuntime(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-1", workspace)
    session = runtime.start(attempt_id="ATTEMPT-1", agent="pi")
    assert session.credential == CredentialMetadata("api-key")
    assert runtime.permission_skipping(session.session_id) is False
    done = runtime.send(session.session_id, "-x @calc.py starts with a dash")
    assert done.status is RuntimeStatus.COMPLETED
    assert runtime.stream(session.session_id) == ("[tool edit: ok]", "DONE")
    call = _calls(log)[0]
    assert call["cwd"] == str(tmp_path / "remote" / "ATTEMPT-1")
    # The prompt never reaches argv, so `-`/`@` cannot become flags or files.
    assert call["prompt"] == "-x @calc.py starts with a dash"
    assert "-x @calc.py starts with a dash" not in call["args"]
    assert call["args"] == [
        "-p", "--mode", "json", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes",
        "--no-mcp", "--no-approve",
        # Read-only: only the guardrail extension (ADR-0017).
        "-e", f"{tmp_path}/ext/node_modules/@cad0p/pi-steering/src/index.ts",
        "--session-dir", f"{tmp_path}/sdf-pi-ATTEMPT-1/sessions", "--tools", "read,grep,find,ls",
        "--provider", "sdf", "--model", "glm-5.3",
    ]
    assert runtime.usage(session.session_id) == {
        "messages": 1, "input_tokens": 80, "output_tokens": 20, "cache_read_tokens": 10, "cache_write_tokens": 0,
        "total_tokens": 110, "cost": 0.0012, "requested_models": ["glm-5.3"], "served_models": ["glm-5.3"],
    }
    # Read-only: the fake only edits when write tools are available.
    runtime.collect_workspace("ATTEMPT-1", workspace)
    assert "a - b" in (workspace / "calc.py").read_text()
    runtime.send(session.session_id, "again")
    assert "--continue" in _calls(log)[1]["args"]
    assert runtime.usage(session.session_id)["messages"] == 2
    assert runtime.terminate(session.session_id).status is RuntimeStatus.TERMINATED
    runtime.terminate(session.session_id)
    assert transport.closed == 1


def test_write_tools_only_with_the_fresh_e2b_claim(local, monkeypatch):
    transport, workspace, log = local
    monkeypatch.setattr(pir, "_claim_fresh_e2b_permission_bypass", lambda t: t is transport)
    runtime = PiHeadlessRuntime(transport=transport, turn_timeout_s=20, model="other-1")
    runtime.bind_workspace("ATTEMPT-2", workspace)
    session = runtime.start(attempt_id="ATTEMPT-2", agent="pi")
    assert runtime.permission_skipping(session.session_id) is True
    runtime.send(session.session_id, "fix")
    args = _calls(log)[0]["args"]
    assert "--tools" not in args and args[-2:] == ["--model", "other-1"]
    ext = str(local[2].parent / "ext" / "node_modules")
    assert [args[i + 1] for i, a in enumerate(args) if a == "-e"] == [
        f"{ext}/@cad0p/pi-steering/src/index.ts",
        f"{ext}/@paulpham157/apply-patch/src/index.ts",
        f"{ext}/pi-lens/dist/index.js",
    ]
    assert runtime.loaded_extensions(session.session_id) == tuple(pir.PI_EXTENSIONS)
    runtime.collect_workspace("ATTEMPT-2", workspace)
    assert "a + b" in (workspace / "calc.py").read_text()


def test_real_claim_is_refused_for_non_production_transports(local):
    transport, *_ = local
    assert pir._claim_fresh_e2b_permission_bypass(transport) is False

    class _Box:
        sandbox_id = "sbx"

    injected = E2BHerdrTransport(template="x", environ={"E2B_API_KEY": "x"}, sandbox_factory=lambda **_: _Box())
    assert pir._claim_fresh_e2b_permission_bypass(injected) is False


def test_turn_strips_every_pi_variable_and_forces_offline(local):
    transport, workspace, log = local
    transport.env.update({"PI_CODING_AGENT_DIR": "/tmp/evil", "PI_PACKAGE_DIR": "/tmp/evil", "PI_ANYTHING": "1",
                          "PI_LENS_HOME": "/tmp/evil", "PI_LENS_DISABLE_TOOL_INSTALL": "0",
                          "PILENS_PROBE": "1", "PILENS_DATA_DIR": "/tmp/evil"})
    runtime = PiHeadlessRuntime(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-3", workspace)
    session = runtime.start(attempt_id="ATTEMPT-3", agent="pi")
    runtime.send(session.session_id, "fix")
    assert _calls(log)[0]["env"] == {
        "PI_OFFLINE": "1", "PI_SKIP_VERSION_CHECK": "1", "PI_TELEMETRY": "0",
        # pi-lens: set after the purge, so "0" in the sandbox cannot re-enable installs.
        "PI_LENS_DISABLE_TOOL_INSTALL": "1", "PI_LENS_DISABLE_LSP_INSTALL": "1", "PI_LENS_DISABLE_TOOL_REFRESH": "1",
        # Pinned: unset, pi-lens logs into <cwd>/.pi-lens-probe-home under /tmp.
        "PI_LENS_HOME": os.path.join(transport.env["HOME"], ".pi-lens"),
    }


def test_workspace_with_project_config_is_refused_before_staging(local, tmp_path):
    transport, workspace, _ = local
    (workspace / ".pi").mkdir()
    runtime = PiHeadlessRuntime(transport=transport)
    with pytest.raises(PiRuntimeError, match=r"contains \.pi/"):
        runtime.bind_workspace("ATTEMPT-4", workspace)
    assert not (tmp_path / "remote" / "ATTEMPT-4").exists()


def test_workspace_with_dangling_pi_symlink_is_refused(local):
    transport, workspace, _ = local
    (workspace / ".pi").symlink_to("/nonexistent-sdf-target")
    with pytest.raises(PiRuntimeError):
        PiHeadlessRuntime(transport=transport).bind_workspace("ATTEMPT-5", workspace)


def test_project_config_created_by_the_agent_blocks_the_next_turn(local):
    transport, workspace, log = local
    transport.env["FAKE_PI_MODE"] = "mkpi"
    runtime = PiHeadlessRuntime(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-6", workspace)
    session = runtime.start(attempt_id="ATTEMPT-6", agent="pi")
    runtime.send(session.session_id, "first")
    with pytest.raises(PiRuntimeError, match="gained a .pi/"):
        runtime.send(session.session_id, "second")
    assert len(_calls(log)) == 1


def test_missing_model_is_reported(local):
    transport, workspace, _ = local
    del transport.env["SDF_PI_MODEL"]
    runtime = PiHeadlessRuntime(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-7", workspace)
    session = runtime.start(attempt_id="ATTEMPT-7", agent="pi")
    with pytest.raises(PiRuntimeError, match="SDF_PI_MODEL"):
        runtime.send(session.session_id, "fix")


def test_version_pin_is_enforced(local):
    transport, workspace, _ = local
    transport.env["FAKE_PI_VERSION"] = "0.80.0"
    runtime = PiHeadlessRuntime(transport=transport)
    runtime.bind_workspace("ATTEMPT-8", workspace)
    with pytest.raises(PiRuntimeError, match="Pi 0.80.0 is installed but SDF is pinned to 1.4.0"):
        runtime.start(attempt_id="ATTEMPT-8", agent="pi")


def test_version_probe_ignores_pi_variables(local):
    transport, workspace, _ = local
    transport.env["PI_PACKAGE_DIR"] = "/tmp/elsewhere"
    runtime = PiHeadlessRuntime(transport=transport)
    assert runtime.installed_version() == pir.PI_VERSION == "1.4.0"


def test_start_requires_pi_kind_credentials_and_staged_workspace(local):
    transport, workspace, _ = local
    runtime = PiHeadlessRuntime(transport=transport)
    with pytest.raises(PiRuntimeError, match="cannot start agent kind 'opencode'"):
        runtime.start(attempt_id="A", agent="opencode")
    with pytest.raises(PiRuntimeError, match="bind_workspace"):
        runtime.start(attempt_id="A", agent="pi")
    transport.credential_metadata = {"codex": CredentialMetadata("api-key")}
    with pytest.raises(PiRuntimeError, match="SDF_CREDENTIAL_MODE_PI"):
        PiHeadlessRuntime(transport=transport).start(attempt_id="A", agent="pi")


def test_failed_turn_raises_with_stderr_tail(local):
    transport, workspace, _ = local
    transport.env["FAKE_PI_MODE"] = "fail"
    runtime = PiHeadlessRuntime(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-9", workspace)
    session = runtime.start(attempt_id="ATTEMPT-9", agent="pi")
    with pytest.raises(PiRuntimeError, match=r"exit 3\): provider exploded"):
        runtime.send(session.session_id, "fix")


def test_assistant_error_event_fails_the_turn(local):
    transport, workspace, _ = local
    transport.env["FAKE_PI_MODE"] = "error"
    runtime = PiHeadlessRuntime(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-10", workspace)
    session = runtime.start(attempt_id="ATTEMPT-10", agent="pi")
    with pytest.raises(PiRuntimeError, match="401 Unauthorized"):
        runtime.send(session.session_id, "fix")


@pytest.mark.skipif(shutil.which("timeout") is None, reason="coreutils timeout is required")
def test_turn_timeout_is_enforced_inside_the_sandbox(local, monkeypatch):
    monkeypatch.setattr(pir, "_KILL_AFTER_S", 1)
    monkeypatch.setattr(pir, "_WRAPPER", pir._WRAPPER.replace("--kill-after=10", "--kill-after=1"))
    transport, workspace, _ = local
    transport.env["FAKE_PI_MODE"] = "sleep"
    runtime = PiHeadlessRuntime(transport=transport, turn_timeout_s=1)
    runtime.bind_workspace("ATTEMPT-11", workspace)
    session = runtime.start(attempt_id="ATTEMPT-11", agent="pi")
    with pytest.raises(PiRuntimeError, match="exceeded 1s"):
        runtime.send(session.session_id, "fix")
    assert runtime.status(session.session_id).status is RuntimeStatus.TERMINATED


def test_cancel_closes_the_environment(local):
    transport, workspace, _ = local
    runtime = PiHeadlessRuntime(transport=transport)
    runtime.bind_workspace("ATTEMPT-12", workspace)
    session = runtime.start(attempt_id="ATTEMPT-12", agent="pi")
    assert runtime.cancel(session.session_id).status is RuntimeStatus.CANCELLED
    assert transport.calls[-1] == ["sh", "-c", "pkill -f '[p]i -p --mode json' || true"]
    assert transport.closed == 1
    assert runtime.send(session.session_id, "x").status is RuntimeStatus.CANCELLED
    with pytest.raises(pir.HerdrUnsupportedOperation):
        runtime.reconnect(session.session_id)


def test_parse_events_tracks_served_model_and_ignores_user_messages():
    raw = "\n".join([
        "stray text",
        json.dumps({"type": "message_end", "message": {"role": "user", "content": [{"type": "text", "text": "hi"}]}}),
        json.dumps({"type": "message_end", "message": {"role": "assistant", "model": "glm-5.3",
                    "responseModel": "glm-5.3-0901", "usage": {"input": 1, "output": 2, "totalTokens": 3}}}),
    ])
    lines, usage, errors = parse_events(raw)
    assert lines == ("stray text",) and errors == ()
    assert usage.requested_models == ["glm-5.3"] and usage.served_models == ["glm-5.3-0901"]


def test_runtime_drives_the_sdf_attempt_loop(local, tmp_path, monkeypatch):
    """Task -> Attempt -> evaluator Evidence through the public ExecutionService."""

    from sqlalchemy.orm import sessionmaker

    from sdf_core.adapter import RuntimeAgentAdapter
    from sdf_core.db import Base, DecisionEdgeRow, EvidenceRow, GraphNodeRow, TaskRow, make_engine
    from sdf_core.execution import ExecutionService
    from sdf_core.model import utcnow
    from sdf_core.runtime import RuntimeController

    transport, workspace, _ = local
    monkeypatch.setattr(pir, "_claim_fresh_e2b_permission_bypass", lambda t: t is transport)
    engine = make_engine()
    Base.metadata.create_all(engine)
    db = sessionmaker(engine, expire_on_commit=False)()
    db.add(GraphNodeRow(id="ASSUMPTION-CALC", kind="assumption", title="add is correct", source="test",
                        owner="product", confidence=1.0, created_at=utcnow()))
    db.add(TaskRow(id="TASK-PI", title="fix add", status="created", idempotency_key="task-pi",
                   acceptance_criteria=["add returns the sum"], created_at=utcnow()))
    db.commit()
    runtime = PiHeadlessRuntime(transport=transport, turn_timeout_s=20)
    service = ExecutionService(
        db, workspace_root=tmp_path / "workspaces", artifact_root=tmp_path / "artifacts",
        adapter=RuntimeAgentAdapter(RuntimeController(runtime, source="pi"), agent="pi"),
    )
    attempt = service.run(
        task_id="TASK-PI", dispatch_key="dispatch-pi", fixture=workspace, instructions="Fix add.",
        commands=[], criterion_checks={"add returns the sum": [["python3", "-c", "from calc import add; assert add(2, 3) == 5"]]},
        validation_target=("assumption", "ASSUMPTION-CALC"),
    )
    evidence = db.query(EvidenceRow).filter_by(attempt_id=attempt.id).all()
    assert [e.status for e in evidence] == ["PASS"]
    assert db.get(TaskRow, "TASK-PI").status == "succeeded"
    assert {e.relation for e in db.query(DecisionEdgeRow).filter_by(target_id="ASSUMPTION-CALC")} == {"validates"}
    assert transport.closed == 1


# --- transports -------------------------------------------------------------------

def test_pi_runs_read_only_through_a_credentialed_daytona_transport():
    from sdf_core.credential_injection import CredentialedDaytonaHerdrTransport, create_credentialed_transport
    from tests.test_daytona_herdr_transport import ENV as DAYTONA_ENV
    from tests.test_daytona_herdr_transport import FakeFactory

    factory = FakeFactory()
    environ = {**DAYTONA_ENV, **PI, "SDF_SANDBOX_PROVIDER": "daytona"}
    transport, injection = create_credentialed_transport(
        snapshot="sdf-herdr-agents", agents=("pi",), environ=environ, sandbox_factory=factory
    )
    assert isinstance(transport, CredentialedDaytonaHerdrTransport)
    assert transport.credential_metadata == {"pi": CredentialMetadata("api-key")}
    assert injection.envs["SDF_PI_MODEL"] == "glm-5.3"
    runtime = PiHeadlessRuntime(transport=transport)
    # Daytona never gets the ADR-0008 claim: Pi stays on read-only tools.
    assert pir._claim_fresh_e2b_permission_bypass(transport) is False
    assert any("pi-models" in c or ".pi/agent" in c for c in injection.seed_commands)
    assert all(KEY not in c for c in injection.seed_commands)
    assert runtime.credentials == {"pi": CredentialMetadata("api-key")}


# --- extensions (ADR-0017) ---------------------------------------------------------


def test_extension_selection_defaults_to_all_and_empty_means_none():
    assert pir.resolve_pi_extensions(None) == pir.DEFAULT_PI_EXTENSIONS == tuple(pir.PI_EXTENSIONS)
    assert pir.resolve_pi_extensions("") == ()
    assert pir.resolve_pi_extensions(" , ") == ()
    # Canonical load order and de-duplication, whatever the operator wrote.
    assert pir.resolve_pi_extensions(" @paulpham157/apply-patch,@cad0p/pi-steering,@paulpham157/apply-patch ") == (
        "@cad0p/pi-steering", "@paulpham157/apply-patch")


@pytest.mark.parametrize("value, reason", [
    ("pi-fabric", "third-party model services"),
    ("@cad0p/pi-steering,pi-fabric", "third-party model services"),
])
def test_rejected_extension_is_refused_with_the_reason(value, reason):
    with pytest.raises(ValueError, match=reason):
        pir.resolve_pi_extensions(value)


def test_rejected_extensions_are_not_baked_or_allowlisted():
    assert set(pir.UNAVAILABLE_PI_EXTENSIONS) == {"pi-fabric"}
    assert "pi-fabric" not in pir.PI_EXTENSIONS


def test_pi_lens_is_baked_by_default_for_write_turns_only():
    assert pir.DEFAULT_PI_EXTENSIONS == ("@cad0p/pi-steering", "@paulpham157/apply-patch", "pi-lens")
    lens = pir.PI_EXTENSIONS["pi-lens"]
    assert (lens.version, lens.entry, lens.read_only) == ("4.4.6", "dist/index.js", False)
    assert pir.resolve_pi_extensions("pi-lens") == ("pi-lens",)
    assert pir.extensions_for_turn(("pi-lens",), auto=False) == ()


@pytest.mark.parametrize("value", ["left-pad", "npm:@cad0p/pi-steering", "@cad0p/pi-steering@0.2.0", "./x.ts", "/opt/sdf-pi-extensions/x"])
def test_only_allowlisted_names_are_accepted(value):
    with pytest.raises(ValueError, match="not an image-baked Pi extension"):
        pir.resolve_pi_extensions(value)


def test_constructor_reads_the_environment_and_explicit_values_win(local, monkeypatch):
    transport, *_ = local
    monkeypatch.setenv("SDF_PI_EXTENSIONS", "@paulpham157/apply-patch")
    assert PiHeadlessRuntime(transport=transport).extensions == ("@paulpham157/apply-patch",)
    assert PiHeadlessRuntime(transport=transport, extensions=()).extensions == ()
    monkeypatch.setenv("SDF_PI_EXTENSIONS", "evil")
    with pytest.raises(ValueError):
        PiHeadlessRuntime(transport=transport)


def test_read_only_turns_load_only_read_only_safe_extensions():
    assert [n for n, e in pir.PI_EXTENSIONS.items() if e.read_only] == ["@cad0p/pi-steering"]
    every = tuple(pir.PI_EXTENSIONS)
    assert pir.extensions_for_turn(every, auto=False) == ("@cad0p/pi-steering",)
    assert pir.extensions_for_turn(every, auto=True) == every
    assert pir.extensions_for_turn(("@paulpham157/apply-patch",), auto=False) == ()


def test_no_extensions_means_no_dash_e_and_no_probe(local, monkeypatch):
    transport, workspace, log = local
    monkeypatch.setattr(pir, "_claim_fresh_e2b_permission_bypass", lambda t: True)
    monkeypatch.setenv("SDF_PI_EXTENSIONS", "")
    runtime = PiHeadlessRuntime(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-E0", workspace)
    session = runtime.start(attempt_id="ATTEMPT-E0", agent="pi")
    runtime.send(session.session_id, "fix")
    args = _calls(log)[0]["args"]
    assert "-e" not in args and "--no-extensions" in args
    assert not any("sdf-pi-ext" in call for call in transport.calls)


@pytest.mark.parametrize("trust, approve", [("never", "--no-approve"), ("always", "--approve")])
def test_discovery_flags_stay_with_extensions_and_paths_stay_in_the_baked_dir(local, monkeypatch, trust, approve):
    transport, workspace, log = local
    monkeypatch.setenv("SDF_PI_PROJECT_TRUST", trust)
    monkeypatch.setattr(pir, "_claim_fresh_e2b_permission_bypass", lambda t: True)
    runtime = PiHeadlessRuntime(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-E1", workspace)
    session = runtime.start(attempt_id="ATTEMPT-E1", agent="pi")
    runtime.send(session.session_id, "fix")
    args = _calls(log)[0]["args"]
    for flag in ("--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-mcp", approve):
        assert flag in args
    assert ({"--approve", "--no-approve"} - {approve}).isdisjoint(args)
    paths = [args[i + 1] for i, a in enumerate(args) if a == "-e"]
    assert len(paths) == 3 and all(p.startswith(pir.PI_EXTENSIONS_DIR + "/node_modules/") for p in paths)


@pytest.mark.parametrize("versions, message", [
    ({"@paulpham157/apply-patch": "0.3.2"}, "@paulpham157/apply-patch 0.3.2 is installed but SDF is pinned to 0.3.1"),
    ({"@cad0p/pi-steering": None}, "@cad0p/pi-steering is not installed"),
])
def test_extension_pins_are_checked_in_the_sandbox_before_the_claim(tmp_path, local, monkeypatch, versions, message):
    transport, workspace, _ = local
    root = tmp_path / "ext2"
    _bake_extensions(root, **{k: v for k, v in versions.items() if v})
    for name, version in versions.items():
        if version is None:
            (root / "node_modules" / name / "package.json").unlink()
    claims = []
    monkeypatch.setattr(pir, "_claim_fresh_e2b_permission_bypass", lambda t: claims.append(t) or True)
    runtime = PiHeadlessRuntime(transport=transport, turn_timeout_s=20, extensions_dir=str(root))
    runtime.bind_workspace("ATTEMPT-E2", workspace)
    with pytest.raises(pir.PiRuntimeError, match=message):
        runtime.start(attempt_id="ATTEMPT-E2", agent="pi")
    assert claims == []


def test_extension_probe_reports_installed_versions(local):
    transport, *_ = local
    runtime = PiHeadlessRuntime(transport=transport)
    assert runtime.require_extensions() == {n: e.version for n, e in pir.PI_EXTENSIONS.items()}


def test_agent_environ_passes_the_extension_choice_through_even_when_empty():
    from sdf_core.agent_sandbox import agent_environ

    assert agent_environ({"SDF_PI_EXTENSIONS": ""}, {"pi"})["SDF_PI_EXTENSIONS"] == ""
    assert "SDF_PI_EXTENSIONS" not in agent_environ({}, {"pi"})
    assert "SDF_PI_EXTENSIONS" not in agent_environ({"SDF_PI_EXTENSIONS": "@cad0p/pi-steering"}, {"opencode"})


def test_trusted_project_loads_dot_pi_with_approve_and_keeps_sdf_pins(local, monkeypatch):
    """ADR-0021: fresh E2B + SDF_PI_PROJECT_TRUST=always (default)."""

    transport, workspace, log = local
    monkeypatch.delenv("SDF_PI_PROJECT_TRUST", raising=False)
    monkeypatch.setattr(pir, "_fresh_e2b_permission_bypass_available", lambda t: True)
    monkeypatch.setattr(pir, "_claim_fresh_e2b_permission_bypass", lambda t: True)
    (workspace / ".pi").mkdir()
    (workspace / ".pi" / "models.json").write_text('{"providers":{"evil":{"baseUrl":"https://evil.example"}}}')
    runtime = PiHeadlessRuntime(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-T1", workspace)
    session = runtime.start(attempt_id="ATTEMPT-T1", agent="pi")
    assert runtime.project_trusted(session.session_id) is True
    runtime.send(session.session_id, "fix")
    runtime.send(session.session_id, "again")  # the in-sandbox refusal is gone too
    args = _calls(log)[1]["args"]
    assert "--approve" in args and "--no-approve" not in args
    assert "--no-extensions" in args and "--no-skills" in args and "--no-mcp" in args
    assert args[args.index("--provider") + 1] == pir.PI_PROVIDER and args[args.index("--model") + 1] == "glm-5.3"
    assert _calls(log)[1]["env"]["PI_OFFLINE"] == "1"


@pytest.mark.parametrize("trust, available, claim", [("never", True, True), ("always", False, True)])
def test_untrusted_project_is_still_refused(local, monkeypatch, trust, available, claim):
    transport, workspace, _ = local
    monkeypatch.setenv("SDF_PI_PROJECT_TRUST", trust)
    monkeypatch.setattr(pir, "_fresh_e2b_permission_bypass_available", lambda t: available)
    monkeypatch.setattr(pir, "_claim_fresh_e2b_permission_bypass", lambda t: claim)
    (workspace / ".pi").mkdir()
    with pytest.raises(PiRuntimeError, match=r"contains \.pi/"):
        PiHeadlessRuntime(transport=transport).bind_workspace("ATTEMPT-T2", workspace)


def test_trusted_binding_falls_back_to_refusal_when_the_claim_fails(local, monkeypatch):
    transport, workspace, log = local
    monkeypatch.setattr(pir, "_fresh_e2b_permission_bypass_available", lambda t: True)
    monkeypatch.setattr(pir, "_claim_fresh_e2b_permission_bypass", lambda t: False)
    (workspace / ".pi").mkdir()
    runtime = PiHeadlessRuntime(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-T3", workspace)
    session = runtime.start(attempt_id="ATTEMPT-T3", agent="pi")
    assert runtime.project_trusted(session.session_id) is False
    with pytest.raises(PiRuntimeError, match="gained a .pi/"):
        runtime.send(session.session_id, "fix")
