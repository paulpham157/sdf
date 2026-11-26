# User-typed prompt text

Checked against this repository on 2026-11-26. No live agent session was
read. Rules marked **specified** are the contract of this note and of
`internal/promptextract`, covered by synthetic tests. Rules marked
**verified** are already stated in this repository; the citation is the
source. **needs verification** means this note does not guess a path or a
schema, and someone still has to confirm it on that agent.

`internal/promptextract` is experimental. It is intentionally unwired from
`sdf serve`, the CLI, and the SQLite store. Wiring waits on a future ADR
under `docs/adr/` and on a live transcript for each agent. The package
stays pure Go, standard library only, and reads an `io.Reader`. It does
not open `~/.claude`. The default stays: prompt text is stored only with
an opt-in such as `--store-content`.

"User-typed prompt text" (phần text bạn gõ vào prompt) is the plain text a
person types into the prompt box and submits with Enter. The filter drops
a record with `isMeta`, `isSidechain`, or `isCompactSummary` set to JSON
`true`, drops `tool_result` blocks, strips the harness tags listed below,
and drops the two exact interrupt strings. Whether a live file uses
`isMeta` for a loaded skill or an internal command is **needs
verification**. The filter does not treat that as a fact. A slash command
or local-command payload that arrives inside the harness tags below is
removed even when `isMeta` is absent or JSON `false`.

## Status words

| Mark | Meaning |
| --- | --- |
| verified | Stated in this repository. The file is named next to the claim. |
| specified | Contract of this note and `internal/promptextract`. Synthetic tests cover it. Not checked against a live capture here. |
| needs verification | Confirm on that agent. Do not treat a guess as the schema. |

## Claude Code session JSONL

The path below is **needs verification**. It is commonly reported and is
not confirmed against a live file or against a path already written in
this repository:

```text
~/.claude/projects/<project-path-encoded>/<session-id>.jsonl
```

The JSONL keep and drop rules in the table below are **specified**. One
JSON object per line. The path is not cited in
[docs/agents.md](../../docs/agents.md) or in
[session-boundary.md](session-boundary.md). The repository's Claude Code
facts are the OTLP catalog and the session hooks. The encoding inside
`<project-path-encoded>` (which characters replace a slash, a colon, or a
space) is **needs verification**.

Keep a record when `type` is the JSON string `user` and `isMeta`,
`isSidechain`, and `isCompactSummary` are not JSON `true`. Skipping
`isMeta: true` is **specified**. What a live file uses that flag for
(a loaded skill, an internal command, or something else) is **needs
verification**. `isSidechain: true` is the filter's mark for a subagent
prompt, and `isCompactSummary: true` is its mark for an auto-compaction
summary. Both meanings are **needs verification** on a live file. The
filter skips the record when the flag is JSON `true` either way.

Drop `tool_result` blocks. Read plain text from `message.content` when it
is a JSON string, or from blocks whose `type` is the JSON string `text`
when `message.content` is an array. Strip the harness regions listed in
the behavior table.

Keys are exact. `isMeta` is not `IsMeta`. `sessionId` is not `session_id`.
`content` is not `Content`. `encoding/json` would fold those if the code
decoded into a struct; the filter decodes object keys into a map so the
spelling in the file is the spelling it matches.

A record the filter keeps can carry optional JSON strings `uuid`,
`sessionId`, and `timestamp`. A missing key or JSON `null` becomes an
empty string. Any other JSON type for those three fields is a line error.
A live file that stores `timestamp` as a number is **needs verification**.

### Record shapes

String content:

```json
{"type":"user","message":{"role":"user","content":"sửa bug ở hàm login"},"uuid":"u-1","sessionId":"sess-1","timestamp":"2026-11-26T15:04:05Z"}
```

Array content:

```json
{"type":"user","message":{"content":[{"type":"text","text":"sửa bug ở hàm login"}]}}
```

These two examples are synthetic, same as the tests. They are not a
capture.

### What the filter does with a line

`internal/promptextract.Extract` reads an `io.Reader`. It does not open a
path. Lines are split on `\n`. A trailing `\r` is stripped. A blank line
is skipped. The line number is 1-based and counts blank lines.

