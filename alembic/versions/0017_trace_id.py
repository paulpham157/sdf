"""OpenTelemetry trace id on Attempts and agent runs (ADR 0024)."""

from alembic import op
import sqlalchemy as sa


revision = "0017_trace_id"
down_revision = "0016_usage_ledger"
branch_labels = None
depends_on = None


def upgrade():
    with op.batch_alter_table("attempts") as batch:
        batch.add_column(sa.Column("trace_id", sa.String(32), nullable=True))
    with op.batch_alter_table("agent_runs") as batch:
        batch.add_column(sa.Column("trace_id", sa.String(32), nullable=True))


def downgrade():
    with op.batch_alter_table("agent_runs") as batch:
        batch.drop_column("trace_id")
    with op.batch_alter_table("attempts") as batch:
        batch.drop_column("trace_id")
