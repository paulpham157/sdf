from datetime import datetime, timedelta, timezone

from fastapi.testclient import TestClient

from sdf_core.api import app
from sdf_core.impact import V0_METRICS


client = TestClient(app)


def make_objective(objective_id: str, business_context_id: str) -> None:
    client.post(
        "/business-contexts",
        json={"id": business_context_id, "title": "context", "owner": "product", "source": "test"},
    )
    response = client.post(
        "/objectives",
        json={
            "id": objective_id,
            "title": "objective",
            "business_context_id": business_context_id,
            "owner": "product",
            "source": "test",
        },
    )
    assert response.status_code == 201


def test_declare_metric_on_real_objective():
    make_objective("OBJ-METRIC-001", "BC-METRIC-001")
    response = client.post(
        "/objectives/OBJ-METRIC-001/metric",
        json={
            "metric_name": "incident_minutes",
            "baseline": 120.0,
            "target": 60.0,
            "unit": "minutes",
            "source": "pagerduty",
            "owner": "sre",
            "measurement_window_days": 30,
        },
    )
    assert response.status_code == 201
    body = response.json()
    assert body["objective_id"] == "OBJ-METRIC-001"
    assert body["baseline"] == 120.0
    assert body["target"] == 60.0
    assert body["declared_at"]


def test_redeclaring_metric_on_same_objective_conflicts():
    make_objective("OBJ-METRIC-002", "BC-METRIC-002")
    payload = {
        "metric_name": "incident_minutes",
        "baseline": 120.0,
        "target": 60.0,
        "unit": "minutes",
        "source": "pagerduty",
        "owner": "sre",
        "measurement_window_days": 30,
    }
    first = client.post("/objectives/OBJ-METRIC-002/metric", json=payload)
    second = client.post("/objectives/OBJ-METRIC-002/metric", json=payload)
    assert first.status_code == 201
    assert second.status_code == 409


def test_declaration_with_target_equal_to_baseline_is_unprocessable():
    make_objective("OBJ-METRIC-003", "BC-METRIC-003")
    response = client.post(
        "/objectives/OBJ-METRIC-003/metric",
        json={
            "metric_name": "incident_minutes",
            "baseline": 120.0,
            "target": 120.0,
            "unit": "minutes",
            "source": "pagerduty",
            "owner": "sre",
            "measurement_window_days": 30,
        },
    )
    assert response.status_code == 422
    assert "target must differ from baseline" in response.json()["detail"]


def test_missing_objective_404s_on_all_impact_endpoints():
    assert client.post(
        "/objectives/OBJ-DOES-NOT-EXIST/metric",
        json={
            "metric_name": "x", "baseline": 1.0, "target": 2.0, "unit": "u",
            "source": "s", "owner": "o", "measurement_window_days": 1,
        },
    ).status_code == 404
    assert client.post(
        "/objectives/OBJ-DOES-NOT-EXIST/observations",
        json={"metric_name": "x", "value": 1.0, "observed_at": datetime.now(timezone.utc).isoformat(), "source": "s", "mode": "live"},
    ).status_code == 404
    assert client.get("/objectives/OBJ-DOES-NOT-EXIST/impact").status_code == 404


def test_non_objective_node_404s_as_objective():
    client.post("/assumptions", json={"id": "ASSUMPTION-IMPACT-001", "title": "x", "owner": "o", "source": "s"})
    assert client.get("/objectives/ASSUMPTION-IMPACT-001/impact").status_code == 404


def test_synthetic_observation_never_allows_a_business_claim():
    make_objective("OBJ-METRIC-004", "BC-METRIC-004")
    client.post(
        "/objectives/OBJ-METRIC-004/metric",
        json={
            "metric_name": "incident_minutes", "baseline": 120.0, "target": 60.0, "unit": "minutes",
            "source": "pagerduty", "owner": "sre", "measurement_window_days": 30,
        },
    )
    observation = client.post(
        "/objectives/OBJ-METRIC-004/observations",
        json={
            "metric_name": "incident_minutes", "value": 90.0,
            "observed_at": datetime.now(timezone.utc).isoformat(), "source": "pagerduty", "mode": "synthetic",
        },
    )
    assert observation.status_code == 201
    impact = client.get("/objectives/OBJ-METRIC-004/impact").json()
    assert impact["status"] == "synthetic_only"
    assert impact["claim_allowed"] is False
    assert impact["delta"] is None


