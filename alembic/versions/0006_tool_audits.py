"""persist Attempt-bound Tool Proxy audit records

Revision ID: 0006_tool_audits
Revises: 0005_impact_measurement
"""
from alembic import op
import sqlalchemy as sa

revision = "0006_tool_audits"
down_revision = "0005_impact_measurement"
branch_labels = None
depends_on = None


def upgrade():
    op.create_table(
        "tool_audits",
        sa.Column("audit_id", sa.String(120), primary_key=True),
        sa.Column("attempt_id", sa.String(120), nullable=False),
        sa.Column("action_id", sa.String(120), nullable=False),
        sa.Column("actor", sa.String(120), nullable=False),
        sa.Column("tool", sa.String(120), nullable=False),
        sa.Column("action", sa.String(120), nullable=False),
        sa.Column("resource", sa.String(500), nullable=False),
        sa.Column("context", sa.JSON(), nullable=False),
        sa.Column("event", sa.String(64), nullable=False),
        sa.Column("decision", sa.String(32), nullable=False),
        sa.Column("executed", sa.Boolean(), nullable=False, server_default=sa.false()),
        sa.Column("outcome", sa.String(64), nullable=False),
        sa.Column("detail", sa.String(1000), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
    )
    op.create_index("ix_tool_audits_attempt_id", "tool_audits", ["attempt_id"])
    op.create_index("ix_tool_audits_action_id", "tool_audits", ["action_id"])


def downgrade():
    op.drop_index("ix_tool_audits_action_id", table_name="tool_audits")
    op.drop_index("ix_tool_audits_attempt_id", table_name="tool_audits")
    op.drop_table("tool_audits")
