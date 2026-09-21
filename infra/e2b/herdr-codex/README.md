# E2B Herdr + Codex template

Pinned execution image for the SDF remote runtime:

- Herdr `0.9.1`
- Codex CLI `0.155.1`
- Node.js `22` runtime

The Herdr Linux x86_64 asset is SHA-256 verified during the image build.

Build from this directory:

```bash
e2b template create sdf-herdr-codex \
  --path infra/e2b/herdr-codex \
  --dockerfile Dockerfile \
  --ready-cmd 'herdr status server --json'
```

The image does not contain provider credentials. Inject the Codex connection
through the E2B/Herdr control plane at sandbox creation time.
