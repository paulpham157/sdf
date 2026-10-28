"""Live ADR-0019: a turn's detached / double-forked leftovers never survive it.

Skipped by default.  Gated on ``SDF_LIVE_E2B=1`` and ``SDF_LIVE_CONTAINMENT``
(``1`` = all six kinds, or a comma-separated subset) plus ``E2B_API_KEY`` and
``VULTR_INFERENCE_API_KEY`` (env / repo ``.env``; presence only, values never
printed).  Template: ``SDF_E2B_VULTR_TEMPLATE`` (prebuilt agents image) for
every kind; ``SDF_LIVE_CONTAINMENT_SCENARIOS`` picks ``timeout,sigkill,cancel``.

Per (kind, scenario), one fresh sandbox: the agent is asked to run
``sh spawn.sh``, which starts ``setsid sleep 300``, ``env -i setsid sleep 301``
(no tag), a double-forked workspace writer (``tick.sh``, reparented to
PID 1) and then blocks in ``sleep 600``.  Once the spawn marker exists the test

* ``timeout`` - lets the runtime's own turn timeout fire;
* ``sigkill`` - SIGKILLs the agent process mid-turn;
* ``cancel`` - calls ``runtime.cancel`` (provider teardown deferred so the
  sandbox can be inspected).

A Herdr turn that outlives its wait returns ``RUNNING`` (by design); the test
then terminates the session as the adapter does.  Afterwards no ``sleep
300/301/600`` and no writer may be left, the workspace ``leak.txt`` must stop
growing and no turn cgroup may remain.
"""

from __future__ import annotations

import json
import os
import threading
import time
from pathlib import Path
from typing import Any

import pytest

import sdf_core.e2b_herdr_transport as e2b_transport_module
from sdf_core.agent_models import model_agent_args
from sdf_core.credential_injection import create_credentialed_transport
from sdf_core.herdr_runtime import HerdrRuntime
from sdf_core.opencode_runtime import OpenCodeHeadlessRuntime
from tests import test_vultr_agents_live as V

ENV = V.ENV
_SEL = ENV.get("SDF_LIVE_CONTAINMENT", "")
KINDS = ("pi", "qwen", "goose", "opencode", "codex", "claude") if _SEL == "1" else tuple(k for k in _SEL.split(",") if k)
SCENARIOS = tuple(ENV.get("SDF_LIVE_CONTAINMENT_SCENARIOS", "timeout,sigkill,cancel").split(","))
LIVE = ENV.get("SDF_LIVE_E2B") == "1" and bool(KINDS) and bool(ENV.get("E2B_API_KEY")) and bool(ENV.get(V.KEY_VAR))
pytestmark = pytest.mark.skipif(not LIVE, reason="live: set SDF_LIVE_E2B=1 SDF_LIVE_CONTAINMENT=1 with E2B/Vultr keys")
TEMPLATE = ENV.get("SDF_E2B_VULTR_TEMPLATE", "")
REPORT_DIR = Path("/tmp/sdf-live-reports")
D = "/tmp/sdf-live"

SPAWN = f"""#!/bin/sh
# SDF ADR-0019 live probe: leave processes behind, then block.
mkdir -p {D}; pwd > {D}/ws
setsid sleep 300 </dev/null >/dev/null 2>&1 &
env -i /usr/bin/setsid /bin/sleep 301 </dev/null >/dev/null 2>&1 &
( setsid sh "$PWD/tick.sh" "$PWD" </dev/null >/dev/null 2>&1 & )
touch {D}/spawned
echo spawned
sleep 600
"""
WRITER = 'while :; do date >> "$1/leak.txt"; sleep 1; done\n'
PROMPT = ("Run exactly this one shell command in the current directory and wait for it to finish: "
          "`sh spawn.sh` . It is a long-running integration probe; do not stop it, do not run anything else, "
          "do not edit any file.")
# Qwen Code + glm-5.3: the open-ended PROMPT above (plus a workspace file named
# ``leak_writer.sh``) made the model reason for 3-7 min (~23k chars: tool
# timeouts, "do not run anything else", whether to read the scripts first)
# before its first tool call.  A literal tool call with neutral file names
# reaches the spawn in seconds; nothing about containment changes.
FAST_PROMPT = ("Call run_shell_command exactly once, immediately, with command `sh spawn.sh` and timeout "
               "600000 (foreground). Do not read or inspect any file first. The script prints 'spawned' and "
               "then keeps running for minutes; that is expected, just wait. Afterwards reply 'done'.")
