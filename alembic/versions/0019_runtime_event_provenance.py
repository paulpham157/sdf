"""Runtime events: provenance check and append-only (#57, ADR 0029).

``tool_*`` kinds are action-boundary events that only the Tool Proxy
(``source = 'tool-proxy'``) may write, and the Tool Proxy writes nothing
else. Rows that already break the rule are not rewritten or deleted: the
upgrade fails and names the count, so an operator decides what they were.
"""

from alembic import op
import sqlalchemy as sa


revision = "0019_runtime_event_provenance"
down_revision = "0018_tool_audit_principal"
branch_labels = None
depends_on = None

_CHECK = "ck_runtime_events_provenance"
_RULE = (
    "(substr(kind, 1, 5) = 'tool_' AND source = 'tool-proxy') "
    "OR (substr(kind, 1, 5) <> 'tool_' AND source <> 'tool-proxy')"
)


def upgrade():
    bind = op.get_bind()
    bad = bind.execute(sa.text(f"SELECT COUNT(*) FROM runtime_events WHERE NOT ({_RULE})")).scalar()
    if bad:
        raise RuntimeError(
            f"{bad} runtime_events row(s) break the provenance rule (tool_* kinds only from source "
            "'tool-proxy'); inspect them before upgrading (ADR 0029)"
        )
    with op.batch_alter_table("runtime_events") as batch:
        batch.create_check_constraint(_CHECK, sa.text(_RULE))

    # SQLite relies on the equivalent SQLAlchemy session guard (sdf_core.db).
    if bind.dialect.name != "postgresql":
        return
    op.execute("""
        CREATE FUNCTION sdf_reject_runtime_event_mutation() RETURNS trigger AS $$
        BEGIN
            RAISE EXCEPTION 'runtime_events rows are append-only';
        END;
        $$ LANGUAGE plpgsql;
    """)
    op.execute("""
        CREATE TRIGGER trg_runtime_events_append_only BEFORE UPDATE OR DELETE ON runtime_events
        FOR EACH ROW EXECUTE FUNCTION sdf_reject_runtime_event_mutation();
    """)


def downgrade():
    if op.get_bind().dialect.name == "postgresql":
        op.execute("DROP TRIGGER IF EXISTS trg_runtime_events_append_only ON runtime_events")
        op.execute("DROP FUNCTION IF EXISTS sdf_reject_runtime_event_mutation()")
    with op.batch_alter_table("runtime_events") as batch:
        batch.drop_constraint(_CHECK, type_="check")
