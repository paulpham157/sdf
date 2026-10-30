"""Evidence mode value set on attempts and evidence (ADR 0031).

``/run`` with the live adapter now records ``evidence_mode = 'live'`` (plan
runs already did). This adds the CHECK that the column holds one of
``sdf_core.impact.EvidenceMode``'s values. Existing rows are never rewritten:
Evidence is append-only, and live ``/run`` rows written before this change
stay ``synthetic`` (the ledger ``sandbox`` row remains the authoritative live
signal, ADR 0030). Rows with any other value make the upgrade fail with a
count, so an operator decides what they were.
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
        with op.batch_alter_table(table) as batch:
            batch.create_check_constraint(name, sa.text(_RULE))


def downgrade():
    for table, name in _TABLES.items():
        with op.batch_alter_table(table) as batch:
            batch.drop_constraint(name, type_="check")
