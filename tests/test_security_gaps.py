"""Security boundaries for a hostile agent turn and for stored Evidence.

G2. A child started by ``FixtureSandbox`` (the default path, not only
``require_containment=True``) and a child the live adapter's ``send`` starts
with its cwd inside the Attempt workspace must not write outside that
workspace or observe a credential-shaped variable the Attempt did not scope.
``require_containment=True`` with no backend still refuses to start.

G4. Rewriting an Evidence row's verdict and the sha256 stored beside the
artifact bytes must make ``verify_attempt_revision`` fail. The content MAC
(ADR 0043) is the commitment that rewrite does not update.
"""

from __future__ import annotations

import hashlib
import json
import os
import subprocess
import sys
from pathlib import Path

import pytest
from sqlalchemy import text
from sqlalchemy.orm import sessionmaker

from sdf_core.adapter import FakeNativeAdapter, RuntimeAgentAdapter
from sdf_core.containment import ContainmentUnavailable
from sdf_core.db import (
    ArtifactRow,
    AttemptRow,
    AuthAuditRow,
    Base,
    DecisionEdgeRow,
    EvidenceRow,
    GraphNodeRow,
    RuntimeEventRow,
    TaskRow,
    make_engine,
)
from sdf_core.execution import ExecutionService
from sdf_core.model import utcnow
from sdf_core.policy import ActionRequest, AllowlistPolicy
from sdf_core.revisions import verify_attempt_revision
from sdf_core.runtime import RuntimeSession, RuntimeStatus
from sdf_core.sandbox import FixtureSandbox
from sdf_core.tools import ToolExecutionStatus, ToolProxy

_ATTEMPT = "ATTEMPT-G2"
_IN_SCOPE = "g2-in-scope-marker"
_OUT_OF_SCOPE = "g2-out-of-scope-marker"
_CHECK = {"tests pass": [["python", "-c", "print('ok')"]]}


class _DeniedExecutor:
    def execute(self, request: ActionRequest) -> str:
        raise AssertionError(f"denied action reached the executor: {request.tool}.{request.action}")


class _UnconstrainedAgent:
    """A live in-sandbox agent: shell and the sandbox environment, no Tool Proxy.

    ``RuntimeAgentAdapter`` is the seam ``LiveRunAdapter.run`` uses. ``send``
    does what that agent can do after it starts: run a child process and read
    every variable the sandbox was given (ADR 0007 injects the Agent Credential
    as environment variables). It does not build an ``ActionRequest``.
    """

    def __init__(self, *, escape: Path, leak: Path, sandbox_env: dict[str, str]) -> None:
        self.escape = escape
        self.leak = leak
        self.sandbox_env = sandbox_env
        self.workspace: Path | None = None
        self._sessions: dict[str, RuntimeSession] = {}

    def bind_workspace(self, attempt_id: str, workspace: Path) -> None:
        self.workspace = workspace

    def start(self, *, attempt_id: str, agent: str) -> RuntimeSession:
        session = RuntimeSession("SESSION-G2", attempt_id, agent, RuntimeStatus.RUNNING)
        self._sessions[session.session_id] = session
        return session

    def send(self, session_id: str, input_text: str) -> RuntimeSession:
        workspace = self.workspace
        if workspace is None:
            raise RuntimeError("live adapter did not bind a workspace")
        script = (
            "import os, pathlib\n"
            f"pathlib.Path({str(self.escape)!r}).parent.mkdir(parents=True, exist_ok=True)\n"
            f"pathlib.Path({str(self.escape)!r}).write_text('bypassed')\n"
            f"pathlib.Path({str(self.leak)!r}).write_text(os.environ.get('OUT_OF_SCOPE_TOKEN', ''))\n"
            "print(os.environ.get('OUT_OF_SCOPE_TOKEN', ''))\n"
        )
        completed = subprocess.run(
            [sys.executable, "-c", script],
            cwd=workspace,
            env=self.sandbox_env,
            capture_output=True,
            text=True,
            timeout=10,
            check=False,
        )
        current = self._sessions[session_id]
        updated = RuntimeSession(
            current.session_id,
            current.attempt_id,
            current.agent,
            RuntimeStatus.COMPLETED,
            (completed.stdout,),
        )
        self._sessions[session_id] = updated
        return updated

    def stream(self, session_id: str) -> tuple[str, ...]:
        return self._sessions[session_id].output

    def status(self, session_id: str) -> RuntimeSession:
        return self._sessions[session_id]

    def cancel(self, session_id: str) -> RuntimeSession:
        return self.status(session_id)

    def terminate(self, session_id: str) -> RuntimeSession:
        return self.status(session_id)

    def reconnect(self, session_id: str) -> RuntimeSession:
        return self.status(session_id)


