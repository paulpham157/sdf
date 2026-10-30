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


def test_migration_0023_attempt_revisions_round_trips_on_sqlite(tmp_path, monkeypatch):
    """#62: 0023 adds the revision table and nullable bindings; legacy rows stay unbound and untouched."""

    from sqlalchemy import text

    database_url = f"sqlite:///{tmp_path / 'm0023.sqlite'}"
    monkeypatch.setenv("SDF_DATABASE_URL", database_url)
    config = Config("alembic.ini")
    engine = create_engine(database_url)

    def evidence_ddl():
        with engine.connect() as connection:
            return connection.execute(text("SELECT sql FROM sqlite_master WHERE name = 'evidence'")).scalar()

    command.upgrade(config, "0022_tool_audit_mode_check")
    with engine.begin() as connection:
        connection.execute(text(
            "INSERT INTO evidence (id, attempt_id, kind, status, command, confidence, evidence_mode, created_at) "
            "VALUES ('EV-LEGACY', 'ATTEMPT-LEGACY', 'evaluation', 'PASS', 'c', 1.0, 'synthetic', '2026-10-01')"))
        connection.execute(text(
            "INSERT INTO artifacts (id, attempt_id, kind, uri, sha256, size_bytes, created_at) "
            "VALUES ('ART-LEGACY', 'ATTEMPT-LEGACY', 'diff', 'x', :sha, 0, '2026-10-01')"), {"sha": "0" * 64})
    command.upgrade(config, "head")
    inspector = inspect(engine)
    assert "attempt_revisions" in inspector.get_table_names()
    for table in ("artifacts", "evidence"):
        assert {"revision_id", "tree_sha256"} <= {c["name"] for c in inspector.get_columns(table)}
    assert "ck_evidence_evidence_mode" in evidence_ddl()  # no rebuild dropped the CHECK
    with engine.connect() as connection:
        assert connection.execute(text("SELECT revision_id, tree_sha256 FROM evidence")).all() == [(None, None)]
        assert connection.execute(text("SELECT revision_id, tree_sha256 FROM artifacts")).all() == [(None, None)]

    command.downgrade(config, "0022_tool_audit_mode_check")
    inspector = inspect(engine)
    assert "attempt_revisions" not in inspector.get_table_names()
    assert "revision_id" not in {c["name"] for c in inspector.get_columns("evidence")}
    assert "ck_evidence_evidence_mode" in evidence_ddl()
    with engine.connect() as connection:
        assert connection.execute(text("SELECT id FROM evidence")).scalars().all() == ["EV-LEGACY"]
    command.upgrade(config, "head")
    assert "attempt_revisions" in inspect(engine).get_table_names()
    engine.dispose()
