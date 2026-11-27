package promptextract

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

// Fixtures in this file are synthetic. They are not a capture from a live
// agent, and they do not belong in tests/samples.

func TestExtractUserTypedPrompts(t *testing.T) {
	reminder := "<system-reminder>\nloaded skill\nline two\n</system-reminder>"
	cases := []struct {
		name string
		body any
		meta any
		want string
		skip bool
	}{
		{name: "string", body: "sửa bug ở hàm login", want: "sửa bug ở hàm login"},
		{name: "array text", body: []any{userText("sửa bug ở hàm login")}, want: "sửa bug ở hàm login"},
		{name: "isMeta true", body: "sửa bug ở hàm login", meta: true, skip: true},
		{name: "isMeta false", body: "sửa bug ở hàm login", meta: false, want: "sửa bug ở hàm login"},
		{name: "isMeta null", body: "sửa bug ở hàm login", meta: nil, want: "sửa bug ở hàm login"},
		{
			name: "tool_result only",
			body: []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "secret tool output", "text": "secret tool output"}},
			skip: true,
		},
		{
			name: "mixed tool_result and text",
			body: []any{
				map[string]any{"type": "tool_result", "content": "secret tool output"},
				userText("sửa bug ở hàm login"),
			},
			want: "sửa bug ở hàm login",
		},
		{name: "reminder string", body: "<system-reminder>loaded skill</system-reminder>", skip: true},
		{name: "reminder multiline string", body: reminder, skip: true},
		{
			name: "reminder beside user text",
			body: "sửa bug ở hàm login\n" + reminder,
			want: "sửa bug ở hàm login",
		},
		{
			name: "mixed array user text and reminder block",
			body: []any{
				userText("sửa bug ở hàm login"),
				userText(reminder),
			},
			want: "sửa bug ở hàm login",
		},
		{
			name: "two text blocks",
			body: []any{userText("sửa bug ở hàm login"), userText("và thêm test")},
			want: "sửa bug ở hàm login\nvà thêm test",
		},
		{name: "unclosed reminder", body: "sửa bug ở hàm login <system-reminder>still hidden", want: "sửa bug ở hàm login <system-reminder>still hidden"},
		{
			name: "two reminders",
			body: "a <system-reminder>1</system-reminder> b <system-reminder>2</system-reminder> c",
			want: "a  b  c",
		},
		{
			name: "pasted reminder is removed",
			body: "please keep <system-reminder>this example</system-reminder> in the note",
			want: "please keep  in the note",
		},
		{name: "empty string", body: "", skip: true},
		{name: "whitespace", body: " \n\t ", skip: true},
		{name: "whitespace array block", body: []any{userText(" \n\t ")}, skip: true},
		{name: "command-name stripped", body: "<command-name>/compact</command-name>", skip: true},
		{name: "local-command-stdout stripped", body: "<local-command-stdout>total 0</local-command-stdout>", skip: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := map[string]any{
				"type":    "user",
				"message": map[string]any{"role": "user", "content": tc.body},
			}
			// A nil meta stays off the record, except the case that writes JSON null.
			if tc.name == "isMeta null" || tc.meta != nil {
				rec["isMeta"] = tc.meta
			}
			line, err := json.Marshal(rec)
			if err != nil {
				t.Fatal(err)
			}
			res, err := Extract(bytes.NewReader(append(line, '\n')))
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Errors) != 0 {
				t.Fatalf("errors: %v", res.Errors)
			}
			if tc.skip {
				if len(res.Prompts) != 0 {
					t.Fatalf("kept %q", res.Prompts[0].Text)
				}
				return
			}
			if len(res.Prompts) != 1 {
				t.Fatalf("prompts %#v", res.Prompts)
			}
			if res.Prompts[0].Text != tc.want {
				t.Fatalf("text %q want %q", res.Prompts[0].Text, tc.want)
			}
			if res.Prompts[0].Line != 1 {
				t.Fatalf("line %d", res.Prompts[0].Line)
			}
		})
	}
}

func userText(s string) map[string]any {
	return map[string]any{"type": "text", "text": s}
}

func TestExtractOptionalIDsAndNonUserRecords(t *testing.T) {
	user := map[string]any{
		"type":      "user",
		"uuid":      "u-1",
		"sessionId": "sess-1",
		"timestamp": "2026-11-26T15:04:05Z",
		"message":   map[string]any{"content": "sửa bug ở hàm login"},
	}
	assistant := map[string]any{
		"type":    "assistant",
		"message": map[string]any{"content": []any{userText("sửa bug ở hàm login")}},
	}
	system := map[string]any{"type": "system", "message": map[string]any{"content": "sửa bug ở hàm login"}}
	summary := map[string]any{"type": "summary", "summary": "sửa bug ở hàm login"}
	snake := map[string]any{
		"type":       "user",
		"session_id": "not-this",
		"message":    map[string]any{"content": "sửa bug ở hàm login"},
	}
	var buf bytes.Buffer
	for _, rec := range []any{user, assistant, system, summary, snake} {
		b, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(b)
		buf.WriteString("\r\n")
	}
	res, err := Extract(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("errors: %v", res.Errors)
	}
	if len(res.Prompts) != 2 {
		t.Fatalf("prompts %#v", res.Prompts)
	}
	got := res.Prompts[0]
	if got.Line != 1 || got.UUID != "u-1" || got.SessionID != "sess-1" || got.Timestamp != "2026-11-26T15:04:05Z" || got.Text != "sửa bug ở hàm login" {
		t.Fatalf("first %#v", got)
	}
	if res.Prompts[1].Line != 5 || res.Prompts[1].SessionID != "" || res.Prompts[1].Text != "sửa bug ở hàm login" {
		t.Fatalf("session_id was read: %#v", res.Prompts[1])
	}
}

