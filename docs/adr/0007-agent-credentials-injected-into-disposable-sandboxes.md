# Agent Credentials are injected into disposable sandboxes

Status: accepted

The persistent Herdr runtime gives each agent its Agent Credential as
sandbox-wide environment variables at sandbox creation (E2B Python SDK
`Sandbox.create(envs=)`), rather than routing model calls through a host-side
proxy that keeps the secret out of the box. We accept that the credential lives
inside a disposable, short-lived sandbox in exchange for a single operator login
and no public gateway to run. Each agent kind is configured with an explicit
Credential Mode — `subscription` (a herdr-e2b plugin connection, resolved by the
plugin's own code so it stays the single owner of the connection format) or
`api-key` (dedicated `SDF_*` host variables, never the shell's
`ANTHROPIC_API_KEY`/`ANTHROPIC_BASE_URL`, which may point at a host-local
proxy). There is no default and no silent fallback between modes: a missing
mode or credential fails before any sandbox is created.

## Consequences

- Only one credential per agent kind is present in a box; conflicting provider
  variables are stripped.
- A custom provider base URL is allowed only in `api-key` mode and loopback or
  private addresses are rejected, since a cloud sandbox cannot reach them.
- A `subscription` credential must outlive the sandbox (expiry later than now +
  sandbox timeout + 10 minutes) or creation is refused with a reconnect hint.
- Agent config is seeded by variable name only; secrets never appear in a
  Herdr pane, command line or Evidence. The Runtime Session records the mode
  and connection id, never the secret.
- The headless `e2b-box run` path keeps the plugin's own credential selection.
