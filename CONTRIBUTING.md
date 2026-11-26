# Contributing

Pull request checks and the post-merge checks are different workflows. A push
to `main` does not repeat the pull request matrix. Main CI does not deploy or
publish a release. The Docs workflow deploys the documentation site when `docs/`
or the site config changes on `main`. Only the Release workflow creates a
release, as a draft, when a version tag matching `v[0-9]+.[0-9]+.[0-9]+*`
(for example `v0.1.0` or `v0.1.0-rc.1`, not `v1`) is pushed. Publishing a
stable release (not a draft or prerelease, and a tag that is exactly
`vMAJOR.MINOR.PATCH`) opens a pull request that updates `CITATION.cff`. The
Citation workflow dispatches a gitleaks scan of that pull request's
`base..head` range and waits for it. It posts the ruleset status and merges
only when the repository variable `CITATION_AUTOMERGE` is exactly `true`.
That variable is off until a maintainer sets it. Cite this repository reads
the default branch, so it stays stale until that pull request merges.

| Workflow | When | Why |
| --- | --- | --- |
| PR CI (`.github/workflows/pr.yml`) | `pull_request` opened, new commits (`synchronize`), or reopened | Fast feedback before merge: `gofmt` and staticcheck on the stable Go toolchain, `go vet` and `go test` on oldstable and stable, a `release toolchain (go.mod)` job that checks the `toolchain` line against the Go binary goreleaser would use, and a gitleaks scan of the commits in the pull request. |
| Main CI (`.github/workflows/main.yml`) | `push` to `main` | Fuller check of the merged tree: one stable `go test` (a squash of a stale branch can fail even when each side passed), `go test -race`, build `sdf` with `CGO_ENABLED=0` and run `tests/samples` through `sdf serve` over HTTP and gRPC then `sdf summary`, a Goreleaser snapshot uploaded as a workflow artifact, CodeQL, and gitleaks over main's history. |
| Release (`.github/workflows/release.yml`) | `push` of a tag matching `v[0-9]+.[0-9]+.[0-9]+*`, nothing else | `go test`, then `goreleaser release --clean`: archives for linux, darwin, and windows on amd64 and arm64 plus `checksums.txt`, uploaded to a draft GitHub Release that a maintainer publishes by hand. This job has `contents: write` to upload the draft. |
| Citation (`.github/workflows/citation.yml`, `.github/workflows/citation-gitleaks.yml`) | a GitHub Release is published, and it is not a draft or prerelease | Opens `citation/vMAJOR.MINOR.PATCH` from the default branch and a pull request that sets `version` (no leading `v`), `date-released` (the UTC calendar date of `published_at`), and the release tag's peeled commit in `CITATION.cff`. Skips drafts, prereleases, tags that are not `vMAJOR.MINOR.PATCH`, a file that already matches, and a release older than the version already in the file. The release tag is not moved. A `GITHUB_TOKEN` pull request does not start PR CI, so the Citation job dispatches `citation-gitleaks.yml` on that branch. The scan is the same `base..head` range as PR CI, not a scan of every branch. GitHub does not count a `workflow_dispatch` job check toward a ruleset. The ruleset check is named `gitleaks (full history)` because that is the PR CI job name. The job posts a commit status of that name and merges only when the repository variable `CITATION_AUTOMERGE` is exactly `true`. Otherwise it leaves the pull request for a human. Cite this repository stays stale until that merge; confirm the pull request merged before sending people to Cite. `contents: write` on that job is repo-wide. The updater refuses to point the contents API at the default branch; the permission itself does not. `pull-requests: write`, `actions: write`, and `statuses: write` are the other permissions on that job. The token is not left in git config. A merge made with `GITHUB_TOKEN` does not start Main CI or the push gitleaks job. The concurrency group cancels a queued citation job and does not cancel one that has already started. If opening the pull request returns 403, turn on Settings → Actions → General → Workflow permissions → Allow GitHub Actions to create and approve pull requests, then re-run; the branch is already written. |
| Scheduled (`.github/workflows/schedule.yml`) | Mondays 03:17 UTC | Depends on time, not on a new commit: `govulncheck` (new advisories) and a gitleaks scan of every branch. |
| Docs (`.github/workflows/docs.yml`) | `pull_request` or `push` to `main` when `docs/`, `skills/`, or the site config changes; also `workflow_dispatch` | On a pull request, `mkdocs build --strict` only. On `main`, that same build is uploaded and deployed to GitHub Pages. The intended URL is `https://docs.softwaredecisionfabric.tech/` after the one-time setup below. Until then the site is at `https://paulpham157.github.io/sdf/`. |

[![Repository workflows: PR CI, Main CI, Docs (site build and Pages deploy), Release (version-tag draft), and the Monday scheduled scan.](docs/diagrams/09-ci-workflows.png)](docs/diagrams/09-ci-workflows.png)

