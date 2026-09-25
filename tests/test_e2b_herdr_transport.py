"""Persistent Herdr transport over the E2B Python SDK, against a fake sandbox.

The live counterpart is tests/test_e2b_herdr_transport_live.py.
"""

import time
from types import SimpleNamespace

import pytest
from e2b import CommandExitException, FileType, NotFoundException, TimeoutException

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
        self.fs = FakeFilesystem()
        self.files = self.fs

    def _run(self, cmd, **kwargs):
        self.runs.append((cmd, kwargs))
        if self._error is not None:
            raise self._error
        return SimpleNamespace(stdout=self._outputs(cmd), stderr="", exit_code=0)

    def kill(self, **kwargs):
        self.kills.append(kwargs)
        return True

class FakeFilesystem:
    """In-memory envd filesystem: absolute path -> bytes, plus explicit dirs."""

    def __init__(self):
        self.files = {}
        self.dirs = set()
        self.symlinks = set()
        self.calls = []

    def _parents(self, path):
        parts = path.strip("/").split("/")[:-1]
        for i in range(1, len(parts) + 1):
            self.dirs.add("/" + "/".join(parts[:i]))

    def write_files(self, files, **kwargs):
        self.calls.append(("write_files", [f["path"] for f in files], kwargs))
        for entry in files:
            data = entry["data"]
            self.files[entry["path"]] = data.encode() if isinstance(data, str) else bytes(data)
            self._parents(entry["path"])

    def make_dir(self, path, **kwargs):
        self.calls.append(("make_dir", path, kwargs))
        self._parents(path + "/x")
        return True

    def remove(self, path, **kwargs):
        self.calls.append(("remove", path, kwargs))
        prefix = path.rstrip("/") + "/"
        if path not in self.dirs and path not in self.files:
            raise NotFoundException(f"path '{path}' does not exist")
        self.files = {k: v for k, v in self.files.items() if k != path and not k.startswith(prefix)}
        self.dirs = {d for d in self.dirs if d != path and not d.startswith(prefix)}

    def list(self, path, depth=1, **kwargs):
        self.calls.append(("list", path, depth, kwargs))
        if path not in self.dirs:
            raise NotFoundException(f"path '{path}' does not exist")
        prefix = path.rstrip("/") + "/"
        entries = []
        for d in sorted(self.dirs):
            if d.startswith(prefix):
                entries.append(SimpleNamespace(path=d, name=d.rsplit("/", 1)[1], type=FileType.DIR))
        for f in sorted(self.files):
            if f.startswith(prefix):
                kind = FileType.SYMLINK if f in self.symlinks else FileType.FILE
                entries.append(SimpleNamespace(path=f, name=f.rsplit("/", 1)[1], type=kind))
        return entries

    def read(self, path, format="text", **kwargs):
        self.calls.append(("read", path, format, kwargs))
        data = self.files[path]
        return data if format == "bytes" else data.decode()


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

def _fixture(tmp_path):
    fixture = tmp_path / "fixture"
    fixture.joinpath("pkg").mkdir(parents=True)
    fixture.joinpath("app.py").write_text("print('local')\n", encoding="utf-8")
    fixture.joinpath("pkg", "mod.py").write_bytes(b"\x00binary\xff")
    return fixture


def test_staging_writes_the_workspace_into_the_attempt_directory_via_the_sdk_filesystem(tmp_path):
    factory = FakeFactory()
    transport = _transport(factory, request_timeout_seconds=7.0)

    remote = transport.stage_workspace("ATTEMPT-WORKSPACE", _fixture(tmp_path))

    fs = factory.sandbox.fs
    assert remote == "/tmp/sdf/ATTEMPT-WORKSPACE"
    assert fs.files == {
        "/tmp/sdf/ATTEMPT-WORKSPACE/app.py": b"print('local')\n",
        "/tmp/sdf/ATTEMPT-WORKSPACE/pkg/mod.py": b"\x00binary\xff",
    }
    assert "/tmp/sdf/ATTEMPT-WORKSPACE" in fs.dirs
    assert factory.sandbox.runs == []  # no shell round-trip for staging
    assert all(call[-1]["request_timeout"] == 7.0 for call in fs.calls)
    assert len(factory.creates) == 1


def test_staging_replaces_stale_remote_state_from_a_previous_stage(tmp_path):
    factory = FakeFactory()
    transport = _transport(factory)
    factory.sandbox.fs.write_files([{"path": "/tmp/sdf/ATTEMPT-WORKSPACE/stale.txt", "data": b"old"}])

    transport.stage_workspace("ATTEMPT-WORKSPACE", _fixture(tmp_path))

    assert "/tmp/sdf/ATTEMPT-WORKSPACE/stale.txt" not in factory.sandbox.fs.files


