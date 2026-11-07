"""ADR 0044: human read model and approver inbox. Local SQLite, no live provider."""

from __future__ import annotations

import logging
import uuid
from datetime import datetime, timedelta, timezone

import pytest
from fastapi.routing import APIRoute
from sqlalchemy import func, select

from sdf_core import auth
from sdf_core.api import SessionLocal, app
from sdf_core.db import (
    AgentMessageRow,
    AgentRunRow,
    AttemptRow,
    AuthAuditRow,
    DecisionRequestRow,
    EvidenceRow,
    GraphNodeRow,
    PlanRow,
    TaskRow,
)
from sdf_core.observability import redact
from sdf_core.plans import PlanSpec, propose_plan
from tests.agent_comm_support import make_fixture, plan_dict
from tests.auth_support import client_as
from tests.check_support import write_checks

TASK_FIELDS = {"id", "title", "status", "acceptance_criteria", "created_at"}
TASK_DETAIL_FIELDS = TASK_FIELDS | {"attempt_ids"}
ATTEMPT_FIELDS = {
    "id", "task_id", "status", "agent", "parent_attempt_id", "model_tier", "cost_usd", "provider",
    "latency_ms", "input_tokens", "output_tokens", "escalation_reason", "created_at", "model_profile",
    "outcome", "evidence_mode", "error_class", "trace_id", "principal",
}
PLAN_FIELDS = {
    "id", "title", "status", "objective_id", "proposed_by", "content_sha256", "created_at",
    "decision_request_id",
}
OBJECTIVE_FIELDS = {"id", "title", "owner", "source", "created_at"}
INBOX_FIELDS = {
    "id", "subject_kind", "subject_id", "run_id", "question", "options", "status", "requested_by",
    "resolved_by", "resolver_kind", "resolution", "decision_node_id", "created_at", "resolved_at",
    "material", "materiality_source", "matched_rules", "actionable",
}
HIDDEN = ("idempotency_key", "dispatch_key", "executor_id", "executor_heartbeat_at")
NEW_PATHS = (
    "/tasks",
    "/tasks/{task_id}",
    "/attempts",
    "/attempts/{attempt_id}",
    "/plans",
    "/objectives",
    "/inbox",
)


def _routes():
    return {(method, route.path) for route in app.routes if isinstance(route, APIRoute)
            for method in route.methods if method != "HEAD"}


def _aware(stamp: str) -> datetime:
    parsed = datetime.fromisoformat(stamp)
    if parsed.tzinfo is None:
        return parsed.replace(tzinfo=timezone.utc)
    return parsed.astimezone(timezone.utc)


def _ordered(items: list[dict]) -> bool:
    keys = [(_aware(item["created_at"]), item["id"]) for item in items]
    return keys == sorted(keys)


def _pages(client, path: str, *, limit: int, extra: dict | None = None) -> list[dict]:
    items: list[dict] = []
    cursor = None
    seen: set[str] = set()
    for _ in range(10_000):
        params = {"limit": limit, **(extra or {})}
        if cursor is not None:
            params["cursor"] = cursor
        response = client.get(path, params=params)
        assert response.status_code == 200, (path, response.status_code, response.text)
        body = response.json()
        assert set(body) == {"items", "next_cursor"}
        assert isinstance(body["items"], list)
        assert len(body["items"]) <= limit
        items.extend(body["items"])
        cursor = body["next_cursor"]
        if cursor is None:
            return items
        assert cursor not in seen
        seen.add(cursor)
    raise AssertionError(f"{path} cursor did not end")


def _human_reads(caplog):
    return [record for record in caplog.records if record.getMessage() == "human_read"]


@pytest.fixture
def read_log(caplog):
    # Alembic's fileConfig disables loggers that already existed. A later test
    # still has to see the human_read record (ADR 0044).
    logger = logging.getLogger("sdf.api")
    previous = logger.level
    touched = []
    current = logger
    while current is not None:
        touched.append((current, current.disabled))
        current.disabled = False
        current = current.parent
    logger.setLevel(logging.INFO)
    logger.addHandler(caplog.handler)
    caplog.set_level(logging.INFO, logger="sdf.api")
    try:
        yield caplog
    finally:
        logger.removeHandler(caplog.handler)
        logger.setLevel(previous)
        for item, was_disabled in touched:
            item.disabled = was_disabled


