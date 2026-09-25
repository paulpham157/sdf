# 10: `subscription` Credential Mode, end to end

**What to build:** An agent configured with `subscription` mode and `SDF_CONNECTION_<AGENT>` uses the operator's host login: Claude gets `CLAUDE_CODE_OAUTH_TOKEN`, Codex gets `CODEX_AUTH_JSON` seeded into `~/.codex/auth.json`. A connection whose expiry is not later than now + sandbox timeout + 10 minutes is refused before sandbox creation with the exact `e2b-box auth connect <agent>` command. A base URL set for a subscription agent is rejected.

**Blocked by:** 03, 06, 09

**Status:** ready-for-agent — tracked in https://github.com/paulpham157/sdf/issues/14

Parent spec: `.scratch/agent-credentials/spec.md`; governing ADR-0007.

- [ ] Pure tests: expiry margin boundary, missing connection name, base URL rejected in subscription mode
- [ ] Live Attempt: Codex with `subscription` (`codex-personal`) on the fixture Task reaches idle, answers a prompt, Runtime Session carries `subscription` metadata and the connection id; Evaluator Evidence recorded
- [ ] No secret in pane scrollback; sandbox always killed
- [ ] Note: `codex-personal` expires 2026-10-03 — reconnect first if running later
