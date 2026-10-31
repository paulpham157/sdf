"""Immutable repository revisions for Attempts (#62, ADR 0032).

Every Attempt that ``ExecutionService`` starts records which repository
snapshot it began from and which snapshot it produced.  Artifacts and
Evidence carry the revision they were derived from, so a reader can tell
exactly which bytes an evaluator checked.  A passing command or an agent's
report is never treated as proof of anything beyond that snapshot.

Tree identity (``TREE_FORMAT``): the Git tree object id, in Git's SHA-256
object format, of every regular file and symlink under the directory.

* Any path component named ``.git`` is skipped, as Git does.
* ``.gitignore``, ``.gitattributes`` and filters are **not** applied: every
  file counts, so an agent cannot hide a change behind an ignore rule.
* The mode is ``100755`` when the owner-execute bit is set, otherwise
  ``100644``.  Symlinks are ``120000`` (their target is hashed, never
  followed).  Empty directories and special files are omitted, as in Git.

The id is computed in-process from file bytes; no ``git`` binary runs, so a
repository config written by an agent (hooks, fsmonitor, filters) cannot
execute on the host.  It can be reproduced independently with::

    git init --object-format=sha256 && git add -A -f && git write-tree

(identical except for a nested repository, which Git records as a gitlink).
"""

from __future__ import annotations

import hashlib
import os
import re
import stat
from pathlib import Path
from typing import Any
from uuid import uuid4

from sqlalchemy import select
from sqlalchemy.orm import Session

from .db import ArtifactRow, AttemptRevisionRow, AttemptRow, EvidenceRow, RevisionBindingError
from .fixtures import fixtures_root
from .model import utcnow

TREE_FORMAT = "git-sha256-tree-v1"
_HEX64 = re.compile(r"[0-9a-f]{64}")
_GIT_OID = re.compile(r"[0-9a-f]{40}|[0-9a-f]{64}")
_CHUNK = 1 << 20

__all__ = [
    "TREE_FORMAT", "RevisionBindingError", "tree_digest", "repository_identity", "record_base_revision",
    "close_revision", "revision_for_attempt", "revision_payload", "verify_attempt_revision",
]


def _object_id(kind: bytes, size: int, chunks) -> Any:
    digest = hashlib.sha256(kind + b" " + str(size).encode() + b"\0")
    for chunk in chunks:
        digest.update(chunk)
    return digest


def _file_chunks(path: str, size: int):
    remaining = size
    with open(path, "rb") as handle:
        while remaining > 0:
            chunk = handle.read(min(_CHUNK, remaining))
            if not chunk:
                break
            remaining -= len(chunk)
            yield chunk
    if remaining:
        raise RevisionBindingError(f"file changed while it was hashed: {path}")


def _tree(directory: str, skip_names: frozenset[str] = frozenset()) -> bytes | None:
    entries: list[tuple[bytes, bytes]] = []
    with os.scandir(directory) as listing:
        for entry in listing:
            if entry.name == ".git" or entry.name in skip_names:
                continue
            info = entry.stat(follow_symlinks=False)
            name = os.fsencode(entry.name)
            if stat.S_ISLNK(info.st_mode):
                target = os.fsencode(os.readlink(entry.path))
                mode, sort_key = b"120000", name
                oid = _object_id(b"blob", len(target), [target]).digest()
            elif stat.S_ISDIR(info.st_mode):
                sub = _tree(entry.path, skip_names)
                if sub is None:
                    continue
                mode, sort_key, oid = b"40000", name + b"/", sub
            elif stat.S_ISREG(info.st_mode):
                mode = b"100755" if info.st_mode & stat.S_IXUSR else b"100644"
                sort_key = name
                oid = _object_id(b"blob", info.st_size, _file_chunks(entry.path, info.st_size)).digest()
            else:
                continue  # sockets, fifos, devices: Git does not track them either
            entries.append((sort_key, mode + b" " + name + b"\0" + oid))
    if not entries:
        return None
    body = b"".join(item for _, item in sorted(entries))
    return _object_id(b"tree", len(body), [body]).digest()


