package task

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

var repoFiles = map[string]string{
	"go.mod":                  "module x\n",
	"Makefile":                "test:\n\tgo test ./...\n",
	"scripts/check.sh":        "#!/bin/sh\nexit 0\n",
	"pkg/a/a.go":              "package a\n",
	"pkg/a/a_test.go":         "package a\n// test a\n",
	"pkg/b/deep/b_test.go":    "package deep\n// test b\n",
	"pkg/b/deep/testdata/x.t": "fixture\n",
	"README.md":               "readme\n",
}

func exitPtr(n int64) *int64 { return &n }

func sampleInput() Input {
	return Input{
		ID:        "add-widget",
		Title:     "Add the widget",
		Objective: "Ship the widget",
		Criteria: []CriterionInput{
			{Name: "unit", Argv: []string{"make", "test"}, Designated: []string{"pkg/**/*_test.go", "./pkg/b/deep/testdata/*", "pkg/a/a_test.go"}},
			{Name: "script", Argv: []string{"sh", "./scripts/check.sh", "--flag"}, TimeoutS: 30, ExpectExit: exitPtr(0)},
		},
	}
}

func TestBuildExpandsDesignatedFilesAtTheBase(t *testing.T) {
	dir, sha := testRepo(t, repoFiles)
	s, err := Build(context.Background(), dir, sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	if s.BaseSHA != sha || s.BaseTree != git(t, dir, "rev-parse", "HEAD^{tree}") {
		t.Fatalf("base %s tree %s", s.BaseSHA, s.BaseTree)
	}
	var paths []string
	for _, f := range s.DesignatedFiles {
		paths = append(paths, f.Path)
		if f.GitBlob != git(t, dir, "rev-parse", "HEAD:"+f.Path) || f.Mode != "100644" {
			t.Fatalf("%+v", f)
		}
	}
	want := "Makefile pkg/a/a_test.go pkg/b/deep/b_test.go pkg/b/deep/testdata/x.t scripts/check.sh"
	if got := strings.Join(paths, " "); got != want {
		t.Fatalf("designated %q, want %q", got, want)
	}
	if s.Criteria[0].Name != "script" || s.Criteria[1].Name != "unit" {
		t.Fatalf("criteria not sorted: %+v", s.Criteria)
	}
	if got := strings.Join(s.Criteria[1].Designated, " "); got != "pkg/**/*_test.go pkg/a/a_test.go pkg/b/deep/testdata/*" {
		t.Fatalf("globs %q", got)
	}
	if s.Criteria[1].TimeoutS != DefaultTimeoutS || !s.SecretScan {
		t.Fatalf("defaults: %+v secret_scan=%t", s.Criteria[1], s.SecretScan)
	}
}

func TestHashIsDeterministicAcrossInputOrderAndWorkingTreeEdits(t *testing.T) {
	dir, _ := testRepo(t, repoFiles)
	ctx := context.Background()
	a, err := Build(ctx, dir, sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	in := sampleInput()
	in.Criteria[0], in.Criteria[1] = in.Criteria[1], in.Criteria[0]
	in.Criteria[1].Designated = []string{"pkg/a/a_test.go", "pkg/b/deep/testdata/*", "pkg/**/*_test.go", "pkg/a/a_test.go"}
	in.Criteria[1].TimeoutS = DefaultTimeoutS
	// An uncommitted edit to a designated file does not change the base.
	if err := writeFile(dir, "pkg/a/a_test.go", "edited\n"); err != nil {
		t.Fatal(err)
	}
	b, err := Build(ctx, dir, in)
	if err != nil {
		t.Fatal(err)
	}
	ca, ha, err := a.Hash()
	if err != nil {
		t.Fatal(err)
	}
	cb, hb, err := b.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ca, cb) || ha != hb {
		t.Fatalf("hash differs:\n%s\n%s", ca, cb)
	}
	for i := 0; i < 5; i++ {
		c, h, _ := a.Hash()
		if !bytes.Equal(c, ca) || h != ha {
			t.Fatal("hash not stable")
		}
	}
	if ha != PayloadHash(KindTask, ca) || !strings.HasPrefix(ha, "sha256:") {
		t.Fatalf("hash scheme: %s", ha)
	}

	// A committed change to a designated file changes the hash at the new base.
	commitFiles(t, dir, map[string]string{"pkg/a/a_test.go": "changed\n"})
	c, err := Build(ctx, dir, sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	if _, hc, _ := c.Hash(); hc == ha {
		t.Fatal("hash unchanged after the test file changed")
	}
}

func TestCanonicalBytesArePinned(t *testing.T) {
	s := Spec{
		Schema:   Schema,
		ID:       "t1",
		Title:    "T",
		BaseSHA:  strings.Repeat("a", 40),
		BaseTree: strings.Repeat("b", 40),
		Criteria: []Criterion{{Name: "unit", Argv: []string{"go", "test", "./..."}, TimeoutS: 600}},
		DesignatedFiles: []DesignatedFile{
			{Path: "x_test.go", Mode: "100644", GitBlob: strings.Repeat("c", 40), SHA256: strings.Repeat("d", 64)},
		},
		SecretScan: true,
	}
	c, h, err := s.Hash()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"base_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","base_tree":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","criteria":[{"argv":["go","test","./..."],"designated":[],"expect_exit":0,"name":"unit","timeout_s":600}],"designated_files":[{"git_blob":"cccccccccccccccccccccccccccccccccccccccc","mode":"100644","path":"x_test.go","sha256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}],"id":"t1","objective":"","schema":"sdf.task/v1","secret_scan":true,"title":"T"}`
	if string(c) != want {
		t.Fatalf("\n got %s\nwant %s", c, want)
	}
	// sha256("sdf.task/v1\n" + want). Changing it means every stored task id changes meaning.
	// Checked independently with: printf 'sdf.task/v1\n%s' "$want" | sha256sum
	const wantHash = "sha256:7418505609e2648057ae7dc3836d1c69c218d693c1c989abc16fcdabaa0be8af"
	if h != wantHash {
		t.Fatalf("hash %s", h)
	}

	got, gotHash, err := Parse(append(c, '\n'))
	if err != nil || gotHash != h || got.ID != "t1" {
		t.Fatalf("parse: %v %s", err, gotHash)
	}
}

func TestParseRefusesNonCanonicalFiles(t *testing.T) {
	dir, _ := testRepo(t, repoFiles)
	s, err := Build(context.Background(), dir, sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	c, _, _ := s.Hash()
	var pretty bytes.Buffer
	_ = json.Indent(&pretty, c, "", "  ")
	cases := map[string][]byte{
		"indented":     pretty.Bytes(),
		"extra field":  bytes.Replace(c, []byte(`{"base_sha"`), []byte(`{"x":1,"base_sha"`), 1),
		"float":        bytes.Replace(c, []byte(`"timeout_s":30`), []byte(`"timeout_s":30.0`), 1),
		"two newlines": append(append([]byte(nil), c...), '\n', '\n'),
		"edited title": bytes.Replace(c, []byte(`"Add the widget"`), []byte(`"Add the gadget"`), 1),
	}
	for name, b := range cases {
		_, h, err := Parse(b)
		if name == "edited title" {
			// Still canonical, but it is a different task.
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if _, orig, _ := s.Hash(); h == orig {
				t.Fatalf("%s: same hash", name)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestBuildRefusesBadInput(t *testing.T) {
	dir, _ := testRepo(t, repoFiles)
	ctx := context.Background()
	mod := func(f func(*Input)) Input { in := sampleInput(); f(&in); return in }
	for name, in := range map[string]Input{
		"bad id":        mod(func(in *Input) { in.ID = "../x" }),
		"no title":      mod(func(in *Input) { in.Title = " " }),
		"no criteria":   mod(func(in *Input) { in.Criteria = nil }),
		"dup criterion": mod(func(in *Input) { in.Criteria[1].Name = "unit" }),
		"empty argv":    mod(func(in *Input) { in.Criteria[0].Argv = nil }),
		"glob no match": mod(func(in *Input) { in.Criteria[0].Designated = []string{"nope/*_test.go"} }),
		"glob escapes":  mod(func(in *Input) { in.Criteria[0].Designated = []string{"../x"} }),
		"glob absolute": mod(func(in *Input) { in.Criteria[0].Designated = []string{"/etc/passwd"} }),
		"bad base":      mod(func(in *Input) { in.Base = "no-such-rev" }),
		"option base":   mod(func(in *Input) { in.Base = "--all" }),
		"timeout":       mod(func(in *Input) { in.Criteria[0].TimeoutS = MaxTimeoutS + 1 }),
		"expect_exit":   mod(func(in *Input) { in.Criteria[0].ExpectExit = exitPtr(256) }),
	} {
		if _, err := Build(ctx, dir, in); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestMatchGlob(t *testing.T) {
	for _, c := range []struct {
		glob, path string
		want       bool
	}{
		{"**/*_test.go", "a_test.go", true},
		{"**/*_test.go", "x/y/a_test.go", true},
		{"pkg/**", "pkg/a/b", true},
		{"pkg/*", "pkg/a/b", false},
		{"pkg/*_test.go", "pkg/a_test.go", true},
		{"pkg/**/b", "pkg/b", true},
		{"a", "ab", false},
	} {
		if got := matchGlob(c.glob, c.path); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %t", c.glob, c.path, got)
		}
	}
}
