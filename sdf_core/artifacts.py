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
        destination.mkdir(parents=True)
        for source in fixture.rglob("*"):
            relative = source.relative_to(fixture)
            target = destination / relative
            if source.is_dir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(source.read_bytes())
        return destination


class ArtifactStore:
    def __init__(self, root: Path):
        self.root = root

    def capture_diff(self, *, artifact_id: str, before: Path, after: Path, files: Iterable[str]) -> Artifact:
        chunks: list[str] = []
        for relative in files:
            old = (before / relative).read_text(encoding="utf-8").splitlines(keepends=True) if (before / relative).exists() else []
            new = (after / relative).read_text(encoding="utf-8").splitlines(keepends=True) if (after / relative).exists() else []
            chunks.extend(difflib.unified_diff(old, new, fromfile=f"a/{relative}", tofile=f"b/{relative}"))
        return self._write(artifact_id, "diff", "".join(chunks).encode("utf-8"))

    def capture_process(self, *, artifact_id: str, stdout: str, stderr: str, exit_code: int) -> Artifact:
        payload = json.dumps({"stdout": stdout, "stderr": stderr, "exit_code": exit_code}, ensure_ascii=False, indent=2).encode("utf-8")
        return self._write(artifact_id, "process_log", payload)

    def capture_evaluator_output(self, *, artifact_id: str, evidence: Evidence) -> Artifact:
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
