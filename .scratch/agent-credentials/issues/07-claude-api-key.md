# 07: Claude Code in `api-key` mode, end to end

**What to build:** A Claude Code agent in the persistent runtime runs on an operator-supplied Anthropic key: the key's tail is pre-approved in Claude's `customApiKeyResponses` and the Attempt workspace is pre-trusted at runtime, so the agent answers a prompt without any dialog.

**Blocked by:** 03, 05, 06

**Status:** ready-for-agent — tracked in https://github.com/paulpham157/sdf/issues/11

Parent spec: `.scratch/agent-credentials/spec.md`; governing ADR-0007.

- [ ] Live-gated wiring test (dummy key): seed files have the expected shape; no secret in any Herdr pane scrollback
- [ ] Live Attempt (gated additionally on `SDF_ANTHROPIC_API_KEY`) on the fixture Task: Claude reaches idle, answers a prompt, Runtime Session carries `api-key` metadata; Evaluator Evidence recorded
- [ ] Skipped with explicit reason when the key is absent; sandbox always killed
