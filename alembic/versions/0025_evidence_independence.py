"""Evidence independence columns (ADR 0037).

Adds nullable columns on ``evidence`` so a row can say what class it is
(``execution``, ``verification`` or ``outcome``), who produced and verified it,
which artifact bytes it checked, which oracle and environment it used, and
when it was collected and expires.

Existing rows are not rewritten. Evidence is append-only, and a row from
before this migration keeps NULL, which cannot accept an artifact. No backfill
is attempted: those runs never recorded a verifier or an oracle digest, and
the values must not be guessed.

The columns are plain nullable ``ADD COLUMN`` on both dialects (no SQLite
table rebuild, so no index, trigger or CHECK is touched). ``ADD COLUMN`` on
PostgreSQL neither updates rows nor fires the Evidence append-only trigger.

PostgreSQL also gets ``ck_evidence_evidence_class``: NULL, or one of the three
classes. SQLite relies on the ORM session guard in ``sdf_core.db``.

Downgrade drops the check (PostgreSQL) and the columns. It discards the
independence record.
"""

import sqlalchemy as sa

from alembic import op

revision = "0025_evidence_independence"
down_revision = "0024_decision_material_fence"
branch_labels = None
depends_on = None

_COLUMNS = (
    ("evidence_class", sa.String(16)),
    ("producer_identity", sa.String(120)),
    ("producer_role", sa.String(32)),
    ("verifier_identity", sa.String(120)),
    ("verifier_role", sa.String(32)),
    ("subject_artifact_sha256", sa.String(64)),
    ("oracle_digest", sa.String(64)),
    ("environment_digest", sa.String(64)),
    ("collected_at", sa.DateTime(timezone=True)),
    ("expires_at", sa.DateTime(timezone=True)),
)


def upgrade():
    for name, type_ in _COLUMNS:
        op.add_column("evidence", sa.Column(name, type_, nullable=True))
    if op.get_bind().dialect.name != "postgresql":
        return
    op.create_check_constraint(
        "ck_evidence_evidence_class",
        "evidence",
        "evidence_class IS NULL OR evidence_class IN ('execution', 'verification', 'outcome')",
    )


def downgrade():
    if op.get_bind().dialect.name == "postgresql":
        op.drop_constraint("ck_evidence_evidence_class", "evidence", type_="check")
    for name, _type in reversed(_COLUMNS):
        op.drop_column("evidence", name)
