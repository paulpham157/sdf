# Outcomes

Telemetry says a tool call succeeded. It does not say the change was merged.
`sdf summary` attaches an outcome from the local checkout, and stops when the
match would be a guess. Nothing in this step calls the network unless you
pass `--github`.

## Linking a session to a commit

A link is **exact** when `claude_code.tool_result` carries
`vcs.ref.head.revision` or a full 40-character `git_commit_id` (including one
inside `tool_parameters`). Claude Code adds those only when
`OTEL_LOG_TOOL_DETAILS=1`, and an abbreviated id is ignored. The SHA has to
exist in the checkout. If it does not, the session stays unlinked. SDF does
not substitute a hook commit or a different commit from the same window.

A link is **hook** when an `sdf.git.commit` log from `sdf init --git-hooks`
is already on this session. The hook copied the session id the agent put in
the environment, so the SHA is that session's commit. Several commits in one
session are all kept. A `post-rewrite` event drops the SHA it replaced.
The checkout path on the event is used to verify the object when `--repo`
is not set. `--repo PATH` is that checkout instead, including when the path
on the hook event still exists on disk. If `--repo` or the hook path
disagrees with a working directory the agent exported, the session stays
unlinked. A hook SHA that is not in this checkout can still match an
`SDF-Session` line in a commit that is. A missing exported SHA does not fall
through to the hook.

A link is **trailer** when a commit message has a line `SDF-Session: <id>`
for this session id. Summary reads that line from the message. It does not
use git's trailer block, which keeps only the last block: a GitHub squash
message often puts a blank line and then `Co-authored-by` after the session
line, and that block would hide `SDF-Session`. The line is how a squash
merge can stay attributed after the original SHA is gone, and only when the
squash message still contains it. See [Git hooks](receiver.md#git-hooks).

A link is **heuristic** when none of those fired, the session has one
absolute working directory (`pi.cwd` from pi-otel, or a single
`workspace.host_paths` value from Claude Code), that directory is a git
checkout on this machine, and exactly one commit has a committer time inside
the session window widened by two seconds. Zero commits, or more than one,
stays unlinked. The same commit in two sessions' windows unlinks both.
A commit that carries an `SDF-Session` trailer, or that a hook already
attributed to another session, is not given out by the heuristic.
`--repo PATH` supplies a checkout when the telemetry has no directory, and
it is the checkout used for a hook or trailer link when you pass it. If it
disagrees with a directory the agent did export, the session stays unlinked.

Without the git hooks, Codex and OpenCode do not export a working directory
or a commit SHA on the signals this receiver stores, so those sessions stay
unlinked unless you pass `--repo` and the time window contains one commit.
Pi exports `pi.cwd` and can use the heuristic, and the hook when the
session file stem matches `pi.session.id`. A commit with no session id is
not attached to a session by the hook. Under Codex's default
`workspace-write` sandbox the hook does not run, because `.git` is
read-only. See [Git hooks](receiver.md#git-hooks).

## Pull requests and CI

`--github` asks for the pull request and CI status of a commit that is already
linked. It uses `gh` when `gh` is on `PATH`, and otherwise `GH_TOKEN` or
`GITHUB_TOKEN` against `api.github.com`. The token is a header, not an
argument. The remote has to be `github.com`. The commit's pull request list
is `GET /repos/{owner}/{repo}/commits/{sha}/pulls`. That list is empty for a
closed pull request that was not merged. When it is empty and the hook
recorded a branch for that commit, summary lists
`GET /repos/{owner}/{repo}/pulls?state=all&head={owner}:{branch}` and keeps
a pull request whose head is that commit. A closed, unmerged pull request
is then reported, `accepted` is false, and the reason says the pull request
was closed without merging. No pull request means not accepted. Several
pull requests, or a failed query, means accepted is unknown. CI is
`success`, `pending`, `failure`, or `none`. GitHub's combined status is
`pending` with `total_count` 0 when the commit has no statuses. That is
`none` when there are no check runs either, not a pending check.

`cost_per_accepted_outcome_usd` is the sum of `computed_cost_usd` for sessions
whose pull request is merged, divided by how many such sessions there are.
It is `unknown`, not zero, when any session in the set has no known outcome,
when an accepted session has unknown computed cost, when the same commit is
accepted on two sessions, or when nothing was merged.

## Decision records

A decision is something a caller writes down. The summary does not infer one
from telemetry, and recording one changes nothing about collection or
summarization.

Each record has an id, one statement, and a status of `hypothesis` or
`decision`. A hypothesis is an assumption, not a settled fact. An edge connects
two endpoints and uses one of `constrains`, `implements`, `supersedes`,
`validates`, or `contradicts`. An endpoint is a decision id, a session id, or a
full 40-character commit SHA. A session or commit endpoint is stored only when
the store already has telemetry for it; the edge is not a copy of that
telemetry. A commit SHA is matched without regard to case, so a SHA the agent
exported in uppercase still matches the lowercase ref the edge stores.

`GET /api/decisions` returns the records and edges in insertion order. An empty
store returns empty lists. `sdf decision add` writes one record. It needs an id,
a statement, and a status of `hypothesis` or `decision`. `sdf decision link`
writes one edge. It needs a relation and two `KIND:REF` endpoints. No route
writes a record.

## Local budget

A budget is one ceiling in USD over one window, read from a local file and
compared with the computed session cost. The comparison is reported. Nothing is
blocked or throttled when the total passes the ceiling.

The file names `ceiling_usd` and `window`. `window` is `store`, for every
session, or `day`, for one UTC calendar day named as `YYYY-MM-DD`. A day window
places a session by its start time. A session with no start time cannot be
placed, so its cost counts as unknown instead of being dropped.

The result is `within` when the total is at or under the ceiling, `over` when
it is above, and `unknown` otherwise. `unknown` carries no total. One session
with an unknown computed cost makes the whole window unknown, and so does a
total that is not a terminating decimal. A window with no sessions at all says
`no sessions`. A day window whose sessions all lack a start time does not: it
has sessions, and the cost is unknown.

The budget is applied when a summary is built with a budget file. `sdf summary
--budget PATH` and `sdf serve --budget PATH` pass that file. Without one, the
report is unchanged and the `budget` field is absent. A file that fails to load
exits non-zero and prints no report. The page reads the summary, so a budget
that is in the report is in `GET /api/summary`.
