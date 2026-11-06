"""Objective link for outcome Evidence, and a nullable attempt (ADR 0042).

Adds nullable ``objective_id`` on ``evidence``. ``attempt_id`` becomes
nullable so an outcome row can name an Objective and not an Attempt.

``evidence_class`` stays nullable. Rows written before a class was required
keep NULL and are not rewritten. Evidence is append-only. ``ADD COLUMN``
does not fire the append-only trigger. This migration does not add
``NOT NULL`` on ``evidence_class``.

PostgreSQL ``ck_evidence_class_subject`` allows NULL, or execution /
verification with an attempt and no objective, or outcome with an objective
and no attempt. SQLite gets the same CHECK. The ORM refuses a new row with
no class; the CHECK does not, so a historical NULL still inserts and reads.

The evidence insert guard learns the outcome shape: no attempt, no
revision. Attempt-scoped rows keep the ADR 0032 binding rules. Artifacts
still use ``sdf_revision_binding_guard``.

Downgrade refuses to run when an outcome row has a NULL ``attempt_id``,
because restoring ``NOT NULL`` would require deleting Evidence. It drops
the objective column, the link check, and the outcome guard, and restores
the previous evidence trigger.
"""

import sqlalchemy as sa

from alembic import op

revision = "0028_evidence_classes"
down_revision = "0026_oracle_visibility"
branch_labels = None
depends_on = None

# Frozen copy of sdf_core.acceptance.EVIDENCE_SUBJECT_SQL at this revision.
_SUBJECT_RULE = (
    "evidence_class IS NULL OR "
    "(evidence_class IN ('execution', 'verification') AND attempt_id IS NOT NULL AND objective_id IS NULL) OR "
    "(evidence_class = 'outcome' AND objective_id IS NOT NULL AND attempt_id IS NULL)"
)

# Same binding rules as sdf_revision_binding_guard (migration 0023), plus the
# outcome short-circuit. This function is only installed on evidence, so it
# may read evidence columns artifacts do not have.
_EVIDENCE_GUARD = """
CREATE FUNCTION sdf_evidence_revision_binding_guard() RETURNS trigger AS $$
DECLARE
    own_id text;
    own_tree text;
    other_attempt text;
    required boolean;
    attempt_found boolean;
BEGIN
    IF NEW.attempt_id IS NULL THEN
        IF NEW.evidence_class IS DISTINCT FROM 'outcome'
           OR NEW.objective_id IS NULL
           OR NEW.revision_id IS NOT NULL
           OR NEW.tree_sha256 IS NOT NULL THEN
            RAISE EXCEPTION 'outcome evidence % must name an objective and no attempt revision (ADR 0042)', NEW.id;
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.evidence_class = 'outcome' THEN
        RAISE EXCEPTION 'outcome evidence % must not name an attempt (ADR 0042)', NEW.id;
    END IF;
    SELECT id, result_tree_sha256 INTO own_id, own_tree FROM attempt_revisions WHERE attempt_id = NEW.attempt_id;
    IF NEW.revision_id IS NULL THEN
        IF own_id IS NOT NULL THEN
            RAISE EXCEPTION 'missing revision binding: % % has none but attempt % has revision %',
                TG_TABLE_NAME, NEW.id, NEW.attempt_id, own_id;
        END IF;
        IF NEW.tree_sha256 IS NOT NULL THEN
            RAISE EXCEPTION 'revision binding: % % names a tree without a revision', TG_TABLE_NAME, NEW.id;
        END IF;
        SELECT revision_required, true INTO required, attempt_found FROM attempts WHERE id = NEW.attempt_id;
        IF attempt_found IS NULL THEN
            RAISE EXCEPTION 'missing revision binding: % % names unknown attempt %', TG_TABLE_NAME, NEW.id,
                NEW.attempt_id;
        END IF;
        IF required IS NOT NULL THEN
            RAISE EXCEPTION 'missing revision binding: % %: attempt % was created after migration 0023 and has no revision',
                TG_TABLE_NAME, NEW.id, NEW.attempt_id;
        END IF;
        RETURN NEW;
    END IF;
    IF own_id IS NULL OR own_id <> NEW.revision_id THEN
        SELECT attempt_id INTO other_attempt FROM attempt_revisions WHERE id = NEW.revision_id;
        IF other_attempt IS NOT NULL THEN
            RAISE EXCEPTION 'cross-Attempt revision binding: % % of attempt % refers to a revision of attempt %',
                TG_TABLE_NAME, NEW.id, NEW.attempt_id, other_attempt;
        END IF;
        RAISE EXCEPTION 'revision binding: % % refers to unknown revision %', TG_TABLE_NAME, NEW.id, NEW.revision_id;
    END IF;
    IF own_tree IS NULL OR NEW.tree_sha256 IS DISTINCT FROM own_tree THEN
        RAISE EXCEPTION 'mismatched revision: % % does not name the result tree of revision %',
            TG_TABLE_NAME, NEW.id, own_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
"""


