package genai

// Pin of the GenAI semantic conventions these names come from. See the
// package documentation for how to move it.
const (
	// SemconvVersion is the semantic-conventions-genai release.
	SemconvVersion = "0.1.0"
	// SemconvCommit is the commit the v0.1.0 tag points at.
	SemconvCommit = "d0e74ba6c48bcc4ded473b1828a8212f06b23753"
	// SchemaURL is the schema_url of the registry manifest at the tag. It
	// keeps the -dev suffix; v0.1.0 publishes no schema transformation.
	SchemaURL = "https://opentelemetry.io/schemas/semconv-genai-dev/0.1.0-dev"
	// CoreSemconvVersion is the core semantic-conventions release v0.1.0
	// depends on.
	CoreSemconvVersion = "1.45.0"
	// CoreSchemaURL is the schema URL of that core release.
	CoreSchemaURL = "https://opentelemetry.io/schemas/" + CoreSemconvVersion
	// WeaverVersion is the OTel Weaver release the v0.1.0 registry was
	// resolved with (versions.env at the tag).
	WeaverVersion = "v0.28.0"
)
