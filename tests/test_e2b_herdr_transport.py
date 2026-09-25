"""Persistent Herdr transport over the E2B Python SDK, against a fake sandbox.

The live counterpart is tests/test_e2b_herdr_transport_live.py.
"""

import base64
import io
import tarfile
import time
from types import SimpleNamespace

import pytest
from e2b import CommandExitException, TimeoutException

from sdf_core.e2b_herdr_transport import E2BHerdrTransport
from sdf_core.herdr_runtime import HerdrRuntime, HerdrRuntimeError

ENV = {"E2B_API_KEY": "<REDACTED>", "E2B_DOMAIN": "e2b.dev", "PATH": "/bin"}

class FakeSandbox:
    def __init__(self, outputs=None, error=None):
        self.sandbox_id = "irjxx6neqsa85eo4v5ym5"
        self.runs = []
        self.writes = {}
        self.kills = []
        self._outputs = outputs or (lambda cmd: '{"result":{}}')
        self._error = error
        self.commands = SimpleNamespace(run=self._run)
        self.files = SimpleNamespace(write=self._write)

    def _run(self, cmd, **kwargs):
        self.runs.append((cmd, kwargs))
        if self._error is not None:
            raise self._error
        return SimpleNamespace(stdout=self._outputs(cmd), stderr="", exit_code=0)

    def _write(self, path, data, **kwargs):
        self.writes[path] = (data, kwargs)

    def kill(self, **kwargs):
        self.kills.append(kwargs)
        return True

class FakeFactory:
    def __init__(self, sandbox=None):
        self.sandbox = sandbox or FakeSandbox()
        self.creates = []

    def __call__(self, **kwargs):
        self.creates.append(kwargs)
        return self.sandbox

def _transport(factory, **kwargs):
    return E2BHerdrTransport(template="herdr-codex", sandbox_factory=factory, environ=ENV, **kwargs)

def test_one_sandbox_is_created_and_reused_for_every_command():
    factory = FakeFactory()
    transport = _transport(factory, envs={"SOME_NAME": "value"})
    runtime = HerdrRuntime(transport=transport)

    assert runtime._raw(runtime._command("agent", "read", "agent-1")) == '{"result":{}}'
    assert runtime._raw(runtime._command("agent", "read", "agent-1")) == '{"result":{}}'

    assert transport.sandbox_id == "irjxx6neqsa85eo4v5ym5"
    assert len(factory.creates) == 1
    create = factory.creates[0]
    assert create["template"] == "herdr-codex"
    assert create["timeout"] == 900
    assert create["envs"] == {"SOME_NAME": "value"}
    assert create["api_key"] == "<REDACTED>"
    assert create["domain"] == "e2b.dev"
    assert len(factory.sandbox.runs) == 2
    assert factory.sandbox.runs[0][0].startswith("herdr agent read agent-1")

def test_every_command_carries_explicit_command_and_request_timeouts():
    factory = FakeFactory()
    transport = _transport(factory, request_timeout_seconds=12.5)

    transport.run(("herdr", "--version"), 4_000)

    assert factory.creates[0]["request_timeout"] == 12.5
    _, kwargs = factory.sandbox.runs[0]
    assert kwargs["timeout"] == 4.0
    assert kwargs["request_timeout"] == 12.5

def test_command_arguments_are_shell_quoted():
    factory = FakeFactory()
    _transport(factory).run(("herdr", "pane", "send", "p1", "it's; rm -rf /"), 1000)

    assert factory.sandbox.runs[0][0] == "herdr pane send p1 'it'\"'\"'s; rm -rf /'"

def test_host_timeout_kills_the_sandbox_and_forgets_it():
    factory = FakeFactory(FakeSandbox(error=TimeoutException("context deadline exceeded")))
    transport = _transport(factory)

    with pytest.raises(HerdrRuntimeError, match="timed out"):
        transport.run(("herdr", "wait"), 1000)

    assert len(factory.sandbox.kills) == 1
    assert transport.sandbox_id is None
    transport.close()
    assert len(factory.sandbox.kills) == 1

