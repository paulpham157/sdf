# ADR 0046: Server-Sent Events over one runtime-event stream

Status: proposed

Numbered 0046 because 0043 is the evidence content MAC, 0044 is the read
model, and 0045 is human actions. Third step of the human-interaction
workstream. Depends on [ADR 0044](0044-human-read-model-and-approver-inbox.md)
and [ADR 0045](0045-human-actions.md) for sequencing. Does not add an event
table, an event kind, or a bus. Polling stays the source of truth.

## Context

Consumers already pull normalized runtime events and a merged timeline.

`GET /attempts/{attempt_id}/runtime-events` returns `normalized(row)` for
every row of that Attempt, in the deterministic merge order
(`sdf_core/api.py:801`, `sdf_core/events.py:140-160`,
`sdf_core/events.py:163-179`). `GET /attempts/{attempt_id}/event-streams`
returns, per stream, the sequence range and the missing sequence numbers
(`sdf_core/api.py:809`, `sdf_core/events.py:189-206`). A stream is
`(source, attempt_id, session_id)`. `sequence` is authoritative inside one
stream. Across streams, the merge key is persisted time, then stream key,
then sequence (ADR 0029). An event stored out of order does not fill the
gap. The gap list is `range(1, last_sequence)` minus the sequences present
(`sdf_core/events.py:200-201`).

`GET /runs/{run_id}/timeline` is `build_timeline`: one list sorted by time,
redacted, read-only (`sdf_core/api.py:958`, `sdf_core/timeline.py:1-10`,
`sdf_core/timeline.py:90-140`). Timeline entries are not a stream and they
have no `sequence`. ADR 0029 says the timeline stays a persisted-time view
across record types. Evidence entries in that list carry kind, status,
criterion, and class. They do not carry `evidence_id`
(`sdf_core/timeline.py:76-81`). Two evidence rows can share `created_at`
and `kind`, so the entry has no unique id.

Rows in `runtime_events` are append-only. `_APPEND_ONLY` includes
`RuntimeEventRow` (`sdf_core/db.py:669`, `sdf_core/db.py:673-681`). The
unique identity is `(source, attempt_id, session_id, sequence)`
(`sdf_core/db.py:243-248`). Normalized views set `acceptance_evidence` to
false. No event changes Attempt status, Task status, or Evidence
(`sdf_core/events.py:19-22`; ADR 0029). A human cancel that wins appends
`runtime_cancelled` only as a consequence of `RuntimeController.cancel`
(ADR 0045, `sdf_core/runtime.py:367-371`). That kind is already in
`RUNTIME_LIFECYCLE_KINDS` (`sdf_core/events.py:49-56`).

Authentication is per request. `authorize` checks the bearer token, then
the role, then returns (`sdf_core/api.py:153-171`). `verify_token` rejects
a revoked or expired token (`sdf_core/auth.py:240-243`). Nothing in the
process re-checks a token after the handler has started, because every
current handler returns. `last_used_at` moves at most once a minute
(`sdf_core/auth.py:65`, `sdf_core/auth.py:244-246`).

There is no `StreamingResponse`, no `text/event-stream` route, and no
WebSocket route in `sdf_core`. The repository has no browser client.

Safe methods default to `viewer` (`sdf_core/auth.py:331-335`). A viewer can
already GET the event list and the timeline.

ADR 0023 caps concurrent live `/run` calls per process and answers the
overflow with 429 and `Retry-After`. The auth failure limiter is in
memory, per process (`sdf_core/auth.py:252-282`). Both are the patterns
for "this process will only hold so many of these".

## Decision

The owner accepted these choices in the review of PR #124.

- 4 open streams per principal, 64 per process, a queue of 100, and a
  token re-check every 15 seconds.
- An unknown timeline `Last-Event-ID` returns 409. v1 does not ship a
  timeline SSE route, so this rule applies when that route is added. v1
  polls `GET /runs/{id}/timeline`.
