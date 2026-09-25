import json
from pathlib import Path

import pytest

from sdf_core.herdr_runtime import HerdrBindingSnapshot, HerdrRuntime, HerdrRuntimeError, SshHerdrTransport
from sdf_core.runtime import RuntimeStatus


class FakeHerdr:
    def __init__(self):
        self.calls = []

    def __call__(self, command, timeout_ms):
        self.calls.append((tuple(command), timeout_ms))
        if tuple(command[1:3]) == ("workspace", "create"):
            return json.dumps({"workspaceId": "ws-1", "paneId": "pane-1"})
        if tuple(command[1:3]) == ("agent", "start"):
            return json.dumps({"agentSessionId": "agent-1", "status": "working"})
        if tuple(command[1:3]) == ("agent", "prompt"):
            return json.dumps({"status": "idle", "output": "accepted"})
        if tuple(command[1:3]) == ("agent", "read"):
            return json.dumps({"output": "accepted"})
        if tuple(command[1:3]) == ("agent", "get"):
            return json.dumps({"status": "done"})
        if tuple(command[1:3]) == ("api", "snapshot"):
            return json.dumps({"snapshot": {"agents": [{"name": "agent-1", "status": "working", "pane_id": "pane-1"}]}})
        if tuple(command[1:3]) == ("agent", "send-keys"):
            return ""
        if tuple(command[1:3]) == ("pane", "process-info"):
            return json.dumps({"process_info": {"foreground_processes": [{"pid": 1, "name": "zsh"}]}})
        if tuple(command[1:3]) == ("pane", "close"):
            return json.dumps({"type": "ok"})
        raise AssertionError(command)


def test_herdr_probe_records_installed_identity_without_claiming_live_agent():
    def runner(command, timeout_ms):
        assert timeout_ms > 0
        if tuple(command[1:]) == ("--version",):
            return "herdr 0.7.3\n"
        if tuple(command[1:]) == ("session", "list", "--json"):
            return json.dumps({"sessions": [{"name": "default", "running": False}]})
        raise AssertionError(command)

    result = HerdrRuntime(runner=runner).probe()
    assert result.version == "0.7.3"
    assert result.cli_available is True
    assert result.matches("0.7.3") is True
    assert result.sessions[0]["running"] is False
    assert result.as_dict()["cli_available"] is True
    assert result.as_dict()["sessions"][0]["running"] is False


def test_herdr_expected_version_gate_fails_closed():
    def runner(command, timeout_ms):
        if tuple(command[1:]) == ("--version",):
            return "herdr 0.7.3\n"
        if tuple(command[1:]) == ("session", "list", "--json"):
            return json.dumps({"sessions": []})
        raise AssertionError(command)

    runtime = HerdrRuntime(runner=runner, expected_version="0.9.1")
    with pytest.raises(HerdrRuntimeError, match="version mismatch"):
        runtime.require_compatible()


def test_herdr_runtime_maps_documented_cli_lifecycle_and_correlates_attempt():
    runner = FakeHerdr()
    runtime = HerdrRuntime(runner=runner, timeout_ms=3210)

    session = runtime.start(attempt_id="ATTEMPT-101", agent="codex")
    assert session.session_id == "agent-1"
    assert session.attempt_id == "ATTEMPT-101"
    assert session.status is RuntimeStatus.RUNNING

    updated = runtime.send(session.session_id, "hello")
    # A normal prompt return ends the agent's turn; that is still not
    # evaluator acceptance or terminal task completion.
    assert updated.status is RuntimeStatus.COMPLETED
    assert runtime.stream(session.session_id) == ("accepted",)
    assert runtime.status(session.session_id).status is RuntimeStatus.COMPLETED
    assert runtime.reconnect(session.session_id).session_id == "agent-1"
    assert all(timeout == 3210 for _, timeout in runner.calls)