def test_collections_are_viewer_gets_and_stay_off_the_public_set():
    routes = _routes()
    for path in NEW_PATHS:
        assert ("GET", path) in routes
        assert ("GET", path) not in auth.PUBLIC_ROUTES
        assert auth.required_role("GET", path) == "viewer"


def test_collections_paginate_in_created_at_then_id_order(read_log):
    suffix = uuid.uuid4().hex[:10]
    early = datetime(2020, 1, 1, tzinfo=timezone.utc)
    tie = datetime(2020, 1, 2, 3, 4, 5, tzinfo=timezone.utc)
    ids = {
        "tasks": (f"0-task-{suffix}", f"a-task-{suffix}", f"b-task-{suffix}"),
        "attempts": (f"0-attempt-{suffix}", f"a-attempt-{suffix}", f"b-attempt-{suffix}"),
        "plans": (f"0-plan-{suffix}", f"a-plan-{suffix}", f"b-plan-{suffix}"),
        "objectives": (f"0-obj-{suffix}", f"a-obj-{suffix}", f"b-obj-{suffix}"),
    }
    with SessionLocal() as db:
        for index, (created_at, task_id) in enumerate((
            (early, ids["tasks"][0]), (tie, ids["tasks"][1]), (tie, ids["tasks"][2]),
        )):
            db.add(TaskRow(
                id=task_id, title=f"page {index}", status="created", idempotency_key=f"page-task-{task_id}",
                acceptance_criteria=["listed"], created_at=created_at,
            ))
        for offset in range(51):
            task_id = f"task-fill-{suffix}-{offset:02d}"
            db.add(TaskRow(
                id=task_id, title="fill", status="created", idempotency_key=f"fill-{task_id}",
                acceptance_criteria=["listed"], created_at=datetime(2021, 1, 1, tzinfo=timezone.utc) + timedelta(seconds=offset),
            ))
        for created_at, attempt_id in (
            (early, ids["attempts"][0]), (tie, ids["attempts"][1]), (tie, ids["attempts"][2]),
        ):
            db.add(AttemptRow(
                id=attempt_id, task_id=ids["tasks"][0], status="completed", dispatch_key=f"page-{attempt_id}",
                agent="native", created_at=created_at,
            ))
        for offset in range(51):
            attempt_id = f"attempt-fill-{suffix}-{offset:02d}"
            db.add(AttemptRow(
                id=attempt_id, task_id=ids["tasks"][0], status="completed", dispatch_key=f"fill-{attempt_id}",
                agent="native", created_at=datetime(2021, 2, 1, tzinfo=timezone.utc) + timedelta(seconds=offset),
            ))
        for created_at, plan_id in (
            (early, ids["plans"][0]), (tie, ids["plans"][1]), (tie, ids["plans"][2]),
        ):
            db.add(PlanRow(
                id=plan_id, title=f"page {plan_id}", status="proposed", objective_id=None, content={"subtasks": []},
                content_sha256="a" * 64, proposed_by="pager", created_at=created_at,
                decision_request_id=f"DREQ-{plan_id}",
            ))
        for offset in range(51):
            plan_id = f"plan-fill-{suffix}-{offset:02d}"
            db.add(PlanRow(
                id=plan_id, title="fill", status="proposed", content={"subtasks": []}, content_sha256="b" * 64,
                proposed_by="pager", created_at=datetime(2021, 3, 1, tzinfo=timezone.utc) + timedelta(seconds=offset),
                decision_request_id=f"DREQ-{plan_id}",
            ))
        for created_at, node_id in (
            (early, ids["objectives"][0]), (tie, ids["objectives"][1]), (tie, ids["objectives"][2]),
        ):
            db.add(GraphNodeRow(
                id=node_id, kind="objective", title=f"page {node_id}", source="test", owner="product",
                confidence=1.0, created_at=created_at,
            ))
        for offset in range(51):
            node_id = f"obj-fill-{suffix}-{offset:02d}"
            db.add(GraphNodeRow(
                id=node_id, kind="objective", title="fill", source="test", owner="product", confidence=1.0,
                created_at=datetime(2021, 4, 1, tzinfo=timezone.utc) + timedelta(seconds=offset),
            ))
        db.add(GraphNodeRow(
            id=f"not-objective-{suffix}", kind="assumption", title="not listed", source="test", owner="product",
            confidence=1.0, created_at=early,
        ))
        db.commit()

    viewer = client_as("viewer")
    expected = {
        "/tasks": (ids["tasks"], TASK_FIELDS),
        "/attempts": (ids["attempts"], ATTEMPT_FIELDS),
        "/plans": (ids["plans"], PLAN_FIELDS),
        "/objectives": (ids["objectives"], OBJECTIVE_FIELDS),
    }
    for path, (ordered_ids, fields) in expected.items():
        read_log.clear()
        first = viewer.get(path, params={"limit": 1})
        assert first.status_code == 200, first.text
        records = _human_reads(read_log)
        assert records and records[-1].route == path
        assert records[-1].items == len(first.json()["items"])
        assert records[-1].cursor_present is False
        assert not hasattr(records[-1], "actionable_count")
        walked = _pages(viewer, path, limit=1)
        assert _ordered(walked)
        assert len({item["id"] for item in walked}) == len(walked)
        found = [item["id"] for item in walked]
        assert found.index(ordered_ids[0]) < found.index(ordered_ids[1]) < found.index(ordered_ids[2])
        for item in walked:
            assert set(item) == fields
            for hidden in HIDDEN:
                assert hidden not in item
        page = viewer.get(path)
        assert page.status_code == 200, page.text
        body = page.json()
        assert len(body["items"]) == 50
        assert body["next_cursor"]
        read_log.clear()
        again = viewer.get(path, params={"limit": 1, "cursor": body["next_cursor"]})
        repeated = viewer.get(path, params={"limit": 1, "cursor": body["next_cursor"]})
        assert again.status_code == 200 and again.json() == repeated.json()
        assert _human_reads(read_log)[-1].cursor_present is True
        assert viewer.get(path, params={"cursor": "not-a-cursor"}).status_code == 422
        assert viewer.get(path, params={"limit": 101}).status_code == 422
        assert viewer.get(path, params={"limit": 100}).status_code == 200
    objectives = [item["id"] for item in _pages(viewer, "/objectives", limit=100)]
    assert f"not-objective-{suffix}" not in objectives

    from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter

    from sdf_core import tracing

    exporter = InMemorySpanExporter()
    assert tracing.configure_tracing({"SDF_OTEL_ENABLED": "1"}, exporter=exporter)
    try:
        assert tracing.instrument_fastapi(app)
        listed = viewer.get("/tasks", params={"limit": 1})
        inbox = viewer.get("/inbox")
        assert listed.status_code == 200 and inbox.status_code == 200
        spans = [span for span in exporter.get_finished_spans() if (span.attributes or {}).get("sdf.route")]
        by_route = {span.attributes["sdf.route"]: span.attributes for span in spans}
        assert by_route["/tasks"]["sdf.items"] == len(listed.json()["items"])
        assert by_route["/tasks"]["sdf.cursor_present"] is False
        assert "sdf.actionable_count" not in by_route["/tasks"]
        assert by_route["/inbox"]["sdf.items"] == 0
        assert by_route["/inbox"]["sdf.actionable_count"] == 0
        assert by_route["/inbox"]["sdf.cursor_present"] is False
    finally:
        tracing.uninstrument_fastapi(app)
        tracing.shutdown()


