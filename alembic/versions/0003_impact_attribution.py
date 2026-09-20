"""add impact attribution fields and objective metric tables

Revision ID: 0003_impact_attribution
Revises: 0002_evidence_criterion
"""
from alembic import op
import sqlalchemy as sa

revision = "0003_impact_attribution"
down_revision = "0002_evidence_criterion"
branch_labels = None
depends_on = None


def upgrade():
    op.add_column("attempts", sa.Column("parent_attempt_id", sa.String(120), nullable=True))
    op.create_index("ix_attempts_parent_attempt_id", "attempts", ["parent_attempt_id"])
    op.add_column("attempts", sa.Column("model_tier", sa.String(16), nullable=True))
    op.add_column("attempts", sa.Column("model_profile", sa.String(120), nullable=True))
    op.add_column("attempts", sa.Column("provider", sa.String(120), nullable=True))
    op.add_column("attempts", sa.Column("input_tokens", sa.Integer(), nullable=True))
    op.add_column("attempts", sa.Column("output_tokens", sa.Integer(), nullable=True))
    op.add_column("attempts", sa.Column("cost_usd", sa.Float(), nullable=True))
    op.add_column("attempts", sa.Column("latency_ms", sa.Integer(), nullable=True))
    op.add_column("attempts", sa.Column("escalation_reason", sa.String(120), nullable=True))
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
    op.drop_column("attempts", "escalation_reason")
    op.drop_column("attempts", "latency_ms")
    op.drop_column("attempts", "cost_usd")
    op.drop_column("attempts", "output_tokens")
    op.drop_column("attempts", "input_tokens")
    op.drop_column("attempts", "provider")
    op.drop_column("attempts", "model_profile")
    op.drop_column("attempts", "model_tier")
    op.drop_index("ix_attempts_parent_attempt_id", table_name="attempts")
    op.drop_column("attempts", "parent_attempt_id")
