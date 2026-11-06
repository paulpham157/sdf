from tests.auth_support import admin_client
from tests.check_support import write_checks
from datetime import datetime, timezone

from sdf_core.adapter import FakeNativeAdapter
from sdf_core.api import configured_containment, configured_tool_policy
from sdf_core.api import SessionLocal
from sdf_core.db import AttemptRow, RuntimeEventRow, TaskRow
from sdf_core.policy import ActionRequest


client = admin_client()


def test_configured_containment_requires_explicit_smoke_promotion(monkeypatch):
    monkeypatch.setenv("SDF_CONTAINMENT_BACKEND", "macos")
    monkeypatch.delenv("SDF_CONTAINMENT_SMOKE", raising=False)
    backend, required = configured_containment()
    assert backend is None
    assert required is True

    monkeypatch.setenv("SDF_CONTAINMENT_SMOKE", "passed")
    backend, required = configured_containment()
    assert backend is not None
    assert required is True

    monkeypatch.setenv("SDF_CONTAINMENT_BACKEND", "e2b")
    monkeypatch.setenv("E2B_API_KEY", "<REDACTED>")
    monkeypatch.setenv("SDF_CONTAINMENT_SMOKE", "passed")
    backend, required = configured_containment()
    assert backend is not None
    assert backend.name == "e2b-box"
    assert required is True


def test_e2b_mode_removes_network_request_from_server_allowlist(monkeypatch):
    monkeypatch.setenv("SDF_CONTAINMENT_BACKEND", "e2b")
    monkeypatch.setenv("SDF_TOOL_ALLOWLIST", "process:run,network:request")
    policy = configured_tool_policy()
    request = ActionRequest(
        attempt_id="ATTEMPT-E2B-NETWORK-POLICY",
        actor="agent:test",
        tool="network",
        action="request",
        resource="https://example.invalid",
        context={"method": "GET", "body": b""},
    )
    assert policy.decide(request).effect.value == "deny"


def test_runtime_event_endpoint_exposes_source_label():
    db = SessionLocal()
    try:
        db.add(AttemptRow(
            id="ATTEMPT-API-EVENT-001",
            task_id="TASK-API-EVENT-001",
            status="running",
            dispatch_key="dispatch-api-event-001",
            agent="test",
            created_at=datetime.now(timezone.utc),
        ))
        db.add(RuntimeEventRow(
            attempt_id="ATTEMPT-API-EVENT-001",
            session_id="SESSION-API-EVENT-001",
            sequence=1,
            source="runtime",
            kind="runtime_started",
            status="running",
            payload={},
            created_at=datetime.now(timezone.utc),
        ))
        db.commit()
    finally:
        db.close()

    response = client.get("/attempts/ATTEMPT-API-EVENT-001/runtime-events")
    assert response.status_code == 200
    assert response.json()[0]["source"] == "runtime"


def test_task_trace_includes_attempt_runtime_events():
    task_id = "TASK-API-TRACE-EVENT"
    attempt_id = "ATTEMPT-API-TRACE-EVENT"
    db = SessionLocal()
    try:
        db.add(TaskRow(
            id=task_id, title="trace", status="running",
            idempotency_key="task-api-trace-event", acceptance_criteria=[],
            created_at=datetime.now(timezone.utc),
        ))
        db.add(AttemptRow(
            id=attempt_id, task_id=task_id, status="running",
            dispatch_key="dispatch-api-trace-event", agent="test",
            created_at=datetime.now(timezone.utc),
        ))
        db.add(RuntimeEventRow(
            attempt_id=attempt_id, session_id="SESSION-API-TRACE-EVENT",
            sequence=1, source="runtime", kind="runtime_started",
            status="running", payload={}, created_at=datetime.now(timezone.utc),
        ))
        db.commit()
    finally:
        db.close()

    response = client.get(f"/tasks/{task_id}/trace")
    assert response.status_code == 200
    assert response.json()["runtime_events"][0]["attempt_id"] == attempt_id


