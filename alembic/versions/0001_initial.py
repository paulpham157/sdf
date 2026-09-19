"""create SDF Core v0 tables

Revision ID: 0001_initial
Revises:
"""
from alembic import op
import sqlalchemy as sa

revision = "0001_initial"
down_revision = None
branch_labels = None
depends_on = None


def upgrade():
    op.create_table("graph_nodes",
        sa.Column("id", sa.String(120), primary_key=True), sa.Column("kind", sa.String(40), nullable=False),
        sa.Column("title", sa.String(500), nullable=False), sa.Column("source", sa.String(500), nullable=False),
        sa.Column("owner", sa.String(120), nullable=False), sa.Column("confidence", sa.Float(), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False), sa.Column("metadata_json", sa.JSON(), nullable=True))
    op.create_index("ix_graph_nodes_kind", "graph_nodes", ["kind"])
    op.create_table("tasks",
        sa.Column("id", sa.String(120), primary_key=True), sa.Column("title", sa.String(500), nullable=False),
        sa.Column("status", sa.String(32), nullable=False), sa.Column("idempotency_key", sa.String(200), nullable=False, unique=True),
        sa.Column("acceptance_criteria", sa.JSON(), nullable=False), sa.Column("created_at", sa.DateTime(timezone=True), nullable=False))
    op.create_index("ix_tasks_status", "tasks", ["status"])
    op.create_table("attempts",
        sa.Column("id", sa.String(120), primary_key=True), sa.Column("task_id", sa.String(120), nullable=False),
        sa.Column("status", sa.String(32), nullable=False), sa.Column("dispatch_key", sa.String(200), nullable=False, unique=True),
        sa.Column("agent", sa.String(120), nullable=False), sa.Column("created_at", sa.DateTime(timezone=True), nullable=False))
    op.create_index("ix_attempts_task_id", "attempts", ["task_id"])
    op.create_index("ix_attempts_status", "attempts", ["status"])
    op.create_table("artifacts",
        sa.Column("id", sa.String(120), primary_key=True), sa.Column("attempt_id", sa.String(120), nullable=False),
        sa.Column("kind", sa.String(40), nullable=False), sa.Column("uri", sa.String(500), nullable=False),
        sa.Column("sha256", sa.String(64), nullable=False), sa.Column("size_bytes", sa.Integer(), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False))
    op.create_index("ix_artifacts_attempt_id", "artifacts", ["attempt_id"])
    op.create_table("evidence",
        sa.Column("id", sa.String(120), primary_key=True), sa.Column("attempt_id", sa.String(120), nullable=False),
        sa.Column("kind", sa.String(40), nullable=False), sa.Column("status", sa.String(32), nullable=False),
        sa.Column("command", sa.Text(), nullable=False), sa.Column("exit_code", sa.Integer(), nullable=True),
        sa.Column("artifact_ref", sa.String(120), nullable=True), sa.Column("confidence", sa.Float(), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False))
    op.create_index("ix_evidence_attempt_id", "evidence", ["attempt_id"])
    op.create_table("decision_edges",
        sa.Column("id", sa.Integer(), primary_key=True), sa.Column("source_kind", sa.String(40), nullable=False),
        sa.Column("source_id", sa.String(120), nullable=False), sa.Column("target_kind", sa.String(40), nullable=False),
        sa.Column("target_id", sa.String(120), nullable=False), sa.Column("relation", sa.String(40), nullable=False),
        sa.Column("source", sa.String(500), nullable=False), sa.Column("owner", sa.String(120), nullable=False),
        sa.Column("confidence", sa.Float(), nullable=False), sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("evidence_ref", sa.String(500), nullable=True),
        sa.CheckConstraint("relation IN ('motivates','constrains','implements','depends_on','measures','validates','contradicts')", name="ck_decision_edges_relation"))


def downgrade():
    op.drop_table("decision_edges")
    op.drop_index("ix_evidence_attempt_id", table_name="evidence")
    op.drop_table("evidence")
    op.drop_index("ix_artifacts_attempt_id", table_name="artifacts")
    op.drop_table("artifacts")
    op.drop_index("ix_attempts_status", table_name="attempts")
    op.drop_index("ix_attempts_task_id", table_name="attempts")
    op.drop_table("attempts")
    op.drop_index("ix_tasks_status", table_name="tasks")
    op.drop_table("tasks")
    op.drop_index("ix_graph_nodes_kind", table_name="graph_nodes")
    op.drop_table("graph_nodes")
