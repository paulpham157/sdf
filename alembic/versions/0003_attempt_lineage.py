"""add attempt lineage and model-cost metadata

Revision ID: 0003_attempt_lineage
Revises: 0002_evidence_criterion
"""
from alembic import op
import sqlalchemy as sa

revision = "0003_attempt_lineage"
down_revision = "0002_evidence_criterion"
branch_labels = None
depends_on = None


def upgrade():
    op.add_column("attempts", sa.Column("parent_attempt_id", sa.String(120), nullable=True))
    op.add_column("attempts", sa.Column("model_tier", sa.String(32), nullable=False, server_default="basic"))
    op.add_column("attempts", sa.Column("cost_usd", sa.Float(), nullable=False, server_default="0"))
    op.create_index("ix_attempts_parent_attempt_id", "attempts", ["parent_attempt_id"])


def downgrade():
    op.drop_index("ix_attempts_parent_attempt_id", table_name="attempts")
    op.drop_column("attempts", "cost_usd")
    op.drop_column("attempts", "model_tier")
    op.drop_column("attempts", "parent_attempt_id")
