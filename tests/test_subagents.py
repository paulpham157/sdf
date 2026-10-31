"""ADR-0020: sub-agents are off for every agent kind, from host-controlled argv/config."""

from __future__ import annotations

import json
import os
import shutil
import subprocess

import pytest

from sdf_core import subagents
from sdf_core.credential_injection import prepare_credential_injection
from sdf_core.goose_runtime import GooseHeadlessRuntime
from sdf_core.headless_runtime import HeadlessBinding
from sdf_core.herdr_runtime import _agent_start_args
from sdf_core.pi_runtime import PI_EXTENSIONS, UNAVAILABLE_PI_EXTENSIONS, resolve_pi_extensions
from sdf_core.qwen_runtime import QwenHeadlessRuntime

VULTR = {
    "SDF_CREDENTIAL_MODE_OPENCODE": "api-key",
    "OPENCODE_API_KEY": "vk-test-0123456789abcdef-secret",
    "OPENCODE_BASE_URL": "https://llm.example.com/v1",
}


@pytest.mark.parametrize("dangerous", [False, True])
def test_claude_always_disallows_the_agent_and_team_tools(dangerous):
    args = _agent_start_args("claude", "/w", allow_dangerous_permissions=dangerous)
    i = args.index("--disallowedTools")
    denied = args[i + 1].split(",")
    assert {"Agent", "Task", "Workflow", "ListAgents", "SendMessage"} <= set(denied)
    # One comma-joined value: a later positional cannot be parsed as a tool name.
    assert not any(a in ("Agent", "Task") for a in args)
    assert ("--dangerously-skip-permissions" in args) is dangerous


def test_codex_disables_v1_and_caps_v2_on_the_command_line():
    args = _agent_start_args("codex", "/tmp/sdf/A")
    pairs = [args[i + 1] for i, a in enumerate(args) if a == "-c"]
    assert "features.multi_agent=false" in pairs
    # Catalog-v2 models ignore the flags; one thread slot refuses every spawn.
    assert "features.multi_agent_v2={enabled=false,max_concurrent_threads_per_session=1}" in pairs
    assert args[: len(subagents.CODEX_ARGS)] == subagents.CODEX_ARGS


@pytest.mark.parametrize("agent", ["opencode", "pi", "qwen", "goose"])
def test_headless_kinds_get_no_pane_start_args(agent):
    assert subagents.start_args(agent) == ()


class _NoExec:
    def exec_argv(self, argv, timeout_ms):  # pragma: no cover - argv tests never run a command
        raise AssertionError("no command expected")


def _binding(auto: bool, turns: int = 0) -> HeadlessBinding:
    return HeadlessBinding("ATTEMPT-1", "/w", auto, turns=turns)


@pytest.mark.parametrize("auto", [False, True])
@pytest.mark.parametrize("turns", [0, 1])
def test_qwen_excludes_the_agent_tools_every_turn(auto, turns):
    argv = QwenHeadlessRuntime(transport=_NoExec()).agent_argv(_binding(auto, turns), "/tmp/p")
    excluded = argv[argv.index("--exclude-tools") + 1].split(",")
    assert {"agent", "create_sub_session", "team_create", "workflow", "send_message", "list_agents"} <= set(excluded)


@pytest.mark.parametrize("auto", [False, True])
@pytest.mark.parametrize("turns", [0, 1])
def test_goose_never_loads_a_profile_or_summon(auto, turns):
    argv = GooseHeadlessRuntime(transport=_NoExec()).agent_argv(_binding(auto, turns), "/tmp/p")
    assert "--no-profile" in argv
    assert not any("summon" in a or "delegate" in a for a in argv)
    builtins = [argv[i + 1] for i, a in enumerate(argv) if a == "--with-builtin"]
    assert all(set(b.split(",")) <= set(subagents.GOOSE_ALLOWED_BUILTINS) for b in builtins)


def test_pi_extension_allowlist_has_no_subagent_extension():
    assert "pi-fabric" in UNAVAILABLE_PI_EXTENSIONS
    with pytest.raises(ValueError, match="ADR-0017"):
        resolve_pi_extensions("pi-fabric")
    assert not any("agent" in name or "fabric" in name for name in PI_EXTENSIONS)


@pytest.mark.skipif(shutil.which("node") is None, reason="node is required to execute the seed script")
def test_opencode_user_config_denies_the_subagent_tool_last(tmp_path):
    injection = prepare_credential_injection(["opencode"], {**VULTR, "OPENCODE_MODEL": "vultr/glm-5.3"})
    env = {"HOME": str(tmp_path), "PATH": os.environ["PATH"], **injection.envs}
    for command in injection.seed_commands:
        subprocess.run(["bash", "-c", command], env=env, check=True)
    config = json.loads((tmp_path / ".config" / "opencode" / "opencode.json").read_text())
    rules = [(r["action"], r["resource"], r["effect"]) for r in config["permissions"]]
    for action in subagents.OPENCODE_DENIED_ACTIONS:
        # Last match wins: nothing after the deny re-allows it.
        i = rules.index((action, "*", "deny"))
        assert not any(a in (action, "*") and e != "deny" for a, _, e in rules[i + 1 :])
    assert "subagent" in subagents.OPENCODE_DENIED_ACTIONS