| Line | Result |
| --- | --- |
| `type` is not the JSON string `user` (`assistant`, `system`, `summary`, a missing type, a non-string type) | skipped |
| JSON `null` as the whole line | skipped |
| `type` is `user` and `isMeta`, `isSidechain`, or `isCompactSummary` is JSON `true` | skipped |
| those three flags missing, JSON `false`, or JSON `null` | the flag does not skip the record |
| any of those three flags any other JSON type | line error (`isMeta must be a boolean`, and the same sentence for the other two keys) |
| `message` missing or JSON `null` | skipped |
| `message.content` is a string | that string, after harness-tag stripping and trimming |
| `message.content` is an array | each object with `type` `text`; other types, including `tool_result`, are dropped |
| `message.content` any other JSON type, a non-object `message`, a non-object array element, or a `text` block whose `text` is not a JSON string | line error |
| text that is empty or only whitespace after stripping | skipped |
| text that is exactly `[Request interrupted by user]` or `[Request interrupted by user for tool use]` after trimming | skipped, on the whole prompt and on each text block before the join |
| a larger string that only contains one of those interrupt sentences | kept |
| two kept `text` blocks | joined with one `\n`, each block trimmed first |
| well-formed `<system-reminder>` … `</system-reminder>`, including nested pairs, a different ASCII case, and an opening tag whose attributes begin with a space or a tab | removed. A nested outer pair takes the tail inside it with it, so `tail</system-reminder>` does not leak |
| `<system-reminder>` with no closer, including inside one array block | left intact, and the text after the opener is kept. `how do I close <system-reminder> tags? also fix login` stays that string |
| well-formed `<command-name>`, `<command-message>`, `<command-args>`, `<local-command-stdout>`, `<local-command-stderr>`, `<local-command-caveat>`, `<bash-input>`, `<bash-stdout>`, `<bash-stderr>` | removed, exact lowercase, no attributes. An unclosed opener of one of these tags is left intact, same as an unclosed reminder |
| JSON that is not an object and not JSON `null`, or two JSON values on one line | line error `malformed json` |
| raw line longer than `MaxLineBytes` (32 MiB, including the newline) | line error `ErrLineTooLong`; the parser resumes at the next newline |
| more than `MaxTagDepth` (32) open tags of one name | the extra openers are literal text. Unclosed mentions do not fail the line and do not drop the prompt. When a closed nest is deeper than 32, Go strips only the 32 tracked openers, so up to N-32 closers and the text outside those tracked pairs can remain. If 32 unclosed openers already fill the cap, a later balanced pair is not pushed: its closer closes the 32nd opener still on the stack, and the cut runs from that opener through the closer |
| stream caps `MaxInputBytes` (256 MiB), `MaxPrompts` (10000), `MaxOutputBytes` (32 MiB of extracted text), or `MaxErrors` (1000) | the extract fails closed for the whole stream. The error is `ErrInputTooLarge`, `ErrTooManyPrompts`, `ErrOutputTooLarge`, or `ErrTooManyErrors`, and `Result` is empty. Prompts already collected are discarded |
| a read error from the underlying reader | returned; prompts and line errors already collected stay in the result |

A line error does not stop the scan. `Result.Errors` holds it.
`Result.Prompts` holds what was kept. `Extract`'s error return is a nil
reader, a non-positive limit, a stream cap, or a failure from the
underlying reader. A stream cap is not a partial success.

`MaxLineBytes` caps the raw length of one input line only. It is not the
peak memory for that line. Decoding one max-sized line keeps the raw
buffer, the JSON value, and the extracted string, so peak memory is
several times higher, about 3-5x `MaxLineBytes`. `bufio.Scanner`'s default
64 KiB token limit is too small for a session line, so the reader is
`bufio.Reader` and `ReadSlice('\n')`. A prompt longer than 32 MiB of raw
line would be reported as too long. Whether a real prompt reaches that
size is **needs verification**. A huge `tool_result` line over the cap is
also reported as too long, because the parser cannot classify a line it
refuses to hold. The stream caps above are what stop `Result.Prompts` and
`Result.Errors` from growing without a bound.