func TestExtractSkipsEmptyAndReportsMalformed(t *testing.T) {
	good := `{"type":"user","message":{"content":"sửa bug ở hàm login"}}`
	stream := "\n" + good + "\n" + "\n" + "{not json\n" + `{"type":"User","message":{"content":"nope"}}` + "\n" + good
	res, err := Extract(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Prompts) != 2 {
		t.Fatalf("prompts %#v errors %v", res.Prompts, res.Errors)
	}
	if res.Prompts[0].Line != 2 || res.Prompts[1].Line != 6 {
		t.Fatalf("lines %d %d", res.Prompts[0].Line, res.Prompts[1].Line)
	}
	if len(res.Errors) != 1 || res.Errors[0].Line != 4 || !strings.Contains(res.Errors[0].Error(), "malformed json") {
		t.Fatalf("errors %v", res.Errors)
	}
	// The last record has no trailing newline.
	if res.Prompts[1].Text != "sửa bug ở hàm login" {
		t.Fatalf("tail %q", res.Prompts[1].Text)
	}
}

func TestExtractContentShapeErrors(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{name: "content number", line: `{"type":"user","message":{"content":1}}`, want: "message.content must be a string or an array of blocks"},
		{name: "isMeta string", line: `{"type":"user","isMeta":"true","message":{"content":"nope"}}`, want: "isMeta must be a boolean"},
		{name: "timestamp number", line: `{"type":"user","timestamp":1,"message":{"content":"nope"}}`, want: "timestamp must be a string"},
		{name: "message array", line: `{"type":"user","message":["sửa bug ở hàm login"]}`, want: "message must be an object"},
		{name: "block string", line: `{"type":"user","message":{"content":["sửa bug ở hàm login"]}}`, want: "message.content block must be an object"},
		{name: "text not string", line: `{"type":"user","message":{"content":[{"type":"text","text":1}]}}`, want: "text block's text must be a string"},
		{name: "trailing value", line: `{"type":"user","message":{"content":"sửa bug ở hàm login"}}{"type":"user"}`, want: "trailing data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Extract(strings.NewReader(tc.line + "\n" + `{"type":"user","message":{"content":"kept"}}`))
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Errors) != 1 || res.Errors[0].Line != 1 || !strings.Contains(res.Errors[0].Error(), tc.want) {
				t.Fatalf("errors %v", res.Errors)
			}
			if len(res.Prompts) != 1 || res.Prompts[0].Line != 2 || res.Prompts[0].Text != "kept" {
				t.Fatalf("prompts %#v", res.Prompts)
			}
		})
	}
}

