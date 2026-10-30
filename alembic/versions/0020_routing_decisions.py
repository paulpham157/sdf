"""Routing decisions recorded per live Attempt (#58, ADR 0030)."""

from alembic import op
import sqlalchemy as sa


revision = "0020_routing_decisions"
down_revision = "0019_runtime_event_provenance"
branch_labels = None
depends_on = None


def upgrade():
    op.create_table(
        "routing_decisions",
        sa.Column("id", sa.String(120), primary_key=True),
        sa.Column("attempt_id", sa.String(120), nullable=False),
        sa.Column("selected_agent", sa.String(32), nullable=False),
        sa.Column("mode", sa.String(16), nullable=False),
        sa.Column("reason", sa.String(500), nullable=False),
        sa.Column("request", sa.JSON(), nullable=True),
        sa.Column("candidates", sa.JSON(), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.CheckConstraint("mode IN ('explicit','measured')", name="ck_routing_decisions_mode"),
    )
    op.create_index("ix_routing_decisions_attempt_id", "routing_decisions", ["attempt_id"], unique=True)
    op.create_index("ix_routing_decisions_selected_agent", "routing_decisions", ["selected_agent"])

    # SQLite relies on the equivalent SQLAlchemy session guard (sdf_core.db).
    if op.get_bind().dialect.name != "postgresql":
        return
    op.execute("""
        CREATE FUNCTION sdf_reject_routing_decision_mutation() RETURNS trigger AS $$
        BEGIN
            RAISE EXCEPTION 'routing_decisions rows are append-only';
        END;
        $$ LANGUAGE plpgsql;
    """)
    op.execute("""
        CREATE TRIGGER trg_routing_decisions_append_only BEFORE UPDATE OR DELETE ON routing_decisions
        FOR EACH ROW EXECUTE FUNCTION sdf_reject_routing_decision_mutation();
    """)


def downgrade():
    if op.get_bind().dialect.name == "postgresql":
        op.execute("DROP TRIGGER IF EXISTS trg_routing_decisions_append_only ON routing_decisions")
        op.execute("DROP FUNCTION IF EXISTS sdf_reject_routing_decision_mutation()")
    op.drop_index("ix_routing_decisions_selected_agent", table_name="routing_decisions")
    op.drop_index("ix_routing_decisions_attempt_id", table_name="routing_decisions")
    op.drop_table("routing_decisions")