- One SSE connection is one runtime stream. There is no composite cursor.
- v1 ships only the runtime-event SSE.

1. **One runtime stream per connection.**

   `GET /attempts/{attempt_id}/runtime-events/stream?source=<source>&session_id=<session_id>`

   `source` and `session_id` are required. Without them the connection
   would mix streams, and a single `Last-Event-ID` cannot resume every
   stream's sequence (ADR 0029). A missing query parameter is 422. An
   unknown Attempt is 404.

   The response media type is `text/event-stream`. Each SSE event id is the
   decimal `sequence` of that one stream. The `data` field is the JSON
   object `normalized` already returns (`sdf_core/events.py:163-179`),
   including `acceptance_evidence: false`.

   `Last-Event-ID` is that sequence. The server sends rows of this stream
   with `sequence` greater than the header, in `sequence` order. A missing
   header starts at sequence 0, so the first stored row is included. After
   the historical rows, the connection stays open and sends rows that are
   committed later for this stream.

   **The tail is a database poll.** There is no hook on the insert path.
   A writer of `runtime_events` does not call into the stream, and a slow
   client cannot block that writer. The poll interval is 1 second. Each
   poll opens a session, reads rows of this stream with `sequence` greater
   than the cursor, closes the session, then sleeps until the next
   interval. The session lifetime is that one read. It does not stay open
   across the sleep, and it does not stay open for the life of the HTTP
   connection.

   Gaps use the rule in `summarize`. Let `cursor` be the `Last-Event-ID`
   (or 0) and let `last` be the highest sequence stored for this stream at
   the moment the poll notices a hole. The missing numbers are those in
   `range(cursor + 1, last)` that have no row (`sdf_core/events.py:200-201`
   is the same subtraction over `range(1, last)`). The server sends one SSE
   event with `event: gap` and `data` `{"gaps": [...], "stream": {...}}`
   before it sends any data event past the hole. It does not invent a row
   for a missing sequence. When a late row arrives, it is sent as a normal
   event with its own sequence. The client keeps the gap until that
   sequence appears. A `Last-Event-ID` that is not a non-negative integer
   is 422. A `Last-Event-ID` greater than any stored sequence is a legal
   cursor: the server sends nothing until a higher sequence exists.

2. **Timeline in v1 is the existing GET.** v1 does not add
   `GET /runs/{run_id}/timeline/stream`. Clients poll
   `GET /runs/{run_id}/timeline` (`sdf_core/api.py:958`).

   Evidence entries today have no unique id (`sdf_core/timeline.py:76-81`).
   This ADR adds `evidence_id`, the Evidence row's `id`, to each evidence
   entry `build_timeline` returns. That field is what a later timeline
   stream would use as its event id, together with `at`, `source`, and
   `kind` on the other entry types. Two evidence rows that share a
   timestamp are then distinct. The field is a read of the row that already
   exists. It is not a new sequence and it is not written to
   `runtime_events`.

   When a timeline stream is added later, `Last-Event-ID` that is not an
   id on the current `build_timeline` result returns 409, and the client
   falls back to the GET. v1 does not implement that route. Runtime entries
   inside the timeline do not grow a second gap algorithm. Clients that
   need gap reporting open the route in section 1.

3. **Polling remains the source of truth.** `GET /attempts/{id}/runtime-events`
   and `GET /runs/{id}/timeline` are unchanged aside from `evidence_id` on
   timeline evidence entries. A client that sees `event: gap`, a dropped
   connection, or an overflow calls the GET and replaces its local copy
   with that response. The stream is a hint that new rows exist.