def test_herdr_runtime_accepts_09_wrappers_and_raw_prompt_read_output():
    def runner(command, timeout_ms):
        operation = tuple(command[1:3])
        if operation == ("workspace", "create"):
            return json.dumps({"result": {"workspace": {"workspace_id": "ws-9"}, "root_pane": {"pane_id": "pane-9"}}})
        if operation == ("agent", "start"):
            return json.dumps({"result": {"agent": {"name": "codex-9", "agent_status": "idle", "pane_id": "pane-9"}}})
        if operation == ("agent", "prompt"):
            return ""
        if operation == ("agent", "read"):
            return "hello from agent\n"
        if operation == ("agent", "get"):
            return json.dumps({"result": {"agent": {"name": "codex-9", "agent_status": "done"}}})
        if operation == ("api", "snapshot"):
            return json.dumps({"result": {"snapshot": {"agents": [{"name": "codex-9", "pane_id": "pane-9"}]}}})
        raise AssertionError(command)

    runtime = HerdrRuntime(runner=runner)
    session = runtime.start(attempt_id="ATTEMPT-09", agent="codex")
    assert session.session_id == "codex-9"
    assert runtime.send(session.session_id, "hello").output == ("hello from agent",)
    assert runtime.status(session.session_id).status is RuntimeStatus.COMPLETED
    assert runtime.reconnect(session.session_id).session_id == "codex-9"


def test_herdr_runtime_start_is_idempotent_for_attempt():
    runner = FakeHerdr()
    runtime = HerdrRuntime(runner=runner)
    first = runtime.start(attempt_id="ATTEMPT-102", agent="codex")
    second = runtime.start(attempt_id="ATTEMPT-102", agent="codex")
    assert second == first
    assert sum(tuple(command[1:3]) == ("agent", "start") for command, _ in runner.calls) == 1


def test_herdr_runtime_binds_attempt_workspace_before_start(tmp_path: Path):
    runner = FakeHerdr()
    runtime = HerdrRuntime(runner=runner)
    runtime.bind_workspace("ATTEMPT-WORKSPACE", tmp_path)
    runtime.start(attempt_id="ATTEMPT-WORKSPACE", agent="codex")
    workspace_commands = [command for command, _ in runner.calls if tuple(command[1:3]) == ("workspace", "create")]
    assert workspace_commands
    assert "--cwd" in workspace_commands[0]
    assert str(tmp_path.resolve()) in workspace_commands[0]


def test_herdr_runtime_can_bind_workspace_in_remote_execution_environment():
    runner = FakeHerdr()
    runtime = HerdrRuntime(runner=runner)
    runtime.bind_remote_workspace("ATTEMPT-REMOTE", "/workspace/attempt-remote")
    runtime.start(attempt_id="ATTEMPT-REMOTE", agent="codex")
    workspace_commands = [command for command, _ in runner.calls if tuple(command[1:3]) == ("workspace", "create")]
    assert "--cwd" in workspace_commands[0]
    assert "/workspace/attempt-remote" in workspace_commands[0]


def test_ssh_herdr_transport_builds_remote_command_without_shell_interpolation():
    calls = []

    def executor(command, timeout_ms):
        calls.append((tuple(command), timeout_ms))
        return "ok"

    transport = SshHerdrTransport(
        host="sandbox.example",
        user="runner",
        port=2222,
        herdr_binary="/opt/herdr/bin/herdr",
        executor=executor,
    )
    assert transport.run(["herdr", "agent", "prompt", "agent-1", "say hi"], 1234) == "ok"
    assert calls == [(
        ("ssh", "-p", "2222", "runner@sandbox.example", "/opt/herdr/bin/herdr", "agent", "prompt", "agent-1", "say hi"),
        1234,
    )]


def test_herdr_runtime_restores_durable_binding_without_redispatch():
    runner = FakeHerdr()
    runtime = HerdrRuntime(runner=runner)
    restored = runtime.restore_binding(HerdrBindingSnapshot(
        attempt_id="ATTEMPT-RESTORED",
        session_id="agent-1",
        agent="codex",
        workspace_id="ws-1",
        pane_id="pane-1",
    ))

    assert restored.attempt_id == "ATTEMPT-RESTORED"
    reconnected = runtime.reconnect("agent-1")
    assert reconnected.session_id == "agent-1"
    assert not any(tuple(command[1:3]) == ("agent", "start") for command, _ in runner.calls)


