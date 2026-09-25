# 08: Codex in `api-key` mode

**What to build:** A Codex agent in the persistent runtime runs on an operator-supplied OpenAI key: `~/.codex/auth.json` is seeded in API-key auth mode from the sandbox variable, and a custom base URL is applied the way Codex 0.155.1 actually honours it.

**Blocked by:** 03, 05, 06

**Status:** ready-for-agent — tracked in https://github.com/paulpham157/sdf/issues/12

Parent spec: `.scratch/agent-credentials/spec.md`; governing ADR-0007.

- [ ] Verified (and noted in the ticket) whether Codex 0.155.1 takes its base URL from an env var or a config key; plan uses that
- [ ] Live-gated wiring test (dummy key): auth.json has API-key auth mode; Codex starts without a login screen; no secret in pane scrollback
- [ ] Live Attempt optional, gated on `SDF_OPENAI_API_KEY`; skipped with explicit reason otherwise
