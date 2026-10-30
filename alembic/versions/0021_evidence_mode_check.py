"""Evidence mode value set on attempts and evidence (ADR 0031).

``/run`` with the live adapter now records ``evidence_mode = 'live'`` (plan
runs already did). This adds the CHECK that the column holds one of
``sdf_core.impact.EvidenceMode``'s values. Existing rows are never rewritten:
Evidence is append-only, and live ``/run`` rows written before this change
stay ``synthetic`` (the ledger ``sandbox`` row remains the authoritative live
signal, ADR 0030). Rows with any other value make the upgrade fail with a
count, so an operator decides what they were.

PostgreSQL locking: ``ADD CONSTRAINT ... CHECK`` takes ``ACCESS EXCLUSIVE`` and
scans the table. Splitting it into ``NOT VALID`` + ``VALIDATE CONSTRAINT``
inside this migration would not help: Alembic runs the migration in one
transaction, so the first lock is held until commit anyway, and an
autocommit block would let the constraint and the version stamp diverge on a
crash. At today's table sizes the scan is short. For a large ``evidence``
table, an operator can add the constraints beforehand, outside a transaction:
``ALTER TABLE evidence ADD CONSTRAINT ck_evidence_evidence_mode CHECK (...)
NOT VALID; ALTER TABLE evidence VALIDATE CONSTRAINT ck_evidence_evidence_mode``
(same for ``attempts``). This migration then reuses them: it adds only the
missing constraints and validates any that are still ``NOT VALID``.
"""

import sqlalchemy as sa

from alembic import op

revision = "0021_evidence_mode_check"
down_revision = "0020_routing_decisions"
branch_labels = None
depends_on = None

# Frozen copy of the value set at this revision (a migration must not change
# meaning if the enum grows later); sdf_core.impact.EvidenceMode is the source.
_RULE = "evidence_mode IN ('synthetic', 'live')"
_TABLES = {"attempts": "ck_attempts_evidence_mode", "evidence": "ck_evidence_evidence_mode"}


def upgrade():
    bind = op.get_bind()
    for table in _TABLES:
        bad = bind.execute(sa.text(f"SELECT COUNT(*) FROM {table} WHERE NOT ({_RULE})")).scalar()
        if bad:
            raise RuntimeError(
                f"{bad} {table} row(s) have an evidence_mode outside ('synthetic', 'live'); "
                "inspect them before upgrading (ADR 0031)"
            )
    for table, name in _TABLES.items():
        if bind.dialect.name == "postgresql":
            validated = bind.execute(sa.text(
                "SELECT convalidated FROM pg_constraint WHERE conname = :name AND conrelid = to_regclass(:table)"
            ), {"name": name, "table": table}).scalar()
            if validated is None:
                op.execute(f"ALTER TABLE {table} ADD CONSTRAINT {name} CHECK ({_RULE})")
            elif not validated:
                op.execute(f"ALTER TABLE {table} VALIDATE CONSTRAINT {name}")
            continue
        with op.batch_alter_table(table) as batch:
            batch.create_check_constraint(name, sa.text(_RULE))


def downgrade():
    for table, name in _TABLES.items():
        with op.batch_alter_table(table) as batch:
            batch.drop_constraint(name, type_="check")
