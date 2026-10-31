# Changelog

## Unreleased

### Security

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
