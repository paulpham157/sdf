"""Pinned casefold column: migration, writers, and inbox failure modes."""

from __future__ import annotations

import hashlib
import os
import uuid
from datetime import datetime, timezone

import pytest
from alembic import command
from alembic.config import Config
from sqlalchemy import create_engine, inspect, text
from sqlalchemy.exc import IntegrityError

from sdf_core.casefold import SUPPORTED_UNICODE_VERSIONS, casefold
from sdf_core.db import PlanRow, install_sqlite_plan_casefold_triggers
from sdf_core.plans import SeparationOfDutiesError, decide_plan
from tests.auth_support import client_as


def test_an_unchecked_unicode_version_is_refused(monkeypatch):
    import unicodedata

    from sdf_core.casefold import UnsupportedUnicodeVersion

    monkeypatch.setattr(unicodedata, "unidata_version", "9.0.0", raising=False)
    with pytest.raises(UnsupportedUnicodeVersion, match="9.0.0"):
        casefold("Straße")


def test_casefold_digest_is_stable_on_supported_unicode():
    import unicodedata

    version = unicodedata.unidata_version
    if version not in SUPPORTED_UNICODE_VERSIONS:
        pytest.fail(
            f"Unicode {version} has not been checked against str.casefold. "
            f"Supported versions are {tuple(sorted(SUPPORTED_UNICODE_VERSIONS))}. "
            "Refusing to assume a new Unicode version still matches."
        )
    digest = hashlib.sha256()
    for code_point in range(0x110000):
        if 0xD800 <= code_point <= 0xDFFF:
            continue
        character = chr(code_point)
        folded = character.casefold()
        assert casefold(character) == folded
        digest.update(folded.encode("utf-8"))
        digest.update(b"\0")
    # Unicode 14.0.0, 15.0.0, and 15.1.0 share this full casefold.
    assert digest.hexdigest() == "2af944b6cb24e3be0307122af2e8a2b9396941bc32d0e82fdb3fac4f96327a83"
    assert casefold("Straße") == "strasse"
    assert casefold("ß") == "ss"
    assert casefold("Straße") != "Straße".lower()
    assert casefold("ﬃ") == "ffi"


def test_decide_plan_uses_the_same_casefold_as_the_stored_key():
    from sdf_core.api import SessionLocal

    plan_id = f"PLAN-FOLD-{uuid.uuid4().hex[:8]}"
    request_id = f"DREQ-{plan_id}"
    now = datetime(2019, 5, 1, tzinfo=timezone.utc)
    with SessionLocal() as db:
        db.add(PlanRow(
            id=plan_id, title="fold", status="proposed", content={"subtasks": []},
            content_sha256="a" * 64, proposed_by="Straße", created_at=now,
            decision_request_id=request_id,
        ))
        from sdf_core.db import DecisionRequestRow

        db.add(DecisionRequestRow(
            id=request_id, subject_kind="plan", subject_id=plan_id, question="Approve?",
            options=["approve", "reject"], status="pending", requested_by="Straße", created_at=now,
        ))
        db.commit()
        stored = db.get(PlanRow, plan_id).proposed_by_casefold
        assert stored == casefold("Straße") == casefold("strasse")
        with pytest.raises(SeparationOfDutiesError):
            decide_plan(db, plan_id, approve=True, decided_by="strasse")
        db.rollback()


