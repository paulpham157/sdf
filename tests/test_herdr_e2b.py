from pathlib import Path

import pytest

from sdf_core.herdr_e2b import E2BAdapterError, HerdrE2BAdapter


def test_plan_is_deterministic_and_dry_run_has_no_provider_call(tmp_path: Path):
    calls: list[tuple[str, ...]] = []
    adapter = HerdrE2BAdapter(runner=lambda command, *_: calls.append(tuple(command)) or "{}", environ={})
    plan = adapter.plan(attempt_id="ATTEMPT-001", checkout=tmp_path, template="codex", agent="codex", task="GOAL.md", timeout_ms=1234)

    result = adapter.execute(plan)
    assert result.status == "dry-run"
    assert result.attempt_id == "ATTEMPT-001"
    assert calls == []
    assert plan.commands[1][4:6] == ("--task", "GOAL.md")
    assert plan.commands[1][-1] == "--json"


def test_live_mode_fails_closed_without_key_or_plugin(tmp_path: Path):
    adapter = HerdrE2BAdapter(environ={})
    plan = adapter.plan(attempt_id="ATTEMPT-001", checkout=tmp_path, template="codex", agent="codex")
    with pytest.raises(E2BAdapterError, match="E2B_API_KEY"):
        adapter.execute(plan, live=True)


def test_live_result_correlates_provider_ids_without_network(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    adapter = HerdrE2BAdapter(
        runner=lambda command, *_: '{"ok":true,"status":"done","sandboxId":"sb-1","herdrSessionId":"hs-1","workspaceId":"w-1"}' if command[1] == "run" else "{}",
        environ={"E2B_API_KEY": "<REDACTED>"},
    )
    monkeypatch.setattr("sdf_core.herdr_e2b.shutil.which", lambda _: "/usr/local/bin/e2b-box")
    plan = adapter.plan(attempt_id="ATTEMPT-009", checkout=tmp_path, template="codex", agent="codex")
    result = adapter.execute(plan, live=True)
    assert result.sandbox_id == "sb-1"
    assert result.herdr_session_id == "hs-1"
    assert result.workspace_id == "w-1"
    assert result.attempt_id == "ATTEMPT-009"
