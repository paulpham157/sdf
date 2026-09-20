"""Deterministic provider-neutral routing across model profiles.

Profiles are the configured shape of a model/agent.  A profile is not proof
that the agent is available or that its advertised capabilities are working;
that proof comes from :class:`RoutingMeasurement`.  ``ModelRouter.route``
keeps the original static-profile path for deterministic configuration tests,
while passing measurements makes routing use the measured capability,
availability, latency, cost and success signals exclusively.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Mapping, Sequence

from .escalation import ModelTier


@dataclass(frozen=True, slots=True)
class ModelProfile:
    name: str
    tier: ModelTier
    capabilities: frozenset[str]
    cost_usd: float
    latency_ms: int
    load: float = 0.0
    available: bool = True

    def __post_init__(self) -> None:
        if not self.name.strip():
            raise ValueError("profile name must be non-empty")
        if self.cost_usd < 0 or self.latency_ms < 0:
            raise ValueError("profile cost and latency must not be negative")
        if not 0 <= self.load <= 1:
            raise ValueError("profile load must be between 0 and 1")


@dataclass(frozen=True, slots=True)
class RoutingMeasurement:
    """Observed routing signals for one profile.

    ``sample_count`` prevents a profile with no observations from being
    treated as qualified.  Capabilities are measured/qualified capabilities,
    not merely the profile's configuration claim.
    """

    capabilities: frozenset[str]
    available: bool
    latency_ms: float
    cost_usd: float
    success_rate: float
    sample_count: int
    load: float = 0.0

    def __post_init__(self) -> None:
        if self.latency_ms < 0 or self.cost_usd < 0:
            raise ValueError("measured cost and latency must not be negative")
        if not 0 <= self.success_rate <= 1:
            raise ValueError("measured success rate must be between 0 and 1")
        if self.sample_count <= 0:
            raise ValueError("measured signals require at least one sample")
        if not 0 <= self.load <= 1:
            raise ValueError("measured load must be between 0 and 1")


@dataclass(frozen=True, slots=True)
class RoutingRequest:
    required_capabilities: frozenset[str]
    max_cost_usd: float
    max_latency_ms: int
    min_success_rate: float = 0.0


@dataclass(frozen=True, slots=True)
class RouteDecision:
    profile: ModelProfile | None
    reason: str


class ModelRouter:
    """Choose the best eligible profile using verified operational signals."""

    def route(
        self,
        request: RoutingRequest,
        profiles: Sequence[ModelProfile],
        *,
        success_rates: Mapping[str, float] | None = None,
        measurements: Mapping[str, RoutingMeasurement] | None = None,
    ) -> RouteDecision:
        if request.max_cost_usd < 0 or request.max_latency_ms < 0:
            raise ValueError("routing limits must not be negative")
        if not 0 <= request.min_success_rate <= 1:
            raise ValueError("minimum success rate must be between 0 and 1")
        if success_rates is not None and measurements is not None:
            raise ValueError("provide measured signals or legacy success rates, not both")
        profile_names = [profile.name for profile in profiles]
        if len(profile_names) != len(set(profile_names)):
            raise ValueError("profile names must be unique")
        rates = success_rates or {}
        # Supplying measurements opts into qualified routing: an absent
        # measurement is not allowed to fall back to a stale profile claim.
        candidates: list[tuple[ModelProfile, RoutingMeasurement | None]] = []
        for profile in profiles:
            measured = measurements.get(profile.name) if measurements is not None else None
            if measurements is not None and measured is None:
                continue
            candidates.append((profile, measured))

        eligible = [
            (profile, measured)
            for profile, measured in candidates
            if (measured.available if measured is not None else profile.available)
            and request.required_capabilities.issubset(
                measured.capabilities if measured is not None else profile.capabilities
            )
            and (measured.cost_usd if measured is not None else profile.cost_usd) <= request.max_cost_usd
            and (measured.latency_ms if measured is not None else profile.latency_ms) <= request.max_latency_ms
            and (measured.success_rate if measured is not None else rates.get(profile.name, 0.5)) >= request.min_success_rate
        ]
        if not eligible:
            return RouteDecision(None, "no eligible profile")

        def score(candidate: tuple[ModelProfile, RoutingMeasurement | None]) -> tuple[float, str]:
            profile, measured = candidate
            rate = measured.success_rate if measured is not None else rates.get(profile.name, 0.5)
            if not 0 <= rate <= 1:
                raise ValueError(f"success rate must be between 0 and 1: {profile.name}")
            cost = measured.cost_usd if measured is not None else profile.cost_usd
            latency = measured.latency_ms if measured is not None else profile.latency_ms
            load = measured.load if measured is not None else profile.load
            value = rate * 100 - cost * 10 - latency / 1000 - load * 10
            return value, profile.name

        selected, _ = max(eligible, key=score)
        return RouteDecision(selected, "selected highest-scoring eligible profile")

    def route_measured(
        self,
        request: RoutingRequest,
        profiles: Sequence[ModelProfile],
        measurements: Mapping[str, RoutingMeasurement],
    ) -> RouteDecision:
        """Route only from qualified observations.

        This explicit entry point makes the production boundary visible to
        callers: a missing observation never silently falls back to static
        profile metadata.
        """

        return self.route(request, profiles, measurements=measurements)
