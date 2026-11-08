# Changelog

## Unreleased

### Changed

- **One local command verifies your repository.** `uv run python -m sdf_core.verify /path/to/your/repository` snapshots that repository, runs an already logged-in Codex or Claude CLI under Landlock, and accepts the Task only from the project's frozen checks. The offline demo (`scripts/demo.py`, `scripts/demo-evidence.sh`, `fixtures/offline-evidence`) is removed. With `SDF_DATABASE_URL` unset, the API and `python -m sdf_core.auth_cli` share one private SQLite file and local mode generates the auth pepper and Evidence MAC key into a mode-0600 file. An explicit database URL, including PostgreSQL, does not turn authentication off.

### Documentation

- **Session import and history audit (ADR 0048, proposed).** `sdf import`
  reads local agent session files the operator names and projects them as
  self-reported Attempts and Claims. It never auto-accepts a Task.
  `sdf audit` reports read-only git, pull request, CI and revert proxies.
  Neither writes verification Evidence. The format survey is
  [`docs/research/session-history-formats.md`](docs/research/session-history-formats.md).

### Fixed

- **Material-decision tests wait for the requester's report before a human resolves (#140).**
  Approving as soon as the request row exists (plus a short sleep) raced the
  coordinator: under load the human could approve before verification was
  requested, so `held` stayed `['pending']` while status was already
  `approved`. The helper now polls until that agent's `done`/`failed` report
  is on the hub, then waits, then resolves.
- **The Core query relay leaves a 0-byte in-progress request for the next poll.**
  A non-atomic drop can appear empty; consuming it deleted the file without
  writing a response, so a bypass of the shim timed out and
  `test_relay_end_to_end_with_real_shim_and_message_visibility` raised
  `KeyError: 'forged'`.

### Security

- **Workspace children are confined when they skip the Tool Proxy (ADR 0004).**
  On Linux, fixture sandbox children and the live adapter's workspace
  subprocesses install a Landlock ruleset (write only the workspace and roots
  the trusted caller names) and drop credential-shaped environment variables
  the Attempt's credential scope did not grant (`sdf_core/process_containment.py`).
- **Evidence rows carry a versioned content MAC (ADR 0043, proposed).**
  Migration `0029_evidence_content_mac` adds `evidence.content_mac`, an
  HMAC-SHA256 over the verdict fields and the bound artifact sha256, stamped
  at insert. Migration `0031_evidence_mac_v2` adds `content_mac_version` and
  extends the payload to the acceptance columns (producer, verifier, class,
  mode, subject/oracle/environment digests, collection and expiry times,
  oracle visibility, and input seed). New inserts are version 2. Existing
  MAC'd rows are backfilled to version 1 and still verify with the original
  payload. A NULL MAC is accepted only when `content_mac_version` is also
  NULL (a row written before 0029). `verify_attempt_revision` recomputes the
  MAC for the stored version and reports a mismatch or a missing MAC on a
  versioned row as `failed`. `SDF_EVIDENCE_MAC_KEY` (at least 32 characters)
  is required to start and to stamp Evidence. A missing or short key is not
  replaced with the public development constant. Format-1 and NULL MACs fail
  verification and are not re-signed. Routing and retry treat a stored PASS
  as success only when the current MAC matches.
- **Verification Evidence is independent of the agent workspace (ADR 0039).**
  Checks run from a sealed oracle copied from the server fixture. Pytest and
  script verdicts are a result frame read by a non-dumpable notary, and subject
  code runs in a separate worker under seccomp. Migration
  `0025_evidence_independence` adds the class, producer, verifier, subject
  digest, oracle and environment digests, `collected_at` and `expires_at`.
  Only a verification `PASS` that passes the acceptance gate accepts an
  artifact; a gated `PASS` is stored as `INCONCLUSIVE`. Linux x86_64 only:
  elsewhere the check exits 86 and is not a pass.
- **Held-out and randomized oracles (ADR 0040).** Checks under `.sdf-held-out/`
  stay on the server and are sealed only at verification. A randomized check
  gets a server seed when it runs. Migration `0026_oracle_visibility` adds
  `oracle_visibility` and `input_seed`. A Task that maps a held-out or
  randomized check is not accepted on a visible `PASS` alone.
