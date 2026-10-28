"""Live OpenCode Attempt in a fresh E2B sandbox against Vultr Serverless Inference.

Skipped by default.  Gated on ``SDF_LIVE_E2B=1`` and ``SDF_LIVE_OPENCODE=1``
plus ``E2B_API_KEY`` and ``VULTR_INFERENCE_API_KEY`` (env or the repo-root
``.env``, loaded into a private mapping).  Runs the full SDF loop:
Task -> Attempt -> OpenCodeHeadlessRuntime (one ``opencode run`` turn in the
sandbox) -> host-side criterion checks -> Evidence.  Egress is restricted to
the inference host; the sandbox is killed and verified gone.

Template: ``SDF_E2B_OPENCODE_TEMPLATE`` (default: the shared
``sdf_core.agent_sandbox.DEFAULT_TEMPLATE``, the pinned ``sdf-herdr-agents``
image with OpenCode 2.2.0; E2B's public ``opencode`` template ships an older
OpenCode the runtime does not support).  Provider/model follow the normal per-agent config
(``SDF_OPENCODE_BASE_URL``, ``SDF_OPENCODE_MODEL``, ...); Vultr is the default.
"""

from __future__ import annotations

import json
import os
import time
from pathlib import Path
from urllib.parse import urlsplit

import pytest
from sqlalchemy.orm import sessionmaker

from sdf_core.adapter import RuntimeAgentAdapter
from sdf_core.agent_sandbox import DEFAULT_TEMPLATE
from sdf_core.credential_injection import create_credentialed_transport
from sdf_core.credentials import DEFAULT_OPENCODE_BASE_URL, load_dotenv
from sdf_core.db import ArtifactRow, Base, DecisionEdgeRow, EvidenceRow, GraphNodeRow, TaskRow, make_engine
from sdf_core.execution import ExecutionService
from sdf_core.model import utcnow
from sdf_core.opencode_runtime import OpenCodeHeadlessRuntime
from sdf_core.runtime import RuntimeController
from tests.test_herdr_e2b_execution import POSITIVE

ENV = dict(os.environ)
load_dotenv(environ=ENV)
KEY_VAR = ENV.get("SDF_OPENCODE_API_KEY_ENV", "VULTR_INFERENCE_API_KEY")
LIVE = (
    ENV.get("SDF_LIVE_E2B") == "1" and ENV.get("SDF_LIVE_OPENCODE") == "1"
    and bool(ENV.get("E2B_API_KEY")) and bool(ENV.get(KEY_VAR))
)
pytestmark = pytest.mark.skipif(
    not LIVE, reason="live OpenCode check: set SDF_LIVE_E2B=1 SDF_LIVE_OPENCODE=1 with E2B_API_KEY and VULTR_INFERENCE_API_KEY"
)

TEMPLATE = ENV.get("SDF_E2B_OPENCODE_TEMPLATE", DEFAULT_TEMPLATE)
CHECKS = {
    "add returns the sum": [["python3", "-c", "from calc import add; assert add(2, 3) == 5"]],
    "sub still subtracts": [["python3", "-c", "from calc import sub; assert sub(5, 3) == 2"]],
}


def _leaks(text: str, secrets: dict[str, str]) -> list[str]:
    return sorted(
        name for name, value in secrets.items() if value in text or any(
            value[i : i + 16] in text for i in range(0, max(len(value) - 16, 0) + 1, 8)
        )
    )


def _running_ids() -> set[str]:
    from e2b import Sandbox

    paginator = Sandbox.list(api_key=ENV["E2B_API_KEY"])
    ids: set[str] = set()
    while paginator.has_next:
        ids.update(item.sandbox_id for item in paginator.next_items())
    return ids


