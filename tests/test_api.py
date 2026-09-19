from fastapi.testclient import TestClient

from sdf_core.api import app


client = TestClient(app)


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