- Live-gated pytest (`SDF_LIVE_E2B`, `SDF_LIVE_FULL_LOOP`, `SDF_LIVE_VULTR_AGENTS`)
  no longer inherits the hermetic `SDF_EVALUATOR_CONTAINMENT=off`. Those tests
  force `require` and the promoted E2B containment backend, so evaluator checks
  are not host processes that can read another same-uid `/proc/*/environ`.
  Hermetic tests stay on the host and do not need Docker. Explicit `off` on a
  deployment is still accepted (ADR 0025) and the `evaluator_runs_on_host`
  warning names provider key variables that are set, not their values.
- Python checks resolve top-level imports on the pre-workspace `sys.path`
  snapshot, so agent code that moves the workspace to the front of `sys.path`
  cannot shadow the stdlib or an installed package.
- The full-loop `add-sum` check asserts a range of inputs (negatives, zero,
  larger values) and an `int` result. A single `add(2, 3) == 5` is not enough.
- GitHub Actions are pinned to commit SHAs. `.github/dependabot.yml` watches
  `github-actions` and `uv`.
- Evaluator checks that run in `e2b-box` are not pulled back onto the host.
  `pull` refuses a non-git Attempt workspace unless `--force`, and it would
  copy files the check wrote into the host workspace. `e2b-box exec` exits 1
  when the remote command fails while still printing `{"ok": true, "exitCode":
  ...}`; that exit code is the check result. A check that could not be
  measured stays `INCONCLUSIVE` and records the backend it attempted.
  `SDF_SANDBOX_ROOT` is not forwarded into the box.
- A failed `e2b-box kill` after a measured check no longer discards the
  verdict. Kill is retried; the cleanup error is recorded on the Evidence
  (`cleanup_error`) and logged. Transient connection errors on sync and exec
  are retried a few times, but exec is not retried once it returned measured
  JSON. A sync failure reports stdout, stderr, and the box log's `FAILED:`
  line. The live full-loop leak check also matches evaluator boxes keyed by
  `herdrWorktreeKey`.

### Breaking API changes

- **A keyless `POST /tasks/{id}/run` is handled exactly once (ADR 0041, proposed).**
  A request without `dispatch_key` gets a derived `keyless-<sha256>` key over
  the request fields, so a retry of the same body returns the stored Attempt
  and does not run the adapter again, also after the Attempt is terminal.
  Send a new explicit `dispatch_key` for a new execution.
- **Dead running Attempts are settled (ADR 0041, proposed).** Migration
  `0027_attempt_executor_lease` adds `executor_id` and `executor_heartbeat_at`.
  Startup, shutdown, the lease refresher and `ExecutionService.begin` move a
  `running` Attempt whose lease is dead to `failed` (`error_class` `killed`
  when empty) and its Task to `failed`. Attempt and Task writes in recovery,
  `finish` and cancel are compare-and-swap.
- **Every new Evidence row has a class (ADR 0042, proposed).** Migration
  `0028_evidence_classes` adds `objective_id` and makes `attempt_id` nullable
  for outcome rows. `finish` writes an `execution` row for the diff and
  process log; it never accepts an artifact. A coverage gap is `verification`
  with status `INCONCLUSIVE`. `/scorecard` and `/impact-scorecard` count only
  gated verification `PASS` rows as verified passes and report other `PASS`
  rows separately.
- **Outcome Evidence on `POST /objectives/{id}/evidence` requires an approver (ADR 0042).**
  An operator token is no longer enough; the bar is the same as `POST /decisions`.
  `verifier_identity` must differ from the recording principal and from
  `Attempt.agent` on Attempts under that Objective's Tasks (a direct
  `implements` edge, or a Task of a Plan with that `objective_id`). The body
  must include `mode` (`synthetic` or `live`), stored as `evidence_mode`.
  The row still does not validate a Task, count as a verified pass, or
  satisfy a held-out oracle.

- **LLM credentials use one allow-listed name per value (ADR 0034).**
  Claude reads `ANTHROPIC_BASE_URL`, `ANTHROPIC_API_KEY` and `ANTHROPIC_MODEL`.
  Codex reads `OPENAI_BASE_URL`, `OPENAI_API_KEY` and `OPENAI_MODEL`.
  Pi, Qwen, OpenCode and Goose read `PI_*`, `QWEN_*`, `OPENCODE_*` and
  `GOOSE_*` the same way. `SDF_*_API_KEY`, `SDF_*_BASE_URL`, `SDF_*_MODEL`,
  `*_API_KEY_ENV` and `VULTR_INFERENCE_API_KEY` are not read.
  Unset Claude and Codex base URLs stay the official APIs. Headless kinds
  have no default host and fail closed without `*_BASE_URL` and `*_API_KEY`.
  OpenCode's title model is the same id as `OPENCODE_MODEL` (default
  `vultr/glm-5.3`, a local provider prefix). `SDF_HERDR_CODEX_MODEL` is no
  longer an alias.

