"""Live-test config must not silently run evaluator checks on the host.

Hermetic tests keep ``SDF_EVALUATOR_CONTAINMENT=off`` (no Docker, no e2b-box).
A live gate plus that session default used to execute agent-written workspace
code as the pytest user. ADR 0025's default for a live agent is ``require``.
"""

from __future__ import annotations

import os
from pathlib import Path

import pytest

from sdf_core import api
from sdf_core.api import SessionLocal
from sdf_core.containment import ContainmentUnavailable
from sdf_core.execution import ExecutionService
from tests.check_support import ADD_SUM_CHECK
from tests.live_containment import (
    LIVE_GATE_ENVS,
    enforce_live_evaluator_containment,
    install_live_execution_service,
    live_gate_enabled,
    module_is_live_test,
    node_is_live_gated,
)


class _Node:
    def __init__(self, path: str, reasons: tuple[str, ...] = ()):
        self.path = Path(path)
        self._reasons = reasons

    def iter_markers(self, name):
        if name != "skipif":
            return ()
        return [_Mark(reason) for reason in self._reasons]


class _Mark:
    def __init__(self, reason: str):
        self.kwargs = {"reason": reason}


def test_hermetic_session_keeps_containment_off_without_a_live_gate(monkeypatch):
    for name in LIVE_GATE_ENVS:
        monkeypatch.delenv(name, raising=False)
    assert live_gate_enabled() is False
    assert os.environ["SDF_EVALUATOR_CONTAINMENT"] == "off"
    assert api.evaluator_mode(live=False) == "off"


@pytest.mark.parametrize("gate,value", [
    ("SDF_LIVE_E2B", "1"),
    ("SDF_LIVE_FULL_LOOP", "1"),
    ("SDF_LIVE_VULTR_AGENTS", "pi"),
])
def test_live_gate_cannot_keep_the_hermetic_off_default(monkeypatch, gate, value):
    """The session ``off`` is what the live full loop inherited. It must not stick."""

    for name in LIVE_GATE_ENVS:
        monkeypatch.delenv(name, raising=False)
    monkeypatch.setenv("SDF_EVALUATOR_CONTAINMENT", "off")
    monkeypatch.delenv("SDF_CONTAINMENT_BACKEND", raising=False)
    monkeypatch.delenv("SDF_CONTAINMENT_SMOKE", raising=False)
    monkeypatch.setenv(gate, value)
    assert live_gate_enabled() is True
    enforce_live_evaluator_containment(monkeypatch)
    assert api.evaluator_mode(live=True) == "require"
    evaluator = api.configured_evaluator(live=True)
    assert evaluator.require_containment is True
    assert evaluator.containment_backend is not None
    assert evaluator.runner != "host"


def test_enforced_live_config_does_not_run_the_check_on_the_host(tmp_path, monkeypatch):
    monkeypatch.setenv("SDF_EVALUATOR_CONTAINMENT", "off")
    monkeypatch.setenv("SDF_LIVE_FULL_LOOP", "1")
    monkeypatch.delenv("SDF_CONTAINMENT_BACKEND", raising=False)
    monkeypatch.delenv("SDF_CONTAINMENT_SMOKE", raising=False)
    # Never call a real e2b-box, even if one is installed and a key is set.
    calls: list = []

    def fake_run(self, command, **kwargs):
        calls.append(tuple(command))
        raise ContainmentUnavailable("hermetic")

    monkeypatch.setattr("sdf_core.e2b_containment.E2BContainmentBackend.run", fake_run)
    enforce_live_evaluator_containment(monkeypatch)
    marker = tmp_path / "host-ran"
    workspace = tmp_path / "ws"
    workspace.mkdir()
    (workspace / "calc.py").write_text("import os\nos._exit(0)\n", encoding="utf-8")
    code = f"import os; open({str(marker)!r}, 'w').write('ran'); os._exit(0)"
    evidence = api.configured_evaluator(live=True)._run_one(
        command=["python3", "-c", code], workspace=workspace, attempt_id="A", index=1,
        criterion="c", reference=None,
    )
    assert calls, "the check was not handed to the containment backend"
    assert not marker.exists()
    assert evidence.status == "INCONCLUSIVE"
    assert evidence.runner != "host"


