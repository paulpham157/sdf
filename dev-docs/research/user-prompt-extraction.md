# User-typed prompt text

Checked against this repository on 2026-11-26. That pass is the OTLP
baseline. A later capture, on 2026-11-27, did not re-verify OTLP fields.

That 2026-11-27 capture ran on Linux, under an isolated `$HOME`, in
non-interactive mode, through a multi-protocol API gateway
(Anthropic-compatible root for Claude, OpenAI-compatible for
Codex/OpenCode/Pi/Qwen/Grok, Gemini protocol for Gemini; Copilot never
reached the gateway). Eight CLIs were tested: six full 3-turn captures
(Claude Code, Codex, OpenCode, Pi, Qwen, Grok), one partial capture
(Gemini), and one auth-blocked run (Copilot). Raw captures are not
committed. Local filename timestamps came from a UTC+7 box clock. This
note does not record a zone name.

Rules marked **specified** are the contract of this note and of
`internal/promptextract`, covered by synthetic tests. Those tests include
a hand-written golden fixture,
`internal/promptextract/testdata/claude_code_live_shape_v2_1_332.jsonl`,
modeled on the live Claude Code 2.1.332 shape. It uses placeholder paths,
synthetic ids, and synthetic timestamps. It is not a raw capture. Rules
marked **verified** are already stated in this repository; the citation is
the source. **verified (live)** means the 2026-11-27 capture, with the
CLI version, Linux, and non-interactive mode named in the cell. **needs
verification** means this note does not guess a path or a schema, and
someone still has to confirm it on that agent.

