"""label runtime event sources and make lifecycle replay idempotent

Revision ID: 0007_runtime_event_idempotency
Revises: 0006_tool_audits
"""

from alembic import op
import sqlalchemy as sa


revision = "0007_runtime_event_idempotency"
down_revision = "0006_tool_audits"
branch_labels = None
depends_on = None


_IDENTITY_INDEX = "uq_runtime_events_source_attempt_session_sequence"


def upgrade():
    # Existing 0004 rows were all emitted by the internal runtime source.  A
    # server default lets this migration run on a populated database without
    # inventing null/unknown provenance for those historical observations.
    op.add_column(
        "runtime_events",
        sa.Column("source", sa.String(120), nullable=False, server_default="runtime"),
    )

    # The pre-0007 schema had no uniqueness guard.  Keep the first durable
    # observation for any already-redelivered identity before adding the
    # constraint; future duplicates are rejected/no-op at the database edge.
    op.execute(
        sa.text(
            """
            DELETE FROM runtime_events
            WHERE id NOT IN (
                SELECT MIN(id)
                FROM runtime_events
                GROUP BY source, attempt_id, session_id, sequence
            )
            """
        )
    )

    # A unique index is portable across PostgreSQL and SQLite (the latter
    # cannot ALTER TABLE to add a named UNIQUE constraint).
    op.create_index(
        _IDENTITY_INDEX,
        "runtime_events",
        ["source", "attempt_id", "session_id", "sequence"],
        unique=True,
    )


def downgrade():
    op.drop_index(_IDENTITY_INDEX, table_name="runtime_events")
    op.drop_column("runtime_events", "source")
