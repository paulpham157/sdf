# Decision and link recall

SDF does not infer a decision from telemetry. A decision exists only when
something calls `sdf decision add`, and a commit is linked only when
something calls `sdf decision link` (or a later recorder writes the same
rows). This page is how that behavior is measured.

Two measurements share the names and stay separate:

- **Labeled recall** runs the harness in `internal/decision/recall` on
  synthetic fixtures. It needs ground truth.
- **Link recall by proxy** is `sdf decision recall`. It needs no labels.
  A commit counts as a miss when its session has a decision and the commit
  has no `implements` edge. The ratio is
  `commits_linked / commits_in_decision_sessions`, the same totals
  `sdf decision audit` already computes. The overview chart's recall is a
  different share: every recorded commit that has an `implements` edge,
  over all recorded commits.

## Labeled metrics

Each fixture is ingested on its own, then compared with its labels.

| Metric | Numerator | Denominator | When the denominator is 0 |
| --- | --- | --- | --- |
| Decision recall | Labeled decisions that match a stored decision | Labeled decisions | Undefined |
| Link recall | Labeled `implements` commit links that were stored | Labeled commit links | Undefined |
| Decision precision | Those same hits | Stored decisions | Undefined |
| Link precision | Those same hits | Stored `implements` edges from a commit | Undefined |

A labeled decision matches a stored decision when the ids are equal, or,
if no id matched, when the statements are equal after trimming, case
folding, and collapsing whitespace. Each stored decision matches at most
one label. A link matches when an `implements` edge runs from that commit
to the stored decision that matched the label. Session edges are not
links in this metric. The sums are taken over fixtures, and the ratios
are computed from the sums.

The 95% Wilson score interval is reported for recall when the label count
is non-zero. Zero successes pin the low end at 0. A full set of successes
pins the high end at 1.

## Fixture format

Fixtures live in `internal/decision/recall/fixtures/*.json`. They are
hand-written, marked `sdf.fixture=recall`, and contain no secrets. Each
file is one scenario:

- `name`: lowercase words separated by hyphens.
- `notes`: one short description of what the scenario holds.
- `logs`: OTLP/JSON log export bodies. The harness decodes them with
  `otlp.Decode` and stores them with `store.Insert`.
- `labels.decisions`: `id`, `statement`, `status` (`hypothesis` or
  `decision`), and optional `decided_by` (`user` or `agent`). The
  statement must appear in a log body.
- `labels.links`: `commit` (lowercase 40-character SHA) and `decision`
  (a labeled id). The SHA must be an `sdf.git.commit` event in `logs`.
  The edge means that commit implements that decision. An unlabeled
  commit is a commit that must stay unlinked.

Turns use a log named `recall.turn` with `session.id`, `recall.role`, and
the utterance in the body. Commits use the `sdf.git.commit` shape the git
hook posts, so `sdf decision audit` can see them.

## Scenarios

| Fixture | Labels |
| --- | --- |
| `approve` | User approves WAL mode. One decision, no commit. |
| `reject` | User rejects PostgreSQL and stays on SQLite. One decision, no commit. |
| `decide-commit` | One decision, then one commit that implements it. |
| `unrelated-commit` | One decision, then a typo-fix commit that does not implement it. |
| `plain-chat` | A question and an answer. No decision and no commit. |
| `long-session` | The decision is the first turn. Eight turns and twelve hours later, one commit implements it. |
| `many-to-many` | Two decisions and three commits. The middle commit implements both. |

## Adding a scenario

Add one JSON file in that directory. Keep it small. Use a new session id
and a new SHA. The loader rejects a statement that is not in a log body, a
link whose commit was not ingested, a duplicate id, and a duplicate
statement inside the file. `go test ./internal/decision/recall` runs every
file. The CI gate compares the skill-only run with the snapshot below.
A new label changes the denominators, so update the snapshot and this
page in the same change.

The harness calls a `Recorder` after ingest. `SkillOnly` writes nothing.
Pass another `Recorder` to `Measure` to score a writer. A recorder that is
a measurement must not read `Fixture.Labels`. The package does not import
an auto-linker. When `internal/decision/autolink.go` lands (#281), a
recorder can call it and the same fixtures will score that path.

## Baseline

Measured on the skill-only pipeline, which is what CI runs. Nothing is
recorded, so both recalls are 0 and both precisions are undefined.

| | Labeled | Recorded | Hits | Recall | Precision | Wilson 95% (recall) |
| --- | --- | --- | --- | --- | --- | --- |
| Decisions | 7 | 0 | 0 | 0 | undefined | [0, 0.354330] |
| Commit links | 6 | 0 | 0 | 0 | undefined | [0, 0.390334] |

Per scenario the hits are 0. `approve`, `reject`, `decide-commit`,
`unrelated-commit`, and `long-session` each have one labeled decision
(Wilson high 0.793451). `many-to-many` has two decisions (Wilson high
0.657620) and four links (Wilson high 0.489891). `plain-chat` has none,
so its recall is undefined. Labeled links are one each on `decide-commit`
and `long-session` (Wilson high 0.793451).

Issue #284's target is link recall at least 95% and a precision drop of at
most 5 points against a stronger pipeline. These floors are the measured
baseline. Raise them when a recorder is plugged in and the new numbers
have been measured. The test fails if recall drops below the snapshot, if
a precision that was undefined becomes defined, or if the label counts
change.

## `sdf decision recall`

```bash
sdf decision recall [--db PATH] [--json] [--since DURATION|RFC3339] [--until RFC3339]
```

The store is opened read-only and is not created. A missing file in an
existing directory is an empty store and exits 0. JSON uses schema
`sdf.decision.recall/v1`. `link_recall_by_proxy` and `wilson95` are null
when no commit in the window sits in a session that has a decision.
`missed_commits` lists those misses. `--since 24h` is the last 24 hours
before the command runs. Exit 0 means the report was written.

## Out of scope

Real-agent runs (k=5 on a cheap model) and A/B tests of the skill against
a new mechanism are periodic. They need a model API and the network, and
this tree does not run them. The CI gate is the fixture harness only.
