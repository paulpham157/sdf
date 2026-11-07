"""Security boundaries for a hostile agent turn and for stored Evidence.

G2. A child started by ``FixtureSandbox`` (the default path, not only
``require_containment=True``) and a child the live adapter's ``send`` starts
with its cwd inside the Attempt workspace must not write outside that
workspace or observe a credential-shaped variable the Attempt did not scope.
``require_containment=True`` with no backend still refuses to start.

G4. Rewriting an Evidence row's verdict and the sha256 stored beside the
artifact bytes must make ``verify_attempt_revision`` fail. The content MAC
(ADR 0043) is the commitment that rewrite does not update. A same-store
writer, which SQLite allows, must also fail verification when it clears
``content_mac`` on a row this process signed, or when it rewrites an
acceptance column the MAC payload does not cover. A NULL MAC on a row that
was never signed (written before migration 0029) is the legacy case and is
not these tests.
"""

from __future__ import annotations

import hashlib
import json
import os
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

import pytest
from sqlalchemy import text
from sqlalchemy.orm import sessionmaker

from sdf_core.acceptance import AcceptanceError, check_verification_acceptance, is_hidden_oracle, is_verified_pass
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
from sdf_core.evidence_mac import evidence_content_mac
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
        evidence = db.query(EvidenceRow).filter_by(
            attempt_id=attempt.id, evidence_class="verification",
        ).one()
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


# Columns ``acceptance.py`` uses to decide independence, binding, freshness, or
# hidden-oracle status, and that ``evidence_content_mac`` does not sign.
# Values are legal for the column and, where the acceptance gate reads the
# column, still a forgery that gate accepts: a different verifier who is not
# the attempt's agent, a non-agent role, an expiry further in the future, a
# held-out bit, a non-empty oracle digest. Subject digest and evidence class
# are included because the MAC omits them too; the acceptance gate may reject
# those two forgeries on its own, which is not a content-MAC check.
_UNSIGNED_ACCEPTANCE_COLUMNS = (
    "verifier_identity",
    "verifier_role",
    "expires_at",
    "oracle_visibility",
    "subject_artifact_sha256",
    "oracle_digest",
    "evidence_class",
    "evidence_mode",
    "producer_identity",
    "producer_role",
    "environment_digest",
    "collected_at",
    "input_seed",
)
_FORGED_ACCEPTANCE_VALUE = {
    "verifier_identity": "forged-verifier",
    "verifier_role": "stakeholder",
    "expires_at": datetime(2099, 1, 1, tzinfo=timezone.utc),
    "oracle_visibility": "held_out",
    "subject_artifact_sha256": "ab" * 32,
    "oracle_digest": "cd" * 32,
    "evidence_class": "execution",
    "evidence_mode": "live",
    "producer_identity": "forged-producer",
    "producer_role": "agent",
    "environment_digest": "ef" * 32,
    "collected_at": datetime(2020, 1, 1, tzinfo=timezone.utc),
    "input_seed": "forged-input-seed",
}


def _acceptance_after_rewrite(row, *, attempt_agent: str, artifact_sha256: str) -> str:
    """How the acceptance gate treats ``row`` after a metadata-only rewrite."""

    try:
        check_verification_acceptance(
            row, attempt_agent=attempt_agent, artifact_sha256=artifact_sha256,
        )
    except AcceptanceError as exc:
        return f"rejects ({exc})"
    return (
        "accepts"
        f"; is_verified_pass={is_verified_pass(row, attempt_agent=attempt_agent, artifact_sha256=artifact_sha256)}"
        f"; is_hidden_oracle={is_hidden_oracle(row)}"
    )


def test_cleared_modern_content_mac_is_rejected(tmp_path: Path):
    """G4: clearing content_mac on a signed row must not hide a verdict rewrite.

    ADR 0043 / migration 0029 leave a genuine pre-migration row unsigned, and
    ``verify_attempt_revision`` skips a falsy ``content_mac``. That skip is for
    a row that was never signed. There is no immutable per-row marker that a
    MAC is required. This row is inserted by this process, so the ORM stamps a
    MAC. The tamper is the same SQLite rewrite as
    ``test_rewritten_evidence_is_rejected_by_verification`` (verdict columns
    plus the evaluator artifact bytes and ``artifacts.sha256``) and then sets
    ``content_mac`` to NULL. Verification must reject it. This test does not
    change the legacy unsigned-row rule.
    """

    with _session() as db:
        attempt = _run_passing_attempt(db, tmp_path)
        evidence = db.query(EvidenceRow).filter_by(
            attempt_id=attempt.id, evidence_class="verification",
        ).one()
        assert evidence.status == "PASS"
        assert evidence.content_mac and len(evidence.content_mac) == 64
        original_mac = evidence.content_mac
        original = (evidence.status, evidence.command, evidence.confidence)
        baseline = verify_attempt_revision(db, attempt.id)
        assert baseline["status"] == "verified" and baseline["problems"] == [], baseline
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
                "UPDATE evidence SET status = :status, command = :command, confidence = :confidence, "
                "content_mac = NULL WHERE id = :id"
            ),
            {"status": "FAIL", "command": "echo tampered", "confidence": 0.0, "id": evidence.id},
        )
        db.commit()
        db.expire_all()

        rewritten = db.get(EvidenceRow, evidence.id)
        assert rewritten is not None
        assert rewritten.content_mac is None
        assert (rewritten.status, rewritten.command, rewritten.confidence) == ("FAIL", "echo tampered", 0.0)
        report = verify_attempt_revision(db, attempt.id)
        assert report["status"] != "verified", (
            "G4 cleared content MAC: a modern signed verification row was rewritten and content_mac was "
            f"set to NULL. evidence {evidence.id} status {original[0]!r} -> {rewritten.status!r}, "
            f"command {original[1]!r} -> {rewritten.command!r}, confidence {original[2]!r} -> {rewritten.confidence!r}; "
            f"the stamped MAC was {original_mac} and is now NULL; evaluator_output was swapped to FAIL and "
            "artifacts.sha256 was updated to those bytes, so the file check still matches. "
            f"verify_attempt_revision returned {report['status']!r} problems={report['problems']}. "
            "A NULL MAC is only the pre-migration case (ADR 0043). This row was signed by this process "
            "and then cleared. There is no per-row marker, and verify_attempt_revision skips a falsy "
            "content_mac, so the rewrite verifies."
        )


