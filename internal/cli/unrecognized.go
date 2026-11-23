package cli

import (
	"fmt"
	"io"

	"github.com/paulpham157/sdf/internal/ledger"
)

// unrecognizedArgs reports an unexpected positional argument or unknown
// flag. ledger.Safe leaves printable text alone and returns a Go-quoted
// ASCII string when the value has a control character, so a typo cannot
// write a terminal escape to stderr. Do not wrap the result in %q: Safe
// already quotes when it needs to.
func unrecognizedArgs(stderr io.Writer, arg string) {
	fmt.Fprintf(stderr, "unrecognized arguments: %s\n", ledger.Safe(arg))
}
