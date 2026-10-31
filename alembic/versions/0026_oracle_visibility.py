"""Held-out oracle visibility and randomized input seed (ADR 0035).

Adds nullable columns on ``evidence``:

* ``oracle_visibility`` — ``visible`` or ``held_out``, NULL when the row
  predates this migration or is not a check.
* ``input_seed`` — the server seed recorded after a randomized check. NULL
  when the check was not randomized.

Existing rows are not rewritten. Evidence is append-only, and a row from
before this migration keeps NULL. NULL visibility does not satisfy a task
that declares a held-out or randomized oracle, and it does not change tasks
that declare neither.

The columns are plain nullable ``ADD COLUMN`` on both dialects (no SQLite
table rebuild, so no index, trigger or CHECK is touched). ``ADD COLUMN`` on
PostgreSQL neither updates rows nor fires the Evidence append-only trigger.

PostgreSQL also gets ``ck_evidence_oracle_visibility``: NULL, or ``visible``,
or ``held_out``. SQLite relies on the ORM session guard in ``sdf_core.db``.

Downgrade drops the check (PostgreSQL) and the columns. It discards the
visibility and seed.
"""

import sqlalchemy as sa

from alembic import op

revision = "0026_oracle_visibility"
down_revision = "0025_evidence_independence"
branch_labels = None
depends_on = None


def upgrade():
    op.add_column("evidence", sa.Column("oracle_visibility", sa.String(16), nullable=True))
    op.add_column("evidence", sa.Column("input_seed", sa.String(128), nullable=True))
    if op.get_bind().dialect.name != "postgresql":
        return
    op.create_check_constraint(
        "ck_evidence_oracle_visibility",
        "evidence",
        "oracle_visibility IS NULL OR oracle_visibility IN ('visible', 'held_out')",
    )


def downgrade():
    if op.get_bind().dialect.name == "postgresql":
        op.drop_constraint("ck_evidence_oracle_visibility", "evidence", type_="check")
    op.drop_column("evidence", "input_seed")
    op.drop_column("evidence", "oracle_visibility")
