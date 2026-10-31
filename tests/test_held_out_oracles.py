"""ADR 0035: visible examples are not outcome-level proof.

A subject that hard-codes the visible example, or that patches the worker
RPC encoder to report it, passes that check. Acceptance is denied because
the held-out or randomized check fails. Held-out bytes are not in the
workspace, MCP tool results, or API responses. The seed is recorded after
the run and is not readable by the subject before or during it.
"""

from __future__ import annotations

import json
import os
import sys
import uuid
from pathlib import Path

import pytest
from alembic import command
from alembic.config import Config
from sqlalchemy import create_engine, inspect, select, text
from sqlalchemy.orm import sessionmaker

from sdf_core.acceptance import (
    AcceptanceError,
    check_hidden_oracle_acceptance,
    check_verification_acceptance,
)
from sdf_core.api import SessionLocal
from sdf_core.artifacts import ArtifactStore, WorkspaceManager
from sdf_core.checks import CheckConfigError, CheckRegistry, OraclePolicy
from sdf_core.core_mcp import CoreQueryService
from sdf_core.db import ArtifactRow, AttemptRow, DecisionEdgeRow, EvidenceRow
from sdf_core.evaluator import DeterministicEvaluator, Evidence
from sdf_core.model import utcnow
from sdf_core.oracle import HELD_OUT_DIR, seal_oracle_digest
from sdf_core.revisions import tree_digest
from tests.auth_support import admin_client
from tests.check_support import write_checks
from tests.revision_support import bound

client = admin_client()
SENTINEL = "HELD_OUT_SENTINEL_9f3c2a"

# The contiguous sentinel lives only in held-out files. The probe builds it
# at runtime so the subject source, which is copied into the workspace, does
# not itself contain the oracle bytes.
PROBE = '''
def probe():
    import os
    marker = "HELD_OUT_SENTINEL_" + "9f3c2a"
    if os.environ.get("SDF_ORACLE_SEED"):
        return "env"
    ppid = os.getppid()
    for name in ("environ", "cmdline"):
        try:
            raw = open(f"/proc/{ppid}/{name}", "rb").read()
        except OSError:
            raw = b""
        if b"SDF_ORACLE_SEED" in raw or marker.encode() in raw:
            return "parent-" + name
    roots = [os.environ.get("HOME") or "", os.environ.get("SDF_SANDBOX_ROOT") or "", os.getcwd()]
    names = (
        ".sdf-held-out/test_hidden.py",
        ".sdf-held-out/test_property.py",
        "test_hidden.py",
        "test_property.py",
    )
    for root in roots:
        if not root:
            continue
        for rel in names:
            try:
                text = open(os.path.join(root, rel), encoding="utf-8").read()
            except OSError:
                continue
            if marker in text:
                return "file"
    for rel in ("/proc/self/cwd/../.sdf-held-out/test_hidden.py",
                "/proc/self/cwd/../.sdf-held-out/test_property.py"):
        try:
            text = open(rel, encoding="utf-8").read()
        except OSError:
            continue
        if marker in text:
            return "proc-cwd"
    return ""
'''

HONEST = "def add(a, b):\n    return a + b\n\n" + PROBE
HARDCODE = (
    "def add(a, b):\n"
    "    if (a, b) == (2, 3):\n"
    "        return 5\n"
    "    return a - b\n\n"
) + PROBE
ENCODER_LIE = (
    "def add(a, b):\n"
    "    return a - b\n\n"
    "def _lie():\n"
    "    import sys\n"
    "    main = sys.modules.get('__main__')\n"
    "    if main is None or not hasattr(main, 'encode'):\n"
    "        return\n"
    "    orig = main.encode\n"
    "    def encode(obj, _orig=orig):\n"
    "        if isinstance(obj, int) and not isinstance(obj, bool):\n"
    "            return {'kind': 'value', 'value': 5}\n"
    "        return _orig(obj)\n"
    "    main.encode = encode\n"
    "_lie()\n\n"
) + PROBE