def test_legacy_commands_field_is_rejected(tmp_path):
    """ADR 0025: callers cannot hand the evaluator commands, not even as observations."""
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n")
    client.post("/tasks", json={"id": "TASK-LEGACY-OBS", "title": "observe", "idempotency_key": "legacy-observe"})
    result = client.post("/tasks/TASK-LEGACY-OBS/run", json={
        "fixture_dir": str(fixture), "commands": [["python", "-c", "print('legacy-observation')"]],
    })
    assert result.status_code == 422
    db = SessionLocal()
    try:
        assert db.get(TaskRow, "TASK-LEGACY-OBS").status == "created"
        assert db.query(AttemptRow).filter_by(task_id="TASK-LEGACY-OBS").count() == 0
    finally:
        db.close()


def test_can_create_business_intent_chain_and_trace_it():
    context = client.post(
        "/business-contexts",
        json={"id": "BC-001", "title": "Reduce deployment lead time", "owner": "product", "source": "test"},
    )
    assert context.status_code == 201

    objective = client.post(
        "/objectives",
        json={
            "id": "OBJ-001",
            "title": "Ship a verified change in under one hour",
            "business_context_id": "BC-001",
            "owner": "product",
            "source": "test",
        },
    )
    assert objective.status_code == 201

    task = client.post(
        "/tasks",
        json={
            "id": "TASK-001",
            "title": "Add a health endpoint",
            "objective_id": "OBJ-001",
            "idempotency_key": "task-create-001",
            "acceptance_criteria": ["GET /health returns 200"],
        },
    )
    assert task.status_code == 201

    trace = client.get("/tasks/TASK-001/trace")
    assert trace.status_code == 200
    assert [node["id"] for node in trace.json()["nodes"]] == ["TASK-001", "OBJ-001", "BC-001"]
    assert trace.json()["edges"][0]["relation"] == "implements"


def test_objective_metric_and_attempt_telemetry_are_persisted(tmp_path):
    context = client.post(
        "/business-contexts",
        json={"id": "BC-METRIC-API", "title": "Metric context", "owner": "product", "source": "test"},
    )
    assert context.status_code == 201
    objective = client.post(
        "/objectives",
        json={
            "id": "OBJ-METRIC-API",
            "title": "Reduce lead time",
            "business_context_id": "BC-METRIC-API",
            "owner": "product",
            "source": "roadmap",
            "success_metric": "lead_time_minutes",
            "baseline": 60,
            "target": 30,
            "measurement_source": "prod-metrics",
            "measurement_window": "30d",
        },
    )
    assert objective.status_code == 201
    assert objective.json()["metadata"]["metric"] == {
        "success_metric": "lead_time_minutes",
        "baseline": 60.0,
        "target": 30.0,
        "source": "prod-metrics",
        "owner": "product",
        "measurement_window": "30d",
    }

    fixture = tmp_path / "fixture"
    fixture.mkdir()
    created = client.post(
        "/tasks",
        json={"id": "TASK-METRIC-RUN", "title": "telemetry", "idempotency_key": "metric-run", "acceptance_criteria": []},
    )
    assert created.status_code == 201
    response = client.post("/tasks/TASK-METRIC-RUN/run", json={"fixture_dir": str(fixture)})
    assert response.status_code == 200
    # Telemetry is server-measured (#50): the fake adapter costs nothing,
    # reports no tokens, and its latency is the wall clock SDF observed.
    body = response.json()
    assert body["provider"] == "fake"
    assert body["cost_usd"] == 0.0
    assert body["latency_ms"] is not None and body["latency_ms"] >= 0
    assert body["input_tokens"] is None and body["output_tokens"] is None
    assert body["escalation_reason"] is None


def test_task_creation_is_idempotent():
    payload = {
        "id": "TASK-IDEMPOTENT",
        "title": "Repeatable task",
        "idempotency_key": "task-idempotent-001",
        "acceptance_criteria": [],
    }
    first = client.post("/tasks", json=payload)
    second = client.post("/tasks", json={**payload, "title": "Changed title"})
    assert first.status_code == 201
    assert second.status_code == 200
    assert second.json()["id"] == first.json()["id"]
    assert second.json()["title"] == "Repeatable task"


