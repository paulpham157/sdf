package cli

import (
	"testing"

	"github.com/paulpham157/sdf/internal/record"
)

func TestParseStoreContent(t *testing.T) {
	for _, off := range []string{"", "0", "false", "off", "no", " , "} {
		spec, err := parseStoreContent(off)
		if err != nil || spec.on() || spec.keep() != nil {
			t.Fatalf("%q: %+v %v", off, spec, err)
		}
	}
	for _, all := range []string{"1", "true", "on", "yes", "all", "ALL"} {
		spec, err := parseStoreContent(all)
		if err != nil || !spec.all || spec.describe() != "every agent" {
			t.Fatalf("%q: %+v %v", all, spec, err)
		}
	}
	spec, err := parseStoreContent("qwen, claude ,copilot-cli")
	if err != nil || spec.describe() != "claude-code, copilot-cli, qwen-code" {
		t.Fatalf("%+v %v", spec, err)
	}
	keep := spec.keep()
	if !keep(record.Record{ServiceName: record.Str("claude-code")}) || keep(record.Record{ServiceName: record.Str("codex")}) {
		t.Fatal("per-agent keep")
	}
	if _, err := parseStoreContent("claude,cursor"); err == nil {
		t.Fatal("unknown agent accepted")
	}
}
