"""Fence material decision requests in PostgreSQL (#60, ADR 0033).

Materiality is stored in the asking agent message's ``meta``.
``agent_messages`` is already append-only on PostgreSQL
(``trg_agent_messages_append_only``, migration 0014) and in the ORM, so that
meta cannot be rewritten or deleted. A pending ``decision_requests`` row,
however, could still be re-pointed at another message, or resolved by a
policy by a writer that bypasses ``PlanService``. Either would downgrade the
gate. This migration adds:

* ``trg_decision_requests_material_fence`` (BEFORE UPDATE): a decision
  request's identity (``subject_kind``, ``subject_id``, ``run_id``,
  ``requested_by``, ``message_id``, ``question``, ``options``,
  ``created_at``) never changes after insert. A request whose agent message
  is material (``meta.material`` is JSON ``true``) is resolved only with
  ``resolver_kind = 'human'``.

SQLite relies on the equivalent ORM session guard in ``sdf_core.db``. No
column or table change. Downgrade drops the trigger and its function.

Revision ID: 0024_decision_material_fence
Revises: 0023_attempt_revisions
"""

from alembic import op

revision = "0024_decision_material_fence"
down_revision = "0023_attempt_revisions"
branch_labels = None
depends_on = None


def upgrade():
    if op.get_bind().dialect.name != "postgresql":
        return  # SQLite relies on the equivalent session guard in sdf_core.db.
    op.execute("""
        CREATE FUNCTION sdf_decision_request_material_fence() RETURNS trigger AS $$
        DECLARE
            msg_meta json;
        BEGIN
            IF NEW.id IS DISTINCT FROM OLD.id
               OR NEW.subject_kind IS DISTINCT FROM OLD.subject_kind
               OR NEW.subject_id IS DISTINCT FROM OLD.subject_id
               OR NEW.run_id IS DISTINCT FROM OLD.run_id
               OR NEW.requested_by IS DISTINCT FROM OLD.requested_by
               OR NEW.message_id IS DISTINCT FROM OLD.message_id
               OR NEW.question IS DISTINCT FROM OLD.question
               OR NEW.options::jsonb IS DISTINCT FROM OLD.options::jsonb
               OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
                RAISE EXCEPTION 'decision request % subject, message and question are immutable', OLD.id;
            END IF;
            IF NEW.status <> 'pending' AND NEW.resolver_kind IS DISTINCT FROM 'human'
               AND NEW.subject_kind = 'agent_request' AND NEW.message_id IS NOT NULL THEN
                SELECT meta INTO msg_meta FROM agent_messages WHERE id = NEW.message_id;
                IF msg_meta IS NOT NULL AND json_typeof(msg_meta -> 'material') = 'boolean'
                   AND (msg_meta ->> 'material') = 'true' THEN
                    RAISE EXCEPTION 'only a human resolves material decision request % (ADR 0033)', OLD.id;
                END IF;
            END IF;
            RETURN NEW;
        END;
        $$ LANGUAGE plpgsql;
    """)
    op.execute("""
        CREATE TRIGGER trg_decision_requests_material_fence BEFORE UPDATE ON decision_requests
        FOR EACH ROW EXECUTE FUNCTION sdf_decision_request_material_fence();
    """)


def downgrade():
    if op.get_bind().dialect.name != "postgresql":
        return
    op.execute("DROP TRIGGER IF EXISTS trg_decision_requests_material_fence ON decision_requests")
    op.execute("DROP FUNCTION IF EXISTS sdf_decision_request_material_fence()")