def test_staging_keeps_empty_directories(tmp_path):
    fixture = _fixture(tmp_path)
    fixture.joinpath("empty").mkdir()
    factory = FakeFactory()

    _transport(factory).stage_workspace("ATTEMPT-WORKSPACE", fixture)

    assert "/tmp/sdf/ATTEMPT-WORKSPACE/empty" in factory.sandbox.fs.dirs


def test_staging_rejects_symlinks_before_provisioning(tmp_path):
    fixture = _fixture(tmp_path)
    fixture.joinpath("link").symlink_to(fixture / "app.py")
    factory = FakeFactory()

    with pytest.raises(ValueError, match="symlink"):
        _transport(factory).stage_workspace("ATTEMPT-WORKSPACE", fixture)
    assert factory.creates == []


def test_staging_rejects_a_workspace_over_the_transfer_limit(tmp_path, monkeypatch):
    monkeypatch.setattr("sdf_core.e2b_herdr_transport._TRANSFER_LIMIT", 8)
    factory = FakeFactory()

    with pytest.raises(ValueError, match="limit"):
        _transport(factory).stage_workspace("ATTEMPT-WORKSPACE", _fixture(tmp_path))
    assert factory.creates == []


def test_staging_rejects_unsafe_attempt_ids(tmp_path):
    with pytest.raises(ValueError, match="attempt_id"):
        _transport(FakeFactory()).stage_workspace("../escape", _fixture(tmp_path))


def test_collection_replaces_the_local_workspace_with_remote_state(tmp_path):
    fixture = _fixture(tmp_path)
    factory = FakeFactory()
    transport = _transport(factory)
    transport.stage_workspace("ATTEMPT-WORKSPACE", fixture)
    fs = factory.sandbox.fs
    fs.write_files([
        {"path": "/tmp/sdf/ATTEMPT-WORKSPACE/app.py", "data": b"print('remote')\n"},
        {"path": "/tmp/sdf/ATTEMPT-WORKSPACE/new/created.txt", "data": b"agent output"},
    ])
    fs.remove("/tmp/sdf/ATTEMPT-WORKSPACE/pkg/mod.py")
    fixture.joinpath("local-only.txt").write_text("discard", encoding="utf-8")

    transport.collect_workspace("ATTEMPT-WORKSPACE", fixture)

    assert fixture.joinpath("app.py").read_text(encoding="utf-8") == "print('remote')\n"
    assert fixture.joinpath("new", "created.txt").read_bytes() == b"agent output"
    assert not fixture.joinpath("local-only.txt").exists()
    assert not fixture.joinpath("pkg", "mod.py").exists()
    assert fixture.joinpath("pkg").is_dir()
    assert factory.sandbox.runs == []  # no shell/base64 round-trip for collection
    assert any(call[0] == "read" and call[2] == "bytes" for call in fs.calls)
    assert list(tmp_path.glob("sdf-e2b-collect-*")) == []


def test_collection_failure_leaves_the_local_workspace_untouched(tmp_path):
    fixture = _fixture(tmp_path)
    factory = FakeFactory()
    transport = _transport(factory)
    transport.stage_workspace("ATTEMPT-WORKSPACE", fixture)

    def failing_read(path, **kwargs):
        raise NotFoundException("gone")

    factory.sandbox.fs.read = failing_read

    with pytest.raises(HerdrRuntimeError, match="collection failed"):
        transport.collect_workspace("ATTEMPT-WORKSPACE", fixture)
    assert fixture.joinpath("app.py").read_text(encoding="utf-8") == "print('local')\n"
    assert list(tmp_path.glob("sdf-e2b-collect-*")) == []


def test_collection_of_a_missing_remote_workspace_raises(tmp_path):
    fixture = _fixture(tmp_path)

    with pytest.raises(HerdrRuntimeError, match="collection failed"):
        _transport(FakeFactory()).collect_workspace("ATTEMPT-WORKSPACE", fixture)
    assert fixture.joinpath("app.py").exists()


def test_collection_rejects_remote_symlinks(tmp_path):
    fixture = _fixture(tmp_path)
    factory = FakeFactory()
    transport = _transport(factory)
    transport.stage_workspace("ATTEMPT-WORKSPACE", fixture)
    fs = factory.sandbox.fs
    fs.write_files([{"path": "/tmp/sdf/ATTEMPT-WORKSPACE/link", "data": b""}])
    fs.symlinks.add("/tmp/sdf/ATTEMPT-WORKSPACE/link")

    with pytest.raises(HerdrRuntimeError, match="unsafe"):
        transport.collect_workspace("ATTEMPT-WORKSPACE", fixture)
    assert fixture.joinpath("app.py").read_text(encoding="utf-8") == "print('local')\n"


