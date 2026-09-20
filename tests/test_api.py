from fastapi.testclient import TestClient
from datetime import datetime, timezone

from sdf_core.api import app, configured_containment, configured_tool_policy
from sdf_core.api import SessionLocal
from sdf_core.db import AttemptRow, RuntimeEventRow, TaskRow
from sdf_core.policy import ActionRequest


client = TestClient(app)


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


def test_omitted_mapping_keeps_legacy_command_observable(tmp_path):
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n")
    client.post("/tasks", json={"id": "TASK-LEGACY-OBS", "title": "observe", "idempotency_key": "legacy-observe"})
    result = client.post("/tasks/TASK-LEGACY-OBS/run", json={
        "fixture_dir": str(fixture), "commands": [["python", "-c", "print('legacy-observation')"]],
    })
    assert result.status_code == 200
    assert result.json()["task_status"] == "inconclusive"
    trace = client.get("/tasks/TASK-LEGACY-OBS/trace").json()
    observed = [client.get(f"/evidence/{n['id']}").json() for n in trace["nodes"] if n["kind"] == "evidence"]
    assert any("legacy-observation" in item["command"] for item in observed)


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
    response = client.post(
        "/tasks/TASK-METRIC-RUN/run",
        json={
            "fixture_dir": str(fixture),
            "commands": [],
            "provider": "fake-provider",
            "latency_ms": 123.5,
            "input_tokens": 10,
            "output_tokens": 20,
            "escalation_reason": "initial timeout",
        },
    )
    assert response.status_code == 200
    assert response.json()["provider"] == "fake-provider"
    assert response.json()["latency_ms"] == 123.5
    assert response.json()["input_tokens"] == 10
    assert response.json()["output_tokens"] == 20
    assert response.json()["escalation_reason"] == "initial timeout"


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
        assert [row.kind for row in rows] == ["tool_policy_decided", "tool_action_executed"]
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
    created = client.post(
        "/tasks",
        json={"id": "TASK-RUN-001", "title": "Run fixture", "idempotency_key": "task-run-001", "acceptance_criteria": ["command succeeds"]},
    )
    assert created.status_code == 201
    response = client.post(
        "/tasks/TASK-RUN-001/run",
        json={
            "fixture_dir": str(fixture),
            "commands": [["python", "-c", "print('ok')"]],
            "criterion_checks": {"command succeeds": [["python", "-c", "print('ok')"]]},
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
    client.post("/assumptions", json={"id": "ASSUMPTION-API-001", "title": "change is safe", "owner": "product", "source": "test"})
    client.post("/tasks", json={"id": "TASK-TRACE-001", "title": "trace", "idempotency_key": "task-trace-001", "acceptance_criteria": ["command succeeds"]})
    response = client.post("/tasks/TASK-TRACE-001/run", json={
        "fixture_dir": str(fixture),
        "commands": [["python", "-c", "print('ok')"]],
        "criterion_checks": {"command succeeds": [["python", "-c", "print('ok')"]]},
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
        json={"fixture_dir": str(fixture), "commands": [["python", "-c", "print('arbitrary pass')"]]},
    )

    assert response.status_code == 200
    assert response.json()["task_status"] == "inconclusive"
    trace = client.get("/tasks/TASK-MISSING-COVERAGE-001/trace").json()
    evidence = next(node for node in trace["nodes"] if node["kind"] == "evidence")
    assert evidence["title"] == "INCONCLUSIVE"


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