`MaxInputBytes` (256 MiB) fails closed for the entire stream: one byte
past the cap returns `ErrInputTooLarge` and an empty `Result`. The 256 MiB
figure is **needs verification** for a long session that is mostly base64.
A future ADR under `docs/adr/` is what picks the cap before this package
is wired in. Opening-tag attribute scans are capped (`maxTagAttrBytes`,
1024) and a failed scan does not reread a tail it has already proved
contains no `>`, so a line of repeated unclosed `<system-reminder` stays
linear.

`\r`-only line breaks are **needs verification**. They would arrive as one
line.

`Extract` returns raw, unredacted text. A prompt can contain pasted
credentials or other PII. The filter does not strip control characters,
ANSI escapes, NUL, or bidi overrides. A terminal or UI that prints the
text has to sanitize it. Any future persistence has to be opt-in, the
same kind of choice as `--store-content`, and any key that stores the
prompt has to be added to the content-key list in
`internal/record/redact.go` so the default redaction still drops it.
Extracted prompt text must never feed decision inference.

### Quick filter (jq)

This is a manual inspection filter for the same keep/drop rules, for a
file you already trust and that is small enough to read by hand. It is
not a parser for untrusted or large files: the regex strip can be
quadratic. It prints one prompt per kept record and does not print line
numbers. Where the parity test compares them, the text matches
`internal/promptextract`, including an opener that is only a space or a
tab, with no newline, no unquoted `<`, and no self-closing slash. The
list below is what still differs.

Go and jq differ where the record is not well-formed, and on the caps:

- `fromjson?` skips a malformed line and does not record a line number.
  Go records `malformed json`.
- A non-boolean `isMeta`, `isSidechain`, or `isCompactSummary` is skipped
  here and is a line error in Go.
- A non-object `.message` is skipped here (`objects` yields nothing) and
  is the line error `message must be an object` in Go. The guard is
  `(.message | objects | .content)`, so a non-object `.message` does not
  throw.
- A non-object element in a `content` array is skipped by `select` here
  and is the line error `message.content block must be an object` in Go.
- A `text` block whose `text` is not a JSON string is skipped by `select`
  here and is the line error `text block's text must be a string` in Go.
- `message.content` that is neither a string nor an array is skipped here
  and is a line error in Go.
- A bare JSON `null` line is skipped by both.
- `MaxLineBytes`, `MaxInputBytes`, `MaxPrompts`, `MaxOutputBytes`,
  `MaxErrors`, and `MaxTagDepth` are Go only. An opener past
  `MaxTagDepth` stays literal in Go. jq has no such cap and strips the
  whole nest.
- Crossed tags of different kinds are not a nest. For
  `<bash-stdout>a<command-name>b</bash-stdout>c</command-name>d`, Go
  leaves `c</command-name>d` and jq leaves `<bash-stdout>ad`, because jq
  removes `<command-name>` before `<bash-stdout>`. Do not use the quick
  filter to check a crossed line.
- A `<` inside a quoted attribute is left in place by jq, because the
  attribute class excludes `<`. Go allows that `<` and can strip the
  pair. An unquoted `<`, a newline (before the attributes or inside a
  quoted value), and a self-closing slash are left by both.
- The attribute cap is 1023 code points in jq and 1023 bytes in Go: the
  `>` has to fall inside the 1024-byte window that starts at the first
  space or tab. A multibyte attribute near that cap can differ.
- jq's closers do not allow whitespace before `>`. Go does, including a
  newline. An opener's attribute list is a space or a tab in both.
- `gsub`, not `index` plus a slice. jq's `index` on a string is a byte
  offset, and `.[n:m]` counts code points, so a slice at that offset cuts
  a multibyte prompt (the Vietnamese example) inside the tag.

