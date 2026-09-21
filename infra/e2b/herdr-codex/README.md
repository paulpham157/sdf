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

The image also contains a deployment-owned Herdr bridge at
`POST /v1/command` on port `8787`. Set `HERDR_ENDPOINT_TOKEN` or mount a secret
at `HERDR_ENDPOINT_TOKEN_FILE` (default `/run/secrets/herdr_endpoint_token`)
when creating the sandbox; the bridge rejects unauthenticated requests, shell
commands, oversized bodies, and timeouts over two minutes. The SDF client uses
`HerdrEndpointTransport` against the HTTPS-forwarded endpoint.
