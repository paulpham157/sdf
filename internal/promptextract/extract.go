package promptextract

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// MaxLineBytes is the largest raw line Extract will parse, including the
// trailing newline. bufio.Scanner stops at 64 KiB by default, and a Claude
// Code session line can be larger than that, so Extract reads with
// bufio.Reader instead. A longer line is skipped and reported as
// ErrLineTooLong. The limit is the memory bound for one line.
const MaxLineBytes = 32 << 20

// ErrLineTooLong is the error inside a LineError when a line is longer
// than the maximum (MaxLineBytes for Extract).
var ErrLineTooLong = errors.New("line exceeds the maximum line size")

// Prompt is one user-typed prompt. UUID, SessionID, and Timestamp are
// empty when the record omits them. Line is 1-based in the stream.
type Prompt struct {
	Line      int
	UUID      string
	SessionID string
	Timestamp string
	Text      string
}

// LineError is a bad line that was skipped. The rest of the stream is
// still read. Line is 1-based.
type LineError struct {
	Line int
	Err  error
}

func (e LineError) Error() string {
	return fmt.Sprintf("line %d: %v", e.Line, e.Err)
}

func (e LineError) Unwrap() error { return e.Err }

// Result is every prompt Extract kept and every line it skipped because
// the line was malformed or too long.
type Result struct {
	Prompts []Prompt
	Errors  []LineError
}

// Extract reads a Claude Code session JSONL stream and returns the
// user-typed prompts it recognizes.
//
// A malformed line or a line longer than MaxLineBytes is recorded in
// Result.Errors and skipped. A read error from r, other than the end of
// the stream, is returned. Prompts and line errors collected before that
// read error are still in the Result. A nil reader is an error.
func Extract(r io.Reader) (Result, error) {
	return extractLimited(r, MaxLineBytes)
}

func extractLimited(r io.Reader, maxLine int) (Result, error) {
	if r == nil {
		return Result{}, errors.New("promptextract: nil reader")
	}
	if maxLine < 1 {
		return Result{}, errors.New("promptextract: max line must be positive")
	}
	br := bufio.NewReader(r)
	var res Result
	n := 0
	for {
		line, tooLong, err := readLine(br, maxLine)
		if err != nil && err != io.EOF {
			return res, fmt.Errorf("promptextract: read: %w", err)
		}
		if tooLong {
			n++
			res.Errors = append(res.Errors, LineError{
				Line: n,
				Err:  fmt.Errorf("%w (%d bytes)", ErrLineTooLong, maxLine),
			})
			if err == io.EOF {
				return res, nil
			}
			continue
		}
		if err == io.EOF && len(line) == 0 {
			return res, nil
		}
		n++
		if len(bytes.TrimSpace(line)) > 0 {
			p, ok, perr := promptFromLine(n, line)
			if perr != nil {
				res.Errors = append(res.Errors, LineError{Line: n, Err: perr})
			} else if ok {
				res.Prompts = append(res.Prompts, p)
			}
		}
		if err == io.EOF {
			return res, nil
		}
	}
}

// readLine reads one newline-delimited line. The returned line does not
// include the trailing newline. tooLong is set when the raw line, including
// its newline, is longer than max; the rest of that line is discarded.
// err is io.EOF when the stream ends, including when the last line has no
// newline. Any other error is from the underlying reader.
func readLine(r *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	var buf []byte
	for {
		frag, e := r.ReadSlice('\n')
		if e != nil && e != bufio.ErrBufferFull && e != io.EOF {
			return nil, false, e
		}
		if tooLong {
			if e == bufio.ErrBufferFull {
				continue
			}
			if e == io.EOF {
				return nil, true, io.EOF
			}
			return nil, true, nil
		}
		if len(frag) > max-len(buf) {
			buf = nil
			tooLong = true
			if e == bufio.ErrBufferFull {
				continue
			}
			if e == io.EOF {
				return nil, true, io.EOF
			}
			return nil, true, nil
		}
		buf = append(buf, frag...)
		if e == bufio.ErrBufferFull {
			continue
		}
		return bytes.TrimRight(buf, "\r\n"), false, e
	}
}

