# Contributing

## Agent guidance

Agents are the primary builders of this repository. Before editing, read
these, in order:

1. [AGENTS.md](AGENTS.md). Build and test commands, what this collector
   does not do, and the rule for `tests/samples/`.
2. [README.md](README.md). The short overview for a person: what SDF is,
   how to install it, the quickstart, supported agents, configuration, and
   links onward. Keep it that short. The command list and a source build
   belong in this file.
3. [docs/](docs/). The behavior. The index is the
   [Documentation](README.md#documentation) table in the README: what each
   agent exports, the receiver, content storage and `sdf redact`, the
   summary and computed cost, outcomes, decision records and the local
   budget, file permissions, the `sdf-decisions` skill, `sdf check`, the
   ledger and `sdf audit`, tasks and `sdf verify`, the GitHub Action,
   known gaps, and how each agent marks a session. When behavior changes,
   update the doc that describes it rather than growing the README.

This file then has [building from a clone](#development-setup), the
[command list](#usage), and the [checks](#checks). Report a vulnerability
the way [SECURITY.md](SECURITY.md) describes.

## Development setup

People install a release with `go install`, as in the
[Quickstart](README.md#quickstart). That needs Go 1.26 or newer.
Or build from source (no C compiler needed):

```bash
git clone https://github.com/paulpham157/sdf && cd sdf && make build
```

`make build` writes a static `./sdf`. The SQLite driver is pure Go
(`modernc.org/sqlite`), so the build keeps `CGO_ENABLED=0`. Other targets
are in [AGENTS.md](AGENTS.md). Pull-request and post-merge checks are
[below](#checks).

## Usage

Every command prints its flags with `--help` (or `sdf help <command>`).

| Command | What it does |
| --- | --- |
| `sdf serve` | Receive OTLP, store it in local SQLite, and serve the UI |
| `sdf init` | Print or write the settings that point an agent at the receiver |
| `sdf summary` | Print sessions: duration, tokens, tool calls, and cost |
| `sdf decision` | Record, link, list, graph, retract, audit, brief, or check decisions (`add`, `link`, `list`, `graph`, `retract`, `audit`, `brief`, `check`) |
| `sdf audit` | Verify or print the head of the optional hash-chained ledger (`verify`, `head`) |
| `sdf task` | Declare or show a task's acceptance criteria (`declare`, `show`) |
| `sdf verify` | Run a declared task's checks against a commit |
| `sdf redact` | Drop stored prompt, response, tool, and error text from a database |
| `sdf check` | Run the embedded local cases against the summary |
| `sdf prices refresh` | Fetch the OpenRouter catalog into the local price cache (the only command that uses the network for prices) |
| `sdf version` | Print version, commit, build date, Go version, and platform (`sdf --version` and `sdf -v` are the same) |

`sdf decision` relations include `supersedes`, `refines`, `blocks`,
`conflicts_with`, `constrains`, `implements`, `validates`, and
`contradicts`. `list` and the UI show blocked and conflicted records;
`graph` (also `GET /api/decisions/graph` and the UI's Decisions tab)
shows chapters, endings, and explored %; `brief` prints a ranked,
size-capped list of active constraints, blocked, and conflicting
decisions; `audit` reports unlinked decisions and commits (exit 1 on
findings); `check` exits 1 when an in-force decision forbids `push`,
`merge`, `release`, `deploy`, or an architecture change. `sdf init
--write` for Claude Code and Codex also installs a SessionStart hook
that runs `sdf decision brief` (no flags, 2s timeout, fail-open); other
agents run the command or use the `sdf-decisions` skill
([Decisions at session start](docs/agents.md#decisions-at-session-start)).
The `pre-push` git hook from `sdf init --git-hooks --db PATH` warns
about pushed commits that implement no decision
([pre-push decision check](docs/receiver.md#pre-push-decision-check)).
Details are in [docs/outcomes.md](docs/outcomes.md).

Authentication is off by default; keep the binds on localhost.

## Checks

Pull request checks and the post-merge checks are different workflows. A push
to `main` does not repeat the pull request matrix, and neither workflow
deploys or publishes a release. Only the Release workflow creates one, as a
draft, when a `v*` tag is pushed.

| Workflow | When | Why |
| --- | --- | --- |
| PR CI (`.github/workflows/pr.yml`) | `pull_request` opened, new commits (`synchronize`), or reopened | Fast feedback before merge: `gofmt` and staticcheck on the stable Go toolchain, `go vet` and `go test` on oldstable and stable, and a gitleaks scan of the commits in the pull request. |
| Main CI (`.github/workflows/main.yml`) | `push` to `main` | Fuller check of the merged tree: one stable `go test` (a squash of a stale branch can fail even when each side passed), `go test -race`, build `sdf` with `CGO_ENABLED=0` and run `tests/samples` through `sdf serve` over HTTP and gRPC then `sdf summary`, a Goreleaser snapshot uploaded as a workflow artifact, CodeQL, and gitleaks over main's history. |
| Release (`.github/workflows/release.yml`) | `push` of a `v*` tag, nothing else | `go test`, then `goreleaser release --clean`: archives for linux, darwin, and windows on amd64 and arm64 plus `checksums.txt`, uploaded to a draft GitHub Release that a maintainer publishes by hand. Only this job has `contents: write`. |
| Scheduled (`.github/workflows/schedule.yml`) | Mondays 03:17 UTC | Depends on time, not on a new commit: `govulncheck` (new advisories) and a gitleaks scan of every branch. |

The branch ruleset requires the check name `gitleaks (full history)`. That job
is the pull request scan. The name is the ruleset context. The scan is the
pull request's commit range, not every branch.

Run the pull request checks locally:

The short path:

```bash
make fmt-check
make staticcheck
make vet
make test
```

`make staticcheck` compiles staticcheck 2026.2.2 (`honnef.co/go/tools`
v0.8.2). CI installs that same release as a checksum-verified binary.
`make fmt-check` uses whatever `gofmt` is on `PATH`; CI uses the stable
toolchain.

The post-merge checks:

```bash
make race
make e2e
```

`make e2e` builds the static binary and runs `tests/e2e` (`go test -tags e2e`).
Plain `go test ./...` does not build that package.