4. **Auth at start and again while the connection is open.** The request
   passes `authorize` like any other viewer GET (`sdf_core/api.py:153-171`).
   Every 15 seconds, and before the first byte after a quiet period, the
   handler loads the same token and applies the checks `verify_token`
   applies for revocation and expiry (`sdf_core/auth.py:240-243`). A failed
   check sends `event: goodbye` with `data` `{"reason": "revoked"}` or
   `{"reason": "expired"}` and closes the connection. No further row is
   sent. Role is re-read from the token row as well. A token that no longer
   has `viewer` is closed with `reason` `denied`. The connection does not
   keep the `Principal` object from the first check as a permanent grant.

5. **Backpressure and caps.** Each connection has an in-memory queue of at
   most 100 events. If the queue is full, the handler sends
   `event: goodbye` with `data` `{"reason": "overflow"}` and closes. It
   does not block the session that inserts runtime events, and it does not
   drop events silently. The client reconciles with GET. The poll loop
   drops the connection rather than waiting on a full queue, so the insert
   path stays untouched.

   Caps, per process, same shape as the auth limiter
   (`sdf_core/auth.py:252-282`): 4 open streams per principal, 64 open
   streams per process. The next attempt receives 429 and `Retry-After: 15`.
   A second OS process has its own counters. A heartbeat comment
   (`: keep-alive`) goes out every 15 seconds so an idle proxy can see the
   connection is alive. The comment is not an event and it has no id.

6. **What is not added.** No WebSocket. No timeline SSE in v1. No composite
   cursor across streams. No new `runtime_events.kind`. The names `gap` and
   `goodbye` are SSE event types on the wire. They are not inserted into
   `runtime_events`. No bus, no LISTEN/NOTIFY requirement, no change to the
   unique sequence key. No callback from the code that inserts a runtime
   event. A deployment that has more than one API process delivers a new
   row on the next poll of a process that can see the commit. Clients that
   need a complete tail still poll.

The stream route is a safe method and requires `viewer`. It is not public.

## Invariants

- A stream connection sends only rows of the requested
  `(source, attempt_id, session_id)`, in `sequence` order.
- `Last-Event-ID` on that connection is a sequence. The next row has a
  greater sequence. Reconnect does not renumber and does not replay the
  id named in the header.
- The tail reads the database on a 1-second poll. Each poll's session is
  closed before the sleep. The insert path has no stream hook.
- Missing sequences are reported with the same gap arithmetic as
  `summarize` (`sdf_core/events.py:200-201`). The server does not write a
  filler row.
- v1 does not open a timeline SSE. Timeline evidence entries include
  `evidence_id`. A future timeline stream returns 409 for an unknown
  `Last-Event-ID`.
- The stream handler does not insert, update, or delete a domain row.
  Adding `evidence_id` to a timeline entry is a field on the read model.
  Token `last_used_at` continues to follow ADR 0015.
- A revoked, expired, or no-longer-viewer token stops receiving bytes
  within one 15-second re-check.
- When the per-connection queue exceeds 100, the server closes. It does
  not discard a prefix and continue. It does not block writers.
- `acceptance_evidence` on every data event built by `normalized` is false.
- After any stream failure, `GET /attempts/{id}/runtime-events` and
  `GET /runs/{id}/timeline` still return the full stored view.

## Proof

Tests to add. An in-process client reads the SSE body. No live provider
and no second machine.

- `tests/test_human_live.py::test_runtime_stream_resumes_from_last_event_id`
  Writes sequences 1, 2, and 4. A client with `Last-Event-ID: 2` receives
  `event: gap` containing `3`, then the data event for sequence 4, and
  never receives sequence 2 again.
- `tests/test_human_live.py::test_gap_list_matches_summarize`
  For the same rows, `event: gap` lists the same integers as
  `summarize(...).gaps` filtered to those above the cursor.
- `tests/test_human_live.py::test_stream_does_not_write_rows`
  Counts `runtime_events`, `auth_audit`, and `attempts` around a stream
  that sends one historical event and one heartbeat. Domain counts are
  unchanged. `auth_audit` gains no `mutation` row.
