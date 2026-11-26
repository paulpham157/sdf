# AGENTS.md

SDF is a Go collector. It receives OpenTelemetry from coding agents and
summarizes a session. It does not launch or sandbox agents.

Go 1.26 or newer. The SQLite driver is pure Go (`modernc.org/sqlite`), so keep
`CGO_ENABLED=0`. `make build` does.

```bash
make build         # static binary ./sdf
make test          # go test ./...
make fmt-check     # gofmt -l, fails if anything differs
make staticcheck   # staticcheck 2026.2.2 (honnef.co/go/tools v0.8.2)
make vet
make race          # go test -race
make e2e           # builds ./sdf, then tests/e2e over HTTP and gRPC
```

`make e2e` needs the built binary. Plain `go test ./...` does not run it,
because that package builds only with `-tags e2e`.

CI runs `fmt-check`, `staticcheck`, `vet`, and `test` on a pull request, and
adds `race` and `e2e` after merge. Neither deploys nor publishes a release;
`release.yml` creates a draft GitHub Release only when a `v*` tag is pushed.
Details are in `CONTRIBUTING.md`.

`tests/samples/` is hand-written OTLP marked `sdf.fixture=sample`. Do not
replace it with a capture from a live agent.

Docs: `README.md` is the short overview. `docs/` is what the public site
publishes (what each agent exports, the receiver, content storage and
`sdf redact`, the summary and computed cost, outcomes, decision records
and the local budget, `sdf check`, tasks and `sdf verify`, the ledger and
`sdf audit`). Known gaps (`dev-docs/gaps.md`), session-boundary research
(`dev-docs/research/session-boundary.md`), user-prompt extraction
(`dev-docs/research/user-prompt-extraction.md`, with the experimental
filter in `internal/promptextract`), design tokens
(`design/tokens.md`), and diagram authoring (`dev-docs/diagrams.md`) stay
in the repository. The index is in `CONTRIBUTING.md`. Skill pages live
in `skills/` and are published under Reference. Record user-visible
changes in `CHANGELOG.md` under `[Unreleased]`. When behavior changes,
update the doc that describes it rather than growing the README.
