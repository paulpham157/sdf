// Package promptextract pulls user-typed prompt text out of a Claude Code
// session JSONL stream.
//
// The package is experimental. It is intentionally unwired from sdf serve,
// the CLI, and the SQLite store. Wiring still waits on an ADR under
// docs/adr/, on the Claude edge cases that are still open, and on the
// default that prompt text is stored only with an opt-in such as
// --store-content. A well-formed Claude Code 2.1.332 pass (Linux,
// non-interactive, 2026-11-27) is recorded in
// dev-docs/research/user-prompt-extraction.md. This package stays
// Claude-only: it does not read the other seven agents. Nothing in this
// module imports it. Extract reads an io.Reader and does not open
// ~/.claude.
//
// What the filter drops, on the rules in
// dev-docs/research/user-prompt-extraction.md: a record whose type is not
// the JSON string "user"; a user record with isMeta, isSidechain, or
// isCompactSummary set to JSON true; tool_result blocks; balanced
// <system-reminder> regions (ASCII case-insensitive, attributes allowed on
// the opener); balanced <command-name>, <command-message>, <command-args>,
// <local-command-stdout>, <local-command-stderr>, <local-command-caveat>,
// <bash-input>, <bash-stdout>, and <bash-stderr> regions; and text that is
// exactly "[Request interrupted by user]" or "[Request interrupted by user
// for tool use]". An unclosed <system-reminder> is left in place, so text
// after it is kept. Whether a live file marks skills or internal commands
// with isMeta is needs verification. This package does not treat that as
// a fact.
//
// Extract returns raw, unredacted text. A prompt can contain pasted
// credentials or other PII. Extract does not strip control characters,
// ANSI escapes, NUL, or bidi overrides. A terminal or UI that prints the
// text has to sanitize it. Any future persistence has to be opt-in, the
// same kind of choice as --store-content, and any key that stores the
// prompt has to be added to the content-key list in internal/record/redact.go
// so the default redaction still drops it. Extracted prompt text must
// never feed decision inference.
//
// MaxLineBytes caps the raw length of one input line. Peak memory for a
// line of that size is about 3-5x higher because of JSON decoding. The
// stream caps (MaxInputBytes, MaxPrompts, MaxOutputBytes, MaxErrors) fail
// closed with an empty Result.
package promptextract