def upgrade():
    op.add_column("evidence", sa.Column("objective_id", sa.String(120), nullable=True))
    bind = op.get_bind()
    outcome = bind.execute(sa.text(
        "SELECT COUNT(*) FROM evidence WHERE evidence_class = 'outcome'"
    )).scalar()
    if outcome:
        raise RuntimeError(
            f"{outcome} evidence row(s) already have evidence_class 'outcome' without an objective link; "
            "inspect them before upgrading (ADR 0042). This migration does not rewrite Evidence."
        )
    if bind.dialect.name == "postgresql":
        op.execute("ALTER TABLE evidence ALTER COLUMN attempt_id DROP NOT NULL")
        op.create_check_constraint("ck_evidence_class_subject", "evidence", _SUBJECT_RULE)
        op.execute("DROP TRIGGER IF EXISTS trg_evidence_revision_binding ON evidence")
        op.execute(_EVIDENCE_GUARD)
        op.execute(
            "CREATE TRIGGER trg_evidence_revision_binding BEFORE INSERT ON evidence "
            "FOR EACH ROW EXECUTE FUNCTION sdf_evidence_revision_binding_guard()"
        )
    else:
        with op.batch_alter_table("evidence") as batch:
            batch.alter_column("attempt_id", existing_type=sa.String(length=120), nullable=True)
            batch.create_check_constraint("ck_evidence_class_subject", _SUBJECT_RULE)
    op.create_index("ix_evidence_objective_id", "evidence", ["objective_id"])


def downgrade():
    bind = op.get_bind()
    nulls = bind.execute(sa.text("SELECT COUNT(*) FROM evidence WHERE attempt_id IS NULL")).scalar()
    if nulls:
        raise RuntimeError(
            f"{nulls} evidence row(s) have no attempt_id. Downgrade cannot restore NOT NULL "
            "without deleting Evidence, and Evidence is append-only (ADR 0042)."
        )
    op.drop_index("ix_evidence_objective_id", table_name="evidence")
    if bind.dialect.name == "postgresql":
        op.execute("DROP TRIGGER IF EXISTS trg_evidence_revision_binding ON evidence")
        op.execute(
            "CREATE TRIGGER trg_evidence_revision_binding BEFORE INSERT ON evidence "
            "FOR EACH ROW EXECUTE FUNCTION sdf_revision_binding_guard()"
        )
        op.execute("DROP FUNCTION IF EXISTS sdf_evidence_revision_binding_guard()")
        op.drop_constraint("ck_evidence_class_subject", "evidence", type_="check")
        op.drop_column("evidence", "objective_id")
        op.execute("ALTER TABLE evidence ALTER COLUMN attempt_id SET NOT NULL")
    else:
        with op.batch_alter_table("evidence") as batch:
            batch.drop_constraint("ck_evidence_class_subject", type_="check")
            batch.alter_column("attempt_id", existing_type=sa.String(length=120), nullable=False)
        op.drop_column("evidence", "objective_id")