```jq
def strip_harness:
  # Innermost balanced pair, repeated until the string stops changing.
  def once:
    # Space or tab, then at most 1022 more code points. A newline, a "<",
    # and a self-closing slash do not form an opener. A "<" inside quotes
    # is the case listed above where this still differs from Go.
    gsub("(?i)<system-reminder(?:[ \\t](?:[^<>\\n/]|/(?![ \\t]*>)){0,1022})?>(?:(?!</?system-reminder[\\s>])[\\s\\S])*?</system-reminder>"; "")
    | gsub("<command-name>(?:(?!</?command-name[\\s>])[\\s\\S])*?</command-name>"; "")
    | gsub("<command-message>(?:(?!</?command-message[\\s>])[\\s\\S])*?</command-message>"; "")
    | gsub("<command-args>(?:(?!</?command-args[\\s>])[\\s\\S])*?</command-args>"; "")
    | gsub("<local-command-stdout>(?:(?!</?local-command-stdout[\\s>])[\\s\\S])*?</local-command-stdout>"; "")
    | gsub("<local-command-stderr>(?:(?!</?local-command-stderr[\\s>])[\\s\\S])*?</local-command-stderr>"; "")
    | gsub("<local-command-caveat>(?:(?!</?local-command-caveat[\\s>])[\\s\\S])*?</local-command-caveat>"; "")
    | gsub("<bash-input>(?:(?!</?bash-input[\\s>])[\\s\\S])*?</bash-input>"; "")
    | gsub("<bash-stdout>(?:(?!</?bash-stdout[\\s>])[\\s\\S])*?</bash-stdout>"; "")
    | gsub("<bash-stderr>(?:(?!</?bash-stderr[\\s>])[\\s\\S])*?</bash-stderr>"; "");
  until(. as $s | once == $s; once);

def trim: sub("^\\s+"; "") | sub("\\s+$"; "");

def interrupt:
  . == "[Request interrupted by user]" or . == "[Request interrupted by user for tool use]";

def text:
  if . == null then empty
  elif type == "string" then
    strip_harness | trim | select(length > 0) | select(interrupt | not)
  elif type == "array" then
    [ .[]
      | select(type == "object" and .type == "text" and (.text | type) == "string")
      | .text | strip_harness | trim
      | select(length > 0)
      | select(interrupt | not)
    ]
    | join("\n")
    | select(length > 0)
    | select(interrupt | not)
  else empty
  end;

fromjson?
| select(type == "object" and .type == "user")
| select(
    (.isMeta == false or .isMeta == null)
    and (.isSidechain == false or .isSidechain == null)
    and (.isCompactSummary == false or .isCompactSummary == null)
  )
| (.message | objects | .content)
| text
```

```bash
jq -Rr -f user-prompt.jq ~/.claude/projects/<project-path-encoded>/<session-id>.jsonl
```

Save the program as `user-prompt.jq`. `-R` feeds one raw line at a time so
a bad line does not abort the rest of the file. The path in that command
is the **needs verification** path above.

### Edge cases still to verify

These are the cases a live Claude Code file has to answer. The synthetic
tests lock today's filter behavior. They do not prove a live file has
that shape. Every row in this table is **needs verification** on a live
transcript.

