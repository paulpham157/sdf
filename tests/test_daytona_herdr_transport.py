"""Persistent Herdr transport over the Daytona Python SDK, against a fake sandbox.

The live counterpart is tests/test_daytona_herdr_transport_live.py.
"""

from __future__ import annotations

import time
from types import SimpleNamespace

import pytest

from sdf_core.daytona_herdr_transport import DaytonaHerdrTransport, DaytonaNotFoundError
from sdf_core.herdr_runtime import HerdrRuntime, HerdrRuntimeError

ENV = {"DAYTONA_API_KEY": "<REDACTED>", "DAYTONA_TARGET": "us", "PATH": "/bin"}


class FakeSandbox:
    def __init__(self, outputs=None, error=None, exit_code=0):
        self.id = "sbx-daytona-fake-001"
        self.runs = []
        self.deletes = []
        self._outputs = outputs or (lambda cmd: '{"result":{}}')
        self._error = error
        self._exit_code = exit_code
        self.process = SimpleNamespace(exec=self._exec)
        self.fs = FakeFilesystem()

    def _exec(self, cmd, **kwargs):
        self.runs.append((cmd, kwargs))
        if self._error is not None:
            raise self._error
        return SimpleNamespace(result=self._outputs(cmd), exit_code=self._exit_code, stderr="")

    def delete(self, **kwargs):
        self.deletes.append(kwargs)
        return True


class FakeFilesystem:
    """In-memory Daytona filesystem: absolute path -> bytes, plus explicit dirs."""

    def __init__(self):
        self.files: dict[str, bytes] = {}
        self.dirs: set[str] = set()
        self.symlinks: set[str] = set()
        self.calls: list[tuple] = []

    def _parents(self, path: str) -> None:
        parts = path.strip("/").split("/")[:-1]
        for i in range(1, len(parts) + 1):
            self.dirs.add("/" + "/".join(parts[:i]))

    def upload_files(self, files, **kwargs):
        destinations = []
        for entry in files:
            destination = entry["destination"] if isinstance(entry, dict) else entry.destination
            source = entry["source"] if isinstance(entry, dict) else entry.source
            destinations.append(destination)
            data = source.encode() if isinstance(source, str) else bytes(source)
            self.files[destination] = data
            self._parents(destination)
        self.calls.append(("upload_files", destinations, kwargs))

    def create_folder(self, path, mode="755", **kwargs):
        self.calls.append(("create_folder", path, mode, kwargs))
        self._parents(path + "/x")
        return True

    def delete_file(self, path, recursive=False, **kwargs):
        self.calls.append(("delete_file", path, recursive, kwargs))
        prefix = path.rstrip("/") + "/"
        if path not in self.dirs and path not in self.files:
            raise DaytonaNotFoundError(f"path '{path}' does not exist")
        self.files = {k: v for k, v in self.files.items() if k != path and not k.startswith(prefix)}
        self.dirs = {d for d in self.dirs if d != path and not d.startswith(prefix)}

    def list_files(self, path, **kwargs):
        self.calls.append(("list_files", path, kwargs))
        if path not in self.dirs:
            raise DaytonaNotFoundError(f"path '{path}' does not exist")
        prefix = path.rstrip("/") + "/"
        entries = []
        for d in sorted(self.dirs):
            if d.startswith(prefix) and "/" not in d[len(prefix) :]:
                entries.append(SimpleNamespace(name=d.rsplit("/", 1)[1], is_dir=True, is_symlink=False))
        for f in sorted(self.files):
            if f.startswith(prefix) and "/" not in f[len(prefix) :]:
                entries.append(
                    SimpleNamespace(
                        name=f.rsplit("/", 1)[1],
                        is_dir=False,
                        is_symlink=f in self.symlinks,
                    )
                )
        return entries

    def download_file(self, path, **kwargs):
        self.calls.append(("download_file", path, kwargs))
        return self.files[path]


class FakeFactory:
    def __init__(self, sandbox=None):
        self.sandbox = sandbox or FakeSandbox()
        self.creates = []

    def __call__(self, **kwargs):
        self.creates.append(kwargs)
        return self.sandbox


def _transport(factory, **kwargs):
    return DaytonaHerdrTransport(
        snapshot="sdf-herdr-agents",
        sandbox_factory=factory,
        environ=ENV,
        **kwargs,
    )