def test_live_observation_in_window_allows_a_business_claim():
    make_objective("OBJ-METRIC-005", "BC-METRIC-005")
    client.post(
        "/objectives/OBJ-METRIC-005/metric",
        json={
            "metric_name": "incident_minutes", "baseline": 120.0, "target": 60.0, "unit": "minutes",
            "source": "pagerduty", "owner": "sre", "measurement_window_days": 30,
        },
    )
    observation = client.post(
        "/objectives/OBJ-METRIC-005/observations",
        json={
            "metric_name": "incident_minutes", "value": 90.0,
            "observed_at": datetime.now(timezone.utc).isoformat(), "source": "pagerduty", "mode": "live",
        },
    )
    assert observation.status_code == 201
    impact = client.get("/objectives/OBJ-METRIC-005/impact").json()
    assert impact["status"] == "measured"
    assert impact["claim_allowed"] is True
    assert impact["delta"] == 30.0


def test_undeclared_objective_reports_no_claim():
    make_objective("OBJ-METRIC-006", "BC-METRIC-006")
    impact = client.get("/objectives/OBJ-METRIC-006/impact").json()
    assert impact["status"] == "undeclared"
    assert impact["claim_allowed"] is False


def test_scorecard_reflects_a_real_task_attempt_evidence_chain(tmp_path):
    # /impact-scorecard aggregates over every Task ever created against the shared
    # app-level DB (same convention every other test in this module relies
    # on), so this asserts the delta this test's own chain contributes
    # rather than an absolute value that other tests' fixtures would pollute.
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "app.py").write_text("print('old')\n", encoding="utf-8")
    make_objective("OBJ-SCORECARD-001", "BC-SCORECARD-001")

    before = {item["name"]: item for item in client.get("/impact-scorecard").json()["metrics"]}
    assert set(before) == set(V0_METRICS)

    client.post(
        "/tasks",
        json={
            "id": "TASK-SCORECARD-001",
            "title": "scorecard task",
            "objective_id": "OBJ-SCORECARD-001",
            "idempotency_key": "task-scorecard-001",
            "acceptance_criteria": ["command succeeds"],
        },
    )
    response = client.post(
        "/tasks/TASK-SCORECARD-001/run",
        json={
            "fixture_dir": str(fixture),
            "commands": [["python", "-c", "print('ok')"]],
            "criterion_checks": {"command succeeds": [["python", "-c", "print('ok')"]]},
        },
    )
    assert response.status_code == 200
    assert response.json()["task_status"] == "succeeded"

    after = {item["name"]: item for item in client.get("/impact-scorecard").json()["metrics"]}

    def delta(name: str, field: str) -> float:
        return (after[name][field] or 0) - (before[name][field] or 0)

    # One new task, attempted once, accepted on the first attempt, with its
    # single criterion covered by PASS evidence and a full objective->task
    # ->attempt->artifact->evidence trace, and no escalation.
    assert delta("accepted_task_rate", "numerator") == 1
    assert delta("accepted_task_rate", "denominator") == 1
    assert delta("first_attempt_success_rate", "numerator") == 1
    assert delta("first_attempt_success_rate", "denominator") == 1
    assert delta("evidence_coverage", "numerator") == 1
    assert delta("evidence_coverage", "denominator") == 1
    assert delta("trace_completeness", "numerator") == 1
    assert delta("trace_completeness", "denominator") == 1
    assert delta("escalation_rate", "numerator") == 0
    assert delta("escalation_rate", "denominator") == 1
    assert after["cost_per_accepted_task"]["basis"] == "synthetic"
