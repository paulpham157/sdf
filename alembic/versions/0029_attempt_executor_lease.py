"""Executor lease columns on Attempts (ADR 0043).

Adds nullable ``executor_id`` and ``executor_heartbeat_at`` on ``attempts``.
A running Attempt with both NULL has no executor. Startup settles it.
Existing rows are not rewritten: a running Attempt from before this migration
has no heartbeat, so the next process start treats it as dead. That is the
crash this column exists to close. No backfill is attempted.

The columns are plain nullable ``ADD COLUMN`` on both dialects (no SQLite
table rebuild, so no index, trigger or CHECK is touched).

This revision follows ``0028_evidence_classes``. It does not use ``0027``.

Downgrade drops the columns. It discards the lease.
"""

import sqlalchemy as sa
from alembic import op


revision = "0029_attempt_executor_lease"
down_revision = "0028_evidence_classes"
branch_labels = None
depends_on = None


def upgrade():
    op.add_column("attempts", sa.Column("executor_id", sa.String(length=64), nullable=True))
    op.add_column("attempts", sa.Column("executor_heartbeat_at", sa.DateTime(timezone=True), nullable=True))


def downgrade():
    op.drop_column("attempts", "executor_heartbeat_at")
    op.drop_column("attempts", "executor_id")