func TestExtractNullMessageAndAbsentIDs(t *testing.T) {
	res, err := Extract(strings.NewReader("null\n{\"type\":\"user\",\"uuid\":null,\"sessionId\":null,\"timestamp\":null,\"message\":null}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Prompts) != 0 || len(res.Errors) != 0 {
		t.Fatalf("%#v", res)
	}
}

func TestExtractExactKeys(t *testing.T) {
	// Keys are matched exactly. IsMeta, SessionId, and Content are not the
	// fields this filter reads.
	line := `{"type":"user","IsMeta":true,"SessionId":"nope","message":{"Content":"hidden","content":"sửa bug ở hàm login"}}`
	res, err := Extract(strings.NewReader(line))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 || len(res.Prompts) != 1 {
		t.Fatalf("%#v", res)
	}
	if res.Prompts[0].Text != "sửa bug ở hàm login" || res.Prompts[0].SessionID != "" {
		t.Fatalf("%#v", res.Prompts[0])
	}
}

func TestExtractUnicodeEscapeReminder(t *testing.T) {
	line := `{"type":"user","message":{"content":"keep \u003csystem-reminder\u003ehidden\u003c/system-reminder\u003e tail"}}`
	res, err := Extract(strings.NewReader(line))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 || len(res.Prompts) != 1 || res.Prompts[0].Text != "keep  tail" {
		t.Fatalf("%#v", res)
	}
}

func TestStripHarnessNestedOpener(t *testing.T) {
	in := "<system-reminder>outer <system-reminder>inner</system-reminder> tail</system-reminder>sửa bug"
	got := strings.TrimSpace(stripHarness(in))
	want := "sửa bug"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestExtractLargeLine(t *testing.T) {
	const scannerLimit = 64 * 1024
	body := "sửa bug ở hàm login " + strings.Repeat("x", scannerLimit)
	line, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"content": body},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(line) <= scannerLimit {
		t.Fatalf("fixture is %d bytes, want a line over the bufio.Scanner default", len(line))
	}
	res, err := Extract(bytes.NewReader(append(line, '\n')))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 || len(res.Prompts) != 1 {
		t.Fatalf("%#v", res)
	}
	if res.Prompts[0].Text != body {
		t.Fatalf("length %d want %d", len(res.Prompts[0].Text), len(body))
	}
}

func TestExtractLineTooLongResyncs(t *testing.T) {
	big, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"content": strings.Repeat("a", 10000)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(big) <= 4096 {
		t.Fatal("fixture fits in one bufio chunk")
	}
	var stream bytes.Buffer
	stream.Write(big)
	stream.WriteByte('\n')
	stream.WriteString("{\"type\":\"user\",\"message\":{\"content\":\"after\"}}\n")
	res, err := extractLimited(&stream, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 1 || res.Errors[0].Line != 1 || !errors.Is(res.Errors[0], ErrLineTooLong) {
		t.Fatalf("errors %v", res.Errors)
	}
	if len(res.Prompts) != 1 || res.Prompts[0].Line != 2 || res.Prompts[0].Text != "after" {
		t.Fatalf("prompts %#v", res.Prompts)
	}
}

func TestReadLineBounds(t *testing.T) {
	got, tooLong, err := readLine(bufio.NewReader(strings.NewReader("abcdefg\n")), 8)
	if err != nil || tooLong || string(got) != "abcdefg" {
		t.Fatalf("exact fit got %q tooLong %v err %v", got, tooLong, err)
	}
	r := bufio.NewReader(strings.NewReader("abcdefgh\nnext\n"))
	got, tooLong, err = readLine(r, 8)
	if err != nil || !tooLong || got != nil {
		t.Fatalf("over got %q tooLong %v err %v", got, tooLong, err)
	}
	got, tooLong, err = readLine(r, 8)
	if err != nil || tooLong || string(got) != "next" {
		t.Fatalf("next got %q tooLong %v err %v", got, tooLong, err)
	}
	_, tooLong, err = readLine(bufio.NewReader(strings.NewReader("abcdefghi")), 8)
	if err != io.EOF || !tooLong {
		t.Fatalf("eof tooLong %v err %v", tooLong, err)
	}
}

func TestExtractReadError(t *testing.T) {
	r := io.MultiReader(strings.NewReader("{\"type\":\"user\",\"message\":{\"content\":\"one\"}}\n"), errReader{io.ErrClosedPipe})
	res, err := Extract(r)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("err %v", err)
	}
	if len(res.Prompts) != 1 || res.Prompts[0].Text != "one" {
		t.Fatalf("prompts %#v", res.Prompts)
	}
}

func TestExtractNilAndEmpty(t *testing.T) {
	if _, err := Extract(nil); err == nil {
		t.Fatal("nil reader")
	}
	res, err := Extract(strings.NewReader(""))
	if err != nil || len(res.Prompts) != 0 || len(res.Errors) != 0 {
		t.Fatalf("%v %#v", err, res)
	}
	if _, err := extractLimited(strings.NewReader(""), 0); err == nil {
		t.Fatal("max line")
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestQuickFilterMatchesExtract(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatal("jq is required when CI=true; quick-filter parity cannot be skipped")
		}
		t.Log("jq is not installed; skipping quick-filter parity locally. CI=true makes this a failure.")
		t.Skip("jq not installed")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	doc, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "dev-docs", "research", "user-prompt-extraction.md"))
	if err != nil {
		t.Fatal(err)
	}
	const fence = "```jq\n"
	i := bytes.Index(doc, []byte(fence))
	if i < 0 {
		t.Fatal("jq fence missing")
	}
	rest := doc[i+len(fence):]
	j := bytes.Index(rest, []byte("\n```"))
	if j < 0 {
		t.Fatal("jq fence not closed")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "user-prompt.jq")
	if err := os.WriteFile(script, rest[:j+1], 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []any{
		"sửa bug ở hàm login",
		[]any{userText("sửa bug ở hàm login")},
		map[string]any{"meta": true, "body": "sửa bug ở hàm login"},
		"<system-reminder>\nloaded skill\nline two\n</system-reminder>",
		"sửa bug ở hàm login\n<system-reminder>\nloaded skill\nline two\n</system-reminder>",
		[]any{userText("sửa bug ở hàm login"), userText("<system-reminder>\nloaded skill\n</system-reminder>")},
		[]any{userText("sửa bug ở hàm login"), userText("và thêm test")},
		[]any{map[string]any{"type": "tool_result", "content": "secret"}, userText("sửa bug ở hàm login")},
		"sửa bug ở hàm login <system-reminder>still hidden",
		"please keep <system-reminder>this example</system-reminder> in the note",
		"a <system-reminder>1</system-reminder> b <system-reminder>2</system-reminder> c",
		"<command-name>/compact</command-name>",
		"<system-reminder>outer <system-reminder>inner</system-reminder> tail</system-reminder>sửa bug",
		"<SYSTEM-REMINDER>secret</SYSTEM-REMINDER>sửa bug",
		"sửa bug <bash-stdout>SECRET=1</bash-stdout>",
		"[Request interrupted by user]",
		"how do I close <system-reminder> tags? also fix login",
		"",
		" \n\t ",
	}
	var raw bytes.Buffer
	for _, c := range cases {
		body := c
		meta := false
		if m, ok := c.(map[string]any); ok {
			body = m["body"]
			meta = true
		}
		rec := map[string]any{"type": "user", "message": map[string]any{"content": body}}
		if meta {
			rec["isMeta"] = true
		}
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		raw.Write(line)
		raw.WriteByte('\n')
	}
	// These stay in step with Go's opener rules: a newline, an unquoted
	// "<", a self-closing slash, and an attribute list past the 1024-byte
	// window are kept. A shorter list is stripped.
	attrEdges := []string{
		"<system-reminder\na=1>secret</system-reminder>sửa bug",
		"<system-reminder a=\"x\ny\">secret</system-reminder>sửa bug",
		"<system-reminder a=<>secret</system-reminder>sửa bug",
		"<system-reminder /> keep </system-reminder>",
		"<system-reminder a=\"1\"/> keep </system-reminder>",
		"<system-reminder a=\"1\" /> keep </system-reminder>",
		"<system-reminder " + strings.Repeat("a", 1023) + ">secret</system-reminder>sửa bug",
		"<system-reminder " + strings.Repeat("a", 1022) + ">secret</system-reminder>sửa bug",
	}
	for _, body := range attrEdges {
		line, err := json.Marshal(map[string]any{
			"type": "user", "message": map[string]any{"content": body},
		})
		if err != nil {
			t.Fatal(err)
		}
		raw.Write(line)
		raw.WriteByte('\n')
	}
	for _, extra := range []string{
		`{"type":"user","isSidechain":true,"message":{"content":"subagent"}}`,
		`{"type":"user","isCompactSummary":true,"message":{"content":"compacted"}}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"how do I close <system-reminder> tags? also fix login"}]}}`,
		`{"type":1,"message":{"content":"not a user record"}}`,
		`null`,
	} {
		raw.WriteString(extra)
		raw.WriteByte('\n')
	}
	// Non-user records are part of the quick filter's contract too.
	for _, typ := range []string{"assistant", "system", "summary"} {
		line, err := json.Marshal(map[string]any{
			"type": typ, "message": map[string]any{"content": "sửa bug ở hàm login"},
		})
		if err != nil {
			t.Fatal(err)
		}
		raw.Write(line)
		raw.WriteByte('\n')
	}
	cmd := exec.Command(jq, "-Rr", "-f", script)
	cmd.Stdin = bytes.NewReader(raw.Bytes())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jq: %v\n%s", err, out)
	}
	res, err := Extract(bytes.NewReader(raw.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("errors %v", res.Errors)
	}
	var want bytes.Buffer
	for _, p := range res.Prompts {
		want.WriteString(p.Text)
		want.WriteByte('\n')
	}
	if !bytes.Equal(out, want.Bytes()) {
		t.Fatalf("jq:\n%s\ngo:\n%s", out, want.Bytes())
	}

	// The hand-written 2.1.332-shaped fixture is part of this parity
	// check. Extract and the doc's jq filter both keep 3 of 3 prompts.
	fixture := readTestdata(t, "claude_code_live_shape_v2_1_332.jsonl")
	cmd = exec.Command(jq, "-Rr", "-f", script)
	cmd.Stdin = bytes.NewReader(fixture)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jq fixture: %v\n%s", err, out)
	}
	res, err = Extract(bytes.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 || len(res.Prompts) != 3 {
		t.Fatalf("fixture prompts %d errors %v", len(res.Prompts), res.Errors)
	}
	want.Reset()
	for _, p := range res.Prompts {
		want.WriteString(p.Text)
		want.WriteByte('\n')
	}
	if !bytes.Equal(out, want.Bytes()) {
		t.Fatalf("fixture jq:\n%s\ngo:\n%s", out, want.Bytes())
	}
}

func TestQuickFilterNonObjectMessageAndCrossedTags(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatal("jq is required when CI=true; quick-filter parity cannot be skipped")
		}
		t.Log("jq is not installed; skipping quick-filter difference checks locally. CI=true makes this a failure.")
		t.Skip("jq not installed")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	doc, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "dev-docs", "research", "user-prompt-extraction.md"))
	if err != nil {
		t.Fatal(err)
	}
	const fence = "```jq\n"
	i := bytes.Index(doc, []byte(fence))
	if i < 0 {
		t.Fatal("jq fence missing")
	}
	rest := doc[i+len(fence):]
	j := bytes.Index(rest, []byte("\n```"))
	if j < 0 {
		t.Fatal("jq fence not closed")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "user-prompt.jq")
	if err := os.WriteFile(script, rest[:j+1], 0o600); err != nil {
		t.Fatal(err)
	}
	raw := "{\"type\":\"user\",\"message\":[\"sửa bug\"]}\n" +
		"{\"type\":\"user\",\"message\":{\"content\":\"kept\"}}\n" +
		"{\"type\":\"user\",\"message\":{\"content\":\"<bash-stdout>a<command-name>b</bash-stdout>c</command-name>d\"}}\n"
	cmd := exec.Command(jq, "-Rr", "-f", script)
	cmd.Stdin = strings.NewReader(raw)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jq: %v\n%s", err, out)
	}
	// A non-object message is skipped here. Go records a line error.
	// The crossed line is not the same string in jq and in Go.
	want := "kept\n<bash-stdout>ad\n"
	if string(out) != want {
		t.Fatalf("jq:\n%s", out)
	}
	res, err := Extract(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 1 || res.Errors[0].Line != 1 || !strings.Contains(res.Errors[0].Error(), "message must be an object") {
		t.Fatalf("errors %v", res.Errors)
	}
	if len(res.Prompts) != 2 || res.Prompts[0].Text != "kept" || res.Prompts[1].Text != "c</command-name>d" {
		t.Fatalf("prompts %#v", res.Prompts)
	}
}

