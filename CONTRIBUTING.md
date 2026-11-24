# Contributing

Pull request checks and the post-merge checks are different workflows. A push
to `main` does not repeat the pull request matrix. Main CI does not deploy or
publish a release. The Docs workflow deploys the documentation site when `docs/`
or the site config changes on `main`. Only the Release workflow creates a
release, as a draft, when a `v*` tag is pushed.

| Workflow | When | Why |
| --- | --- | --- |
| PR CI (`.github/workflows/pr.yml`) | `pull_request` opened, new commits (`synchronize`), or reopened | Fast feedback before merge: `gofmt` and staticcheck on the stable Go toolchain, `go vet` and `go test` on oldstable and stable, and a gitleaks scan of the commits in the pull request. |
| Main CI (`.github/workflows/main.yml`) | `push` to `main` | Fuller check of the merged tree: one stable `go test` (a squash of a stale branch can fail even when each side passed), `go test -race`, build `sdf` with `CGO_ENABLED=0` and run `tests/samples` through `sdf serve` over HTTP and gRPC then `sdf summary`, a Goreleaser snapshot uploaded as a workflow artifact, CodeQL, and gitleaks over main's history. |
| Release (`.github/workflows/release.yml`) | `push` of a `v*` tag, nothing else | `go test`, then `goreleaser release --clean`: archives for linux, darwin, and windows on amd64 and arm64 plus `checksums.txt`, uploaded to a draft GitHub Release that a maintainer publishes by hand. Only this job has `contents: write`. |
| Scheduled (`.github/workflows/schedule.yml`) | Mondays 03:17 UTC | Depends on time, not on a new commit: `govulncheck` (new advisories) and a gitleaks scan of every branch. |
| Docs (`.github/workflows/docs.yml`) | `pull_request` or `push` to `main` when `docs/`, `skills/`, or the site config changes; also `workflow_dispatch` | On a pull request, `mkdocs build --strict` only. On `main`, that same build is uploaded and deployed to GitHub Pages. The intended URL is `https://docs.softwaredecisionfabric.tech/` after the one-time setup below. Until then the site is at `https://paulpham157.github.io/sdf/`. |

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

## Documentation site

The Markdown under `docs/` is built with [MkDocs](https://www.mkdocs.org/)
and the [Material](https://squidfunk.github.io/mkdocs-material/) theme.
Pages render the existing files with no rewrite of their body text.
Mermaid fences, search, dark mode, and an Edit on GitHub link are enabled.
Skill files under `skills/` are included at build time so the relative
links already in the docs keep working.

Build or preview locally:

```bash
python3 -m venv .venv-docs
.venv-docs/bin/pip install -r requirements-docs.txt
make docs          # mkdocs build --strict
make docs-serve    # http://127.0.0.1:8000/
```

`--strict` fails the build on a broken internal link or missing anchor.
`.venv-docs/` and the generated `site/` directory are gitignored.

### One-time GitHub Pages setup

This is a maintainer step. The workflow does not enable Pages, create
DNS records, or change repository settings.

1. In the repository on GitHub: **Settings → Pages → Build and
   deployment → Source**, choose **GitHub Actions**.
2. Merge a change that triggers the Docs workflow on `main`, or run the
   **Docs** workflow from the Actions tab (`workflow_dispatch`) on
   `main`. The first deploy after step 1 is what publishes the site.
   Until a custom domain is attached, the site is at
   `https://paulpham157.github.io/sdf/`.
3. Custom domain (optional, intended production URL):
   - Create a DNS `CNAME` record for `docs.softwaredecisionfabric.tech`
     pointing at `paulpham157.github.io`.
   - In **Settings → Pages → Custom domain**, enter
     `docs.softwaredecisionfabric.tech` and wait for the TLS
     certificate. Turn on **Enforce HTTPS** once it is ready.
   - The built site already contains a `CNAME` file with that hostname.
4. If the GitHub `github-pages` environment is created with required
   reviewers, a maintainer has to approve the first deploy.

No extra token or Actions secret is required. The deploy job uses the
default `GITHUB_TOKEN` with `pages: write` and `id-token: write`.