def test_plan_status_filter_is_the_plan_status_set():
    suffix = uuid.uuid4().hex[:10]
    now = datetime(2022, 5, 1, tzinfo=timezone.utc)
    made = {}
    with SessionLocal() as db:
        for index, plan_status in enumerate(("proposed", "approved", "rejected")):
            plan_id = f"PLAN-STATUS-{plan_status}-{suffix}"
            made[plan_status] = plan_id
            db.add(PlanRow(
                id=plan_id, title=plan_status, status=plan_status, content={"subtasks": []},
                content_sha256="c" * 64, proposed_by="pager", created_at=now + timedelta(seconds=index),
                decision_request_id=f"DREQ-STATUS-{plan_id}",
            ))
        db.commit()
    viewer = client_as("viewer")
    everyone = {item["id"]: item["status"] for item in _pages(viewer, "/plans", limit=100)}
    for plan_status, plan_id in made.items():
        assert everyone[plan_id] == plan_status
        filtered = _pages(viewer, "/plans", limit=100, extra={"status": plan_status})
        assert filtered
        assert {item["status"] for item in filtered} == {plan_status}
        assert plan_id in {item["id"] for item in filtered}
    assert viewer.get("/plans", params={"status": "pending"}).status_code == 422
    assert viewer.get("/plans", params={"status": ""}).status_code == 422