def test_collection_rejects_entries_outside_the_attempt_directory(tmp_path):
    fixture = _fixture(tmp_path)
    factory = FakeFactory()
    transport = _transport(factory)
    transport.stage_workspace("ATTEMPT-WORKSPACE", fixture)
    original = factory.sandbox.fs.list

    def escaping_list(path, depth=1, **kwargs):
        entries = original(path, depth, **kwargs)
        return [*entries, SimpleNamespace(path="/etc/passwd", name="passwd", type=FileType.FILE)]

    factory.sandbox.fs.list = escaping_list

    with pytest.raises(HerdrRuntimeError, match="unsafe"):
        transport.collect_workspace("ATTEMPT-WORKSPACE", fixture)


def test_collection_rejects_remote_state_over_the_transfer_limit(tmp_path, monkeypatch):
    fixture = _fixture(tmp_path)
    factory = FakeFactory()
    transport = _transport(factory)
    transport.stage_workspace("ATTEMPT-WORKSPACE", fixture)
    monkeypatch.setattr("sdf_core.e2b_herdr_transport._TRANSFER_LIMIT", 8)

    with pytest.raises(HerdrRuntimeError, match="limit"):
        transport.collect_workspace("ATTEMPT-WORKSPACE", fixture)
    assert fixture.joinpath("app.py").read_text(encoding="utf-8") == "print('local')\n"


def test_the_persistent_transport_has_no_cli_runner_seam():
    import inspect

    import sdf_core.e2b_herdr_transport as module

    source = inspect.getsource(module)
    assert "subprocess" not in source
    assert "runner" not in inspect.signature(E2BHerdrTransport).parameters
    assert "tarfile" not in source and "base64" not in source


def test_a_runtime_attempt_round_trips_through_the_sandbox_and_artifacts_are_pulled_home(tmp_path):
    from sdf_core.adapter import RuntimeAgentAdapter
    from sdf_core.artifacts import ArtifactStore

    fixture = _fixture(tmp_path)
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    workspace.joinpath("app.py").write_text("print('local')\n", encoding="utf-8")
    workspace.joinpath("local-only.txt").write_text("discard", encoding="utf-8")
    fixture.joinpath("pkg", "mod.py").unlink()
    fixture.joinpath("local-only.txt").write_text("discard", encoding="utf-8")
    sandbox = FakeSandbox()
    remote = "/tmp/sdf/ATTEMPT-E2E"

    def herdr(cmd):
        args = cmd.split()
        operation = tuple(args[1:3])
        if args[1] == "--version":
            return "herdr 0.0.0"
        if operation == ("workspace", "create"):
            assert args[-2:] == ["--cwd", remote]
            return '{"workspaceId":"ws-1","paneId":"pane-1"}'
        if operation == ("agent", "start"):
            return '{"agentSessionId":"agent-1","status":"working"}'
        if operation == ("agent", "prompt"):
            # The agent edits the staged remote Attempt directory.
            sandbox.fs.write_files([{"path": f"{remote}/app.py", "data": b"print('remote')\n"}])
            sandbox.fs.remove(f"{remote}/local-only.txt")
            return '{"status":"working"}'
        if operation == ("agent", "read"):
            return '{"output":"edited"}'
        if operation == ("agent", "get"):
            return '{"status":"done"}'
        if operation == ("pane", "close"):
            return '{"type":"ok"}'
        if operation == ("pane", "process-info"):
            return '{"process_info":{"shell_pid":10,"foreground_processes":[{"pid":10,"name":"sh"}]}}'
        raise AssertionError(cmd)

    sandbox._outputs = herdr
    factory = FakeFactory(sandbox)
    transport = _transport(factory)

    result = RuntimeAgentAdapter(HerdrRuntime(transport=transport), agent="codex").run(
        attempt_id="ATTEMPT-E2E", workspace=workspace, instructions="update app"
    )
    diff = ArtifactStore(tmp_path / "artifacts").capture_diff(
        artifact_id="ATTEMPT-E2E-DIFF", before=fixture, after=workspace, files=result.changed_files
    )

    assert result.status == "completed"
    assert result.changed_files == ("app.py", "local-only.txt")
    assert workspace.joinpath("app.py").read_text(encoding="utf-8") == "print('remote')\n"
    assert not workspace.joinpath("local-only.txt").exists()
    text = diff.path.read_text(encoding="utf-8")
    assert "+print('remote')" in text and "-discard" in text
    assert diff.path.is_relative_to(tmp_path / "artifacts")
    assert len(factory.creates) == 1
    assert sandbox.kills  # the Attempt owner closed the sandbox