`internal/promptextract` is experimental and Claude-only. It is
intentionally unwired from `sdf serve`, the CLI, and the SQLite store.
The well-formed Claude pass is recorded under [Pre-wiring contract](#pre-wiring-contract).
Wiring still waits on an ADR under `docs/adr/`, on the edge cases that
are still **needs verification**, and on the default below. The package
stays pure Go, standard library only, and reads an `io.Reader`. It does
not open `~/.claude`. The default stays: prompt text is stored only with
an opt-in such as `--store-content`. On the five other JSONL sessions
from that capture (Codex, Pi, Qwen, Grok `chat_history` and
`prompt_history`, and the partial Gemini file) this extractor returned
0 prompts and 0 line errors. OpenCode's SQLite file is not that stream:
reading it as JSONL yields hundreds of `malformed json` line errors.
Copilot wrote no prompt transcript; feeding `workspace.yaml` yields
`malformed json` errors. Neither an empty `Prompts` slice nor zero
`Errors` identifies the format or the agent. An empty `Prompts` slice
does not prove the session had no prompts. See the pre-wiring contract.

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
| verified | Stated in this repository. The file is named next to the claim. The 2026-11-26 in-repo check is the OTLP baseline. |
| verified (live) | Confirmed on a live CLI on 2026-11-27, Linux, non-interactive mode. The cell names the CLI version. Distinct from **verified** (in-repo OTLP citations) and from **specified** (the `Extract` contract). |
| specified | Contract of this note and `internal/promptextract`. Synthetic tests cover it, including the hand-written Claude Code 2.1.332 golden fixture. Not a raw capture. |
| needs verification | Not observed in the 2026-11-27 capture, or not stated in this repository. Do not treat a guess as the schema. |
| partial | A 2026-11-27 capture started and did not cover the schema. The cell says what was seen. The rest stays **needs verification**. Gemini only, in this note. |
| needs verification (auth required) | The 2026-11-27 run did not authenticate, so no prompt transcript was written. This is not a claim that the agent is unsupported. Copilot only, in this note. |

## Claude Code session JSONL

The path below is **verified (live)** for Claude Code 2.1.332 on Linux,
non-interactive (`--print --bare --permission-mode bypassPermissions`,
entrypoint `sdk-cli`), on 2026-11-27. The model was a non-Anthropic model
the Claude CLI did not recognize. `--bare` suppresses hooks and context,
so attachments may differ in normal use. Path encoding was observed on
Linux only. `Extract` does not open the path.

```text
~/.claude/projects/<project-path-encoded>/<session-id>.jsonl
```

`<project-path-encoded>` replaces `/` with `-`, including the leading
`/`. The placeholder project `/tmp/example-project` encodes as
`-tmp-example-project`. Colon, space, backslash, and any non-Linux
encoding are **needs verification**. Interactive mode was not part of
this capture and is **needs verification**.

The JSONL keep and drop rules in the behavior table are **specified**.
The edge-case rows marked **verified (live)** are the ones the
2026-11-27 file actually had. One JSON object per line. The path is not
cited in [docs/agents.md](../../docs/agents.md) or in
[session-boundary.md](session-boundary.md). The repository's Claude Code
OTLP facts stay the 2026-11-26 baseline. This capture did not re-verify
them.

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
On the 2026-11-27 file those three were JSON strings (**verified
(live)**). A numeric `timestamp` was not in that file and is **needs
verification**. The same records carried a separate numeric `sentAtMs`.
The filter does not read `sentAtMs`.

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
capture. The golden fixture under `internal/promptextract/testdata/` is
also hand-written. It is shaped like the 2.1.332 file and it is not that
file.

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
is **verified (live)** for Claude Code 2.1.332 on Linux (non-interactive,
2026-11-27), with `/` encoded as `-`, including the leading `/`. On the
well-formed user records from that capture, `Extract` and this jq program
agreed 3/3. The hand-written golden fixture locks that result: both keep
the same three prompts and report no line errors. `Extract` still does
not open the path.

### Edge cases

The 2026-11-27 capture was Claude Code 2.1.332, Linux, non-interactive
(`--print --bare --permission-mode bypassPermissions`, entrypoint
`sdk-cli`). The model was a non-Anthropic model the Claude CLI did not
recognize. `--bare` suppresses hooks and context, so attachments may
differ in normal use. Path encoding was observed on Linux only. The
capture did not re-verify OTLP. Interactive mode was not run. A row
marked **verified (live)** is what that file did. A row marked **needs
verification** was not in that capture. The synthetic tests still lock
today's filter behavior. They do not prove an unobserved row has that
shape on a live file.

| Case | What the filter does today | 2026-11-27, Claude Code 2.1.332, Linux, non-interactive |
| --- | --- | --- |
| `type: "user"` string `message.content` | Kept, after harness-tag stripping and trimming. | **verified (live).** The three kept prompts were JSON strings on `message.content`. |
| `isMeta` | JSON `true` skips the record. Missing, `false`, and `null` do not. The filter does not claim the flag means a loaded skill or an internal command. | **verified (live)** that the key was absent on the kept user records. `isMeta: true` was not in the file and is **needs verification**. Do not treat a guess as what `true` means. |
| `isCompactSummary` | JSON `true` skips the record. The filter treats that as an auto-compaction summary. | **verified (live)** that the key was absent on the kept user records. `isCompactSummary: true` was not in the file and is **needs verification**. |
| `isSidechain` | JSON `true` skips the record. The filter treats that as a subagent prompt. Missing, `false`, and `null` do not skip. | **verified (live)** that kept user records had `isSidechain: false` and were kept. `isSidechain: true` was not in the file and is **needs verification**. |
| `promptSource` | Not read. It does not skip a record. | **verified (live)** value `"sdk"` on the kept prompts. It is not an interactive marker. Other values are **needs verification**. |
| `timestamp` / `uuid` / `sessionId` / `sentAtMs` | `timestamp`, `uuid`, and `sessionId` must be JSON strings. A number is a line error. `session_id` is ignored. `sentAtMs` is not read. | **verified (live)** JSON strings for `timestamp`, `uuid`, and `sessionId`, plus a separate numeric `sentAtMs`. A numeric `timestamp` was not in the file and is **needs verification**. |
| `<system-reminder>` on `type: "attachment"` | `type` other than the string `user` is skipped, so the stripper never sees this record. | **verified (live).** The harness tag was on `rendered[].content`, beside an `attachment` object, with `renderedRole` `"user"` and `"system"`. It was not a top-level `content` string. Those records were skipped. |
| Unclosed user-typed `<system-reminder>` | An unclosed `<system-reminder>` stays, and the text after it stays. | **verified (live).** A user-typed unclosed `<system-reminder>` was kept intact. |
| `tool_result`-only array | `tool_result` blocks are dropped. An array with no kept `text` block is skipped. A `text` field sitting on a `tool_result` block is not read. `toolUseResult` and `sourceToolAssistantUUID` are not read. | **verified (live).** A `type: "user"` record whose `message.content` array held only a `tool_result` block, with no sibling `text` block, was dropped. That record also had top-level `toolUseResult` and `sourceToolAssistantUUID`, and it had no `promptSource` and no `sentAtMs`. |
| Extra record types | Anything other than the string `user` is skipped, including a non-string `type`. | **verified (live)** types besides `user`: `atis-latch`, `queue-operation`, `api-request-shape`, `api-request`, `assistant` (with `tool_use`), `last-prompt`, `cost-state`, `tools-used`, `mode`, and `attachment`. `queue-operation` was `operation: "enqueue"` with the duplicate prompt on `content`. `last-prompt` carried `lastPrompt` and `leafUuid`, and that record had no `cwd` and no `timestamp`. Skipping them avoids counting the text twice. `mode` was `"normal"` on this `--print` run and is not an interactive marker. The capture does not prove a later file has no further types. |
| Interactive mode | The filter has no mode flag. The JSON value `"normal"` does not skip or keep a record. | **needs verification.** This capture was non-interactive only. The `type: "mode"` record was `"normal"` anyway. Whether an interactive session changes that record is **needs verification**. |
| Slash commands and `<command-name>`, `<command-message>`, `<command-args>` | A well-formed lowercase pair is removed even when `isMeta` is absent or `false`. Slash-command arguments inside `<command-args>` are dropped with the tag. An unclosed opener stays, with the text after it. Attributes and a different case are not stripped. | **needs verification.** Slash commands were not run in this capture. |
| `<local-command-stdout>`, `<local-command-stderr>`, `<local-command-caveat>` | Same rule as the command tags. The payload is not user-typed text. | **needs verification.** Not observed. |
| `<bash-input>`, `<bash-stdout>`, `<bash-stderr>` | A well-formed lowercase pair is removed, so shell output (which can hold secrets or environment variables) is not returned as user-typed text. An unclosed opener stays. | **needs verification.** Not observed. If a person pasted one of these tags on purpose, today's rule cannot tell that from harness output. |
| IDE context tags | Unknown tags stay in the text. This repository does not name the tags an IDE injects (open file, selection, and the like). | **needs verification.** Not observed. Do not invent a tag spelling in the filter first. |
| `[Request interrupted by user]` and `[Request interrupted by user for tool use]` | Skipped only when the trimmed block, or the trimmed prompt, is exactly one of those two strings. A larger sentence that contains the words is kept. | **needs verification.** Not observed. |
| Well-formed pasted `<system-reminder>` | A well-formed pair is removed, including one the person pasted on purpose, including a different ASCII case and an opener with attributes. The text on either side remains. | **needs verification.** A well-formed pasted pair was not in the file. Today's rule cannot tell a pasted pair from a harness pair. The unclosed case is the row above. |
| Sibling `text` next to `tool_result` | Sibling `text` blocks are kept. | **needs verification.** **Open question before wiring.** Not observed. If tool, web, or repo content lands in a sibling `text` block, that text is not user-typed and must not be kept. |
| Nested `<system-reminder>` | The outer closer ends the outer opener. `<system-reminder>outer <system-reminder>inner</system-reminder> tail</system-reminder>sửa bug` becomes `sửa bug`. Openers past 32 of the same name stay literal, so a pile of unclosed mentions does not drop the prompt. jq strips a deeper closed nest entirely; Go strips only the openers it tracked. | **needs verification.** Not observed. The harness reminder in this capture sat on `type: "attachment"`, not nested inside the user string. |
| Tag spelling on a user record | `<system-reminder>` matches ASCII case-insensitively. Attributes on the opener begin with a space or a tab (`' '` / `'\t'`), not a newline. A newline anywhere in that opener, including inside quotes, means it is not a tag. A closer may still have ASCII whitespace, including a newline, before `>`. The command, local-command, and bash tags are exact lowercase with no attributes. A self-closing tag is not stripped, including a space or tab before the slash (`<system-reminder />`, `<system-reminder a="1"/>`). | **needs verification** for case, attributes, and the command, local-command, and bash tags on a user record. The attachment row covers the tag on `type: "attachment"`. |
| `\r`-only line breaks | They would arrive as one line. Lines are split on `\n`, and a trailing `\r` is stripped. | **needs verification.** Not observed. |
| Stream and line caps | `MaxLineBytes`, `MaxInputBytes`, `MaxPrompts`, `MaxOutputBytes`, `MaxErrors`, and `MaxTagDepth` are **specified**. A stream cap fails closed with an empty `Result`. | **needs verification** against a real session. Whether a prompt or a base64-heavy file reaches a cap is not known from this capture. |
| Project-path encoding | Not implemented. The filter never opens the path. | **verified (live)** on Linux: `/` becomes `-`, including the leading `/`. Colon, space, backslash, and non-Linux encodings are **needs verification**. |
| OTLP cross-check | Not done by `Extract`. | **needs verification.** This capture did not compare the JSONL sentence to `claude_code.user_prompt`. |

## Cross-agent verification matrix

The eight agents `sdf init` accepts are `claude-code`, `codex`, `opencode`,
`pi`, `qwen`, `grok`, `gemini`, and `copilot`
([docs/agents.md](../../docs/agents.md), and the same list in
`internal/cli/agentlist_test.go`). Cursor is in the decision-hook tables
and is not one of those eight, so it is outside this matrix.

OTLP facts below are **verified** from the docs on the 2026-11-26 baseline.
The 2026-11-27 capture did not re-verify any OTLP field. Local transcript
cells marked **verified (live)** or **partial** are from that capture
(Linux, non-interactive, the version in the cell). **needs verification
(auth required)** is not a claim that the agent has no transcript.

| Agent | Local transcript | OTLP user-prompt text in this repo | Meta / injected markers | Extractor |
| --- | --- | --- | --- | --- |
| claude-code 2.1.332 | **verified (live).** Linux, non-interactive `--print --bare --permission-mode bypassPermissions` (entrypoint `sdk-cli`), 2026-11-27. The model was a non-Anthropic model the Claude CLI did not recognize. `--bare` suppresses hooks and context, so attachments may differ in normal use. `~/.claude/projects/<project-path-encoded>/<session-id>.jsonl`, `/` replaced with `-`, including the leading `/`. Path encoding was observed on Linux only. Observed keep/drop rules are in the edge-case table. `mode` was `"normal"` and is not an interactive marker. Interactive mode and the unobserved rows stay **needs verification**. | **verified.** Log `claude_code.user_prompt`. Prompt text stays redacted unless `OTEL_LOG_USER_PROMPTS`. The published setup does not set it. sdf still drops the text unless `--store-content`. [docs/agents.md](../../docs/agents.md) Claude Code section. Not re-verified on 2026-11-27. | **verified (live)** that kept user records had `isSidechain: false`, omitted `isMeta` and `isCompactSummary`, and carried `promptSource: "sdk"`, which is not an interactive marker. `<system-reminder>` was on `type: "attachment"` (`renderedRole` `"user"` and `"system"`). **specified** that JSON `true` for those three flags skips the record. What a live file uses `isMeta: true`, `isSidechain: true`, or `isCompactSummary: true` for is **needs verification**. Not described on the OTLP event. | `internal/promptextract` (experimental, unwired, Claude-only). |
| codex 0.172.0 | **verified (live).** Linux, `codex exec`, 2026-11-27. `~/.codex/sessions/YYYY/MM/DD/rollout-<local-ts>-<session-id>.jsonl`, plus SQLite sidecars. Do not commit those `.db`, `-wal`, or `-shm` files. Hook payloads in this repository still include `transcript_path` ([session-boundary.md](session-boundary.md), schemas at `69231e11`). That hook field is not this session path, and this capture did not re-check the hook. | **verified.** Log `codex.user_prompt`. Prompt text is `[REDACTED]` unless `otel.log_user_prompt = true`. User prompt text is not copied onto the trace-safe event. [docs/agents.md](../../docs/agents.md) Codex section. Not re-verified on 2026-11-27. | **verified (live).** Harness and user markers live inside `payload.internal_chat_message_metadata_passthrough.content_item_kinds` (`["user.text"]` versus `["environments.environment_context"]`) and `payload.internal_chat_message_metadata_passthrough.content_item_metadata[].provenance.type` (`"user"` versus `"harness"`). `event_msg` is a cleaner non-internal record: `payload.type` is `"item_completed"`, `payload.item.type` is `"UserMessage"`, and the text is `payload.item.content[].text`. A naive `role=user` read leaks the harness context. The documented OTLP marker is still the `[REDACTED]` placeholder. | none |
| opencode 2.4.2 | **verified (live).** Linux, `opencode run`, 2026-11-27. SQLite at `$XDG_DATA_HOME/opencode/opencode.db` (default `~/.local/share/opencode/opencode.db`). No session JSONL. Typed prompts are `session_message` rows (`type = 'user'`, JSON `data.text`, epoch-ms timestamps). Tool calls and results are in `session_part`. | **verified** as a limit. v2 exports runner spans with `session.id` and process logs. It has no `user_prompt` catalog. v1's `experimental.openTelemetry` flag is ignored by v2. [docs/agents.md](../../docs/agents.md) OpenCode section. The receiver still drops `ai.*` keys because that v1 stream sent `ai.prompt` and the rest ([docs/receiver.md](../../docs/receiver.md) content storage). Whether v2 puts prompt text on a span is **needs verification**. Not re-verified on 2026-11-27. | **needs verification.** Typed prompts are `session_message` with `type = 'user'`. This capture did not report a separate injected-text marker. | none. A reader has to speak SQLite, which rules out one shared `io.Reader`. |
| pi 1.6.1 | **verified (live).** Linux, `-p`, 2026-11-27. `~/.pi/agent/sessions/--<dash-path>--/<UTC-ts-dashes>_<session-id>.jsonl`. Schema `version: 3`. The directory encoding strips the leading `/`, replaces `/` with `-`, and wraps that as `--<dash-path>--`. Replacing `\` and `:` with `-` was not exercised on this Linux path and stays **needs verification** off Linux. The entry `timestamp` is an ISO string. The nested `message.timestamp` is a Unix-ms integer. The basename `{timestamp}_{sessionId}.jsonl` was already **verified** here ([session-boundary.md](session-boundary.md); [docs/receiver.md](../../docs/receiver.md) `PI_SESSION_FILE`). An earlier note in this table said an ephemeral session has no file. This capture did not check that case, so it stays **needs verification**. | **verified** as a limit. pi-otel logs are `pi.session.start` and `pi.session.end` only. The agents doc says to leave content capture off and does not name a prompt attribute on `chat` spans. Not re-verified on 2026-11-27. | **verified (live).** `type: "message"`, `message.role: "user"`. Live `message.content` is an array `[{"type":"text","text":"..."}]`. A vendor example shows a bare string; a future reader should accept both. Tool results are a separate `message.role: "toolResult"`. An empty `role: "system"` record was present. No `isMeta`. | none |
| qwen 0.28.7 | **verified (live).** Linux, `--auth-type openai -y`, 2026-11-27. `~/.qwen/projects/<claude-style-encoded>/chats/<session-id>.jsonl`, plus `<session-id>.title.json` and `meta.json`. `~/.qwen/settings.json` is still settings, not this transcript. `type: "user"`, `provenance: "real_user"`, `message.parts: [{"text": "..."}]`. There is no `message.content`, so the Claude extractor yields no prompts (a silent miss on every typed line from this capture). Slash commands and skills stay **needs verification**. | **verified** from the v0.28.4 docs mapping, not a live capture. Events are `qwen-code.*` and include `user_prompt`. `logPrompts` defaults to true and puts `prompt`, `request_text`, `response_text`, and `function_args` on logs. `request_text` can hold file contents and reads of `~/.qwen/memories/`. `sdf init qwen` sets `logPrompts` false. `includeSensitiveSpanAttributes` defaults to false and, when true, copies messages and tool data into spans; `sdf init qwen` forces it back to false when it finds it on ([docs/agents.md](../../docs/agents.md) Qwen section, `internal/record/redact.go`). Every `tool_call` still sends `function_args`. sdf drops those keys unless `--store-content`. Which key on the `user_prompt` event is the typed sentence is **needs verification**. Not re-verified on 2026-11-27. | **verified** that log text is broader than the typed prompt (`request_text`, memories, tool arguments) and that `includeSensitiveSpanAttributes` is a separate span-content switch. **verified (live)** on the local file: a reminder on `type: "system"` (`subtype: "permission_context_snapshot"`) and a tool result on `type: "tool_result"`. Slash commands and skill bodies stay **needs verification**. | none |
| grok 1.0.92 | **verified (live).** Linux, `-p`, 2026-11-27. Per session: `~/.grok/sessions/<url-encoded-cwd>/<session-id>/chat_history.jsonl` and `updates.jsonl`. Project-level: `~/.grok/sessions/<url-encoded-cwd>/prompt_history.jsonl`. The encoding replaces `/` with `%2F`. `prompt_history.jsonl` spans sessions for the same cwd, so a reader must filter by `session_id`. Its records are `{timestamp, session_id, prompt, is_bash}` and the `prompt` text is unwrapped. `is_bash: true` is shell input and may hold secrets. Treat that as a trust rule: do not return it as user-typed prompt text until a live check says otherwise, and it stays **needs verification**. The session folder also holds `system_prompt.txt` (a vendor prompt). Never read or commit that file. `session_search.sqlite` is an FTS store that can hold secrets. It has the same unvacuumed WAL and freelist risk as any other live SQLite file: do not copy it and then delete rows. Do not commit the database or its `-wal` or `-shm` files. The string `transcriptPath` in `internal/setup/grok_test.go` is still a decoy, not this schema. | **verified.** Logs `grok_code.*` include `user_prompt`. Prompt text is off unless `otel_log_user_prompts` / `OTEL_LOG_USER_PROMPTS`. `sdf init grok` pins the config keys false. The span name `agent.prompt` is a turn span, not the prompt body. [docs/agents.md](../../docs/agents.md) Grok section. Not re-verified on 2026-11-27. | **verified (live).** In `chat_history.jsonl`, some `type: "user"` lines are not typed prompts: they have no `prompt_index`, or they have `synthetic_reason: "system_reminder"`. Typed prompts have `prompt_index` and are wrapped in `<user_query>`. Do not add `<user_query>` to Claude's strip list. | none |
| gemini 0.71.1 | **partial.** Linux, `-p`, 2026-11-27. `~/.gemini/projects.json` maps the cwd to a short name. The chat file is `~/.gemini/tmp/<short-name>/chats/session-<ts>-<shortid>.jsonl`. The file is an append-only patch log with `$set` and `$rewindTo`. The sixth line (`$rewindTo`) rewound turn 1 after a provider HTTP 500 (`convert_request_failed`). Assistant records, tool records, multi-turn prompts, `$set` patch semantics, and the replay meaning of `$rewindTo` stay **needs verification**. `~/.gemini/settings.json` is still settings, not this transcript. | **verified** from the published telemetry doc and source at `eecccfcd`, not a live capture. Log event `gemini_cli.user_prompt`. `logPrompts` defaults to true and adds `prompt`, `request_text`, `response_text`, and `function_args`. The log body is a sentence such as a tool-call summary, not the prompt. Span prompts require `telemetry.traces=true`. sdf drops the text unless `--store-content`. Whether `prompt` is only the typed sentence is **needs verification**. Not re-verified on 2026-11-27. | **partial.** Turn 1's typed prompt and an injected `<session_context>` were both `type: "user"`, with top-level `content: [{"text": "..."}]` and no `message` wrapper. A `role=user` read leaks that harness context. Assistant, tool, `$set` patch semantics, and `$rewindTo` replay stay **needs verification**. | none |
| copilot 1.0.116 | **needs verification (auth required).** Linux, 2026-11-27. The unauthenticated run wrote `~/.copilot/session-state/<uuid>/workspace.yaml` (metadata only) and `~/.copilot/logs/`. It did not write a prompt transcript. Do not mark the agent unsupported. `~/.copilot/hooks/` is still a hook directory. `COPILOT_OTEL_FILE_EXPORTER_PATH` in tests is still an OTLP file exporter, not a transcript. | **verified** from the docs and `copilot help monitoring` 1.0.114, not a live capture. Traces and metrics, no logs. `OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT` defaults to `NO_CONTENT`. `SPAN_ONLY` and `SPAN_AND_EVENT` put prompts on spans. `EVENT_ONLY` puts them on span events, which sdf does not store. The attributes are `gen_ai.input.messages`, `gen_ai.output.messages`, `gen_ai.system_instructions`, and the tool-call keys. Each user message is one `invoke_agent` span. Separating the typed sentence from system and tool messages inside `gen_ai.input.messages` is **needs verification**. Not re-verified on 2026-11-27. | **verified** that the capture, when on, is the GenAI message list and not a single typed-text field. Local injected markers are **needs verification (auth required)**. | none |

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
5. Keep the notes in this file. A live non-interactive capture updates
   the cell to **verified (live)**, with the CLI version, the date, and
   the OS. That mark is not the in-repo **verified** mark, and it is not
   the **specified** `Extract` contract. Leave **partial** and **needs
   verification (auth required)** as they are when the capture did not
   finish.

The 2026-11-27 pass completed step 1, the short-prompt and tool-turn
parts of step 2 (without the slash command, which remains **needs
verification**), and step 3. Step 4 (OTLP) was not done. Copilot stopped
at authentication, so its prompt transcript is still unknown. Paths in
the matrix come from that capture, not from memory.

Claude Code's well-formed pass is done for 2.1.332 on 2026-11-27.
`promptextract.Extract` and the jq filter agreed 3/3 on the well-formed
user records. The hand-written golden fixture locks that shape. Line
errors belong to the Go result. The raw capture is not in this
repository. Wiring still waits on the [pre-wiring contract](#pre-wiring-contract).

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
  the module and fails if another package imports it. It is experimental,
  Claude-only, and stays unwired under the [pre-wiring contract](#pre-wiring-contract).
- `Extract` returns raw, unredacted text. It may contain pasted
  credentials or other PII. It does not strip control characters, ANSI
  escapes, NUL, or bidi overrides. A terminal or UI that prints a prompt
  has to sanitize that output.
- Tests under `internal/promptextract` are synthetic. They are not live
  session files. One fixture is hand-written to the Claude Code 2.1.332
  shape observed on 2026-11-27. Raw captures are not committed, and
  `testdata/` does not hold another agent's transcript or a `.db`,
  `-wal`, or `-shm` file.
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
- Reject path encodings that escape the tree. Claude (`/` to `-`), Pi
  (`--<dash-path>--`), and Grok (`/` to `%2F`) encodings are lossy. Treat
  the encoded directory name as one literal path component. Do not decode
  it and then join or walk. The [pre-wiring contract](#pre-wiring-contract)
  has the rule.
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

## Pre-wiring contract

These rules apply before any reader is called from `sdf serve`. This
package does not open session files.

There is no single multi-agent reader. OpenCode is SQLite. Gemini's chat
file is a `$set` / `$rewindTo` event log. The JSONL shapes of Claude,
Codex, Pi, Qwen, and Grok are not the same object. Each agent needs its
own reader, selected from a known agent provenance. Callers do not sniff
the file and guess the agent.

`internal/promptextract` is the Claude reader only. On the five other
JSONL sessions from the 2026-11-27 capture (Codex, Pi, Qwen, Grok
`chat_history` and `prompt_history`, and the partial Gemini file) it
returned 0 prompts and 0 line errors. That is a silent miss on those
files, not a property of every byte those agents can write. OpenCode's
SQLite file, read as JSONL, yields hundreds of `malformed json` line
errors. Copilot wrote no prompt transcript; feeding `workspace.yaml`
yields `malformed json` errors. Neither an empty `Prompts` slice nor
zero `Errors` identifies the format or the agent. An empty `Prompts`
slice does not prove the session had no prompts. A naive read of every
`role=user` or every `type: "user"` leaks harness context on Codex
(`environments.environment_context`, provenance `"harness"`, under
`payload.internal_chat_message_metadata_passthrough.content_item_metadata[]`),
Grok (non-typed `type: "user"` lines, including
`synthetic_reason: "system_reminder"`), and Gemini (injected
`<session_context>` on `type: "user"`).

OpenCode is SQLite. A future reader opens `file:<path>?mode=ro` and sets
`PRAGMA query_only=ON`. It does not use `immutable=1` on a live WAL.
`mode=ro` against a live WAL database, including its `-shm` file, is
**needs verification**. SQLite opens `-wal` and `-shm` by path itself, so
`O_NOFOLLOW` and `fstat` on the descriptors the caller opened do not
close that TOCTOU gap. Closing it takes a custom VFS or a containment
check on the directory before SQLite opens the sidecars. A corrupt
database is untrusted input. The reader does not query `credential` or `account`, the tables this
capture observed. `control_account` was not observed in this capture. Do not build a fixture by copying a
live database and then deleting rows. An unvacuumed WAL or freelist page
can still hold the deleted secrets. Grok's `session_search.sqlite` is an
FTS store that can hold secrets and has the same unvacuumed WAL and
freelist risk. Grok's `prompt_history.jsonl` spans sessions for the same
cwd. `is_bash: true` is shell input and may hold secrets; that is a trust
rule, and the value stays **needs verification**. The Grok session folder
holds `system_prompt.txt`, a vendor prompt. Never read or commit it.
This repository does not commit `.db`, `-wal`, or `-shm` files under
`internal/promptextract/testdata/`.

Path encodings are lossy, so different project paths can land on the same
directory name. Claude replaces `/` with `-`, including the leading `/`,
observed on Linux only. Pi strips the leading `/`, replaces `/` with `-`,
and wraps the directory as `--<dash-path>--`. Replacing `\` and `:` with
`-` was not exercised on the Linux path and stays **needs verification**
off Linux. Pi's entry `timestamp` is an ISO string. The nested
`message.timestamp` is a Unix-ms integer. Grok replaces `/` with `%2F`.
Treat the encoded directory name as one literal path component. Do not
decode it and then join or walk. A decoded `%2F..%2F` is a traversal.

The Claude well-formed pass is done. On 2.1.332 (Linux, non-interactive,
2026-11-27) `Extract` and the jq filter agreed 3/3, and the golden
fixture locks that shape. Wiring into `sdf serve` still waits on an ADR
under `docs/adr/`, on the edge-case rows that are still **needs
verification**, and on `sdf serve` dropping prompt content by default
unless `--store-content`.
