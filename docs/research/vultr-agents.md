# Codex, Claude Code and Pi on Vultr Serverless Inference (glm-5.3)

Live check: `tests/test_vultr_agents_live.py` (gated on `SDF_LIVE_E2B=1`,
`SDF_LIVE_VULTR_AGENTS=1|codex,claude,pi`, `E2B_API_KEY`,
`VULTR_INFERENCE_API_KEY`). Same endpoint and model as the OpenCode check.

## Wiring (no proxy needed)

| Agent | How | Settings |
|---|---|---|
| codex 0.157.0 | native, Responses API | `SDF_CREDENTIAL_MODE_CODEX=api-key`, `SDF_CODEX_API_KEY_ENV=VULTR_INFERENCE_API_KEY`, `SDF_OPENAI_BASE_URL=https://api.vultrinference.com/v1`, `SDF_CODEX_MODEL=glm-5.3`, **plus agent arg `-c web_search="disabled"`** |
| claude 2.1.282 | native, Anthropic `/v1/messages` | `SDF_CREDENTIAL_MODE_CLAUDE=api-key`, `SDF_CLAUDE_API_KEY_ENV=VULTR_INFERENCE_API_KEY`, `SDF_ANTHROPIC_BASE_URL=https://api.vultrinference.com`, `SDF_CLAUDE_MODEL=glm-5.3`; user settings `env.ANTHROPIC_DEFAULT_HAIKU_MODEL`/`ANTHROPIC_SMALL_FAST_MODEL=glm-5.3` |
| pi 0.73.1 | the `pi` agent kind ([ADR 0012](../adr/0012-pi-agent-kind.md)), headless `pi -p --mode json` | `SDF_CREDENTIAL_MODE_PI=api-key` only; defaults `VULTR_INFERENCE_API_KEY`, `https://api.vultrinference.com/v1`, `glm-5.3`. Seeded `~/.pi/agent/models.json` provider `sdf`, `api: openai-completions`, `apiKey: "VULTR_INFERENCE_API_KEY"` (variable name) |

## Findings

- Vultr's Responses API rejects Codex's hosted `web_search` tool with
  400 `Unsupported tool type`; the first turn fails until it is disabled.
- Vultr's Anthropic endpoint reports `usage` 0/0 when streaming (non-streaming
  reports real counts), so Claude Code transcripts carry no token counts.
  The served model (`message.model`) is reported correctly.
- Unknown model ids fall back silently, so the check reads the served model:
  Claude transcript `message.model`; Pi `responseModel` (set only when the
  served model differs from the requested one); Codex records only the
  requested model in its rollout.
- Workspace config: Claude now starts with `--setting-sources user` and
  Codex with pinned `approval_policy`/`sandbox_mode`/network `-c` overrides.
  Residual for Codex: project `[mcp_servers]` and hooks in a trusted workspace
  still load; containment is the sandbox and the egress allowlist. Pi has no
  way to ignore a project `.pi/`, so the Pi runtime refuses to run when one is
  present (host-side before staging and in the sandbox before every turn).
- Pi as an agent kind (2026-10-28): Attempt completed, both criteria PASS,
  served model `glm-5.3`, 4,180 in / 91 out tokens, $0.0034, 37.4 s,
  egress locked, 0 sandboxes left.
