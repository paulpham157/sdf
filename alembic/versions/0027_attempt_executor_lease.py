"""Executor lease on Attempts (ADR 0029 startup reconciliation).

Adds nullable columns:

* ``executor_id`` — the process that moved the Attempt to ``running``.
* ``executor_heartbeat_at`` — the last heartbeat from that process.

Existing rows stay NULL. NULL is not a lease, and startup does not fail
those rows: it cannot tell them from a worker that never wrote a lease.
New runs set both columns in the same commit that enters ``running``.

Both dialects use ``ADD COLUMN`` (no SQLite table rebuild, so indexes,
triggers and CHECKs are untouched). The columns are nullable, so the
append-only triggers on other tables are not involved and no row is rewritten.

Downgrade drops the columns. Attempts that were running keep their status;
only the lease is discarded.
"""

import sqlalchemy as sa

from alembic import op

revision = "0027_attempt_executor_lease"
down_revision = "0026_oracle_visibility"
branch_labels = None
depends_on = None


def upgrade():
    op.add_column("attempts", sa.Column("executor_id", sa.String(64), nullable=True))
    op.add_column("attempts", sa.Column("executor_heartbeat_at", sa.DateTime(timezone=True), nullable=True))


def downgrade():
    op.drop_column("attempts", "executor_heartbeat_at")
    op.drop_column("attempts", "executor_id")