def test_task_and_attempt_detail_round_trip(tmp_path):
    principal = f"runner-{uuid.uuid4().hex[:8]}"
    operator = client_as("operator", principal=principal)
    viewer = client_as("viewer")
    task_id = f"TASK-READ-{uuid.uuid4().hex[:8]}"
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    write_checks(fixture, {"prints-ok": ["python", "-c", "print('ok')"]})
    created = operator.post("/tasks", json={
        "id": task_id, "title": "Read me", "idempotency_key": task_id,
        "acceptance_criteria": ["command succeeds"],
    })
    assert created.status_code == 201, created.text
    run = operator.post(f"/tasks/{task_id}/run", json={
        "fixture_dir": str(fixture),
        "criterion_checks": {"command succeeds": ["prints-ok"]},
    })
    assert run.status_code == 200, run.text
    attempt_id = run.json()["attempt_id"]
    earlier_id = f"ATTEMPT-EARLIER-{uuid.uuid4().hex[:8]}"
    with SessionLocal() as db:
        db.add(AttemptRow(
            id=earlier_id, task_id=task_id, status="completed", dispatch_key=f"hidden-dispatch-{earlier_id}",
            agent="native", created_at=datetime(2019, 1, 1, tzinfo=timezone.utc), principal=principal,
            executor_id="exec-should-hide", executor_heartbeat_at=datetime(2019, 1, 2, tzinfo=timezone.utc),
        ))
        db.commit()

    detail = viewer.get(f"/tasks/{task_id}")
    assert detail.status_code == 200, detail.text
    body = detail.json()
    assert set(body) == TASK_DETAIL_FIELDS
    assert body["id"] == task_id
    assert body["title"] == "Read me"
    assert body["acceptance_criteria"] == ["command succeeds"]
    attempts = _pages(viewer, "/attempts", limit=100)
    attempt_order = [item["id"] for item in attempts if item["task_id"] == task_id]
    assert body["attempt_ids"] == attempt_order == [earlier_id, attempt_id]
    for hidden in HIDDEN:
        assert hidden not in body
    assert "exec-should-hide" not in detail.text

    earlier = viewer.get(f"/attempts/{earlier_id}")
    assert earlier.status_code == 200, earlier.text
    assert set(earlier.json()) == ATTEMPT_FIELDS
    assert "exec-should-hide" not in earlier.text
    assert "hidden-dispatch" not in earlier.text
    for hidden in HIDDEN:
        assert hidden not in earlier.json()

    current = viewer.get(f"/attempts/{attempt_id}")
    assert current.status_code == 200, current.text
    attempt = current.json()
    assert set(attempt) == ATTEMPT_FIELDS
    assert attempt["task_id"] == task_id
    assert attempt["principal"] == principal
    assert attempt["id"] == attempt_id
    listed = next(item for item in attempts if item["id"] == attempt_id)
    assert set(listed) == ATTEMPT_FIELDS
    timeline = viewer.get(f"/runs/{attempt_id}/timeline")
    assert timeline.status_code == 200, timeline.text
    audits = [
        entry for entry in timeline.json()["entries"]
        if entry["source"] == "auth_audit" and entry.get("route") == "/tasks/{task_id}/run"
    ]
    assert audits
    assert attempt["principal"] == audits[-1]["principal"] == principal
    assert viewer.get("/tasks/missing-task-xyz").status_code == 404
    assert viewer.get("/attempts/missing-attempt-xyz").status_code == 404


