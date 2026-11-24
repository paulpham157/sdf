# Contributing

Pull request checks and the post-merge checks are different workflows. A push
to `main` does not repeat the pull request matrix, and neither workflow
deploys or publishes a release. Only the Release workflow creates one, as a
draft, when a `v*` tag is pushed. Publishing that draft opens a pull request
that updates `CITATION.cff`. The Citation workflow dispatches the gitleaks
scan, posts the required commit status, and merges the pull request. Cite
this repository reads the default branch, so it stays stale until that pull
request merges.

| Workflow | When | Why |
| --- | --- | --- |
| PR CI (`.github/workflows/pr.yml`) | `pull_request` opened, new commits (`synchronize`), or reopened | Fast feedback before merge: `gofmt` and staticcheck on the stable Go toolchain, `go vet` and `go test` on oldstable and stable, and a gitleaks scan of the commits in the pull request. |
| Main CI (`.github/workflows/main.yml`) | `push` to `main` | Fuller check of the merged tree: one stable `go test` (a squash of a stale branch can fail even when each side passed), `go test -race`, build `sdf` with `CGO_ENABLED=0` and run `tests/samples` through `sdf serve` over HTTP and gRPC then `sdf summary`, a Goreleaser snapshot uploaded as a workflow artifact, CodeQL, and gitleaks over main's history. |
| Release (`.github/workflows/release.yml`) | `push` of a `v*` tag, nothing else | `go test`, then `goreleaser release --clean`: archives for linux, darwin, and windows on amd64 and arm64 plus `checksums.txt`, uploaded to a draft GitHub Release that a maintainer publishes by hand. This job has `contents: write` to upload the draft. |
| Citation (`.github/workflows/citation.yml`, `.github/workflows/citation-gitleaks.yml`) | a GitHub Release is published, and it is not a draft or prerelease | Opens `citation/vMAJOR.MINOR.PATCH` from the default branch and a pull request that sets `version` (no leading `v`), `date-released` (the UTC calendar date of `published_at`), and the release tag's peeled commit in `CITATION.cff`. Skips drafts, prereleases, tags that are not `vMAJOR.MINOR.PATCH`, a file that already matches, and a release older than the version already in the file. The release tag is not moved. A `GITHUB_TOKEN` pull request does not start PR CI, so the Citation job dispatches `citation-gitleaks.yml` on that branch. The scan is the same base..head gitleaks range as PR CI. GitHub does not count a `workflow_dispatch` job check toward a ruleset, so after the run succeeds the job posts a commit status named `gitleaks (full history)` on the head SHA and merges that pull request with a merge commit. Cite this repository stays stale until that merge; confirm the pull request merged before sending people to Cite. `contents: write`, `pull-requests: write`, `actions: write`, and `statuses: write` cover the branch, the pull request, the dispatch, the status, and the merge. The token is not left in git config. |
| Scheduled (`.github/workflows/schedule.yml`) | Mondays 03:17 UTC | Depends on time, not on a new commit: `govulncheck` (new advisories) and a gitleaks scan of every branch. |

The branch ruleset requires the check name `gitleaks (full history)`. On a
human pull request that check is the PR CI job. The name is the ruleset
context. The scan is the pull request's commit range, not every branch. The
ruleset requires that check before the default branch updates, and GitHub
Actions cannot bypass it. The ruleset does not name an app, so a commit status
with that context satisfies it. The Citation workflow posts that status on the
citation head SHA after its dispatched scan succeeds, then merges. That merge
is how `CITATION.cff` lands on the default branch.

Run the pull request checks locally:

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
