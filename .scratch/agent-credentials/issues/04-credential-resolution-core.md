# 04: Credential resolution core: explicit Credential Mode per agent

**What to build:** A pure module turns an agent kind and an environment into a credential plan (Credential Mode, connection id, sandbox variables to set, variable names to strip, seed steps by variable name). A Credential Mode must be declared per agent (`SDF_CREDENTIAL_MODE_CLAUDE`, `SDF_CREDENTIAL_MODE_CODEX`); there is no default and no fallback between modes. In `api-key` mode keys come only from `SDF_ANTHROPIC_API_KEY` / `SDF_OPENAI_API_KEY`; the shell's `ANTHROPIC_*` / `OPENAI_*` are ignored. SDF loads the repo-root `.env` without overriding existing variables.

**Blocked by:** None (can start immediately)

**Status:** ready-for-agent — tracked in https://github.com/paulpham157/sdf/issues/8

Parent spec: `.scratch/agent-credentials/spec.md`; governing ADR-0007.

- [ ] Missing or unknown mode fails with an error naming the variable
- [ ] `api-key` plan for Claude sets `ANTHROPIC_API_KEY`; for Codex sets `OPENAI_API_KEY`; conflicting provider variables are listed for stripping
- [ ] Shell `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`, `OPENAI_API_KEY`, `OPENAI_BASE_URL` never reach a plan
- [ ] No error message contains any secret value (asserted)
- [ ] `.env` loaded with real environment winning; `.env` git-ignored; `.env.example` committed with names only
- [ ] Pure tests only (no network, no E2B)
