"""enforce append-only Evidence in PostgreSQL

Revision ID: 0009_evidence_append_only
Revises: 0008_tool_audit_idempotency
"""

from alembic import op


revision = "0009_evidence_append_only"
down_revision = "0008_tool_audit_idempotency"
branch_labels = None
depends_on = None


_FUNCTION = "sdf_reject_evidence_mutation"
_TRIGGER = "trg_evidence_append_only"


def upgrade():
    # SQLite has no portable trigger body shared with PostgreSQL; its ORM
    # session guard remains active. PostgreSQL gets the database-level fence.
    if op.get_bind().dialect.name != "postgresql":
        return
    op.execute(f"""
        CREATE FUNCTION {_FUNCTION}() RETURNS trigger AS $$
        BEGIN
            RAISE EXCEPTION 'Evidence rows are append-only';
        END;
        $$ LANGUAGE plpgsql;
    """)
    op.execute(f"""
        CREATE TRIGGER {_TRIGGER}
        BEFORE UPDATE OR DELETE ON evidence
        FOR EACH ROW EXECUTE FUNCTION {_FUNCTION}();
    """)


def downgrade():
    if op.get_bind().dialect.name != "postgresql":
        return
    op.execute(f"DROP TRIGGER IF EXISTS {_TRIGGER} ON evidence")
    op.execute(f"DROP FUNCTION IF EXISTS {_FUNCTION}()")
