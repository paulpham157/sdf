"""make Attempt-bound Tool Proxy audit replay idempotent

Revision ID: 0008_tool_audit_idempotency
Revises: 0007_runtime_event_idempotency
"""

from alembic import op
import sqlalchemy as sa


revision = "0008_tool_audit_idempotency"
down_revision = "0007_runtime_event_idempotency"
branch_labels = None
depends_on = None


_CONSTRAINT = "uq_tool_audits_attempt_action_event"


def upgrade():
    # A prior sink could redeliver the same policy/execution event with a new
    # random audit_id. Keep the earliest observation before enforcing replay
    # identity for populated databases.
    op.execute(sa.text("""
        DELETE FROM tool_audits
        WHERE audit_id NOT IN (
            SELECT MIN(audit_id)
            FROM tool_audits
            GROUP BY attempt_id, action_id, event
        )
    """))
    # A unique index is portable across PostgreSQL and SQLite.
    op.create_index(
        _CONSTRAINT,
        "tool_audits",
        ["attempt_id", "action_id", "event"],
        unique=True,
    )


def downgrade():
    op.drop_index(_CONSTRAINT, table_name="tool_audits")
