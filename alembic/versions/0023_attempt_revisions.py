"""Attempt revisions and revision-bound artifacts/Evidence (#62, ADR 0032).

Adds ``attempt_revisions`` (one row per Attempt: repository identity, base
tree, optional base commit, result tree) and nullable ``revision_id`` /
``tree_sha256`` columns on ``artifacts`` and ``evidence``.

Existing rows are never rewritten: Evidence is append-only and rows from
before this revision keep NULL bindings, which the API reports as
``unbound``. No backfill is attempted, because the snapshot those Attempts
evaluated was never recorded and must not be guessed.

The columns are plain nullable ``ADD COLUMN`` on both dialects (no SQLite
table rebuild, so no index, trigger or CHECK is touched). ``ADD COLUMN`` on
PostgreSQL neither updates rows nor fires the Evidence append-only trigger.

PostgreSQL also gets the database fences that the ORM session guards
(``sdf_core.db``) provide on SQLite:

* ``trg_attempt_revisions_guard``: no DELETE; ``id``, ``attempt_id``,
  ``repository``, ``tree_format``, ``base_tree_sha256`` and ``created_at``
  are immutable; ``base_commit``, ``base_commit_source``,
  ``result_tree_sha256`` and ``finished_at`` are set at most once.
* ``trg_artifacts_revision_binding`` / ``trg_evidence_revision_binding``
  (BEFORE INSERT): a row for an Attempt that has a revision must name that
  revision and its result tree; a row may not name another Attempt's
  revision or an unknown one. Rows for Attempts without a revision (from
  before this migration) may stay unbound.

Downgrade drops the fences, the four columns and the table. It discards the
recorded revisions.
"""

import sqlalchemy as sa

from alembic import op

revision = "0023_attempt_revisions"
down_revision = "0022_tool_audit_mode_check"
branch_labels = None
depends_on = None

_BOUND = ("artifacts", "evidence")


