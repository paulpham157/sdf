package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// qwenObject is a JSON object that keeps key order, so a rewritten user
// file differs from the original only where sdf changed it.
type qwenObject struct {
	keys []string
	vals map[string]any
}

func newQwenObject() *qwenObject {
	return &qwenObject{vals: map[string]any{}}
}

func (o *qwenObject) get(key string) (any, bool) {
	v, ok := o.vals[key]
	return v, ok
}

func (o *qwenObject) set(key string, v any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

// qwenParseJSON reads one JSON value. Numbers stay json.Number, so a big
// integer is written back exactly. A duplicate key keeps its first position
// and its last value, as JSON.parse does.
func qwenParseJSON(body []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	v, err := qwenDecode(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the JSON value")
	}
	return v, nil
}

func qwenDecode(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := newQwenObject()
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("object key is %T", keyTok)
				}
				v, err := qwenDecode(dec)
				if err != nil {
					return nil, err
				}
				obj.set(key, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := qwenDecode(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return tok, nil
	}
}

// qwenWriteJSON indents like JSON.stringify(v, null, indent) and does
// not escape &, <, or >.
func qwenWriteJSON(v any, indent string) ([]byte, error) {
	var b bytes.Buffer
	if err := qwenEncode(&b, v, indent, 0); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func qwenEncode(b *bytes.Buffer, v any, indent string, level int) error {
	pad := func(n int) {
		b.WriteByte('\n')
		b.WriteString(strings.Repeat(indent, n))
	}
	switch t := v.(type) {
	case *qwenObject:
		if len(t.keys) == 0 {
			b.WriteString("{}")
			return nil
		}
		b.WriteByte('{')
		for i, key := range t.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			pad(level + 1)
			if err := qwenEncodeScalar(b, key); err != nil {
				return err
			}
			b.WriteString(": ")
			if err := qwenEncode(b, t.vals[key], indent, level+1); err != nil {
				return err
			}
		}
		pad(level)
		b.WriteByte('}')
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			pad(level + 1)
			if err := qwenEncode(b, item, indent, level+1); err != nil {
				return err
			}
		}
		pad(level)
		b.WriteByte(']')
	default:
		return qwenEncodeScalar(b, t)
	}
	return nil
}

func qwenEncodeScalar(b *bytes.Buffer, v any) error {
	if n, ok := v.(json.Number); ok {
		b.WriteString(n.String())
		return nil
	}
	var tmp bytes.Buffer
	enc := json.NewEncoder(&tmp)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	b.Write(bytes.TrimRight(tmp.Bytes(), "\n"))
	return nil
}

// qwenDetectIndent returns the indent of the first indented line, or two
// spaces (what Qwen Code writes).
func qwenDetectIndent(body []byte) string {
	for _, line := range strings.Split(string(body), "\n")[1:] {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && len(trimmed) < len(line) {
			return line[:len(line)-len(trimmed)]
		}
	}
	return "  "
}

// qwenStripComments removes // and /* */ comments outside strings, for
// reading a settings file Qwen Code accepts (strip-json-comments). It is
// used only to read; a file with comments is not rewritten.
func qwenStripComments(body []byte) []byte {
	var out bytes.Buffer
	inString, escaped := false, false
	for i := 0; i < len(body); i++ {
		c := body[i]
		if inString {
			out.WriteByte(c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(body) && body[i+1] == '/' {
			for i < len(body) && body[i] != '\n' {
				i++
			}
			if i < len(body) {
				out.WriteByte('\n')
			}
			continue
		}
		if c == '/' && i+1 < len(body) && body[i+1] == '*' {
			i += 2
			for i+1 < len(body) && !(body[i] == '*' && body[i+1] == '/') {
				i++
			}
			i++
			out.WriteByte(' ')
			continue
		}
		out.WriteByte(c)
	}
	return out.Bytes()
}
