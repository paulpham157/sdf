"""API authentication: hashed bearer tokens and an append-only auth audit (ADR 0015)."""

from alembic import op
import sqlalchemy as sa


revision = "0015_api_auth"
down_revision = "0014_agent_comm"
branch_labels = None
depends_on = None


def upgrade():
    op.create_table(
        "api_tokens",
        sa.Column("id", sa.String(32), primary_key=True),
        sa.Column("principal", sa.String(120), nullable=False),
        sa.Column("kind", sa.String(16), nullable=False),
        sa.Column("roles", sa.JSON(), nullable=False),
        sa.Column("secret_hash", sa.String(64), nullable=False),
        sa.Column("description", sa.String(500), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("created_by", sa.String(120), nullable=False),
        sa.Column("expires_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("revoked_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("revoked_by", sa.String(120), nullable=True),
        sa.Column("last_used_at", sa.DateTime(timezone=True), nullable=True),
        sa.CheckConstraint("kind IN ('human','service')", name="ck_api_tokens_kind"),
    )
    op.create_index("ix_api_tokens_principal", "api_tokens", ["principal"])
    op.create_table(
        "auth_audit",
        sa.Column("id", sa.Integer(), primary_key=True, autoincrement=True),
        sa.Column("at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("event", sa.String(32), nullable=False),
        sa.Column("principal", sa.String(120), nullable=True),
        sa.Column("token_id", sa.String(32), nullable=True),
        sa.Column("method", sa.String(8), nullable=True),
        sa.Column("path", sa.String(500), nullable=True),
        sa.Column("route", sa.String(200), nullable=True),
        sa.Column("status_code", sa.Integer(), nullable=True),
        sa.Column("client", sa.String(64), nullable=True),
        sa.Column("detail", sa.JSON(), nullable=True),
    )
    op.create_index("ix_auth_audit_at", "auth_audit", ["at"])
    op.create_index("ix_auth_audit_event", "auth_audit", ["event"])
    op.create_index("ix_auth_audit_principal", "auth_audit", ["principal"])
    with op.batch_alter_table("agent_runs") as batch:
        batch.add_column(sa.Column("started_by", sa.String(120), nullable=True))
        batch.add_column(sa.Column("cancel_requested_by", sa.String(120), nullable=True))

    # SQLite relies on the equivalent SQLAlchemy session guards (sdf_core.db).
    if op.get_bind().dialect.name != "postgresql":
        return
    op.execute("""
        CREATE FUNCTION sdf_reject_auth_audit_mutation() RETURNS trigger AS $$
        BEGIN
            RAISE EXCEPTION 'auth_audit rows are append-only';
        END;
        $$ LANGUAGE plpgsql;
    """)
    op.execute("""
        CREATE TRIGGER trg_auth_audit_append_only BEFORE UPDATE OR DELETE ON auth_audit
        FOR EACH ROW EXECUTE FUNCTION sdf_reject_auth_audit_mutation();
    """)
    op.execute("""
        CREATE FUNCTION sdf_api_token_guard() RETURNS trigger AS $$
        BEGIN
            IF TG_OP = 'DELETE' THEN
                RAISE EXCEPTION 'API tokens are revoked, never deleted';
            END IF;
            IF NEW.id IS DISTINCT FROM OLD.id OR NEW.principal IS DISTINCT FROM OLD.principal
               OR NEW.kind IS DISTINCT FROM OLD.kind OR NEW.roles::text IS DISTINCT FROM OLD.roles::text
               OR NEW.secret_hash IS DISTINCT FROM OLD.secret_hash OR NEW.expires_at IS DISTINCT FROM OLD.expires_at
               OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.created_by IS DISTINCT FROM OLD.created_by
               OR NEW.description IS DISTINCT FROM OLD.description THEN
                RAISE EXCEPTION 'API token identity, roles, secret and expiry are immutable';
            END IF;
            IF OLD.revoked_at IS NOT NULL AND (NEW.revoked_at IS DISTINCT FROM OLD.revoked_at
                                               OR NEW.revoked_by IS DISTINCT FROM OLD.revoked_by) THEN
                RAISE EXCEPTION 'a revoked API token stays revoked';
            END IF;
            RETURN NEW;
        END;
        $$ LANGUAGE plpgsql;
    """)
    op.execute("""
        CREATE TRIGGER trg_api_tokens_guard BEFORE UPDATE OR DELETE ON api_tokens
        FOR EACH ROW EXECUTE FUNCTION sdf_api_token_guard();
    """)


def downgrade():
    if op.get_bind().dialect.name == "postgresql":
        op.execute("DROP TRIGGER IF EXISTS trg_api_tokens_guard ON api_tokens")
        op.execute("DROP TRIGGER IF EXISTS trg_auth_audit_append_only ON auth_audit")
        op.execute("DROP FUNCTION IF EXISTS sdf_api_token_guard()")
        op.execute("DROP FUNCTION IF EXISTS sdf_reject_auth_audit_mutation()")
    with op.batch_alter_table("agent_runs") as batch:
        batch.drop_column("cancel_requested_by")
        batch.drop_column("started_by")
    op.drop_table("auth_audit")
    op.drop_table("api_tokens")
