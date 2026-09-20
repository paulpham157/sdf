from sdf_core.e2b_herdr_transport import E2BHerdrTransport
from sdf_core.herdr_runtime import HerdrRuntime


def test_e2b_herdr_transport_keeps_one_sandbox_for_multiple_commands():
    calls = []

    def runner(command, timeout_ms, env):
        calls.append((tuple(command), timeout_ms, dict(env)))
        if tuple(command[:4]) == ("e2b", "sandbox", "create", "--detach"):
            return "sandbox created: irjxx6neqsa85eo4v5ym5\n"
        if command[1:3] == ("sandbox", "exec"):
            return "{\"result\":{}}"
        if command[1:3] == ("sandbox", "kill"):
            return "killed\n"
        raise AssertionError(command)

    transport = E2BHerdrTransport(
        template="herdr-codex",
        runner=runner,
        environ={"E2B_API_KEY": "<REDACTED>", "PATH": "/bin"},
    )
    runtime = HerdrRuntime(transport=transport)
    assert runtime._raw(runtime._command("agent", "read", "agent-1")) == "{\"result\":{}}"
    assert runtime._raw(runtime._command("agent", "read", "agent-1")) == "{\"result\":{}}"
    assert transport.sandbox_id == "irjxx6neqsa85eo4v5ym5"
    assert sum(command[1:3] == ("sandbox", "create") for command, _, _ in calls) == 1
    assert sum(command[1:3] == ("sandbox", "exec") for command, _, _ in calls) == 2
    transport.close()
    assert transport.sandbox_id is None
    assert sum(command[1:3] == ("sandbox", "kill") for command, _, _ in calls) == 1


def test_e2b_herdr_transport_requires_api_key_before_provisioning():
    transport = E2BHerdrTransport(template="herdr-codex", runner=lambda *_: "", environ={})
    try:
        transport.run(("herdr", "--version"), 1000)
    except Exception as exc:
        assert "E2B_API_KEY" in str(exc)
    else:  # pragma: no cover
        raise AssertionError("missing API key must fail closed")