def test_run_without_dispatch_key_gets_unique_claim_per_task_request(tmp_path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    for task_id, key in (("TASK-DISPATCH-A", "dispatch-api-a"), ("TASK-DISPATCH-B", "dispatch-api-b")):
        created = client.post(
            "/tasks",
            json={"id": task_id, "title": "dispatch", "idempotency_key": key, "acceptance_criteria": []},
        )
        assert created.status_code == 201
    responses = [
        client.post(f"/tasks/{task_id}/run", json={"fixture_dir": str(fixture), "instructions": "same"})
        for task_id in ("TASK-DISPATCH-A", "TASK-DISPATCH-B")
    ]
    assert all(response.status_code == 200 for response in responses)
    assert responses[0].json()["attempt_id"] != responses[1].json()["attempt_id"]


def test_structured_tool_endpoint_denies_by_default_without_side_effect(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    attempt_id = "ATTEMPT-API-TOOL-DENY"
    db = SessionLocal()
    try:
        db.add(TaskRow(
            id="TASK-API-TOOL-DENY", title="tool", status="running",
            idempotency_key="task-api-tool-deny", acceptance_criteria=[],
            created_at=datetime.now(timezone.utc),
        ))
        db.add(AttemptRow(
            id=attempt_id, task_id="TASK-API-TOOL-DENY", status="running",
            dispatch_key="dispatch-api-tool-deny", agent="test",
            created_at=datetime.now(timezone.utc),
        ))
        db.commit()
    finally:
        db.close()
    workspace = tmp_path / ".sdf-workspaces" / attempt_id
    workspace.mkdir(parents=True)
    (workspace / "safe.txt").write_text("before")
    monkeypatch.delenv("SDF_TOOL_ALLOWLIST", raising=False)

    response = client.post(f"/attempts/{attempt_id}/actions", json={
        "actor": "agent:test", "tool": "filesystem", "action": "write",
        "resource": "safe.txt", "context": {"data": "after"},
    })

    assert response.status_code == 200
    assert response.json()["status"] == "denied"
    assert (workspace / "safe.txt").read_text() == "before"


def test_structured_tool_endpoint_uses_server_allowlist_and_persists_event(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    attempt_id = "ATTEMPT-API-TOOL-ALLOW"
    db = SessionLocal()
    try:
        db.add(TaskRow(
            id="TASK-API-TOOL-ALLOW", title="tool", status="running",
            idempotency_key="task-api-tool-allow", acceptance_criteria=[],
            created_at=datetime.now(timezone.utc),
        ))
        db.add(AttemptRow(
            id=attempt_id, task_id="TASK-API-TOOL-ALLOW", status="running",
            dispatch_key="dispatch-api-tool-allow", agent="test",
            created_at=datetime.now(timezone.utc),
        ))
        db.commit()
    finally:
        db.close()


    workspace = tmp_path / ".sdf-workspaces" / attempt_id
    workspace.mkdir(parents=True)
    monkeypatch.setenv("SDF_TOOL_ALLOWLIST", "filesystem:write")

    response = client.post(f"/attempts/{attempt_id}/actions", json={
        "actor": "agent:test", "tool": "filesystem", "action": "write",
        "resource": "result.txt", "context": {"data": "from-api"},
    })

    assert response.status_code == 200
    assert response.json()["status"] == "executed"
    assert (workspace / "result.txt").read_text() == "from-api"
    db = SessionLocal()
    try:
        rows = db.query(RuntimeEventRow).filter_by(
            source="tool-proxy", attempt_id=attempt_id,
        ).order_by(RuntimeEventRow.sequence).all()
        assert [row.kind for row in rows] == ["tool_requested", "tool_policy_decided", "tool_action_executed"]
    finally:
        db.close()


def test_public_process_action_fails_closed_without_configured_containment(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    attempt_id = "ATTEMPT-API-PROCESS-CONTAINMENT"
    db = SessionLocal()
    try:
        db.add(TaskRow(
            id="TASK-API-PROCESS-CONTAINMENT", title="process", status="running",
            idempotency_key="task-api-process-containment", acceptance_criteria=[],
            created_at=datetime.now(timezone.utc),
        ))
        db.add(AttemptRow(
            id=attempt_id, task_id="TASK-API-PROCESS-CONTAINMENT", status="running",
            dispatch_key="dispatch-api-process-containment", agent="test",
            created_at=datetime.now(timezone.utc),
        ))
        db.commit()
    finally:
        db.close()
    workspace = tmp_path / ".sdf-workspaces" / attempt_id
    workspace.mkdir(parents=True)
    monkeypatch.setenv("SDF_TOOL_ALLOWLIST", "process:run")
    monkeypatch.delenv("SDF_CONTAINMENT_BACKEND", raising=False)
    monkeypatch.delenv("SDF_REQUIRE_CONTAINMENT", raising=False)
    outside = tmp_path / "bypass.txt"

    response = client.post(f"/attempts/{attempt_id}/actions", json={
        "actor": "agent:test", "tool": "process", "action": "run",
        "resource": ".", "context": {
            "command": ["python", "-c", f"open({str(outside)!r}, 'w').write('escape')"],
        },
    })

    assert response.status_code == 200
    assert response.json()["status"] == "failed"
    assert not outside.exists()


def test_run_endpoint_executes_fixture_and_updates_task(tmp_path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    write_checks(fixture, {"prints-ok": ["python", "-c", "print('ok')"]})
    created = client.post(
        "/tasks",
        json={"id": "TASK-RUN-001", "title": "Run fixture", "idempotency_key": "task-run-001", "acceptance_criteria": ["command succeeds"]},
    )
    assert created.status_code == 201
    response = client.post(
        "/tasks/TASK-RUN-001/run",
        json={
            "fixture_dir": str(fixture),
            "criterion_checks": {"command succeeds": ["prints-ok"]},
        },
    )
    assert response.status_code == 200
    assert response.json()["task_status"] == "succeeded"
    assert response.json()["attempt_status"] == "completed"
    trace = client.get("/tasks/TASK-RUN-001/trace")
    assert trace.status_code == 200
    trace_ids = {node["id"] for node in trace.json()["nodes"]}
    assert response.json()["attempt_id"] in trace_ids
    assert any(node_id.startswith("EVIDENCE-") for node_id in trace_ids)


def test_evidence_endpoint_and_full_trace_include_artifact_and_assumption(tmp_path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    write_checks(fixture, {"prints-ok": ["python", "-c", "print('ok')"]})
    client.post("/assumptions", json={"id": "ASSUMPTION-API-001", "title": "change is safe", "owner": "product", "source": "test"})
    client.post("/tasks", json={"id": "TASK-TRACE-001", "title": "trace", "idempotency_key": "task-trace-001", "acceptance_criteria": ["command succeeds"]})
    response = client.post("/tasks/TASK-TRACE-001/run", json={
        "fixture_dir": str(fixture),
        "criterion_checks": {"command succeeds": ["prints-ok"]},
        "validation_target_kind": "assumption", "validation_target_id": "ASSUMPTION-API-001",
    })
    assert response.status_code == 200
    trace = client.get("/tasks/TASK-TRACE-001/trace").json()
    ids = {node["id"] for node in trace["nodes"]}
    assert "ASSUMPTION-API-001" in ids
    assert any(node["kind"] == "artifact" for node in trace["nodes"])
    evidence_id = next(node["id"] for node in trace["nodes"] if node["kind"] == "evidence")
    evidence_response = client.get(f"/evidence/{evidence_id}")
    assert evidence_response.status_code == 200
    assert evidence_response.json()["criterion"] == "command succeeds"
    assert evidence_response.json()["artifact_ref"].endswith("-OUTPUT")


def test_run_with_missing_criterion_coverage_is_inconclusive(tmp_path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    created = client.post(
        "/tasks",
        json={
            "id": "TASK-MISSING-COVERAGE-001",
            "title": "Missing coverage",
            "idempotency_key": "task-missing-coverage-001",
            "acceptance_criteria": ["health check is deterministic"],
        },
    )
    assert created.status_code == 201
    response = client.post(
        "/tasks/TASK-MISSING-COVERAGE-001/run",
        json={"fixture_dir": str(fixture)},
    )

    assert response.status_code == 200
    assert response.json()["task_status"] == "inconclusive"
    trace = client.get("/tasks/TASK-MISSING-COVERAGE-001/trace").json()
    evidence = next(node for node in trace["nodes"] if node["kind"] == "evidence")
    assert evidence["title"] == "INCONCLUSIVE"


def test_run_retry_without_dispatch_key_does_not_create_second_attempt(tmp_path, monkeypatch):
    """One logical POST /run, retried with no dispatch key, runs once.

    ``RunTaskRequest.dispatch_key`` stays optional (owner decision on PR #112,
    ADR 0041). The same body for the same task derives one dispatch key.
    ADR 0031 replays that key, including after the Attempt is terminal, so a
    timeout retry does not start a second Attempt. A deliberate new Attempt
    sends an explicit key (and ``parent_attempt_id`` when it is a retry).
    """

    class CountingAdapter:
        provider = "fake"
        evidence_mode = "synthetic"

        def __init__(self):
            self.calls = 0
            self._fake = FakeNativeAdapter("app.py", "print('updated')\n")

        def run(self, *, attempt_id, workspace, instructions):
            self.calls += 1
            return self._fake.run(attempt_id=attempt_id, workspace=workspace, instructions=instructions)

    monkeypatch.chdir(tmp_path)
    adapter = CountingAdapter()
    monkeypatch.setattr("sdf_core.api.configured_run_adapter", lambda _db: adapter)
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    task_id = "TASK-RUN-RETRY-NO-KEY"
    created = client.post("/tasks", json={
        "id": task_id,
        "title": "retry without a dispatch key",
        "idempotency_key": "task-run-retry-no-key",
        "acceptance_criteria": [],
    })
    assert created.status_code == 201
    body = {"fixture_dir": str(fixture), "instructions": "complete the task"}
    first = client.post(f"/tasks/{task_id}/run", json=body)
    second = client.post(f"/tasks/{task_id}/run", json=body)
    assert first.status_code == 200, first.text

    db = SessionLocal()
    try:
        attempts = db.query(AttemptRow).filter_by(task_id=task_id).all()
        attempt_rows = [(row.id, row.status, row.dispatch_key) for row in attempts]
        parent_id = attempts[0].id if attempts else None
    finally:
        db.close()
    assert adapter.calls == 1 and len(attempt_rows) == 1, (
        "one logical POST /run without a dispatch key must invoke the adapter once and leave one Attempt; "
        f"adapter ran {adapter.calls} time(s), second response {second.status_code}: {second.text}, "
        f"attempts={attempt_rows}"
    )
    assert second.status_code == 200, second.text
    assert second.json()["attempt_id"] == parent_id
    retried = client.post(f"/tasks/{task_id}/run", json={
        **body,
        "dispatch_key": "dispatch-run-retry-explicit",
        "parent_attempt_id": parent_id,
    })
    assert retried.status_code == 200, retried.text
    assert retried.json()["attempt_id"] != attempts[0].id
    assert adapter.calls == 2


def test_derived_dispatch_key_is_stable_per_task_and_logical_request():
    """The fields ADR 0041 hashes, and the ones that must not collide."""

    from sdf_core.dispatch_key import derived_dispatch_key

    fields = dict(
        fixture_dir="/fixtures/app", instructions="same", criterion_checks={"c": ["a", "b"]},
        validation_target_kind=None, validation_target_id=None, parent_attempt_id=None, model_tier=None,
    )
    first = derived_dispatch_key(task_id="TASK-A", **fields)
    again = derived_dispatch_key(task_id="TASK-A", **fields)
    other_task = derived_dispatch_key(task_id="TASK-B", **fields)
    other_instructions = derived_dispatch_key(task_id="TASK-A", **{**fields, "instructions": "other"})
    explicit_basic = derived_dispatch_key(task_id="TASK-A", **{**fields, "model_tier": "basic"})
    reordered = derived_dispatch_key(task_id="TASK-A", **{**fields, "criterion_checks": {"c": ["b", "a"]}})
    inherit = derived_dispatch_key(task_id="TASK-A", **{**fields, "parent_attempt_id": "ATTEMPT-1", "model_tier": None})
    named_tier = derived_dispatch_key(task_id="TASK-A", **{**fields, "parent_attempt_id": "ATTEMPT-1", "model_tier": "basic"})
    other_fixture = derived_dispatch_key(task_id="TASK-A", **{**fields, "fixture_dir": "/fixtures/other"})
    half_target = derived_dispatch_key(
        task_id="TASK-A", **{**fields, "validation_target_kind": "hypothesis", "validation_target_id": None},
    )
    both_target = derived_dispatch_key(
        task_id="TASK-A", **{**fields, "validation_target_kind": "hypothesis", "validation_target_id": "H1"},
    )
    keyed = derived_dispatch_key(
        task_id="TASK-A", **{**fields, "criterion_checks": {"d": ["b"], "c": ["a"]}},
    )
    keyed_reordered = derived_dispatch_key(
        task_id="TASK-A", **{**fields, "criterion_checks": {"c": ["a"], "d": ["b"]}},
    )
    assert first == again == explicit_basic == half_target
    assert keyed == keyed_reordered
    assert first.startswith("keyless-") and len(first) <= 200
    assert len({first, other_task, other_instructions, reordered, inherit, named_tier, other_fixture, both_target, keyed}) == 9


def test_concurrent_keyless_runs_create_one_attempt(tmp_path):
    """Two overlapping runs of one derived key leave one Attempt and one adapter call."""

    import threading
    from concurrent.futures import ThreadPoolExecutor

    from sqlalchemy import create_engine, event
    from sqlalchemy.orm import sessionmaker

    from sdf_core.adapter import FakeNativeAdapter
    from sdf_core.db import Base, TaskRow
    from sdf_core.dispatch_key import derived_dispatch_key
    from sdf_core.execution import ExecutionService
    from sdf_core.model import utcnow

    engine = create_engine(f"sqlite:///{tmp_path / 'race.sqlite'}", connect_args={"timeout": 30})

    @event.listens_for(engine, "connect")
    def _pragmas(connection, _record):
        connection.execute("PRAGMA journal_mode=WAL")
        connection.execute("PRAGMA busy_timeout=30000")

    Base.metadata.create_all(engine)
    factory = sessionmaker(engine, expire_on_commit=False)
    task_id = "TASK-KEYLESS-RACE"
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    key = derived_dispatch_key(
        task_id=task_id, fixture_dir=str(fixture), instructions="complete the task",
        criterion_checks=None, validation_target_kind=None, validation_target_id=None,
        parent_attempt_id=None, model_tier=None,
    )
    with factory() as db:
        db.add(TaskRow(
            id=task_id, title="race", status="created", idempotency_key="task-keyless-race",
            acceptance_criteria=[], created_at=utcnow(),
        ))
        db.commit()

    class Adapter:
        provider = "fake"
        evidence_mode = "synthetic"

        def __init__(self):
            self.calls = 0
            self._lock = threading.Lock()
            self._fake = FakeNativeAdapter("app.py", "print('updated')\n")

        def run(self, *, attempt_id, workspace, instructions):
            with self._lock:
                self.calls += 1
            return self._fake.run(attempt_id=attempt_id, workspace=workspace, instructions=instructions)

    adapter = Adapter()
    barrier = threading.Barrier(2)

    def once():
        barrier.wait(timeout=10)
        db = factory()
        try:
            service = ExecutionService(
                db, workspace_root=tmp_path / ".sdf-workspaces", artifact_root=tmp_path / ".sdf-artifacts",
                adapter=adapter,
            )
            return service.run(
                task_id=task_id, dispatch_key=key, fixture=fixture, instructions="complete the task", commands=[],
            ).id
        finally:
            db.close()

    with ThreadPoolExecutor(max_workers=2) as pool:
        ids = list(pool.map(lambda _index: once(), (0, 1)))
    assert ids[0] == ids[1]
    assert adapter.calls == 1
    with factory() as db:
        assert db.query(AttemptRow).filter_by(task_id=task_id).count() == 1


def test_scorecard_endpoint_exposes_local_measurement_contract():
    response = client.get("/scorecard")

    assert response.status_code == 200
    assert set(response.json()) == {
        "first_attempt_success_rate",
        "accepted_task_rate",
        "evidence_coverage",
        "trace_completeness",
        "time_to_accepted_evidence_seconds",
        "escalation_rate",
        "cost_per_accepted_task",
        "escalation_exhaustion_rate",
    }
