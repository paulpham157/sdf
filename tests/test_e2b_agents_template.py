"""Static checks for the sdf-herdr-agents E2B template (no network)."""

import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1] / "infra" / "e2b"
AGENTS = ROOT / "herdr-agents"


def _pins() -> dict[str, str]:
    dockerfile = (AGENTS / "Dockerfile").read_text()
    return dict(re.findall(r"^ARG (\w+_VERSION)=(\S+)$", dockerfile, re.MULTILINE))


def test_dockerfile_pins_herdr_codex_and_claude_code_to_exact_versions():
    pins = _pins()
    assert set(pins) >= {"HERDR_VERSION", "CODEX_VERSION", "CLAUDE_CODE_VERSION"}
    for version in pins.values():
        assert re.fullmatch(r"\d+\.\d+\.\d+", version), version
    dockerfile = (AGENTS / "Dockerfile").read_text()
    assert re.search(r"^ARG HERDR_SHA256=[0-9a-f]{64}$", dockerfile, re.MULTILINE)
    assert '"@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}"' in dockerfile
    assert '"@openai/codex@${CODEX_VERSION}"' in dockerfile


def test_readme_records_every_pinned_version():
    readme = (AGENTS / "README.md").read_text()
    assert "sdf-herdr-agents" in readme
    for version in _pins().values():
        assert f"`{version}`" in readme


def test_claude_first_run_state_is_baked_without_credentials():
    state = json.loads((AGENTS / "claude" / "claude.json").read_text())
    assert state["hasCompletedOnboarding"] is True
    assert state["bypassPermissionsModeAccepted"] is True
    assert isinstance(state.get("theme"), str) and state["theme"]
    # Herdr opens panes in the home directory; staged workspaces live under /tmp/sdf.
    for trusted in ("/home/user", "/home/user/project", "/workspace", "/tmp/sdf"):
        assert state["projects"][trusted]["hasTrustDialogAccepted"] is True
    settings = json.loads((AGENTS / "claude" / "settings.json").read_text())
    assert settings["skipDangerousModePermissionPrompt"] is True
    for payload in (state, settings):
        text = json.dumps(payload).lower()
        for forbidden in ("apikey", "api_key", "oauth", "token", "customapikeyresponses", "primaryapikey"):
            assert forbidden not in text


def test_template_carries_no_credential():
    forbidden = re.compile(
        r"ANTHROPIC_API_KEY|ANTHROPIC_BASE_URL|CLAUDE_CODE_OAUTH_TOKEN|OPENAI_API_KEY|CODEX_AUTH_JSON|auth\.json|\.credentials\.json"
    )
    for path in AGENTS.rglob("*"):
        if path.is_file() and path.name != "README.md":
            assert not forbidden.search(path.read_text()), path


def test_codex_only_template_is_retired_by_adr():
    assert not (ROOT / "herdr-codex").exists()
    adr = (ROOT.parents[1] / "docs" / "adr" / "0009-retire-sdf-herdr-codex-template.md").read_text()
    assert "Status: accepted" in adr
    assert "sdf-herdr-agents" in (AGENTS / "README.md").read_text()
