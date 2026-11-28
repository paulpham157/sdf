// Package live measures decision and link recall by driving the real
// agent CLIs SDF records. It is opt-in. A run with no API key does not
// call the network, and go test does not call a provider.
//
// Each supported CLI is configured to export OTLP to a temporary sdf
// serve, to load the shipped sdf-decisions skill, and to call a model
// through a localhost proxy. The proxy forwards to an OpenAI-compatible
// base (Chat Completions and Responses) or an Anthropic Messages base,
// retries HTTP 429 and 5xx, and holds the API key. Config files name an
// environment variable for the key and do not contain it.
//
// Gemini CLI is unsupported with this provider: it speaks Gemini
// generateContent, and TokenRouter has no working generateContent
// endpoint. Codex is unsupported too: TokenRouter returns HTTP 400 when a
// Responses request includes tool_choice or reasoning, and Codex has no
// setting that omits those fields. The runner reports those reasons and
// does not invent a session.
package live
