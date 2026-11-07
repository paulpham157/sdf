"""Remote-effect fence on Attempts.

Adds nullable ``attempts.remote_effect``. NULL means this Attempt never
entered a provider sandbox create. ``uncertain`` is committed before that
create and stays until this process confirms the provider resource is gone
(``stopped``). Existing rows are left NULL: they did not pass through the
new create fence. No backfill.

The column is a nullable ``ADD COLUMN`` on SQLite and PostgreSQL (no table
rebuild). PostgreSQL also gets a CHECK. SQLite cannot add a CHECK without
rebuilding ``attempts``, so the ORM CHECK covers databases created from the
model; migrated SQLite files rely on the application writing only the two
values.

Downgrade drops the CHECK on PostgreSQL and then the column.
"""

import sqlalchemy as sa
from alembic import op


revision = "0030_attempt_remote_effect"
down_revision = "0029_evidence_content_mac"
branch_labels = None
depends_on = None

_CHECK = "remote_effect IS NULL OR remote_effect IN ('uncertain', 'stopped')"


def upgrade():
    op.add_column("attempts", sa.Column("remote_effect", sa.String(length=16), nullable=True))
    if op.get_bind().dialect.name == "postgresql":
        op.create_check_constraint("ck_attempts_remote_effect", "attempts", _CHECK)


def downgrade():
    if op.get_bind().dialect.name == "postgresql":
        op.drop_constraint("ck_attempts_remote_effect", "attempts", type_="check")
    op.drop_column("attempts", "remote_effect")
