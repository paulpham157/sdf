"""#60 / ADR 0033 review SF2: a provider sandbox timeout (E2B) is extended during a material hold.

All fakes: no live E2B or Daytona sandbox is used.
"""

from __future__ import annotations

import time
from pathlib import Path
from types import SimpleNamespace

import pytest

from sdf_core import coordinator as coordinator_module
from sdf_core.agent_sandbox import AgentSpec, E2BAgentSandbox
from sdf_core.coordinator import Coordinator
from sdf_core.e2b_herdr_transport import E2BHerdrTransport
from sdf_core.herdr_runtime import HerdrRuntimeError
from tests.test_material_decision_gate import (  # noqa: F401 - fixtures
    _approved, _config, _resolve_when_pending, asks_then_works, db, engine, fixture,
)
from tests.agent_comm_support import fake_factory, lead_behavior, worker_behavior


class _Lifetime:
    """A fake provider lifetime bolted onto a sandbox (seconds on the monotonic clock)."""

    def __init__(self, left: float, fail: Exception | None = None, supports: bool = True):
        self.expires = time.monotonic() + left
        self.fail = fail
        self.calls: list[int] = []
        self.supports = supports

    def lifetime_left_s(self):
        return max(0.0, self.expires - time.monotonic())

    def extend_lifetime(self, seconds: int):
        self.calls.append(seconds)
        if self.fail is not None:
            raise self.fail
        self.expires = time.monotonic() + seconds


def _agents(**sandboxes):
    return {aid: SimpleNamespace(sandbox=sbx) for aid, sbx in sandboxes.items()}


def _bare_coordinator():
    return Coordinator.__new__(Coordinator)


def test_keep_alive_extends_only_what_is_short_and_skips_sandboxes_without_a_provider_timeout():
    short, long_enough = _Lifetime(100), _Lifetime(5000)
    log: list = []
    coordinator = _bare_coordinator()
    coordinator.log = lambda message: None
    assert coordinator._keep_alive(_agents(a=short, b=long_enough, c=SimpleNamespace()), 1000.0, log, {}, 7.0) is None
    assert short.calls == [int(1000 + coordinator_module.KEEPALIVE_STEP_S)]
    assert long_enough.calls == []
    assert log == [{"agent": "a", "at_s": 7.0, "left_s": pytest.approx(100, abs=1), "extended_to_s": 1600}]
    # Daytona-style (no provider timeout): lifetime_left_s() is None, nothing to do.
    none = SimpleNamespace(lifetime_left_s=lambda: None, extend_lifetime=lambda s: pytest.fail("not called"))
    assert coordinator._keep_alive(_agents(d=none), 1000.0, [], {}, 0.0) is None


def test_a_failed_extension_is_retried_and_fails_the_run_only_near_expiry():
    coordinator = _bare_coordinator()
    coordinator.log = lambda message: None
    broken = _Lifetime(500, fail=HerdrRuntimeError("E2B sandbox lifetime extension failed: limit"))
    log: list = []
    tried: dict = {}
    assert coordinator._keep_alive(_agents(w=broken), 1000.0, log, tried, 1.0) is None  # 500 s left: not yet
    assert coordinator._keep_alive(_agents(w=broken), 1000.0, log, tried, 1.1) is None  # retry is rate-limited
    assert len(broken.calls) == 1 and "limit" in log[0]["error"]
    broken.expires = time.monotonic() + 30  # under LIFETIME_FAIL_MARGIN_S
    error = coordinator._keep_alive(_agents(w=broken), 1000.0, log, tried, 2.0)
    assert error is not None and error.startswith("sandbox of w would expire during a material decision hold")
    assert "limit" in error  # the provider's own reason, not a generic one
    unsupported = SimpleNamespace(lifetime_left_s=lambda: 10.0)
    error = coordinator._keep_alive(_agents(u=unsupported), 1000.0, [], {}, 0.0)
    assert error is not None and "does not support it" in error


def _with_lifetimes(tmp_path, behaviors, lifetimes: dict):
    running: set[str] = set()
    created: list = []
    base = fake_factory(tmp_path / "boxes", running, behaviors, created)

    def build(spec: AgentSpec):
        sandbox = base(spec)
        life = lifetimes.get(spec.agent_id)
        if life is not None:
            sandbox.lifetime_left_s = life.lifetime_left_s
            sandbox.extend_lifetime = life.extend_lifetime
        return sandbox

    return build, running


@pytest.fixture()
def fast_keepalive(monkeypatch):
    monkeypatch.setattr(coordinator_module, "KEEPALIVE_MARGIN_S", 0.5)
    monkeypatch.setattr(coordinator_module, "KEEPALIVE_STEP_S", 1.0)
    monkeypatch.setattr(coordinator_module, "KEEPALIVE_RETRY_S", 0.05)
    monkeypatch.setattr(coordinator_module, "LIFETIME_FAIL_MARGIN_S", 0.3)