VISIBLE = "from calc import add\n\ndef test_visible_example():\n    assert add(2, 3) == 5\n"
HIDDEN = (
    f"# {SENTINEL}\n"
    "from calc import add, probe\n\n"
    "def test_hidden_case():\n"
    "    assert probe() == \"\"\n"
    "    assert add(7, 11) == 18\n"
)
PROPERTY = (
    f"# {SENTINEL}\n"
    "import os\n"
    "import random\n"
    "from calc import add, probe\n\n"
    "def test_seed_hidden_from_the_subject():\n"
    "    assert probe() == \"\"\n\n"
    "def test_add_property():\n"
    "    seed = os.environ['SDF_ORACLE_SEED']\n"
    "    rng = random.Random(seed)\n"
    "    for _ in range(8):\n"
    "        a = rng.randrange(1, 1_000_000)\n"
    "        b = rng.randrange(1, 1_000_000)\n"
    "        assert add(a, b) - a == b\n"
)


@pytest.fixture(autouse=True)
def _venv_on_path(monkeypatch):
    monkeypatch.setenv("PATH", f"{os.path.dirname(sys.executable)}{os.pathsep}{os.environ.get('PATH', '')}")


def _fixture(tmp_path: Path, calc: str) -> Path:
    root = tmp_path / "fixture"
    (root / "tests").mkdir(parents=True)
    (root / "calc.py").write_text(calc, encoding="utf-8")
    (root / "tests" / "test_visible.py").write_text(VISIBLE, encoding="utf-8")
    held = root / HELD_OUT_DIR
    held.mkdir()
    (held / "test_hidden.py").write_text(HIDDEN, encoding="utf-8")
    (held / "test_property.py").write_text(PROPERTY, encoding="utf-8")
    write_checks(root, {"visible-example": ["pytest", "-q", "tests/test_visible.py"]})
    (held / "sdf-checks.json").write_text(json.dumps({"checks": {
        "hidden-case": ["pytest", "-q", ".sdf-held-out/test_hidden.py"],
        "hidden-property": {
            "argv": ["pytest", "-q", ".sdf-held-out/test_property.py"],
            "randomized": True,
        },
    }}), encoding="utf-8")
    return root


def _run_task(fixture: Path, criteria: dict[str, list[str]], *, assumption: str | None = None) -> dict:
    task_id = f"TASK-HO-{uuid.uuid4().hex[:10]}"
    created = client.post("/tasks", json={
        "id": task_id, "title": "held-out", "idempotency_key": task_id,
        "acceptance_criteria": list(criteria),
    })
    assert created.status_code == 201, created.text
    body: dict = {"fixture_dir": str(fixture), "criterion_checks": criteria, "instructions": "complete the task"}
    if assumption is not None:
        client.post("/assumptions", json={
            "id": assumption, "title": "add is addition", "owner": "product", "source": "test",
        })
        body["validation_target_kind"] = "assumption"
        body["validation_target_id"] = assumption
    response = client.post(f"/tasks/{task_id}/run", json=body)
    assert response.status_code == 200, response.text
    return response.json()


def _rows(attempt_id: str) -> list[EvidenceRow]:
    with SessionLocal() as db:
        return list(db.scalars(select(EvidenceRow).where(EvidenceRow.attempt_id == attempt_id)))


def _assert_sentinel_absent(attempt_id: str, payload: dict) -> None:
    """Held-out source is not in the workspace, artifacts, or these responses."""

    blobs = [json.dumps(payload, default=str)]
    evidence = client.get(f"/runs/{attempt_id}/timeline")
    assert evidence.status_code == 200
    blobs.append(evidence.text)
    trace = None
    for row in _rows(attempt_id):
        got = client.get(f"/evidence/{row.id}")
        assert got.status_code == 200
        blobs.append(got.text)
        with SessionLocal() as db:
            stored = db.get(EvidenceRow, row.id)
            blobs.append(json.dumps(CoreQueryService._evidence(stored), default=str))
    with SessionLocal() as db:
        attempt = db.get(AttemptRow, attempt_id)
        trace_response = client.get(f"/tasks/{attempt.task_id}/trace")
        assert trace_response.status_code == 200
        blobs.append(trace_response.text)
        trace = trace_response.json()
    for root_name in (".sdf-workspaces", ".sdf-artifacts"):
        root = Path(root_name)
        if not root.exists():
            continue
        for path in root.rglob("*"):
            if not path.is_file() or attempt_id not in str(path):
                continue
            blobs.append(path.read_text(encoding="utf-8", errors="replace"))
    combined = "\n".join(blobs)
    assert SENTINEL not in combined, "held-out oracle content leaked"
    assert trace is not None