def test_read_payloads_do_not_expose_held_out_oracle_output(tmp_path):
    suffix = uuid.uuid4().hex
    fixture_bytes = f"HELD-OUT-BYTES-{suffix}"
    withheld_stdout = f"WITHHELD-STDOUT-{suffix}"
    withheld_stderr = f"WITHHELD-STDERR-{suffix[:12]}"
    hidden_name = f"hidden-oracle-{suffix[:12]}"
    held = tmp_path / ".sdf-held-out"
    held.mkdir()
    (held / f"{hidden_name}.py").write_text(fixture_bytes, encoding="utf-8")
    task_id = f"TASK-HELD-{suffix[:10]}"
    attempt_id = f"ATTEMPT-HELD-{suffix[:10]}"
    plan_id = f"PLAN-HELD-{suffix[:10]}"
    objective_id = f"OBJ-HELD-{suffix[:10]}"
    request_id = f"DREQ-HELD-{suffix[:10]}"
    now = datetime.now(timezone.utc)
    with SessionLocal() as db:
        db.add(TaskRow(
            id=task_id, title="visible task", status="created", idempotency_key=fixture_bytes,
            acceptance_criteria=["visible criterion"], created_at=now,
        ))
        db.add(AttemptRow(
            id=attempt_id, task_id=task_id, status="completed", dispatch_key=withheld_stdout,
            agent="native", created_at=now, executor_id=withheld_stderr,
            executor_heartbeat_at=now,
        ))
        db.add(PlanRow(
            id=plan_id, title="visible plan", status="proposed", objective_id=objective_id,
            content={"oracle": fixture_bytes, "stdout": withheld_stdout, "stderr": withheld_stderr,
                     "path": f".sdf-held-out/{hidden_name}.py"},
            content_sha256="d" * 64, proposed_by="someone", created_at=now, decision_request_id=request_id,
        ))
        db.add(DecisionRequestRow(
            id=request_id, subject_kind="plan", subject_id=plan_id, question="Approve the visible plan?",
            options=["approve", "reject"], status="pending", requested_by="someone", created_at=now,
        ))
        db.add(GraphNodeRow(
            id=objective_id, kind="objective", title="visible objective", source="test", owner="product",
            confidence=1.0, created_at=now,
            metadata_json={"fixture": fixture_bytes, "stdout": withheld_stdout, "stderr": withheld_stderr},
        ))
        db.commit()

    viewer = client_as("viewer", "approver", principal=f"reader-{suffix[:8]}")
    blobs = [
        _pages_text(viewer, "/tasks"),
        viewer.get(f"/tasks/{task_id}").text,
        _pages_text(viewer, "/attempts"),
        viewer.get(f"/attempts/{attempt_id}").text,
        _pages_text(viewer, "/plans"),
        _pages_text(viewer, "/objectives"),
        _pages_text(viewer, "/inbox"),
    ]
    combined = "\n".join(blobs)
    for secret in (fixture_bytes, withheld_stdout, withheld_stderr, hidden_name, ".sdf-held-out/"):
        assert secret not in combined
    attempt = viewer.get(f"/attempts/{attempt_id}").json()
    task = viewer.get(f"/tasks/{task_id}").json()
    for hidden in HIDDEN:
        assert hidden not in attempt and hidden not in task


def _pages_text(client, path: str) -> str:
    chunks = []
    cursor = None
    for _ in range(10_000):
        params = {"limit": 100}
        if cursor is not None:
            params["cursor"] = cursor
        response = client.get(path, params=params)
        assert response.status_code == 200, response.text
        chunks.append(response.text)
        cursor = response.json()["next_cursor"]
        if cursor is None:
            return "\n".join(chunks)
    raise AssertionError(path)