def test_sqlite_rejects_a_missing_fold_and_inbox_fails_closed_if_one_is_forced():
    from sdf_core.api import SessionLocal

    engine = SessionLocal.kw["bind"]

    plan_id = f"PLAN-NULL-{uuid.uuid4().hex[:8]}"
    with pytest.raises(IntegrityError, match="proposed_by_casefold"):
        with engine.begin() as connection:
            connection.execute(text(
                "INSERT INTO plans (id, title, status, content, content_sha256, proposed_by, "
                "created_at, decision_request_id, proposed_by_casefold) "
                "VALUES (:id, 't', 'proposed', '{}', :sha, 'Ada', '2020-01-01', :req, NULL)"
            ), {"id": plan_id, "sha": "b" * 64, "req": f"DREQ-{plan_id}"})
    with engine.begin() as connection:
        connection.execute(text("DROP TRIGGER IF EXISTS trg_plans_casefold_insert"))
        connection.execute(text("DROP TRIGGER IF EXISTS trg_plans_casefold_update"))
        connection.execute(text(
            "INSERT INTO plans (id, title, status, content, content_sha256, proposed_by, "
            "created_at, decision_request_id, proposed_by_casefold) "
            "VALUES (:id, 't', 'proposed', '{}', :sha, 'Ada', '2020-01-01', :req, NULL)"
        ), {"id": plan_id, "sha": "b" * 64, "req": f"DREQ-{plan_id}"})
    try:
        response = client_as("approver", principal="ada").get("/inbox")
        assert response.status_code == 503
        assert response.json()["detail"].startswith(f"plan {plan_id} is missing")
        assert "items" not in response.json()
        with SessionLocal() as db:
            assert db.get(PlanRow, plan_id).proposed_by_casefold is None
    finally:
        with engine.begin() as connection:
            connection.execute(text("DELETE FROM plans WHERE id = :id"), {"id": plan_id})
            install_sqlite_plan_casefold_triggers(connection)
    with SessionLocal() as db:
        assert db.get(PlanRow, plan_id) is None


def test_a_corrupt_fold_fails_the_inbox_instead_of_listing_it():
    from sdf_core.api import SessionLocal
    from sdf_core.db import DecisionRequestRow

    engine = SessionLocal.kw["bind"]

    plan_id = f"PLAN-BAD-{uuid.uuid4().hex[:8]}"
    request_id = f"DREQ-{plan_id}"
    now = datetime(2019, 6, 1, tzinfo=timezone.utc)

    with SessionLocal() as db:
        db.add(PlanRow(
            id=plan_id, title="bad", status="proposed", content={"subtasks": []},
            content_sha256="c" * 64, proposed_by="Ada", created_at=now,
            decision_request_id=request_id,
        ))
        db.add(DecisionRequestRow(
            id=request_id, subject_kind="plan", subject_id=plan_id, question="Approve?",
            options=["approve", "reject"], status="pending", requested_by="Ada", created_at=now,
        ))
        db.commit()
    with pytest.raises(IntegrityError, match="immutable"):
        with engine.begin() as connection:
            connection.execute(
                text("UPDATE plans SET proposed_by_casefold = 'nope' WHERE id = :id"),
                {"id": plan_id},
            )
    with engine.begin() as connection:
        connection.execute(text("DROP TRIGGER IF EXISTS trg_plans_casefold_update"))
        connection.execute(
            text("UPDATE plans SET proposed_by_casefold = 'nope' WHERE id = :id"),
            {"id": plan_id},
        )
    try:
        cursor = None
        failed = None
        for _page in range(100):
            params = {"limit": 50}
            if cursor is not None:
                params["cursor"] = cursor
            response = client_as("approver", principal="bob").get("/inbox", params=params)
            if response.status_code == 503:
                failed = response
                break
            assert response.status_code == 200, response.text
            body = response.json()
            assert all(item["id"] != plan_id for item in body["items"])
            cursor = body["next_cursor"]
            if cursor is None:
                break
        assert failed is not None
        assert "does not match the pinned casefold" in failed.json()["detail"]
        assert "items" not in failed.json()
    finally:
        with engine.begin() as connection:
            connection.execute(text("DELETE FROM decision_requests WHERE id = :id"), {"id": request_id})
            connection.execute(text("DELETE FROM plans WHERE id = :id"), {"id": plan_id})
            install_sqlite_plan_casefold_triggers(connection)


def _insert_old_plan(connection, plan_id: str, proposed_by: str) -> None:
    connection.execute(text(
        "INSERT INTO plans (id, title, status, content, content_sha256, proposed_by, "
        "created_at, decision_request_id) "
        "VALUES (:id, 't', 'proposed', :content, :sha, :by, :at, :req)"
    ), {
        "id": plan_id, "content": "{}", "sha": "d" * 64, "by": proposed_by,
        "at": "2020-01-01 00:00:00", "req": f"DREQ-{plan_id}",
    })


