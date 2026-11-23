package verify

import (
	"os"
	"sync"
)

// nsInitArg, as os.Args[1], makes this binary (sdf, or a test binary that
// links this package) act as pid 1 of a criterion's namespace instead of
// running normally. See isolate_linux.go.
const nsInitArg = "__sdf-verify-ns-init"

func init() {
	if len(os.Args) > 1 && os.Args[1] == nsInitArg {
		os.Exit(nsInitMain(os.Args[2:]))
	}
}

// Isolation names how criteria were run: IsolationNamespace (Linux user,
// pid and mount namespaces, everything killed when the check exits) or
// IsolationNone.
const (
	IsolationNamespace = "pid-namespace"
	IsolationNone      = "none"
)

var (
	probeMu    sync.Mutex
	probeDone  bool
	probeOK    bool
	probeError string
)

// isolationAvailable reports, once per process, whether a criterion can run
// in its own namespaces here.
var isolationAvailable = func() (bool, string) {
	probeMu.Lock()
	defer probeMu.Unlock()
	if !probeDone {
		probeOK, probeError = probeIsolation()
		probeDone = true
	}
	return probeOK, probeError
}

// resetIsolationProbe forgets the cached probe (tests only).
func resetIsolationProbe() {
	probeMu.Lock()
	probeDone = false
	probeMu.Unlock()
}
