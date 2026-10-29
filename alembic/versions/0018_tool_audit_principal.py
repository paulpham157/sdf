"""Principal on Tool Proxy audits and Attempts; per-delivery decision rows (#51, ADR 0026)."""

from alembic import op
import sqlalchemy as sa


revision = "0018_tool_audit_principal"
down_revision = "0017_trace_id"
branch_labels = None
depends_on = None

_OLD_UNIQUE = "uq_tool_audits_attempt_action_event"
_NEW_UNIQUE = "uq_tool_audits_execution_event"
_WHERE = sa.text("event <> 'policy_decided'")


def upgrade():
    with op.batch_alter_table("tool_audits") as batch:
        batch.add_column(sa.Column("principal", sa.String(120), nullable=True))
        batch.create_index("ix_tool_audits_principal", ["principal"])
    # Execution events stay unique per action (idempotency fence); policy
    # decisions become one row per delivery so each principal is recorded.
    op.drop_index(_OLD_UNIQUE, table_name="tool_audits")
    op.create_index(_NEW_UNIQUE, "tool_audits", ["attempt_id", "action_id", "event"], unique=True,
                    sqlite_where=_WHERE, postgresql_where=_WHERE)
    with op.batch_alter_table("attempts") as batch:
        batch.add_column(sa.Column("principal", sa.String(120), nullable=True))
        batch.create_index("ix_attempts_principal", ["principal"])


def downgrade():
    with op.batch_alter_table("attempts") as batch:
        batch.drop_index("ix_attempts_principal")
        batch.drop_column("principal")
    op.drop_index(_NEW_UNIQUE, table_name="tool_audits")
    # The old index needs one policy_decided row per action: keep the earliest
    # and drop the per-delivery duplicates (lossy, documented in ADR 0026).
    postgres = op.get_bind().dialect.name == "postgresql"
    if postgres:
        op.execute(sa.text("ALTER TABLE tool_audits DISABLE TRIGGER trg_tool_audits_append_only"))
    op.execute(sa.text("""
        DELETE FROM tool_audits
        WHERE event = 'policy_decided' AND audit_id NOT IN (
            SELECT MIN(audit_id) FROM tool_audits
            WHERE event = 'policy_decided'
            GROUP BY attempt_id, action_id
        )
    """))
    if postgres:
        op.execute(sa.text("ALTER TABLE tool_audits ENABLE TRIGGER trg_tool_audits_append_only"))
    op.create_index(_OLD_UNIQUE, "tool_audits", ["attempt_id", "action_id", "event"], unique=True)
    with op.batch_alter_table("tool_audits") as batch:
        batch.drop_index("ix_tool_audits_principal")
        batch.drop_column("principal")
