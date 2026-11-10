package record

import (
	"fmt"
	"strconv"
	"strings"
)

// SummaryText renders a value the way the Python summary's _text does.
// Bools are "true" and "false". Empty strings are absent.
func SummaryText(v any) *string {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case bool:
		if t {
			return Str("true")
		}
		return Str("false")
	case string:
		return nonEmpty(strings.TrimSpace(t))
	case int:
		return Str(strconv.Itoa(t))
	case int64:
		return Str(strconv.FormatInt(t, 10))
	case int32:
		return Str(strconv.FormatInt(int64(t), 10))
	case float64:
		return Str(strconv.FormatFloat(t, 'f', -1, 64))
	case float32:
		return Str(strconv.FormatFloat(float64(t), 'f', -1, 64))
	default:
		return nonEmpty(strings.TrimSpace(fmt.Sprint(t)))
	}
}

// OTLPText renders a value the way the Python otlp module's _text does.
// Bools use Python's str(True) / str(False).
func OTLPText(v any) *string {
	if v == nil {
		return nil
	}
	var s string
	switch t := v.(type) {
	case string:
		s = t
	case bool:
		if t {
			s = "True"
		} else {
			s = "False"
		}
	case int:
		s = strconv.Itoa(t)
	case int64:
		s = strconv.FormatInt(t, 10)
	case float64:
		s = strconv.FormatFloat(t, 'f', -1, 64)
	default:
		s = fmt.Sprint(t)
	}
	return nonEmpty(strings.TrimSpace(s))
}

// Number parses a metric or token amount. Bools are rejected, matching
// Python's check that bool is a subclass of int.
func Number(v any) *float64 {
	switch t := v.(type) {
	case nil, bool:
		return nil
	case int:
		f := float64(t)
		return &f
	case int64:
		f := float64(t)
		return &f
	case int32:
		f := float64(t)
		return &f
	case float64:
		f := t
		return &f
	case float32:
		f := float64(t)
		return &f
	case string:
		if strings.TrimSpace(t) == "" {
			return nil
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return nil
		}
		return &f
	default:
		return nil
	}
}

// Boolish parses the loose truthiness used for pi.tool.is_error.
// ok is false when the value is not a bool, number, or true/false/0/1 string.
func Boolish(v any) (val bool, ok bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case int:
		return t != 0, true
	case int64:
		return t != 0, true
	case float64:
		return t != 0, true
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "1":
			return true, true
		case "false", "0":
			return false, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

// Str returns a pointer to a copy of s.
func Str(s string) *string {
	out := s
	return &out
}

// Int64 returns a pointer to n.
func Int64(n int64) *int64 {
	out := n
	return &out
}

// Float returns a pointer to f.
func Float(f float64) *float64 {
	out := f
	return &out
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return Str(s)
}
