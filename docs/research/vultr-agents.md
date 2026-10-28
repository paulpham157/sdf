# Codex, Claude Code, Pi, Qwen Code and Goose on Vultr Serverless Inference (glm-5.3)

Live check: `tests/test_vultr_agents_live.py` (gated on `SDF_LIVE_E2B=1`,
`SDF_LIVE_VULTR_AGENTS=1|codex,claude,pi,qwen,goose`, `E2B_API_KEY`,
`VULTR_INFERENCE_API_KEY`). Same endpoint and model as the OpenCode check.
Aider was an agent kind until 2026-10-28 and was removed by
[ADR 0016](../adr/0016-remove-aider-and-cursor-agent-kinds.md); its results
below are historical.

## Wiring (no proxy needed)

| Agent | How | Settings |
|---|---|---|
| codex 0.157.0 | native, Responses API | `SDF_CREDENTIAL_MODE_CODEX=api-key`, `SDF_CODEX_API_KEY_ENV=VULTR_INFERENCE_API_KEY`, `SDF_OPENAI_BASE_URL=https://api.vultrinference.com/v1`, `SDF_CODEX_MODEL=glm-5.3`, **plus agent arg `-c web_search="disabled"`** |
| claude 2.1.282 | native, Anthropic `/v1/messages` | `SDF_CREDENTIAL_MODE_CLAUDE=api-key`, `SDF_CLAUDE_API_KEY_ENV=VULTR_INFERENCE_API_KEY`, `SDF_ANTHROPIC_BASE_URL=https://api.vultrinference.com`, `SDF_CLAUDE_MODEL=glm-5.3`; user settings `env.ANTHROPIC_DEFAULT_HAIKU_MODEL`/`ANTHROPIC_SMALL_FAST_MODEL=glm-5.3` |
| pi 0.73.1 | the `pi` agent kind ([ADR 0012](../adr/0012-pi-agent-kind.md)), headless `pi -p --mode json` | `SDF_CREDENTIAL_MODE_PI=api-key` only; `SDF_PI_API_KEY_ENV=VULTR_INFERENCE_API_KEY` (default key variable `SDF_PI_API_KEY`); defaults `https://api.vultrinference.com/v1`, `glm-5.3`. Seeded `~/.pi/agent/models.json` provider `sdf`, `api: openai-completions`, `apiKey: "VULTR_INFERENCE_API_KEY"` (variable name) |

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
- Prebuilt `sdf-herdr-agents` template (2026-10-28,
  `SDF_E2B_VULTR_TEMPLATE=sdf-herdr-agents`, no runtime install): all four
  agents pass both criteria on glm-5.3 with egress locked to
  `api.vultrinference.com` and 0 sandboxes left. Codex 48,783 in / 410 out
  ($0.038, 80.4 s); Claude 3 calls, served `glm-5.3` (usage 0/0, see above;
  43.2 s); Pi 4,177 in / 97 out ($0.0034, 22.6 s); OpenCode 20,671 in / 121
  out ($0.016, 17.8 s). Sandbox setup before the Attempt: 15.5-22.2 s,
  against 27.2-27.4 s for the `codex` template plus install.
- On that template Claude Code starts fast enough that Herdr reports it
  interactive a few seconds before it accepts Enter: the prompt sits in the
  composer and Herdr reports `agent_prompt_stalled` (reproduced with a dummy
  key; a manual Enter, or waiting about 8 s, submits it). The Herdr runtime
  now resubmits Enter for Claude as it already did for Codex.
- Qwen Code / Goose / Aider as agent kinds (2026-10-28, runtime install on
  the `codex` template, [ADR 0013](../adr/0013-openai-compatible-headless-agent-kinds.md)):
  all three complete with both criteria PASS, egress locked, 0 sandboxes left.
  Qwen 26,496 in / 371 out ($0.021, 62.1 s, served `glm-5.3`); Goose 7,515 /
  221 ($0.0063, 34.1 s; Goose's own cost estimate $0.0115 is not
  authoritative); Aider 785 / 65 ($0.0008, 78.2 s). Goose and Aider do not
  report the served model. Aider 0.86.2 rejects `--chat-mode code` (it aliases
  `--edit-format`), so write mode omits the flag.
- Rebuilt `sdf-herdr-agents` with Qwen Code / Goose / Aider (2026-10-28,
  build `60e6172b-fe69-4c5e-b81a-b60b28a59c9b`, still public). Verified first
  on an identical private staging build, no runtime install, egress locked,
  0 sandboxes left, all both criteria PASS: Codex 38,977 / 381 ($0.030,
  44.9 s, setup 13.5 s); Claude (usage 0/0, 71.3 s, setup 47.2 s); Pi 4,176
  / 89 ($0.0034, 12.9 s, setup 7.0 s); OpenCode 20,662 / 110 ($0.016,
  16.2 s); Qwen 26,503 / 363 ($0.021, 36.3 s, setup 21.2 s vs 41 s with the
  runtime install); Goose 7,466 / 166 ($0.0061, 24.1 s, setup 18.3 s vs
  28.7 s); Aider 785 / 65 ($0.0008, 31.2 s, setup 16.2 s vs 70.9 s). ADR 0014
  agent-comm (OpenCode lead, Pi workers) succeeded on staging ($0.075,
  58.6 s). Smoke on the public build: Pi ($0.0034, setup 18.2 s) and Qwen
  ($0.021, setup 15.8 s) pass.
- Removed Aider and the Cursor Agent CLI (2026-10-28,
  [ADR 0016](../adr/0016-remove-aider-and-cursor-agent-kinds.md)); rebuilt
  `sdf-herdr-agents` as build `a8d59540-a08d-4352-8c5a-60d90987ad21`, still
  public, rootfs 2.2 GiB (was 4.1 GiB). Verified first on an identical
  private staging build, no runtime install, egress locked, 0 sandboxes left,
  both criteria PASS for all six kinds: Codex 52,338 / 610 ($0.041, 50.9 s);
  Claude (usage 0/0, 39.7 s); Pi 4,176 / 94 ($0.0034, 17.3 s); OpenCode
  27,725 / 173 ($0.022); Qwen 26,417 / 392 ($0.021, 18.9 s); Goose 7,397 /
  196 ($0.0061, 22.6 s). ADR 0014 agent-comm (OpenCode lead, Pi workers)
  succeeded ($0.088, 149.1 s, 13 messages); a first attempt run in parallel
  with the OpenCode test failed at the lead's credential seed step and passed
  when rerun alone. Smoke on the public build: Pi ($0.0034, 23.5 s) and Qwen
  ($0.021, 29.9 s) pass.

Update 2026-10-28: pins moved to codex 0.165.0, claude 2.1.308, opencode
(`@opencode/cli`) 2.2.0 and pi (`@earendil-works`) 1.4.0; see
[ADR 0018](../adr/0018-opencode-2-and-pi-1-upgrade.md). The versions above are
those verified at the time.
