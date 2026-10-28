"""Qwen Code and Goose agent kinds (ADR-0013), no network; Aider was removed (ADR-0016)."""

from __future__ import annotations

import json
import os
import shutil
import stat
import subprocess
import textwrap
from pathlib import Path

import pytest

from sdf_core.subagents import QWEN_ARGS

from sdf_core import headless_runtime as hr
from sdf_core.agent_models import agent_model_args
from sdf_core.credential_injection import prepare_credential_injection
from sdf_core.credentials import (
    AGENT_KINDS,
    DEFAULT_OPENAI_COMPATIBLE_BASE_URL,
    CredentialConfigError,
    CredentialMode,
    resolve_credential_plan,
)
from sdf_core.goose_runtime import GooseHeadlessRuntime, GooseRuntimeError
from sdf_core.goose_runtime import parse_output as parse_goose
from sdf_core.qwen_runtime import QwenHeadlessRuntime, QwenRuntimeError
from sdf_core.qwen_runtime import parse_events as parse_qwen
from sdf_core.runtime import CredentialMetadata, RuntimeStatus

KEY = "vk-test-0123456789abcdef-secret"
KINDS = ("qwen", "goose")
SEEDS = {"qwen": "qwen-key-ref", "goose": "goose-provider"}
RUNTIMES = {"qwen": (QwenHeadlessRuntime, QwenRuntimeError), "goose": (GooseHeadlessRuntime, GooseRuntimeError)}
VERSIONS = {"qwen": "0.26.2", "goose": " 1.56.0"}
CONFIG = {"qwen": (".qwen", ".env"), "goose": (".goose",)}


def env_for(kind: str, **extra: str) -> dict[str, str]:
    return {f"SDF_CREDENTIAL_MODE_{kind.upper()}": "api-key", f"SDF_{kind.upper()}_API_KEY": KEY, **extra}


# --- credential plan ------------------------------------------------------------

@pytest.mark.parametrize("kind", KINDS)
def test_kind_is_api_key_only_with_vultr_defaults(kind):
    upper = kind.upper()
    assert kind in AGENT_KINDS
    plan = resolve_credential_plan(kind, env_for(kind))
    assert plan.mode is CredentialMode.API_KEY
    assert dict(plan.set_variables) == {
        f"SDF_{upper}_API_KEY": KEY, f"SDF_{upper}_BASE_URL": DEFAULT_OPENAI_COMPATIBLE_BASE_URL,
        f"SDF_{upper}_MODEL": "glm-5.3",
    }
    assert DEFAULT_OPENAI_COMPATIBLE_BASE_URL == "https://api.vultrinference.com/v1"
    assert [(s.action, s.variable) for s in plan.seed_steps] == [(SEEDS[kind], f"SDF_{upper}_API_KEY")]
    with pytest.raises(CredentialConfigError, match=f"subscription is not supported for {kind}"):
        resolve_credential_plan(kind, env_for(kind, **{f"SDF_CREDENTIAL_MODE_{upper}": "subscription"}))
    with pytest.raises(CredentialConfigError, match=f"SDF_CREDENTIAL_MODE_{upper}"):
        resolve_credential_plan(kind, {f"SDF_{upper}_API_KEY": KEY})


@pytest.mark.parametrize("kind", KINDS)
def test_named_key_variable_without_fallback(kind):
    upper = kind.upper()
    env = env_for(kind, **{f"SDF_{upper}_API_KEY_ENV": "VULTR_INFERENCE_API_KEY", "VULTR_INFERENCE_API_KEY": "v"})
    plan = resolve_credential_plan(kind, env)
    assert plan.set_variables["VULTR_INFERENCE_API_KEY"] == "v"
    assert f"SDF_{upper}_API_KEY" not in plan.set_variables
    with pytest.raises(CredentialConfigError, match="requires GATEWAY_API_KEY") as err:
        resolve_credential_plan(kind, env_for(kind, **{f"SDF_{upper}_API_KEY_ENV": "GATEWAY_API_KEY"}))
    assert KEY not in str(err.value)
    # The bare vendor variable is never read implicitly (ADR-0007).
    with pytest.raises(CredentialConfigError, match=f"SDF_{upper}_API_KEY"):
        resolve_credential_plan(kind, {f"SDF_CREDENTIAL_MODE_{upper}": "api-key", "VULTR_INFERENCE_API_KEY": KEY})