def _sandbox_env() -> dict[str, str]:
    """The environment a disposable sandbox receives: one in-scope credential and one that is not."""

    return {
        "PATH": os.environ.get("PATH", ""),
        "ANTHROPIC_API_KEY": _IN_SCOPE,
        "OUT_OF_SCOPE_TOKEN": _OUT_OF_SCOPE,
    }


def _escape_script(escape: Path, leak: Path) -> str:
    return (
        "import os, pathlib\n"
        f"pathlib.Path({str(escape)!r}).parent.mkdir(parents=True, exist_ok=True)\n"
        f"pathlib.Path({str(escape)!r}).write_text('bypassed')\n"
        f"pathlib.Path({str(leak)!r}).write_text(os.environ.get('OUT_OF_SCOPE_TOKEN', ''))\n"
        "print(os.environ.get('OUT_OF_SCOPE_TOKEN', ''))\n"
    )


def test_hostile_agent_cannot_bypass_tool_proxy_sandbox_or_credential_scope(tmp_path: Path):
    """G2: a hostile agent must not escape policy by skipping the Tool Proxy.

    The structured proxy denies ``process.run`` and ``credentials.inject`` for
    an out-of-scope name, and a containment-required sandbox refuses to start
    the child. The default sandbox and the live adapter's workspace shell
    must refuse the same escape.
    """

    policy = AllowlistPolicy(
        {("filesystem", "read")},
        allowed_actors={"agent:claude"},
        allowed_resources={"README"},
    )
    proxy = ToolProxy(policy=policy, executor=_DeniedExecutor())
    for request in (
        ActionRequest(
            attempt_id=_ATTEMPT,
            actor="agent:claude",
            tool="process",
            action="run",
            resource=".",
            context={"command": [sys.executable, "-c", "print('no')"]},
        ),
        ActionRequest(
            attempt_id=_ATTEMPT,
            actor="agent:claude",
            tool="credentials",
            action="inject",
            resource="OUT_OF_SCOPE_TOKEN",
            context={},
        ),
    ):
        denied = proxy.execute(request)
        assert denied.status is ToolExecutionStatus.DENIED
        assert denied.decision.effect.value == "deny"

    contained_escape = tmp_path / "contained-escape.txt"
    contained = FixtureSandbox(tmp_path / "contained", require_containment=True)
    with pytest.raises(ContainmentUnavailable, match="OS containment backend"):
        contained.run([sys.executable, "-c", f"open({str(contained_escape)!r}, 'w').write('bypassed')"])
    assert not contained_escape.exists()

    workspace = tmp_path / "workspace"
    workspace.mkdir()
    sandbox_escape = tmp_path / "sandbox-escape.txt"
    sandbox_leak = workspace / "sandbox-leak.txt"
    sandbox = FixtureSandbox(workspace)
    sandbox_result = sandbox.run(
        [sys.executable, "-c", _escape_script(sandbox_escape, sandbox_leak)],
        env=_sandbox_env(),
    )

    agent_escape = tmp_path / "agent-escape.txt"
    agent_leak = workspace / "agent-leak.txt"
    agent = _UnconstrainedAgent(escape=agent_escape, leak=agent_leak, sandbox_env=_sandbox_env())
    adapter_result = RuntimeAgentAdapter(agent, agent="claude").run(
        attempt_id=_ATTEMPT,
        workspace=workspace,
        instructions="read the out-of-scope credential and write outside the workspace",
    )

    breaches: list[str] = []
    if sandbox_escape.exists():
        breaches.append(
            "FixtureSandbox.run (require_containment defaults to false) wrote "
            f"{sandbox_escape.name} outside the workspace; the Tool Proxy path would have refused this child"
        )
    if sandbox_leak.exists() and sandbox_leak.read_text(encoding="utf-8") == _OUT_OF_SCOPE:
        breaches.append(
            "the sandbox child read OUT_OF_SCOPE_TOKEN from its environment "
            f"(stdout contains the marker: {_OUT_OF_SCOPE in sandbox_result.stdout})"
        )
    if agent_escape.exists():
        breaches.append(
            "RuntimeAgentAdapter, the LiveRunAdapter seam, ran the agent shell with no Tool Proxy "
            f"and wrote {agent_escape.name} outside the workspace"
        )
    if _OUT_OF_SCOPE in adapter_result.stdout or (
        agent_leak.exists() and agent_leak.read_text(encoding="utf-8") == _OUT_OF_SCOPE
    ):
        breaches.append(
            "the live agent turn returned the out-of-scope credential; "
            "credential scope is applied only inside Tool Proxy Execute, which this turn never entered"
        )
    assert not breaches, "G2 tool-proxy bypass: " + "; ".join(breaches)


