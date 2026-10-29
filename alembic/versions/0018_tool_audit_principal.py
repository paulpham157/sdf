"""Authenticated principal on Tool Proxy audit rows (#51)."""

from alembic import op
import sqlalchemy as sa


revision = "0018_tool_audit_principal"
down_revision = "0017_trace_id"
branch_labels = None
depends_on = None


def upgrade():
    with op.batch_alter_table("tool_audits") as batch:
        batch.add_column(sa.Column("principal", sa.String(120), nullable=True))
        batch.create_index("ix_tool_audits_principal", ["principal"])


def downgrade():
    with op.batch_alter_table("tool_audits") as batch:
        batch.drop_index("ix_tool_audits_principal")
        batch.drop_column("principal")