- **Artifacts and Evidence are bound to immutable Attempt revisions (#62, ADR 0032).**
  - Migration `0023_attempt_revisions` adds `attempt_revisions` and nullable
    `revision_id` / `tree_sha256` on `artifacts` and `evidence`. Existing rows
    stay `NULL` (`unbound`); nothing is backfilled or rewritten.
  - Every Attempt started by `ExecutionService` records its repository
    identity and base tree. The E2B adapter also records its
    "sdf attempt baseline" commit. `finish` records the result tree before
    any artifact. Tree ids are Git SHA-256-format tree ids of the full
    workspace (`.git` excluded, ignore rules not applied).
  - Behaviour change: `finish` fails the Attempt, writing no artifacts, when
    it has no base revision, when the fixture changed after `begin`, or when
    an adapter-reported result tree does not match.
  - A new artifact/Evidence row with a missing, mismatched, cross-Attempt or
    unknown revision binding is refused by the ORM guards and, on
    PostgreSQL, by insert triggers. Revision rows cannot be deleted, their
    base is immutable and results are set once. An artifact's binding cannot
    be changed after insert (ORM guard and PostgreSQL `BEFORE UPDATE`
    trigger).
  - New column `attempts.revision_required`. It is `NULL` on Attempts that
    existed before 0023 (the durable legacy marker) and forced to `TRUE` on
    every later Attempt (ORM guard; PostgreSQL trigger, whatever the INSERT
    says). It is immutable. Only a `NULL` Attempt may have unbound
    artifacts or Evidence; an unknown Attempt may not. Behaviour change:
    code or tests that write Evidence for an Attempt without a revision
    must now give it one.
  - Behaviour change: after each check the workspace is hashed again. If it
    differs from the revision's result tree, a PASS from that check is
    recorded as `INCONCLUSIVE`. The note is added to its stderr, and
    `tree_after_check_sha256` / `tree_drift` are added to the
    evaluator-output artifact and node metadata. A FAIL stays FAIL. To keep
    ordinary checks from drifting, hardened `python`/`pytest` checks now run
    with `-B`: `-I` ignored `PYTHONDONTWRITEBYTECODE`, so `__pycache__`
    used to be written into the workspace. A check that writes elsewhere in
    the workspace (for example pytest's `tmp_path`, because `TMPDIR` is the
    workspace) is now `INCONCLUSIVE`.
  - New `GET /attempts/{id}/revision` returns the revision and a binding and
    artifact-digest check. `GET /tasks/{id}/trace` adds `revisions`;
    `GET /evidence/{id}` adds `revision_id`, `tree_sha256` and
    `evidence_mode`. Evaluator-output artifacts include the revision.
  - Attempt workspaces now keep the fixture's owner-execute bit (owner
    only).
  - The `repository` identity of a directory outside `SDF_FIXTURES_ROOT` is
    `path-sha256:<16 hex>` instead of the absolute host path.
- **Fixture confinement and error hardening (CodeQL triage).**
  - `POST /plans` now stores the resolved, confined absolute `fixture_dir`.
    Before, it validated the path against `SDF_FIXTURES_ROOT` but stored the
    raw string, and the Coordinator resolved that string against the server's
    working directory: `"."` passed validation (it is the fixtures root) yet
    ran on the server's cwd, and any relative name ran on `<cwd>/<name>`.
  - The Coordinator re-checks the Plan fixture (`sdf_core/fixtures.py`, shared
    with the API) in `create_run` (409) and at execution (failed run, no
    sandbox). Legacy Plan rows with a relative path resolve against the
    fixtures root; legacy absolute paths outside the root are now refused.
  - `fixture_dir` may no longer be the fixtures root itself (400).
  - Copying a fixture that contains a symlink into an Attempt workspace is
    refused (the copy used to follow it and read the host target).
  - Run errors, run-summary violations and Core query errors are redacted
    (`observability.redact`) before they are stored and returned (CodeQL alert 19).
  - `SDF_LOG_FORMAT=text` escapes CR/LF in the message, context ids and extra
    values, so a caller-supplied value cannot forge a log line (CodeQL alert 21).
