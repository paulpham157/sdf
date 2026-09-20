from sdf_core.escalation import EscalationPolicy, ModelTier


def test_failed_attempt_escalates_one_tier_within_bounds():
    policy = EscalationPolicy(max_attempts=3, max_tier=ModelTier.HIGH, max_cost=10.0)

    decision = policy.decide(
        status="FAIL",
        attempt_number=1,
        current_tier=ModelTier.BASIC,
        spent_cost=0.2,
        next_attempt_cost=0.5,
    )

    assert decision.should_escalate is True
    assert decision.next_tier is ModelTier.MEDIUM
    assert decision.exhausted is False


def test_pass_never_escalates():
    decision = EscalationPolicy().decide(
        status="PASS",
        attempt_number=1,
        current_tier=ModelTier.BASIC,
        spent_cost=0.0,
        next_attempt_cost=1.0,
    )

    assert decision.should_escalate is False
    assert decision.next_tier is None
    assert decision.exhausted is False


def test_escalation_exhausts_on_attempt_tier_or_cost_bound():
    policy = EscalationPolicy(max_attempts=2, max_tier=ModelTier.MEDIUM, max_cost=1.0)

    by_attempt = policy.decide(status="FAIL", attempt_number=2, current_tier=ModelTier.BASIC, spent_cost=0.0, next_attempt_cost=0.1)
    by_tier = policy.decide(status="FAIL", attempt_number=1, current_tier=ModelTier.MEDIUM, spent_cost=0.0, next_attempt_cost=0.1)
    by_cost = policy.decide(status="INCONCLUSIVE", attempt_number=1, current_tier=ModelTier.BASIC, spent_cost=0.95, next_attempt_cost=0.1)

    assert all(item.exhausted and not item.should_escalate for item in (by_attempt, by_tier, by_cost))