def test_herdr_runtime_reconnects_after_adapter_restart_from_snapshot():
    first_runner = FakeHerdr()
    first = HerdrRuntime(runner=first_runner)
    session = first.start(attempt_id="ATTEMPT-RESTART", agent="codex")
    snapshot = HerdrBindingSnapshot(
        attempt_id=session.attempt_id,
        session_id=session.session_id,
        agent=session.agent,
        workspace_id="ws-1",
        pane_id="pane-1",
    )

    restarted_runner = FakeHerdr()
    restarted = HerdrRuntime(runner=restarted_runner)
    restored = restarted.restore_binding(snapshot)

    assert restarted.reconnect(restored.session_id).status is RuntimeStatus.COMPLETED
    assert not any(tuple(command[1:3]) == ("agent", "start") for command, _ in restarted_runner.calls)


def test_herdr_binding_snapshot_round_trips_and_rejects_incomplete_payload():
    snapshot = HerdrBindingSnapshot("a", "s", "codex", "w", "p")
    assert HerdrBindingSnapshot.from_mapping(snapshot.as_dict()) == snapshot
    with pytest.raises(ValueError, match="non-empty strings"):
        HerdrBindingSnapshot.from_mapping({"attempt_id": "a"})


def test_herdr_restore_rejects_attempt_rebinding_to_another_session():
    runtime = HerdrRuntime(runner=FakeHerdr())
    runtime.restore_binding(HerdrBindingSnapshot("attempt", "session-a", "codex", "w", "p"))
    with pytest.raises(HerdrRuntimeError, match="different restored session"):
        runtime.restore_binding(HerdrBindingSnapshot("attempt", "session-b", "codex", "w", "p"))


def test_herdr_runtime_cancels_only_after_agent_process_is_gone():
    runtime = HerdrRuntime(runner=FakeHerdr())
    session = runtime.start(attempt_id="ATTEMPT-103", agent="codex")
    cancelled = runtime.cancel(session.session_id)
    assert cancelled.status is RuntimeStatus.CANCELLED


def test_herdr_runtime_terminates_by_closing_bound_pane():
    runtime = HerdrRuntime(runner=FakeHerdr())
    session = runtime.start(attempt_id="ATTEMPT-104", agent="codex")
    terminated = runtime.terminate(session.session_id)
    assert terminated.status is RuntimeStatus.TERMINATED


def test_herdr_runtime_does_not_accept_a_remaining_foreground_child():
    calls = 0

    def runner(command, timeout_ms):
        nonlocal calls
        operation = tuple(command[1:3])
        if operation == ("workspace", "create"):
            return json.dumps({"workspaceId": "ws-child", "paneId": "pane-child"})
        if operation == ("agent", "start"):
            return json.dumps({"agentSessionId": "agent-child", "status": "working"})
        if operation == ("agent", "send-keys"):
            return ""
        if operation == ("pane", "process-info"):
            calls += 1
            return json.dumps({"process_info": {
                "shell_pid": 10,
                "foreground_processes": [
                    {"pid": 10, "name": "zsh"},
                    {"pid": 11, "name": "sleep", "argv0": "sleep"},
                ],
            }})
        if operation == ("pane", "close"):
            return json.dumps({"type": "ok"})
        raise AssertionError(command)

    runtime = HerdrRuntime(runner=runner, timeout_ms=20)
    session = runtime.start(attempt_id="ATTEMPT-CHILD", agent="codex")
    with pytest.raises(HerdrRuntimeError, match="foreground child"):
        runtime.cancel(session.session_id)
    assert calls >= 1


def test_herdr_runtime_escalates_cancel_to_the_attempt_pane_when_ctrl_c_leaves_codex_foreground():
    calls = []
    pane_closed = False

    def runner(command, timeout_ms):
        nonlocal pane_closed
        calls.append(tuple(command))
        operation = tuple(command[1:3])
        if operation == ("workspace", "create"):
            return json.dumps({"workspaceId": "ws-active", "paneId": "pane-active"})
        if operation == ("agent", "start"):
            return json.dumps({"agentSessionId": "agent-active", "status": "working"})
        if operation == ("agent", "send-keys"):
            return ""
        if operation == ("pane", "process-info"):
            if pane_closed:
                raise RuntimeError("pane not found")
            return json.dumps({"process_info": {
                "shell_pid": 10,
                "foreground_processes": [
                    {"pid": 10, "name": "zsh"},
                    {"pid": 11, "name": "codex", "cmdline": "codex exec sleep 60"},
                ],
            }})
        if operation == ("pane", "close"):
            pane_closed = True
            return json.dumps({"type": "ok"})
        raise AssertionError(command)

    runtime = HerdrRuntime(runner=runner, timeout_ms=20)
    session = runtime.start(attempt_id="ATTEMPT-ACTIVE", agent="codex")

    assert runtime.cancel(session.session_id).status is RuntimeStatus.CANCELLED
    assert ("herdr", "pane", "close", "pane-active") in calls


