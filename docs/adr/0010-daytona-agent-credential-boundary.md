# Daytona Agent Credential boundary

Status: accepted

The persistent Herdr runtime may use either the default E2B provider or the
explicitly selected Daytona provider. In both paths, the runtime resolves each
Agent Credential before provisioning and passes its environment variables at
sandbox creation; provider-neutral seed steps then configure agent state from
those sandbox variables (ADR-0007). The credential is therefore available
inside the selected provider's sandbox and is not kept behind a host-side model
proxy. This is a credential-placement decision, not evidence that the two
providers provide equivalent isolation or containment.

ADR-0008 remains specific to a fresh, production-created E2B sandbox. Its
Claude-only `--dangerously-skip-permissions` grant is not part of the Daytona
path. Selecting Daytona, reconnecting to a sandbox, injecting a custom
transport, or using a non-production E2B factory does not authorize that flag.
SDF's host-side policy and Tool Proxy responsibilities remain unchanged.

## Consequences

- E2B remains the default. Daytona is an explicit alternative for the
  persistent Herdr transport and receives credentials at its own sandbox
  creation boundary.
- Daytona must not inherit the permission bypass accepted for the fresh E2B
  path in ADR-0008.
- A missing credential mode or credential fails before provider provisioning;
  resolved credential values are not written into Herdr commands or Evidence.
- Local fake-provider tests establish adapter behavior only. They do not prove
  Daytona containment for public process actions, provider isolation,
  production readiness, or live lifecycle and cleanup behavior. Those remain
  unverified and require separately gated live evidence.
- Provider-specific sandbox guarantees, including the effectiveness of
  Daytona snapshot isolation and deletion after provider/network failures, are
  not established by this decision. SDF makes no equivalence claim between
  providers.