@pytest.mark.parametrize("kind", KINDS)
@pytest.mark.parametrize("name", ["PATH", "lower_api_key", "OPENAI_API_KEY", "E2B_API_KEY", "OPENAI_X_API_KEY",
                                  "SDF_CONNECTION_X_API_KEY", KEY])
def test_named_key_must_be_a_dedicated_key_name(kind, name):
    env = env_for(kind, **{f"SDF_{kind.upper()}_API_KEY_ENV": name, name: "value-of-the-named-var"})
    with pytest.raises(CredentialConfigError) as err:
        resolve_credential_plan(kind, env)
    assert KEY not in str(err.value) and "value-of-the-named-var" not in str(err.value)


@pytest.mark.parametrize(("kind", "name"), [("qwen", "QWEN_X_API_KEY"), ("goose", "GOOSE_X_API_KEY"),
                                            ("qwen", "GEMINI_X_API_KEY")])
def test_named_key_cannot_use_a_prefix_the_wrapper_unsets(kind, name):
    env = env_for(kind, **{f"SDF_{kind.upper()}_API_KEY_ENV": name, name: "v"})
    with pytest.raises(CredentialConfigError, match="control variable"):
        resolve_credential_plan(kind, env)


@pytest.mark.parametrize("kind", KINDS)
def test_custom_endpoint_and_model_rules(kind):
    upper = kind.upper()
    plan = resolve_credential_plan(kind, env_for(kind, **{f"SDF_{upper}_BASE_URL": "https://llm.example.com/v1",
                                                         f"SDF_{upper}_MODEL": "org/big-1"}))
    assert plan.set_variables[f"SDF_{upper}_BASE_URL"] == "https://llm.example.com/v1"
    assert plan.set_variables[f"SDF_{upper}_MODEL"] == "org/big-1"
    assert agent_model_args(kind, {})[-1] == "glm-5.3"
    for url in ("http://api.vultrinference.com/v1", "https://127.0.0.1/v1", "https://localhost/v1"):
        with pytest.raises(CredentialConfigError):
            resolve_credential_plan(kind, env_for(kind, **{f"SDF_{upper}_BASE_URL": url}))
    for model in ("glm-5.3:high", "glm 5.3", "-x"):
        with pytest.raises(CredentialConfigError):
            resolve_credential_plan(kind, env_for(kind, **{f"SDF_{upper}_MODEL": model}))


def test_kinds_share_a_sandbox_with_codex_without_a_clash():
    environ = {**env_for("qwen"), **env_for("goose"),
               "SDF_CREDENTIAL_MODE_CODEX": "api-key", "SDF_OPENAI_API_KEY": "codex-key-value"}
    injection = prepare_credential_injection(
        ["codex", "qwen", "goose"], environ,
        base_envs={"QWEN_HOME": "/tmp/evil", "GOOSE_PATH_ROOT": "/tmp/evil", "KEEP": "1"},
    )
    assert injection.envs["OPENAI_API_KEY"] == "codex-key-value"
    assert not {"QWEN_HOME", "GOOSE_PATH_ROOT"} & set(injection.envs)
    assert injection.envs["KEEP"] == "1"
    assert all(KEY not in c and "codex-key-value" not in c for c in injection.seed_commands)
    assert KEY not in repr(injection)


@pytest.mark.skipif(shutil.which("node") is None, reason="node is required to execute the seed scripts")
@pytest.mark.parametrize("kind", KINDS)
def test_seed_records_the_key_by_name_only(kind, tmp_path):
    injection = prepare_credential_injection([kind], env_for(kind))
    env = {"HOME": str(tmp_path), "PATH": os.environ["PATH"], **injection.envs}
    for command in injection.seed_commands:
        subprocess.run(["bash", "-c", command], env=env, check=True)
    if kind == "goose":
        path = tmp_path / ".config/goose/custom_providers/sdf_openai.json"
        provider = json.loads(path.read_text())
        assert provider["api_key_env"] == "SDF_GOOSE_API_KEY"
        assert provider["engine"] == "openai" and provider["base_url"] == DEFAULT_OPENAI_COMPATIBLE_BASE_URL
        assert provider["models"] == [{"name": "glm-5.3", "context_limit": 200000}]
    else:
        path = tmp_path / f".config/sdf/{kind}-api-key-name"
        assert path.read_text() == f"SDF_{kind.upper()}_API_KEY\n"
    assert KEY not in path.read_text()
    assert stat.S_IMODE(path.stat().st_mode) == 0o600
    assert stat.S_IMODE(path.parent.stat().st_mode) == 0o700