# --- registry and digest ------------------------------------------------------


def test_oracle_digest_covers_visibility_and_seed():
    argv = ["pytest", "-q", "tests/test_visible.py"]
    visible = seal_oracle_digest(argv)
    held = seal_oracle_digest(argv, visibility="held_out")
    seeded = seal_oracle_digest(argv, visibility="held_out", input_seed="abc")
    assert len({visible, held, seeded}) == 3
    assert visible == seal_oracle_digest(argv, visibility="visible", input_seed=None)
    assert seeded == seal_oracle_digest(argv, visibility="held_out", input_seed="abc")


def test_public_manifest_cannot_declare_a_held_out_check(tmp_path):
    write_checks(tmp_path, {"hidden": {"argv": ["pytest", "-q", ".sdf-held-out/t.py"], "visibility": "held_out"}})
    with pytest.raises(CheckConfigError, match="copied into the agent workspace"):
        CheckRegistry.load(tmp_path)


def test_held_out_manifest_rejects_python_c(tmp_path):
    held = tmp_path / HELD_OUT_DIR
    held.mkdir()
    (held / "sdf-checks.json").write_text(
        json.dumps({"checks": {"bad": ["python3", "-c", f"assert {SENTINEL!r} == {SENTINEL!r}"]}}),
        encoding="utf-8",
    )
    with pytest.raises(CheckConfigError, match="not -c"):
        CheckRegistry.load(tmp_path)


def test_held_out_check_must_live_under_the_server_directory(tmp_path):
    held = tmp_path / HELD_OUT_DIR
    held.mkdir()
    (held / "sdf-checks.json").write_text(
        json.dumps({"checks": {"bad": ["pytest", "-q", "tests/test_visible.py"]}}),
        encoding="utf-8",
    )
    with pytest.raises(CheckConfigError, match="must live under"):
        CheckRegistry.load(tmp_path)


def test_workspace_copy_omits_held_out_and_the_base_tree_still_matches(tmp_path):
    fixture = _fixture(tmp_path, HONEST)
    workspace = WorkspaceManager(tmp_path / "ws").create("ATTEMPT-HO", fixture)
    assert not (workspace / HELD_OUT_DIR).exists()
    copied = "\n".join(
        path.read_text(encoding="utf-8") for path in workspace.rglob("*") if path.is_file()
    )
    assert SENTINEL not in copied
    assert tree_digest(fixture, skip_names=frozenset({HELD_OUT_DIR})) == tree_digest(workspace)
    assert tree_digest(fixture) != tree_digest(workspace)
    # An agent file under that name is part of the result tree.
    planted = workspace / HELD_OUT_DIR / "planted.py"
    planted.parent.mkdir()
    planted.write_text("agent = True\n", encoding="utf-8")
    assert tree_digest(workspace) != tree_digest(fixture, skip_names=frozenset({HELD_OUT_DIR}))


def test_diff_does_not_include_held_out_bytes(tmp_path):
    fixture = _fixture(tmp_path, HONEST)
    workspace = WorkspaceManager(tmp_path / "ws2").create("ATTEMPT-HO2", fixture)
    diff = ArtifactStore(tmp_path / "art").capture_diff(
        artifact_id="ART", before=fixture, after=workspace, files=[f"{HELD_OUT_DIR}/test_hidden.py"],
    )
    assert SENTINEL not in diff.path.read_text(encoding="utf-8")


