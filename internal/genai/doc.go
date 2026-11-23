// Package genai holds every OpenTelemetry GenAI semantic-convention name SDF
// reads or emits: gen_ai.* attribute keys, gen_ai.usage.* token labels,
// metric names, gen_ai.operation.name values (which SDF also matches as span
// names), and gen_ai.token.modality values.
//
// # Pin
//
// The names are pinned to semantic-conventions-genai v0.1.0 (tag v0.1.0,
// commit d0e74ba), whose schema URL at the tag is
// https://opentelemetry.io/schemas/semconv-genai-dev/0.1.0-dev and which
// depends on core semantic conventions v1.45.0. The release's resolved
// registry is vendored under testdata/semconv-genai-v0.1.0 with SHA256SUMS,
// and the tests check every exported constant against it. Names that SDF
// must still read but v0.1.0 does not define (removed or renamed attributes,
// agent-specific spellings) live in legacy.go and are listed, with a reason,
// in the test's notInRegistry table.
//
// # Moving the pin
//
//  1. Download the new release's registry-dev.yaml and manifest-dev.yaml
//     (gh release download <tag> -R open-telemetry/semantic-conventions-genai)
//     into a new testdata/semconv-genai-<version> directory, write
//     SHA256SUMS with sha256sum, and check it against the asset digests from
//     gh api repos/open-telemetry/semantic-conventions-genai/releases/tags/<tag>.
//  2. Point vendorDir and vendoredSHA256 in registry_test.go at it and delete
//     the old directory.
//  3. Update the pin constants in pin.go: SemconvVersion, SemconvCommit
//     (git rev-parse <tag>^{commit}), SchemaURL (schema_url in the manifest
//     at the tag), CoreSemconvVersion/CoreSchemaURL (its dependencies), and
//     WeaverVersion (versions.env at the tag).
//  4. Run go test ./internal/genai. A constant whose name left the registry
//     fails; move it to legacy.go and add it to notInRegistry with a reason,
//     or follow the rename. A legacy name that the new registry defines also
//     fails; move it back out of legacy.go.
//  5. Adding a constant needs no test change as long as its Go name starts
//     with one of the prefixes TestConstantsExistInRegistry classifies.
package genai