Source: [09-ci-workflows.tldr](docs/diagrams/09-ci-workflows.tldr) (open in [tldraw](https://www.tldraw.com); the official file extension is `.tldr`).

The branch ruleset (id 26272056) requires the check name `gitleaks (full history)`. On a
human pull request that check is the PR CI job, and the scan is that pull
request's commit range, not every branch. The citation scan is the same
`base..head` range. The check's name is the ruleset context; it does not
mean the citation workflow scans every branch. The ruleset requires that
check before the default branch updates. It does not pin the check to the
GitHub Actions app (integration 15368), so a commit status with that
context satisfies it. Any token with `statuses: write` can post that
status. When `CITATION_AUTOMERGE` is exactly `true`, the Citation workflow
posts it on the citation head SHA only after a fresh
`GET /repos/{owner}/{repo}/actions/runs/{id}` reports the dispatched run as
`completed` / `success` for that head SHA and that base SHA, on that
citation branch, for `.github/workflows/citation-gitleaks.yml`, and only
after the branch tip is still the verified `github-actions[bot]` citation
commit whose `CITATION.cff` bytes are the ones just written. It then
merges, and it reads the tip again immediately before the merge. That
merge is how `CITATION.cff` lands on the default branch. The merge also
reads every page of the pull request file list and stops unless the list
is exactly one file, `CITATION.cff`, with status `modified` and no
previous filename. A `GITHUB_TOKEN` merge does not start Main CI or the
push gitleaks job on `main`.

An existing `citation/vMAJOR.MINOR.PATCH` branch is reused when its tip is
the default branch, or when it is strictly behind that branch. A branch
that is behind is fast-forwarded to the default branch. A branch that is
ahead is reused only when every commit the default branch does not already
contain is a verified single-parent commit authored and committed by
`github-actions[bot]` with this updater's message. A diverged branch, a
commit by anyone else, or a compare the API does not return in one page
fails the job and leaves the ref in place. The ref is not force-pushed.

The job's permissions are `contents: write`, `pull-requests: write`,
`actions: write`, and `statuses: write`. `contents: write` is repo-wide.
Only `putCitation` refuses the default branch. The workflow grants nothing
else. `CITATION_AUTOMERGE` is off by default, so the status is not posted
and the pull request is not merged. This workflow cannot change the
ruleset. Before setting `CITATION_AUTOMERGE` to `true`, pin ruleset
26272056's required check `gitleaks (full history)` to GitHub Actions
(integration 15368), or explicitly accept the unpinned-check risk.
Pinning that check to GitHub Actions breaks the post-and-merge path
unless a bypass is configured: a commit status posted with `statuses: write`
is not a check from the GitHub Actions app, so the ruleset will reject the
merge.

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
Mermaid fences and search are enabled. Each page has a View on GitHub
link to that page's source file. The header has a Website link to the
product site, on desktop and on a phone. That control is
`docs/overrides/partials/header.html`, a copy of the mkdocs-material 9.8.2
header partial, and it has to be re-synced when Material is upgraded. The
theme follows `design/tokens.css` (see [`design/tokens.md`](design/tokens.md)), including
a dark scheme the reader can select. The app UI stays light.
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

Do this **before** merging a change that deploys the docs site to
`main`. The workflow does not enable Pages, create DNS records, or
change repository settings. If Pages is not enabled first, the deploy
job on the merge commit will fail; after you enable it, re-run the
Docs workflow on `main` (`workflow_dispatch`).

1. In the repository on GitHub: **Settings → Pages → Build and
   deployment → Source**, choose **GitHub Actions**.
2. After Pages is enabled, merge the docs-site change (or any later
   change that triggers the Docs workflow) to `main`, or run the
   **Docs** workflow from the Actions tab (`workflow_dispatch`) on
   `main`. Until a custom domain is attached in the Pages settings,
   the site is at `https://paulpham157.github.io/sdf/`.
3. Custom domain (optional, intended production URL):
   - Create a DNS `CNAME` record for `docs.softwaredecisionfabric.tech`
     pointing at `paulpham157.github.io`.
   - In **Settings → Pages → Custom domain**, enter
     `docs.softwaredecisionfabric.tech` and wait for the TLS
     certificate. Turn on **Enforce HTTPS** once it is ready.
   - Do not add a `CNAME` file to the built site. Setting the domain
     only in the Pages settings keeps
     `https://paulpham157.github.io/sdf/` working as a fallback.
4. If the GitHub `github-pages` environment is created with required
   reviewers, a maintainer has to approve the first deploy.

No extra token or Actions secret is required. The deploy job uses the
default `GITHUB_TOKEN` with `pages: write` and `id-token: write`.

## Developer and internal documentation

The site at <https://docs.softwaredecisionfabric.tech/> is for people
learning and using `sdf`. The notes below stay in this repository.
`CONTRIBUTING.md` is their index. Skill pages live in `skills/` and are
published under Reference, because they tell an agent how to record and
check decisions. Record user-visible changes in `CHANGELOG.md` under
`[Unreleased]`.

| Document | What it covers |
| --- | --- |
| [design/tokens.md](design/tokens.md) | Shared cream, charcoal, and rust tokens, and how the UI, the docs site, and the landing page use them. Sits beside `design/tokens.json`, `design/tokens.py`, and `design/tokens.css`. |
| [dev-docs/gaps.md](dev-docs/gaps.md) | Known limits of the collector and the summary |
| [dev-docs/research/session-boundary.md](dev-docs/research/session-boundary.md) | How each agent marks session open and close |
| [dev-docs/diagrams.md](dev-docs/diagrams.md) | How to open, edit, and re-export the tldraw diagrams. The `.tldr` sources and PNG renders stay in `docs/diagrams/` because the user guides embed them. |
| [docs/adr/README.md](docs/adr/README.md) | Architecture decision records. [ADR 0001](docs/adr/0001-diagram-tool-excalidraw-vs-tldraw.md) proposes keeping tldraw for those diagrams. |

When a limit or a session-boundary fact changes, update the matching file
here and the user guide in `docs/` that describes the behavior.