def test_live_opencode_vultr_attempt_in_e2b(tmp_path: Path):
    environ = {
        "E2B_API_KEY": ENV["E2B_API_KEY"],
        "SDF_CREDENTIAL_MODE_OPENCODE": "api-key",
        "SDF_OPENCODE_API_KEY_ENV": KEY_VAR,
        KEY_VAR: ENV[KEY_VAR],
    }
    for name in ("SDF_OPENCODE_BASE_URL", "SDF_OPENCODE_MODEL", "SDF_OPENCODE_SMALL_MODEL"):
        if ENV.get(name):
            environ[name] = ENV[name]
    inference_host = urlsplit(environ.get("SDF_OPENCODE_BASE_URL", DEFAULT_OPENCODE_BASE_URL)).hostname
    secrets = {name: environ[name] for name in ("E2B_API_KEY", KEY_VAR)}
    before = _running_ids()

    transport, injection = create_credentialed_transport(
        agents=("opencode",), environ=environ, template=TEMPLATE, timeout_seconds=900,
        network={"allow_out": [inference_host], "deny_out": ["0.0.0.0/0"]},
    )
    created: list[str] = []
    probes: dict[str, str] = {}
    close = transport.close

    def recording_close(timeout_ms: int = 30_000) -> None:
        if transport.sandbox_id:
            created.append(transport.sandbox_id)
        close(timeout_ms)

    transport.close = recording_close  # type: ignore[method-assign]
    runtime = OpenCodeHeadlessRuntime(transport=transport, turn_timeout_s=480)
    terminate = runtime.terminate

    def probing_terminate(session_id: str):
        # Before teardown: egress elsewhere must fail, the inference host must connect.
        probe = (
            "for u in https://example.com https://registry.npmjs.org https://" + inference_host + "/v1/models; do "
            "code=$(curl -s -o /dev/null -m 8 -w '%{http_code}' \"$u\" 2>/dev/null); echo \"$u $code\"; done"
        )
        try:
            probes["egress"] = transport.exec_argv(["sh", "-c", probe], 60_000)
        except Exception as exc:  # noqa: BLE001 - diagnostics only
            probes["egress"] = f"probe failed: {type(exc).__name__}"
        return terminate(session_id)

    runtime.terminate = probing_terminate  # type: ignore[method-assign]

    engine = make_engine()
    Base.metadata.create_all(engine)
    db = sessionmaker(engine, expire_on_commit=False)()
    db.add(GraphNodeRow(id="ASSUMPTION-CALC", kind="assumption", title="add() is correct", source="test",
                        owner="product", confidence=1.0, created_at=utcnow()))
    db.add(TaskRow(id="TASK-OC-LIVE", title="fix add", status="created", idempotency_key="task-oc-live",
                   acceptance_criteria=list(CHECKS), created_at=utcnow()))
    db.commit()
    fixture = tmp_path / "fixture"
    fixture.mkdir()
    (fixture / "calc.py").write_text(
        "def add(a, b):\n    return a - b\n\n\ndef sub(a, b):\n    return a - b\n", encoding="utf-8"
    )
    service = ExecutionService(
        db, workspace_root=tmp_path / "workspaces", artifact_root=tmp_path / "artifacts",
        adapter=RuntimeAgentAdapter(RuntimeController(runtime, source="opencode-e2b"), agent="opencode"),
    )
    started = time.monotonic()
    try:
        attempt = service.run(
            task_id="TASK-OC-LIVE", dispatch_key="dispatch-oc-live", fixture=fixture, instructions=POSITIVE,
            commands=[], criterion_checks=CHECKS, validation_target=("assumption", "ASSUMPTION-CALC"),
        )
    finally:
        transport.close()  # idempotent; the runtime already closed it on success
        elapsed = round(time.monotonic() - started, 1)
        deadline = time.monotonic() + 30
        while (_running_ids() - before) and time.monotonic() < deadline:
            time.sleep(1)
        leftover = sorted(_running_ids() - before)

    session_id = next(iter(runtime._bindings))
    evidence = db.query(EvidenceRow).filter_by(attempt_id=attempt.id).all()
    relations = {edge.relation for edge in db.query(DecisionEdgeRow).filter_by(target_id="ASSUMPTION-CALC")}
    artifacts = db.query(ArtifactRow).filter_by(attempt_id=attempt.id).all()
    artifact_text = "\n".join(
        Path(a.uri).read_text(errors="replace") for a in artifacts if a.uri and Path(a.uri).is_file()
    )
    stream = "\n".join(runtime.stream(session_id))
    leaked = _leaks(
        "\n".join((stream, artifact_text, probes.get("egress", ""), repr(injection), *injection.seed_commands,
                   json.dumps([(e.criterion, e.status, e.command) for e in evidence], default=str))),
        secrets,
    )
    report = {
        "template": TEMPLATE, "attempt": attempt.id, "attempt_status": attempt.status,
        "task": db.get(TaskRow, "TASK-OC-LIVE").status,
        "evidence": sorted((e.criterion, e.status) for e in evidence), "relations": sorted(relations),
        "permission_skipping": runtime.permission_skipping(session_id), "usage": runtime.usage(session_id),
        "sandboxes_created": len(created), "elapsed_s": elapsed, "leftover_sandboxes": len(leftover),
        "egress": probes.get("egress", "").strip().splitlines(), "leaks": leaked,
        "stream_tail": runtime.stream(session_id)[-8:],
    }
    print("SDF_LIVE_OPENCODE_REPORT " + json.dumps(report, default=str))
    assert not leaked
    assert not leftover and len(created) == 1
    assert report["task"] == "succeeded"
    assert report["evidence"] == [("add returns the sum", "PASS"), ("sub still subtracts", "PASS")]
    assert relations == {"validates"}
