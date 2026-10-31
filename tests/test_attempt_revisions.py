"""Attempt revisions bind artifacts and Evidence to immutable snapshots (#62, ADR 0032)."""

from __future__ import annotations

import json
import os
import shutil
import subprocess
from pathlib import Path

import hashlib

import pytest
from sqlalchemy import insert
from sqlalchemy.orm import sessionmaker

from sdf_core.adapter import AdapterResult, FakeNativeAdapter
from sdf_core.db import (
    ArtifactRow, AttemptRevisionRow, AttemptRow, Base, EvidenceRow, RevisionBindingError, TaskRow, make_engine,
)
from sdf_core.execution import ExecutionService
from sdf_core.model import utcnow
from sdf_core.revisions import (
    TREE_FORMAT, repository_identity, revision_for_attempt, tree_digest, verify_attempt_revision,
)
from tests.auth_support import admin_client
from tests.check_support import write_checks

CHECK = {"tests pass": [["python", "-c", "print('ok')"]]}


def _git_write_tree(directory: Path) -> str:
    env = {"PATH": os.environ.get("PATH", ""), "HOME": str(directory), "GIT_CONFIG_NOSYSTEM": "1",
           "GIT_CONFIG_GLOBAL": os.devnull}
    scratch = directory.parent / f"{directory.name}-gitdir"
    base = ["git", f"--git-dir={scratch}", f"--work-tree={directory}"]
    subprocess.run([*base, "init", "-q", "--object-format=sha256"], check=True, capture_output=True, env=env)
    subprocess.run([*base, "add", "-A", "-f"], check=True, capture_output=True, env=env)
    return subprocess.run([*base, "write-tree"], check=True, capture_output=True, text=True, env=env).stdout.strip()


def _session():
    engine = make_engine()
    Base.metadata.create_all(engine)
    return sessionmaker(engine, expire_on_commit=False)()


def _fixture(tmp_path: Path, name: str = "fixture") -> Path:
    fixture = tmp_path / name
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    return fixture


def _task(db, task_id: str) -> None:
    db.add(TaskRow(id=task_id, title=task_id, status="created", idempotency_key=task_id,
                   acceptance_criteria=["tests pass"], created_at=utcnow()))
    db.commit()


def _service(db, tmp_path: Path, adapter=None) -> ExecutionService:
    return ExecutionService(db, workspace_root=tmp_path / "workspaces", artifact_root=tmp_path / "artifacts",
                            adapter=adapter or FakeNativeAdapter("app.py", "print('new')\n"))


def _run(db, tmp_path: Path, task_id: str, fixture: Path, adapter=None):
    _task(db, task_id)
    return _service(db, tmp_path, adapter).run(task_id=task_id, dispatch_key=f"{task_id}-1", fixture=fixture,
                                               instructions="update", commands=[], criterion_checks=CHECK)


# --- tree identity -----------------------------------------------------------


@pytest.mark.skipif(shutil.which("git") is None, reason="git is not installed")
def test_tree_digest_equals_git_sha256_write_tree(tmp_path: Path):
    root = tmp_path / "tree"
    (root / "pkg" / "deep").mkdir(parents=True)
    (root / "empty-dir").mkdir()
    (root / "a.txt").write_text("alpha\n")
    (root / "pkg" / "b.py").write_text("print('b')\n")
    (root / "pkg" / "deep" / "c.bin").write_bytes(bytes(range(256)))
    (root / "pkg-sorted-after").write_text("dir/file ordering\n")
    run = root / "run.sh"
    run.write_text("#!/bin/sh\necho hi\n")
    run.chmod(0o755)
    (root / ".gitignore").write_text("ignored.log\n")
    (root / "ignored.log").write_text("still part of the tree\n")
    os.symlink("a.txt", root / "link")
    try:
        expected = _git_write_tree(root)
    except subprocess.CalledProcessError as exc:  # pragma: no cover - old git
        pytest.skip(f"git without sha256 object format: {exc.stderr!r}")
    assert tree_digest(root) == expected
    # A .git directory never counts; any other change does.
    (root / ".git").mkdir()
    (root / ".git" / "HEAD").write_text("ref: refs/heads/main\n")
    assert tree_digest(root) == expected
    (root / "ignored.log").write_text("changed\n")
    assert tree_digest(root) != expected


