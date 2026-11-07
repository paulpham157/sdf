"""Durable provider-sandbox rows for Attempt recovery.

One row per create that was entered. ``sandbox_id`` stays NULL until the
provider returns an id. ``phase=absent`` is not set by this migration and is
not a flag an operator UPDATE is expected to bless: application code sets it
only after a separate lookup reports that id gone.

Existing databases gain an empty table. No Attempt is rewritten.

SQLite receives the table without a follow-up CHECK alter. PostgreSQL gets
the phase CHECK. Databases created from the ORM include the CHECK on both.

Downgrade drops the table.
"""

import sqlalchemy as sa
from alembic import op


revision = "0032_attempt_remote_sandboxes"
down_revision = "0031_evidence_mac_v2"
branch_labels = None
depends_on = None


def upgrade():
    op.create_table(
        "attempt_remote_sandboxes",
        sa.Column("id", sa.String(length=40), primary_key=True),
        sa.Column("attempt_id", sa.String(length=120), nullable=False),
        sa.Column("provider", sa.String(length=32), nullable=False),
        sa.Column("sandbox_id", sa.String(length=120), nullable=True),
        sa.Column("phase", sa.String(length=16), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
    )
    op.create_index("ix_attempt_remote_sandboxes_attempt_id", "attempt_remote_sandboxes", ["attempt_id"])
    if op.get_bind().dialect.name == "postgresql":
        op.create_check_constraint(
            "ck_attempt_remote_sandboxes_phase",
            "attempt_remote_sandboxes",
            "phase IN ('creating', 'identified', 'absent')",
        )


def downgrade():
    if op.get_bind().dialect.name == "postgresql":
        op.drop_constraint("ck_attempt_remote_sandboxes_phase", "attempt_remote_sandboxes", type_="check")
    op.drop_index("ix_attempt_remote_sandboxes_attempt_id", table_name="attempt_remote_sandboxes")
    op.drop_table("attempt_remote_sandboxes")
