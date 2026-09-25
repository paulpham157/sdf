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
    assert plan.commands[0][4:6] == ("--task", "GOAL.md")
    assert plan.commands[0][-1] == "--json"


def test_live_mode_fails_closed_without_key_or_plugin(tmp_path: Path):
    adapter = HerdrE2BAdapter(environ={})
    plan = adapter.plan(attempt_id="ATTEMPT-001", checkout=tmp_path, template="codex", agent="codex")
    with pytest.raises(E2BAdapterError, match="E2B_API_KEY"):
        adapter.execute(plan, live=True)


def test_live_result_correlates_provider_ids_without_network(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    environments = []

    def runner(command, *_args):
        environments.append(dict(_args[-1]))
        return '{"ok":true,"status":"done","sandboxId":"sb-1","pull":{"ok":true},"herdrSessionId":"hs-1","workspaceId":"w-1"}' if command[1] == "run" else "{}"

    adapter = HerdrE2BAdapter(
        runner=runner,
        environ={"E2B_API_KEY": "<REDACTED>", "E2B_DOMAIN": "e2b.dev"},
    )
    monkeypatch.setattr("sdf_core.herdr_e2b.shutil.which", lambda _: "/usr/local/bin/e2b-box")
    plan = adapter.plan(attempt_id="ATTEMPT-009", checkout=tmp_path, template="codex", agent="codex")
    result = adapter.execute(plan, live=True)
    assert result.sandbox_id == "sb-1"
    assert result.herdr_session_id == "hs-1"
    assert result.workspace_id == "w-1"
    assert result.attempt_id == "ATTEMPT-009"
    assert result.cleanup_attempted is True
    assert result.cleanup_succeeded is True
    assert all(env["E2B_DOMAIN"] == "e2b.dev" for env in environments)


def test_live_failure_still_attempts_cleanup(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    calls: list[tuple[str, ...]] = []

    def runner(command, *_):
        calls.append(tuple(command))
        if command[1] == "run":
            return "not-json"
        return "{}"

    adapter = HerdrE2BAdapter(runner=runner, environ={"E2B_API_KEY": "<REDACTED>"})
    monkeypatch.setattr("sdf_core.herdr_e2b.shutil.which", lambda _: "/usr/local/bin/e2b-box")
    plan = adapter.plan(attempt_id="ATTEMPT-CLEANUP", checkout=tmp_path, template="codex", agent="codex")

    with pytest.raises(E2BAdapterError, match="invalid JSON"):
        adapter.execute(plan, live=True)

    assert [command[1] for command in calls] == ["run", "kill"]
