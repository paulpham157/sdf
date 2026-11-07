# ADR 0044: a human read model and an approver inbox over the rows SDF already stores

Status: accepted (maintainer, 2026-11-06, PR #124)

Numbered 0044 because [ADR 0043](0043-evidence-content-mac.md) is the
evidence content MAC. First step of the human-interaction workstream. Does
not add a table, a role, or an event kind. Read paths stay reads. Later
steps are [ADR 0045](0045-human-actions.md),
[ADR 0046](0046-live-updates.md), and
[ADR 0047](0047-console-ui.md). Sequencing is at the end of this file.

## Context

A person who holds a token can already call the mutating gates, and can
already read a single plan, a single run, a run's messages, and the full
decision-request list. They cannot list the work those gates are about.

The HTTP surface is the 38 handlers registered on `app` in
`sdf_core/api.py`, from `GET /healthz` (`sdf_core/api.py:286`) through
`POST /decision-requests/{request_id}/resolve` (`sdf_core/api.py:1513`).
`FastAPI(..., dependencies=[Depends(authorize)])` runs `authorize` for every
one of them (`sdf_core/api.py:257`). `PUBLIC_ROUTES` contains only
`GET /healthz` (`sdf_core/auth.py:302`, `sdf_core/api.py:157-159`).
`/docs` and `/redoc` are off, and `/openapi.json` is a normal route
(`sdf_core/api.py:258`, `sdf_core/api.py:297`; ADR 0015).

Roles are `viewer`, `operator`, `approver`, and `admin`
(`sdf_core/auth.py:44-49`). `operator` implies `viewer`. `approver` implies
`viewer` and does not imply `operator`. `admin` implies all four. A service
token cannot hold `approver` or `admin` (`sdf_core/auth.py:51-52`,
`sdf_core/auth.py:176-177`). `Principal.can_approve` is true only when the
principal has `approver`, the token kind is `human`, and the name is not
reserved (`sdf_core/auth.py:95-97`). `authorize` rejects an approver route
when `can_approve` is false (`sdf_core/api.py:165-167`).

Safe methods that are not listed in `ROUTE_ROLES` require `viewer`. Any
other unlisted method requires `admin` (`sdf_core/auth.py:305-335`).
`authorize` writes an `AuthAuditRow` with `event="mutation"` only after a
non-safe method. A safe method returns before that write
(`sdf_core/api.py:169-186`). `auth_audit` is append-only
(`sdf_core/db.py:897-910`; ADR 0015).

What a human can already change:

- `POST /plans/{plan_id}/approve` and `POST /plans/{plan_id}/reject`
  (`sdf_core/api.py:1346-1352`). The decider is the principal. A proposer
  cannot approve their own plan unless `SDF_AUTH_SINGLE_USER` is set.
  Rejecting one's own plan is allowed. The comparison is case-insensitive
  (`sdf_core/plans.py:197-217`, `sdf_core/api.py:1327-1332`; ADR 0015).
- `POST /decision-requests/{request_id}/resolve` (`sdf_core/api.py:1513`).
  Plan requests go through `decide_plan`. A material agent request is
  resolved only by a human (ADR 0033, `sdf_core/plans.py:312-337`).
- `POST /runs/{run_id}/cancel` (`sdf_core/api.py:1475`). This is an
  operator route (`sdf_core/auth.py:321`).

What a human can already read, one id at a time:

- `GET /plans/{plan_id}` (`sdf_core/api.py:1319`).
- `GET /runs/{run_id}`, `GET /runs/{run_id}/messages`, and
  `GET /runs/{run_id}/timeline` (`sdf_core/api.py:1467`,
  `sdf_core/api.py:1489`, `sdf_core/api.py:958`).
- `GET /decision-requests`, the whole table, optionally filtered by
  `status_filter` and `run_id`, ordered by `created_at`, returned as a JSON
  array (`sdf_core/api.py:1502`). The handler does not look at
  `can_approve` or at who proposed the plan.
- `GET /tasks/{task_id}/trace`, `GET /attempts/{attempt_id}/revision`,
  `GET /attempts/{attempt_id}/runtime-events`,
  `GET /attempts/{attempt_id}/event-streams`, and
  `GET /evidence/{evidence_id}` (`sdf_core/api.py:702`, `sdf_core/api.py:785`,
  `sdf_core/api.py:801`, `sdf_core/api.py:809`, `sdf_core/api.py:777`).

There is no `GET /tasks`, `GET /attempts`, `GET /plans`, `GET /objectives`,
`GET /tasks/{task_id}`, or `GET /attempts/{attempt_id}`. Objectives exist as
`GraphNodeRow` rows with `kind="objective"` (`sdf_core/api.py:626-639`).
Tasks exist as `TaskRow` (`sdf_core/db.py:53-60`). Attempts exist as
`AttemptRow` (`sdf_core/db.py:67-105`). Plans exist as `PlanRow`
(`sdf_core/db.py:372-387`).

`GET /runs/{run_id}/timeline` is already a read model. `build_timeline`
merges stored rows and returns them. It does not insert
(`sdf_core/timeline.py:1-10`, `sdf_core/timeline.py:90-91`). Every entry
passes its fields through `redact_value` (`sdf_core/timeline.py:46-47`).
Message bodies on that view are redacted and cut to 400 characters.
The full bodies stay on `GET /runs/{run_id}/messages`
(`sdf_core/timeline.py:8-10`, `sdf_core/timeline.py:36`,
`sdf_core/timeline.py:121`, `sdf_core/api.py:1495-1499`). Redaction masks
bearer tokens and known secret shapes (`sdf_core/observability.py:10-13`;
ADR 0023). Auth-audit entries on that timeline already carry `principal`
(`sdf_core/timeline.py:84-86`, `sdf_core/timeline.py:100-103`).

Held-out oracle bytes are a different rule from actor binding. ADR 0040
(migration `0026_oracle_visibility`) stores visibility and a seed, replaces
held-out stdout and stderr with a fixed withheld note, and does not put
the withheld output on API responses (`docs/adr/0040-held-out-oracles.md`).
ADR 0026 is the Tool Proxy actor and `attempts.principal` binding
(`sdf_core/db.py:92-95`). The migration number 0026 is not ADR 0026.

Runtime events already have a per-stream `sequence` and report missing
sequence numbers as `gaps`. A stream is `(source, attempt_id, session_id)`.
Nothing is synthesised to fill a gap (ADR 0029, `sdf_core/events.py:1-28`,
`sdf_core/events.py:189-206`). This ADR does not change that contract.
Live delivery of new rows is [ADR 0046](0046-live-updates.md).

There is no browser client in this repository, no `CORSMiddleware`, and no
SSE or WebSocket handler. The console that will call these reads is
[ADR 0047](0047-console-ui.md).

## Decision

Add read routes on the existing tables. Do not add a projection table, a
cache, or a role.

The owner accepted these choices in the review of PR #124: page size 50 and
cap 100; a proposer's own plan is omitted from the inbox, and
`POST /plans/{id}/reject` still accepts that proposer; a cursor envelope
for `GET /decision-requests` comes later and is opt-in, so the default
stays a JSON array; embedded message bodies are capped at 400 characters,
with full bodies still on `GET /runs/{id}/messages`.

1. **Collections.** These routes require `viewer`, which is the default for
   a safe method (`sdf_core/auth.py:331-335`).

   | Route | Rows | Order |
   | --- | --- | --- |
   | `GET /tasks` | `TaskRow` | `created_at`, then `id` |
   | `GET /attempts` | `AttemptRow` | `created_at`, then `id` |
   | `GET /plans` | `PlanRow` | `created_at`, then `id` |
   | `GET /objectives` | `GraphNodeRow` with `kind="objective"` | `created_at`, then `id` |

   `GET /plans` accepts `status`. The value must be `proposed`, `approved`,
   or `rejected` (`sdf_core/db.py:376`, `sdf_core/plans.py:153`,
   `sdf_core/plans.py:239`). Any other value is 422. Omitting `status`
   returns every plan.

   The response is `{"items": [...], "next_cursor": "<opaque>" | null}`.
   `limit` defaults to 50 and is at most 100. A larger `limit` is 422.
   There is no `offset`. The cursor is the last item's `(created_at, id)`
   for that route, opaque to the client. The next page is the rows strictly
   after that pair in the order above. A cursor the server cannot decode is
   422. A filter that matches nothing is `{"items": [], "next_cursor": null}`,
   including the first page.

   Collection fields, and no others:

   | Route | Fields |
   | --- | --- |
   | `GET /tasks` | `id`, `title`, `status`, `acceptance_criteria`, `created_at` |
   | `GET /attempts` | `id`, `task_id`, `status`, `agent`, `parent_attempt_id`, `model_tier`, `cost_usd`, `provider`, `latency_ms`, `input_tokens`, `output_tokens`, `escalation_reason`, `created_at`, `model_profile`, `outcome`, `evidence_mode`, `error_class`, `trace_id`, `principal` |
   | `GET /plans` | `id`, `title`, `status`, `objective_id`, `proposed_by`, `content_sha256`, `created_at`, `decision_request_id` |
   | `GET /objectives` | `id`, `title`, `owner`, `source`, `created_at` |

   `owner` on an objective is the domain owner stored on the graph node
   (`sdf_core/db.py:31`, `sdf_core/api.py:639`). ADR 0015 treats that field
   as domain data, not as the caller.

   Omitted from every collection and every detail in this ADR:
   `tasks.idempotency_key` (`sdf_core/db.py:58`), `attempts.dispatch_key`
   (`sdf_core/db.py:73`), and the lease columns `executor_id` and
   `executor_heartbeat_at` (`sdf_core/db.py:104-105`).

2. **Single resources.**

   `GET /tasks/{task_id}` fields: `id`, `title`, `status`,
   `acceptance_criteria`, `created_at`, and `attempt_ids`. The attempt ids
   are in the same order as `GET /attempts`. `idempotency_key` is absent.

   `GET /attempts/{attempt_id}` fields are the `GET /attempts` fields.
   `dispatch_key`, `executor_id`, and `executor_heartbeat_at` are absent.
   `principal` is the authenticated starter stored on the row
   (`sdf_core/db.py:92-95`; ADR 0026). That name is already on timeline
   auth-audit entries (`sdf_core/timeline.py:84-86`,
   `sdf_core/timeline.py:100-103`). Putting it on this payload does not
   reveal a principal the timeline hides.

   An unknown id is 404. Both payloads pass string fields through
   `redact_value` before they are returned, the same function the timeline
   uses (`sdf_core/timeline.py:46-47`).

   These payloads do not include held-out fixture bytes and do not include
   withheld stdout or stderr. ADR 0040 already keeps that output off API
   responses and replaces it with a fixed note. The new routes do not add
   a field that carries the original bytes, the fixture tree under
   `.sdf-held-out/`, or the stdout and stderr the evaluator withheld.
   They also do not inline Evidence `command` text.

3. **Inbox.** `GET /inbox` is a filtered read of `DecisionRequestRow` and
   the plan that a plan request points at. It does not have its own table.
   Every proposed plan already has one pending `DecisionRequestRow`
   (`sdf_core/plans.py:162-165`).

   An item is included only when all of the following hold:

   - the request status is `pending`;
   - `Principal.can_approve` is true for the caller;
   - for `subject_kind="plan"`, the caller's name does not casefold-equal
     `PlanRow.proposed_by`, unless `single_user_mode()` is true. That is the
     same comparison `decide_plan` uses to refuse self-approval
     (`sdf_core/plans.py:213-217`, `sdf_core/auth.py:140-141`).

   A viewer, an operator, a service token, or a reserved name gets
   `{"items": [], "next_cursor": null}`. They do not get 403, because
   `GET /auth/whoami` already reports `can_approve`
   (`sdf_core/api.py:291`). A proposer does not see their own plan in
   this list. Rejecting one's own plan stays on
   `POST /plans/{plan_id}/reject`, which ADR 0015 already allows. The inbox
   is the set of requests the caller may approve or resolve.

   Each item has these fields: `id`, `subject_kind`, `subject_id`,
   `run_id`, `question`, `options`, `status`, `requested_by`,
   `resolved_by`, `resolver_kind`, `resolution`, `decision_node_id`,
   `created_at`, `resolved_at` (the payload from
   `decision_request_payload`, `sdf_core/plans.py:358-367`), plus
   `material`, `materiality_source`, `matched_rules`, and when material
   `consequence`, `authority`, and `blocks` (`sdf_core/decision_gate.py:124-137`),
   plus `actionable` set to true. A plan item also has `proposed_by` and
   `content_sha256`. The same cursor rule as the collections applies,
   ordered by `DecisionRequestRow.created_at`, then `id`.

   `GET /decision-requests` stays the unfiltered JSON array it is today
   (`sdf_core/api.py:1502`). This ADR does not paginate it. A later change
   may add an opt-in cursor query. The default response stays the array.

4. **Redaction.** Collection items, detail payloads, and inbox items run
   their string fields through `redact_value`. Where a message body is
   embedded, it is cut to 400 characters and then redacted, matching
   `_BODY_CHARS` on the timeline (`sdf_core/timeline.py:36`,
   `sdf_core/timeline.py:121`). `GET /runs/{run_id}/messages` is unchanged
   and remains the full-body read.

5. **Reads do not mutate domain rows.** Handlers for the routes in this ADR
   do not `add`, update, or delete tasks, attempts, plans, objectives,
   decision requests, evidence, messages, or audit rows. The existing
   `last_used_at` commit inside `_authenticate` stays
   (`sdf_core/api.py:149`). It is token bookkeeping from ADR 0015, and it
   already happens for every authenticated request, including today's GETs.

`GET /tasks/{task_id}/trace` and the existing attempt sub-resources stay
as they are. The new detail routes do not replace them. `GET /plans/{plan_id}`
stays `plan_payload` (`sdf_core/plans.py:245-255`).

## Invariants

- Every new route is a safe method. It is classified as `viewer` by the
  default in `required_role`, or listed explicitly as `viewer`. It is not
  added to `PUBLIC_ROUTES`.
- A response never includes a row or a field that the caller's role cannot
  read through the timeline or through an existing GET of the same row.
  The inbox is narrower than that: a caller who fails `can_approve`, or who
  proposed the plan and is not in single-user mode, does not receive that
  plan as an inbox item.
- String fields are redacted with `redact_value`. Embedded message bodies
  are truncated to 400 characters and then redacted.
- No payload in this ADR contains `idempotency_key`, `dispatch_key`,
  `executor_id`, or `executor_heartbeat_at`.
- No payload in this ADR contains held-out fixture bytes, withheld stdout,
  or withheld stderr (ADR 0040).
- A handler in this ADR does not insert or update a domain row. A test can
  count rows before and after a GET and see the same counts, apart from
  `api_tokens.last_used_at`, which ADR 0015 already updates.
- Page order is `created_at` ascending, then `id` ascending. `limit` is
  50 unless the client sends a smaller number, and it is never above 100.
  Repeating a request with the same cursor returns the same next page when
  no row has been inserted inside that key range.
- `GET /decision-requests` remains an unfiltered JSON array.

## Proof

Tests to add. Local SQLite, the same `client_as` helper as
`tests/test_api_auth.py`. No live provider.

- `tests/test_human_read_model.py::test_collections_are_viewer_gets_and_stay_off_the_public_set`
  builds the route table the way `test_route_table_is_classified` does and
  requires each new path to be absent from `PUBLIC_ROUTES` and to require
  `viewer`.
- `tests/test_human_read_model.py::test_collections_paginate_in_created_at_then_id_order`
  inserts three rows with two sharing a timestamp and walks `limit=1` until
  `next_cursor` is null. The ids come back in `(created_at, id)` order.
  A bad cursor and `limit=101` are 422. The default page size is 50.
- `tests/test_human_read_model.py::test_plan_status_filter_is_the_plan_status_set`
  uses `proposed`, `approved`, and `rejected`, and requires 422 for
  `status=pending`.
- `tests/test_human_read_model.py::test_task_and_attempt_detail_round_trip`
  creates a task and an attempt through the existing services and reads them
  back. The payloads contain only the fields listed above. They omit
  `idempotency_key`, `dispatch_key`, `executor_id`, and
  `executor_heartbeat_at`. Unknown ids are 404. `principal` on the attempt
  equals the timeline auth-audit `principal` for the run that started it.
- `tests/test_human_read_model.py::test_read_payloads_do_not_expose_held_out_oracle_output`
  plants a held-out fixture byte string and a withheld stdout or stderr
  string that ADR 0040 would replace with the fixed note. Neither string
  appears in `GET /tasks`, `GET /tasks/{id}`, `GET /attempts`,
  `GET /attempts/{id}`, `GET /plans`, `GET /objectives`, or `GET /inbox`.
- `tests/test_human_read_model.py::test_inbox_hides_unactionable_requests`
  covers a viewer, an operator, a service token, a proposer who holds
  `approver`, a different approver, and the same proposer with
  `SDF_AUTH_SINGLE_USER=1`. Only the last two see the plan request. The
  proposer's `POST /plans/{id}/reject` still succeeds.
- `tests/test_human_read_model.py::test_reads_do_not_insert_domain_rows`
  counts `tasks`, `attempts`, `plans`, `decision_requests`, `evidence`,
  `agent_messages`, and `auth_audit` before and after each new GET. The
  counts match. `auth_audit` gains no `mutation` row.
- `tests/test_human_read_model.py::test_read_payloads_redact_like_the_timeline`
  stores a bearer-shaped string in a title the timeline would redact and
  requires the collection item and the timeline entry to show the same
  masked value. An embedded message body is at most 400 characters.
- `tests/test_api_auth.py::test_every_route_requires_a_token` and
  `test_health_is_public_and_openapi_is_not` keep passing with the new
  routes included.

Metrics. Each new GET logs one `human_read` record on the existing `sdf`
logger (`sdf_core/observability.py:1-13`) with `route`, `items`, and
`cursor_present`. `GET /inbox` also logs `actionable_count`. The tests
assert those fields on the log record. When `SDF_OTEL_ENABLED` is set, the
same fields are attributes on the request span (ADR 0024). This ADR does
not add an exporter.

## Alternatives rejected

- **A projection or inbox table.** The rows to list are `TaskRow`,
  `AttemptRow`, `PlanRow`, `GraphNodeRow`, and `DecisionRequestRow`. A
  second store would drift from those rows. The timeline already proved a
  read can be a query (`sdf_core/timeline.py:1-10`).
- **Offset pages.** An insert on an earlier page shifts every later offset.
  The cursor is the key the order already uses.
- **Folding the inbox into `GET /decision-requests` by changing the default.**
  That route is an unfiltered array (`sdf_core/api.py:1502`). Changing its
  body or its default filter would break current clients. A later opt-in
  cursor is allowed. The default stays the array. The inbox is a new path.
- **Hiding decision requests from viewers entirely.** Viewers can already
  GET them, and they can already GET a plan by id. The new rule is about
  what is actionable.
- **Returning lease columns or dispatch keys.** Those fields are operational
  (`sdf_core/db.py:73`, `sdf_core/db.py:104-105`). The read model does not
  need them to show status.
- **A new role such as `auditor` or `inbox`.** `viewer` already covers every
  safe method (`sdf_core/auth.py:44-49`). `can_approve` already answers
  whether the caller may act (`sdf_core/auth.py:95-97`).

## Consequences

- Clients that list work no longer need an id they learned out of band.
- The inbox and `POST /plans/{id}/reject` disagree on purpose for a
  proposer: the inbox omits the plan, and the reject route still accepts
  the proposer (ADR 0015). That split is the accepted choice.
- `GET /decision-requests` can still return a large array. Paginating it
  is a later opt-in. The default stays the array.
- A database with a large `created_at` tie uses `id` as the tiebreak. Ids
  are strings. The order is deterministic. It is not a time order among
  equal timestamps.
- Attempt `principal` on the new detail matches a name the timeline already
  shows on auth-audit entries.

## Open questions

None. The owner closed the list in the review of PR #124. The choices are
in the Decision above.

## Sequencing

The workstream order is fixed:

1. This ADR (read model and inbox).
2. [ADR 0045](0045-human-actions.md) (attempt cancel, human messages;
   attestation deferred to a future ADR).
3. [ADR 0046](0046-live-updates.md) (SSE over the existing runtime-event
   rows; the timeline stays a polled GET in v1).
4. [ADR 0047](0047-console-ui.md) (thin client).

No implementation of this workstream starts until this ADR is accepted.
ADR 0028: a proposed ADR is not authority. Accepting this ADR accepts the
routes, the cursor, the field lists, the inbox filter, and the read-only
rule above. It does not accept the later ADRs, and it does not accept
writing code for them.

Code for step 2 starts only after this ADR and ADR 0045 are both accepted.
Code for step 3 starts only after those two and ADR 0046 are accepted.
Code for step 4 starts only after all four are accepted. Human attestation
of evidence is not one of the four steps. No attestation code starts until
a later ADR defines an explicit attestation class and rule. ADR 0045
records that boundary.

## Amendment: proposer casefold column

The inbox must hide a plan when `proposed_by.casefold()` equals the caller,
the same comparison `decide_plan` uses, without loading every proposer.
That comparison is `str.casefold`, not SQL `lower()`.

`plans.proposed_by_casefold` stores `str.casefold(proposed_by)` at insert.
Migration `0032_plan_casefold` backfills existing rows in Python, in batches,
and then rejects a missing fold. The inbox compares that column to
`str.casefold` of the caller with a binary collation (`C` on PostgreSQL,
`BINARY` on SQLite). It does not repair rows on GET. A missing fold or a
stored fold that disagrees with `str.casefold(proposed_by)` on a row the
page would return makes `GET /inbox` fail instead of returning a partial list.
`can_approve` false still yields an empty page only when every plan has a
fold; a missing fold fails the listing for that caller too, because an empty
page would otherwise look complete.

Python 3.11, 3.12, and 3.13 ship Unicode 14.0.0, 15.0.0, and 15.1.0. Full
casefold mappings are the same in those versions, so `str.casefold` agrees.
Any other Unicode version is refused at fold time. A direct write that stores
a non-null but wrong fold is unsupported: the database can require the value
and freeze it, and it cannot recompute `str.casefold`.

The equality index finds one caller's plans. The inbox still walks pending
requests in `(created_at, id)` order and skips the caller's rows inside the
database while it fills `LIMIT`. That walk is not an application fetch, and
the equality index does not make it a seek. `next_cursor` remains the last
returned item, or null when nothing eligible remains.