def test_execution_service_in_a_live_test_does_not_default_to_the_host(tmp_path, monkeypatch):
    monkeypatch.setenv("SDF_EVALUATOR_CONTAINMENT", "off")
    monkeypatch.setenv("SDF_LIVE_E2B", "1")
    monkeypatch.delenv("SDF_CONTAINMENT_BACKEND", raising=False)
    enforce_live_evaluator_containment(monkeypatch)
    install_live_execution_service(monkeypatch)
    db = SessionLocal()
    try:
        service = ExecutionService(db, workspace_root=tmp_path / "w", artifact_root=tmp_path / "a")
        assert service.evaluator.require_containment is True
        assert service.evaluator.containment_backend is not None
        assert service.evaluator.runner != "host"
    finally:
        db.close()


def test_explicit_off_stays_valid_when_provider_keys_are_set_and_is_warned(monkeypatch):
    """ADR 0025 allows host mode. Refuse is a policy change; warn, and do not log values."""

    monkeypatch.setenv("SDF_EVALUATOR_CONTAINMENT", "off")
    monkeypatch.setenv("E2B_API_KEY", "<REDACTED>")
    monkeypatch.setenv("VULTR_INFERENCE_API_KEY", "<REDACTED>")
    warnings: list = []
    monkeypatch.setattr(api._log, "warning", lambda msg, *a, **k: warnings.append((msg, k.get("extra"))))
    assert api.evaluator_mode(live=True) == "off"
    evaluator = api.configured_evaluator(live=True)
    assert evaluator.require_containment is False
    assert evaluator.runner == "host"
    assert warnings and warnings[0][0] == "evaluator_runs_on_host"
    extra = warnings[0][1]
    assert extra["provider_keys"] == ["E2B_API_KEY", "VULTR_INFERENCE_API_KEY"]
    assert "<REDACTED>" not in str(extra)
    assert "provider key variables are set" in extra["reason"]


def test_live_gated_modules_are_covered_by_the_containment_guard():
    """A new live-gated module that the fixture would miss fails here.

    The fixture selects a test by file name (``*_live.py`` / ``test_live_*``)
    or by a ``skipif`` reason that names a live gate. A mixed module such as
    ``test_herdr_e2b_execution.py`` has to put the gate in that reason; a
    mention elsewhere in the file is not enough.
    """

    import re

    uncovered: list[str] = []
    tests = Path(__file__).resolve().parent
    reason_re = re.compile(r"""reason\s*=\s*(?P<q>['"])(?P<reason>.*?)(?P=q)""")
    for path in sorted(tests.glob("test_*.py")):
        if path.name == Path(__file__).name:
            continue
        text = path.read_text(encoding="utf-8")
        if not any(gate in text for gate in LIVE_GATE_ENVS):
            continue
        if module_is_live_test(path):
            assert node_is_live_gated(_Node(str(path))) is True
            continue
        reasons = [match.group("reason") for match in reason_re.finditer(text)]
        gated = [reason for reason in reasons if any(gate in reason for gate in LIVE_GATE_ENVS)]
        if not gated:
            uncovered.append(path.name)
            continue
        assert node_is_live_gated(_Node(str(path), tuple(gated))) is True
        assert node_is_live_gated(_Node(str(path))) is False
    assert uncovered == []


def test_vultr_agents_value_zero_is_not_a_live_gate():
    assert live_gate_enabled({"SDF_LIVE_VULTR_AGENTS": "0"}) is False
    assert live_gate_enabled({"SDF_LIVE_VULTR_AGENTS": ""}) is False
    assert live_gate_enabled({}) is False