| Case | What the filter does today | What to confirm on a live file |
| --- | --- | --- |
| `isMeta` | JSON `true` skips the record. Missing, `false`, and `null` do not. The filter does not claim the flag means a loaded skill or an internal command. | Record what a live file sets `isMeta` on. Do not treat a guess as the schema. |
| Slash commands and `<command-name>`, `<command-message>`, `<command-args>` | A well-formed lowercase pair is removed even when `isMeta` is absent or `false`. Slash-command arguments inside `<command-args>` are dropped with the tag. An unclosed opener stays, with the text after it. Attributes and a different case are not stripped. | Confirm the live tag spelling, whether the record is also `isMeta: true`, and whether the tags nest or carry attributes. |
| `<local-command-stdout>`, `<local-command-stderr>`, `<local-command-caveat>` | Same rule as the command tags. The payload is not user-typed text. | Confirm the live spelling and that the payload is harness output. |
| `<bash-input>`, `<bash-stdout>`, `<bash-stderr>` | A well-formed lowercase pair is removed, so shell output (which can hold secrets or environment variables) is not returned as user-typed text. An unclosed opener stays. | Confirm the live spelling. If a person pasted one of these tags on purpose, today's rule cannot tell that from harness output. |
| `isSidechain` | JSON `true` skips the record. The filter treats that as a subagent prompt. | Confirm a live subagent record sets this flag, and that a person-typed prompt does not. |
| `isCompactSummary` | JSON `true` skips the record. The filter treats that as an auto-compaction summary. | Confirm a live compaction record sets this flag. |
| `[Request interrupted by user]` and `[Request interrupted by user for tool use]` | Skipped only when the trimmed block, or the trimmed prompt, is exactly one of those two strings. A larger sentence that contains the words is kept. | Confirm the live harness uses those exact strings and does not wrap them. |
| IDE context tags | Unknown tags stay in the text. This repository does not name the tags an IDE injects (open file, selection, and the like). | Copy the tag spelling from a live record. Do not invent one in the filter first. |
| User-pasted `<system-reminder>` | A well-formed pair is removed, including one the person pasted on purpose, including nested pairs, a different ASCII case, and an opener with attributes. The text on either side remains. An unclosed `<system-reminder>` stays, and the text after it stays (`how do I close <system-reminder> tags? also fix login`). | Decide whether pasted text must survive. Today's rule cannot tell a pasted pair from a harness pair. Confirm the live tag's case and attributes. |
| Mixed `tool_result` and `text` in one array | `tool_result` blocks are dropped. Sibling `text` blocks are kept. A `text` field sitting on a `tool_result` block is not read. | **Open question before wiring.** Confirm on a live transcript that tool, web, and repo content never lands in a sibling `text` block. If it does, the sibling text is not user-typed and must not be kept. |
| Nested `<system-reminder>` | The outer closer ends the outer opener. `<system-reminder>outer <system-reminder>inner</system-reminder> tail</system-reminder>sửa bug` becomes `sửa bug`. Openers past 32 of the same name stay literal, so a pile of unclosed mentions does not drop the prompt. jq strips a deeper closed nest entirely; Go strips only the openers it tracked. | Confirm the harness does not nest the tag, and how deep a live file nests it. |
| Tag spelling | `<system-reminder>` matches ASCII case-insensitively. Attributes on the opener begin with a space or a tab (`' '` / `'\t'`), not a newline. A newline anywhere in that opener, including inside quotes, means it is not a tag. A closer may still have ASCII whitespace, including a newline, before `>`. The command, local-command, and bash tags are exact lowercase with no attributes. A self-closing tag is not stripped, including a space or tab before the slash (`<system-reminder />`, `<system-reminder a="1"/>`). | Confirm the live spelling of each tag. |
| `timestamp` / `uuid` / `sessionId` types | JSON strings. A number is a line error. `session_id` is ignored. | Confirm the live key names and JSON types. |
| Other `type` values | Anything other than the string `user` is skipped, including a non-string `type` and values this note does not list. | List the `type` strings a real file contains (`assistant` and `summary` are the ones the tests name, plus `system`). |
| Project-path encoding | Not implemented. The filter never opens the path. | Record one real directory name next to the project path it came from. |

## Cross-agent verification matrix

The eight agents `sdf init` accepts are `claude-code`, `codex`, `opencode`,
`pi`, `qwen`, `grok`, `gemini`, and `copilot`
([docs/agents.md](../../docs/agents.md), and the same list in
`internal/cli/agentlist_test.go`). Cursor is in the decision-hook tables
and is not one of those eight, so it is outside this matrix.

OTLP facts below are **verified** from the docs. A local transcript path
is listed only when this repository already states it. Every other local
path is **needs verification** on purpose.

