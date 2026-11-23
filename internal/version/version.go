// Package version reports which sdf build is running.
//
// Release builds set Version, Commit, and Date with -ldflags -X. A build
// without them, such as `go install github.com/paulpham157/sdf/cmd/sdf@v0.1.0`
// or a plain `go build`, falls back to the module version and VCS settings
// the Go toolchain records in the binary.
package version

import (
	"fmt"
	"regexp"
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

// pseudo matches the tail of a Go pseudo-version, such as
// v0.0.0-20261123054030-cda1d87f0356 or v0.1.1-0.20261123054030-cda1d87f0356.
var pseudo = regexp.MustCompile(`[-.](\d{14})-([0-9a-f]{12})(?:\+[0-9A-Za-z.-]+)?$`)

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
	if info.Commit == "" || info.Date == "" {
		// go install ...@<commit> records a pseudo-version and no VCS
		// settings. Its last two fields are the commit time and revision.
		if m := pseudo.FindStringSubmatch(info.Version); m != nil {
			if info.Date == "" {
				info.Date = m[1][0:4] + "-" + m[1][4:6] + "-" + m[1][6:8] + "T" + m[1][8:10] + ":" + m[1][10:12] + ":" + m[1][12:14] + "Z"
			}
			if info.Commit == "" {
				info.Commit = m[2]
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
