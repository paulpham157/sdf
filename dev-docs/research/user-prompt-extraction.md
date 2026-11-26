# User-typed prompt text

Checked against this repository on 2026-11-26. No live agent session was
read. Rules marked **specified** are the contract of this note and of
`internal/promptextract`, covered by synthetic tests. Rules marked
**verified** are already stated in this repository; the citation is the
source. **needs verification** means this note does not guess a path or a
schema, and someone still has to confirm it on that agent.

"User-typed prompt text" (phần text bạn gõ vào prompt) is the plain text a
person types into the prompt box and submits with Enter. It does not
include an internal slash command, a `tool_result`, a `<system-reminder>`
block, a loaded skill body, or a harness message injected beside the box.

`sdf` does not read these session files today. It receives OTLP. The filter
below is experimental and is not wired into `sdf serve`, the CLI, or the
SQLite store.

## Status words

| Mark | Meaning |
| --- | --- |
| verified | Stated in this repository. The file is named next to the claim. |
| specified | Contract of this note and `internal/promptextract`. Synthetic tests cover it. Not checked against a live capture here. |
| needs verification | Confirm on that agent. Do not treat a guess as the schema. |

## Claude Code session JSONL

**specified.** The file this filter is written for:

```text
~/.claude/projects/<project-path-encoded>/<session-id>.jsonl
```

One JSON object per line. This path is not cited in
[docs/agents.md](../../docs/agents.md) or in
[session-boundary.md](session-boundary.md). The repository's Claude Code
facts are the OTLP catalog and the session hooks. The encoding inside
`<project-path-encoded>` (which characters replace a slash, a colon, or a
space) is **needs verification**.

Keep a record when `type` is the JSON string `user` and `isMeta` is not
JSON `true`. `isMeta: true` is the specified mark for a loaded skill, an
internal command, or a harness-generated message. That meaning is
**specified** and **needs verification** on a live file.

Drop `tool_result` blocks. Read plain text from `message.content` when it
is a JSON string, or from blocks whose `type` is the JSON string `text`
when `message.content` is an array. Remove `<system-reminder>` regions.

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
| `type` is `user` and `isMeta` is JSON `true` | skipped |
| `isMeta` missing, JSON `false`, or JSON `null` | not meta |
| `isMeta` any other JSON type | line error |
| `message` missing or JSON `null` | skipped |
| `message.content` is a string | that string, after reminder stripping |
| `message.content` is an array | each object with `type` `text`; other types, including `tool_result`, are dropped |
| `message.content` any other JSON type, a non-object `message`, or a non-object array element | line error |
| a `text` block whose `text` is not a JSON string | line error |
| text that is empty or only whitespace after stripping | skipped |
| two kept `text` blocks | joined with one `\n`, each block trimmed first |
| well-formed `<system-reminder>` … `</system-reminder>` | removed, including when it spans lines, including more than one region |
| `<system-reminder>` with no closer | that opener and the rest of the string are dropped |
| JSON that is not one object, or two JSON values on one line | line error `malformed json` |
| raw line longer than `MaxLineBytes` (32 MiB, including the newline) | line error `ErrLineTooLong`; the parser resumes at the next newline |
| a read error from the underlying reader | returned; prompts already collected stay in the result |

A line error does not stop the scan. `Result.Errors` holds it.
`Result.Prompts` holds what was kept. `Extract`'s error return is only a
nil reader, a non-positive limit inside the tests, or a failure from the
underlying reader.

The 32 MiB cap is the memory bound for one line. `bufio.Scanner`'s default
64 KiB token limit is too small for a session line, so the reader is
`bufio.Reader` and `ReadSlice('\n')`. A prompt longer than 32 MiB would be
reported as too long. Whether a real prompt reaches that size is
**needs verification**. A huge `tool_result` line over the cap is also
reported as too long, because the parser cannot classify a line it refuses
to hold.

`\r`-only line breaks are **needs verification**. They would arrive as one
line.

### Quick filter (jq)

This is the inspection filter for the same keep/drop rules. It prints one
prompt per kept record and does not print line numbers. `fromjson?` skips
a malformed line instead of recording an error. A user record whose
`isMeta` is not a boolean, or whose `content` is not a string or an array,
is skipped here and is a line error in Go. On a well-formed record the
text matches `internal/promptextract`.

