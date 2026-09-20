"""enforce DecisionEdge endpoint integrity in PostgreSQL

Revision ID: 0010_decision_edge_integrity
Revises: 0009_evidence_append_only
"""

from alembic import op


revision = "0010_decision_edge_integrity"
down_revision = "0009_evidence_append_only"
branch_labels = None
depends_on = None


_FUNCTION = "sdf_validate_decision_edge"
_TRIGGER = "trg_decision_edges_endpoint_integrity"


def upgrade():
    if op.get_bind().dialect.name != "postgresql":
        return
    op.execute(f"""
        CREATE FUNCTION {_FUNCTION}() RETURNS trigger AS $$
        DECLARE
            endpoint_exists boolean;
        BEGIN
            IF NEW.source_kind = 'task' THEN
                SELECT EXISTS (SELECT 1 FROM tasks WHERE id = NEW.source_id) INTO endpoint_exists;
            ELSIF NEW.source_kind = 'attempt' THEN
                SELECT EXISTS (SELECT 1 FROM attempts WHERE id = NEW.source_id) INTO endpoint_exists;
            ELSE
                SELECT EXISTS (SELECT 1 FROM graph_nodes WHERE id = NEW.source_id) INTO endpoint_exists;
            END IF;
            IF NOT endpoint_exists THEN
                RAISE EXCEPTION 'decision edge source does not exist: %:%', NEW.source_kind, NEW.source_id;
            END IF;

            IF NEW.target_kind = 'task' THEN
                SELECT EXISTS (SELECT 1 FROM tasks WHERE id = NEW.target_id) INTO endpoint_exists;
            ELSIF NEW.target_kind = 'attempt' THEN
                SELECT EXISTS (SELECT 1 FROM attempts WHERE id = NEW.target_id) INTO endpoint_exists;
            ELSE
                SELECT EXISTS (SELECT 1 FROM graph_nodes WHERE id = NEW.target_id) INTO endpoint_exists;
            END IF;
            IF NOT endpoint_exists THEN
                RAISE EXCEPTION 'decision edge target does not exist: %:%', NEW.target_kind, NEW.target_id;
            END IF;
            RETURN NEW;
        END;
        $$ LANGUAGE plpgsql;
    """)
    op.execute(f"""
        CREATE TRIGGER {_TRIGGER}
        BEFORE INSERT OR UPDATE ON decision_edges
        FOR EACH ROW EXECUTE FUNCTION {_FUNCTION}();
    """)


def downgrade():
    if op.get_bind().dialect.name != "postgresql":
        return
    op.execute(f"DROP TRIGGER IF EXISTS {_TRIGGER} ON decision_edges")
    op.execute(f"DROP FUNCTION IF EXISTS {_FUNCTION}()")