def test_one_sandbox_is_created_and_reused_for_every_command():
    factory = FakeFactory()
    transport = _transport(factory, envs={"SOME_NAME": "value"})
    runtime = HerdrRuntime(transport=transport)

    assert runtime._raw(runtime._command("agent", "read", "agent-1")) == '{"result":{}}'
    assert runtime._raw(runtime._command("agent", "read", "agent-1")) == '{"result":{}}'

    assert transport.sandbox_id == "sbx-daytona-fake-001"
    assert len(factory.creates) == 1
    create = factory.creates[0]
    assert create["snapshot"] == "sdf-herdr-agents"
    assert create["timeout"] == 900
    assert create["env_vars"] == {"SOME_NAME": "value"}
    assert len(factory.sandbox.runs) == 2
    assert factory.sandbox.runs[0][0].startswith("herdr agent read agent-1")


def test_every_command_carries_an_explicit_timeout():
    factory = FakeFactory()
    transport = _transport(factory)

    transport.run(("herdr", "--version"), 4_000)

    _, kwargs = factory.sandbox.runs[0]
    assert kwargs["timeout"] == 4


def test_command_arguments_are_shell_quoted():
    factory = FakeFactory()
    _transport(factory).run(("herdr", "pane", "send", "p1", "it's; rm -rf /"), 1000)

    assert factory.sandbox.runs[0][0] == "herdr pane send p1 'it'\"'\"'s; rm -rf /'"


def test_host_timeout_kills_the_sandbox_and_forgets_it():
    factory = FakeFactory(FakeSandbox(error=TimeoutError("context deadline exceeded")))
    transport = _transport(factory)

    with pytest.raises(HerdrRuntimeError, match="timed out"):
        transport.run(("herdr", "wait"), 1000)

    assert len(factory.sandbox.deletes) == 1
    assert transport.sandbox_id is None
    transport.close()
    assert len(factory.sandbox.deletes) == 1


def test_a_stalled_request_hits_the_host_watchdog_and_kills_the_sandbox():
    sandbox = FakeSandbox()
    sandbox.process = SimpleNamespace(exec=lambda *_, **__: time.sleep(1))
    factory = FakeFactory(sandbox)
    transport = _transport(factory, request_timeout_seconds=0.05)

    started = time.monotonic()
    with pytest.raises(HerdrRuntimeError, match="timed out"):
        transport.run(("herdr", "wait"), 50)

    assert time.monotonic() - started < 0.9
    assert len(sandbox.deletes) == 1
    assert transport.sandbox_id is None


def test_non_zero_exit_raises_with_result_tail_and_keeps_the_sandbox():
    factory = FakeFactory(FakeSandbox(outputs=lambda _cmd: "pane not found", exit_code=1))
    transport = _transport(factory)

    with pytest.raises(HerdrRuntimeError, match="pane not found"):
        transport.run(("herdr", "pane", "close", "p1"), 1000)

    assert transport.sandbox_id == "sbx-daytona-fake-001"
    assert factory.sandbox.deletes == []


def test_close_is_idempotent_deletes_the_sandbox_and_forgets_its_id():
    factory = FakeFactory()
    transport = _transport(factory)
    transport.run(("herdr", "--version"), 1000)

    transport.close()
    transport.close()

    assert len(factory.sandbox.deletes) == 1
    assert transport.sandbox_id is None


def test_close_forgets_the_id_even_when_delete_fails():
    factory = FakeFactory()

    def failing_delete(**_):
        raise RuntimeError("network down")

    factory.sandbox.delete = failing_delete
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
    transport = DaytonaHerdrTransport(snapshot="sdf-herdr-agents", environ={})

    with pytest.raises(HerdrRuntimeError, match="DAYTONA_API_KEY"):
        transport.run(("herdr", "--version"), 1000)


def test_an_existing_sandbox_id_is_reattached_not_recreated():
    factory = FakeFactory()
    connects = []

    def connector(sandbox_id, **kwargs):
        connects.append((sandbox_id, kwargs))
        return factory.sandbox

    transport = _transport(factory, sandbox_id="sbx-daytona-fake-001", sandbox_connector=connector)
    transport.run(("herdr", "--version"), 1000)
    transport.close()

    assert factory.creates == []
    assert connects[0][0] == "sbx-daytona-fake-001"
    assert len(factory.sandbox.deletes) == 1


def _fixture(tmp_path):
    fixture = tmp_path / "fixture"
    fixture.joinpath("pkg").mkdir(parents=True)
    fixture.joinpath("app.py").write_text("print('local')\n", encoding="utf-8")
    fixture.joinpath("pkg", "mod.py").write_bytes(b"\x00binary\xff")
    return fixture


