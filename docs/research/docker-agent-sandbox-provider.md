# Docker Agentic Platform / Cloud Sandboxes as an SDF provider candidate

Public sources rechecked 2026-10-03; this is a docs-only survey for
[#45](https://github.com/paulpham157/sdf/issues/45), mapped against
SDF’s existing E2B seams (`E2BHerdrTransport`, `E2BContainmentBackend`) and
[ADR-0007](../adr/0007-agent-credentials-injected-into-disposable-sandboxes.md).
No Docker access was supplied for this assignment; **no provider API, CLI
cloud, SDK, account, or credential checks** were made. Public documentation
reads are not runtime verification.

**Recommendation: `defer`.** On paper the cloud sandbox lifecycle can cover
create / exec / filesystem / delete, but the SDK is JavaScript/TypeScript-first
(SDF is Python), the API is experimental, and credential/lifecycle integration
is unproven. No Docker provider or selection change is authorized here.

[#46](https://github.com/paulpham157/sdf/issues/46) explicitly prioritizes E2B
for the live full loop and must not block on Docker research. Accepted main
`d232b7d1da70f63cd8a312a7e94cbc7e61626cb1` already contains a selectable
persistent `DaytonaHerdrTransport` (`SDF_SANDBOX_PROVIDER=daytona`; default
`e2b`). That code presence is not live/full-loop acceptance. The
[2026-09-30 owner disposition](https://github.com/paulpham157/sdf/pull/48#issuecomment-5918847947)
accepted a docs-only **defer** survey, not provider readiness or a Daytona-first
Human priority.

## 1. Product identity

Docker documents three related but distinct surfaces:

| Surface | What it is | Official URLs |
| --- | --- | --- |
| **Docker Agentic Platform** | Experimental Console + pay-as-you-go cloud access for isolated agent sandboxes (kits, secrets, MCP, network policies) | [docs overview](https://docs.docker.com/agentic-platform/), [signup/billing](https://docs.docker.com/agentic-platform/signup/) |
| **Docker Sandboxes** | `sbx` CLI for **local** (free) and **cloud** (metered) microVM sandboxes; same CLI, separate backends | [sandboxes overview](https://docs.docker.com/ai/sandboxes/), [cloud sandboxes](https://docs.docker.com/ai/sandboxes/cloud/), [product page](https://www.docker.com/products/docker-sandboxes/) |
| **Sandboxes API / SDK** | Programmatic create / processes / files / secrets against **cloud** sandboxes | [API & SDK](https://docs.docker.com/ai/sandboxes-api/), [concepts](https://docs.docker.com/ai/sandboxes-api/concepts/), [auth](https://docs.docker.com/ai/sandboxes-api/authentication/) |

This is **not** plain Docker Engine or Compose. Local/cloud sandboxes are
documented as microVM isolation with a private Docker Engine *inside* the
sandbox ([product page](https://www.docker.com/products/docker-sandboxes/);
[architecture](https://docs.docker.com/ai/sandboxes/architecture/)). Cloud mode
uses Docker-managed compute and rejects host workspace mounts
([local vs cloud](https://docs.docker.com/ai/sandboxes/cloud/local-vs-cloud/)).

## 2. Auth model (public; no live calls)

Cloud API/CLI/SDK access requires:

1. An active **Docker Agentic Platform pay-as-you-go** subscription on a personal
   Docker account ([signup](https://docs.docker.com/agentic-platform/signup/)).
2. Authentication as that same account:
   - Interactive: OAuth device flow via `@docker/sandboxes` `oauth()`
     ([authentication](https://docs.docker.com/ai/sandboxes-api/authentication/)).
   - Automation: Docker **PAT with `sandbox:use`** (registry scopes alone are
     insufficient), exchanged at `https://hub.docker.com/v2/auth/token` for a
     short-lived Bearer token used against
     `https://connect.docker.com/sandboxes`
     ([authentication](https://docs.docker.com/ai/sandboxes-api/authentication/)).

Agent model-provider keys are **separate** from Docker sign-in: store as cloud
secrets and attach at sandbox creation
([authentication — Authenticate agents](https://docs.docker.com/ai/sandboxes-api/authentication/);
[get a stored secret into a sandbox](https://docs.docker.com/ai/sandboxes-api/cookbook/get-a-stored-secret-into-a-sandbox/)).

**SDF implication:** provider access would be a prerequisite for a separately
authorized POC, not permission to probe from this research lane. This survey
makes **no readiness claim**.

## 3. Sandbox lifecycle ↔ SDF seams

### Persistent Herdr — `E2BHerdrTransport`

| SDF operation | E2B code seam (not a live guarantee) | Docker cloud (docs) |
| --- | --- | --- |
| Create | `Sandbox.create(template=, timeout=, envs=)` | `client.kits.launch` / `client.create` + `waitUntilRunning`; optional `lifecycle.timeoutMs` / `onTimeout` ([create](https://docs.docker.com/ai/sandboxes-api/cookbook/create-your-first-sandbox/), [keep running](https://docs.docker.com/ai/sandboxes-api/cookbook/keep-a-cloud-sandbox-running/)); CLI default TTL **1h**, renewals capped **24h from creation** ([cloud usage](https://docs.docker.com/ai/sandboxes/cloud/usage/)) |
| `run` / `_exec` | `sandbox.commands.run(cmd, timeout=, request_timeout=)`; timeout path attempts sandbox kill | `sandbox.processes.run({ args }, { timeoutMs })` or exec endpoint; exit code is separate from SDK success ([run command](https://docs.docker.com/ai/sandboxes-api/cookbook/run-your-first-command/)). A process/client timeout is not deletion confirmation; an SDF kill-on-timeout policy would need explicit implementation. |
| `stage_workspace` | FS API `make_dir` / `write_files` (≤8 MiB) | `sandbox.files.mkdir` / `write` / `upload` ([copy file in](https://docs.docker.com/ai/sandboxes-api/cookbook/copy-a-file-into-a-cloud-sandbox/)); CLI `sbx --cloud cp`. No host bind-mount in cloud. |
| `collect_workspace` | `files.list` + `files.read` | `sandbox.files.all` / `read` / `download` ([read files out](https://docs.docker.com/ai/sandboxes-api/cookbook/read-files-out-of-a-sandbox/)) |
| `close` | `sandbox.kill` | `sandbox.delete` (+ `waitUntilDeleted`); optional stop/resume ([delete](https://docs.docker.com/ai/sandboxes-api/cookbook/delete-a-cloud-sandbox/), [cloud usage](https://docs.docker.com/ai/sandboxes/cloud/usage/)). Closing the SDK client does **not** delete the sandbox. |

`waitUntilRunning` does not prove kit setup has finished; a future Herdr image
would need its own readiness check ([concepts](https://docs.docker.com/ai/sandboxes-api/concepts/#wait-for-kit-setup)).

### Provider identity and Attempt lifecycle

**Documented Docker behavior, not live-tested:** the server-assigned resource
name `sandboxes/<uid>` remains stable for its lifetime; `displayName` is a
mutable label, not identity
([resource names](https://docs.docker.com/ai/sandboxes-api/concepts/#resource-names)).
Refresh the same resource to obtain its current endpoint after a connection
change ([endpoint recovery](https://docs.docker.com/ai/sandboxes-api/cookbook/recover-when-the-endpoint-moves/)).
Processes can be found by an application-assigned `session` tag and selected
by saved process name; reconnect output from the last downstream-handled
sequence rather than start a duplicate command. Multiple matches require
disambiguation; no running match does not prove the command never started
([process reconnect](https://docs.docker.com/ai/sandboxes-api/cookbook/find-a-process-you-lost-track-of/)).
Deletion removes processes and sandbox-local files but is asynchronous and can
be refused. Keep the resource name, use a current handle, and confirm deletion;
client closure or wait timeout is not cleanup. Volumes, snapshots, and stored
secrets have separate lifetimes ([delete](https://docs.docker.com/ai/sandboxes-api/cookbook/delete-a-cloud-sandbox/)).

**Proposed SDF mapping — not implemented or verified:** preserve the Attempt
ID as domain execution identity and link its Runtime Session separately.
Persist the Docker resource name as provider binding data before dispatch;
never substitute it for SDF IDs or bind by `displayName`. For long-running work,
also persist the process name, session tag, and downstream output cursor.
After SDF restart, load that binding, refresh the same sandbox, and reconnect
only to the identified process. An ambiguous/missing process requires outcome
reconciliation, not automatic redispatch. Collect Artifacts before deletion;
confirm deletion before closing the Runtime Session and retiring its binding.
Retain identity and expose cleanup uncertainty if deletion fails or times out.
This is a process/output reconnect design, not proof of resumable coding-agent
conversation state, crash recovery, or exactly-once execution.

### One-shot containment — `E2BContainmentBackend`

E2B path: `e2b-box sync` → `exec` → pull → kill via CLI plugin.

Docker analogue on paper: `client.withSandbox(...)` (create → callback →
attempt delete) or `sbx --cloud create` / `exec` / `rm --force`
([create cookbook](https://docs.docker.com/ai/sandboxes-api/cookbook/create-your-first-sandbox/);
[cloud usage](https://docs.docker.com/ai/sandboxes/cloud/usage/)). Still needs a
Python-facing client (REST or wrapped CLI), not the E2B plugin binary.

## 4. SDK / API surface vs seams — gaps

- **Language:** Documented SDK is **`@docker/sandboxes` (JavaScript/TypeScript / Node ≥20)**
  ([install](https://docs.docker.com/ai/sandboxes-api/install/)). REST + OpenAPI
  are documented as the any-language path. SDF’s transports are Python; a
  Docker-backed `HerdrTransport` would wrap HTTP (or `sbx`) itself—more glue
  than E2B’s Python SDK or Daytona’s Python SDK.
- **Image/template:** Kits (`shell`, agent kits) or `imageRef` / prepared image
  resources ([concepts](https://docs.docker.com/ai/sandboxes-api/concepts/)), not
  E2B template strings. An SDF Herdr+agents image would need a kit or registry
  image story.
- **Process model:** Argv arrays and explicit timeouts map cleanly to `_exec`;
  kill-on-command-timeout behavior is an SDF policy choice, not documented as
  platform default.
- **Filesystem:** Upload/list/read/download map to stage/collect; unbounded
  trees need SDF-side size/depth guards (as with E2B’s 8 MiB / depth limits).
- **Experimental:** Docs mark Agentic Platform and Sandboxes API/SDK as
  experimental; interfaces may change
  ([API overview](https://docs.docker.com/ai/sandboxes-api/)).
- **Local `sbx`:** Free local microVMs are a different product surface (host
  mounts, local secret store). SDF’s cloud-disposable Attempt model aligns with
  **cloud** sandboxes, not local Desktop/Engine.

## 5. ADR-0007 credential fit

ADR-0007: resolve credentials before create; inject as sandbox-wide **envs** at
create; fail closed; never put secrets in panes/Evidence; strip conflicting
provider vars.

Docker’s documented cloud path:

- Prefer **stored secrets** + `storage: { secrets: [...] }` at create; docs say
  keep provider keys out of command args, source files, and **plain environment
  variables** inside the sandbox
  ([authentication](https://docs.docker.com/ai/sandboxes-api/authentication/);
  [secret attach cookbook](https://docs.docker.com/ai/sandboxes-api/cookbook/get-a-stored-secret-into-a-sandbox/)).
- Fail-closed before create still fits.
- Secret metadata responses omit token values (aligned with Evidence hygiene).
- CLI `--env` exists ([cloud usage](https://docs.docker.com/ai/sandboxes/cloud/usage/)),
  but the agent-auth guidance is secrets-first, not E2B-style `envs=`.

**Paper verdict:** intent (create-time injection, no secrets in panes) aligns;
the **mechanism** differs from ADR-0007’s E2B `Sandbox.create(envs=)` and from
SDF seed scripts that read named `process.env` variables. A Docker adapter
would need an ADR amendment or a proven env-equivalent path before claiming
ADR-0007 compliance.

## 6. Pricing / quotas and gaps vs E2B / Daytona

### Docker (public)

- Local `sbx` compute: free ([sandboxes overview](https://docs.docker.com/ai/sandboxes/)).
- Cloud: pay-as-you-go, metered **by the second** from CPU/memory size; no
  recurring Agentic Platform fee; inference billed by model provider
  ([signup billing](https://docs.docker.com/agentic-platform/signup/)).
- Product page publishes cloud rates (verify in Console before spend):

  | Size | vCPUs | Memory | Per hour |
  | --- | --- | --- | --- |
  | Micro | 1 | 2 GiB | $0.07 |
  | Small | 2 | 4 GiB | $0.14 |
  | Medium | 4 | 8 GiB | $0.28 |
  | Large | 8 | 16 GiB | $0.56 |
  | XL | 16 | 32 GiB | $1.12 |

  Source: [docker.com/products/docker-sandboxes](https://www.docker.com/products/docker-sandboxes/).
- Default account quotas (API docs): 10 concurrent sandboxes, 50 stored, 100
  volumes, 100 secrets; account-specific quotas may differ and rate limits apply
  ([limits](https://docs.docker.com/ai/sandboxes-api/limits/)).

### E2B (SDF default; public pricing)

- Python SDK already wired; Hobby is free plus usage, with $100 one-time usage
  credit; the public guide lists $0.000014/s for 1 vCPU
  ([pricing](https://e2b.dev/pricing)). This is not an all-in cost comparison.
- `envs=` at create matches ADR-0007 as implemented.

### Daytona (selectable in accepted main; no live claim)

- Python and other SDKs with API-key configuration
  ([docs](https://www.daytona.io/docs/en/)). Public pricing lists $0.0504/vCPU/h
  and $0.0162/GiB/h memory, calculated per second; $0.0858/vCPU/h is the
  **Windows** rate, not the general compute rate
  ([pricing](https://www.daytona.io/pricing)). Storage and plan terms differ;
  recheck before spend. No Daytona code or worktree is changed by this survey.

| Dimension | Docker cloud | E2B | Daytona |
| --- | --- | --- | --- |
| SDF language fit | REST/CLI glue (TS SDK official) | Python SDK in-tree | Python SDK |
| Credential inject | Secrets attach (envs discouraged for keys) | `envs=` per ADR-0007 | Create-time env injection in accepted-main transport |
| SDF status | Research only; no access checks | Default persistent transport | Selectable persistent transport |
| Evidence here | Experimental public docs | Static repo seam only | Static accepted-main seam only |
| Local option | Free local microVMs | Cloud-centric | Cloud-centric |

## 7. Recommendation and residuals

**`defer`**

Rationale: lifecycle coverage is plausible on paper, but JavaScript/TypeScript-
first SDK and experimental API increase Python integration work; ADR-0007
requires a secrets-vs-envs decision; durable binding, reconnect, and confirmed
cleanup remain proposals. Docker stays research-only and non-blocking for
E2B-first #46 or the existing Daytona option.

### Residual risks (if reopened)

- Experimental API churn.
- Process-timeout → sandbox-kill semantics unproven without a live call.
- Durable Attempt/Runtime Session binding, reconnect ambiguity, and cleanup
  failure handling unimplemented for Docker.
- Secret service types / seed-script compatibility with Herdr agents.
- Dollar rates on the marketing page may diverge from Console billing; recheck
  before any paid POC.
- Concurrent-sandbox quota (default 10) vs Attempt parallelism.

### Reopen when

Only on a separate, explicitly scoped and authorized POC with supplied access,
bounded cost, credential-policy decision, identity/reconnect/cleanup acceptance
criteria, and a named owner. Access alone or this docs acceptance does not
authorize implementation, live calls, or a provider-selection change. No
follow-up issue or new triage label is created here.

## Sources (primary)

- [Docker Agentic Platform](https://docs.docker.com/agentic-platform/)
- [Sign up / billing](https://docs.docker.com/agentic-platform/signup/)
- [Docker Sandboxes](https://docs.docker.com/ai/sandboxes/)
- [Cloud sandboxes](https://docs.docker.com/ai/sandboxes/cloud/) / [usage](https://docs.docker.com/ai/sandboxes/cloud/usage/) / [local vs cloud](https://docs.docker.com/ai/sandboxes/cloud/local-vs-cloud/)
- [Sandboxes API & SDK](https://docs.docker.com/ai/sandboxes-api/) / [auth](https://docs.docker.com/ai/sandboxes-api/authentication/) / [limits](https://docs.docker.com/ai/sandboxes-api/limits/) / [concepts](https://docs.docker.com/ai/sandboxes-api/concepts/)
- Cookbook: [create](https://docs.docker.com/ai/sandboxes-api/cookbook/create-your-first-sandbox/), [run](https://docs.docker.com/ai/sandboxes-api/cookbook/run-your-first-command/), [upload](https://docs.docker.com/ai/sandboxes-api/cookbook/copy-a-file-into-a-cloud-sandbox/), [download](https://docs.docker.com/ai/sandboxes-api/cookbook/read-files-out-of-a-sandbox/), [delete](https://docs.docker.com/ai/sandboxes-api/cookbook/delete-a-cloud-sandbox/), [secrets](https://docs.docker.com/ai/sandboxes-api/cookbook/get-a-stored-secret-into-a-sandbox/), [TTL](https://docs.docker.com/ai/sandboxes-api/cookbook/keep-a-cloud-sandbox-running/)
- Identity/reconnect: [resource names](https://docs.docker.com/ai/sandboxes-api/concepts/#resource-names), [endpoint recovery](https://docs.docker.com/ai/sandboxes-api/cookbook/recover-when-the-endpoint-moves/), [process reconnect](https://docs.docker.com/ai/sandboxes-api/cookbook/find-a-process-you-lost-track-of/)
- [Product pricing table](https://www.docker.com/products/docker-sandboxes/)
- [E2B pricing](https://e2b.dev/pricing); [Daytona docs](https://www.daytona.io/docs/en/) / [pricing](https://www.daytona.io/pricing)
- In-repo: `CONTEXT.md`, `sdf_core/e2b_herdr_transport.py`, `sdf_core/e2b_containment.py`, ADR-0007; accepted-main `sdf_core/credential_injection.py` and `sdf_core/daytona_herdr_transport.py` at the SHA above. Code presence is not live verification.
