"""Proposer casefold column (ADR 0044 amendment).

Adds ``plans.proposed_by_casefold`` and backfills it with ``str.casefold`` on a
supported Unicode version. SQL ``lower()`` is not used. Batches stay at
``BACKFILL_BATCH`` rows. The migration transaction holds locks until commit:
run it while the previous application is not inserting plans. After the
constraints commit, an old writer that omits the column fails the insert.

SQLite does not enforce VARCHAR length. Triggers reject a NULL fold, a fold
longer than 360 characters, a change to ``proposed_by``, and a change to a
fold that is already set. A direct insert of a non-null but incorrect fold is
unsupported; the inbox refuses to list a page that contains one.

PostgreSQL stores the column with COLLATE "C" so a locale cannot merge two
different folds. ``SET NOT NULL`` plus a length CHECK reject a missing or
oversized value. ``sdf_plan_guard`` rejects a later change to the proposer or
its fold.

``ix_plans_proposed_by_casefold`` supports ``fold = :caller``. It does not turn
the inbox's ordered scan, which skips the caller's plans, into an index seek.
``ix_decision_requests_status_created_id`` matches ``status`` plus the keyset
order. The engine may still read ineligible pending rows while it fills
``LIMIT``. ``ix_plans_casefold_null`` makes the missing-fold probe a lookup.

Downgrade drops the key and restores the previous plan guard. Roll the
application back in the same window. This revises ``0031_evidence_mac_v2``.
"""

from sqlalchemy import inspect, text

from alembic import op

revision = "0032_plan_casefold"
down_revision = "0031_evidence_mac_v2"
branch_labels = None
depends_on = None

_PLAN_GUARD = """
CREATE OR REPLACE FUNCTION sdf_plan_guard() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'plans cannot be deleted';
    END IF;
    IF NEW.proposed_by <> OLD.proposed_by THEN
        RAISE EXCEPTION 'plan proposer is immutable once proposed';
    END IF;
    IF NEW.proposed_by_casefold IS NULL THEN
        RAISE EXCEPTION 'plans.proposed_by_casefold is required';
    END IF;
    IF OLD.proposed_by_casefold IS NOT NULL
       AND NEW.proposed_by_casefold IS DISTINCT FROM OLD.proposed_by_casefold THEN
        RAISE EXCEPTION 'plan proposer casefold is immutable once set';
    END IF;
    IF char_length(NEW.proposed_by_casefold) > 360 THEN
        RAISE EXCEPTION 'plans.proposed_by_casefold exceeds 360 characters';
    END IF;
    IF NEW.content::text <> OLD.content::text OR NEW.content_sha256 <> OLD.content_sha256
       OR NEW.title <> OLD.title OR NEW.id <> OLD.id
       OR NEW.decision_request_id <> OLD.decision_request_id THEN
        RAISE EXCEPTION 'plan content is immutable once proposed';
    END IF;
    IF NEW.status <> OLD.status THEN
        IF OLD.status <> 'proposed' OR NEW.status NOT IN ('approved','rejected') THEN
            RAISE EXCEPTION 'plan status may only move once from proposed';
        END IF;
        IF NEW.status = 'approved' AND NOT EXISTS (
            SELECT 1 FROM decision_requests d WHERE d.id = NEW.decision_request_id
               AND d.subject_id = NEW.id AND d.status = 'approved' AND d.resolver_kind = 'human'
        ) THEN
            RAISE EXCEPTION 'plan approval requires a human-approved decision request';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
"""