- **Review follow-ups for #73.**
  - `POST /attempts/{id}/actions` now reports the mode of the `policy_decided`
    row this delivery wrote, looked up by id (no timestamp ties).
    `ToolExecution.audit_records` now also carries the decision record on
    replay and lost-claim returns.
  - On SQLite, migration `0022` runs its table rebuild in a `SAVEPOINT`, so a
    failed verify leaves the database exactly at 0021.
  - Removed a dead `_evidence_mode` copy from the `ReplayAwareAuditSink`
    Protocol.
- **Tool Proxy audits carry the evidence mode (#71, ADR 0031 §9).**
  - Every `tool_audits` row is stamped with its Attempt's `evidence_mode`
    when it is written, so audits from a live `/run` are `live`. Rows
    written before the Attempt's promotion stay `synthetic` (append-only,
    never rewritten). A row for an unknown Attempt is `synthetic`.
  - `POST /attempts/{id}/actions` and the timeline's tool-audit entries now
    include `evidence_mode`.
  - Migration `0022_tool_audit_mode_check` adds `ck_tool_audits_evidence_mode`
    (`synthetic`, `live`). It refuses to run if a stored row holds another
    value, and never rewrites rows. On SQLite it rebuilds the table and
    verifies that every index (including the partial unique fence) and the
    column default are unchanged. On PostgreSQL it reuses an operator's
    `NOT VALID` constraint.
