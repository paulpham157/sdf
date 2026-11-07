"""Host process exit against a provider process that outlives it.

The provider listens on localhost and is the only place that knows whether a
sandbox is still running. Recovery is allowed to mark the Attempt stopped
only after that process reports the id absent.
"""

from __future__ import annotations

import socket
import subprocess
import sys
import textwrap
import time
from collections.abc import Iterator
from contextlib import contextmanager
from pathlib import Path

from sqlalchemy import create_engine, select
from sqlalchemy.orm import sessionmaker

from sdf_core.db import AttemptRemoteSandboxRow, AttemptRow, Base, TaskRow
from sdf_core.model import utcnow
from sdf_core.remote_effects import reconcile_attempt


PROVIDER = textwrap.dedent(
    """
    import socket, sys
    state = {}
    server = socket.socket()
    server.bind(("127.0.0.1", 0))
    server.listen(8)
    sys.stdout.write(str(server.getsockname()[1]) + "\\n")
    sys.stdout.flush()
    while True:
        conn, _addr = server.accept()
        with conn:
            raw = b""
            while not raw.endswith(b"\\n"):
                chunk = conn.recv(1024)
                if not chunk:
                    break
                raw += chunk
            if not raw:
                continue
            op, _, name = raw.decode().strip().partition(" ")
            if op == "CREATE":
                state[name] = "running"
                answer = "running"
            elif op == "KILL":
                state.pop(name, None)
                answer = "killed"
            elif op == "GET":
                answer = state.get(name, "absent")
            elif op == "STOP":
                conn.sendall(b"bye\\n")
                raise SystemExit
            else:
                answer = "error"
            conn.sendall((answer + "\\n").encode())
    """
)


def _ask(port: int, command: str) -> str:
    with socket.create_connection(("127.0.0.1", port), timeout=5) as conn:
        conn.sendall((command + "\n").encode())
        data = b""
        while not data.endswith(b"\n"):
            data += conn.recv(1024)
    return data.decode().strip()


def _run_host(database: Path, port: int, attempt_id: str, ready: Path, *, identify: bool) -> None:
    """Start the host and SIGKILL it after the durable write. No cleanup runs."""

    script = textwrap.dedent(
        f"""
        import socket, time
        from pathlib import Path
        from sqlalchemy import create_engine
        from sqlalchemy.orm import sessionmaker
        from sdf_core.recovery import note_remote_effect_uncertain
        from sdf_core.remote_effects import identify_remote_sandbox
        engine = create_engine("sqlite:///{database}")
        Session = sessionmaker(engine, expire_on_commit=False)
        db = Session()
        effect_id = note_remote_effect_uncertain(db, {attempt_id!r}, provider="e2b")
        db.close()
        with socket.create_connection(("127.0.0.1", {port}), timeout=5) as conn:
            conn.sendall(b"CREATE sbx-orphan\\n")
            conn.recv(64)
        if {identify}:
            db = Session()
            identify_remote_sandbox(db, effect_id, "sbx-orphan")
            db.close()
        Path({str(ready)!r}).write_text("ready")
        time.sleep(30)
        """
    )
    process = subprocess.Popen([sys.executable, "-c", script])
    deadline = time.monotonic() + 10
    while not ready.exists():
        if process.poll() is not None:
            raise AssertionError(f"host exited before the ready marker: {process.returncode}")
        if time.monotonic() > deadline:
            process.kill()
            raise AssertionError("host did not record the sandbox before the deadline")
        time.sleep(0.05)
    process.kill()
    process.wait(timeout=5)
    assert process.returncode < 0


def _provider() -> tuple[subprocess.Popen[str], int]:
    process = subprocess.Popen([sys.executable, "-c", PROVIDER], stdout=subprocess.PIPE, text=True)
    assert process.stdout is not None
    port = int(process.stdout.readline().strip())
    return process, port


def _database(tmp_path: Path):
    database = tmp_path / "recovery.sqlite"
    engine = create_engine(f"sqlite:///{database}")
    Base.metadata.create_all(engine)
    factory = sessionmaker(engine, expire_on_commit=False)
    db = factory()
    db.add(TaskRow(
        id="TASK-PROCESS", title="process", status="running", idempotency_key="task-process",
        acceptance_criteria=[], created_at=utcnow(),
    ))
    db.add(AttemptRow(
        id="ATTEMPT-PROCESS", task_id="TASK-PROCESS", status="running", dispatch_key="dispatch-process",
        agent="native", created_at=utcnow(),
    ))
    db.commit()
    db.close()
    return database, factory


@contextmanager
def _running_provider(tmp_path: Path) -> Iterator[tuple[Path, object, int]]:
    """Database plus a provider process. The provider is stopped on the way out."""

    database, factory = _database(tmp_path)
    provider, port = _provider()
    try:
        yield database, factory, port
    finally:
        _ask(port, "STOP")
        provider.wait(timeout=5)


def _reconcile(factory, port: int):
    db = factory()
    try:
        return reconcile_attempt(
            db, "ATTEMPT-PROCESS",
            kill=lambda _provider, sandbox_id: _ask(port, f"KILL {sandbox_id}"),
            absent=lambda _provider, sandbox_id: _ask(port, f"GET {sandbox_id}") == "absent",
        ), db.get(AttemptRow, "ATTEMPT-PROCESS").remote_effect
    finally:
        db.close()


def test_host_exit_before_the_id_is_recorded_cannot_be_reconciled(tmp_path: Path) -> None:
    with _running_provider(tmp_path) as (database, factory, port):
        _run_host(database, port, "ATTEMPT-PROCESS", tmp_path / "ready-unknown", identify=False)
        assert _ask(port, "GET sbx-orphan") == "running"
        result, effect = _reconcile(factory, port)
        assert result.resolved is False and result.blocked == "unknown_create"
        assert effect == "uncertain"
        assert _ask(port, "GET sbx-orphan") == "running"
        db = factory()
        try:
            row = db.scalars(select(AttemptRemoteSandboxRow)).one()
            assert row.phase == "creating" and row.sandbox_id is None
        finally:
            db.close()


def test_host_exit_after_the_id_is_recorded_resolves_only_when_the_provider_reports_absent(
    tmp_path: Path,
) -> None:
    with _running_provider(tmp_path) as (database, factory, port):
        _run_host(database, port, "ATTEMPT-PROCESS", tmp_path / "ready-known", identify=True)
        assert _ask(port, "GET sbx-orphan") == "running"
        result, effect = _reconcile(factory, port)
        assert result.resolved is True and result.blocked is None
        assert effect == "stopped"
        assert _ask(port, "GET sbx-orphan") == "absent"