| Agent | Local transcript in this repo | OTLP user-prompt text in this repo | Meta / injected markers in this repo | Extractor |
| --- | --- | --- | --- | --- |
| claude-code | **needs verification.** The path above is commonly reported and is not confirmed against a live file or a path in this repository. The JSONL keep/drop rules are **specified**. | **verified.** Log `claude_code.user_prompt`. Prompt text stays redacted unless `OTEL_LOG_USER_PROMPTS`. The published setup does not set it. sdf still drops the text unless `--store-content`. [docs/agents.md](../../docs/agents.md) Claude Code section. | **specified** that JSON `true` for `isMeta`, `isSidechain`, and `isCompactSummary` skips the record. What a live file uses those flags for is **needs verification**. Not described on the OTLP event. | `internal/promptextract`, experimental, unwired, synthetic tests only. Wiring waits on an ADR and a live transcript. |
| codex | **needs verification.** Hook payloads include `transcript_path` ([session-boundary.md](session-boundary.md), schemas at `69231e11`). The path value and the file format are not described. | **verified.** Log `codex.user_prompt`. Prompt text is `[REDACTED]` unless `otel.log_user_prompt = true`. User prompt text is not copied onto the trace-safe event. [docs/agents.md](../../docs/agents.md) Codex section. | **needs verification** for a local file. The documented OTLP marker is the `[REDACTED]` placeholder. | none |
| opencode | **needs verification.** This repository does not name a session file. | **verified** as a limit. v2 exports runner spans with `session.id` and process logs. It has no `user_prompt` catalog. v1's `experimental.openTelemetry` flag is ignored by v2. [docs/agents.md](../../docs/agents.md) OpenCode section. The receiver still drops `ai.*` keys because that v1 stream sent `ai.prompt` and the rest ([docs/receiver.md](../../docs/receiver.md) content storage). Whether v2 puts prompt text on a span is **needs verification**. | **needs verification.** | none |
| pi | **verified** file *name* only: `{timestamp}_{sessionId}.jsonl` ([session-boundary.md](session-boundary.md); [docs/receiver.md](../../docs/receiver.md) `PI_SESSION_FILE`, basename with `.jsonl` removed). The directory, and the JSONL schema for user text, are **needs verification**. An ephemeral session has no file. | **verified** as a limit. pi-otel logs are `pi.session.start` and `pi.session.end` only. The agents doc says to leave content capture off and does not name a prompt attribute on `chat` spans. | **needs verification.** | none |
| qwen | **needs verification.** `~/.qwen/settings.json` is settings, not a transcript. | **verified** from the v0.28.4 docs mapping, not a live capture. Events are `qwen-code.*` and include `user_prompt`. `logPrompts` defaults to true and puts `prompt`, `request_text`, `response_text`, and `function_args` on logs. `request_text` can hold file contents and reads of `~/.qwen/memories/`. `sdf init qwen` sets `logPrompts` false. `includeSensitiveSpanAttributes` defaults to false and, when true, copies messages and tool data into spans; `sdf init qwen` forces it back to false when it finds it on ([docs/agents.md](../../docs/agents.md) Qwen section, `internal/record/redact.go`). Every `tool_call` still sends `function_args`. sdf drops those keys unless `--store-content`. Which key on the `user_prompt` event is the typed sentence is **needs verification**. | **verified** that log text is broader than the typed prompt (`request_text`, memories, tool arguments) and that `includeSensitiveSpanAttributes` is a separate span-content switch. Slash commands and skill bodies in a local file are **needs verification**. | none |
| grok | **needs verification.** No session-file path is documented. A test fixture uses the string `transcriptPath` as a decoy value (`internal/setup/grok_test.go`); that is not a schema. | **verified.** Logs `grok_code.*` include `user_prompt`. Prompt text is off unless `otel_log_user_prompts` / `OTEL_LOG_USER_PROMPTS`. `sdf init grok` pins the config keys false. The span name `agent.prompt` is a turn span, not the prompt body. [docs/agents.md](../../docs/agents.md) Grok section. | **needs verification** for a local file. | none |
| gemini | **needs verification.** `~/.gemini/settings.json` is settings, not a transcript. | **verified** from the published telemetry doc and source at `eecccfcd`, not a live capture. Log event `gemini_cli.user_prompt`. `logPrompts` defaults to true and adds `prompt`, `request_text`, `response_text`, and `function_args`. The log body is a sentence such as a tool-call summary, not the prompt. Span prompts require `telemetry.traces=true`. sdf drops the text unless `--store-content`. Whether `prompt` is only the typed sentence is **needs verification**. | **needs verification** for harness text inside those attributes. | none |
| copilot | **needs verification.** No session-file path is documented. `~/.copilot/hooks/` is a hook directory. `COPILOT_OTEL_FILE_EXPORTER_PATH` in tests is an OTLP file exporter, not a transcript. | **verified** from the docs and `copilot help monitoring` 1.0.114, not a live capture. Traces and metrics, no logs. `OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT` defaults to `NO_CONTENT`. `SPAN_ONLY` and `SPAN_AND_EVENT` put prompts on spans. `EVENT_ONLY` puts them on span events, which sdf does not store. The attributes are `gen_ai.input.messages`, `gen_ai.output.messages`, `gen_ai.system_instructions`, and the tool-call keys. Each user message is one `invoke_agent` span. Separating the typed sentence from system and tool messages inside `gen_ai.input.messages` is **needs verification**. | **verified** that the capture, when on, is the GenAI message list and not a single typed-text field. Local injected markers are **needs verification**. | none |

