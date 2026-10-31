"""Evaluator containment for live-gated tests (ADR 0025).

``tests/conftest.py`` sets ``SDF_EVALUATOR_CONTAINMENT=off`` so hermetic plan
runs can execute checks without a containment backend. That session default
used to apply to live tests as well, so a live full loop ran agent-written
workspace code (``python3 -c`` importing ``calc.py``) on the host, as the
pytest user. Another same-uid process — the ``uv run`` parent, the launching
shell — still has provider keys in ``/proc/<pid>/environ``. Production leaves
the variable unset, which is ``require`` for a live agent.

This module is the guard the session fixture applies. It does not change the
operator contract: an explicit ``off`` on a deployment is still accepted and
warned (ADR 0025).
"""

from __future__ import annotations

import os
from collections.abc import Mapping
from pathlib import Path

# Gates named by the live-test finding. Other live modules also require
# SDF_LIVE_E2B, so they are covered by the first variable.
LIVE_GATE_ENVS = ("SDF_LIVE_E2B", "SDF_LIVE_FULL_LOOP", "SDF_LIVE_VULTR_AGENTS")
_OFF_VALUES = {"0", "false", "no"}


def live_gate_enabled(env: Mapping[str, str] | None = None) -> bool:
    """True when a live-test gate is set in ``env`` (default: the process)."""

    env = os.environ if env is None else env
    if env.get("SDF_LIVE_E2B") == "1" or env.get("SDF_LIVE_FULL_LOOP") == "1":
        return True
    selected = (env.get("SDF_LIVE_VULTR_AGENTS") or "").strip()
    return bool(selected) and selected.lower() not in _OFF_VALUES


def module_is_live_test(path: Path | str) -> bool:
    """A module whose tests are live-gated as a group (``pytestmark``)."""

    name = Path(path).name
    return name.endswith("_live.py") or name.startswith("test_live_")


def node_is_live_gated(node) -> bool:
    """A collected test that must not inherit the hermetic containment default.

    Whole live modules match by file name. A live test inside a mixed module
    (``tests/test_herdr_e2b_execution.py``) matches when its ``skipif`` reason
    names a live gate. Hermetic tests in that file do not.
    """

    path = getattr(node, "path", None)
    if path is None:
        path = Path(str(node.fspath))
    if module_is_live_test(path):
        return True
    for mark in node.iter_markers("skipif"):
        reason = str(mark.kwargs.get("reason", ""))
        if any(gate in reason for gate in LIVE_GATE_ENVS):
            return True
    return False


def enforce_live_evaluator_containment(monkeypatch) -> None:
    """Force ``require`` and a promoted backend, ignoring a session ``off``.

    Unset ``SDF_CONTAINMENT_BACKEND`` becomes ``e2b`` with
    ``SDF_CONTAINMENT_SMOKE=passed``, the operator seam ``configured_evaluator``
    already uses (ADR 0025). A missing ``e2b-box`` then makes the check
    INCONCLUSIVE instead of a host process. An operator-selected backend is
    left alone; smoke is promoted only for the e2b backend this harness sets
    or the operator already named.
    """

    monkeypatch.setenv("SDF_EVALUATOR_CONTAINMENT", "require")
    backend = (os.environ.get("SDF_CONTAINMENT_BACKEND") or "").strip().lower()
    if not backend:
        monkeypatch.setenv("SDF_CONTAINMENT_BACKEND", "e2b")
        backend = "e2b"
    if backend == "e2b" and (os.environ.get("SDF_CONTAINMENT_SMOKE") or "").strip().lower() != "passed":
        monkeypatch.setenv("SDF_CONTAINMENT_SMOKE", "passed")


def install_live_execution_service(monkeypatch) -> None:
    """``ExecutionService()`` in a live test uses the configured live evaluator.

    The API path already calls ``configured_evaluator``. Direct
    ``ExecutionService`` construction (the Vultr and real-agent loops) defaults
    to a host evaluator and would ignore the env var.
    """

    from sdf_core.api import configured_evaluator
    from sdf_core.execution import ExecutionService

    original = ExecutionService.__init__

    def init(self, db, *, workspace_root, artifact_root, adapter=None, evaluator=None):
        if evaluator is None:
            evaluator = configured_evaluator(live=True)
        original(
            self, db, workspace_root=workspace_root, artifact_root=artifact_root,
            adapter=adapter, evaluator=evaluator,
        )

    monkeypatch.setattr(ExecutionService, "__init__", init)
