# 06: Create-time Agent Credential injection and Runtime Session metadata

**What to build:** The persistent runtime resolves credential plans for the agent kinds it will start before creating the sandbox, passes their variables as sandbox-wide envs at creation, strips conflicting names, and runs the plans' seed steps once before any agent starts. Each Runtime Session records its agent's Credential Mode and connection id, never the secret. A configuration error means no sandbox is created.

**Blocked by:** 01, 04

**Status:** ready-for-agent — tracked in https://github.com/paulpham157/sdf/issues/10

Parent spec: `.scratch/agent-credentials/spec.md`; governing ADR-0007.

- [ ] Configuration error raised before any sandbox is created
- [ ] Live-gated wiring test on a real sandbox with dummy credential values: expected variable names present, conflicting names absent — compared by name or hash, values never printed
- [ ] Seed commands reference variable names only (no secret value in any command line)
- [ ] `RuntimeSession` gains optional secret-free credential metadata, persisted wherever Runtime Sessions are stored
- [ ] Existing default suite remains provider-free and green