def test_inbox_hides_unactionable_requests(tmp_path, monkeypatch, read_log):
    proposer_name = "Ada.Lee"
    proposer = client_as("operator", "approver", principal=proposer_name)
    other = client_as("approver", principal="Bob.Approver")
    viewer = client_as("viewer", principal="Vera.Viewer")
    operator = client_as("operator", principal="Omar.Operator")
    service = client_as("operator", principal="ci-inbox", kind="service")
    assert proposer.get("/auth/whoami").json()["can_approve"] is True
    assert viewer.get("/auth/whoami").json()["can_approve"] is False
    assert service.get("/auth/whoami").json()["can_approve"] is False

    plan_id, plan_body = _propose(proposer, tmp_path / "main")
    request_id = plan_body["decision_request"]["id"]
    agent_id = f"DREQ-AGENT-{uuid.uuid4().hex[:8]}"
    folded_id = f"PLAN-FOLD-{uuid.uuid4().hex[:8]}"
    spec = PlanSpec(**plan_dict(make_fixture(tmp_path / "fold"), folded_id)).model_copy(update={"proposed_by": "Case.Fold"})
    with SessionLocal() as db:
        db.add(DecisionRequestRow(
            id=agent_id, subject_kind="agent_request", subject_id=f"msg-{agent_id}",
            question="Which library?", options=["left", "right"], status="pending",
            requested_by=proposer_name, created_at=datetime.now(timezone.utc),
        ))
        propose_plan(db, spec)
        folded = db.get(PlanRow, folded_id)
        folded_request = folded.decision_request_id
        db.commit()

    for client in (viewer, operator, service):
        read_log.clear()
        response = client.get("/inbox")
        assert response.status_code == 200, response.text
        assert response.json() == {"items": [], "next_cursor": None}
        record = _human_reads(read_log)[-1]
        assert record.route == "/inbox" and record.items == 0 and record.actionable_count == 0
        assert record.cursor_present is False

    proposer_items = _pages(proposer, "/inbox", limit=100)
    proposer_ids = {item["id"] for item in proposer_items}
    assert request_id not in proposer_ids
    assert folded_request in proposer_ids
    assert agent_id in proposer_ids
    agent_item = next(item for item in proposer_items if item["id"] == agent_id)
    assert set(agent_item) == INBOX_FIELDS
    assert agent_item["actionable"] is True
    assert "proposed_by" not in agent_item

    other_items = _pages(other, "/inbox", limit=100)
    other_ids = {item["id"] for item in other_items}
    assert request_id in other_ids and folded_request in other_ids and agent_id in other_ids
    plan_item = next(item for item in other_items if item["id"] == request_id)
    assert set(plan_item) == INBOX_FIELDS | {"proposed_by", "content_sha256"}
    assert plan_item["proposed_by"] == proposer_name
    assert plan_item["content_sha256"] == plan_body["content_sha256"]
    assert plan_item["status"] == "pending" and plan_item["actionable"] is True
    assert plan_item["material"] is False
    assert "consequence" not in plan_item
    folded_caller = client_as("approver", principal="case.fold")
    assert folded_request not in {item["id"] for item in _pages(folded_caller, "/inbox", limit=100)}
    same_case = client_as("approver", principal="Case.Fold")
    assert folded_request not in {item["id"] for item in _pages(same_case, "/inbox", limit=100)}

    requests = viewer.get("/decision-requests")
    assert requests.status_code == 200
    body = requests.json()
    assert isinstance(body, list)
    assert any(item["id"] == request_id and item["status"] == "pending" for item in body)

    monkeypatch.setenv("SDF_AUTH_SINGLE_USER", "1")
    assert request_id in {item["id"] for item in _pages(proposer, "/inbox", limit=100)}
    assert folded_request in {item["id"] for item in _pages(folded_caller, "/inbox", limit=100)}
    monkeypatch.setenv("SDF_AUTH_SINGLE_USER", "")
    assert request_id not in {item["id"] for item in _pages(proposer, "/inbox", limit=100)}

    rejected = proposer.post(f"/plans/{plan_id}/reject", json={"rationale": "changed my mind"})
    assert rejected.status_code == 200, rejected.text
    assert request_id not in {item["id"] for item in _pages(other, "/inbox", limit=100)}
    read_log.clear()
    page = other.get("/inbox", params={"limit": 1})
    assert page.status_code == 200
    record = _human_reads(read_log)[-1]
    assert record.route == "/inbox"
    assert record.items == len(page.json()["items"]) == record.actionable_count
    assert all(item["actionable"] is True for item in page.json()["items"])


def _propose(client, tmp_path):
    plan_id = f"PLAN-INBOX-{uuid.uuid4().hex[:8]}"
    response = client.post("/plans", json=plan_dict(make_fixture(tmp_path), plan_id))
    assert response.status_code == 201, response.text
    return plan_id, response.json()


