from sdf_core.db import AttemptRow, Base, DecisionEdgeRow, GraphNodeRow, make_engine
from sdf_core.escalation import ModelTier, create_escalated_attempt
from sdf_core.model import utcnow
from sqlalchemy.orm import sessionmaker


def test_escalated_attempt_preserves_parent_lineage_and_is_idempotent():
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        prior = AttemptRow(
            id="ATTEMPT-001", task_id="TASK-001", status="failed", dispatch_key="dispatch-1",
            agent="codex", created_at=utcnow(), model_tier="basic", cost_usd=0.2,
        )
        db.add_all([
            prior,
            GraphNodeRow(id="TASK-001", kind="task", title="task", source="test", owner="sdf", confidence=1.0, created_at=utcnow()),
        ])
        db.commit()

        child = create_escalated_attempt(
            db, prior=prior, dispatch_key="dispatch-2", model_tier=ModelTier.MEDIUM,
            agent="codex", cost_usd=0.5,
        )
        same = create_escalated_attempt(
            db, prior=prior, dispatch_key="dispatch-2", model_tier=ModelTier.MEDIUM,
            agent="codex", cost_usd=0.5,
        )
        db.commit()

        assert same.id == child.id
        assert child.parent_attempt_id == prior.id
        assert child.model_tier == "medium"
        assert child.cost_usd == 0.5
        edge = db.query(DecisionEdgeRow).filter_by(source_id=child.id, relation="depends_on").one()
        assert edge.target_id == prior.id