- **Live Evidence mode (Decision #8 option B, ADR 0031).**
  - An Attempt and its Evidence are `evidence_mode = "live"` once the run
    reached a real sandbox. That means E2B or Daytona via the live `/run`
    adapter, or a started plan-run sandbox.
  - Fake runs stay `synthetic`, and so do sandbox-creation failures. A
    failure after the sandbox existed is live.
  - The Attempt is created with its mode (`ExecutionService.begin(evidence_mode=...)`).
    A live `/run` is promoted `synthetic` → `live` once, committed as soon as
    a sandbox id exists. Plan-run Attempts now match their Evidence, so their
    basis is no longer `mixed`.
  - A missing `E2B_API_KEY` / `DAYTONA_API_KEY` is now a `400` raised before
    any Attempt is written.
  - `POST /tasks/{id}/run` returns `evidence_mode`. The timeline's
    `attempt_created` entry and the `sdf.attempt` span (`sdf.evidence_mode`)
    carry it too.
  - Migration `0021_evidence_mode_check` adds a CHECK (`synthetic`, `live`) on
    `attempts` and `evidence`. It refuses to run if a stored row holds another
    value, and never rewrites rows. On PostgreSQL it reuses constraints an
    operator pre-created `NOT VALID`, validating them.
  - Rows written before this change keep their mode.
  - Routing still identifies live samples by the usage-ledger `sandbox` row,
    not by Evidence mode. `live` is not production impact (ADR 0006).
- **Qualified routing (#58, ADR 0030).** `SDF_RUN_AGENT=auto` with
  `SDF_RUN_ROUTE_AGENTS` picks the agent from trusted measurements, and fails
  with `400` when no agent qualifies. Every live `/run` Attempt now gets an
  append-only `routing_decisions` row (migration `0020_routing_decisions`).
  New read endpoints: `GET /attempts/{id}/routing` and
  `GET /routing/qualifications`.
- **Runtime event contract (#57, ADR 0029).**
  - `runtime_events` gains migration `0019_runtime_event_provenance`. `tool_*`
    kinds are accepted only from `source = 'tool-proxy'`, which emits nothing
    else, and rows are append-only. The migration refuses to run if stored rows
    break the rule.
  - The Tool Proxy now records `tool_requested` before `tool_policy_decided`.
  - `GET /attempts/{id}/runtime-events` and `/tasks/{id}/trace` return events
    in the contract's deterministic order, with `category`,
    `trusted_boundary` and `acceptance_evidence` (always `false`). The new
    `GET /attempts/{id}/event-streams` summarises each stream, including gaps.
  - `RuntimeController.recover(attempt_id)` restores a session binding from
    the durable stream. `start` then asks the runtime whether it still has
    that session. If not, a new session id rebinds the Attempt (the new
    stream starts with `runtime_started` carrying `supersedes_session_id`,
    and the old session is retired); the same id is recorded as a new
    `runtime_started` with `replaces_lost_session: true`.
- **Registered checks (#52, ADR 0025).** `POST /tasks/{id}/run` no longer
  accepts `commands`; `criterion_checks` maps criteria to check ids registered
  in the fixture's `sdf-checks.json` or the server checks file. Unknown request
  keys return `422`. Live `/run` and plan-run checks default to
  `SDF_EVALUATOR_CONTAINMENT=require`, so without a promoted containment
  backend they are `INCONCLUSIVE` until one is promoted or containment is set
  to `off` explicitly.
- **Server-measured telemetry (#50, ADR 0027).**
  - `POST /tasks/{id}/run` rejects `cost_usd`, `provider`, `latency_ms`,
    `input_tokens`, `output_tokens` and `escalation_reason` with `422`; the
    server measures or derives them.
  - Retries (`parent_attempt_id`) must name a finished Attempt of the same Task
    that did not pass every check, at a `model_tier` no lower than the parent's
    (default: the parent's tier); otherwise `409`.
  - `POST /plans/{id}/runs` rejects `price_in_per_m`, `price_out_per_m`,
    `sandbox_usd_per_s` and `evidence_mode` in `config` with `422`. Set prices
    with `SDF_RUN_PRICE_IN_PER_M`, `SDF_RUN_PRICE_OUT_PER_M` and
    `SDF_RUN_SANDBOX_USD_PER_S`; an invalid value returns `503`.

### Added

- **One-command offline evidence demo.** `uv run scripts/demo.py` walks the
  public fake-adapter HTTP loop twice: an honest edit that verification
  accepts, and a plausible-but-wrong edit the agent claims succeeded
  (`print('updated ')` plus a weak test rewrite) that SDF rejects. It prints
  a four-beat story (intent, the agent's claim for each task, the independent
  check, and "A accepted / B rejected, though the agent said done") and writes
  a two-column HTML page. `--only accepted|rejected` runs one task. No E2B or
  model key is required. The sealed oracle still needs Linux x86_64 with
  Landlock (ADR 0039); other hosts exit with a pointer to GitHub Codespaces.
  `scripts/demo-evidence.sh` remains as a wrapper. A Codespaces dev container
  lives in `.devcontainer/devcontainer.json`. The demo sets a labelled
  fixture `SDF_EVIDENCE_MAC_KEY` when the variable is unset, so the API can
  start and Task A can be accepted; a shorter user-supplied key is rejected.

- **Human read model and approver inbox (ADR 0044).** Viewer `GET /tasks`,
  `/attempts`, `/plans` and `/objectives` list rows in keyset pages (50 by
  default, at most 100); `GET /tasks/{id}` and `GET /attempts/{id}` read one.
  `GET /inbox` lists the pending decision requests the caller may resolve,
  empty for a principal that cannot approve. Payloads are redacted and omit
  lease columns, dispatch keys and held-out oracle output.
  `GET /decision-requests` is unchanged.
- **Go orchestration core `sdf-orch` (ADR 0035, proposed).** `go/cmd/sdf-orch`
  runs an API gateway, task queue, Attempt dispatcher and in-process message
  bus, with `/healthz` and `/metrics`. Its Tool Proxy idempotency fence keeps a
  local SQLite event log (`-outcome-db` / `SDF_TOOL_OUTCOME_DB`, ADR 0037) and
  stamps each Attempt's evidence mode (ADR 0038). It does not write
  PostgreSQL Attempt state. `.github/workflows/go.yml` runs `go test ./...`
  when `go/**` changes (ADR 0036). See `docs/sdf-orch.md`.
- **Architecture canvas.** `docs/SDF Core Architecture.tldraw` replaces
  `docs/system_architecture.tldraw` with four pages: architecture, Task and
  Attempt lifecycle, the `/run` sequence and the Evidence verification
  pipeline.
- **LLM transports are OpenAI-compatible or Claude Messages only (ADR 0034).**
  Each agent kind maps to one family (`openai` or `anthropic`). Codex requires
  the OpenAI Responses API, not Chat Completions alone. Vultr remains the
  example OpenAI-compatible endpoint (base URL and model strings) and is not a
  protocol. E2B egress and coordinator `attempts.provider` follow the
  configured host (`{protocol}:{host}`), not the literal provider `vultr`.
  `/run` records `{sandbox}:{protocol}:{host}`. Claude with no base URL uses
  `https://api.anthropic.com`. `/run` Codex is given the example OpenAI-compatible
  URL only when the resolved key variable is `VULTR_INFERENCE_API_KEY`;
  `SDF_OPENAI_API_KEY` or any other key name, with no base URL, stays on
  `https://api.openai.com/v1` (the same host the coordinator records).
  `/run` Claude does not take `VULTR_INFERENCE_API_KEY` unless
  `SDF_ANTHROPIC_BASE_URL` is set; otherwise it requires `SDF_ANTHROPIC_API_KEY`
  (or another configured Claude key) and is not given the example key for
  `api.anthropic.com`. Pi's
  seeded model prices are `0`; the ledger estimates `glm-5.3` from
  `PRICES_PER_MTOK` or `SDF_PRICE_GLM_5_3` and marks that cost `estimated`.
  Every agent sandbox declares `credential_environ`; a missing attribute is
  an error, not a silent example-host default. See the ADR for the migration
  notes.
- **Material decision gate (#60, narrow scope, ADR 0033).** An agent can mark a decision request as material: `sdf-msg request --material --consequence TEXT` (and the `material`/`consequence` fields of the MCP `request` tool). A material request needs a non-empty consequence and at least two distinct options. While it is pending, it blocks its scope until a human resolves it: the worker's subtask, or the whole run when the lead asks. Blocked means no agent turns, no verification, no Attempt start or finish for the affected plan tasks (`DecisionGateError`), and no run success. Policies no longer resolve material requests, and `POST /decision-requests/{id}/resolve` with a policy outcome is refused for them. Any human outcome, including `rejected`, lifts the gate and the agent is told the result. Each hold is written to the run summary under `material_decisions`. Decision request API responses now include `material`, `consequence`, `authority` and `blocks`. Non-material requests behave exactly as before. There is no schema change: materiality is stored in the append-only agent message metadata (see the next entry for the PostgreSQL fence).
- **Rule-based materiality and a paused wall clock while held (#60, ADR 0033 amendment).**
  - A decision request is now also material when a deterministic rule in `sdf_core/materiality.py` matches its question, an option, its consequence, or the new optional `action` field (`sdf-msg request-decision --action TEXT`).
  - The rule categories cover deleting data, external sends and publishing, spending money or paid resources, credentials and secrets, destructive git (force-push, pushing to main, history rewrites), schema migrations and shared databases, network egress, and merging.
  - Rules only upgrade a request, never downgrade an agent's declaration.
  - The message metadata, the API (`materiality_source`: `agent`/`rule`/`null`, `matched_rules`) and the run summary record which rules matched. Non-material API responses now also carry these two fields.
  - The host tells an agent when its request became material by rule.
  - While a run only waits on a pending material request (no turn in flight, no host verification), that time no longer counts toward `max_wall_s`. A new run config field, `max_material_hold_s` (default 3600, at most 86400), caps it instead.
  - Run summary `timings` gains `material_hold_s` and `counted_wall_s`, and each `material_decisions` entry gains `pending_s`.
- **Material decision requests cannot be downgraded, and sandboxes survive a hold (#60, ADR 0033 §1c/§3c).**
  - Migration `0024_decision_material_fence` (PostgreSQL trigger `trg_decision_requests_material_fence`; no schema change) and the ORM session guard: a decision request's subject, run, requester, message, question, options and creation time are immutable after insert, and a request whose agent message is material is resolved only by a human, even by a writer that bypasses `PlanService`. Agent messages were already append-only (migration 0014). Downgrade drops the trigger.
  - Behaviour change: code that rewrites one of those decision request fields now gets an error.
  - While a material request is pending, the coordinator extends E2B sandbox timeouts (`Sandbox.set_timeout`) so a sandbox lives for the rest of the run's counted wall time plus a margin. If it cannot extend one, it retries; with 60 s or less left, the run fails with "sandbox of … would expire during a material decision hold …" instead of losing the sandbox. The run summary gains `sandbox_keepalive`. Daytona sandboxes (auto-stop off) and local sandboxes have no provider timeout and are left alone.
  - Fix (found in the live E2B test): E2B caps `set_timeout` at the account's maximum sandbox lifetime without an error. The coordinator now reads the end time back (`get_info`) and uses what was granted; a capped extension is logged in `sandbox_keepalive` (`capped`, `granted_s`), and the run fails cleanly near the cap with "the provider capped its lifetime at …" instead of trusting the requested lifetime. Retried-extension failures now repeat the provider's reason.
