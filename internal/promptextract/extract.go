package promptextract

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MaxLineBytes is the largest raw line Extract will read, including the
// trailing newline. It caps that input line only. Decoding one line of
// that size keeps several copies (the raw buffer, the JSON value, and the
// extracted string), so peak memory for one max-sized line is several
// times higher, about 3-5x MaxLineBytes. bufio.Scanner stops at 64 KiB by
// default, so Extract uses bufio.Reader instead. A longer line is skipped
// and reported as ErrLineTooLong.
const MaxLineBytes = 32 << 20

// MaxInputBytes is the most Extract will read from the stream. A further
// byte fails closed with ErrInputTooLarge.
const MaxInputBytes = 256 << 20

// MaxPrompts is the most prompts Extract will return. One more fails
// closed with ErrTooManyPrompts.
const MaxPrompts = 10000

// MaxOutputBytes is the most extracted prompt text, in bytes, Extract will
// return. One more byte fails closed with ErrOutputTooLarge.
const MaxOutputBytes = 32 << 20

// MaxErrors is the most line errors Extract will return. One more fails
// closed with ErrTooManyErrors.
const MaxErrors = 1000

// ErrLineTooLong is the error inside a LineError when a line is longer
// than the maximum (MaxLineBytes for Extract).
var ErrLineTooLong = errors.New("line exceeds the maximum line size")

// ErrInputTooLarge, ErrTooManyPrompts, ErrOutputTooLarge, and
// ErrTooManyErrors are returned by Extract when a stream cap is hit.
// The Result is empty: a capped extract is not a partial success.
var (
	ErrInputTooLarge  = errors.New("stream exceeds the maximum input size")
	ErrTooManyPrompts = errors.New("too many prompts")
	ErrOutputTooLarge = errors.New("extracted text exceeds the maximum output size")
	ErrTooManyErrors  = errors.New("too many line errors")
)

// ErrTagDepth is the line error when one harness tag is nested deeper
// than MaxTagDepth.
var ErrTagDepth = errors.New("harness tag nested too deeply")

// Prompt is one user-typed prompt. UUID, SessionID, and Timestamp are
// empty when the record omits them. Line is 1-based in the stream.
// Text is raw: it is not redacted.
type Prompt struct {
	Line      int
	UUID      string
	SessionID string
	Timestamp string
	Text      string
}

// LineError is a bad line that was skipped. The rest of the stream is
// still read, until a stream cap fails the extract. Line is 1-based.
type LineError struct {
	Line int
	Err  error
}

func (e LineError) Error() string {
	return fmt.Sprintf("line %d: %v", e.Line, e.Err)
}

func (e LineError) Unwrap() error { return e.Err }

// Result is every prompt Extract kept and every line it skipped because
// the line was malformed, too long, or nested a harness tag too deeply.
// A stream cap does not return a partial Result.
type Result struct {
	Prompts []Prompt
	Errors  []LineError
}

type bound struct {
	maxLine    int
	maxInput   int
	maxPrompts int
	maxOutput  int
	maxErrors  int
}

func defaultBounds() bound {
	return bound{
		maxLine:    MaxLineBytes,
		maxInput:   MaxInputBytes,
		maxPrompts: MaxPrompts,
		maxOutput:  MaxOutputBytes,
		maxErrors:  MaxErrors,
	}
}

// Extract reads a Claude Code session JSONL stream and returns the
// user-typed prompts it recognizes.
//
// A malformed line, a line longer than MaxLineBytes, or a line whose
// harness tags nest deeper than MaxTagDepth is recorded in Result.Errors
// and skipped. A read error from r, other than the end of the stream, is
// returned with the prompts and line errors collected before it. Hitting
// MaxInputBytes, MaxPrompts, MaxOutputBytes, or MaxErrors returns that
// sentinel and an empty Result. A nil reader is an error.
func Extract(r io.Reader) (Result, error) {
	return extractBounded(r, defaultBounds())
}

