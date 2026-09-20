"""add impact and runtime measurement fields

Revision ID: 0005_impact_measurement
Revises: 0004_runtime_events
"""
from alembic import op
import sqlalchemy as sa

revision = "0005_impact_measurement"
down_revision = "0004_runtime_events"
branch_labels = None
depends_on = None


def upgrade():
    op.add_column("attempts", sa.Column("provider", sa.String(120), nullable=True))
    op.add_column("attempts", sa.Column("latency_ms", sa.Float(), nullable=True))
    op.add_column("attempts", sa.Column("input_tokens", sa.Integer(), nullable=True))
    op.add_column("attempts", sa.Column("output_tokens", sa.Integer(), nullable=True))
    op.add_column("attempts", sa.Column("escalation_reason", sa.String(500), nullable=True))
    op.add_column("evidence", sa.Column("measured_at", sa.DateTime(timezone=True), nullable=True))


def downgrade():
    op.drop_column("evidence", "measured_at")
    op.drop_column("attempts", "escalation_reason")
    op.drop_column("attempts", "output_tokens")
    op.drop_column("attempts", "input_tokens")
    op.drop_column("attempts", "latency_ms")
    op.drop_column("attempts", "provider")