def tree_digest(root: Path, *, skip_names: frozenset[str] = frozenset()) -> str:
    """The ``TREE_FORMAT`` id of everything under ``root`` (an empty tree for an empty directory).

    ``skip_names`` is only for the server's held-out oracle directory when
    comparing a fixture to the workspace copy (ADR 0035). The result tree of
    an Attempt does not use it, so an agent cannot hide a file by that name.
    """

    root = Path(root)
    if root.is_symlink() or not root.is_dir():
        raise RevisionBindingError(f"revision root must be a real directory: {root}")
    oid = _tree(os.fspath(root), skip_names)
    return oid.hex() if oid is not None else _object_id(b"tree", 0, []).hexdigest()


def repository_identity(fixture: Path) -> str:
    """A provider-neutral repository identifier for the Attempt's source.

    A fixture under ``SDF_FIXTURES_ROOT`` is ``fixture:<path relative to the
    root>``; any other directory (in-process callers, tests) is
    ``path-sha256:<first 16 hex of sha256(absolute real path)>``: stable for
    the same directory without exposing a host path through the API.  No
    forge-specific data is canonical: the immutable revision is the tree id,
    not a URL.
    """

    resolved = Path(os.path.realpath(fixture))
    root = fixtures_root()
    if resolved != root and resolved.is_relative_to(root):
        return "fixture:" + resolved.relative_to(root).as_posix()
    return "path-sha256:" + hashlib.sha256(os.fsencode(resolved)).hexdigest()[:16]


def revision_for_attempt(db: Session, attempt_id: str) -> AttemptRevisionRow | None:
    return db.scalar(select(AttemptRevisionRow).where(AttemptRevisionRow.attempt_id == attempt_id))


def record_base_revision(db: Session, attempt: AttemptRow, *, fixture: Path, workspace: Path) -> AttemptRevisionRow:
    """Record the snapshot the Attempt starts from: its freshly copied workspace."""

    if revision_for_attempt(db, attempt.id) is not None:
        raise RevisionBindingError(f"attempt {attempt.id} already has a base revision")
    row = AttemptRevisionRow(
        id=f"REV-{uuid4().hex[:12]}", attempt_id=attempt.id, repository=repository_identity(fixture)[:500],
        tree_format=TREE_FORMAT, base_tree_sha256=tree_digest(workspace), created_at=utcnow(),
    )
    db.add(row)
    db.flush()
    return row


def close_revision(
    db: Session, attempt: AttemptRow, *, fixture: Path, workspace: Path,
    reported_result_tree: str | None = None, base_commit: str | None = None,
    base_commit_source: str | None = None,
) -> AttemptRevisionRow:
    """Record the snapshot the Attempt produced, before any artifact is written.

    Refuses (``RevisionBindingError``) when the Attempt has no base revision,
    when the fixture no longer matches the base (the diff would describe a
    different change), or when an adapter-reported result tree differs from
    the tree SDF computes now.
    """

    revision = revision_for_attempt(db, attempt.id)
    if revision is None:
        raise RevisionBindingError(f"attempt {attempt.id} has no base revision; refusing unbound artifacts")
    if revision.result_tree_sha256 is not None:
        raise RevisionBindingError(f"attempt {attempt.id} already has a result revision")
    from .oracle import HELD_OUT_DIR

    # The workspace copy omits the held-out directory. Compare the fixture
    # without it. The result tree below is the workspace as it stands, with
    # nothing skipped, so a file the agent wrote there still counts.
    fixture_tree = tree_digest(fixture, skip_names=frozenset({HELD_OUT_DIR}))
    if fixture_tree != revision.base_tree_sha256:
        raise RevisionBindingError(
            f"fixture changed since attempt {attempt.id} began (base {revision.base_tree_sha256[:12]}, "
            f"now {fixture_tree[:12]}); its diff would not describe the evaluated change"
        )
    result_tree = tree_digest(workspace)
    if reported_result_tree is not None and reported_result_tree != result_tree:
        raise RevisionBindingError(
            f"attempt {attempt.id} workspace changed after the adapter finished "
            f"(reported {str(reported_result_tree)[:12]}, now {result_tree[:12]})"
        )
    if base_commit is not None:
        if not isinstance(base_commit, str) or not _GIT_OID.fullmatch(base_commit):
            raise RevisionBindingError("base_commit must be a 40 or 64 character lowercase hex Git object id")
        revision.base_commit = base_commit
        revision.base_commit_source = (base_commit_source or "adapter")[:32]
    revision.result_tree_sha256 = result_tree
    revision.finished_at = utcnow()
    # Flush now: the PostgreSQL binding trigger must see the result tree
    # before any artifact or Evidence row that refers to it is inserted.
    db.flush()
    return revision


