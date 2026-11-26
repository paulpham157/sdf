// Package promptextract pulls user-typed prompt text out of a Claude Code
// session JSONL stream.
//
// The text is the plain text a person types into the prompt box and submits.
// Loaded skills, internal commands, and other harness messages are marked
// isMeta and are skipped. tool_result blocks are skipped. <system-reminder>
// regions are removed. The rules and the cases that still need a live
// session are written down in dev-docs/research/user-prompt-extraction.md.
//
// Extract reads an io.Reader. It does not open ~/.claude, and nothing in
// sdf serve, the CLI, or the SQLite store calls it. Prompt text stays out
// of the store unless a future opt-in, the same kind of choice as
// --store-content, decides otherwise.
package promptextract