```jq
def strip_reminders:
  # gsub/sub, not index plus a slice. jq's index on a string is a byte
  # offset, and .[n:m] counts code points, so a slice at that offset cuts
  # a multibyte prompt (the Vietnamese example) inside the tag.
  gsub("(?s)<system-reminder>.*?</system-reminder>"; "")
  | sub("(?s)<system-reminder>.*"; "");

def trim: sub("^\\s+"; "") | sub("\\s+$"; "");

def text:
  if . == null then empty
  elif type == "string" then strip_reminders | trim | select(length > 0)
  elif type == "array" then
    [ .[]
      | select(type == "object" and .type == "text" and (.text | type) == "string")
      | .text | strip_reminders | trim
      | select(length > 0)
    ]
    | join("\n")
    | select(length > 0)
  else empty
  end;

fromjson?
| select(type == "object" and .type == "user")
| if has("isMeta") and (.isMeta | type) != "boolean" and .isMeta != null then empty
  elif .isMeta == true then empty
  else .message.content | text
  end
```

```bash
jq -Rr -f user-prompt.jq ~/.claude/projects/<project-path-encoded>/<session-id>.jsonl
```

Save the program as `user-prompt.jq`. `-R` feeds one raw line at a time so
a bad line does not abort the rest of the file.

### Edge cases still to verify

These are the cases a live Claude Code file has to answer. The synthetic
tests lock today's filter behavior. They do not prove a live file has
that shape.

| Case | What the filter does today | What to confirm on a live file |
| --- | --- | --- |
| Slash commands, `<command-name>`, `<local-command-stdout>` | Left in the text when the record is `type: user` and `isMeta` is not `true`. The tags are not stripped. | Are these records `isMeta: true`? If a non-meta user record carries them, decide whether to drop the record or strip the tags. Record any sibling tags in the same object (`command-message`, `command-args`, or others). |
| IDE context tags | Unknown tags stay in the text. This repository does not name the tags an IDE injects (open file, selection, and the like). | Copy the tag spelling from a live record. Do not invent one in the filter first. |
| User-pasted `<system-reminder>` | A well-formed pair is removed, including one the person pasted on purpose. The text on either side remains, spaces included (`please keep  in the note`). An unclosed `<system-reminder>` drops the rest of that string. | Decide whether pasted text must survive. Today's rule cannot tell a pasted pair from a harness pair. |
| Mixed `tool_result` and `text` in one array | `tool_result` blocks are dropped. Sibling `text` blocks are kept. A `text` field sitting on a `tool_result` block is not read. | Confirm a live record ever mixes them. If the whole array is untrusted once a `tool_result` is present, dropping the sibling text is a one-line change. |
| Nested `<system-reminder>` | The first closer ends the first opener. `<system-reminder>outer <system-reminder>inner</system-reminder> tail</system-reminder>sửa bug` becomes `tail</system-reminder>sửa bug`. | Confirm the harness does not nest the tag. |
| Tag spelling | The match is the exact lowercase `<system-reminder>` and `</system-reminder>`. A space, an attribute, or different case is not a reminder. | Confirm the live tag is exactly that spelling. |
| `timestamp` / `uuid` / `sessionId` types | JSON strings. A number is a line error. `session_id` is ignored. | Confirm the live key names and JSON types. |
| Other `type` values | Anything other than the string `user` is skipped, including values this note does not list. | List the `type` strings a real file contains (`assistant` and `summary` are the ones the tests name, plus `system`). |
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
| claude-code | **specified** path above. Live file and path encoding **needs verification**. | **verified.** Log `claude_code.user_prompt`. Prompt text stays redacted unless `OTEL_LOG_USER_PROMPTS`. The published setup does not set it. sdf still drops the text unless `--store-content`. [docs/agents.md](../../docs/agents.md) Claude Code section. | **specified** `isMeta` for the JSONL filter. Not described on the OTLP event. **needs verification** on a live JSONL file. | `internal/promptextract`, synthetic tests only |
| codex | **needs verification.** Hook payloads include `transcript_path` ([session-boundary.md](session-boundary.md), schemas at `69231e11`). The path value and the file format are not described. | **verified.** Log `codex.user_prompt`. Prompt text is `[REDACTED]` unless `otel.log_user_prompt = true`. User prompt text is not copied onto the trace-safe event. [docs/agents.md](../../docs/agents.md) Codex section. | **needs verification** for a local file. The documented OTLP marker is the `[REDACTED]` placeholder. | none |
| opencode | **needs verification.** This repository does not name a session file. | **verified** as a limit. v2 exports runner spans with `session.id` and process logs. It has no `user_prompt` catalog. v1's `experimental.openTelemetry` flag is ignored by v2. [docs/agents.md](../../docs/agents.md) OpenCode section. The receiver still drops `ai.*` keys because that v1 stream sent `ai.prompt` and the rest ([docs/receiver.md](../../docs/receiver.md) content storage). Whether v2 puts prompt text on a span is **needs verification**. | **needs verification.** | none |
| pi | **verified** file *name* only: `{timestamp}_{sessionId}.jsonl` ([session-boundary.md](session-boundary.md); [docs/receiver.md](../../docs/receiver.md) `PI_SESSION_FILE`, basename with `.jsonl` removed). The directory, and the JSONL schema for user text, are **needs verification**. An ephemeral session has no file. | **verified** as a limit. pi-otel logs are `pi.session.start` and `pi.session.end` only. The agents doc says to leave content capture off and does not name a prompt attribute on `chat` spans. | **needs verification.** | none |
| qwen | **needs verification.** `~/.qwen/settings.json` is settings, not a transcript. | **verified** from the v0.28.4 docs mapping, not a live capture. Events are `qwen-code.*` and include `user_prompt`. `logPrompts` defaults to true and puts `prompt`, `request_text`, `response_text`, and `function_args` on logs. `request_text` can hold file contents and reads of `~/.qwen/memories/`. `sdf init qwen` sets `logPrompts` false. Every `tool_call` still sends `function_args`. sdf drops those keys unless `--store-content`. Which key on the `user_prompt` event is the typed sentence is **needs verification**. | **verified** that log text is broader than the typed prompt (`request_text`, memories, tool arguments). Slash commands and skill bodies in a local file are **needs verification**. | none |
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
3. Record the marker for injected text (`isMeta`, a tag, a redaction
   placeholder, or none).