def test_tree_digest_tracks_content_mode_and_names(tmp_path: Path):
    root = tmp_path / "t"
    root.mkdir()
    empty = tree_digest(root)
    (root / "x").write_text("1")
    one = tree_digest(root)
    assert len(one) == 64 and one != empty
    (root / "x").chmod(0o755)
    assert tree_digest(root) != one
    (root / "x").chmod(0o644)
    assert tree_digest(root) == one
    (root / "x").rename(root / "y")
    assert tree_digest(root) != one


def test_repository_identity_is_provider_neutral(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("SDF_FIXTURES_ROOT", str(tmp_path))
    fixture = _fixture(tmp_path)
    assert repository_identity(fixture) == "fixture:fixture"
    monkeypatch.setenv("SDF_FIXTURES_ROOT", str(tmp_path / "elsewhere"))
    expected = hashlib.sha256(os.fsencode(os.path.realpath(fixture))).hexdigest()[:16]
    assert repository_identity(fixture) == f"path-sha256:{expected}"  # no host path in the API


def test_workspace_copy_keeps_the_execute_bit(tmp_path: Path):
    from sdf_core.artifacts import WorkspaceManager

    fixture = _fixture(tmp_path)
    (fixture / "run.sh").write_text("#!/bin/sh\n")
    (fixture / "run.sh").chmod(0o755)
    workspace = WorkspaceManager(tmp_path / "ws").create("ATTEMPT-X", fixture)
    assert os.access(workspace / "run.sh", os.X_OK)
    assert not os.access(workspace / "app.py", os.X_OK)
    assert (workspace / "run.sh").stat().st_mode & 0o011 == 0  # the owner bit only
    assert tree_digest(workspace) == tree_digest(fixture)


# --- execution records and binds the revision ---------------------------------


def test_run_records_base_and_result_and_binds_every_row(tmp_path: Path):
    fixture = _fixture(tmp_path)
    base = tree_digest(fixture)
    with _session() as db:
        attempt = _run(db, tmp_path, "TASK-REV-1", fixture)
        assert db.get(TaskRow, "TASK-REV-1").status == "succeeded"
        revision = revision_for_attempt(db, attempt.id)
        assert revision.tree_format == TREE_FORMAT
        assert revision.repository.startswith(("fixture:", "path-sha256:"))
        assert revision.base_tree_sha256 == base
        assert revision.result_tree_sha256 == tree_digest(tmp_path / "workspaces" / attempt.id) != base
        # The fake adapter has no Git baseline: nothing is invented for it.
        assert revision.base_commit is None and revision.base_commit_source is None
        assert attempt.evidence_mode == "synthetic"
        artifacts = db.query(ArtifactRow).filter_by(attempt_id=attempt.id).all()
        evidence = db.query(EvidenceRow).filter_by(attempt_id=attempt.id).all()
        assert {a.kind for a in artifacts} == {"diff", "process_log", "evaluator_output"}
        assert evidence and all(e.criterion == "tests pass" for e in evidence)
        for row in (*artifacts, *evidence):
            assert (row.revision_id, row.tree_sha256) == (revision.id, revision.result_tree_sha256)
        output = json.loads(Path(db.get(ArtifactRow, evidence[0].artifact_ref).uri).read_text())
        assert output["revision"] == {"revision_id": revision.id, "tree_sha256": revision.result_tree_sha256,
                                      "tree_after_check_sha256": revision.result_tree_sha256, "tree_drift": False}
        assert verify_attempt_revision(db, attempt.id) == {
            "attempt_id": attempt.id, "status": "verified", "problems": [], "artifacts": 3, "evidence": 1}


def test_verification_detects_a_tampered_artifact(tmp_path: Path):
    with _session() as db:
        attempt = _run(db, tmp_path, "TASK-REV-TAMPER", _fixture(tmp_path))
        diff = db.get(ArtifactRow, f"{attempt.id}-DIFF")
        Path(diff.uri).write_text("forged diff\n")
        report = verify_attempt_revision(db, attempt.id)
        assert report["status"] == "failed"
        assert report["problems"] == [f"artifact {diff.id} file digest does not match its recorded sha256"]


def test_finish_refuses_when_the_fixture_changed_after_begin(tmp_path: Path):
    fixture = _fixture(tmp_path)

    class Mutating(FakeNativeAdapter):
        def run(self, **kwargs):
            (fixture / "app.py").write_text("print('moved base')\n")
            return super().run(**kwargs)

    with _session() as db:
        _task(db, "TASK-REV-MOVED")
        with pytest.raises(RevisionBindingError, match="fixture changed"):
            _service(db, tmp_path, Mutating("app.py", "print('new')\n")).run(
                task_id="TASK-REV-MOVED", dispatch_key="moved", fixture=fixture, instructions="x", commands=[],
                criterion_checks=CHECK)
        attempt = db.query(AttemptRow).filter_by(task_id="TASK-REV-MOVED").one()
        assert attempt.status == "failed" and db.get(TaskRow, "TASK-REV-MOVED").status == "failed"
        assert db.query(ArtifactRow).filter_by(attempt_id=attempt.id).count() == 0
        assert revision_for_attempt(db, attempt.id).result_tree_sha256 is None


def test_finish_refuses_an_adapter_result_tree_that_does_not_match(tmp_path: Path):
    class Lying(FakeNativeAdapter):
        def run(self, **kwargs):
            result = super().run(**kwargs)
            return AdapterResult(**{**result.__dict__, "result_tree_sha256": "0" * 64})

    with _session() as db:
        _task(db, "TASK-REV-LIE")
        with pytest.raises(RevisionBindingError, match="workspace changed after the adapter finished"):
            _service(db, tmp_path, Lying("app.py", "print('new')\n")).run(
                task_id="TASK-REV-LIE", dispatch_key="lie", fixture=_fixture(tmp_path), instructions="x",
                commands=[], criterion_checks=CHECK)
        attempt = db.query(AttemptRow).filter_by(task_id="TASK-REV-LIE").one()
        assert attempt.status == "failed"
        assert db.query(EvidenceRow).filter_by(attempt_id=attempt.id).count() == 0


def test_finish_refuses_an_attempt_without_a_base_revision(tmp_path: Path):
    with _session() as db:
        _task(db, "TASK-REV-NONE")
        db.add(AttemptRow(id="ATTEMPT-NO-REV", task_id="TASK-REV-NONE", status="running", dispatch_key="no-rev",
                          agent="test", created_at=utcnow()))
        db.get(TaskRow, "TASK-REV-NONE").status = "running"
        db.commit()
        (tmp_path / "workspaces" / "ATTEMPT-NO-REV").mkdir(parents=True)
        with pytest.raises(RevisionBindingError, match="no base revision"):
            _service(db, tmp_path).finish(
                attempt_id="ATTEMPT-NO-REV", fixture=_fixture(tmp_path), commands=[],
                result=AdapterResult(attempt_id="ATTEMPT-NO-REV", status="completed", changed_files=()))
        assert db.get(AttemptRow, "ATTEMPT-NO-REV").status == "failed"
        assert db.query(ArtifactRow).filter_by(attempt_id="ATTEMPT-NO-REV").count() == 0


@pytest.mark.skipif(shutil.which("git") is None, reason="git is not installed")
def test_e2b_adapter_records_the_baseline_commit(tmp_path: Path):
    from sdf_core.herdr_e2b import HerdrE2BNativeAdapter

    with _session() as db:
        _task(db, "TASK-REV-E2B")
        # Dry run (live=False): no network; the baseline commit is still real and local.
        attempt = _service(db, tmp_path, HerdrE2BNativeAdapter(live=False)).run(
            task_id="TASK-REV-E2B", dispatch_key="e2b", fixture=_fixture(tmp_path), instructions="x", commands=[])
        workspace = tmp_path / "workspaces" / attempt.id
        head = subprocess.run(["git", "rev-parse", "HEAD"], cwd=workspace, capture_output=True, text=True,
                              check=True).stdout.strip()
        revision = revision_for_attempt(db, attempt.id)
        assert (revision.base_commit, revision.base_commit_source) == (head, "e2b-baseline")
        # The .git the baseline added is not part of the tree identity.
        assert revision.result_tree_sha256 == revision.base_tree_sha256 == tree_digest(workspace)
        assert attempt.evidence_mode == "synthetic"


# --- binding fences ----------------------------------------------------------


def _two_finished_attempts(db, tmp_path: Path):
    first = _run(db, tmp_path, "TASK-REV-A", _fixture(tmp_path, "fa"))
    second = _run(db, tmp_path, "TASK-REV-B", _fixture(tmp_path, "fb"))
    return revision_for_attempt(db, first.id), revision_for_attempt(db, second.id)


def _artifact(attempt_id: str, **binding) -> ArtifactRow:
    return ArtifactRow(id=f"ART-{attempt_id}-{len(binding)}-{sorted(binding.items())!r}"[:120], attempt_id=attempt_id,
                       kind="diff", uri="x", sha256="0" * 64, size_bytes=0, created_at=utcnow(), **binding)


def _evidence(attempt_id: str, **binding) -> EvidenceRow:
    return EvidenceRow(id=f"EV-{attempt_id}-x"[:120], attempt_id=attempt_id, kind="evaluation", status="PASS",
                       command="c", exit_code=0, artifact_ref=None, confidence=1.0, criterion="tests pass",
                       measured_at=utcnow(), created_at=utcnow(), **binding)


@pytest.mark.parametrize("make", [_artifact, _evidence])
def test_missing_mismatched_cross_attempt_and_unknown_bindings_are_rejected(tmp_path: Path, make):
    with _session() as db:
        a, b = _two_finished_attempts(db, tmp_path)
        cases = [
            ({}, "missing revision binding"),
            ({"revision_id": a.id, "tree_sha256": "f" * 64}, "mismatched revision"),
            ({"revision_id": a.id}, "mismatched revision"),
            ({"revision_id": b.id, "tree_sha256": b.result_tree_sha256}, "cross-Attempt"),
            ({"revision_id": "REV-nope", "tree_sha256": a.result_tree_sha256}, "unknown revision"),
        ]
        for binding, message in cases:
            db.add(make(a.attempt_id, **binding))
            with pytest.raises(RevisionBindingError, match=message):
                db.flush()
            db.rollback()
        db.add(make(a.attempt_id, revision_id=a.id, tree_sha256=a.result_tree_sha256))
        db.commit()  # the valid binding is accepted


def _legacy_attempt(db, attempt_id: str) -> None:
    """An Attempt as migration 0023 found it: ``revision_required`` NULL (only a
    pre-0023 row, or direct SQL on SQLite, can look like this)."""

    db.execute(insert(AttemptRow.__table__).values(
        id=attempt_id, task_id="TASK-LEGACY", status="completed", dispatch_key=f"legacy-{attempt_id}", agent="old",
        created_at=utcnow(), evidence_mode="synthetic", revision_required=None))
    db.commit()


def test_an_attempt_created_after_0023_outside_begin_cannot_get_unbound_evidence(tmp_path: Path):
    with _session() as db:
        # Created through the ORM without begin (and without a revision): the guard
        # marks it, whatever the caller says, so a missing revision row is not "legacy".
        db.add(AttemptRow(id="ATTEMPT-SIDE", task_id="T", status="completed", dispatch_key="side", agent="x",
                          created_at=utcnow(), revision_required=None))
        db.commit()
        assert db.get(AttemptRow, "ATTEMPT-SIDE").revision_required is True
        for make in (_evidence, _artifact):
            db.add(make("ATTEMPT-SIDE"))
            with pytest.raises(RevisionBindingError, match="created after migration 0023"):
                db.flush()
            db.rollback()
        # Unknown Attempts are not legacy either.
        db.add(_evidence("ATTEMPT-GHOST"))
        with pytest.raises(RevisionBindingError, match="unknown attempt"):
            db.flush()
        db.rollback()
        # A genuine pre-0023 Attempt still may; and nobody can flip the marker.
        _legacy_attempt(db, "ATTEMPT-OLD")
        db.add(_evidence("ATTEMPT-OLD"))
        db.commit()
        db.get(AttemptRow, "ATTEMPT-OLD").revision_required = True
        with pytest.raises(RevisionBindingError, match="immutable"):
            db.flush()
        db.rollback()
        db.get(AttemptRow, "ATTEMPT-SIDE").revision_required = None
        with pytest.raises(RevisionBindingError, match="immutable"):
            db.flush()


def test_an_artifact_binding_cannot_be_rewritten_after_insert(tmp_path: Path):
    with _session() as db:
        a, b = _two_finished_attempts(db, tmp_path)
        artifact = db.query(ArtifactRow).filter_by(attempt_id=a.attempt_id, kind="diff").one()
        for field, value in (("revision_id", b.id), ("tree_sha256", "f" * 64), ("revision_id", None)):
            setattr(artifact, field, value)
            with pytest.raises(RevisionBindingError, match="immutable after insert"):
                db.flush()
            db.rollback()
            artifact = db.query(ArtifactRow).filter_by(attempt_id=a.attempt_id, kind="diff").one()
        assert (artifact.revision_id, artifact.tree_sha256) == (a.id, a.result_tree_sha256)


def test_a_check_write_stays_out_of_the_attempt_workspace(tmp_path: Path):
    """A check's cwd is the sealed oracle, so a cwd write cannot move the revision (ADR 0036).

    The same command used to land ``dropped.txt`` in the Attempt workspace and
    force INCONCLUSIVE (ADR 0032). The drift hook still downgrades a PASS when
    the workspace itself changes; the next test covers that.
    """

    fixture = _fixture(tmp_path)
    checks = {"tests pass": [["python", "-c", "open('dropped.txt', 'w').write('x')"],
                             ["python", "-c", "print('ok')"]]}
    with _session() as db:
        _task(db, "TASK-REV-DRIFT")
        attempt = _service(db, tmp_path).run(task_id="TASK-REV-DRIFT", dispatch_key="drift-1", fixture=fixture,
                                             instructions="x", commands=[], criterion_checks=checks)
        workspace = tmp_path / "workspaces" / attempt.id
        assert not (workspace / "dropped.txt").exists()
        assert db.get(TaskRow, "TASK-REV-DRIFT").status == "succeeded"
        revision = revision_for_attempt(db, attempt.id)
        evidence = db.query(EvidenceRow).filter_by(attempt_id=attempt.id).order_by(EvidenceRow.id).all()
        assert [e.status for e in evidence] == ["PASS", "PASS"]
        for row in evidence:
            assert row.evidence_class == "verification"
            assert (row.revision_id, row.tree_sha256) == (revision.id, revision.result_tree_sha256)
            output = json.loads(Path(db.get(ArtifactRow, row.artifact_ref).uri).read_text())
            assert output["revision"]["tree_drift"] is False
            assert output["revision"]["tree_after_check_sha256"] == revision.result_tree_sha256
        assert verify_attempt_revision(db, attempt.id)["status"] == "verified"


def test_an_evaluator_without_the_hook_is_still_drift_checked(tmp_path: Path):
    from sdf_core.evaluator import DeterministicEvaluator

    class NoHook(DeterministicEvaluator):
        def evaluate(self, *, after_check=None, **kwargs):  # ignores after_check
            (kwargs["workspace"] / "late.txt").write_text("x")
            return super().evaluate(**kwargs)

    fixture = _fixture(tmp_path)
    with _session() as db:
        _task(db, "TASK-REV-NOHOOK")
        service = _service(db, tmp_path)
        service.evaluator = NoHook()
        service.run(task_id="TASK-REV-NOHOOK", dispatch_key="nohook-1", fixture=fixture, instructions="x",
                    commands=[], criterion_checks=CHECK)
        assert db.get(TaskRow, "TASK-REV-NOHOOK").status == "inconclusive"


def test_unfinished_revision_and_legacy_attempts(tmp_path: Path):
    with _session() as db:
        db.add(AttemptRevisionRow(id="REV-OPEN", attempt_id="ATTEMPT-OPEN", repository="path:/x",
                                  tree_format=TREE_FORMAT, base_tree_sha256="a" * 64, created_at=utcnow()))
        db.commit()
        db.add(_artifact("ATTEMPT-OPEN", revision_id="REV-OPEN", tree_sha256=None))
        with pytest.raises(RevisionBindingError, match="no result tree yet"):
            db.flush()
        db.rollback()
        # An Attempt from before migration 0023 has no revision; its rows stay unbound.
        _legacy_attempt(db, "ATTEMPT-LEGACY")
        db.add(_artifact("ATTEMPT-LEGACY"))
        db.commit()
        assert verify_attempt_revision(db, "ATTEMPT-LEGACY", check_files=False)["status"] == "unbound"
        assert verify_attempt_revision(db, "ATTEMPT-OPEN")["status"] == "incomplete"
        db.add(_artifact("ATTEMPT-LEGACY2", tree_sha256="a" * 64))
        with pytest.raises(RevisionBindingError, match="without a revision"):
            db.flush()


def test_revision_base_is_immutable_results_set_once_never_deleted(tmp_path: Path):
    with _session() as db:
        attempt = _run(db, tmp_path, "TASK-REV-IMM", _fixture(tmp_path))
        revision = revision_for_attempt(db, attempt.id)
        for field, value in (("base_tree_sha256", "b" * 64), ("repository", "path:/other"),
                             ("attempt_id", "ATTEMPT-OTHER")):
            setattr(revision, field, value)
            with pytest.raises(RevisionBindingError, match="immutable"):
                db.flush()
            db.rollback()
        revision.result_tree_sha256 = "c" * 64
        with pytest.raises(RevisionBindingError, match="set once"):
            db.flush()
        db.rollback()
        db.delete(revision)
        with pytest.raises(ValueError, match="cannot be deleted"):
            db.flush()


# --- API -----------------------------------------------------------------------


def test_api_exposes_revision_in_trace_evidence_and_attempt_endpoint(tmp_path: Path):
    client = admin_client()
    fixture = _fixture(tmp_path)
    write_checks(fixture, {"prints-ok": ["python", "-c", "print('ok')"]})
    assert client.post("/tasks", json={"id": "TASK-REV-API", "title": "rev", "idempotency_key": "task-rev-api",
                                       "acceptance_criteria": ["command succeeds"]}).status_code == 201
    run = client.post("/tasks/TASK-REV-API/run", json={"fixture_dir": str(fixture),
                                                      "criterion_checks": {"command succeeds": ["prints-ok"]}})
    assert run.status_code == 200, run.text
    trace = client.get("/tasks/TASK-REV-API/trace").json()
    [revision] = trace["revisions"]
    assert revision["tree_format"] == TREE_FORMAT
    assert revision["base_tree_sha256"] == tree_digest(fixture)
    assert revision["result_tree_sha256"] and revision["base_commit"] is None
    assert revision["evidence_mode"] == "synthetic"
    evidence_id = next(node["id"] for node in trace["nodes"] if node["kind"] == "evidence")
    evidence = client.get(f"/evidence/{evidence_id}").json()
    assert evidence["criterion"] == "command succeeds"
    assert (evidence["revision_id"], evidence["tree_sha256"]) == (revision["id"], revision["result_tree_sha256"])
    body = client.get(f"/attempts/{revision['attempt_id']}/revision").json()
    assert body["revision"] == revision
    assert body["verification"]["status"] == "verified"
    assert body["acceptance_evidence"] is False
    assert client.get("/attempts/ATTEMPT-NOPE/revision").status_code == 404
