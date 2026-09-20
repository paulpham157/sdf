"""store normalized runtime lifecycle events

Revision ID: 0004_runtime_events
Revises: 0003_attempt_lineage
"""
from alembic import op
import sqlalchemy as sa

revision = "0004_runtime_events"
down_revision = "0003_attempt_lineage"
branch_labels = None
depends_on = None


def upgrade():
    op.create_table(
        "runtime_events",
        sa.Column("id", sa.Integer(), primary_key=True, autoincrement=True),
        sa.Column("attempt_id", sa.String(120), nullable=False),
        sa.Column("session_id", sa.String(120), nullable=False),
        sa.Column("sequence", sa.Integer(), nullable=False),
        sa.Column("kind", sa.String(64), nullable=False),
        sa.Column("status", sa.String(32), nullable=False),
        sa.Column("payload", sa.JSON(), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
    )
    op.create_index("ix_runtime_events_attempt_id", "runtime_events", ["attempt_id"])
    op.create_index("ix_runtime_events_session_id", "runtime_events", ["session_id"])


def downgrade():
    op.drop_index("ix_runtime_events_session_id", table_name="runtime_events")
    op.drop_index("ix_runtime_events_attempt_id", table_name="runtime_events")
    op.drop_table("runtime_events")