def revision_payload(revision: AttemptRevisionRow | None, attempt: AttemptRow | None = None) -> dict[str, Any] | None:
    if revision is None:
        return None
    payload = {
        "id": revision.id,
        "attempt_id": revision.attempt_id,
        "repository": revision.repository,
        "tree_format": revision.tree_format,
        "base_tree_sha256": revision.base_tree_sha256,
        "result_tree_sha256": revision.result_tree_sha256,
        "base_commit": revision.base_commit,
        "base_commit_source": revision.base_commit_source,
        "created_at": revision.created_at.isoformat() if revision.created_at else None,
        "finished_at": revision.finished_at.isoformat() if revision.finished_at else None,
    }
    if attempt is not None:
        # The identities are real digests of the server-side workspace in
        # either mode; the mode says whether a live sandbox produced them.
        payload["evidence_mode"] = attempt.evidence_mode
    return payload


def _sha256_file(path: Path) -> str | None:
    try:
        digest = hashlib.sha256()
        with open(path, "rb") as handle:
            for chunk in iter(lambda: handle.read(_CHUNK), b""):
                digest.update(chunk)
        return digest.hexdigest()
    except OSError:
        return None


def verify_attempt_revision(db: Session, attempt_id: str, *, check_files: bool = True) -> dict[str, Any]:
    """Re-check an Attempt's revision bindings and stored artifact digests.

    ``status`` is ``unbound`` (no revision: an Attempt from before migration
    0023 or one that never got a workspace), ``incomplete`` (no result
    revision yet), ``verified`` or ``failed`` with the problems listed.
    This verifies the records and the stored artifact bytes; it does not
    re-run any check and is not acceptance Evidence.
    """

    revision = revision_for_attempt(db, attempt_id)
    artifacts = db.scalars(select(ArtifactRow).where(ArtifactRow.attempt_id == attempt_id).order_by(ArtifactRow.id)).all()
    evidence = db.scalars(select(EvidenceRow).where(EvidenceRow.attempt_id == attempt_id).order_by(EvidenceRow.id)).all()
    problems: list[str] = []
    if revision is None:
        bound = [row.id for row in (*artifacts, *evidence) if row.revision_id is not None]
        if bound:
            problems.append(f"rows reference a revision but the attempt has none: {bound}")
        return {"attempt_id": attempt_id, "status": "failed" if problems else "unbound", "problems": problems,
                "artifacts": len(artifacts), "evidence": len(evidence)}
    for value, name in ((revision.base_tree_sha256, "base_tree_sha256"), (revision.result_tree_sha256, "result_tree_sha256")):
        if value is not None and not _HEX64.fullmatch(value):
            problems.append(f"revision {name} is not a sha256 hex digest")
    for kind, rows in (("artifact", artifacts), ("evidence", evidence)):
        for row in rows:
            if row.revision_id != revision.id:
                problems.append(f"{kind} {row.id} is not bound to revision {revision.id}")
            elif row.tree_sha256 != revision.result_tree_sha256:
                problems.append(f"{kind} {row.id} names tree {row.tree_sha256} but the revision result is "
                                f"{revision.result_tree_sha256}")
    if check_files:
        for row in artifacts:
            actual = _sha256_file(Path(row.uri))
            if actual is None:
                problems.append(f"artifact {row.id} file is missing")
            elif actual != row.sha256:
                problems.append(f"artifact {row.id} file digest does not match its recorded sha256")
    if problems:
        status = "failed"
    elif revision.result_tree_sha256 is None:
        status = "incomplete"
    else:
        status = "verified"
    return {"attempt_id": attempt_id, "status": status, "problems": problems,
            "artifacts": len(artifacts), "evidence": len(evidence)}
