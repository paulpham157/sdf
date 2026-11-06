"""Executor lease columns on Attempts (ADR 0041).

Adds nullable ``executor_id`` and ``executor_heartbeat_at`` on ``attempts``.
A running Attempt with both NULL has no executor. Startup settles it.
Existing rows are not rewritten: a running Attempt from before this migration
has no heartbeat, so the next process start treats it as dead. That is the
crash this column exists to close. No backfill is attempted.

The columns are plain nullable ``ADD COLUMN`` on both dialects (no SQLite
table rebuild, so no index, trigger or CHECK is touched).

Revision id ``0027`` skips ``0025`` and ``0026``, which are the next revisions
on main and are not in this branch. The parent here is ``0024``. Merging main
makes two Alembic heads until a merge revision joins them.

Downgrade drops the columns. It discards the lease.
"""

import sqlalchemy as sa
from alembic import op


revision = "0027_attempt_executor_lease"
down_revision = "0024_decision_material_fence"
branch_labels = None
depends_on = None


def upgrade():
    with op.batch_alter_table("attempts") as batch:
        batch.add_column(sa.Column("executor_id", sa.String(length=64), nullable=True))
        batch.add_column(sa.Column("executor_heartbeat_at", sa.DateTime(timezone=True), nullable=True))


def downgrade():
    with op.batch_alter_table("attempts") as batch:
        batch.drop_column("executor_heartbeat_at")
        batch.drop_column("executor_id")