4. Compare that typed sentence with the OTLP field named above, with the
   agent's own prompt switch off and then on. sdf's store should still
   have dropped it unless `--store-content` was set for that run.
5. Keep the notes in this file. Update the matrix cell from **needs
   verification** to **verified**, with the version and the date.

Claude Code's extra pass is the edge-case table above, on a real
`~/.claude/projects/.../*.jsonl`, fed to `promptextract.Extract` and to
the jq filter. The two should agree on every well-formed user record.
Line errors belong to the Go result.

## Trust boundaries and privacy

**verified** for the collector as it is now:

- sdf drops prompt, response, tool, and error text before storing, for
  every agent, whatever the agent was configured to send. The switch is
  `sdf serve --store-content` or `SDF_STORE_CONTENT`. Rows kept that way
  are marked `sdf.content=kept`. The key list is
  [docs/receiver.md](../../docs/receiver.md) (Content storage) and
  `internal/record/redact.go`.
- The SQLite file is created mode 0600. Group and other bits are stripped
  from an existing file when the process can change the mode
  ([docs/security.md](../../docs/security.md)).
- With the flag off, a prompt that an agent did send is not in the store.
  Turning an agent's own switch on (`logPrompts`, `OTEL_LOG_USER_PROMPTS`,
  `otel.log_user_prompt`, `OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT`)
  changes what is sent. It does not change what sdf keeps.

**specified** for this change:

- `internal/promptextract` is pure Go, standard library only, and reads an
  `io.Reader`. It does not open `~/.claude`, does not import the store,
  and is not called from `cmd/sdf` or `internal/cli`. A unit test walks
  the module and fails if another package imports it.
- Tests under `internal/promptextract` are synthetic. They are not live
  session files.

A later change that wires this filter in has to stay opt-in. It has to
open a session file the user can already read, and it has to leave that
file's mode alone. It must not copy prompt text into SQLite unless the
same kind of opt-in as `--store-content` is on, and a database that holds
the text has to stay owner-readable only, as the telemetry store already
is. The default remains: do not store prompt text.

Extracted text is data from the session, not a configuration file and not
an instruction to sdf. A prompt can contain a literal `<system-reminder>`
or a command tag. The filter's only opinion about those strings is the
table above.