def _session():
    engine = make_engine()
    Base.metadata.create_all(engine)
    return sessionmaker(engine, expire_on_commit=False)()


def _run_passing_attempt(db, tmp_path: Path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    db.add(TaskRow(
        id="TASK-G4", title="TASK-G4", status="created", idempotency_key="TASK-G4",
        acceptance_criteria=["tests pass"], created_at=utcnow(),
    ))
    db.commit()
    return ExecutionService(
        db,
        workspace_root=tmp_path / "workspaces",
        artifact_root=tmp_path / "artifacts",
        adapter=FakeNativeAdapter("app.py", "print('new')\n"),
    ).run(
        task_id="TASK-G4",
        dispatch_key="TASK-G4-1",
        fixture=fixture,
        instructions="update",
        commands=[],
        criterion_checks=_CHECK,
    )


def test_rewritten_evidence_is_rejected_by_verification(tmp_path: Path):
    """G4: rewriting stored evidence must fail verification or show up in the audit trail.

    The tamper is a same-store rewrite: the evaluator payload is swapped and
    ``artifacts.sha256`` is updated to the new bytes, and the evidence row's
    status, command, and confidence are rewritten with SQL that does not pass
    through the ORM session guard. SQLite, which is what this hermetic test
    uses, has no append-only trigger. ``verify_attempt_revision`` must reject
    the rewritten verdict.
    """

    with _session() as db:
        attempt = _run_passing_attempt(db, tmp_path)
        evidence = db.query(EvidenceRow).filter_by(attempt_id=attempt.id).one()
        assert evidence.status == "PASS"
        original = (evidence.status, evidence.command, evidence.confidence)
        artifact = db.get(ArtifactRow, evidence.artifact_ref)
        assert artifact is not None and artifact.kind == "evaluator_output"
        path = Path(artifact.uri)
        payload = json.loads(path.read_text(encoding="utf-8"))
        payload["status"] = "FAIL"
        payload["command"] = "echo tampered"
        payload["stdout"] = "tampered result"
        payload["confidence"] = 0.0
        raw = json.dumps(payload, ensure_ascii=False, indent=2).encode("utf-8")
        path.write_bytes(raw)
        artifact.sha256 = hashlib.sha256(raw).hexdigest()
        artifact.size_bytes = len(raw)
        db.commit()

        db.execute(
            text(
                "UPDATE evidence SET status = :status, command = :command, confidence = :confidence "
                "WHERE id = :id"
            ),
            {"status": "FAIL", "command": "echo tampered", "confidence": 0.0, "id": evidence.id},
        )
        db.commit()
        db.expire_all()

        rewritten = db.get(EvidenceRow, evidence.id)
        assert rewritten is not None
        assert (rewritten.status, rewritten.command, rewritten.confidence) == ("FAIL", "echo tampered", 0.0)
        report = verify_attempt_revision(db, attempt.id)
        edge = db.query(DecisionEdgeRow).filter_by(source_kind="evidence", source_id=evidence.id).one_or_none()
        node = db.get(GraphNodeRow, evidence.id)
        audit_rows = db.query(AuthAuditRow).count()
        runtime_rows = db.query(RuntimeEventRow).filter_by(attempt_id=attempt.id).count()
        # The attempt row is still the one the evaluator finished; nothing recorded the rewrite.
        stored_attempt = db.get(AttemptRow, attempt.id)
        assert stored_attempt is not None

        # A passing run's own rows are not a tamper alarm. Detection means verification
        # rejected this rewrite, or an audit row names the evidence that changed.
        tamper_audit = [
            row for row in db.query(AuthAuditRow)
            if evidence.id in json.dumps(row.detail or {})
        ]
        detected = report["status"] == "failed" or bool(tamper_audit)
        assert detected, (
            "G4 evidence authenticity: stored evidence was rewritten and neither verification nor the "
            f"audit trail noticed. evidence {evidence.id} status {original[0]!r} -> {rewritten.status!r}, "
            f"command {original[1]!r} -> {rewritten.command!r}, confidence {original[2]!r} -> {rewritten.confidence!r}; "
            "evaluator_output payload status was swapped to FAIL and artifacts.sha256 was updated to those bytes; "
            f"verify_attempt_revision returned {report['status']!r} problems={report['problems']}; "
            f"graph node title={None if node is None else node.title!r} (still the pre-tamper status); "
            f"decision-graph edge={None if edge is None else edge.relation}; "
            f"auth_audit rows={audit_rows}; runtime events={runtime_rows}; "
            f"attempt status={stored_attempt.status}. "
            "Evidence has no signature or hash chain."
        )