PROMPTS = {"qwen": FAST_PROMPT}
CHECK = f"""sleep 3
ws=$(cat {D}/ws 2>/dev/null)
s1=$(stat -c %s "$ws/leak.txt" 2>/dev/null || echo 0); sleep 5; s2=$(stat -c %s "$ws/leak.txt" 2>/dev/null || echo 0)
echo "LEAK $s1 $s2"
ps -eo pid=,args= | grep -E '[s]leep (300|301|600)|[t]ick[.]sh' || true
echo "CGROUPS $(find /sys/fs/cgroup/sdf-turns -mindepth 1 -maxdepth 1 -type d 2>/dev/null | wc -l)"
"""


def _environ(kind: str) -> dict[str, str]:
    e = {"E2B_API_KEY": ENV["E2B_API_KEY"], V.KEY_VAR: ENV[V.KEY_VAR]}
    k = V.KEY_VAR
    if kind == "codex":
        e |= {"SDF_CREDENTIAL_MODE_CODEX": "api-key", "SDF_CODEX_API_KEY_ENV": k,
              "SDF_OPENAI_BASE_URL": f"https://{V.INFERENCE_HOST}/v1", "SDF_CODEX_MODEL": V.MODEL}
    elif kind == "claude":
        e |= {"SDF_CREDENTIAL_MODE_CLAUDE": "api-key", "SDF_CLAUDE_API_KEY_ENV": k,
              "SDF_ANTHROPIC_BASE_URL": f"https://{V.INFERENCE_HOST}", "SDF_CLAUDE_MODEL": V.MODEL}
    else:
        e |= {f"SDF_CREDENTIAL_MODE_{kind.upper()}": "api-key", f"SDF_{kind.upper()}_API_KEY_ENV": k}
    return e


def _kill_agent_script(kind: str) -> str:
    if kind in ("codex", "claude"):
        return f"for p in $(pgrep -x {kind}; pgrep -f 'bin/{kind}( |$)'); do sudo kill -9 $p; done; echo KILLED"
    # Headless: SIGKILL the turn's session leader and the agent processes in
    # its session (not the probe's own shell / sleeps).
    return ("for f in /tmp/sdf-turns/*/sid; do s=$(cat $f); "
            "for p in $(ps -eo pid=,sid=,args= | awk -v s=$s '$2==s' "
            "| grep -vE '^ *[0-9]+ +[0-9]+ +((/bin/)?sleep [0-9]+|sh [^ ]*tick[.]sh.*|sh spawn[.]sh)$' "
            "| awk '{print $1}'); do sudo kill -9 $p; done; done; echo KILLED")