def test_herdr_runtime_cancellation_closes_a_provider_execution_environment_once():
    class Transport:
        def __init__(self):
            self.runner = FakeHerdr()
            self.close_calls = []

        def run(self, command, timeout_ms):
            return self.runner(command, timeout_ms)

        def close(self, timeout_ms):
            self.close_calls.append(timeout_ms)

    transport = Transport()
    runtime = HerdrRuntime(transport=transport)
    session = runtime.start(attempt_id="ATTEMPT-E2B-CLEANUP", agent="codex")

    assert runtime.cancel(session.session_id).status is RuntimeStatus.CANCELLED
    assert runtime.terminate(session.session_id).status is RuntimeStatus.TERMINATED
    assert transport.close_calls == [30_000]


def test_herdr_runtime_keeps_a_stalled_prompt_running_when_process_info_confirms_codex_foreground():
    def runner(command, timeout_ms):
        operation = tuple(command[1:3])
        if operation == ("workspace", "create"):
            return json.dumps({"workspaceId": "ws-stalled", "paneId": "pane-stalled"})
        if operation == ("agent", "start"):
            return json.dumps({"agentSessionId": "agent-stalled", "status": "working"})
        if operation == ("agent", "prompt"):
            raise RuntimeError('{"error":{"code":"agent_prompt_stalled"}}')
        if operation == ("agent", "get"):
            return json.dumps({"agent": {"agent_status": "idle", "interactive_ready": True}})
        if operation == ("pane", "send-keys"):
            return ""
        if operation == ("agent", "wait"):
            raise RuntimeError('{"error":{"code":"timeout"}}')
        if operation == ("pane", "process-info"):
            return json.dumps({"process_info": {
                "shell_pid": 10,
                "foreground_processes": [
                    {"pid": 10, "name": "zsh"},
                    {"pid": 11, "name": "codex", "cmdline": "codex exec"},
                ],
            }})
        if operation == ("agent", "read"):
            return ""
        raise AssertionError(command)

    runtime = HerdrRuntime(runner=runner)
    session = runtime.start(attempt_id="ATTEMPT-STALLED", agent="codex")
    assert runtime.send(session.session_id, "work").status is RuntimeStatus.RUNNING


def test_herdr_runtime_rejects_malformed_provider_payload():
    runtime = HerdrRuntime(runner=lambda *_: "not-json")
    with pytest.raises(HerdrRuntimeError, match="JSON"):
        runtime.start(attempt_id="ATTEMPT-104", agent="codex")


def test_herdr_runtime_starts_claude_with_bypass_permissions_for_the_disposable_sandbox():
    runner = FakeHerdr()
    HerdrRuntime(runner=runner).start(attempt_id="ATTEMPT-CLAUDE", agent="claude")

    starts = [command for command, _ in runner.calls if tuple(command[1:3]) == ("agent", "start")]
    assert starts == [
        ("herdr", "agent", "start", "claude", "--kind", "claude", "--pane", "pane-1",
         "--", "--dangerously-skip-permissions"),
    ]


def test_herdr_runtime_passes_no_extra_agent_args_to_codex_without_a_workspace():
    runner = FakeHerdr()
    HerdrRuntime(runner=runner).start(attempt_id="ATTEMPT-CODEX", agent="codex")

    starts = [command for command, _ in runner.calls if tuple(command[1:3]) == ("agent", "start")]
    assert starts == [("herdr", "agent", "start", "codex", "--kind", "codex", "--pane", "pane-1")]


def test_herdr_runtime_starts_codex_trusting_exactly_its_attempt_directory():
    runner = FakeHerdr()
    runtime = HerdrRuntime(runner=runner)
    runtime.bind_remote_workspace("ATTEMPT-CODEX", "/tmp/sdf/ATTEMPT-CODEX")
    runtime.start(attempt_id="ATTEMPT-CODEX", agent="codex")

    (start,) = [command for command, _ in runner.calls if tuple(command[1:3]) == ("agent", "start")]
    assert start[-3:] == ("--", "-c", 'projects."/tmp/sdf/ATTEMPT-CODEX".trust_level="trusted"')

