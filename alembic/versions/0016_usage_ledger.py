"""Usage/cost ledger and the Attempt error class (ADR 0023)."""

from alembic import op
import sqlalchemy as sa


revision = "0016_usage_ledger"
down_revision = "0015_api_auth"
branch_labels = None
depends_on = None


def upgrade():
    op.create_table(
        "usage_ledger",
        sa.Column("id", sa.Integer(), primary_key=True, autoincrement=True),
        sa.Column("attempt_id", sa.String(120), nullable=False),
        sa.Column("run_id", sa.String(120), nullable=True),
        sa.Column("kind", sa.String(16), nullable=False),
        sa.Column("agent", sa.String(32), nullable=True),
        sa.Column("turn", sa.Integer(), nullable=True),
        sa.Column("session_id", sa.String(120), nullable=True),
        sa.Column("sandbox_id", sa.String(120), nullable=True),
        sa.Column("sandbox_provider", sa.String(32), nullable=True),
        sa.Column("model", sa.String(120), nullable=True),
        sa.Column("input_tokens", sa.Integer(), nullable=True),
        sa.Column("output_tokens", sa.Integer(), nullable=True),
        sa.Column("cache_read_tokens", sa.Integer(), nullable=True),
        sa.Column("seconds", sa.Float(), nullable=True),
        sa.Column("cost_usd", sa.Float(), nullable=True),
        sa.Column("source", sa.String(16), nullable=False),
        sa.Column("cost_source", sa.String(16), nullable=False),
        sa.Column("status", sa.String(32), nullable=False),
        sa.Column("error_class", sa.String(32), nullable=True),
        sa.Column("detail", sa.JSON(), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.CheckConstraint("kind IN ('turn','sandbox')", name="ck_usage_ledger_kind"),
        sa.CheckConstraint("source IN ('reported','estimated','missing')", name="ck_usage_ledger_source"),
    )
    op.create_index("ix_usage_ledger_attempt_id", "usage_ledger", ["attempt_id"])
    op.create_index("ix_usage_ledger_run_id", "usage_ledger", ["run_id"])
    with op.batch_alter_table("attempts") as batch:
        batch.add_column(sa.Column("error_class", sa.String(32), nullable=True))

    # SQLite relies on the equivalent SQLAlchemy session guard (sdf_core.db).
    if op.get_bind().dialect.name != "postgresql":
        return
    op.execute("""
        CREATE FUNCTION sdf_reject_usage_ledger_mutation() RETURNS trigger AS $$
        BEGIN
            RAISE EXCEPTION 'usage_ledger rows are append-only';
        END;
        $$ LANGUAGE plpgsql;
    """)
    op.execute("""
        CREATE TRIGGER trg_usage_ledger_append_only BEFORE UPDATE OR DELETE ON usage_ledger
        FOR EACH ROW EXECUTE FUNCTION sdf_reject_usage_ledger_mutation();
    """)


def downgrade():
    if op.get_bind().dialect.name == "postgresql":
        op.execute("DROP TRIGGER IF EXISTS trg_usage_ledger_append_only ON usage_ledger")
        op.execute("DROP FUNCTION IF EXISTS sdf_reject_usage_ledger_mutation()")
    with op.batch_alter_table("attempts") as batch:
        batch.drop_column("error_class")
    op.drop_index("ix_usage_ledger_run_id", table_name="usage_ledger")
    op.drop_index("ix_usage_ledger_attempt_id", table_name="usage_ledger")
    op.drop_table("usage_ledger")