func TestExtractHarnessSidechainAndInterrupts(t *testing.T) {
	mixed := "sửa bug <command-name>/compact</command-name> <command-message>m</command-message> <command-args>a</command-args> <local-command-stdout>out</local-command-stdout> <local-command-stderr>err</local-command-stderr> <local-command-caveat>caveat</local-command-caveat> <bash-input>ls</bash-input> <bash-stdout>SECRET=1</bash-stdout> <bash-stderr>no</bash-stderr>"
	cases := []struct {
		name string
		line string
		want string
		skip bool
		err  string
	}{
		{
			name: "isSidechain",
			line: `{"type":"user","isSidechain":true,"message":{"content":"subagent prompt"}}`,
			skip: true,
		},
		{
			name: "isSidechain false",
			line: `{"type":"user","isSidechain":false,"message":{"content":"sửa bug"}}`,
			want: "sửa bug",
		},
		{
			name: "isSidechain null",
			line: `{"type":"user","isSidechain":null,"message":{"content":"sửa bug"}}`,
			want: "sửa bug",
		},
		{
			name: "isSidechain string",
			line: `{"type":"user","isSidechain":"true","message":{"content":"sửa bug"}}`,
			err:  "isSidechain must be a boolean",
		},
		{
			name: "isCompactSummary",
			line: `{"type":"user","isCompactSummary":true,"message":{"content":"auto summary"}}`,
			skip: true,
		},
		{
			name: "isCompactSummary false",
			line: `{"type":"user","isCompactSummary":false,"message":{"content":"sửa bug"}}`,
			want: "sửa bug",
		},
		{
			name: "interrupt exact",
			line: `{"type":"user","message":{"content":"[Request interrupted by user]"}}`,
			skip: true,
		},
		{
			name: "interrupt tool use",
			line: `{"type":"user","message":{"content":"  [Request interrupted by user for tool use]  "}}`,
			skip: true,
		},
		{
			name: "interrupt beside user text",
			line: `{"type":"user","message":{"content":[{"type":"text","text":"[Request interrupted by user]"},{"type":"text","text":"sửa bug"}]}}`,
			want: "sửa bug",
		},
		{
			name: "interrupt inside a larger sentence",
			line: `{"type":"user","message":{"content":"please keep [Request interrupted by user] in the note"}}`,
			want: "please keep [Request interrupted by user] in the note",
		},
		{
			name: "harness tags stripped",
			line: `{"type":"user","message":{"content":` + jsonString(mixed) + `}}`,
			want: "sửa bug",
		},
		{
			name: "unclosed reminder in array",
			line: `{"type":"user","message":{"content":[{"type":"text","text":"how do I close <system-reminder> tags? also fix login"}]}}`,
			want: "how do I close <system-reminder> tags? also fix login",
		},
		{
			name: "case and attributes",
			line: `{"type":"user","message":{"content":"<SYSTEM-REMINDER>secret</SYSTEM-REMINDER>sửa bug <system-reminder attr=\"x\">secret</system-reminder>"}}`,
			want: "sửa bug",
		},
		{
			name: "attribute quote holds a bracket",
			line: `{"type":"user","message":{"content":"<system-reminder attr=\"a>b\">secret</system-reminder>sửa bug"}}`,
			want: "sửa bug",
		},
		{
			name: "non-string type",
			line: `{"type":1,"message":{"content":"sửa bug"}}`,
			skip: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Extract(strings.NewReader(tc.line + "\n" + `{"type":"user","message":{"content":"kept"}}`))
			if err != nil {
				t.Fatal(err)
			}
			if tc.err != "" {
				if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error(), tc.err) {
					t.Fatalf("errors %v", res.Errors)
				}
				if len(res.Prompts) != 1 || res.Prompts[0].Text != "kept" {
					t.Fatalf("prompts %#v", res.Prompts)
				}
				return
			}
			if len(res.Errors) != 0 {
				t.Fatalf("errors %v", res.Errors)
			}
			if tc.skip {
				if len(res.Prompts) != 1 || res.Prompts[0].Text != "kept" {
					t.Fatalf("prompts %#v", res.Prompts)
				}
				return
			}
			if len(res.Prompts) != 2 || res.Prompts[0].Text != tc.want || res.Prompts[1].Text != "kept" {
				t.Fatalf("prompts %#v", res.Prompts)
			}
		})
	}
}

