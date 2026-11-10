# Contributing

Pull request checks and the post-merge checks are different workflows. A push
to `main` does not repeat the pull request matrix, and neither workflow
deploys or publishes a release.

| Workflow | When | Why |
| --- | --- | --- |
| PR CI (`.github/workflows/pr.yml`) | `pull_request` opened, new commits (`synchronize`), or reopened | Fast feedback before merge: `gofmt` and staticcheck on the stable Go toolchain, `go vet` and `go test` on oldstable and stable, and a gitleaks scan of the commits in the pull request. |
| Main CI (`.github/workflows/main.yml`) | `push` to `main` | Fuller check of the merged tree: one stable `go test` (a squash of a stale branch can fail even when each side passed), `go test -race`, build `sdf` with `CGO_ENABLED=0` and run `tests/samples` through `sdf serve` over HTTP and gRPC then `sdf summary`, a Goreleaser snapshot uploaded as a workflow artifact, CodeQL, and gitleaks over main's history. |
| Scheduled (`.github/workflows/schedule.yml`) | Mondays 03:17 UTC | Depends on time, not on a new commit: `govulncheck` (new advisories) and a gitleaks scan of every branch. |

The branch ruleset requires the check name `gitleaks (full history)`. That job
is the pull request scan. The name is the ruleset context. The scan is the
pull request's commit range, not every branch.

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
