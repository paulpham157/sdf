package cli

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestVersionUsesTheRecordedModuleVersion(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("no build info")
	}
	got := Version()
	if got == "" {
		t.Fatal("empty version")
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		if got != v {
			t.Fatalf("Version() = %q, module version %q", got, v)
		}
		return
	}
	if got != "(devel)" && got != "unknown" && !strings.HasPrefix(got, "0.0.0-") {
		t.Fatalf("Version() = %q", got)
	}
}