def test_migration_backfills_a_populated_sqlite_schema(tmp_path, monkeypatch):
    database_url = f"sqlite:///{tmp_path / 'old.sqlite'}"
    monkeypatch.setenv("SDF_DATABASE_URL", database_url)
    monkeypatch.setattr("sdf_core.casefold.BACKFILL_BATCH", 2)
    config = Config("alembic.ini")
    command.upgrade(config, "0029_evidence_content_mac")
    engine = create_engine(database_url)
    names = ["Ada", "Straße", "ß", "ﬃ", "other"]
    with engine.begin() as connection:
        for index, name in enumerate(names):
            _insert_old_plan(connection, f"OLD-{index}", name)
    command.upgrade(config, "head")
    with engine.connect() as connection:
        rows = connection.execute(text(
            "SELECT proposed_by, proposed_by_casefold FROM plans ORDER BY id"
        )).all()
    assert [(row[0], row[1]) for row in rows] == [(name, casefold(name)) for name in names]
    with engine.begin() as connection:
        with pytest.raises(IntegrityError):
            _insert_old_plan(connection, "OLD-NULL", "Ada")
    command.downgrade(config, "0029_evidence_content_mac")
    assert "proposed_by_casefold" not in {
        column["name"] for column in inspect(engine).get_columns("plans")
    }
    command.upgrade(config, "head")
    with engine.connect() as connection:
        again = connection.execute(text("SELECT count(proposed_by_casefold) FROM plans")).scalar()
    assert again == len(names)
    engine.dispose()


@pytest.mark.skipif(not os.getenv("SDF_POSTGRES_TEST_URL"), reason="SDF_POSTGRES_TEST_URL is not set")
def test_migration_backfills_a_populated_postgres_schema(monkeypatch):
    from sqlalchemy.engine.url import make_url

    url = make_url(os.environ["SDF_POSTGRES_TEST_URL"])
    name = f"sdf_casefold_{uuid.uuid4().hex[:8]}"
    admin = url.set(database="postgres")
    import psycopg

    raw = psycopg.connect(host=url.host, port=url.port or 5432, user=url.username,
                          password=url.password, dbname="postgres", autocommit=True)
    raw.execute(f'CREATE DATABASE "{name}"')
    raw.close()
    target = url.set(database=name)
    monkeypatch.setenv("SDF_DATABASE_URL", target.render_as_string(hide_password=False))
    monkeypatch.setattr("sdf_core.casefold.BACKFILL_BATCH", 2)
    config = Config("alembic.ini")
    engine = create_engine(target)
    try:
        command.upgrade(config, "0029_evidence_content_mac")
        names = ["Ada", "Straße", "ß", "ﬃ", "other"]
        with engine.begin() as connection:
            for index, proposer in enumerate(names):
                _insert_old_plan(connection, f"OLD-{index}", proposer)
        command.upgrade(config, "head")
        with engine.connect() as connection:
            rows = connection.execute(text(
                "SELECT proposed_by, proposed_by_casefold FROM plans ORDER BY id"
            )).all()
        assert [(row[0], row[1]) for row in rows] == [(proposer, casefold(proposer)) for proposer in names]
        with pytest.raises(Exception):
            with engine.begin() as connection:
                connection.execute(text(
                    "INSERT INTO plans (id, title, status, content, content_sha256, proposed_by, "
                    "created_at, decision_request_id) "
                    "VALUES ('OLD-NULL', 't', 'proposed', '{}', :sha, 'Ada', "
                    "TIMESTAMP '2020-01-01', 'DREQ-OLD-NULL')"
                ), {"sha": "d" * 64})
        command.downgrade(config, "0029_evidence_content_mac")
        assert "proposed_by_casefold" not in {
            column["name"] for column in inspect(engine).get_columns("plans")
        }
    finally:
        engine.dispose()
        raw = psycopg.connect(host=url.host, port=url.port or 5432, user=url.username,
                              password=url.password, dbname="postgres", autocommit=True)
        raw.execute(f'DROP DATABASE IF EXISTS "{name}"')
        raw.close()
