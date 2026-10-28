"""plan-approval gate, decision requests and agent messaging (ADR 0014)

Revision ID: 0014_agent_comm
Revises: 0013_tool_audit_evidence_mode
"""

import sqlalchemy as sa
from alembic import op


revision = "0014_agent_comm"
down_revision = "0013_tool_audit_evidence_mode"
branch_labels = None
depends_on = None


def _ts(name, nullable=False):
    return sa.Column(name, sa.DateTime(timezone=True), nullable=nullable)


def upgrade():
    op.create_table(
        "plans",
        sa.Column("id", sa.String(120), primary_key=True),
        sa.Column("title", sa.String(500), nullable=False),
        sa.Column("objective_id", sa.String(120), nullable=True),
        sa.Column("status", sa.String(16), nullable=False),
        sa.Column("content", sa.JSON(), nullable=False),
        sa.Column("content_sha256", sa.String(64), nullable=False),
        sa.Column("proposed_by", sa.String(120), nullable=False),
        _ts("created_at"),
        sa.Column("decision_request_id", sa.String(120), nullable=False),
        _ts("decided_at", nullable=True),
        sa.CheckConstraint("status IN ('proposed','approved','rejected')", name="ck_plans_status"),
    )
    op.create_index("ix_plans_status", "plans", ["status"])
    op.create_table(
        "plan_tasks",
        sa.Column("task_id", sa.String(120), primary_key=True),
        sa.Column("plan_id", sa.String(120), nullable=False),
        sa.Column("subtask_key", sa.String(64), nullable=False),
    )
    op.create_index("ix_plan_tasks_plan_id", "plan_tasks", ["plan_id"])
    op.create_table(
        "decision_requests",
        sa.Column("id", sa.String(120), primary_key=True),
        sa.Column("subject_kind", sa.String(32), nullable=False),
        sa.Column("subject_id", sa.String(120), nullable=False),
        sa.Column("run_id", sa.String(120), nullable=True),
        sa.Column("question", sa.Text(), nullable=False),
        sa.Column("options", sa.JSON(), nullable=False),
        sa.Column("status", sa.String(16), nullable=False),
        sa.Column("requested_by", sa.String(120), nullable=False),
        sa.Column("message_id", sa.String(120), nullable=True),
        sa.Column("resolved_by", sa.String(120), nullable=True),
        sa.Column("resolver_kind", sa.String(16), nullable=True),
        sa.Column("resolution", sa.Text(), nullable=True),
        sa.Column("decision_node_id", sa.String(120), nullable=True),
        _ts("created_at"),
        _ts("resolved_at", nullable=True),
        sa.CheckConstraint("status IN ('pending','approved','rejected','answered')", name="ck_decision_requests_status"),
        sa.CheckConstraint("subject_kind IN ('plan','agent_request')", name="ck_decision_requests_subject"),
        sa.CheckConstraint(
            "resolver_kind IS NULL OR resolver_kind IN ('human','policy')", name="ck_decision_requests_resolver"
        ),
    )
    op.create_index("ix_decision_requests_subject_id", "decision_requests", ["subject_id"])
    op.create_index("ix_decision_requests_run_id", "decision_requests", ["run_id"])
    op.create_index("ix_decision_requests_status", "decision_requests", ["status"])
    op.create_table(
        "agent_runs",
        sa.Column("id", sa.String(120), primary_key=True),
        sa.Column("plan_id", sa.String(120), nullable=False),
        sa.Column("status", sa.String(16), nullable=False),
        sa.Column("config", sa.JSON(), nullable=False),
        _ts("started_at"),
        _ts("finished_at", nullable=True),
        _ts("cancel_requested_at", nullable=True),
        sa.Column("cost_usd", sa.Float(), nullable=False),
        sa.Column("summary", sa.JSON(), nullable=True),
        sa.Column("error", sa.Text(), nullable=True),
        sa.CheckConstraint("status IN ('running','succeeded','failed','cancelled')", name="ck_agent_runs_status"),
    )
    op.create_index("ix_agent_runs_plan_id", "agent_runs", ["plan_id"])
    op.create_index("ix_agent_runs_status", "agent_runs", ["status"])
    op.create_table(
        "agent_run_members",
        sa.Column("id", sa.Integer(), primary_key=True, autoincrement=True),
        sa.Column("run_id", sa.String(120), nullable=False),
        sa.Column("agent_id", sa.String(64), nullable=False),
        sa.Column("role", sa.String(16), nullable=False),
        sa.Column("agent_kind", sa.String(32), nullable=False),
        sa.Column("subtask_key", sa.String(64), nullable=True),
        sa.Column("sandbox_id", sa.String(120), nullable=True),
        sa.Column("status", sa.String(32), nullable=False),
        sa.Column("turns", sa.Integer(), nullable=False),
        sa.Column("input_tokens", sa.Integer(), nullable=False),
        sa.Column("output_tokens", sa.Integer(), nullable=False),
        sa.UniqueConstraint("run_id", "agent_id", name="uq_agent_run_members_run_agent"),
    )
    op.create_index("ix_agent_run_members_run_id", "agent_run_members", ["run_id"])
    op.create_table(
        "agent_messages",
        sa.Column("id", sa.String(120), primary_key=True),
        sa.Column("run_id", sa.String(120), nullable=False),
        sa.Column("seq", sa.Integer(), nullable=False),
        sa.Column("envelope_id", sa.String(64), nullable=False),
        sa.Column("from_agent", sa.String(64), nullable=False),
        sa.Column("from_role", sa.String(16), nullable=False),
        sa.Column("to_agent", sa.String(64), nullable=True),
        sa.Column("to_role", sa.String(16), nullable=True),
        sa.Column("kind", sa.String(24), nullable=False),
        sa.Column("body", sa.Text(), nullable=False),
        sa.Column("body_sha256", sa.String(64), nullable=False),
        sa.Column("truncated", sa.Boolean(), nullable=False),
        sa.Column("attempt_id", sa.String(120), nullable=True),
        sa.Column("source_sandbox_id", sa.String(120), nullable=True),
        sa.Column("meta", sa.JSON(), nullable=True),
        _ts("created_at"),
        sa.UniqueConstraint("run_id", "seq", name="uq_agent_messages_run_seq"),
        sa.UniqueConstraint("run_id", "from_agent", "envelope_id", name="uq_agent_messages_envelope"),
        sa.CheckConstraint(
            "kind IN ('message','report','question','decision_request','decision','system')",
            name="ck_agent_messages_kind",
        ),
    )
    op.create_index("ix_agent_messages_run_id", "agent_messages", ["run_id"])
    op.create_table(
        "agent_claims",
        sa.Column("id", sa.String(120), primary_key=True),
        sa.Column("run_id", sa.String(120), nullable=False),
        sa.Column("message_id", sa.String(120), nullable=False, unique=True),
        sa.Column("agent_id", sa.String(64), nullable=False),
        sa.Column("attempt_id", sa.String(120), nullable=True),
        sa.Column("subtask_key", sa.String(64), nullable=True),
        sa.Column("claimed_status", sa.String(16), nullable=False),
        sa.Column("body", sa.Text(), nullable=False),
        _ts("created_at"),
    )
    op.create_index("ix_agent_claims_run_id", "agent_claims", ["run_id"])

    # SQLite relies on the equivalent SQLAlchemy session guards (sdf_core.db);
    # PostgreSQL also fences direct writes and other connection owners.
    if op.get_bind().dialect.name != "postgresql":
        return
    op.execute("""
        CREATE FUNCTION sdf_reject_agent_comm_mutation() RETURNS trigger AS $$
        BEGIN
            RAISE EXCEPTION '% rows are append-only', TG_TABLE_NAME;
        END;
        $$ LANGUAGE plpgsql;
    """)
    for table in ("agent_messages", "agent_claims"):
        op.execute(f"""
            CREATE TRIGGER trg_{table}_append_only
            BEFORE UPDATE OR DELETE ON {table}
            FOR EACH ROW EXECUTE FUNCTION sdf_reject_agent_comm_mutation();
        """)
    op.execute("""
        CREATE FUNCTION sdf_plan_gate_attempt() RETURNS trigger AS $$
        DECLARE plan_status text;
        BEGIN
            SELECT p.status INTO plan_status FROM plan_tasks pt JOIN plans p ON p.id = pt.plan_id
             WHERE pt.task_id = NEW.task_id;
            IF FOUND AND plan_status <> 'approved' THEN
                RAISE EXCEPTION 'plan gate: task % belongs to a plan that is not approved', NEW.task_id;
            END IF;
            RETURN NEW;
        END;
        $$ LANGUAGE plpgsql;
    """)
    op.execute("""
        CREATE TRIGGER trg_attempts_plan_gate BEFORE INSERT ON attempts
        FOR EACH ROW EXECUTE FUNCTION sdf_plan_gate_attempt();
    """)
    op.execute("""
        CREATE FUNCTION sdf_plan_gate_run() RETURNS trigger AS $$
        BEGIN
            IF NOT EXISTS (SELECT 1 FROM plans WHERE id = NEW.plan_id AND status = 'approved') THEN
                RAISE EXCEPTION 'plan gate: plan % is not approved', NEW.plan_id;
            END IF;
            RETURN NEW;
        END;
        $$ LANGUAGE plpgsql;
    """)
    op.execute("""
        CREATE TRIGGER trg_agent_runs_plan_gate BEFORE INSERT ON agent_runs
        FOR EACH ROW EXECUTE FUNCTION sdf_plan_gate_run();
    """)
    op.execute("""
        CREATE FUNCTION sdf_plan_guard() RETURNS trigger AS $$
        BEGIN
            IF TG_OP = 'DELETE' THEN
                RAISE EXCEPTION 'plans cannot be deleted';
            END IF;
            IF NEW.content::text <> OLD.content::text OR NEW.content_sha256 <> OLD.content_sha256
               OR NEW.title <> OLD.title OR NEW.id <> OLD.id
               OR NEW.decision_request_id <> OLD.decision_request_id THEN
                RAISE EXCEPTION 'plan content is immutable once proposed';
            END IF;
            IF NEW.status <> OLD.status THEN
                IF OLD.status <> 'proposed' OR NEW.status NOT IN ('approved','rejected') THEN
                    RAISE EXCEPTION 'plan status may only move once from proposed';
                END IF;
                IF NEW.status = 'approved' AND NOT EXISTS (
                    SELECT 1 FROM decision_requests d WHERE d.id = NEW.decision_request_id
                       AND d.subject_id = NEW.id AND d.status = 'approved' AND d.resolver_kind = 'human'
                ) THEN
                    RAISE EXCEPTION 'plan approval requires a human-approved decision request';
                END IF;
            END IF;
            RETURN NEW;
        END;
        $$ LANGUAGE plpgsql;
    """)
    op.execute("""
        CREATE TRIGGER trg_plans_guard BEFORE UPDATE OR DELETE ON plans
        FOR EACH ROW EXECUTE FUNCTION sdf_plan_guard();
    """)
    op.execute("""
        CREATE FUNCTION sdf_decision_request_guard() RETURNS trigger AS $$
        BEGIN
            IF TG_OP = 'DELETE' THEN
                RAISE EXCEPTION 'decision requests cannot be deleted';
            END IF;
            IF OLD.status <> 'pending' THEN
                RAISE EXCEPTION 'a resolved decision request is final';
            END IF;
            IF NEW.status <> 'pending' AND NEW.resolver_kind IS NULL THEN
                RAISE EXCEPTION 'a resolved decision request needs a resolver';
            END IF;
            IF NEW.subject_kind = 'plan' AND NEW.status <> 'pending' AND NEW.resolver_kind <> 'human' THEN
                RAISE EXCEPTION 'only a human resolves a plan approval';
            END IF;
            RETURN NEW;
        END;
        $$ LANGUAGE plpgsql;
    """)
    op.execute("""
        CREATE TRIGGER trg_decision_requests_guard BEFORE UPDATE OR DELETE ON decision_requests
        FOR EACH ROW EXECUTE FUNCTION sdf_decision_request_guard();
    """)


def downgrade():
    if op.get_bind().dialect.name == "postgresql":
        for trigger, table in (
            ("trg_decision_requests_guard", "decision_requests"),
            ("trg_plans_guard", "plans"),
            ("trg_agent_runs_plan_gate", "agent_runs"),
            ("trg_attempts_plan_gate", "attempts"),
            ("trg_agent_claims_append_only", "agent_claims"),
            ("trg_agent_messages_append_only", "agent_messages"),
        ):
            op.execute(f"DROP TRIGGER IF EXISTS {trigger} ON {table}")
        for function in (
            "sdf_decision_request_guard", "sdf_plan_guard", "sdf_plan_gate_run",
            "sdf_plan_gate_attempt", "sdf_reject_agent_comm_mutation",
        ):
            op.execute(f"DROP FUNCTION IF EXISTS {function}()")
    for table in (
        "agent_claims", "agent_messages", "agent_run_members", "agent_runs",
        "decision_requests", "plan_tasks", "plans",
    ):
        op.drop_table(table)