def upgrade():
    op.create_table(
        "attempt_revisions",
        sa.Column("id", sa.String(120), primary_key=True),
        sa.Column("attempt_id", sa.String(120), nullable=False),
        sa.Column("repository", sa.String(500), nullable=False),
        sa.Column("tree_format", sa.String(32), nullable=False),
        sa.Column("base_tree_sha256", sa.String(64), nullable=False),
        sa.Column("base_commit", sa.String(64), nullable=True),
        sa.Column("base_commit_source", sa.String(32), nullable=True),
        sa.Column("result_tree_sha256", sa.String(64), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("finished_at", sa.DateTime(timezone=True), nullable=True),
    )
    op.create_index("ix_attempt_revisions_attempt_id", "attempt_revisions", ["attempt_id"], unique=True)
    for table in _BOUND:
        op.add_column(table, sa.Column("revision_id", sa.String(120), nullable=True))
        op.add_column(table, sa.Column("tree_sha256", sa.String(64), nullable=True))

    if op.get_bind().dialect.name != "postgresql":
        return  # SQLite relies on the equivalent session guards in sdf_core.db.
    op.execute("""
        CREATE FUNCTION sdf_attempt_revision_guard() RETURNS trigger AS $$
        BEGIN
            IF TG_OP = 'DELETE' THEN
                RAISE EXCEPTION 'attempt revision rows cannot be deleted';
            END IF;
            IF NEW.id IS DISTINCT FROM OLD.id OR NEW.attempt_id IS DISTINCT FROM OLD.attempt_id
               OR NEW.repository IS DISTINCT FROM OLD.repository OR NEW.tree_format IS DISTINCT FROM OLD.tree_format
               OR NEW.base_tree_sha256 IS DISTINCT FROM OLD.base_tree_sha256
               OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
                RAISE EXCEPTION 'an attempt revision''s base identity is immutable';
            END IF;
            IF (OLD.base_commit IS NOT NULL AND NEW.base_commit IS DISTINCT FROM OLD.base_commit)
               OR (OLD.base_commit_source IS NOT NULL AND NEW.base_commit_source IS DISTINCT FROM OLD.base_commit_source)
               OR (OLD.result_tree_sha256 IS NOT NULL AND NEW.result_tree_sha256 IS DISTINCT FROM OLD.result_tree_sha256)
               OR (OLD.finished_at IS NOT NULL AND NEW.finished_at IS DISTINCT FROM OLD.finished_at) THEN
                RAISE EXCEPTION 'attempt revision result fields are set once and cannot change';
            END IF;
            RETURN NEW;
        END;
        $$ LANGUAGE plpgsql;
    """)
    op.execute("""
        CREATE TRIGGER trg_attempt_revisions_guard BEFORE UPDATE OR DELETE ON attempt_revisions
        FOR EACH ROW EXECUTE FUNCTION sdf_attempt_revision_guard();
    """)
    op.execute("""
        CREATE FUNCTION sdf_revision_binding_guard() RETURNS trigger AS $$
        DECLARE
            own_id text;
            own_tree text;
            other_attempt text;
        BEGIN
            SELECT id, result_tree_sha256 INTO own_id, own_tree FROM attempt_revisions WHERE attempt_id = NEW.attempt_id;
            IF NEW.revision_id IS NULL THEN
                IF own_id IS NOT NULL THEN
                    RAISE EXCEPTION 'missing revision binding: % % has none but attempt % has revision %',
                        TG_TABLE_NAME, NEW.id, NEW.attempt_id, own_id;
                END IF;
                IF NEW.tree_sha256 IS NOT NULL THEN
                    RAISE EXCEPTION 'revision binding: % % names a tree without a revision', TG_TABLE_NAME, NEW.id;
                END IF;
                RETURN NEW;
            END IF;
            IF own_id IS NULL OR own_id <> NEW.revision_id THEN
                SELECT attempt_id INTO other_attempt FROM attempt_revisions WHERE id = NEW.revision_id;
                IF other_attempt IS NOT NULL THEN
                    RAISE EXCEPTION 'cross-Attempt revision binding: % % of attempt % refers to a revision of attempt %',
                        TG_TABLE_NAME, NEW.id, NEW.attempt_id, other_attempt;
                END IF;
                RAISE EXCEPTION 'revision binding: % % refers to unknown revision %', TG_TABLE_NAME, NEW.id, NEW.revision_id;
            END IF;
            IF own_tree IS NULL OR NEW.tree_sha256 IS DISTINCT FROM own_tree THEN
                RAISE EXCEPTION 'mismatched revision: % % does not name the result tree of revision %',
                    TG_TABLE_NAME, NEW.id, own_id;
            END IF;
            RETURN NEW;
        END;
        $$ LANGUAGE plpgsql;
    """)
    for table in _BOUND:
        op.execute(f"""
            CREATE TRIGGER trg_{table}_revision_binding BEFORE INSERT ON {table}
            FOR EACH ROW EXECUTE FUNCTION sdf_revision_binding_guard();
        """)


def downgrade():
    if op.get_bind().dialect.name == "postgresql":
        for table in _BOUND:
            op.execute(f"DROP TRIGGER IF EXISTS trg_{table}_revision_binding ON {table}")
        op.execute("DROP TRIGGER IF EXISTS trg_attempt_revisions_guard ON attempt_revisions")
        op.execute("DROP FUNCTION IF EXISTS sdf_revision_binding_guard()")
        op.execute("DROP FUNCTION IF EXISTS sdf_attempt_revision_guard()")
    for table in _BOUND:
        # Plain DROP COLUMN (SQLite >= 3.35): the columns carry no index or constraint.
        op.drop_column(table, "tree_sha256")
        op.drop_column(table, "revision_id")
    op.drop_index("ix_attempt_revisions_attempt_id", table_name="attempt_revisions")
    op.drop_table("attempt_revisions")
