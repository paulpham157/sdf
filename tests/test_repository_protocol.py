"""Checks for the SDF Repository Protocol (ADR 0028, docs/agents/workflow.md).

The protocol is documentation; this keeps its structure honest: one canonical
workflow guide routed from AGENTS.md, links that resolve, and no parallel
policy/Evidence store in the repository.
"""

from __future__ import annotations

import re
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = "docs/agents/workflow.md"
ADR = "docs/adr/0028-sdf-repository-protocol.md"
FORBIDDEN_STORES = (".sdf/policies", ".sdf/evidence")

_PATH_REF = re.compile(r"`((?:docs|tests)/[A-Za-z0-9_./-]+\.(?:md|py))`|\]\(([^)#\s]+\.md)(?:#[^)]*)?\)")


def referenced_paths(markdown: str, base: Path) -> list[Path]:
    """Repository paths a Markdown file names in backticks or relative links."""
    paths: list[Path] = []
    for code_ref, link_ref in _PATH_REF.findall(markdown):
        if code_ref:
            paths.append(ROOT / code_ref)
        elif not link_ref.startswith(("http://", "https://")):
            paths.append((base / link_ref).resolve())
    return paths


def missing_references(doc: Path) -> list[str]:
    text = doc.read_text(encoding="utf-8")
    return [str(p.relative_to(ROOT)) for p in referenced_paths(text, doc.parent) if not p.exists()]


def test_agents_router_points_to_the_one_workflow_guide() -> None:
    agents = (ROOT / "AGENTS.md").read_text(encoding="utf-8")
    assert WORKFLOW in agents
    # A router, not a copied handbook.
    assert len(agents.splitlines()) <= 40, "AGENTS.md must stay a short router (ADR 0028)"
    assert (ROOT / WORKFLOW).is_file()
    assert (ROOT / ADR).is_file()


@pytest.mark.parametrize("doc", [WORKFLOW, ADR, "AGENTS.md"])
def test_protocol_references_resolve(doc: str) -> None:
    assert missing_references(ROOT / doc) == []


def test_reference_check_reports_a_missing_path(tmp_path: Path) -> None:
    doc = tmp_path / "x.md"
    doc.write_text("See `docs/agents/does-not-exist.md` and [ok](../README.md).", encoding="utf-8")
    missing = [str(p) for p in referenced_paths(doc.read_text(), doc.parent) if not p.exists()]
    assert any(m.endswith("docs/agents/does-not-exist.md") for m in missing)


def test_workflow_names_every_evidence_class() -> None:
    text = (ROOT / WORKFLOW).read_text(encoding="utf-8").lower()
    for evidence_class in (
        "available validation",
        "executed local result",
        "ci run / result",
        "branch protection",
        "runtime observation",
        "human approval",
    ):
        assert evidence_class in text, evidence_class


@pytest.mark.parametrize("store", FORBIDDEN_STORES)
def test_no_parallel_policy_or_evidence_store(store: str) -> None:
    assert not (ROOT / store).exists(), f"{store} would be a second store; ADR 0028 rejects it"