class TurnHerdr:
    """Herdr 0.9.1 as observed live: a finished turn reads ``idle``, never ``done``.

    ``prompt`` is a list of outcomes for successive ``agent prompt`` calls;
    ``states`` is the sequence of ``agent_status`` values ``agent get``/``agent
    wait`` observe (the last one repeats).
    """

    def __init__(self, agent, *, prompt="ok", states=("idle",), foreground=True):
        self.agent = agent
        self.prompt = prompt
        self.states = list(states)
        self.foreground = foreground
        self.calls = []

    def _state(self):
        return self.states.pop(0) if len(self.states) > 1 else self.states[0]

    def __call__(self, command, timeout_ms):
        command = tuple(command)
        self.calls.append(command)
        operation = command[1:3]
        if operation == ("workspace", "create"):
            return json.dumps({"result": {"workspace": {"workspace_id": "ws-t"}, "root_pane": {"pane_id": "pane-t"}}})
        if operation == ("agent", "start"):
            return json.dumps({"result": {"agent": {"name": f"{self.agent}-t", "agent_status": "unknown", "pane_id": "pane-t"}}})
        if operation == ("agent", "get"):
            return json.dumps({"result": {"agent": {
                "name": f"{self.agent}-t", "pane_id": "pane-t", "agent_status": self._state(), "interactive_ready": True,
            }}})
        if operation == ("agent", "wait"):
            wanted = set(command[command.index("--until") + 1 :: 2])
            state = self._state()
            if state not in wanted:
                raise HerdrRuntimeError('{"error":{"code":"timeout"}}')
            return json.dumps({"result": {"agent": {"agent_status": state}}})
        if operation == ("agent", "prompt"):
            if self.prompt == "stalled":
                raise HerdrRuntimeError('{"error":{"code":"agent_prompt_stalled"}}')
            return ""
        if operation == ("agent", "read"):
            return "turn output\n"
        if operation == ("pane", "send-keys"):
            return ""
        if operation == ("pane", "process-info"):
            children = [{"pid": 11, "name": self.agent}] if self.foreground else []
            return json.dumps({"process_info": {"shell_pid": 10, "foreground_processes": [{"pid": 10, "name": "bash"}, *children]}})
        raise AssertionError(command)


@pytest.mark.parametrize("agent", ["codex", "claude"])
def test_a_normal_prompt_return_completes_the_turn_and_a_later_idle_read_keeps_it(agent):
    runner = TurnHerdr(agent, states=("idle",))
    runtime = HerdrRuntime(runner=runner)
    session = runtime.start(attempt_id=f"ATTEMPT-TURN-{agent}", agent=agent)

    sent = runtime.send(session.session_id, "fix it")
    assert sent.status is RuntimeStatus.COMPLETED
    assert sent.output == ("turn output",)
    # Herdr keeps reporting ``idle`` (never ``done``) for the finished turn.
    assert runtime.status(session.session_id).status is RuntimeStatus.COMPLETED
    assert runtime.status(session.session_id).status is RuntimeStatus.COMPLETED
    assert not any(c[1:3] == ("pane", "send-keys") for c in runner.calls)


@pytest.mark.parametrize("agent", ["codex", "claude"])
def test_a_stalled_prompt_with_a_foreground_child_stays_running(agent):
    # The pane never reports ``working`` after the prompt, so no turn is proven.
    runner = TurnHerdr(agent, prompt="stalled", states=("idle",))
    runtime = HerdrRuntime(runner=runner)
    session = runtime.start(attempt_id=f"ATTEMPT-STALL-{agent}", agent=agent)

    assert runtime.send(session.session_id, "fix it").status is RuntimeStatus.RUNNING
    assert runtime.status(session.session_id).status is RuntimeStatus.RUNNING