func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestExtractTagDepth(t *testing.T) {
	deep := nestReminders(MaxTagDepth) + "sửa bug"
	// One opener past the cap is literal. The matching extra closer stays,
	// and the user text after the nest stays. The line is not an error.
	over := nestReminders(MaxTagDepth+1) + "sửa bug"
	stream := `{"type":"user","message":{"content":` + jsonString(deep) + "}}\n" +
		`{"type":"user","message":{"content":` + jsonString(over) + "}}\n"
	res, err := Extract(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("errors %v", res.Errors)
	}
	if len(res.Prompts) != 2 || res.Prompts[0].Text != "sửa bug" || res.Prompts[1].Text != "</system-reminder>sửa bug" {
		t.Fatalf("prompts %#v", res.Prompts)
	}
}

func TestUnclosedOpenersDoNotDropPrompt(t *testing.T) {
	var b strings.Builder
	b.WriteString("<command-name>secret</command-name>")
	for i := 0; i < MaxTagDepth+5; i++ {
		b.WriteString("<command-name>")
	}
	b.WriteString("sửa bug")
	for i := 0; i < MaxTagDepth+5; i++ {
		b.WriteString("<bash-input>")
	}
	in := b.String()
	if got := stripHarness(in); got != strings.TrimPrefix(in, "<command-name>secret</command-name>") {
		t.Fatalf("strip %q", got)
	}
	res, err := Extract(strings.NewReader(`{"type":"user","message":{"content":` + jsonString(in) + "}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 || len(res.Prompts) != 1 {
		t.Fatalf("%#v", res)
	}
	if strings.Contains(res.Prompts[0].Text, "secret") || !strings.Contains(res.Prompts[0].Text, "sửa bug") {
		t.Fatalf("text %q", res.Prompts[0].Text)
	}
	if !strings.Contains(res.Prompts[0].Text, "<command-name>") || !strings.Contains(res.Prompts[0].Text, "<bash-input>") {
		t.Fatalf("unclosed tags dropped: %q", res.Prompts[0].Text)
	}
}

func TestSelfClosingReminderStays(t *testing.T) {
	cases := []string{
		"<system-reminder /> keep </system-reminder>",
		"<system-reminder/> keep </system-reminder>",
		`<system-reminder attr="x" /> keep </system-reminder>`,
		"<system-reminder / > keep </system-reminder>",
	}
	for _, in := range cases {
		if got := stripHarness(in); got != in {
			t.Fatalf("got %q want %q", got, in)
		}
	}
}

func TestCrossedHarnessTags(t *testing.T) {
	in := "<bash-stdout>a<command-name>b</bash-stdout>c</command-name>d"
	if got := stripHarness(in); got != "c</command-name>d" {
		t.Fatalf("got %q", got)
	}
	rev := "<command-name>a<bash-stdout>b</command-name>c</bash-stdout>d"
	if got := stripHarness(rev); got != "c</bash-stdout>d" {
		t.Fatalf("reverse got %q", got)
	}
}

func TestUnclosedAttrScanIsLinear(t *testing.T) {
	patterns := []string{"<system-reminder a", "<system-reminder a=\""}
	for _, p := range patterns {
		s := strings.Repeat(p, (1<<20)/len(p))
		start := time.Now()
		got := stripHarness(s)
		if elapsed := time.Since(start); elapsed >= 5*time.Second {
			t.Fatalf("stripHarness %q took %s", p, elapsed)
		}
		if got != s {
			t.Fatalf("stripHarness changed %q", p)
		}
		line := `{"type":"user","message":{"content":` + jsonString(s) + "}}\n"
		start = time.Now()
		res, err := Extract(strings.NewReader(line))
		if err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed >= 5*time.Second {
			t.Fatalf("Extract %q took %s", p, elapsed)
		}
		if len(res.Errors) != 0 || len(res.Prompts) != 1 || res.Prompts[0].Text != s {
			t.Fatalf("extract %q: %#v", p, res)
		}
	}
}

func nestReminders(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("<system-reminder>")
	}
	b.WriteString("inner")
	for i := 0; i < n; i++ {
		b.WriteString("</system-reminder>")
	}
	return b.String()
}