### What to do on each agent

Do this without committing a capture. `tests/samples/` stays hand-written
OTLP marked `sdf.fixture=sample`.

1. Find whether the agent writes a local transcript. If it does, record
   the path pattern and the file format. If it does not, write that down.
   Do not fill a gap with a path from memory.
2. Submit one short prompt whose text is known, then one slash command,
   then one turn that uses a tool. Note which records are the typed
   prompt, which are tool output, and which are injected.
3. Record the marker for injected text (`isMeta`, `isSidechain`,
   `isCompactSummary`, a tag, a redaction placeholder, or none).
4. Compare that typed sentence with the OTLP field named above, with the
   agent's own prompt switch off and then on. sdf's store should still
   have dropped it unless `--store-content` was set for that run.
5. Keep the notes in this file. Update the matrix cell from **needs
   verification** to **verified**, with the version and the date.

Claude Code's extra pass is the edge-case table above, on a real
`~/.claude/projects/.../*.jsonl`, fed to `promptextract.Extract` and to
the jq filter. The two should agree on every well-formed user record.
Line errors belong to the Go result. Do not wire the package until that
pass and an ADR under `docs/adr/` both exist.

## Trust boundaries and privacy

**verified** for the collector as it is now:

- sdf drops prompt, response, tool, and error text before storing, for
  every agent, whatever the agent was configured to send. The switch is
  `sdf serve --store-content` or `SDF_STORE_CONTENT`. Rows kept that way
  are marked `sdf.content=kept`. The key list is
  [docs/receiver.md](../../docs/receiver.md) (Content storage) and
  `internal/record/redact.go`. Content under a key sdf does not know is
  still stored; see that content-storage section and
  [dev-docs/gaps.md](../gaps.md).
- The SQLite file is created mode 0600. Group and other bits are stripped
  from an existing file when the process can change the mode
  ([docs/security.md](../../docs/security.md)).
- With the flag off, a prompt that an agent did send is not in the store.
  Turning an agent's own switch on (`logPrompts`,
  `includeSensitiveSpanAttributes`, `OTEL_LOG_USER_PROMPTS`,
  `otel.log_user_prompt`, `OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT`)
  changes what is sent. It does not change what sdf keeps.

**specified** for this change:

- `internal/promptextract` is pure Go, standard library only, and reads an
  `io.Reader`. It does not open `~/.claude`, does not import the store,
  and is not called from `cmd/sdf` or `internal/cli`. A unit test walks
  the module and fails if another package imports it. It is experimental
  and stays unwired until an ADR and live transcripts say otherwise.
- `Extract` returns raw, unredacted text. It may contain pasted
  credentials or other PII. It does not strip control characters, ANSI
  escapes, NUL, or bidi overrides. A terminal or UI that prints a prompt
  has to sanitize that output.
- Tests under `internal/promptextract` are synthetic. They are not live
  session files.
- Extracted prompt text must never feed decision inference.

A later change that wires this filter in has to stay opt-in, the same
kind of choice as `--store-content`. Any key that stores the prompt has
to be added to the content-key list in `internal/record/redact.go` so the
text never bypasses default redaction. The database that holds the text
has to stay owner-readable only, as the telemetry store already is. The
default remains: do not store prompt text.

A later change that reads a session file off disk, which this package
does not do, has to meet this checklist before it opens anything:

- Reject a symlink (`O_NOFOLLOW`, or an equivalent containment check that
  the opened file stays inside the tree the user named).
- Reject path encodings that escape the tree (slash, colon, and other
  characters a project-path encoding might smuggle into a relative path).
- Check uid and ownership, and refuse a file that is group-writable or
  world-writable.
- Open and `fstat` the same descriptor, so a swap between the check and
  the read is not a TOCTOU hole.
- A live file can still be growing. A partial final line that has no
  newline yet has to be handled, not parsed as a finished record.
- Scrub secrets and other PII inside the extracted prompt text before
  anything is stored or shown. `Extract` itself does not scrub.

Extracted text is data from the session, not a configuration file and not
an instruction to sdf. A prompt can contain a literal `<system-reminder>`
or a command tag. The filter's only opinion about those strings is the
table above.