def test_a_long_hold_keeps_the_sandboxes_alive_and_records_it(engine, db, fixture, tmp_path, fast_keepalive):
    plan = _approved(db, fixture, "PLAN-KA1")
    lifetimes = {"lead-1": _Lifetime(3.0), "worker-1": _Lifetime(3.0)}
    factory, running = _with_lifetimes(tmp_path, {
        "lead": lead_behavior, "worker": worker_behavior(), "worker-1": asks_then_works(material=True)}, lifetimes)
    coordinator = Coordinator(db, factory=factory, workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art",
                              cleanup_check=lambda ids: ids & running)
    # The human takes 4 s; each sandbox would end 3 s after it started.
    resolver = _resolve_when_pending(engine, plan.id, "approved", "keep them", wait_s=4.0)
    run = coordinator.execute(coordinator.create_run(plan.id, _config(wall=2)).id)
    resolver.join()
    assert run.status == "succeeded", run.error
    log = run.summary["sandbox_keepalive"]
    assert {entry["agent"] for entry in log} == {"lead-1", "worker-1"}
    assert all("extended_to_s" in entry and "error" not in entry for entry in log)
    assert all(life.calls for life in lifetimes.values())
    assert running == set()


def test_a_hold_that_cannot_keep_its_sandbox_alive_fails_the_run_cleanly(engine, db, fixture, tmp_path,
                                                                         fast_keepalive):
    plan = _approved(db, fixture, "PLAN-KA2")
    life = _Lifetime(2.0, fail=HerdrRuntimeError("E2B sandbox lifetime extension failed: over the plan maximum"))
    factory, running = _with_lifetimes(tmp_path, {
        "lead": lead_behavior, "worker": worker_behavior(), "worker-1": asks_then_works(material=True)},
        {"worker-1": life})
    coordinator = Coordinator(db, factory=factory, workspace_root=tmp_path / "ws", artifact_root=tmp_path / "art",
                              cleanup_check=lambda ids: ids & running)
    resolver = _resolve_when_pending(engine, plan.id, "approved", "keep them", wait_s=4.0)
    run = coordinator.execute(coordinator.create_run(plan.id, _config(wall=2)).id)
    resolver.join()
    assert run.status == "failed"
    assert run.error.startswith("sandbox of worker-1 would expire during a material decision hold"), run.error
    assert any("over the plan maximum" in entry.get("error", "") for entry in run.summary["sandbox_keepalive"])
    assert running == set()  # still cleaned up


def test_e2b_sandbox_tracks_and_extends_its_provider_lifetime():
    sandbox = E2BAgentSandbox(AgentSpec("worker-1", "worker", "pi", None), environ={}, sandbox_timeout_s=1800)
    assert sandbox.lifetime_left_s() is None  # not started: nothing to keep alive
    with pytest.raises(RuntimeError, match="no provider lifetime"):
        sandbox.extend_lifetime(600)
    calls: list[int] = []
    sandbox._transport = SimpleNamespace(extend_lifetime=calls.append)
    sandbox._born = time.monotonic()
    sandbox._expires = sandbox._born + 1800
    assert 1790 < sandbox.lifetime_left_s() <= 1800
    sandbox.extend_lifetime(3000)
    assert calls == [3000] and 2990 < sandbox.lifetime_left_s() <= 3000
    sandbox._died = time.monotonic()
    assert sandbox.lifetime_left_s() is None


class _FakeE2BSandbox:
    sandbox_id = "sbx-e2b-fake"

    def __init__(self):
        self.timeouts: list[int] = []

    def set_timeout(self, timeout: int, **opts):
        self.timeouts.append(timeout)
        self.opts = opts


def test_e2b_transport_extends_the_existing_sandbox_with_set_timeout_and_never_creates_one():
    fake = _FakeE2BSandbox()
    transport = E2BHerdrTransport(template="t", environ={"E2B_API_KEY": "test-not-a-key"}, sandbox_id=fake.sandbox_id,
                                  sandbox_connector=lambda sandbox_id, **opts: fake,
                                  sandbox_factory=lambda **kw: pytest.fail("must not create a sandbox"))
    transport.extend_lifetime(2400)
    assert fake.timeouts == [2400] and fake.opts == {"request_timeout": transport.request_timeout_seconds}
    for bad in (0, -5, 1.5, True):
        with pytest.raises(ValueError):
            transport.extend_lifetime(bad)
    fresh = E2BHerdrTransport(template="t", environ={"E2B_API_KEY": "test-not-a-key"},
                              sandbox_factory=lambda **kw: pytest.fail("must not create a sandbox"))
    with pytest.raises(HerdrRuntimeError, match="no sandbox"):
        fresh.extend_lifetime(600)

    class Refusing(_FakeE2BSandbox):
        def set_timeout(self, timeout, **opts):
            raise RuntimeError("timeout exceeds the maximum sandbox lifetime")

    refusing = E2BHerdrTransport(template="t", environ={"E2B_API_KEY": "test-not-a-key"}, sandbox_id="sbx-2",
                                 sandbox_connector=lambda sandbox_id, **opts: Refusing())
    with pytest.raises(HerdrRuntimeError, match="lifetime extension failed: timeout exceeds"):
        refusing.extend_lifetime(90000)