@pytest.mark.parametrize("scenario", ["timeout", "sigkill", "cancel"])
@pytest.mark.parametrize("kind", ["pi", "qwen", "goose", "opencode", "codex", "claude"])
def test_live_turn_leftovers_do_not_survive(kind, scenario, tmp_path, monkeypatch):
    if kind not in KINDS or scenario not in SCENARIOS:
        pytest.skip("not selected")
    headless = kind != "codex" and kind != "claude"
    environ = _environ(kind)
    network = {"allow_out": [V.INFERENCE_HOST], "deny_out": ["0.0.0.0/0"]}
    if kind == "opencode":
        transport, _ = create_credentialed_transport(agents=(kind,), environ=environ, template=TEMPLATE,
                                                     timeout_seconds=900, network=network)
    else:
        monkeypatch.setattr(e2b_transport_module, "_PRODUCTION_SANDBOX_FACTORY", V.BootstrapFactory(kind))
        transport, _ = create_credentialed_transport(agents=(kind,), environ=environ, template=V.BASE_TEMPLATE,
                                                     timeout_seconds=900, network=network)
    real_close = transport.close
    closes: list[str] = []
    transport.close = lambda timeout_ms=30_000: closes.append("deferred")  # type: ignore[method-assign]
    turn_s = 75 if scenario == "timeout" else 400
    if kind == "opencode":
        runtime: Any = OpenCodeHeadlessRuntime(transport=transport, turn_timeout_s=turn_s)
    elif headless:
        runtime = V.HEADLESS[kind](transport=transport, turn_timeout_s=turn_s)
    else:
        args = model_agent_args((kind,), environ)
        if kind == "codex":
            args["codex"] = (*args["codex"], "-c", 'web_search="disabled"')
        runtime = HerdrRuntime(transport=transport, timeout_ms=turn_s * 1000, agent_args=args)
    fixture = tmp_path / "ws"
    fixture.mkdir()
    (fixture / "spawn.sh").write_text(SPAWN)
    (fixture / "tick.sh").write_text(WRITER)
    report: dict[str, Any] = {"kind": kind, "scenario": scenario}
    t0 = time.monotonic()
    box = None
    try:
        runtime.bind_workspace("ATT-CONTAIN", fixture)
        session = runtime.start(attempt_id="ATT-CONTAIN", agent=kind).session_id
        box = transport._ensure_sandbox()
        report["sandbox_id"] = transport.sandbox_id
        result: dict[str, Any] = {}

        def run_turn():
            try:
                result["status"] = runtime.send(session, PROMPTS.get(kind, PROMPT)).status.value
            except Exception as exc:  # noqa: BLE001
                result["error"] = f"{type(exc).__name__}: {str(exc)[-300:]}"

        th = threading.Thread(target=run_turn, daemon=True)
        th.start()
        deadline = time.monotonic() + 240
        spawned = False
        while time.monotonic() < deadline and th.is_alive():
            if box.commands.run(f"test -f {D}/spawned && echo yes || true", timeout=20).stdout.strip() == "yes":
                spawned = True
                break
            time.sleep(2)
        report["spawned"] = spawned
        report["spawned_after_s"] = round(time.monotonic() - t0, 1)
        if spawned:
            time.sleep(3)
            if scenario == "sigkill":
                report["kill"] = box.commands.run(_kill_agent_script(kind), timeout=30).stdout.strip()[-200:]
            elif scenario == "cancel":
                runtime.cancel(session)
                report["cancelled"] = True
        th.join(timeout=turn_s + 120)
        report["turn"] = result
        report["turn_thread_alive"] = th.is_alive()
        if not headless and scenario != "cancel":
            # The ADR-0019 sweep after the turn: leftovers must already be gone
            # (a Herdr turn that outlives its wait stays RUNNING by design, so
            # nothing is swept yet).  Then the caller (adapter / fixture loop)
            # terminates the session, which kills everything.
            report["check_before_terminate"] = box.commands.run(CHECK, timeout=60).stdout.strip().splitlines()
            report["after"] = runtime.terminate(session).status.value
        out = box.commands.run(CHECK, timeout=60).stdout
        report["check"] = out.strip().splitlines()
        if headless:
            report["usage"] = {k: v for k, v in runtime.usage(session).items()
                               if k in ("cost", "input_tokens", "output_tokens", "cache_read_tokens")}
        else:
            try:
                box.files.write("/tmp/sdf-live-records.py", V._RECORDS)
                rec = json.loads(box.commands.run(f"python3 /tmp/sdf-live-records.py {kind}",
                                                  timeout=60).stdout.strip().splitlines()[-1])
            except Exception as exc:  # noqa: BLE001
                rec = {"error": type(exc).__name__}
            report["usage"] = rec
    finally:
        report["wall_s"] = round(time.monotonic() - t0, 1)
        real_close(30_000)
        report["leftover_sandbox"] = bool(report.get("sandbox_id")) and report["sandbox_id"] in V._running_ids()
        REPORT_DIR.mkdir(exist_ok=True)
        (REPORT_DIR / f"contain-{kind}-{scenario}.json").write_text(json.dumps(report, indent=1, default=str))
        print("SDF_CONTAIN_REPORT", json.dumps(report, default=str))

    assert report["spawned"], report
    assert not report["turn_thread_alive"]
    lines = report["check"]
    leak = next(line for line in lines if line.startswith("LEAK")).split()
    assert leak[1] == leak[2], f"workspace still written: {leak}"
    procs = [line for line in lines if not line.startswith(("LEAK", "CGROUPS"))]
    assert procs == [], procs
    if "check_before_terminate" in report and report["turn"].get("status") != "running":
        before = [line for line in report["check_before_terminate"] if not line.startswith(("LEAK", "CGROUPS"))]
        assert before == [], before
    assert next(line for line in lines if line.startswith("CGROUPS")) == "CGROUPS 0", lines
