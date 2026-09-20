"""enforce append-only Tool Proxy audits in PostgreSQL

Revision ID: 0011_tool_audit_append_only
Revises: 0010_decision_edge_integrity
"""

from alembic import op


revision = "0011_tool_audit_append_only"
down_revision = "0010_decision_edge_integrity"
branch_labels = None
depends_on = None


_FUNCTION = "sdf_reject_tool_audit_mutation"
_TRIGGER = "trg_tool_audits_append_only"


def upgrade():
    # SQLite keeps the equivalent SQLAlchemy session guard; PostgreSQL gets a
    # database-level fence for direct writes and other connection owners.
    if op.get_bind().dialect.name != "postgresql":
        return
    op.execute(f"""
        CREATE FUNCTION {_FUNCTION}() RETURNS trigger AS $$
        BEGIN
            RAISE EXCEPTION 'Tool audit rows are append-only';
        END;
        $$ LANGUAGE plpgsql;
    """)
    op.execute(f"""
        CREATE TRIGGER {_TRIGGER}
        BEFORE UPDATE OR DELETE ON tool_audits
        FOR EACH ROW EXECUTE FUNCTION {_FUNCTION}();
    """)


def downgrade():
    if op.get_bind().dialect.name != "postgresql":
        return
    op.execute(f"DROP TRIGGER IF EXISTS {_TRIGGER} ON tool_audits")
    op.execute(f"DROP FUNCTION IF EXISTS {_FUNCTION}()")
