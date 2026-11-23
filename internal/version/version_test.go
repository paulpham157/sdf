package version

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestLdflagsWin(t *testing.T) {
	info := resolve("v0.1.0", "abc123", "2026-11-23T00:00:00Z", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{GoVersion: "go1.26.0", Main: debug.Module{Version: "v9.9.9"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "zzz"}}}, true
	})
	if info.Version != "v0.1.0" || info.Commit != "abc123" || info.Date != "2026-11-23T00:00:00Z" || info.GoVersion != "go1.26.0" {
		t.Fatalf("%+v", info)
	}
}

func TestGoInstallFallsBackToModuleVersion(t *testing.T) {
	// go install ...@v0.1.0 records the module version and no VCS settings.
	info := resolve("", "", "", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{GoVersion: "go1.26.1", Main: debug.Module{Path: "github.com/paulpham157/sdf", Version: "v0.1.0"}}, true
	})
	if info.Version != "v0.1.0" || info.Commit != "unknown" || info.Date != "unknown" || info.GoVersion != "go1.26.1" {
		t.Fatalf("%+v", info)
	}
}

func TestLocalBuildUsesVCSSettings(t *testing.T) {
	info := resolve("", "", "", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{GoVersion: "go1.26.1", Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "deadbeef"},
			{Key: "vcs.time", Value: "2026-11-20T01:02:03Z"},
			{Key: "vcs.modified", Value: "true"},
		}}, true
	})
	if info.Version != "dev" || info.Commit != "deadbeef" || info.Date != "2026-11-20T01:02:03Z" || !info.Modified {
		t.Fatalf("%+v", info)
	}
	text := info.String()
	for _, want := range []string{"sdf dev\n", "commit: deadbeef (modified)\n", "built: 2026-11-20T01:02:03Z\n", "go: go1.26.1\n", "platform: "} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
}

func TestNoBuildInfo(t *testing.T) {
	info := resolve("", "", "", func() (*debug.BuildInfo, bool) { return nil, false })
	if info.Version != "dev" || info.Commit != "unknown" || info.Date != "unknown" || info.GoVersion == "" {
		t.Fatalf("%+v", info)
	}
}

func TestPseudoVersionFillsCommitAndDate(t *testing.T) {
	info := resolve("", "", "", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{GoVersion: "go1.26.1", Main: debug.Module{Version: "v0.0.0-20261123054030-cda1d87f0356"}}, true
	})
	if info.Version != "v0.0.0-20261123054030-cda1d87f0356" || info.Commit != "cda1d87f0356" || info.Date != "2026-11-23T05:40:30Z" {
		t.Fatalf("%+v", info)
	}
	tagged := resolve("", "", "", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, true
	})
	if tagged.Commit != "unknown" || tagged.Date != "unknown" {
		t.Fatalf("%+v", tagged)
	}
}