def test_visible_only_pass_cannot_satisfy_a_task_that_declared_a_hidden_oracle():
    from datetime import timedelta

    future = utcnow() + timedelta(hours=1)
    visible = Evidence(
        evidence_id="EV-V", attempt_id="A", kind="evaluation", status="PASS", command="pytest -q t.py",
        exit_code=0, stdout="", stderr="", confidence=1.0, evidence_class="verification",
        verifier_identity="sdf-evaluator", verifier_role="evaluator",
        subject_artifact_sha256="a" * 64, oracle_digest="b" * 64,
        oracle_visibility="visible", collected_at=utcnow(), expires_at=future,
    )
    check_verification_acceptance(visible, attempt_agent="native", artifact_sha256="a" * 64)
    with pytest.raises(AcceptanceError, match="visible-only pass cannot accept"):
        check_hidden_oracle_acceptance(
            [visible], required=True, attempt_agent="native", artifact_sha256="a" * 64,
        )
    check_hidden_oracle_acceptance(
        [visible], required=False, attempt_agent="native", artifact_sha256="a" * 64,
    )
    hidden = Evidence(
        evidence_id="EV-H", attempt_id="A", kind="evaluation", status="PASS", command="pytest -q h.py",
        exit_code=0, stdout="", stderr="", confidence=1.0, evidence_class="verification",
        verifier_identity="sdf-evaluator", verifier_role="evaluator",
        subject_artifact_sha256="a" * 64, oracle_digest="c" * 64,
        oracle_visibility="held_out", input_seed="d" * 64,
        collected_at=utcnow(), expires_at=future,
    )
    check_hidden_oracle_acceptance(
        [visible, hidden], required=True, attempt_agent="native", artifact_sha256="a" * 64,
    )


# --- the subject cannot see the seed -----------------------------------------


def test_seed_is_not_readable_by_the_subject_before_or_during_the_run(tmp_path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "calc.py").write_text(HONEST, encoding="utf-8")
    (fixture / "test_property.py").write_text(
        "import os, random\n"
        "from calc import add, probe\n\n"
        "def test_seed_hidden_from_the_subject():\n"
        "    assert probe() == ''\n\n"
        "def test_add_property():\n"
        "    seed = os.environ['SDF_ORACLE_SEED']\n"
        "    rng = random.Random(seed)\n"
        "    a = rng.randrange(1, 1_000_000)\n"
        "    b = rng.randrange(1, 1_000_000)\n"
        "    assert add(a, b) - a == b\n",
        encoding="utf-8",
    )
    argv = ["pytest", "-q", "test_property.py"]
    policies = {("property", 0): OraclePolicy(visibility="visible", randomized=True)}
    before = "\n".join(path.read_text(encoding="utf-8") for path in fixture.rglob("*") if path.is_file())
    first = DeterministicEvaluator().evaluate(
        attempt_id="A", workspace=fixture, criteria=("property",),
        criterion_checks={"property": (argv,)}, reference=fixture,
        subject_artifact_sha256="ab" * 32, oracle_policies=policies,
    )
    second = DeterministicEvaluator().evaluate(
        attempt_id="B", workspace=fixture, criteria=("property",),
        criterion_checks={"property": (argv,)}, reference=fixture,
        subject_artifact_sha256="ab" * 32, oracle_policies=policies,
    )
    assert first.status == "PASS", first.evidence[0].stderr
    assert second.status == "PASS", second.evidence[0].stderr
    row = first.evidence[0]
    assert row.oracle_visibility == "visible"
    assert row.input_seed and len(row.input_seed) == 64
    assert row.input_seed != second.evidence[0].input_seed
    assert row.oracle_digest != second.evidence[0].oracle_digest
    assert row.input_seed not in before
    after = "\n".join(path.read_text(encoding="utf-8") for path in fixture.rglob("*") if path.is_file())
    assert row.input_seed not in after
    assert SENTINEL not in after


# --- adversarial acceptance ---------------------------------------------------


def test_hardcoded_visible_example_passes_and_acceptance_is_denied(tmp_path):
    fixture = _fixture(tmp_path, HARDCODE)
    assumption = f"ASSUMPTION-HO-{uuid.uuid4().hex[:8]}"
    body = _run_task(fixture, {
        "visible example": ["visible-example"],
        "held-out case": ["hidden-case"],
    }, assumption=assumption)
    assert body["task_status"] == "failed"
    rows = _rows(body["attempt_id"])
    by_criterion = {row.criterion: row for row in rows}
    assert by_criterion["visible example"].status == "PASS"
    assert by_criterion["visible example"].oracle_visibility == "visible"
    assert by_criterion["visible example"].input_seed is None
    hidden = by_criterion["held-out case"]
    assert hidden.status == "FAIL"
    assert hidden.oracle_visibility == "held_out"
    assert hidden.input_seed is None
    assert SENTINEL not in (hidden.command or "")
    _assert_sentinel_absent(body["attempt_id"], body)
    with SessionLocal() as db:
        edges = list(db.scalars(select(DecisionEdgeRow).where(
            DecisionEdgeRow.target_id == assumption, DecisionEdgeRow.relation == "validates",
        )))
        assert edges == []