@pytest.mark.parametrize("column", _UNSIGNED_ACCEPTANCE_COLUMNS)
def test_content_mac_binds_acceptance_column(tmp_path: Path, column: str):
    """G4: a same-store rewrite of an acceptance column must not stay verified.

    ``evidence_content_mac`` signs the verdict and the bound artifact sha256.
    It does not sign evidence class or mode, producer or verifier identity and
    role, subject or oracle or environment digests, collection or expiry
    timestamps, oracle visibility, or the input seed. ``acceptance.py`` uses
    those columns. The MAC is computed before and after the SQL update. The
    stored MAC is not rewritten. ``verify_attempt_revision`` must reject the
    row. SQLite is the store, matching the other G4 test: PostgreSQL's
    append-only trigger may block the UPDATE, and this finding is about the
    write path that allows it.
    """

    forged = _FORGED_ACCEPTANCE_VALUE[column]
    with _session() as db:
        attempt = _run_passing_attempt(db, tmp_path)
        evidence = db.query(EvidenceRow).filter_by(
            attempt_id=attempt.id, evidence_class="verification",
        ).one()
        artifact = db.get(ArtifactRow, evidence.artifact_ref)
        assert artifact is not None and artifact.kind == "evaluator_output"
        assert evidence.content_mac and len(evidence.content_mac) == 64
        original = getattr(evidence, column)
        assert forged != original, (column, original, forged)
        diff_sha = evidence.subject_artifact_sha256
        assert isinstance(diff_sha, str) and len(diff_sha) == 64
        mac_before = evidence_content_mac(evidence, artifact_sha256=artifact.sha256)
        assert mac_before == evidence.content_mac
        baseline = verify_attempt_revision(db, attempt.id)
        assert baseline["status"] == "verified" and baseline["problems"] == [], baseline

        db.execute(
            text(f"UPDATE evidence SET {column} = :value WHERE id = :id"),
            {"value": forged, "id": evidence.id},
        )
        db.commit()
        db.expire_all()

        rewritten = db.get(EvidenceRow, evidence.id)
        assert rewritten is not None
        assert getattr(rewritten, column) == forged, (column, getattr(rewritten, column), forged)
        mac_after = evidence_content_mac(rewritten, artifact_sha256=artifact.sha256)
        report = verify_attempt_revision(db, attempt.id)
        acceptance = _acceptance_after_rewrite(
            rewritten, attempt_agent=attempt.agent, artifact_sha256=diff_sha,
        )
        assert report["status"] != "verified", (
            f"G4 unsigned metadata: rewriting evidence.{column} from {original!r} to {forged!r} "
            f"left the content MAC unchanged (before={mac_before}, after={mac_after}, "
            f"stored={rewritten.content_mac}) and verify_attempt_revision returned {report['status']!r} "
            f"problems={report['problems']}. Acceptance of the forged row against the original diff "
            f"digest: {acceptance}. evidence_content_mac does not cover {column}, so a same-store writer "
            "can change it without the server key and verification still treats the row as authentic."
        )


def test_legacy_unsigned_row_without_mac_version_still_verifies(tmp_path: Path):
    """A pre-0029 row (NULL MAC and NULL version) is not itself a MAC failure."""

    with _session() as db:
        attempt = _run_passing_attempt(db, tmp_path)
        evidence = db.query(EvidenceRow).filter_by(
            attempt_id=attempt.id, evidence_class="verification",
        ).one()
        assert evidence.content_mac and evidence.content_mac_version == 2
        db.execute(
            text(
                "INSERT INTO evidence ("
                "id, attempt_id, kind, status, command, exit_code, artifact_ref, "
                "confidence, criterion, measured_at, evidence_mode, created_at, "
                "revision_id, tree_sha256, evidence_class, producer_identity, "
                "producer_role, verifier_identity, verifier_role, "
                "subject_artifact_sha256, oracle_digest, environment_digest, "
                "collected_at, expires_at, oracle_visibility, input_seed, "
                "objective_id, content_mac, content_mac_version"
                ") SELECT 'EV-LEGACY-UNSIGNED', attempt_id, kind, status, command, "
                "exit_code, artifact_ref, confidence, criterion, measured_at, "
                "evidence_mode, created_at, revision_id, tree_sha256, evidence_class, "
                "producer_identity, producer_role, verifier_identity, verifier_role, "
                "subject_artifact_sha256, oracle_digest, environment_digest, "
                "collected_at, expires_at, oracle_visibility, input_seed, "
                "objective_id, NULL, NULL FROM evidence WHERE id = :id"
            ),
            {"id": evidence.id},
        )
        db.commit()
        report = verify_attempt_revision(db, attempt.id)
        assert report["status"] == "verified" and report["problems"] == [], report
