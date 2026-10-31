from __future__ import annotations

import difflib
import hashlib
import json
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable

from .evaluator import Evidence


@dataclass(frozen=True)
class Artifact:
    artifact_id: str
    kind: str
    path: Path
    sha256: str
    size_bytes: int


class WorkspaceManager:
    def __init__(self, root: Path):
        self.root = root

    def create(self, attempt_id: str, fixture: Path) -> Path:
        destination = self.root / attempt_id
        if destination.exists():
            raise FileExistsError(f"workspace already exists: {attempt_id}")
        # A symlink in the fixture would be followed by the copy below and pull
        # its host target (any file the server can read) into the agent's
        # workspace, so a fixture with one is refused before anything is copied.
        sources = sorted(fixture.rglob("*"))
        for source in sources:
            if source.is_symlink():
                raise ValueError(f"fixture must not contain symlinks: {source.relative_to(fixture).as_posix()}")
        destination.mkdir(parents=True)
        for source in sources:
            relative = source.relative_to(fixture)
            target = destination / relative
            if source.is_dir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(source.read_bytes())
                # Keep the execute bit so the workspace has the fixture's
                # revision identity (ADR 0032); only the owner bit is set.
                if source.stat().st_mode & 0o100:
                    target.chmod(target.stat().st_mode | 0o100)  # owner only
        return destination


def _text_lines(path: Path) -> list[str] | None:
    """A file's UTF-8 lines, ``[]`` when absent, ``None`` when it is not text."""

    if not path.exists():
        return []
    try:
        return path.read_text(encoding="utf-8").splitlines(keepends=True)
    except UnicodeDecodeError:
        return None


class ArtifactStore:
    def __init__(self, root: Path):
        self.root = root

    def capture_diff(self, *, artifact_id: str, before: Path, after: Path, files: Iterable[str]) -> Artifact:
        chunks: list[str] = []
        for relative in files:
            old, new = _text_lines(before / relative), _text_lines(after / relative)
            if old is None or new is None:
                # A binary file (e.g. an agent's ``__pycache__``) is named, not decoded.
                chunks.append(f"Binary files a/{relative} and b/{relative} differ\n")
                continue
            chunks.extend(difflib.unified_diff(old, new, fromfile=f"a/{relative}", tofile=f"b/{relative}"))
        return self._write(artifact_id, "diff", "".join(chunks).encode("utf-8"))

    def capture_process(self, *, artifact_id: str, stdout: str, stderr: str, exit_code: int) -> Artifact:
        payload = json.dumps({"stdout": stdout, "stderr": stderr, "exit_code": exit_code}, ensure_ascii=False, indent=2).encode("utf-8")
        return self._write(artifact_id, "process_log", payload)

    def capture_evaluator_output(self, *, artifact_id: str, evidence: Evidence,
                                 revision: dict[str, str | None] | None = None) -> Artifact:
        """Persist the evaluator's output as the provenance artifact for Evidence.

        The evaluator output is intentionally separate from the agent process
        log. Callers can therefore prove which deterministic check produced an
        Evidence record without treating agent narration as a test result.
        """
        payload = json.dumps(
            {
                "attempt_id": evidence.attempt_id,
                "evidence_id": evidence.evidence_id,
                "criterion": evidence.criterion,
                "status": evidence.status,
                "command": evidence.command,
                "exit_code": evidence.exit_code,
                "stdout": evidence.stdout,
                "stderr": evidence.stderr,
                "confidence": evidence.confidence,
                "runner": evidence.runner,
                "cleanup_error": evidence.cleanup_error,
                # ADR 0032 (#62): the Attempt revision and tree this check ran against.
                **({"revision": dict(revision)} if revision is not None else {}),
            },
            ensure_ascii=False,
            indent=2,
        ).encode("utf-8")
        return self._write(artifact_id, "evaluator_output", payload)

    def _write(self, artifact_id: str, kind: str, payload: bytes) -> Artifact:
        self.root.mkdir(parents=True, exist_ok=True)
        path = self.root / f"{artifact_id}.{kind}"
        if path.exists():
            raise FileExistsError(f"artifact already exists: {artifact_id}")
        path.write_bytes(payload)
        digest = hashlib.sha256(payload).hexdigest()
        return Artifact(artifact_id, kind, path, digest, len(payload))