def test_a_stalled_prompt_without_a_foreground_child_still_fails():
    runtime = HerdrRuntime(runner=TurnHerdr("claude", prompt="stalled", foreground=False))
    session = runtime.start(attempt_id="ATTEMPT-STALL-GONE", agent="claude")
    with pytest.raises(HerdrRuntimeError, match="agent_prompt_stalled"):
        runtime.send(session.session_id, "fix it")


def test_a_working_read_after_a_completed_turn_reports_running_until_idle_again():
    runner = TurnHerdr("claude", states=("idle",))
    runtime = HerdrRuntime(runner=runner)
    session = runtime.start(attempt_id="ATTEMPT-TURN-AGAIN", agent="claude")
    assert runtime.send(session.session_id, "fix it").status is RuntimeStatus.COMPLETED
    runner.states = ["working"]
    assert runtime.status(session.session_id).status is RuntimeStatus.RUNNING
    # SDF sent no new prompt, so a return to idle is still the same finished turn.
    runner.states = ["idle"]
    assert runtime.status(session.session_id).status is RuntimeStatus.COMPLETED


def test_codex_swallowed_enter_is_resubmitted_until_the_turn_runs_and_then_completes():
    # Codex 0.157 can swallow the submitting Enter: Herdr reports a stall while
    # the pane stays idle.  The runtime presses Enter (never retyping the text)
    # until the turn is seen working, then waits for it to return to idle.
    runner = TurnHerdr("codex", prompt="stalled", states=("idle", "idle", "idle", "working", "idle"))
    runtime = HerdrRuntime(runner=runner)
    session = runtime.start(attempt_id="ATTEMPT-ENTER", agent="codex")

    assert runtime.send(session.session_id, "fix it").status is RuntimeStatus.COMPLETED
    assert runtime.status(session.session_id).status is RuntimeStatus.COMPLETED
    presses = [c for c in runner.calls if c[1:3] == ("pane", "send-keys")]
    assert presses and all(c[-2:] == ("pane-t", "Enter") for c in presses)
    assert sum(c[1:3] == ("agent", "prompt") for c in runner.calls) == 1


def test_codex_resubmit_gives_up_as_running_when_the_turn_never_starts():
    runner = TurnHerdr("codex", prompt="stalled", states=("idle",))
    runtime = HerdrRuntime(runner=runner)
    session = runtime.start(attempt_id="ATTEMPT-ENTER-NEVER", agent="codex")

    assert runtime.send(session.session_id, "fix it").status is RuntimeStatus.RUNNING
    assert sum(c[1:3] == ("pane", "send-keys") for c in runner.calls) == 3


def test_claude_stall_is_never_resubmitted():
    runner = TurnHerdr("claude", prompt="stalled", states=("idle", "working", "idle"))
    runtime = HerdrRuntime(runner=runner)
    session = runtime.start(attempt_id="ATTEMPT-CLAUDE-STALL", agent="claude")
    runtime.send(session.session_id, "fix it")
    assert not any(c[1:3] == ("pane", "send-keys") for c in runner.calls)


def test_send_waits_until_the_agent_is_interactive_before_prompting():
    runner = TurnHerdr("codex")
    readiness = iter([False, False, True])
    original = runner.__call__

    def call(command, timeout_ms):
        if tuple(command[1:3]) == ("agent", "get") and not any(c[1:3] == ("agent", "prompt") for c in runner.calls):
            runner.calls.append(tuple(command))
            return json.dumps({"result": {"agent": {"agent_status": "idle", "interactive_ready": next(readiness, True)}}})
        return original(command, timeout_ms)

    runtime = HerdrRuntime(runner=call, poll_interval_s=0)
    session = runtime.start(attempt_id="ATTEMPT-READY", agent="codex")
    assert runtime.send(session.session_id, "fix it").status is RuntimeStatus.COMPLETED
    gets_before_prompt = runner.calls[: next(i for i, c in enumerate(runner.calls) if c[1:3] == ("agent", "prompt"))]
    assert sum(c[1:3] == ("agent", "get") for c in gets_before_prompt) == 3


def test_send_refuses_a_blocked_agent_before_prompting():
    runner = TurnHerdr("codex", states=("blocked",))
    runtime = HerdrRuntime(runner=runner, poll_interval_s=0)
    session = runtime.start(attempt_id="ATTEMPT-BLOCKED", agent="codex")
    with pytest.raises(HerdrRuntimeError, match="blocked"):
        runtime.send(session.session_id, "fix it")
    assert not any(c[1:3] == ("agent", "prompt") for c in runner.calls)


