# Docker Agentic Platform / Cloud Sandboxes as an SDF provider candidate

Researched 2026-10-01 from official Docker docs and product pages, mapped against
SDF’s existing E2B seams (`E2BHerdrTransport`, `E2BContainmentBackend`) and
[ADR-0007](../adr/0007-agent-credentials-injected-into-disposable-sandboxes.md).
No Docker Agentic Platform subscription or PAT was available; **no live API,
CLI cloud, or SDK calls** were made. Daytona (#44/#46) is compared from public
docs only; that implementation worktree was not touched.

**Recommendation: `defer`.** On paper the cloud sandbox lifecycle can cover
create / exec / filesystem / delete, but the Human’s current product priority
is the live agent loop on Daytona, this account has no Agentic Platform access,
the official programmatic SDK is TypeScript-only (SDF is Python), and the
product is explicitly experimental. Do not implement a Docker provider or
change `SDF_SANDBOX_PROVIDER` until that priority shifts and credentials exist
for a bounded POC.

## 1. Product identity

Docker markets two related but distinct surfaces:

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

Press material (e.g. Cloud Sandboxes launch, 2026-09-24) is secondary; prefer
the docs URLs above for API claims.

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
   - CLI CI path: `sbx login` with PAT
     ([CI/headless](https://docs.docker.com/ai/sandboxes/workflows/automation/)).

Agent model-provider keys are **separate** from Docker sign-in: store as cloud
secrets and attach at sandbox creation
([authentication — Authenticate agents](https://docs.docker.com/ai/sandboxes-api/authentication/);
[get a stored secret into a sandbox](https://docs.docker.com/ai/sandboxes-api/cookbook/get-a-stored-secret-into-a-sandbox/)).

**SDF implication:** without a subscription + PAT (or OAuth), there is nothing
to probe. This research makes **no readiness claim**.

## 3. Sandbox lifecycle ↔ SDF seams

### Persistent Herdr — `E2BHerdrTransport`

| SDF operation | E2B today | Docker cloud (docs) |
| --- | --- | --- |
| Create | `Sandbox.create(template=, timeout=, envs=)` | `client.kits.launch` / `client.create` + `waitUntilRunning`; optional `lifecycle.timeoutMs` / `onTimeout` ([create](https://docs.docker.com/ai/sandboxes-api/cookbook/create-your-first-sandbox/), [keep running](https://docs.docker.com/ai/sandboxes-api/cookbook/keep-a-cloud-sandbox-running/)); CLI default TTL **1h**, renewals capped **24h from creation** ([cloud usage](https://docs.docker.com/ai/sandboxes/cloud/usage/)) |
| `run` / `_exec` | `sandbox.commands.run(cmd, timeout=, request_timeout=)`; host timeout kills sandbox | `sandbox.processes.run({ args }, { timeoutMs })` or exec endpoint; exit code is separate from SDK success ([run command](https://docs.docker.com/ai/sandboxes-api/cookbook/run-your-first-command/)). Docs do **not** state that a process timeout automatically deletes the sandbox (E2B Herdr’s fail-closed kill-on-timeout would need explicit adapter policy). |
| `stage_workspace` | FS API `make_dir` / `write_files` (≤8 MiB) | `sandbox.files.mkdir` / `write` / `upload` ([copy file in](https://docs.docker.com/ai/sandboxes-api/cookbook/copy-a-file-into-a-cloud-sandbox/)); CLI `sbx --cloud cp`. No host bind-mount in cloud. |
| `collect_workspace` | `files.list` + `files.read` | `sandbox.files.all` / `read` / `download` ([read files out](https://docs.docker.com/ai/sandboxes-api/cookbook/read-files-out-of-a-sandbox/)) |
| `close` | `sandbox.kill` | `sandbox.delete` (+ `waitUntilDeleted`); optional stop/resume ([delete](https://docs.docker.com/ai/sandboxes-api/cookbook/delete-a-cloud-sandbox/), [cloud usage](https://docs.docker.com/ai/sandboxes/cloud/usage/)). Closing the SDK client does **not** delete the sandbox. |

### One-shot containment — `E2BContainmentBackend`

E2B path: `e2b-box sync` → `exec` → pull → kill via CLI plugin.

Docker analogue on paper: `client.withSandbox(...)` (create → callback →
attempt delete) or `sbx --cloud create` / `exec` / `rm --force`
([create cookbook](https://docs.docker.com/ai/sandboxes-api/cookbook/create-your-first-sandbox/);
[cloud usage](https://docs.docker.com/ai/sandboxes/cloud/usage/)). Still needs a
Python-facing client (REST or wrapped CLI), not the E2B plugin binary.

## 4. SDK / API surface vs seams — gaps

- **Language:** Official SDK is **`@docker/sandboxes` (TypeScript / Node ≥20)**
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
- CLI `--env` / cloud environment files exist
  ([cloud usage](https://docs.docker.com/ai/sandboxes/cloud/usage/);
  [environment files](https://docs.docker.com/ai/sandboxes/configuration/environment-files/)),
  but the first-party agent-auth guidance is secrets-first, not E2B-style
  `envs=`.

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
  volumes, 100 secrets; rate limits also apply
  ([limits](https://docs.docker.com/ai/sandboxes-api/limits/)).

### E2B (public; SDF’s current provider)

- Python SDK already wired; Hobby $0 + usage credits; published per-second
  vCPU/RAM rates ([e2b.dev/pricing](https://e2b.dev/pricing)).
- `envs=` at create matches ADR-0007 as implemented.

### Daytona (public docs only; #44/#46 elsewhere)

- Python (and other) SDKs; API key Bearer auth
  ([daytona docs](https://www.daytona.io/docs/en/),
  [sandboxes](https://www.daytona.io/docs/en/sandboxes/)).
- Pay-as-you-go reserved resources; public marketing lists e.g. ~$0.0858/vCPU/h
  and free credits ([pricing](https://www.daytona.io/pricing),
  [billing](https://www.daytona.io/docs/en/billing/)).
- Human priority is the live agent loop on this track; do not collide with that
  worktree.

| Dimension | Docker cloud | E2B | Daytona |
| --- | --- | --- | --- |
| SDF language fit | REST/CLI glue (TS SDK official) | Python SDK in-tree | Python SDK |
| Credential inject | Secrets attach (envs discouraged for keys) | `envs=` | Env/config via SDK (verify in #44) |
| Auth gate here | No Agentic Platform PAT | Key already used in SDF | Separate track |
| Maturity label | Experimental | Production-used by SDF | In-flight for SDF |
| Local option | Free local microVMs | Cloud-centric | Cloud-centric |

## 7. Recommendation and residuals

**`defer`**

Rationale: lifecycle coverage is plausible on paper, but (1) Human priority is
the Daytona live agent loop, (2) no Agentic Platform credentials → no POC,
(3) TypeScript-first SDK + experimental API raise integration cost versus
providers with Python SDKs, (4) ADR-0007 would need a secrets-vs-envs decision
before implementation.

### Residual risks (if reopened)

- Experimental API churn.
- Process-timeout → sandbox-kill semantics unproven without a live call.
- Secret service types / seed-script compatibility with Herdr agents.
- Dollar rates on the marketing page may diverge from Console billing; recheck
  before any paid POC.
- Concurrent-sandbox quota (default 10) vs Attempt parallelism.

### Reopen when

Human supplies Agentic Platform access **and** asks for a key-gated POC (then
treat as `needs-key-for-POC` / implementation issue), or explicitly reprioritizes
Docker ahead of Daytona.

## Sources (primary)

- [Docker Agentic Platform](https://docs.docker.com/agentic-platform/)
- [Sign up / billing](https://docs.docker.com/agentic-platform/signup/)
- [Docker Sandboxes](https://docs.docker.com/ai/sandboxes/)
- [Cloud sandboxes](https://docs.docker.com/ai/sandboxes/cloud/) / [usage](https://docs.docker.com/ai/sandboxes/cloud/usage/) / [local vs cloud](https://docs.docker.com/ai/sandboxes/cloud/local-vs-cloud/)
- [Sandboxes API & SDK](https://docs.docker.com/ai/sandboxes-api/) / [auth](https://docs.docker.com/ai/sandboxes-api/authentication/) / [limits](https://docs.docker.com/ai/sandboxes-api/limits/) / [concepts](https://docs.docker.com/ai/sandboxes-api/concepts/)
- Cookbook: [create](https://docs.docker.com/ai/sandboxes-api/cookbook/create-your-first-sandbox/), [run](https://docs.docker.com/ai/sandboxes-api/cookbook/run-your-first-command/), [upload](https://docs.docker.com/ai/sandboxes-api/cookbook/copy-a-file-into-a-cloud-sandbox/), [download](https://docs.docker.com/ai/sandboxes-api/cookbook/read-files-out-of-a-sandbox/), [delete](https://docs.docker.com/ai/sandboxes-api/cookbook/delete-a-cloud-sandbox/), [secrets](https://docs.docker.com/ai/sandboxes-api/cookbook/get-a-stored-secret-into-a-sandbox/), [TTL](https://docs.docker.com/ai/sandboxes-api/cookbook/keep-a-cloud-sandbox-running/)
- [Product pricing table](https://www.docker.com/products/docker-sandboxes/)
- [E2B pricing](https://e2b.dev/pricing); [Daytona docs](https://www.daytona.io/docs/en/) / [pricing](https://www.daytona.io/pricing)
- In-repo: `sdf_core/e2b_herdr_transport.py`, `sdf_core/e2b_containment.py`, `docs/adr/0007-agent-credentials-injected-into-disposable-sandboxes.md`