func TestExtractStreamCaps(t *testing.T) {
	limits := bound{maxLine: 1024, maxInput: 1 << 20, maxPrompts: 2, maxOutput: 1 << 20, maxErrors: 1}
	var many bytes.Buffer
	for i := 0; i < 3; i++ {
		many.WriteString(`{"type":"user","message":{"content":"p"}}` + "\n")
	}
	res, err := extractBounded(&many, limits)
	if !errors.Is(err, ErrTooManyPrompts) || res.Prompts != nil || res.Errors != nil {
		t.Fatalf("prompts cap: %v %#v", err, res)
	}

	wide := bound{maxLine: 1024, maxInput: 1 << 20, maxPrompts: 10, maxOutput: 6, maxErrors: 10}
	res, err = extractBounded(strings.NewReader(`{"type":"user","message":{"content":"abcd"}}`+"\n"+`{"type":"user","message":{"content":"abcd"}}`+"\n"), wide)
	if !errors.Is(err, ErrOutputTooLarge) || res.Prompts != nil || res.Errors != nil {
		t.Fatalf("output cap: %v %#v", err, res)
	}

	res, err = extractBounded(strings.NewReader("{\n{\n"), bound{maxLine: 1024, maxInput: 1 << 20, maxPrompts: 10, maxOutput: 100, maxErrors: 1})
	if !errors.Is(err, ErrTooManyErrors) || res.Prompts != nil || res.Errors != nil {
		t.Fatalf("errors cap: %v %#v", err, res)
	}

	prefix := `{"type":"user","message":{"content":"one"}}` + "\n"
	res, err = extractBounded(strings.NewReader(prefix), bound{maxLine: 1024, maxInput: len(prefix), maxPrompts: 10, maxOutput: 100, maxErrors: 10})
	if err != nil || len(res.Prompts) != 1 || res.Prompts[0].Text != "one" {
		t.Fatalf("exact input: %v %#v", err, res)
	}
	res, err = extractBounded(strings.NewReader(prefix+strings.Repeat("x", 8)), bound{maxLine: 1024, maxInput: len(prefix), maxPrompts: 10, maxOutput: 100, maxErrors: 10})
	if !errors.Is(err, ErrInputTooLarge) || res.Prompts != nil || res.Errors != nil {
		t.Fatalf("input cap: %v %#v", err, res)
	}
}

