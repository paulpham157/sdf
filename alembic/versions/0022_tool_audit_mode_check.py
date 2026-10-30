"""Evidence mode value set on tool_audits (ADR 0031, issue #71).

The Tool Proxy now stamps each audit row with its Attempt's evidence mode at
write time (``live`` once the Attempt reached a real sandbox). This adds the
CHECK that ``tool_audits.evidence_mode`` holds one of
``sdf_core.impact.EvidenceMode``'s values.

Existing rows are never rewritten: tool audits are append-only (ADR 0011
trigger on PostgreSQL, session guard on SQLite). Rows written before this
change stay ``synthetic``. Rows with any other value make the upgrade fail
with a count, so an operator decides what they were.

SQLite cannot add a CHECK in place, so the table is rebuilt (batch "move and
copy"). The rebuild must keep every index exactly as it was, above all the
partial unique index ``uq_tool_audits_execution_event`` (``WHERE event <>
'policy_decided'``), which is the Tool Proxy's idempotency fence (ADR 0026).
It must also keep the ``DEFAULT 'synthetic' NOT NULL`` column definition. The
migration therefore records the CREATE statements of every index and trigger
on the table before the rebuild, restores any that are missing or changed
afterwards, and verifies the result. It raises if anything still differs.
The rebuild runs inside a SQLite ``SAVEPOINT`` (see ``_sqlite_rebuild``), so
a failure leaves the table, its indexes and ``alembic_version`` exactly as
they were at 0021.

Manual recovery, if a SQLite upgrade was interrupted outside SQLite's own
guarantees (for example, the file was copied mid-run, or a driver that
autocommits DDL was used):
1. Drop ``_alembic_tmp_tool_audits`` if it exists.
2. Compare ``sqlite_master`` for ``tool_audits`` with a fresh ``alembic
   upgrade head`` database.
3. If ``tool_audits`` already has ``CONSTRAINT ck_tool_audits_evidence_mode``
   and identical indexes, but ``alembic_version`` is still 0021, run
   ``alembic stamp 0022_tool_audit_mode_check``. Do not re-run the upgrade on
   top of it.
4. If the constraint is absent, simply re-run the upgrade.

PostgreSQL adds the constraint directly. ``ADD CONSTRAINT`` neither updates
rows nor fires the append-only trigger (``BEFORE UPDATE OR DELETE``). As in
0021, a constraint that an operator pre-created ``NOT VALID`` (for a large
table) is reused and validated.
"""

import sqlalchemy as sa

from alembic import op

revision = "0022_tool_audit_mode_check"
down_revision = "0021_evidence_mode_check"
branch_labels = None
depends_on = None

# Frozen copy of the value set at this revision; sdf_core.impact.EvidenceMode is the source.
_RULE = "evidence_mode IN ('synthetic', 'live')"
_TABLE = "tool_audits"
_NAME = "ck_tool_audits_evidence_mode"


def _sqlite_objects(bind) -> dict[str, str]:
    """Named indexes and triggers on the table -> their CREATE statement."""

    rows = bind.execute(sa.text(
        "SELECT name, sql FROM sqlite_master WHERE tbl_name = :t AND type IN ('index', 'trigger') "
        "AND sql IS NOT NULL"
    ), {"t": _TABLE}).all()
    return {name: sql for name, sql in rows}


def _column_ddl(bind, column: str) -> tuple:
    for cid, name, type_, notnull, default, pk in bind.execute(sa.text(f"PRAGMA table_info({_TABLE})")).all():
        if name == column:
            return type_, notnull, default
    return ()


_SAVEPOINT = "sdf_0022_tool_audits"


def _sqlite_rebuild(bind, alter) -> None:
    """Rebuild atomically: on any failure the table is exactly as before.

    The pysqlite driver does not open a transaction for DDL, so a plain
    rebuild that fails half-way could leave ``_alembic_tmp_tool_audits``
    behind or the new table in place with the version still at 0021. A
    ``SAVEPOINT`` opens a real SQLite transaction (or nests in an open one).
    On failure it is rolled back and the error re-raised. On success it is
    left open, so Alembic's version stamp and commit land together with the
    rebuild.
    """

    op.execute(f"SAVEPOINT {_SAVEPOINT}")
    try:
        before = _sqlite_objects(bind)
        column_before = _column_ddl(bind, "evidence_mode")
        with op.batch_alter_table(_TABLE) as batch:
            alter(batch)
        after = _sqlite_objects(bind)
        for name, sql in before.items():
            if after.get(name) != sql:
                if name in after:
                    op.execute(f"DROP {'TRIGGER' if sql.upper().startswith('CREATE TRIGGER') else 'INDEX'} {name}")
                op.execute(sql)
        restored = _sqlite_objects(bind)
        if restored != before or _column_ddl(bind, "evidence_mode") != column_before:
            raise RuntimeError(
                f"{_TABLE} rebuild changed its indexes, triggers or evidence_mode column; refusing (ADR 0031)"
            )
    except BaseException:
        op.execute(f"ROLLBACK TO SAVEPOINT {_SAVEPOINT}")
        op.execute(f"RELEASE SAVEPOINT {_SAVEPOINT}")
        raise


def upgrade():
    bind = op.get_bind()
    bad = bind.execute(sa.text(f"SELECT COUNT(*) FROM {_TABLE} WHERE NOT ({_RULE})")).scalar()
    if bad:
        raise RuntimeError(
            f"{bad} {_TABLE} row(s) have an evidence_mode outside ('synthetic', 'live'); "
            "inspect them before upgrading (ADR 0031)"
        )
    if bind.dialect.name == "postgresql":
        validated = bind.execute(sa.text(
            "SELECT convalidated FROM pg_constraint WHERE conname = :name AND conrelid = to_regclass(:table)"
        ), {"name": _NAME, "table": _TABLE}).scalar()
        if validated is None:
            op.execute(f"ALTER TABLE {_TABLE} ADD CONSTRAINT {_NAME} CHECK ({_RULE})")
        elif not validated:
            op.execute(f"ALTER TABLE {_TABLE} VALIDATE CONSTRAINT {_NAME}")
        return
    if bind.dialect.name == "sqlite":
        _sqlite_rebuild(bind, lambda batch: batch.create_check_constraint(_NAME, sa.text(_RULE)))
        return
    with op.batch_alter_table(_TABLE) as batch:
        batch.create_check_constraint(_NAME, sa.text(_RULE))


def downgrade():
    bind = op.get_bind()
    if bind.dialect.name == "postgresql":
        op.execute(f"ALTER TABLE {_TABLE} DROP CONSTRAINT IF EXISTS {_NAME}")
        return
    if bind.dialect.name == "sqlite":
        _sqlite_rebuild(bind, lambda batch: batch.drop_constraint(_NAME, type_="check"))
        return
    with op.batch_alter_table(_TABLE) as batch:
        batch.drop_constraint(_NAME, type_="check")
