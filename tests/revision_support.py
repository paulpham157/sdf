"""Test helper: bind hand-written artifact/Evidence rows to a revision (ADR 0032).

Since migration 0023 every Attempt created after it needs a revision before
it may have artifacts or Evidence; tests that write such rows directly (to
exercise scorecards, routing, ...) give their Attempt a finished revision.
"""

from __future__ import annotations

from sdf_core.db import AttemptRevisionRow
from sdf_core.model import utcnow
from sdf_core.revisions import TREE_FORMAT


def bound(db, attempt_id: str, tree: str = "a" * 64) -> dict[str, str]:
    """Add (once) a finished revision for ``attempt_id``; return the binding kwargs."""

    revision_id = f"REV-T-{attempt_id}"[:120]
    if db.get(AttemptRevisionRow, revision_id) is None and not any(
            isinstance(item, AttemptRevisionRow) and item.id == revision_id for item in db.new):
        db.add(AttemptRevisionRow(id=revision_id, attempt_id=attempt_id, repository="fixture:test",
                                  tree_format=TREE_FORMAT, base_tree_sha256=tree, result_tree_sha256=tree,
                                  created_at=utcnow(), finished_at=utcnow()))
    return {"revision_id": revision_id, "tree_sha256": tree}