def test_extra_agent_args_are_appended_after_the_runtime_defaults():
    runner = FakeHerdr()
    runtime = HerdrRuntime(runner=runner, agent_args={"codex": ("-c", 'model="small-model"')})
    runtime.bind_remote_workspace("ATTEMPT-ARGS", "/tmp/sdf/ATTEMPT-ARGS")
    runtime.start(attempt_id="ATTEMPT-ARGS", agent="codex")
    (start,) = [command for command, _ in runner.calls if tuple(command[1:3]) == ("agent", "start")]
    assert start[-4:] == ("-c", 'projects."/tmp/sdf/ATTEMPT-ARGS".trust_level="trusted"', "-c", 'model="small-model"')


def test_a_mid_turn_idle_flicker_is_not_the_end_of_the_turn():
    # Codex can read ``idle`` for a moment between steps of one turn; the turn
    # ends only once ``idle`` holds for the settle window.
    runner = TurnHerdr("codex", states=("idle", "working", "idle"))
    runtime = HerdrRuntime(runner=runner, poll_interval_s=0, turn_settle_s=0)
    session = runtime.start(attempt_id="ATTEMPT-FLICKER", agent="codex")

    assert runtime.send(session.session_id, "fix it").status is RuntimeStatus.COMPLETED
    prompt_at = next(i for i, c in enumerate(runner.calls) if c[1:3] == ("agent", "prompt"))
    after = [c[1:3] for c in runner.calls[prompt_at + 1 :]]
    assert ("agent", "wait") in after
    assert after.index(("agent", "wait")) < after.index(("agent", "read"))


def test_stream_falls_back_to_the_visible_screen_while_the_agent_is_working():
    def runner(command, timeout_ms):
        command = tuple(command)
        if command[1:3] == ("agent", "read"):
            if "recent-unwrapped" in command:
                raise HerdrRuntimeError('{"error":{"code":"agent_not_idle"}}')
            assert command[command.index("--source") + 1] == "visible"
            return "visible screen\n"
        return TurnHerdr("codex")(command, timeout_ms)

    runtime = HerdrRuntime(runner=runner)
    session = runtime.start(attempt_id="ATTEMPT-VISIBLE", agent="codex")
    assert runtime.stream(session.session_id) == ("visible screen",)

def _herdr_timeout(command):
    return int(command[command.index("--timeout") + 1])

@pytest.mark.parametrize("timeout_ms", [30_000, 3210])
def test_every_herdr_wait_times_out_before_the_command_that_carries_it(timeout_ms):
    # A transport timeout kills the whole E2B sandbox, so Herdr's own wait
    # bound must expire first and come back as a clean ``timeout``.
    runner = TurnHerdr("codex", prompt="stalled", states=("idle", "idle", "working", "idle"))
    runtime = HerdrRuntime(runner=runner, timeout_ms=timeout_ms, poll_interval_s=0, turn_settle_s=0)
    seen = []
    session = runtime.start(attempt_id="ATTEMPT-BOUND", agent="codex")
    runtime._runner = lambda command, limit: (seen.append((tuple(command), limit)), runner(command, limit))[1]
    runtime.send(session.session_id, "fix it")

    waits = [(c, limit) for c, limit in seen if c[1:3] in {("agent", "prompt"), ("agent", "wait")}]
    assert {c[1:3] for c, _ in waits} == {("agent", "prompt"), ("agent", "wait")}
    assert all(0 < _herdr_timeout(c) < limit for c, limit in waits)

def test_a_herdr_timeout_on_a_long_prompt_leaves_the_turn_running():
    runner = TurnHerdr("claude", states=("working",))
    original = runner.__call__

    def call(command, timeout_ms):
        if tuple(command[1:3]) == ("agent", "prompt"):
            runner.calls.append(tuple(command))
            raise HerdrRuntimeError('{"error":{"code":"timeout"}}')
        return original(command, timeout_ms)

    runtime = HerdrRuntime(runner=call)
    session = runtime.start(attempt_id="ATTEMPT-LONG-TURN", agent="claude")
    assert runtime.send(session.session_id, "fix it").status is RuntimeStatus.RUNNING
