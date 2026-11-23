package cli

import "github.com/paulpham157/sdf/internal/version"

// Version is the CLI version: the -X ldflags value a release build sets on
// github.com/paulpham157/sdf/internal/version.Version, otherwise the module
// version the go tool records (for example v0.1.0 from go install ...@v0.1.0,
// or a v0.0.0-<time>-<revision> pseudo-version for a local build), otherwise
// "dev". `sdf version` also prints the commit, build date, and Go version.
func Version() string {
	return version.Get().Version
}