func extractLimited(r io.Reader, maxLine int) (Result, error) {
	b := defaultBounds()
	b.maxLine = maxLine
	return extractBounded(r, b)
}

func extractBounded(r io.Reader, b bound) (Result, error) {
	if r == nil {
		return Result{}, errors.New("promptextract: nil reader")
	}
	if b.maxLine < 1 || b.maxInput < 1 || b.maxPrompts < 1 || b.maxOutput < 1 || b.maxErrors < 1 {
		return Result{}, errors.New("promptextract: limits must be positive")
	}
	br := bufio.NewReader(&countingReader{r: r, max: b.maxInput})
	var res Result
	outBytes := 0
	n := 0
	for {
		line, tooLong, err := readLine(br, b.maxLine)
		if err != nil && err != io.EOF {
			if errors.Is(err, ErrInputTooLarge) {
				return Result{}, ErrInputTooLarge
			}
			return res, fmt.Errorf("promptextract: read: %w", err)
		}
		if tooLong {
			n++
			if len(res.Errors) >= b.maxErrors {
				return Result{}, ErrTooManyErrors
			}
			res.Errors = append(res.Errors, LineError{
				Line: n,
				Err:  fmt.Errorf("%w (%d bytes)", ErrLineTooLong, b.maxLine),
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
				if len(res.Errors) >= b.maxErrors {
					return Result{}, ErrTooManyErrors
				}
				res.Errors = append(res.Errors, LineError{Line: n, Err: perr})
			} else if ok {
				if len(res.Prompts) >= b.maxPrompts {
					return Result{}, ErrTooManyPrompts
				}
				if len(p.Text) > b.maxOutput || outBytes > b.maxOutput-len(p.Text) {
					return Result{}, ErrOutputTooLarge
				}
				outBytes += len(p.Text)
				res.Prompts = append(res.Prompts, p)
			}
		}
		if err == io.EOF {
			return res, nil
		}
	}
}

// countingReader stops a stream after max bytes. The next read that would
// pass that cap returns ErrInputTooLarge. A stream that ends on the cap
// is not an error.
type countingReader struct {
	r   io.Reader
	n   int
	max int
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.n >= c.max {
		var probe [1]byte
		n, err := c.r.Read(probe[:])
		if n > 0 {
			return 0, ErrInputTooLarge
		}
		return 0, err
	}
	remain := c.max - c.n
	if len(p) > remain {
		p = p[:remain]
	}
	n, err := c.r.Read(p)
	c.n += n
	return n, err
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
	meta, err := jsonBool(fields, "isMeta")
	if err != nil {
		return Prompt{}, false, fmt.Errorf("malformed json: %w", err)
	}
	side, err := jsonBool(fields, "isSidechain")
	if err != nil {
		return Prompt{}, false, fmt.Errorf("malformed json: %w", err)
	}
	compact, err := jsonBool(fields, "isCompactSummary")
	if err != nil {
		return Prompt{}, false, fmt.Errorf("malformed json: %w", err)
	}
	// isMeta, isSidechain, and isCompactSummary skip the record when JSON
	// true. Whether a live file sets them on skills, subagent prompts, or
	// compaction summaries is needs verification.
	if meta || side || compact {
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
	if text == "" || isInterrupt(text) {
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
// error, and the caller skips that line. Map keys keep the spelling in
// the file; encoding/json's case-insensitive struct match is not used.
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

func jsonBool(fields map[string]json.RawMessage, key string) (bool, error) {
	raw, ok := fields[key]
	if !ok {
		return false, nil
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false, nil
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
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
		return stripHarness(s)
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
// the block contributes no text. A non-object element is an error.
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
	text, err := stripHarness(text)
	if err != nil {
		return "", false, err
	}
	text = strings.TrimSpace(text)
	if text == "" || isInterrupt(text) {
		return "", false, nil
	}
	return text, true, nil
}
