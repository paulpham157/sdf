"""Static checks for the sdf-herdr-agents E2B template (no network)."""

import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1] / "infra" / "e2b"
AGENTS = ROOT / "herdr-agents"


def _pins() -> dict[str, str]:
    dockerfile = (AGENTS / "Dockerfile").read_text()
    return dict(re.findall(r"^ARG (\w+_VERSION)=(\S+)$", dockerfile, re.MULTILINE))


def test_dockerfile_pins_herdr_codex_claude_and_cursor_to_exact_versions():
    pins = _pins()
    assert set(pins) >= {
        "HERDR_VERSION",
        "CODEX_VERSION",
        "CLAUDE_CODE_VERSION",
        "OPENCODE_VERSION",
        "PI_VERSION",
        "QWEN_CODE_VERSION",
        "GOOSE_VERSION",
        "AIDER_VERSION",
        "CURSOR_AGENT_VERSION",
    }
    for name, version in pins.items():
        if name == "CURSOR_AGENT_VERSION":
            # Cursor lab builds use YYYY.MM.DD-<git-sha>, not npm-style semver.
            assert re.fullmatch(r"\d{4}\.\d{2}\.\d{2}-[0-9a-f]+", version), version
        else:
            assert re.fullmatch(r"\d+\.\d+\.\d+", version), version
    dockerfile = (AGENTS / "Dockerfile").read_text()
    assert re.search(r"^ARG HERDR_SHA256=[0-9a-f]{64}$", dockerfile, re.MULTILINE)
    assert re.search(r"^ARG CURSOR_AGENT_SHA256=[0-9a-f]{64}$", dockerfile, re.MULTILINE)
    assert '"@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}"' in dockerfile
    assert '"@openai/codex@${CODEX_VERSION}"' in dockerfile
    assert '"opencode-ai@${OPENCODE_VERSION}"' in dockerfile
    assert 'test "$(opencode --version)" = "${OPENCODE_VERSION}"' in dockerfile
    assert '"@mariozechner/pi-coding-agent@${PI_VERSION}"' in dockerfile
    # Pi prints its version on stderr.
    assert 'test "$(pi --version 2>&1)" = "${PI_VERSION}"' in dockerfile
    assert '"@qwen-code/qwen-code@${QWEN_CODE_VERSION}"' in dockerfile
    assert 'test "$(qwen --version)" = "${QWEN_CODE_VERSION}"' in dockerfile
    assert re.search(r"^ARG GOOSE_SHA256=[0-9a-f]{64}$", dockerfile, re.MULTILINE)
    assert 'echo "${GOOSE_SHA256}  /tmp/goose.tar.gz" | sha256sum -c -' in dockerfile
    assert "github.com/aaif-goose/goose/releases/download/v${GOOSE_VERSION}/goose-x86_64-unknown-linux-musl.tar.gz" in dockerfile
    assert '"aider-chat==${AIDER_VERSION}"' in dockerfile
    assert 'test "$(aider --version)" = "aider ${AIDER_VERSION}"' in dockerfile
    assert "downloads.cursor.com/lab/${CURSOR_AGENT_VERSION}/linux/x64/agent-cli-package.tar.gz" in dockerfile
    assert 'ln -sf /opt/cursor-agent/cursor-agent /usr/local/bin/agent' in dockerfile
    assert 'ln -sf /opt/cursor-agent/cursor-agent /usr/local/bin/cursor-agent' in dockerfile
    assert 'test "$(agent --version)" = "${CURSOR_AGENT_VERSION}"' in dockerfile
    assert "CURSOR_API_KEY" not in dockerfile


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
        r"ANTHROPIC_API_KEY|ANTHROPIC_BASE_URL|CLAUDE_CODE_OAUTH_TOKEN|OPENAI_API_KEY|CODEX_AUTH_JSON|CURSOR_API_KEY|auth\.json|\.credentials\.json"
    )
    for path in AGENTS.rglob("*"):
        if path.is_file() and path.name != "README.md":
            assert not forbidden.search(path.read_text()), path


def test_codex_only_template_is_retired_by_adr():
    assert not (ROOT / "herdr-codex").exists()
    adr = (ROOT.parents[1] / "docs" / "adr" / "0009-retire-sdf-herdr-codex-template.md").read_text()
    assert "Status: accepted" in adr
    assert "sdf-herdr-agents" in (AGENTS / "README.md").read_text()


def test_opencode_template_pin_matches_runtime_pin():
    from sdf_core.opencode_runtime import OPENCODE_VERSION

    assert _pins()["OPENCODE_VERSION"] == OPENCODE_VERSION


def test_pi_template_pin_matches_runtime_pin():
    from sdf_core.pi_runtime import PI_VERSION

    assert _pins()["PI_VERSION"] == PI_VERSION


def test_headless_runtime_pins_match_the_dockerfile():
    from sdf_core.aider_runtime import AIDER_VERSION
    from sdf_core.goose_runtime import GOOSE_VERSION
    from sdf_core.qwen_runtime import QWEN_VERSION

    pins = _pins()
    assert pins["QWEN_CODE_VERSION"] == QWEN_VERSION
    assert pins["GOOSE_VERSION"] == GOOSE_VERSION
    assert pins["AIDER_VERSION"] == AIDER_VERSION
