# semantic-conventions-genai v0.1.0 (vendored)

These files are the release assets of
[open-telemetry/semantic-conventions-genai v0.1.0](https://github.com/open-telemetry/semantic-conventions-genai/releases/tag/v0.1.0)
(tag `v0.1.0`, commit `d0e74ba6c48bcc4ded473b1828a8212f06b23753`), copied
byte for byte:

- `registry-dev.yaml`: the Weaver-resolved registry (`file_format: resolved/2.0`).
  It holds the attribute catalog plus every metric, event, span, and entity
  defined at the tag. Its `schema_url` is
  `https://opentelemetry.io/schemas/semconv-genai-dev/0.1.0-dev`, and its only
  dependency is core semconv `https://opentelemetry.io/schemas/1.45.0`.
- `manifest-dev.yaml`: the registry manifest from the same release.

`SHA256SUMS` matches the `digest` GitHub reports for each release asset.
`internal/genai` tests check the files against it, and check every name in
the package against `registry-dev.yaml`. Do not edit these files by hand; see
the package documentation in `internal/genai/doc.go` for how to move the pin.
