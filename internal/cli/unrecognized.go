package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/paulpham157/sdf/internal/ledger"
)

const maxArgDisplay = 256

// unrecognizedArgs reports an unexpected positional argument or unknown
// flag. Secret-bearing flag values are redacted first, then the text is
// quoted with displayArg (ledger.Safe, plus non-ASCII spaces) and capped
// at maxArgDisplay bytes plus an ellipsis. Do not wrap the result in %q:
// displayArg already quotes when it needs to.
func unrecognizedArgs(stderr io.Writer, arg string) {
	fmt.Fprintf(stderr, "unrecognized arguments: %s\n", displayArg(arg))
}

// displayArg prepares user-supplied text for a terminal: redact, quote
// unsafe runes, then truncate. File-path error sites call displayPath or
// displayErr instead, which only quote.
func displayArg(arg string) string {
	return truncateDisplay(quoteArg(redactSecretFlag(arg)), maxArgDisplay)
}

// displayPath is ledger.Safe for a file path shown in an error.
func displayPath(path string) string {
	return ledger.Safe(path)
}

// displayErr is ledger.Safe for an error that may embed a user-supplied path.
func displayErr(err error) string {
	if err == nil {
		return ""
	}
	return ledger.Safe(err.Error())
}

// redactSecretFlag replaces the value of --name=value (or -name=value)
// when the flag name has a hyphen-separated segment token, secret,
// password, passwd, or key, compared without regard to case. The value is
// replaced with [redacted] so a typo cannot echo a secret to stderr.
func redactSecretFlag(arg string) string {
	if !strings.HasPrefix(arg, "-") {
		return arg
	}
	dashes := 0
	for dashes < len(arg) && arg[dashes] == '-' {
		dashes++
	}
	rest := arg[dashes:]
	eq := strings.IndexByte(rest, '=')
	if eq < 0 {
		return arg
	}
	name := rest[:eq]
	if !isSecretFlagName(name) {
		return arg
	}
	return arg[:dashes] + name + "=[redacted]"
}

func isSecretFlagName(name string) bool {
	for _, part := range strings.Split(strings.ToLower(name), "-") {
		switch part {
		case "token", "secret", "password", "passwd", "key":
			return true
		}
	}
	return false
}

// quoteArg is ledger.Safe, and also quotes a string that contains a
// Unicode space separator other than ASCII space (for example U+00A0
// NBSP). Those runes are printable, so Safe would leave them raw.
func quoteArg(s string) string {
	if quoted := ledger.Safe(s); quoted != s {
		return quoted
	}
	for _, r := range s {
		if unicode.Is(unicode.Zs, r) && r != ' ' {
			return strconv.QuoteToASCII(s)
		}
	}
	return s
}

// truncateDisplay caps s at max bytes, then an ellipsis, without
// splitting a UTF-8 rune or a Go escape sequence. It is applied after
// quoting, so a quoted control character such as \x1b is kept whole.
func truncateDisplay(s string, max int) string {
	if max < 0 {
		max = 0
	}
	if len(s) <= max {
		return s
	}
	cut := safeCut(s, max)
	return s[:cut] + "..."
}

// safeCut returns a byte index at most max that does not split a rune
// or a Go-style backslash escape (\n, \xHH, \uHHHH, \UHHHHHHHH, \\).
func safeCut(s string, max int) int {
	if max <= 0 {
		return 0
	}
	if max >= len(s) {
		return len(s)
	}
	return lastCompleteTokenEnd(s, max)
}

func lastCompleteTokenEnd(s string, cut int) int {
	last := 0
	i := 0
	for i < len(s) && i < cut {
		if s[i] != '\\' {
			_, w := utf8.DecodeRuneInString(s[i:])
			if i+w > cut {
				break
			}
			i += w
			last = i
			continue
		}
		end := escapeEnd(s, i)
		if end < 0 || end > cut {
			break
		}
		i = end
		last = i
	}
	return last
}

// escapeEnd is the index just past a Go escape starting at i, or -1 if
// the escape is incomplete.
func escapeEnd(s string, i int) int {
	if i >= len(s) || s[i] != '\\' {
		return -1
	}
	if i+1 >= len(s) {
		return -1
	}
	switch s[i+1] {
	case 'x':
		if i+4 <= len(s) && isHex(s[i+2]) && isHex(s[i+3]) {
			return i + 4
		}
		return -1
	case 'u':
		if i+6 <= len(s) && allHex(s[i+2:i+6]) {
			return i + 6
		}
		return -1
	case 'U':
		if i+10 <= len(s) && allHex(s[i+2:i+10]) {
			return i + 10
		}
		return -1
	case '0', '1', '2', '3', '4', '5', '6', '7':
		end := i + 2
		for end < len(s) && end < i+4 && s[end] >= '0' && s[end] <= '7' {
			end++
		}
		return end
	default:
		// \n, \t, \\, \", and any other single-byte escape.
		return i + 2
	}
}

func isHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

func allHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isHex(s[i]) {
			return false
		}
	}
	return true
}
