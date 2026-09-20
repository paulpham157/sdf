from sdf_core.escalation import ModelTier
import pytest

from sdf_core.routing import ModelProfile, ModelRouter, RoutingMeasurement, RoutingRequest


def profiles():
    return [
        ModelProfile("basic-fast", ModelTier.BASIC, frozenset({"python"}), cost_usd=0.2, latency_ms=800, load=0.1),
        ModelProfile("medium-reliable", ModelTier.MEDIUM, frozenset({"python", "debugging"}), cost_usd=0.8, latency_ms=1400, load=0.2),
        ModelProfile("high-busy", ModelTier.HIGH, frozenset({"python", "debugging"}), cost_usd=1.5, latency_ms=900, load=0.9),
    ]


def test_router_prefers_capable_profile_with_best_verified_score():
    router = ModelRouter()

    decision = router.route(
        RoutingRequest(required_capabilities=frozenset({"python", "debugging"}), max_cost_usd=1.0, max_latency_ms=2000),
        profiles(),
        success_rates={"medium-reliable": 0.92, "high-busy": 0.99},
    )

    assert decision.profile is not None
    assert decision.profile.name == "medium-reliable"
    assert decision.reason == "selected highest-scoring eligible profile"


def test_router_returns_no_route_when_constraints_cannot_be_met():
    decision = ModelRouter().route(
        RoutingRequest(required_capabilities=frozenset({"rust"}), max_cost_usd=1.0, max_latency_ms=1000),
        profiles(),
    )

    assert decision.profile is None
    assert decision.reason == "no eligible profile"


def test_router_excludes_unavailable_profiles_and_is_deterministic():
    candidates = profiles()
    candidates[1] = ModelProfile("medium-reliable", ModelTier.MEDIUM, frozenset({"python", "debugging"}), cost_usd=0.8, latency_ms=1400, load=0.2, available=False)
    request = RoutingRequest(required_capabilities=frozenset({"python"}), max_cost_usd=1.0, max_latency_ms=2000)

    first = ModelRouter().route(request, candidates)
    second = ModelRouter().route(request, candidates)

    assert first.profile == second.profile
    assert first.profile is not None and first.profile.name == "basic-fast"


def test_measured_routing_uses_qualified_signals_over_static_profile_claims():
    candidates = [
        # Static metadata says this is the best option, but the qualified
        # runtime observations say that it is unavailable and lacks debugging.
        ModelProfile("agent-a", ModelTier.BASIC, frozenset({"python", "debugging"}), cost_usd=0.1, latency_ms=100),
        ModelProfile("agent-b", ModelTier.MEDIUM, frozenset({"python"}), cost_usd=0.9, latency_ms=1800),
    ]
    measurements = {
        "agent-a": RoutingMeasurement(
            capabilities=frozenset({"python"}), available=False, latency_ms=100, cost_usd=0.1,
            success_rate=0.99, sample_count=4,
        ),
        "agent-b": RoutingMeasurement(
            capabilities=frozenset({"python", "debugging"}), available=True, latency_ms=700, cost_usd=0.4,
            success_rate=0.86, sample_count=8,
        ),
    }

    decision = ModelRouter().route_measured(
        RoutingRequest(
            required_capabilities=frozenset({"python", "debugging"}),
            max_cost_usd=1.0,
            max_latency_ms=2_000,
            min_success_rate=0.8,
        ),
        candidates,
        measurements,
    )

    assert decision.profile is not None
    assert decision.profile.name == "agent-b"


def test_measured_routing_does_not_fallback_to_unqualified_profile():
    decision = ModelRouter().route_measured(
        RoutingRequest(required_capabilities=frozenset({"python"}), max_cost_usd=1.0, max_latency_ms=2_000),
        profiles(),
        {"unknown-agent": RoutingMeasurement(
            capabilities=frozenset({"python"}), available=True, latency_ms=10, cost_usd=0.01,
            success_rate=1.0, sample_count=1,
        )},
    )

    assert decision.profile is None
    assert decision.reason == "no eligible profile"


def test_measured_route_is_deterministic_for_equal_scores():
    candidates = [
        ModelProfile("agent-z", ModelTier.BASIC, frozenset({"python"}), cost_usd=0.2, latency_ms=100),
        ModelProfile("agent-a", ModelTier.BASIC, frozenset({"python"}), cost_usd=0.2, latency_ms=100),
    ]
    observation = RoutingMeasurement(
        capabilities=frozenset({"python"}), available=True, latency_ms=100, cost_usd=0.2,
        success_rate=0.9, sample_count=3,
    )

    first = ModelRouter().route_measured(
        RoutingRequest(frozenset({"python"}), 1.0, 1_000),
        candidates,
        {"agent-z": observation, "agent-a": observation},
    )
    second = ModelRouter().route_measured(
        RoutingRequest(frozenset({"python"}), 1.0, 1_000),
        list(reversed(candidates)),
        {"agent-z": observation, "agent-a": observation},
    )

    assert first.profile is not None and first.profile.name == "agent-z"
    assert second.profile is not None and second.profile.name == "agent-z"


def test_routing_measurements_require_a_positive_sample_count():
    with pytest.raises(ValueError, match="at least one sample"):
        RoutingMeasurement(
            capabilities=frozenset({"python"}), available=True, latency_ms=100, cost_usd=0.2,
            success_rate=0.9, sample_count=0,
        )
