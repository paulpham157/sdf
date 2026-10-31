# ADR 0036: path-filtered Go CI for the orchestration core

Status: proposed (2026-10-31). Written after merging ADR 0035's Go
scaffold (#102). It becomes accepted when a maintainer merges the
workflow that implements it. It does not change Credential Mode, Tool
Proxy, Attempt lifecycle or Evidence provenance.

## Context

[ADR 0035](0035-port-orchestration-core-to-go.md) added a Go module under
`go/` (orchestration, message bus, API gateway, task queue) with
verification tests for cancellation, idempotency and a 2 000 000-message
backpressure run, plus live `/metrics` for goroutine count and queue
depth. Repository CI today is Python-centric (`pytest` matrix, pip-audit,
CodeQL for Python) plus full-history **gitleaks**. A PR that only
touches `go/` can still be merged without running `go test ./...`, so
regressions in goroutine leaks, idempotency or backpressure would not be
caught automatically.

Gitleaks already scans the whole history for secrets on every PR; this
ADR does not add a second secret scanner for Go.

## Decision

1. **Path-filtered Go tests.** On every pull request (and on pushes to
   `main`) that change paths under `go/**`, or that change the Go
   workflow itself, CI runs `go test ./...` for module
   `github.com/paulpham157/sdf/go`. PRs that do not touch those paths
   skip the Go job.
2. **Dedicated workflow.** Add a separate GitHub Actions workflow (for
   example `.github/workflows/go.yml`) that is not a step inside the
   existing Python CI job. It uses a pinned Go toolchain, checks out the
   repo, and runs tests from the `go/` directory.
3. **ADR 0035 verification stays in CI.** The suite must include:
   - the million-message backpressure test
     (`TestBackpressure_MillionsStableGoroutinesAndRAM`), including its
     instrumented logging of throughput, peak goroutines, peak queue
     depth and heap;
   - cancellation chaos and idempotency tests;
   - metrics coverage that asserts `/metrics` (or the registry snapshot)
     exposes goroutine count and queue depth.
   Default `go test ./...` already runs these; do not mark the
   backpressure test as `-short`-skipped in CI. Use a job timeout long
   enough for the 2 M-message run (on the order of a few minutes).
4. **Secrets.** Rely on the existing gitleaks full-history job. Do not
   duplicate secret scanning in the Go workflow.

## Alternatives considered

1. **Run `go test` as a step in the existing Python CI workflow.**
   Rejected as the primary shape: Python jobs would pay Go setup cost on
   every PR, and Go failures would be harder to see next to `pytest`.
   Path filters on a dedicated workflow keep Python and Go signals
   separate.
2. **Always-on Go job with no path filter.** Rejected: most PRs are still
   Python-only; forcing Go on every change wastes runners without
   improving signal for those PRs.
3. **Only unit tests; skip the million-message backpressure test in CI.**
   Rejected: ADR 0035 lists that test as required proof for backpressure
   and stable goroutine/RAM behaviour; CI is where that proof must stay
   green.
4. **Add a Go-specific secret scanner.** Rejected: gitleaks already
   covers the repository.

## Consequences

- PRs that touch `go/` get a clear Go check; Python-only PRs stay on the
  existing workflow set.
- Maintainers must keep the Go toolchain pin and job timeout aligned with
  the backpressure test's runtime.
- A red Go job blocks merge of Go changes the same way a red `pytest`
  job blocks Python changes (subject to branch protection).
- Documentation and ADR-only edits under `docs/adr/` do not by themselves
  trigger Go CI unless the workflow file or `go/` also changes.

## Verification

1. Open a sample PR that only changes a file under `go/` (for example a
   comment in `go/internal/bus/bus.go`). Confirm the Go workflow runs and
   `go test ./...` is green, including
   `TestBackpressure_MillionsStableGoroutinesAndRAM` and the metrics
   test.
2. Open a sample PR that only changes a Python file and confirm the Go
   workflow is skipped.
3. Confirm gitleaks still runs on both PRs via the existing Security
   workflow.
4. When those checks behave as above, mark this ADR accepted on the
   merging commit of the workflow PR.