# --- headless runtimes ---------------------------------------------------------

FAKE = textwrap.dedent('''\
    #!/usr/bin/env python3
    import json, os, sys, time
    kind = os.path.basename(sys.argv[0]); args = sys.argv[1:]
    if args == ["--version"]:
        print(os.environ.get("FAKE_VERSION") or {"qwen": "0.26.2", "goose": " 1.56.0"}[kind])
        sys.exit(0)
    prompt = sys.stdin.read()
    seen = {k: v for k, v in os.environ.items() if k.split("_")[0] in {"qwen": ("QWEN", "OPENAI", "GEMINI"),
            "goose": ("GOOSE", "OPENAI", "XDG")}[kind]}
    with open(os.environ["FAKE_LOG"], "a") as f:
        f.write(json.dumps({"kind": kind, "cwd": os.getcwd(), "args": args, "prompt": prompt, "env": seen}) + "\\n")
    mode = os.environ.get("FAKE_MODE", "ok")
    if mode == "fail":
        sys.stderr.write("provider exploded\\n"); sys.exit(3)
    if mode == "sleep":
        time.sleep(30)
    if mode == "mkcfg":
        os.mkdir(os.environ["FAKE_CFG"])
    write = ("--approval-mode" in args and args[args.index("--approval-mode") + 1] == "yolo") or \\
            os.environ.get("GOOSE_MODE") == "auto"
    if write:
        open("calc.py", "w").write("def add(a, b):\\n    return a + b\\n")
    if kind == "qwen":
        model = args[args.index("-m") + 1]
        state = os.path.join(os.environ["HOME"], "qwen-turns"); n = int(open(state).read()) if os.path.exists(state) else 0
        n += 1; open(state, "w").write(str(n))
        result = {"type": "result", "subtype": "success", "is_error": mode == "error", "num_turns": 1,
                  "usage": {"input_tokens": 80 * n, "output_tokens": 20 * n, "cache_read_input_tokens": 10 * n, "total_tokens": 100 * n},
                  "permission_denials": [] if write else [{"tool_name": "edit"}],
                  "stats": {"models": {model + "-served": {}}}}
        if mode == "error":
            result["error"] = {"message": "401 Unauthorized"}
        print(json.dumps([{"type": "system", "subtype": "init", "model": model},
                          {"type": "assistant", "message": {"role": "assistant", "content": [
                              {"type": "tool_use", "name": "edit"}, {"type": "text", "text": "DONE"}]}}, result], indent=2))
        sys.exit(0)
    # goose: banner on stdout, then the whole (resumed) session document.
    model = args[args.index("--model") + 1]
    state = os.path.join(os.environ["HOME"], "goose-session.json")
    messages = json.load(open(state)) if "-r" in args and os.path.exists(state) else []
    messages += [{"role": "user", "content": [{"type": "text", "text": prompt}]},
                 {"role": "assistant", "metadata": {"inference": {"requestedModel": model}}, "content": [
                     {"type": "toolRequest", "toolCall": {"value": {"name": "developer__shell"}}},
                     {"type": "text", "text": "DONE"}]}]
    json.dump(messages, open(state, "w"))
    n = len(messages) // 2
    print("    __( O)>  banner\\n   goose is ready")
    print(json.dumps({"messages": messages, "metadata": {"input_tokens": 80 * n, "output_tokens": 20 * n, "total_tokens": 100 * n,
          "cost_usd": 0.001 * n, "status": "error" if mode == "error" else "completed"}}, indent=2))
''')


