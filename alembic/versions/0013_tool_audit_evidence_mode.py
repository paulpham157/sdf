"""add evidence mode to Tool Proxy audits

Revision ID: 0013_tool_audit_evidence_mode
Revises: 0012_impact_attribution
"""

from alembic import op
import sqlalchemy as sa


revision = "0013_tool_audit_evidence_mode"
down_revision = "0012_impact_attribution"
branch_labels = None
depends_on = None


def upgrade():
    op.add_column("tool_audits", sa.Column("evidence_mode", sa.String(16), nullable=False, server_default="synthetic"))


def downgrade():
    op.drop_column("tool_audits", "evidence_mode")
