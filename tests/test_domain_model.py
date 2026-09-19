from datetime import datetime, timezone

import pytest

from sdf_core.model import AttemptState, DecisionEdge, TaskState
from sdf_core.state import transition_attempt, transition_task
from sdf_core.db import Base, DecisionEdgeRow, GraphNodeRow, make_engine


def test_task_follows_decision_lifecycle():
    state = TaskState.CREATED
    for next_state in (TaskState.READY, TaskState.RUNNING, TaskState.EVALUATING, TaskState.SUCCEEDED):
        state = transition_task(state, next_state)
    assert state is TaskState.SUCCEEDED


def test_invalid_task_transition_is_rejected():
    with pytest.raises(ValueError, match="invalid task transition"):
        transition_task(TaskState.CREATED, TaskState.SUCCEEDED)


def test_attempt_can_fail_but_cannot_restart():
    state = AttemptState.CREATED
    for next_state in (AttemptState.DISPATCHED, AttemptState.RUNNING, AttemptState.FAILED):
        state = transition_attempt(state, next_state)
    with pytest.raises(ValueError, match="invalid attempt transition"):
        transition_attempt(state, AttemptState.RUNNING)


def test_decision_edge_preserves_provenance_and_confidence():
    edge = DecisionEdge(
        source_kind="evidence",
        source_id="EVIDENCE-001",
        target_kind="assumption",
        target_id="ASSUMPTION-001",
        relation="validates",
        source="evaluator:test",
        owner="sdf-core",
        confidence=0.95,
        created_at=datetime.now(timezone.utc),
        evidence_ref="artifact://test-log",
    )
    assert edge.relation == "validates"
    assert edge.evidence_ref == "artifact://test-log"


def test_relational_schema_has_nodes_and_edges_for_traceability():
    engine = make_engine()
    Base.metadata.create_all(engine)
    assert "graph_nodes" in Base.metadata.tables
    assert "decision_edges" in Base.metadata.tables
    assert DecisionEdgeRow.__table__.c.confidence.nullable is False
    assert GraphNodeRow.__table__.c.source.nullable is False