// claude_code_live_shape_v2_1_332.jsonl is hand-written. It follows the
// Claude Code 2.1.332 record types observed on Linux, non-interactive, on
// 2026-11-27. It is not a capture: paths, ids, and timestamps are synthetic.
// Fifteen lines: nine non-user types plus attachment in two roles, three
// typed prompts, and one tool_result-only user record.
// queue-operation repeats the greeting on content. last-prompt repeats a
// typed prompt on lastPrompt. The scan below rejects Unicode combining
// marks (category Mn). It does not check NFC normalization.
func TestClaudeCodeLiveShapeFixture(t *testing.T) {
	body := readTestdata(t, "claude_code_live_shape_v2_1_332.jsonl")
	if !utf8.Valid(body) {
		t.Fatal("fixture is not UTF-8")
	}
	lines := bytes.Split(bytes.TrimSuffix(body, []byte("\n")), []byte("\n"))
	if len(lines) != 15 {
		t.Fatalf("lines %d", len(lines))
	}
	res, err := Extract(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("errors %v", res.Errors)
	}
	session := "00000000-0000-4000-8000-000000000001"
	want := []Prompt{
		{Line: 3, UUID: "00000000-0000-4000-8000-000000000011", SessionID: session, Timestamp: "2026-11-27T00:00:01Z", Text: "Chào bạn 👋"},
		{Line: 6, UUID: "00000000-0000-4000-8000-000000000012", SessionID: session, Timestamp: "2026-11-27T00:00:03Z", Text: "Đọc README.md rồi tóm tắt một câu."},
		{Line: 10, UUID: "00000000-0000-4000-8000-000000000013", SessionID: session, Timestamp: "2026-11-27T00:00:07Z", Text: "Giữ nguyên <system-reminder> chưa đóng"},
	}
	if len(res.Prompts) != len(want) {
		t.Fatalf("prompts %#v", res.Prompts)
	}
	for i, p := range res.Prompts {
		if p != want[i] {
			t.Fatalf("prompt %d %#v want %#v", i, p, want[i])
		}
		for _, r := range p.Text {
			if unicode.Is(unicode.Mn, r) {
				t.Fatalf("prompt %d contains a combining mark: %q", i, p.Text)
			}
		}
		if strings.Contains(p.Text, "synthetic tool output") {
			t.Fatalf("toolUseResult leaked into prompt %d: %q", i, p.Text)
		}
	}
	if !strings.Contains(res.Prompts[2].Text, "<system-reminder>") || strings.Contains(res.Prompts[2].Text, "</system-reminder>") {
		t.Fatalf("unclosed reminder not preserved: %q", res.Prompts[2].Text)
	}
	var toolOnly map[string]any
	if err := json.Unmarshal(lines[10], &toolOnly); err != nil {
		t.Fatal(err)
	}
	if toolOnly["toolUseResult"] != "synthetic tool output" || toolOnly["sourceToolAssistantUUID"] != "00000000-0000-4000-8000-000000000021" {
		t.Fatalf("tool result record %#v", toolOnly)
	}
	if _, ok := toolOnly["promptSource"]; ok {
		t.Fatal("tool result record has promptSource")
	}
	if _, ok := toolOnly["sentAtMs"]; ok {
		t.Fatal("tool result record has sentAtMs")
	}
}

