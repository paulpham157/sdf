// Package version reports which sdf build is running.
//
// Release builds set Version, Commit, and Date with -ldflags -X. A build
// without them, such as `go install github.com/paulpham157/sdf/cmd/sdf@v0.1.0`
// or a plain `go build`, falls back to the module version and VCS settings
// the Go toolchain records in the binary.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// Set at link time, for example:
//
//	-X github.com/paulpham157/sdf/internal/version.Version=v0.1.0
var (
	Version = ""
	Commit  = ""
	Date    = ""
)

// Info is one resolved build description.
type Info struct {
	Version   string
	Commit    string
	Date      string
	GoVersion string
	Modified  bool
}

// Get resolves the build description from ldflags, then build info.
func Get() Info {
	return resolve(Version, Commit, Date, readBuildInfo)
}

func readBuildInfo() (*debug.BuildInfo, bool) {
	return debug.ReadBuildInfo()
}

func resolve(ver, commit, date string, read func() (*debug.BuildInfo, bool)) Info {
	info := Info{Version: ver, Commit: commit, Date: date, GoVersion: runtime.Version()}
	if bi, ok := read(); ok && bi != nil {
		if bi.GoVersion != "" {
			info.GoVersion = bi.GoVersion
		}
		if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			info.Version = bi.Main.Version
		}
		for _, setting := range bi.Settings {
			switch setting.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = setting.Value
				}
			case "vcs.time":
				if info.Date == "" {
					info.Date = setting.Value
				}
			case "vcs.modified":
				// Only meaningful when the commit also came from build info.
				info.Modified = commit == "" && setting.Value == "true"
			}
		}
	}
	if info.Version == "" {
		info.Version = "dev"
	}
	if info.Commit == "" {
		info.Commit = "unknown"
	}
	if info.Date == "" {
		info.Date = "unknown"
	}
	return info
}

// String is the multi-line text `sdf version` prints.
func (i Info) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "sdf %s\n", i.Version)
	commit := i.Commit
	if i.Modified && !strings.HasSuffix(commit, "-dirty") {
		commit += " (modified)"
	}
	fmt.Fprintf(&b, "commit: %s\n", commit)
	fmt.Fprintf(&b, "built: %s\n", i.Date)
	fmt.Fprintf(&b, "go: %s\n", i.GoVersion)
	fmt.Fprintf(&b, "platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	return b.String()
}
