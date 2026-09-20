from __future__ import annotations

import pytest
from sqlalchemy import func, select
from sqlalchemy.orm import sessionmaker

from sdf_core.db import Base, DecisionEdgeRow, GraphNodeRow, make_engine
from sdf_core.model import utcnow


def _node(node_id: str) -> GraphNodeRow:
    return GraphNodeRow(
        id=node_id,
        kind="decision",
        title=node_id,
        source="test",
        owner="integrity-tests",
        confidence=1.0,
        created_at=utcnow(),
    )


def _edge(*, source_id: str, target_id: str) -> DecisionEdgeRow:
    return DecisionEdgeRow(
        source_kind="decision",
        source_id=source_id,
        target_kind="decision",
        target_id=target_id,
        relation="depends_on",
        source="test",
        owner="integrity-tests",
        confidence=1.0,
        created_at=utcnow(),
    )


def _session():
    engine = make_engine()
    Base.metadata.create_all(engine)
    return sessionmaker(engine, expire_on_commit=False)()


def test_edge_can_reference_nodes_created_in_the_same_transaction():
    with _session() as db:
        db.add_all([_node("NODE-SAME-SOURCE"), _node("NODE-SAME-TARGET")])
        db.add(_edge(source_id="NODE-SAME-SOURCE", target_id="NODE-SAME-TARGET"))

        db.commit()

        assert db.scalar(select(func.count()).select_from(DecisionEdgeRow)) == 1


def test_edge_can_reference_nodes_committed_by_an_earlier_transaction():
    with _session() as db:
        db.add_all([_node("NODE-EXISTING-SOURCE"), _node("NODE-EXISTING-TARGET")])
        db.commit()

        db.add(_edge(source_id="NODE-EXISTING-SOURCE", target_id="NODE-EXISTING-TARGET"))
        db.commit()

        edge = db.scalar(select(DecisionEdgeRow))
        assert edge is not None
        assert edge.source_id == "NODE-EXISTING-SOURCE"
        assert edge.target_id == "NODE-EXISTING-TARGET"


@pytest.mark.parametrize(
    ("source_id", "target_id", "missing_id"),
    [
        ("NODE-MISSING-SOURCE", "NODE-VALID-TARGET", "NODE-MISSING-SOURCE"),
        ("NODE-VALID-SOURCE", "NODE-MISSING-TARGET", "NODE-MISSING-TARGET"),
    ],
)
def test_edge_rejects_a_dangling_source_or_target(source_id: str, target_id: str, missing_id: str):
    with _session() as db:
        valid_id = "NODE-VALID-TARGET" if source_id == "NODE-MISSING-SOURCE" else "NODE-VALID-SOURCE"
        db.add(_node(valid_id))
        db.commit()

        db.add(_edge(source_id=source_id, target_id=target_id))
        with pytest.raises(ValueError, match=rf"decision edge references missing graph node.*{missing_id}"):
            db.commit()

        db.rollback()
        assert db.scalar(select(func.count()).select_from(DecisionEdgeRow)) == 0