func TestTestdataFixtureHygiene(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	dir := filepath.Join(filepath.Dir(file), "testdata")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("testdata is empty")
	}
	uuidPattern := regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	syntheticUUID := regexp.MustCompile(`^00000000-0000-4000-8000-[0-9]{12}$`)
	// Two backslashes: a JSONL line stores a Windows path as C:\\Users.
	banned := []struct {
		name string
		re   *regexp.Regexp
	}{
		{"email", regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)},
		{"secret-token", regexp.MustCompile(`(?i)sk-[A-Za-z0-9_\-]{16,}`)},
		{"bearer", regexp.MustCompile(`(?i)bearer\s`)},
		{"api-key", regexp.MustCompile(`(?i)api[_-]?key`)},
		{"root-path", regexp.MustCompile(`(?i)/root/`)},
		{"workspace-path", regexp.MustCompile(`(?i)/workspace/`)},
		{"home-path", regexp.MustCompile(`(?i)/home/`)},
		{"users-path", regexp.MustCompile(`(?i)/users/`)},
		{"windows-users", regexp.MustCompile(`(?i)[a-z]:(\\\\|/)users`)},
		{"commit-sha", regexp.MustCompile(`(?i)\b[0-9a-f]{40}\b`)},
	}
	dbSuffix := regexp.MustCompile(`(?i)(\.db|\.sqlite3?|-wal|-shm)$`)
	sawFile := false
	for _, e := range entries {
		name := e.Name()
		fpath := filepath.Join(dir, name)
		info, err := os.Lstat(fpath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Errorf("testdata symlink %s", name)
			continue
		}
		if info.IsDir() {
			t.Errorf("testdata subdirectory %s", name)
			continue
		}
		sawFile = true
		if !strings.HasPrefix(name, "claude_code_") {
			t.Errorf("testdata file %s does not start with claude_code_", name)
		}
		if dbSuffix.MatchString(name) {
			t.Errorf("testdata must not commit %s", name)
		}
		body, err := os.ReadFile(fpath)
		if err != nil {
			t.Fatal(err)
		}
		for _, loc := range uuidPattern.FindAllIndex(body, -1) {
			if !syntheticUUID.Match(body[loc[0]:loc[1]]) {
				t.Errorf("%s line %d matches non-synthetic-uuid", name, hygieneLine(body, loc[0]))
			}
		}
		for i, line := range bytes.Split(bytes.TrimSuffix(body, []byte("\n")), []byte("\n")) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var v any
			if err := json.Unmarshal(line, &v); err != nil {
				t.Fatalf("%s line %d matches invalid-json", name, i+1)
			}
			lineNo := i + 1
			walkJSONStrings(v, func(s string) {
				if windowsDrivePath.MatchString(s) || windowsUsersSegment.MatchString(s) {
					t.Errorf("%s line %d matches windows-path", name, lineNo)
				}
				if strings.HasPrefix(s, "/") && !allowedExamplePath(s) {
					t.Errorf("%s line %d matches example-path", name, lineNo)
				}
			})
		}
		for _, rule := range banned {
			if loc := rule.re.FindIndex(body); loc != nil {
				t.Errorf("%s line %d matches %s", name, hygieneLine(body, loc[0]), rule.name)
			}
		}
	}
	if !sawFile {
		t.Fatal("testdata has no fixture files")
	}
}

// windowsDrivePath matches a decoded Windows path such as C:\ or C:/.
// windowsUsersSegment matches a \Users\ or /Users/ segment in that string.
var (
	windowsDrivePath    = regexp.MustCompile(`(?i)^[a-z]:[\\/]`)
	windowsUsersSegment = regexp.MustCompile(`(?i)[\\/]users[\\/]`)
)

func hygieneLine(body []byte, at int) int {
	return bytes.Count(body[:at], []byte("\n")) + 1
}

// allowedExamplePath accepts the synthetic root and descendants that stay
// under it. A sibling prefix and a .. segment are rejected. path.Clean
// catches an escape that a raw segment check can miss.
func allowedExamplePath(s string) bool {
	const root = "/tmp/example-project"
	if s != root && !strings.HasPrefix(s, root+"/") {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return false
		}
	}
	cleaned := path.Clean(s)
	return cleaned == root || strings.HasPrefix(cleaned, root+"/")
}

func TestFixtureHygienePathRules(t *testing.T) {
	if !allowedExamplePath("/tmp/example-project") || !allowedExamplePath("/tmp/example-project/README.md") {
		t.Fatal("example-path rejected the synthetic root")
	}
	if allowedExamplePath("/tmp/example-project-evil/x") || allowedExamplePath("/tmp/example-project/../homes/x") {
		t.Fatal("example-path allowed an escape")
	}
	// JSONL stores one Windows backslash as two bytes.
	raw := []byte(`{"cwd":"C:\\Users\\alice"}`)
	if regexp.MustCompile(`(?i)[a-z]:(\\\\|/)users`).FindIndex(raw) == nil {
		t.Fatal("windows-users missed a JSON-escaped drive path")
	}
	decoded := `C:\Users\alice`
	if !windowsDrivePath.MatchString(decoded) || !windowsUsersSegment.MatchString(decoded) {
		t.Fatal("windows-path missed a decoded drive path")
	}
}

func walkJSONStrings(v any, fn func(string)) {
	switch t := v.(type) {
	case map[string]any:
		for _, child := range t {
			walkJSONStrings(child, fn)
		}
	case []any:
		for _, child := range t {
			walkJSONStrings(child, fn)
		}
	case string:
		fn(t)
	}
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	body, err := os.ReadFile(filepath.Join(filepath.Dir(file), "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestNotWiredIntoSDF(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		base := d.Name()
		if d.IsDir() && (base == ".git" || base == "vendor" || base == "promptextract") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(body, []byte("github.com/paulpham157/sdf/internal/promptextract")) {
			t.Errorf("%s imports promptextract", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