- `tests/test_human_live.py::test_runtime_tail_is_a_poll_with_a_short_session`
  Inserts a row after the stream is open, without any insert-path callback.
  The row appears on the connection within one 1-second poll. The session
  used for that read is closed before the next sleep. A blocked client
  does not stall the insert.
- `tests/test_human_live.py::test_revoked_token_closes_the_stream`
  Revokes the token after the first event. The next re-check sends
  `reason` `revoked` and no later insert is delivered.
- `tests/test_human_live.py::test_overflow_closes_without_blocking_the_writer`
  Fills the queue past 100 while the client does not read. The connection
  ends with `overflow`. A following insert still commits, and
  `GET /attempts/{id}/runtime-events` returns it.
- `tests/test_human_live.py::test_connection_cap_returns_429`
  The fifth stream for one principal, and the 65th for the process,
  receive 429 and `Retry-After`.
- `tests/test_human_live.py::test_timeline_evidence_entries_have_evidence_id`
  Two evidence rows with the same `created_at` produce two timeline entries
  with different `evidence_id` values. The route table has no
  `/timeline/stream` in v1. `GET /runs/{id}/timeline` still returns the
  full list.
- `tests/test_human_live.py::test_polling_get_is_complete_after_a_gap`
  After the gap test, the GET body contains sequences 1, 2, and 4 and no
  synthesised 3.
- The new route is a viewer GET in
  `tests/test_api_auth.py::test_every_route_requires_a_token`.

Metrics, as `human_stream` log records on the existing `sdf` logger
(ADR 0044 states the OTEL-attribute rule):

- `opened`, with `route` and `principal`
- `gap`, with `count`
- `closed`, with `reason` one of `client`, `revoked`, `expired`, `denied`,
  `overflow`

## Alternatives rejected

- **A new event table or a global sequence.** ADR 0029 already defines
  identity, order, replay, and gaps. A second sequence would split the
  cursor from the unique key in `sdf_core/db.py:243-248`.
- **One SSE connection that multiplexes every stream of an Attempt.** The
  SSE spec gives the client one `Last-Event-ID`. Resuming one id cannot
  name the cursor of every other stream. One connection is one stream.
  Clients open another connection per stream, inside the caps above. A
  composite cursor is not part of this ADR.
- **A timeline SSE in v1.** The timeline has no sequence. Evidence entries
  lack an id today (`sdf_core/timeline.py:76-81`). v1 adds `evidence_id`
  and keeps the client on `GET /runs/{id}/timeline`.
- **An insert-path hook or LISTEN/NOTIFY.** The rows are in the database
  the API process already reads. A hook on insert can block the writer
  when a client is slow. The poll cannot.
- **WebSockets.** The payload is a one-way tail of append-only rows. SSE
  is that shape. A socket would be a second transport to authenticate and
  to cap.
- **Streaming as a substitute for GET.** This ADR does not claim the
  stream is complete across processes. The GET is.

## Consequences

- A console can show new runtime events without waiting for a long poll,
  and it can still rebuild from GET after a gap or a dropped connection.
- Each API process notifies only the clients connected to it, and only
  after its next 1-second poll sees the commit. A client pinned to another
  process learns the row on its next GET, or on its next stream open
  against a process that reads the committed row.
- Writers of runtime events do not take a lock that subscribers hold.
- Long-lived connections re-check the token on the 15-second timer. A
  revoked approver's stream stops even if the browser leaves the tab open.
- The 100-event queue and the 4-per-principal cap are process memory
  limits. They are not durability. Overflow tells the client to GET.
- Heartbeat comments are easy to mistake for events. Clients ignore lines
  that start with `:`.
- Timeline clients in v1 poll. They can tell two evidence entries apart
  by `evidence_id`.

## Open questions

None. The owner closed the list in the review of PR #124. The choices are
in the Decision above.

## Sequencing

Implementation waits until ADR 0044, ADR 0045, and this ADR are accepted.
The console waits for [ADR 0047](0047-console-ui.md) as well. See ADR 0044.
