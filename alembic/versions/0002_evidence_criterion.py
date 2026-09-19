"""store criterion identity on Evidence

Revision ID: 0002_evidence_criterion
Revises: 0001_initial
"""
from alembic import op
import sqlalchemy as sa

revision = "0002_evidence_criterion"
down_revision = "0001_initial"
branch_labels = None
depends_on = None


def upgrade():
    op.add_column("evidence", sa.Column("criterion", sa.String(500), nullable=True))


def downgrade():
    op.drop_column("evidence", "criterion")
