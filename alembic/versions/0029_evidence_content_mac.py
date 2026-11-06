"""Evidence content MAC (ADR 0043).

Adds nullable ``evidence.content_mac``. Rows written after this migration get
an HMAC of their verdict fields and bound artifact sha256 at insert. Rows
already stored stay NULL and are not backfilled: the bytes those rows
committed were never recorded and must not be guessed.

The column is a plain nullable ``ADD COLUMN`` on both dialects (no SQLite
table rebuild, so no index, trigger or CHECK is touched). ``ADD COLUMN`` on
PostgreSQL neither updates rows nor fires the Evidence append-only trigger.
The trigger still rejects a later UPDATE, including an update of
``content_mac``. SQLite has no such trigger; verification of the MAC is what
rejects a rewrite there.

Downgrade drops the column. It discards the MACs.
"""

import sqlalchemy as sa

from alembic import op

revision = "0029_evidence_content_mac"
down_revision = "0028_evidence_classes"
branch_labels = None
depends_on = None


def upgrade():
    op.add_column("evidence", sa.Column("content_mac", sa.String(64), nullable=True))


def downgrade():
    op.drop_column("evidence", "content_mac")
