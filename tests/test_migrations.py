from alembic import command
from alembic.config import Config
import pytest
from sqlalchemy import create_engine, inspect
from sqlalchemy.orm import sessionmaker


def test_alembic_upgrade_head_is_valid_from_empty_sqlite(tmp_path, monkeypatch):
    database_url = f"sqlite:///{tmp_path / 'migration.sqlite'}"
    monkeypatch.setenv("SDF_DATABASE_URL", database_url)
    config = Config("alembic.ini")

    command.upgrade(config, "head")

    inspector = inspect(create_engine(database_url))
    assert "runtime_events" in inspector.get_table_names()
    assert {column["name"] for column in inspector.get_columns("attempts")} >= {
        "parent_attempt_id", "model_tier", "cost_usd", "provider", "latency_ms",
        "input_tokens", "output_tokens", "escalation_reason",
    }
    assert "measured_at" in {column["name"] for column in inspector.get_columns("evidence")}


def test_evidence_rows_are_append_only_at_session_boundary():
    from sdf_core.db import Base, EvidenceRow, make_engine
    from sdf_core.model import utcnow

    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        row = EvidenceRow(
            id="EVIDENCE-IMMUTABLE-001", attempt_id="ATTEMPT-IMMUTABLE-001",
            kind="evaluation", status="PASS", command="pytest", exit_code=0,
            artifact_ref="ARTIFACT-IMMUTABLE-001", confidence=1.0,
            criterion="tests pass", measured_at=utcnow(), created_at=utcnow(),
        )
        db.add(row)
        db.commit()
        row.status = "FAIL"
        with pytest.raises(ValueError, match="append-only"):
            db.commit()
        db.rollback()
        db.delete(row)
        with pytest.raises(ValueError, match="append-only"):
            db.commit()