def test_the_daytona_transport_has_no_provider_lifetime_to_extend():
    # Daytona sandboxes are created with auto_stop_interval=0, so E2BAgentSandbox
    # on that provider reports no lifetime and the coordinator leaves it alone.
    from sdf_core.daytona_herdr_transport import DaytonaHerdrTransport

    assert not hasattr(DaytonaHerdrTransport, "extend_lifetime")


class _Capped(_Lifetime):
    """E2B-like: a timeout past the account maximum is accepted but capped, without an error."""

    def __init__(self, left: float, cap_s: float):
        super().__init__(left)
        self.cap_s = cap_s

    def extend_lifetime(self, seconds: int):
        self.calls.append(seconds)
        granted = min(seconds, self.cap_s)
        self.expires = time.monotonic() + granted
        return granted


def test_a_silently_capped_extension_is_not_trusted_and_fails_cleanly_near_the_cap():
    """Live finding (E2B, 2026-10-31): set_timeout(200000) succeeded but end_at = started_at + 24 h."""

    coordinator = _bare_coordinator()
    coordinator.log = lambda message: None
    capped = _Capped(500, cap_s=300)
    log: list = []
    tried: dict = {}
    assert coordinator._keep_alive(_agents(w=capped), 1000.0, log, tried, 1.0) is None  # 300 s left: not yet
    assert log[-1]["capped"] is True and log[-1]["granted_s"] == pytest.approx(300, abs=1)
    assert coordinator._keep_alive(_agents(w=capped), 1000.0, log, tried, 1.1) is None  # rate-limited retry
    assert len(capped.calls) == 1
    capped.expires = time.monotonic() + 40  # the cap is near; the next extension is capped too
    tried.clear()
    capped.cap_s = 40
    error = coordinator._keep_alive(_agents(w=capped), 1000.0, log, tried, 2.0)
    assert error is not None and error.startswith("sandbox of w would expire during a material decision hold")
    assert "capped its lifetime at" in error
    # An extension that grants enough is still a plain success.
    roomy = _Capped(100, cap_s=86_400)
    log2: list = []
    assert coordinator._keep_alive(_agents(r=roomy), 1000.0, log2, {}, 0.0) is None
    assert "capped" not in log2[-1] and log2[-1]["extended_to_s"] == 1600


def test_e2b_sandbox_uses_the_lifetime_the_provider_granted():
    sandbox = E2BAgentSandbox(AgentSpec("worker-1", "worker", "pi", None), environ={}, sandbox_timeout_s=1800)
    sandbox._born = time.monotonic()
    sandbox._expires = sandbox._born + 1800
    sandbox._transport = SimpleNamespace(extend_lifetime=lambda seconds: 50.0)  # capped by the provider
    assert sandbox.extend_lifetime(3000) == 50.0 and sandbox.lifetime_left_s() <= 50
    sandbox._transport = SimpleNamespace(extend_lifetime=lambda seconds: None)  # end time unreadable
    assert sandbox.extend_lifetime(3000) == 3000.0 and 2990 < sandbox.lifetime_left_s() <= 3000


def test_e2b_transport_reads_back_the_capped_end_time():
    from datetime import datetime, timedelta, timezone

    class Capping(_FakeE2BSandbox):
        def set_timeout(self, timeout, **opts):
            self.timeouts.append(timeout)

        def get_info(self, **opts):
            return SimpleNamespace(end_at=datetime.now(timezone.utc) + timedelta(seconds=3600))

    capping = Capping()
    transport = E2BHerdrTransport(template="t", environ={"E2B_API_KEY": "test-not-a-key"}, sandbox_id="sbx-3",
                                  sandbox_connector=lambda sandbox_id, **opts: capping)
    assert transport.extend_lifetime(200_000) == pytest.approx(3600, abs=5)

    class Unreadable(_FakeE2BSandbox):
        def get_info(self, **opts):
            raise RuntimeError("info unavailable")

    transport = E2BHerdrTransport(template="t", environ={"E2B_API_KEY": "test-not-a-key"}, sandbox_id="sbx-4",
                                  sandbox_connector=lambda sandbox_id, **opts: Unreadable())
    assert transport.extend_lifetime(600) is None