def test_rpc_encoder_lie_passes_the_visible_check_and_fails_the_held_out_check(tmp_path):
    fixture = _fixture(tmp_path, ENCODER_LIE)
    body = _run_task(fixture, {
        "visible example": ["visible-example"],
        "held-out case": ["hidden-case"],
    })
    assert body["task_status"] == "failed"
    rows = {row.criterion: row for row in _rows(body["attempt_id"])}
    assert rows["visible example"].status == "PASS"
    assert rows["held-out case"].status == "FAIL"
    assert rows["held-out case"].oracle_visibility == "held_out"
    _assert_sentinel_absent(body["attempt_id"], body)


def test_rpc_encoder_lie_fails_the_randomized_check(tmp_path):
    fixture = _fixture(tmp_path, ENCODER_LIE)
    body = _run_task(fixture, {
        "visible example": ["visible-example"],
        "randomized property": ["hidden-property"],
    })
    assert body["task_status"] == "failed"
    rows = {row.criterion: row for row in _rows(body["attempt_id"])}
    assert rows["visible example"].status == "PASS"
    assert rows["visible example"].input_seed is None
    hidden = rows["randomized property"]
    assert hidden.status == "FAIL"
    assert hidden.oracle_visibility == "held_out"
    assert hidden.input_seed and len(hidden.input_seed) == 64
    assert hidden.input_seed not in (hidden.command or "")
    _assert_sentinel_absent(body["attempt_id"], body)
    with SessionLocal() as db:
        attempt = db.get(AttemptRow, body["attempt_id"])
        diff = db.scalars(select(ArtifactRow).where(
            ArtifactRow.attempt_id == attempt.id, ArtifactRow.kind == "diff",
        )).one()
        with pytest.raises(AcceptanceError, match="visible-only pass cannot accept"):
            check_hidden_oracle_acceptance(
                [rows["visible example"]], required=True, attempt_agent=attempt.agent,
                artifact_sha256=diff.sha256,
            )


def test_honest_subject_is_accepted_on_the_held_out_and_randomized_rows(tmp_path):
    fixture = _fixture(tmp_path, HONEST)
    assumption = f"ASSUMPTION-OK-{uuid.uuid4().hex[:8]}"
    body = _run_task(fixture, {
        "visible example": ["visible-example"],
        "held-out case": ["hidden-case"],
        "randomized property": ["hidden-property"],
    }, assumption=assumption)
    assert body["task_status"] == "succeeded", body
    rows = {row.criterion: row for row in _rows(body["attempt_id"])}
    assert set(rows) == {"visible example", "held-out case", "randomized property"}
    for row in rows.values():
        assert row.status == "PASS", (row.criterion, row.stderr)
        assert row.evidence_class == "verification"
        assert row.verifier_identity == "sdf-evaluator"
    assert rows["visible example"].oracle_visibility == "visible"
    assert rows["visible example"].input_seed is None
    assert rows["held-out case"].oracle_visibility == "held_out"
    assert rows["held-out case"].input_seed is None
    prop = rows["randomized property"]
    assert prop.oracle_visibility == "held_out"
    assert prop.input_seed and len(prop.input_seed) == 64
    assert prop.oracle_digest and len(prop.oracle_digest) == 64
    _assert_sentinel_absent(body["attempt_id"], body)
    with SessionLocal() as db:
        attempt = db.get(AttemptRow, body["attempt_id"])
        diff = db.scalars(select(ArtifactRow).where(
            ArtifactRow.attempt_id == attempt.id, ArtifactRow.kind == "diff",
        )).one()
        for row in rows.values():
            if row.oracle_visibility == "held_out":
                check_verification_acceptance(row, attempt_agent=attempt.agent, artifact_sha256=diff.sha256)
        check_hidden_oracle_acceptance(
            list(rows.values()), required=True, attempt_agent=attempt.agent, artifact_sha256=diff.sha256,
        )
        validated = list(db.scalars(select(DecisionEdgeRow).where(
            DecisionEdgeRow.target_id == assumption, DecisionEdgeRow.relation == "validates",
        )))
        assert {edge.source_id for edge in validated} == {
            rows["held-out case"].id, rows["randomized property"].id,
        }
        # The seed is on the evidence API after the run, and not in the workspace.
        workspace = Path(".sdf-workspaces") / body["attempt_id"]
        tree = "\n".join(path.read_text(encoding="utf-8", errors="replace") for path in workspace.rglob("*") if path.is_file())
        assert prop.input_seed not in tree
        api_row = client.get(f"/evidence/{prop.id}").json()
        assert api_row["input_seed"] == prop.input_seed
        assert api_row["oracle_visibility"] == "held_out"
        assert SENTINEL not in json.dumps(api_row)