class LocalTransport:
    """Runs argv locally; stages workspaces as plain directory copies."""

    def __init__(self, root: Path, env: dict[str, str], kind: str):
        self.root, self.env = root, env
        self.calls: list[list[str]] = []
        self.closed = 0
        self.credential_metadata = {kind: CredentialMetadata("api-key")}

    def exec_argv(self, argv, timeout_ms):
        self.calls.append(list(argv))
        if "pkill" in argv[-1]:
            return ""  # never signal real processes on the test host
        result = subprocess.run(list(argv), env=self.env, capture_output=True, text=True, timeout=timeout_ms / 1000)
        if result.returncode != 0:
            raise hr.HerdrRuntimeError(result.stderr[-1000:])
        return result.stdout

    def stage_workspace(self, attempt_id, workspace):
        target = self.root / "remote" / attempt_id
        shutil.copytree(workspace, target)
        return str(target)

    def collect_workspace(self, attempt_id, workspace):
        shutil.copytree(self.root / "remote" / attempt_id, workspace, dirs_exist_ok=True)

    def close(self, timeout_ms=30_000):
        self.closed += 1


@pytest.fixture(params=KINDS)
def local(request, tmp_path, monkeypatch):
    kind = request.param
    cls, _ = RUNTIMES[kind]
    bindir = tmp_path / "bin"
    bindir.mkdir()
    fake = bindir / kind
    fake.write_text(FAKE)
    fake.chmod(0o755)
    home = tmp_path / "home"
    (home / ".config/sdf").mkdir(parents=True)
    (home / f".config/sdf/{kind}-api-key-name").write_text("SDF_TEST_API_KEY\n")
    upper = kind.upper()
    env = {"PATH": f"{bindir}:{os.environ['PATH']}", "FAKE_LOG": str(tmp_path / "calls.jsonl"), "HOME": str(home),
           f"SDF_{upper}_MODEL": "glm-5.3", f"SDF_{upper}_BASE_URL": "https://llm.example.com/v1",
           "SDF_TEST_API_KEY": KEY,
           # Ambient provider/agent variables a turn must never see.
           "OPENAI_API_KEY": "ambient-key", "OPENAI_BASE_URL": "https://evil.example/v1", f"{upper}_ANYTHING": "1",
           "XDG_CONFIG_HOME": str(tmp_path / "evil-xdg"), "GEMINI_API_KEY": "g"}
    workspace = tmp_path / "ws"
    workspace.mkdir()
    (workspace / "calc.py").write_text("def add(a, b):\n    return a - b\n")
    monkeypatch.setattr(cls, "SCRATCH_DIR", str(tmp_path))
    monkeypatch.setattr(hr, "_claim_fresh_e2b_permission_bypass", lambda t: False)
    return kind, cls, LocalTransport(tmp_path, env, kind), workspace, tmp_path / "calls.jsonl"


def _calls(log: Path) -> list[dict]:
    return [json.loads(line) for line in log.read_text().splitlines()]


