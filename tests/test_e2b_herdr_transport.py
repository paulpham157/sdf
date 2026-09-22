import base64
import io
import tarfile

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
        environ={"E2B_API_KEY": "<REDACTED>", "E2B_DOMAIN": "e2b.dev", "PATH": "/bin"},
    )
    runtime = HerdrRuntime(transport=transport)
    assert runtime._raw(runtime._command("agent", "read", "agent-1")) == "{\"result\":{}}"
    assert runtime._raw(runtime._command("agent", "read", "agent-1")) == "{\"result\":{}}"
    assert transport.sandbox_id == "irjxx6neqsa85eo4v5ym5"
    assert sum(command[1:3] == ("sandbox", "create") for command, _, _ in calls) == 1
    assert sum(command[1:3] == ("sandbox", "exec") for command, _, _ in calls) == 2
    exec_command = next(command for command, _, _ in calls if command[1:3] == ("sandbox", "exec"))
    assert exec_command[4:6] == ("--", "herdr")
    transport.close()
    assert transport.sandbox_id is None
    assert sum(command[1:3] == ("sandbox", "kill") for command, _, _ in calls) == 1
    assert all(env["E2B_DOMAIN"] == "e2b.dev" for _, _, env in calls)


def test_e2b_herdr_transport_requires_api_key_before_provisioning():
    transport = E2BHerdrTransport(template="herdr-codex", runner=lambda *_: "", environ={})
    try:
        transport.run(("herdr", "--version"), 1000)
    except Exception as exc:
        assert "E2B_API_KEY" in str(exc)
    else:  # pragma: no cover
        raise AssertionError("missing API key must fail closed")


def test_e2b_herdr_transport_stages_and_collects_a_bounded_attempt_workspace(tmp_path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    fixture.joinpath("app.py").write_text("print('local')\n", encoding="utf-8")
    calls = []
    archive = io.BytesIO()
    with tarfile.open(fileobj=archive, mode="w:gz") as tar:
        payload = b"print('remote')\n"
        member = tarfile.TarInfo("app.py")
        member.size = len(payload)
        tar.addfile(member, io.BytesIO(payload))
    collected = base64.b64encode(archive.getvalue()).decode("ascii")

    def runner(command, timeout_ms, env):
        calls.append(tuple(command))
        if tuple(command[:4]) == ("e2b", "sandbox", "create", "--detach"):
            return "sandbox created: irjxx6neqsa85eo4v5ym5\n"
        if command[1:3] == ("sandbox", "exec"):
            return collected if any("tar -C" in str(part) for part in command) else ""
        if command[1:3] == ("sandbox", "kill"):
            return "killed\n"
        raise AssertionError(command)

    transport = E2BHerdrTransport(
        template="herdr-codex", runner=runner, environ={"E2B_API_KEY": "<REDACTED>", "PATH": "/bin"}
    )
    remote = transport.stage_workspace("ATTEMPT-WORKSPACE", fixture)
    fixture.joinpath("local-only.txt").write_text("discard", encoding="utf-8")
    transport.collect_workspace("ATTEMPT-WORKSPACE", fixture)

    assert remote == "/tmp/sdf/ATTEMPT-WORKSPACE"
    assert fixture.joinpath("app.py").read_text(encoding="utf-8") == "print('remote')\n"
    assert not fixture.joinpath("local-only.txt").exists()
    assert any(any("base64 -d" in str(part) for part in command) for command in calls)
    assert any(any("tar -C" in str(part) for part in command) for command in calls)