def test_plan_payload_does_not_include_held_out_file_bytes(tmp_path):
    fixture = _fixture(tmp_path, HONEST)
    plan_id = f"PLAN-HO-{uuid.uuid4().hex[:8]}"
    response = client.post("/plans", json={
        "id": plan_id,
        "title": "held-out stays on the server",
        "proposed_by": "planner",
        "fixture_dir": str(fixture),
        "subtasks": [{
            "key": "add",
            "title": "Fix add",
            "instructions": "Make add(a, b) return a + b. Do not edit tests.",
            "acceptance_criteria": ["visible example"],
            "criterion_checks": {"visible example": [["pytest", "-q", "tests/test_visible.py"]]},
            "allowed_paths": ["calc.py"],
            "protected_paths": ["tests/test_visible.py"],
        }],
    })
    assert response.status_code == 201, response.text
    assert SENTINEL not in response.text
    again = client.get(f"/plans/{plan_id}")
    assert again.status_code == 200
    assert SENTINEL not in again.text


# --- migration ----------------------------------------------------------------


def test_migration_0026_adds_nullable_visibility_and_seed(tmp_path, monkeypatch):
    database_url = f"sqlite:///{tmp_path / 'm0026.sqlite'}"
    monkeypatch.setenv("SDF_DATABASE_URL", database_url)
    config = Config("alembic.ini")
    engine = create_engine(database_url)
    command.upgrade(config, "0025_evidence_independence")
    with engine.begin() as connection:
        connection.execute(text(
            "INSERT INTO attempts (id, task_id, status, dispatch_key, agent, model_tier, cost_usd, created_at, "
            "evidence_mode) VALUES ('ATTEMPT-LEGACY', 'T', 'completed', 'legacy', 'old', 'basic', 0, '2026-10-01', "
            "'synthetic')"
        ))
        connection.execute(text(
            "INSERT INTO evidence (id, attempt_id, kind, status, command, confidence, evidence_mode, created_at) "
            "VALUES ('EV-LEGACY', 'ATTEMPT-LEGACY', 'evaluation', 'PASS', 'c', 1.0, 'synthetic', '2026-10-01')"
        ))
    command.upgrade(config, "head")
    columns = {column["name"] for column in inspect(engine).get_columns("evidence")}
    assert {"oracle_visibility", "input_seed"} <= columns
    with engine.connect() as connection:
        legacy = connection.execute(text(
            "SELECT oracle_visibility, input_seed FROM evidence WHERE id = 'EV-LEGACY'"
        )).one()
        assert legacy == (None, None)
    command.downgrade(config, "0025_evidence_independence")
    assert "oracle_visibility" not in {column["name"] for column in inspect(engine).get_columns("evidence")}
    command.upgrade(config, "head")
    assert "input_seed" in {column["name"] for column in inspect(engine).get_columns("evidence")}
    engine.dispose()


def test_orm_rejects_an_unknown_oracle_visibility():
    from sdf_core.db import Base, make_engine

    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        db.add(AttemptRow(
            id="ATTEMPT-VIS", task_id="T", status="completed", dispatch_key="vis-1",
            agent="native", created_at=utcnow(),
        ))
        db.add(EvidenceRow(
            id="EV-BAD-VIS", attempt_id="ATTEMPT-VIS", kind="evaluation", status="PASS",
            command="c", confidence=1.0, created_at=utcnow(), oracle_visibility="secret",
            **bound(db, "ATTEMPT-VIS"),
        ))
        with pytest.raises(ValueError, match="oracle_visibility"):
            db.commit()