_PLAN_GUARD_PREVIOUS = """
CREATE OR REPLACE FUNCTION sdf_plan_guard() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'plans cannot be deleted';
    END IF;
    IF NEW.content::text <> OLD.content::text OR NEW.content_sha256 <> OLD.content_sha256
       OR NEW.title <> OLD.title OR NEW.id <> OLD.id
       OR NEW.decision_request_id <> OLD.decision_request_id THEN
        RAISE EXCEPTION 'plan content is immutable once proposed';
    END IF;
    IF NEW.status <> OLD.status THEN
        IF OLD.status <> 'proposed' OR NEW.status NOT IN ('approved','rejected') THEN
            RAISE EXCEPTION 'plan status may only move once from proposed';
        END IF;
        IF NEW.status = 'approved' AND NOT EXISTS (
            SELECT 1 FROM decision_requests d WHERE d.id = NEW.decision_request_id
               AND d.subject_id = NEW.id AND d.status = 'approved' AND d.resolver_kind = 'human'
        ) THEN
            RAISE EXCEPTION 'plan approval requires a human-approved decision request';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
"""


def _backfill(connection) -> None:
    from sdf_core.casefold import BACKFILL_BATCH, casefold

    previous = None
    while True:
        rows = connection.execute(
            text(
                "SELECT id, proposed_by FROM plans "
                "WHERE proposed_by_casefold IS NULL ORDER BY id LIMIT :n"
            ),
            {"n": BACKFILL_BATCH},
        ).all()
        if not rows:
            return
        if previous == rows[0].id:
            raise RuntimeError("casefold backfill made no progress")
        previous = rows[0].id
        connection.execute(
            text("UPDATE plans SET proposed_by_casefold = :fold WHERE id = :id"),
            [{"id": row.id, "fold": casefold(row.proposed_by or "")} for row in rows],
        )


def upgrade() -> None:
    bind = op.get_bind()
    columns = {column["name"] for column in inspect(bind).get_columns("plans")}
    if "proposed_by_casefold" not in columns:
        if bind.dialect.name == "postgresql":
            op.execute(
                'ALTER TABLE plans ADD COLUMN proposed_by_casefold VARCHAR(360) COLLATE "C"'
            )
        else:
            op.execute(
                "ALTER TABLE plans ADD COLUMN proposed_by_casefold VARCHAR(360) COLLATE BINARY"
            )
    _backfill(bind)
    if bind.dialect.name == "postgresql":
        op.execute(_PLAN_GUARD)
        op.execute(
            "ALTER TABLE plans ALTER COLUMN proposed_by_casefold SET NOT NULL"
        )
        op.execute(
            "ALTER TABLE plans DROP CONSTRAINT IF EXISTS ck_plans_proposed_by_casefold_len"
        )
        op.execute(
            "ALTER TABLE plans ADD CONSTRAINT ck_plans_proposed_by_casefold_len "
            "CHECK (char_length(proposed_by_casefold) <= 360)"
        )
        op.execute(
            "CREATE INDEX IF NOT EXISTS ix_plans_proposed_by_casefold "
            'ON plans (proposed_by_casefold COLLATE "C")'
        )
    else:
        from sdf_core.db import install_sqlite_plan_casefold_triggers

        install_sqlite_plan_casefold_triggers(bind)
        op.execute(
            "CREATE INDEX IF NOT EXISTS ix_plans_proposed_by_casefold "
            "ON plans (proposed_by_casefold COLLATE BINARY)"
        )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_plans_casefold_null ON plans (id) "
        "WHERE proposed_by_casefold IS NULL"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_decision_requests_status_created_id "
        "ON decision_requests (status, created_at, id)"
    )


def downgrade() -> None:
    bind = op.get_bind()
    for name in (
        "ix_decision_requests_status_created_id",
        "ix_plans_casefold_null",
        "ix_plans_proposed_by_casefold",
    ):
        op.execute(f"DROP INDEX IF EXISTS {name}")
    if bind.dialect.name == "postgresql":
        op.execute("ALTER TABLE plans DROP CONSTRAINT IF EXISTS ck_plans_proposed_by_casefold_len")
        op.execute(_PLAN_GUARD_PREVIOUS)
    else:
        op.execute("DROP TRIGGER IF EXISTS trg_plans_casefold_insert")
        op.execute("DROP TRIGGER IF EXISTS trg_plans_casefold_update")
    op.drop_column("plans", "proposed_by_casefold")
