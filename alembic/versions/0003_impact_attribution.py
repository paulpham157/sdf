"""add impact attribution fields and objective metric tables

Revision ID: 0012_impact_attribution
Revises: 0011_tool_audit_append_only
"""
from alembic import op
import sqlalchemy as sa

revision = "0012_impact_attribution"
down_revision = "0011_tool_audit_append_only"
branch_labels = None
depends_on = None


def upgrade():
    op.add_column("attempts", sa.Column("model_profile", sa.String(120), nullable=True))
    op.add_column("attempts", sa.Column("outcome", sa.String(40), nullable=True))
    op.add_column("attempts", sa.Column("evidence_mode", sa.String(16), nullable=False, server_default="synthetic"))
    op.add_column("evidence", sa.Column("evidence_mode", sa.String(16), nullable=False, server_default="synthetic"))
    op.create_table("objective_metrics",
        sa.Column("objective_id", sa.String(120), primary_key=True), sa.Column("metric_name", sa.String(120), nullable=False),
        sa.Column("baseline", sa.Float(), nullable=False), sa.Column("target", sa.Float(), nullable=False),
        sa.Column("unit", sa.String(40), nullable=False), sa.Column("source", sa.String(500), nullable=False),
        sa.Column("owner", sa.String(120), nullable=False), sa.Column("measurement_window_days", sa.Integer(), nullable=False),
        sa.Column("declared_at", sa.DateTime(timezone=True), nullable=False),
        sa.CheckConstraint("measurement_window_days > 0", name="ck_objective_metrics_window"))
    op.create_table("outcome_observations",
        sa.Column("id", sa.String(120), primary_key=True), sa.Column("objective_id", sa.String(120), nullable=False),
        sa.Column("metric_name", sa.String(120), nullable=False), sa.Column("value", sa.Float(), nullable=False),
        sa.Column("observed_at", sa.DateTime(timezone=True), nullable=False), sa.Column("source", sa.String(500), nullable=False),
        sa.Column("mode", sa.String(16), nullable=False), sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.CheckConstraint("mode IN ('synthetic','live')", name="ck_outcome_observations_mode"))
    op.create_index("ix_outcome_observations_objective_id", "outcome_observations", ["objective_id"])


def downgrade():
    op.drop_index("ix_outcome_observations_objective_id", table_name="outcome_observations")
    op.drop_table("outcome_observations")
    op.drop_table("objective_metrics")
    op.drop_column("evidence", "evidence_mode")
    op.drop_column("attempts", "evidence_mode")
    op.drop_column("attempts", "outcome")
    op.drop_column("attempts", "model_profile")