def test_staging_writes_the_workspace_via_the_sdk_filesystem(tmp_path):
    factory = FakeFactory()
    transport = _transport(factory)

    remote = transport.stage_workspace("ATTEMPT-WORKSPACE", _fixture(tmp_path))

    fs = factory.sandbox.fs
    assert remote == "/tmp/sdf/ATTEMPT-WORKSPACE"
    assert fs.files == {
        "/tmp/sdf/ATTEMPT-WORKSPACE/app.py": b"print('local')\n",
        "/tmp/sdf/ATTEMPT-WORKSPACE/pkg/mod.py": b"\x00binary\xff",
    }
    assert "/tmp/sdf/ATTEMPT-WORKSPACE" in fs.dirs
    assert factory.sandbox.runs == []
    assert any(call[0] == "upload_files" for call in fs.calls)
    assert len(factory.creates) == 1


def test_staging_replaces_stale_remote_state(tmp_path):
    factory = FakeFactory()
    transport = _transport(factory)
    factory.sandbox.fs.create_folder("/tmp/sdf/ATTEMPT-WORKSPACE", "755")
    factory.sandbox.fs.upload_files(
        [SimpleNamespace(source=b"old", destination="/tmp/sdf/ATTEMPT-WORKSPACE/stale.txt")]
    )

    transport.stage_workspace("ATTEMPT-WORKSPACE", _fixture(tmp_path))

    assert "/tmp/sdf/ATTEMPT-WORKSPACE/stale.txt" not in factory.sandbox.fs.files


def test_staging_rejects_symlinks_before_provisioning(tmp_path):
    fixture = _fixture(tmp_path)
    fixture.joinpath("link").symlink_to(fixture / "app.py")
    factory = FakeFactory()

    with pytest.raises(ValueError, match="symlink"):
        _transport(factory).stage_workspace("ATTEMPT-WORKSPACE", fixture)
    assert factory.creates == []


def test_collection_replaces_the_local_workspace_with_remote_state(tmp_path):
    fixture = _fixture(tmp_path)
    factory = FakeFactory()
    transport = _transport(factory)
    transport.stage_workspace("ATTEMPT-WORKSPACE", fixture)
    fs = factory.sandbox.fs
    fs.upload_files(
        [
            SimpleNamespace(source=b"print('remote')\n", destination="/tmp/sdf/ATTEMPT-WORKSPACE/app.py"),
            SimpleNamespace(source=b"agent output", destination="/tmp/sdf/ATTEMPT-WORKSPACE/new/created.txt"),
        ]
    )
    fs.create_folder("/tmp/sdf/ATTEMPT-WORKSPACE/new", "755")
    fs.delete_file("/tmp/sdf/ATTEMPT-WORKSPACE/pkg/mod.py")
    fixture.joinpath("local-only.txt").write_text("discard", encoding="utf-8")

    transport.collect_workspace("ATTEMPT-WORKSPACE", fixture)

    assert fixture.joinpath("app.py").read_text(encoding="utf-8") == "print('remote')\n"
    assert fixture.joinpath("new", "created.txt").read_bytes() == b"agent output"
    assert not fixture.joinpath("local-only.txt").exists()
    assert not fixture.joinpath("pkg", "mod.py").exists()
    assert fixture.joinpath("pkg").is_dir()
    assert factory.sandbox.runs == []
    assert list(tmp_path.glob("sdf-daytona-collect-*")) == []


def test_collection_failure_leaves_the_local_workspace_untouched(tmp_path):
    fixture = _fixture(tmp_path)
    factory = FakeFactory()
    transport = _transport(factory)
    transport.stage_workspace("ATTEMPT-WORKSPACE", fixture)

    def failing_download(path, **kwargs):
        raise DaytonaNotFoundError("gone")

    factory.sandbox.fs.download_file = failing_download

    with pytest.raises(HerdrRuntimeError, match="collection failed"):
        transport.collect_workspace("ATTEMPT-WORKSPACE", fixture)
    assert fixture.joinpath("app.py").read_text(encoding="utf-8") == "print('local')\n"


def test_collection_rejects_remote_symlinks(tmp_path):
    fixture = _fixture(tmp_path)
    factory = FakeFactory()
    transport = _transport(factory)
    transport.stage_workspace("ATTEMPT-WORKSPACE", fixture)
    fs = factory.sandbox.fs
    fs.upload_files([SimpleNamespace(source=b"", destination="/tmp/sdf/ATTEMPT-WORKSPACE/link")])
    fs.symlinks.add("/tmp/sdf/ATTEMPT-WORKSPACE/link")

    with pytest.raises(HerdrRuntimeError, match="unsafe"):
        transport.collect_workspace("ATTEMPT-WORKSPACE", fixture)
