"""Evidence content MAC version and v2 coverage (ADR 0043).

Adds nullable ``evidence.content_mac_version``. Rows that already have a
``content_mac`` (written under migration 0029's v1 payload) are backfilled
to version 1. Rows with a NULL MAC stay NULL: they were written before
migration 0029 and are the legacy case. New inserts use version 2.

Backfill on PostgreSQL updates existing Evidence rows, so the append-only
trigger is disabled for that statement and re-enabled afterwards. The column
is a plain nullable ``ADD COLUMN`` on both dialects (no SQLite table rebuild,
so no index, trigger or CHECK is touched).

Downgrade drops ``content_mac_version``. Existing MACs stay. A later upgrade
classifies a remaining MAC as v1 again.
"""

import sqlalchemy as sa

from alembic import op

revision = "0031_evidence_mac_v2"
down_revision = "0030_attempt_remote_effect"
branch_labels = None
depends_on = None


def upgrade():
    op.add_column("evidence", sa.Column("content_mac_version", sa.Integer(), nullable=True))
    postgres = op.get_bind().dialect.name == "postgresql"
    if postgres:
        op.execute(sa.text("ALTER TABLE evidence DISABLE TRIGGER trg_evidence_append_only"))
    op.execute(sa.text(
        "UPDATE evidence SET content_mac_version = 1 WHERE content_mac IS NOT NULL"
    ))
    if postgres:
        op.execute(sa.text("ALTER TABLE evidence ENABLE TRIGGER trg_evidence_append_only"))


def downgrade():
    op.drop_column("evidence", "content_mac_version")