def test_read_only_turn_uses_stdin_prompt_named_key_and_reports_usage(local, tmp_path):
    kind, cls, transport, workspace, log = local
    runtime = cls(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-1", workspace)
    session = runtime.start(attempt_id="ATTEMPT-1", agent=kind)
    assert session.credential == CredentialMetadata("api-key")
    assert runtime.permission_skipping(session.session_id) is False
    done = runtime.send(session.session_id, "-x @calc.py starts with a dash")
    assert done.status is RuntimeStatus.COMPLETED
    assert "DONE" in runtime.stream(session.session_id)
    call = _calls(log)[0]
    assert call["cwd"] == str(tmp_path / "remote" / "ATTEMPT-1")
    assert call["prompt"] == "-x @calc.py starts with a dash"
    assert "-x @calc.py starts with a dash" not in call["args"]
    args = call["args"]
    env = call["env"]
    if kind == "qwen":
        assert args[:7] == ["-o", "json", "--bare", "--auth-type", "openai", *QWEN_ARGS]
        assert args[7:9] == ["--approval-mode", "plan"] and args[9] == "--session-id"
        assert args[-4:] == ["-m", "glm-5.3", "--openai-base-url", "https://llm.example.com/v1"]
        assert env == {"OPENAI_API_KEY": KEY, "QWEN_USAGE_STATISTICS_ENABLED": "false", "QWEN_TELEMETRY_ENABLED": "false",
                       "QWEN_SANDBOX": "false", "QWEN_CODE_NO_RELAUNCH": "true"}
    elif kind == "goose":
        assert args == ["run", "-i", "-", "--output-format", "json", "--no-profile", "-n", "sdf-ATTEMPT-1",
                        "--provider", "sdf_openai", "--model", "glm-5.3"]
        # Goose reads the key through the seeded provider's api_key_env, not OPENAI_API_KEY.
        assert env == {"GOOSE_MODE": "chat", "GOOSE_DISABLE_KEYRING": "1", "GOOSE_DISABLE_SESSION_NAMING": "true",
                       "GOOSE_TELEMETRY_ENABLED": "false", "GOOSE_TELEMETRY_OFF": "1"}
    usage = runtime.usage(session.session_id)
    assert usage["input_tokens"] == 80 and usage["requested_models"] == ["glm-5.3"]
    if kind == "qwen":
        assert usage["served_models"] == ["glm-5.3-served"]
    else:
        assert usage["served_models"] == [] and usage["served_model_reported"] is False
    runtime.collect_workspace("ATTEMPT-1", workspace)
    assert "a - b" in (workspace / "calc.py").read_text()
    runtime.send(session.session_id, "again")
    second = _calls(log)[1]["args"]
    if kind == "qwen":
        assert second[9:11] == ["--resume", args[10]]
        assert runtime.usage(session.session_id)["input_tokens"] == 160
    else:
        assert "-r" in second
        assert runtime.stream(session.session_id).count("DONE") == 2
        assert runtime.usage(session.session_id)["input_tokens"] == 160
    assert runtime.terminate(session.session_id).status is RuntimeStatus.TERMINATED
    runtime.terminate(session.session_id)
    assert transport.closed == 1


def test_write_mode_only_with_the_fresh_e2b_claim(local, monkeypatch):
    kind, cls, transport, workspace, log = local
    monkeypatch.setattr(hr, "_claim_fresh_e2b_permission_bypass", lambda t: t is transport)
    runtime = cls(transport=transport, turn_timeout_s=20, model="other-1")
    runtime.bind_workspace("ATTEMPT-2", workspace)
    session = runtime.start(attempt_id="ATTEMPT-2", agent=kind)
    assert runtime.permission_skipping(session.session_id) is True
    runtime.send(session.session_id, "fix")
    call = _calls(log)[0]
    args = call["args"]
    if kind == "qwen":
        assert args[args.index("--approval-mode") + 1] == "yolo" and "other-1" in args
    else:
        assert call["env"]["GOOSE_MODE"] == "auto"
        assert args[args.index("--with-builtin") + 1] == "developer" and "other-1" in args
    runtime.collect_workspace("ATTEMPT-2", workspace)
    assert "a + b" in (workspace / "calc.py").read_text()


def test_real_claim_is_refused_for_non_production_transports(local):
    from sdf_core.herdr_runtime import _claim_fresh_e2b_permission_bypass

    _, _, transport, *_ = local
    assert _claim_fresh_e2b_permission_bypass(transport) is False


@pytest.mark.parametrize("index", [0, 1])
def test_workspace_with_project_config_is_refused_before_staging(local, tmp_path, index):
    kind, cls, transport, workspace, _ = local
    entries = CONFIG[kind]
    if index >= len(entries):
        pytest.skip("fewer config entries")
    entry = workspace / entries[index]
    entry.mkdir() if index == 0 else entry.write_text("x")
    _, error = RUNTIMES[kind]
    with pytest.raises(error, match="would load as project config"):
        cls(transport=transport).bind_workspace("ATTEMPT-4", workspace)
    assert not (tmp_path / "remote" / "ATTEMPT-4").exists()


def test_dangling_config_symlink_is_refused(local):
    kind, cls, transport, workspace, _ = local
    (workspace / CONFIG[kind][0]).symlink_to("/nonexistent-sdf-target")
    with pytest.raises(RUNTIMES[kind][1]):
        cls(transport=transport).bind_workspace("ATTEMPT-5", workspace)


def test_project_config_created_by_the_agent_blocks_the_next_turn(local):
    kind, cls, transport, workspace, log = local
    transport.env.update({"FAKE_MODE": "mkcfg", "FAKE_CFG": {"qwen": ".qwen", "goose": ".goose"}[kind]})
    runtime = cls(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-6", workspace)
    session = runtime.start(attempt_id="ATTEMPT-6", agent=kind)
    runtime.send(session.session_id, "first")
    with pytest.raises(RUNTIMES[kind][1], match="gained an entry"):
        runtime.send(session.session_id, "second")
    assert len(_calls(log)) == 1


def test_ancestor_config_is_refused_in_the_sandbox(local, tmp_path):
    kind, cls, transport, workspace, log = local
    if kind == "goose":
        pytest.skip("Goose loads no ancestor config")
    runtime = cls(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-13", workspace)
    session = runtime.start(attempt_id="ATTEMPT-13", agent=kind)
    (tmp_path / "remote" / ".env").write_text("OPENAI_BASE_URL=https://evil.example/v1\n")
    with pytest.raises(RUNTIMES[kind][1], match="gained an entry"):
        runtime.send(session.session_id, "fix")
    assert not log.exists()


def test_missing_endpoint_or_key_is_reported(local):
    kind, cls, transport, workspace, log = local
    runtime = cls(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-7", workspace)
    session = runtime.start(attempt_id="ATTEMPT-7", agent=kind)
    saved = transport.env.pop(f"SDF_{kind.upper()}_MODEL")
    with pytest.raises(RUNTIMES[kind][1], match=f"SDF_{kind.upper()}_MODEL"):
        runtime.send(session.session_id, "fix")
    transport.env[f"SDF_{kind.upper()}_MODEL"] = saved
    if kind != "goose":
        del transport.env["SDF_TEST_API_KEY"]
        with pytest.raises(RUNTIMES[kind][1], match="no .* key"):
            runtime.send(session.session_id, "fix")
        # A key-name file that is not an identifier is never eval'd.
        transport.env["SDF_TEST_API_KEY"] = KEY
        (Path(transport.env["HOME"]) / f".config/sdf/{kind}-api-key-name").write_text("X;touch /tmp/pwn\n")
        with pytest.raises(RUNTIMES[kind][1], match="no .* key"):
            runtime.send(session.session_id, "fix")
    assert not log.exists()


def test_version_pin_is_enforced(local):
    kind, cls, transport, workspace, _ = local
    transport.env["FAKE_VERSION"] = "9.9.9"
    runtime = cls(transport=transport)
    runtime.bind_workspace("ATTEMPT-8", workspace)
    with pytest.raises(RUNTIMES[kind][1], match="9.9.9 is installed but SDF is pinned to"):
        runtime.start(attempt_id="ATTEMPT-8", agent=kind)


def test_start_requires_kind_credentials_and_staged_workspace(local):
    kind, cls, transport, workspace, _ = local
    runtime = cls(transport=transport)
    with pytest.raises(RUNTIMES[kind][1], match="cannot start agent kind 'opencode'"):
        runtime.start(attempt_id="A", agent="opencode")
    with pytest.raises(RUNTIMES[kind][1], match="bind_workspace"):
        runtime.start(attempt_id="A", agent=kind)
    transport.credential_metadata = {"codex": CredentialMetadata("api-key")}
    with pytest.raises(RUNTIMES[kind][1], match=f"SDF_CREDENTIAL_MODE_{kind.upper()}"):
        cls(transport=transport).start(attempt_id="A", agent=kind)
    with pytest.raises(ValueError):
        cls(transport=transport, model="glm:5")


def test_failed_turn_raises_with_stderr_tail(local):
    kind, cls, transport, workspace, _ = local
    transport.env["FAKE_MODE"] = "fail"
    runtime = cls(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-9", workspace)
    session = runtime.start(attempt_id="ATTEMPT-9", agent=kind)
    with pytest.raises(RUNTIMES[kind][1], match=r"exit 3\): .*provider exploded"):
        runtime.send(session.session_id, "fix")


def test_agent_reported_error_fails_the_turn(local):
    kind, cls, transport, workspace, _ = local
    transport.env["FAKE_MODE"] = "error"
    runtime = cls(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-10", workspace)
    session = runtime.start(attempt_id="ATTEMPT-10", agent=kind)
    with pytest.raises(RUNTIMES[kind][1], match="401 Unauthorized|error|AuthenticationError"):
        runtime.send(session.session_id, "fix")


@pytest.mark.skipif(shutil.which("timeout") is None, reason="coreutils timeout is required")
def test_turn_timeout_is_enforced_inside_the_sandbox(local, monkeypatch):
    kind, cls, transport, workspace, _ = local
    monkeypatch.setattr(cls, "WRAPPER", cls.WRAPPER.replace("--kill-after=10", "--kill-after=1"))
    transport.env["FAKE_MODE"] = "sleep"
    runtime = cls(transport=transport, turn_timeout_s=1)
    runtime.bind_workspace("ATTEMPT-11", workspace)
    session = runtime.start(attempt_id="ATTEMPT-11", agent=kind)
    with pytest.raises(RUNTIMES[kind][1], match="exceeded 1s"):
        runtime.send(session.session_id, "fix")
    assert runtime.status(session.session_id).status is RuntimeStatus.TERMINATED


def test_cancel_closes_the_environment(local):
    kind, cls, transport, workspace, _ = local
    runtime = cls(transport=transport)
    runtime.bind_workspace("ATTEMPT-12", workspace)
    session = runtime.start(attempt_id="ATTEMPT-12", agent=kind)
    assert runtime.cancel(session.session_id).status is RuntimeStatus.CANCELLED
    assert transport.calls[-1][-1].startswith("pkill -f '[")
    assert transport.closed == 1
    assert runtime.send(session.session_id, "x").status is RuntimeStatus.CANCELLED
    with pytest.raises(hr.HerdrUnsupportedOperation):
        runtime.reconnect(session.session_id)


def test_runtime_drives_the_sdf_attempt_loop(local, tmp_path, monkeypatch):
    """Task -> Attempt -> evaluator Evidence through the public ExecutionService."""

    from sqlalchemy.orm import sessionmaker

    from sdf_core.adapter import RuntimeAgentAdapter
    from sdf_core.db import Base, EvidenceRow, GraphNodeRow, TaskRow, make_engine
    from sdf_core.execution import ExecutionService
    from sdf_core.model import utcnow
    from sdf_core.runtime import RuntimeController

    kind, cls, transport, workspace, _ = local
    monkeypatch.setattr(hr, "_claim_fresh_e2b_permission_bypass", lambda t: t is transport)
    engine = make_engine()
    Base.metadata.create_all(engine)
    db = sessionmaker(engine, expire_on_commit=False)()
    db.add(GraphNodeRow(id="ASSUMPTION-CALC", kind="assumption", title="add is correct", source="test",
                        owner="product", confidence=1.0, created_at=utcnow()))
    db.add(TaskRow(id="TASK-X", title="fix add", status="created", idempotency_key="task-x",
                   acceptance_criteria=["add returns the sum"], created_at=utcnow()))
    db.commit()
    service = ExecutionService(
        db, workspace_root=tmp_path / "workspaces", artifact_root=tmp_path / "artifacts",
        adapter=RuntimeAgentAdapter(RuntimeController(cls(transport=transport, turn_timeout_s=20), source=kind), agent=kind),
    )
    attempt = service.run(
        task_id="TASK-X", dispatch_key="dispatch-x", fixture=workspace, instructions="Fix add.",
        commands=[], criterion_checks={"add returns the sum": [["python3", "-c", "from calc import add; assert add(2, 3) == 5"]]},
        validation_target=("assumption", "ASSUMPTION-CALC"),
    )
    assert [e.status for e in db.query(EvidenceRow).filter_by(attempt_id=attempt.id)] == ["PASS"]
    assert db.get(TaskRow, "TASK-X").status == "succeeded"
    assert transport.closed == 1


# --- parsers -------------------------------------------------------------------

def test_qwen_parser_reports_served_model_and_rejects_garbage():
    raw = json.dumps([{"type": "system", "subtype": "init", "model": "glm-5.3"},
                      {"type": "result", "is_error": False, "num_turns": 2, "usage": {"input_tokens": 5, "output_tokens": 1},
                       "stats": {"models": {"glm-5.3-0901": {}}}}])
    lines, usage, errors = parse_qwen("noise\n" + raw)
    assert errors == () and usage is not None
    assert usage.requested_models == ["glm-5.3"] and usage.served_models == ["glm-5.3-0901"]
    assert parse_qwen("not json")[2]
    assert parse_qwen("[]")[2] == ("Qwen Code printed no result event",)


def test_goose_parser_skips_earlier_turns_and_banner():
    doc = {"messages": [{"role": "assistant", "content": [{"type": "text", "text": "old"}]},
                        {"role": "assistant", "content": [{"type": "text", "text": "new"}]}],
           "metadata": {"input_tokens": 3, "output_tokens": 2, "status": "completed"}}
    lines, usage, errors, count = parse_goose("banner {x}\n" + json.dumps(doc, indent=1), seen=1)
    assert lines == ("new",) and errors == () and count == 2 and usage.input_tokens == 3
    assert parse_goose("no json")[2]



@pytest.mark.parametrize("removed", ["aider", "cursor"])
def test_removed_kinds_are_unknown_everywhere(removed):
    """ADR-0016: Aider and the Cursor Agent CLI are not agent kinds."""
    from sdf_core import agent_models, credential_injection
    from sdf_core.credentials import CredentialConfigError

    assert AGENT_KINDS == ("claude", "codex", "opencode", "pi", "qwen", "goose")
    with pytest.raises(CredentialConfigError, match="unknown agent kind"):
        resolve_credential_plan(removed, {f"SDF_{removed.upper()}_API_KEY": KEY})
    assert removed not in agent_models.MODEL_VARIABLES and removed not in agent_models.DEFAULT_MODELS
    assert not any(removed in seed for seed in credential_injection._SEED_SCRIPTS)
    assert not (Path(__file__).resolve().parents[1] / "sdf_core" / f"{removed}_runtime.py").exists()


def test_trusted_project_loads_and_keeps_sdf_endpoint_model_and_tool_bans(local, monkeypatch):
    """ADR-0021: fresh E2B + SDF_<KIND>_PROJECT_TRUST=always (default)."""

    kind, cls, transport, workspace, log = local
    monkeypatch.delenv(f"SDF_{kind.upper()}_PROJECT_TRUST", raising=False)
    monkeypatch.setattr(hr, "_fresh_e2b_permission_bypass_available", lambda t: True)
    monkeypatch.setattr(hr, "_claim_fresh_e2b_permission_bypass", lambda t: t is transport)
    (workspace / CONFIG[kind][0]).mkdir()
    runtime = cls(transport=transport, turn_timeout_s=20)
    runtime.bind_workspace("ATTEMPT-T1", workspace)
    session = runtime.start(attempt_id="ATTEMPT-T1", agent=kind)
    assert runtime.project_trusted(session.session_id) is True
    runtime.send(session.session_id, "fix")
    (Path(transport.root) / "remote" / ".env").write_text("OPENAI_BASE_URL=https://evil.example/v1\n")
    runtime.send(session.session_id, "again")
    args = _calls(log)[1]["args"]
    if kind == "qwen":
        assert "--bare" not in args and args[args.index("--exclude-tools") + 1] == QWEN_ARGS[1]
        assert args[-4:] == ["-m", "glm-5.3", "--openai-base-url", "https://llm.example.com/v1"]
    else:
        assert "--no-profile" in args and args[args.index("--provider") + 1] == "sdf_openai"
        assert args[args.index("--model") + 1] == "glm-5.3"


@pytest.mark.parametrize("trust, available", [("never", True), ("always", False)])
def test_untrusted_project_config_is_still_refused(local, monkeypatch, trust, available):
    kind, cls, transport, workspace, _ = local
    monkeypatch.setenv(f"SDF_{kind.upper()}_PROJECT_TRUST", trust)
    monkeypatch.setattr(hr, "_fresh_e2b_permission_bypass_available", lambda t: available)
    (workspace / CONFIG[kind][0]).mkdir()
    with pytest.raises(RUNTIMES[kind][1], match="would load as project config"):
        cls(transport=transport).bind_workspace("ATTEMPT-T2", workspace)