def test_a_stalled_request_hits_the_host_watchdog_and_kills_the_sandbox():
    sandbox = FakeSandbox()
    sandbox.commands = SimpleNamespace(run=lambda *_, **__: time.sleep(1))
    factory = FakeFactory(sandbox)
    transport = _transport(factory, request_timeout_seconds=0.05)

    started = time.monotonic()
    with pytest.raises(HerdrRuntimeError, match="timed out"):
        transport.run(("herdr", "wait"), 50)

    assert time.monotonic() - started < 0.9
    assert len(sandbox.kills) == 1
    assert transport.sandbox_id is None

def test_non_zero_exit_raises_with_stderr_tail_and_keeps_the_sandbox():
    error = CommandExitException(stdout="", stderr="pane not found", exit_code=1, error=None)
    factory = FakeFactory(FakeSandbox(error=error))
    transport = _transport(factory)

    with pytest.raises(HerdrRuntimeError, match="pane not found"):
        transport.run(("herdr", "pane", "close", "p1"), 1000)

    assert transport.sandbox_id == "irjxx6neqsa85eo4v5ym5"
    assert factory.sandbox.kills == []

def test_close_is_idempotent_kills_the_sandbox_and_forgets_its_id():
    factory = FakeFactory()
    transport = _transport(factory)
    transport.run(("herdr", "--version"), 1000)

    transport.close()
    transport.close()

    assert len(factory.sandbox.kills) == 1
    assert transport.sandbox_id is None

def test_close_forgets_the_id_even_when_kill_fails():
    factory = FakeFactory()

    def failing_kill(**_):
        raise RuntimeError("network down")

    factory.sandbox.kill = failing_kill
    transport = _transport(factory)
    transport.run(("herdr", "--version"), 1000)

    with pytest.raises(RuntimeError):
        transport.close()
    assert transport.sandbox_id is None

def test_close_without_a_sandbox_does_not_provision_one():
    factory = FakeFactory()
    _transport(factory).close()
    assert factory.creates == []

def test_missing_api_key_fails_before_provisioning():
    factory = FakeFactory()
    transport = E2BHerdrTransport(template="herdr-codex", sandbox_factory=factory, environ={})

    with pytest.raises(HerdrRuntimeError, match="E2B_API_KEY"):
        transport.run(("herdr", "--version"), 1000)
    assert factory.creates == []

def test_an_existing_sandbox_id_is_reattached_not_recreated():
    factory = FakeFactory()
    connects = []

    def connector(sandbox_id, **kwargs):
        connects.append((sandbox_id, kwargs))
        return factory.sandbox

    transport = _transport(factory, sandbox_id="irjxx6neqsa85eo4v5ym5", sandbox_connector=connector)
    transport.run(("herdr", "--version"), 1000)
    transport.close()

    assert factory.creates == []
    assert connects[0][0] == "irjxx6neqsa85eo4v5ym5"
    assert connects[0][1]["api_key"] == "<REDACTED>"
    assert len(factory.sandbox.kills) == 1

def test_stages_and_collects_a_bounded_attempt_workspace(tmp_path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    fixture.joinpath("app.py").write_text("print('local')\n", encoding="utf-8")
    archive = io.BytesIO()
    with tarfile.open(fileobj=archive, mode="w:gz") as tar:
        payload = b"print('remote')\n"
        member = tarfile.TarInfo("app.py")
        member.size = len(payload)
        tar.addfile(member, io.BytesIO(payload))
    collected = base64.b64encode(archive.getvalue()).decode("ascii")
    factory = FakeFactory(FakeSandbox(outputs=lambda cmd: collected if "tar -C" in cmd and "-czf" in cmd else ""))
    transport = _transport(factory)

    remote = transport.stage_workspace("ATTEMPT-WORKSPACE", fixture)
    fixture.joinpath("local-only.txt").write_text("discard", encoding="utf-8")
    transport.collect_workspace("ATTEMPT-WORKSPACE", fixture)

    assert remote == "/tmp/sdf/ATTEMPT-WORKSPACE"
    assert fixture.joinpath("app.py").read_text(encoding="utf-8") == "print('remote')\n"
    assert not fixture.joinpath("local-only.txt").exists()
    (staged_path, (staged, _)), = factory.sandbox.writes.items()
    with tarfile.open(fileobj=io.BytesIO(staged), mode="r:gz") as tar:
        assert tar.getnames() == ["app.py"]
    assert any(staged_path in cmd and "tar -xzf" in cmd for cmd, _ in factory.sandbox.runs)
    assert len(factory.creates) == 1
