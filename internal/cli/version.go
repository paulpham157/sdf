package cli

import (
	"runtime/debug"
	"strings"
)

// version is the build-time override (-X github.com/paulpham157/sdf/internal/cli.version).
// The Makefile and Goreleaser leave it empty: ldflags are -s -w, and the
// snapshot archive name (0.0.0-<short commit>) is not written into the binary.
// An empty value uses the module version the go tool already records.
var version string

// Version is the CLI version. A module version other than (devel) is returned
// as recorded, including the local pseudo-version v0.0.0-<commit time>-<revision>
// and a +dirty suffix when the tree was modified. When the module version is
// (devel), a vcs revision becomes 0.0.0-<8-char commit>, the same shape as the
// Goreleaser snapshot version_template, plus +dirty when the tree was modified.
func Version() string {
	if v := strings.TrimSpace(version); v != "" && v != "devel" && v != "(devel)" {
		return v
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	rev := ""
	dirty := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			rev = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	if rev == "" {
		if info.Main.Version == "" {
			return "unknown"
		}
		return info.Main.Version
	}
	short := rev
	if len(short) > 8 {
		short = short[:8]
	}
	out := "0.0.0-" + short
	if dirty {
		out += "+dirty"
	}
	return out
}