func promptFromLine(lineNo int, line []byte) (Prompt, bool, error) {
	fields, err := jsonObject(line)
	if err != nil {
		return Prompt{}, false, fmt.Errorf("malformed json: %w", err)
	}
	if fields == nil {
		return Prompt{}, false, nil
	}
	typ, ok := jsonStringOrSkip(fields["type"])
	if !ok || typ != "user" {
		return Prompt{}, false, nil
	}
	meta, err := jsonBool(fields["isMeta"])
	if err != nil {
		return Prompt{}, false, fmt.Errorf("malformed json: %w", err)
	}
	if meta {
		return Prompt{}, false, nil
	}
	uuid, err := optionalString(fields, "uuid")
	if err != nil {
		return Prompt{}, false, fmt.Errorf("malformed json: %w", err)
	}
	sessionID, err := optionalString(fields, "sessionId")
	if err != nil {
		return Prompt{}, false, fmt.Errorf("malformed json: %w", err)
	}
	timestamp, err := optionalString(fields, "timestamp")
	if err != nil {
		return Prompt{}, false, fmt.Errorf("malformed json: %w", err)
	}
	text, err := textFromMessage(fields["message"])
	if err != nil {
		return Prompt{}, false, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Prompt{}, false, nil
	}
	return Prompt{
		Line:      lineNo,
		UUID:      uuid,
		SessionID: sessionID,
		Timestamp: timestamp,
		Text:      text,
	}, true, nil
}

// jsonObject parses one JSON object. A JSON null is a nil map and no
// error. Map keys keep the spelling in the file; encoding/json's
// case-insensitive struct match is not used.
func jsonObject(line []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	var fields map[string]json.RawMessage
	if err := dec.Decode(&fields); err != nil {
		return nil, err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("trailing data")
		}
		return nil, err
	}
	return fields, nil
}

// jsonStringOrSkip reports the string value of a JSON string. ok is false
// when the value is missing, null, or any other JSON type. A record whose
// type is not the string "user" is not a prompt, so a non-string type is
// skipped rather than reported as a broken line.
func jsonStringOrSkip(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || raw[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func jsonBool(raw json.RawMessage) (bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false, nil
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, errors.New("isMeta must be a boolean")
	}
	return v, nil
}

func optionalString(fields map[string]json.RawMessage, key string) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", nil
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return s, nil
}

func textFromMessage(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	if raw[0] != '{' {
		return "", errors.New("message must be an object")
	}
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return "", fmt.Errorf("message: %w", err)
	}
	return textFromContent(msg["content"])
}

func textFromContent(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", fmt.Errorf("message.content: %w", err)
		}
		return stripReminders(s), nil
	case '[':
		var blocks []json.RawMessage
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return "", fmt.Errorf("message.content: %w", err)
		}
		var parts []string
		for _, block := range blocks {
			piece, keep, err := textBlock(block)
			if err != nil {
				return "", err
			}
			if keep {
				parts = append(parts, piece)
			}
		}
		return strings.Join(parts, "\n"), nil
	default:
		return "", errors.New("message.content must be a string or an array of blocks")
	}
}

// textBlock returns the trimmed user text of a type "text" block.
// tool_result blocks and any other type are dropped. keep is false when
// the block contributes no text.
func textBlock(raw json.RawMessage) (string, bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false, nil
	}
	if raw[0] != '{' {
		return "", false, errors.New("message.content block must be an object")
	}
	var block map[string]json.RawMessage
	if err := json.Unmarshal(raw, &block); err != nil {
		return "", false, fmt.Errorf("message.content: %w", err)
	}
	kind, ok := jsonStringOrSkip(block["type"])
	if !ok || kind != "text" {
		return "", false, nil
	}
	textRaw := bytes.TrimSpace(block["text"])
	if len(textRaw) == 0 || bytes.Equal(textRaw, []byte("null")) {
		return "", false, nil
	}
	var text string
	if err := json.Unmarshal(textRaw, &text); err != nil {
		return "", false, errors.New("text block's text must be a string")
	}
	text = strings.TrimSpace(stripReminders(text))
	if text == "" {
		return "", false, nil
	}
	return text, true, nil
}

// systemReminder matches a well-formed <system-reminder> region, including
// one that spans lines. Go's regexp is RE2: the (?s) flag lets . match a
// newline, and the match is non-greedy so two regions on one line stay
// separate.
var systemReminder = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// stripReminders removes well-formed <system-reminder> regions. An opener
// with no closer drops the opener and everything after it in that string,
// which is the rest of the block. A closer with no opener is left in
// place. Nested regions are not matched as a tree: the first closer ends
// the first opener.
func stripReminders(s string) string {
	s = systemReminder.ReplaceAllString(s, "")
	if i := strings.Index(s, "<system-reminder>"); i >= 0 {
		s = s[:i]
	}
	return s
}
