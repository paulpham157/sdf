package promptextract

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
		{name: "unclosed reminder", body: "sửa bug ở hàm login <system-reminder>still hidden", want: "sửa bug ở hàm login"},
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
		{
			name: "command-name kept",
			body: "<command-name>/compact</command-name>",
			want: "<command-name>/compact</command-name>",
		},
		{
			name: "local-command-stdout kept",
			body: "<local-command-stdout>total 0</local-command-stdout>",
			want: "<local-command-stdout>total 0</local-command-stdout>",
		},
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

func TestStripRemindersNestedOpener(t *testing.T) {
	in := "<system-reminder>outer <system-reminder>inner</system-reminder> tail</system-reminder>sửa bug"
	got := strings.TrimSpace(stripReminders(in))
	want := "tail</system-reminder>sửa bug"
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
