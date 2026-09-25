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
    # Herdr's ``idle`` means the agent is ready/observed, not evaluator
    # acceptance or terminal task completion.
    assert updated.status is RuntimeStatus.RUNNING
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