def test_reads_do_not_insert_domain_rows():
    suffix = uuid.uuid4().hex[:8]
    task_id = f"TASK-COUNT-{suffix}"
    attempt_id = f"ATTEMPT-COUNT-{suffix}"
    now = datetime.now(timezone.utc)
    with SessionLocal() as db:
        db.add(TaskRow(
            id=task_id, title="counted", status="created", idempotency_key=task_id,
            acceptance_criteria=["counted"], created_at=now,
        ))
        db.add(AttemptRow(
            id=attempt_id, task_id=task_id, status="completed", dispatch_key=attempt_id,
            agent="native", created_at=now,
        ))
        db.commit()
    viewer = client_as("viewer")
    before = _counts()
    mutations = _mutation_count()
    paths = [
        "/tasks",
        f"/tasks/{task_id}",
        "/attempts",
        f"/attempts/{attempt_id}",
        "/plans",
        "/objectives",
        "/inbox",
    ]
    for path in paths:
        response = viewer.get(path)
        assert response.status_code == 200, (path, response.text)
        assert _counts() == before, path
        assert _mutation_count() == mutations, path
    first = viewer.get("/tasks", params={"limit": 1})
    assert first.status_code == 200
    cursor = first.json()["next_cursor"]
    if cursor:
        second = viewer.get("/tasks", params={"limit": 1, "cursor": cursor})
        assert second.status_code == 200
    assert _counts() == before
    assert _mutation_count() == mutations


def _counts() -> dict[str, int]:
    models = {
        "tasks": TaskRow,
        "attempts": AttemptRow,
        "plans": PlanRow,
        "decision_requests": DecisionRequestRow,
        "evidence": EvidenceRow,
        "agent_messages": AgentMessageRow,
        "auth_audit": AuthAuditRow,
    }
    with SessionLocal() as db:
        return {name: db.scalar(select(func.count()).select_from(model)) for name, model in models.items()}


def _mutation_count() -> int:
    with SessionLocal() as db:
        return db.scalar(select(func.count()).select_from(AuthAuditRow).where(AuthAuditRow.event == "mutation"))


def test_read_payloads_redact_like_the_timeline(tmp_path):
    secret = "Bearer " + ("a" * 24)
    masked = redact(secret)
    body = secret + ("Y" * 500)
    proposer = client_as("operator", "approver", principal=f"prop-{uuid.uuid4().hex[:6]}")
    approver = client_as("approver", principal=f"appr-{uuid.uuid4().hex[:6]}")
    viewer = client_as("viewer")
    plan_id, plan_body = _propose(proposer, tmp_path)
    approved = approver.post(f"/plans/{plan_id}/approve", json={"expected_sha256": plan_body["content_sha256"]})
    assert approved.status_code == 200, approved.text
    task_id = f"TASK-REDACT-{uuid.uuid4().hex[:8]}"
    created = client_as("operator").post("/tasks", json={
        "id": task_id, "title": secret, "idempotency_key": task_id, "acceptance_criteria": ["stays masked"],
    })
    assert created.status_code == 201, created.text
    run_id = f"RUN-REDACT-{uuid.uuid4().hex[:8]}"
    now = datetime.now(timezone.utc)
    with SessionLocal() as db:
        db.add(AgentRunRow(
            id=run_id, plan_id=plan_id, status="running", config={}, started_at=now, started_by="redact-test",
        ))
        db.add(AgentMessageRow(
            id=f"MSG-{run_id}", run_id=run_id, seq=1, envelope_id=uuid.uuid4().hex,
            from_agent="lead-1", from_role="lead", kind="message", body=body,
            body_sha256="ab" * 32, created_at=now,
        ))
        db.commit()

    timeline = viewer.get(f"/runs/{run_id}/timeline")
    assert timeline.status_code == 200, timeline.text
    entry = next(item for item in timeline.json()["entries"] if item["source"] == "message")
    assert len(entry["body"]) <= 400
    assert masked in entry["body"]
    assert secret not in entry["body"]
    assert "Y" * 374 not in entry["body"]
    listed = next(item for item in _pages(viewer, "/tasks", limit=100) if item["id"] == task_id)
    detail = viewer.get(f"/tasks/{task_id}").json()
    assert listed["title"] == detail["title"] == masked
    assert masked in entry["body"]
    messages = viewer.get(f"/runs/{run_id}/messages")
    assert messages.status_code == 200, messages.text
    assert messages.json()[0]["body"] == body
    assert len(messages.json()[0]["body"]) > 400

    blobs = [
        _pages_text(viewer, "/tasks"),
        _pages_text(viewer, "/attempts"),
        _pages_text(viewer, "/plans"),
        _pages_text(viewer, "/objectives"),
        _pages_text(viewer, "/inbox"),
        viewer.get(f"/tasks/{task_id}").text,
    ]
    combined = "\n".join(blobs)
    assert secret not in combined
    assert "Y" * 374 not in combined
    assert body not in combined
