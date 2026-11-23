package task

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

func TestValidIDRefusesControlAndBadUTF8(t *testing.T) {
	for _, id := range []string{"a\nb", "a\x00", "a\tb", "a\u0085b", string([]byte{'a', 0xff}), "é", "", " a", "a b", strings.Repeat("a", 129)} {
		if ValidID(id) {
			t.Errorf("%q accepted", id)
		}
	}
	for _, id := range []string{"a", "add-widget", "v1.2_x", strings.Repeat("a", 128)} {
		if !ValidID(id) {
			t.Errorf("%q refused", id)
		}
	}
}

func TestParseAndLoadRefuseABadID(t *testing.T) {
	dir, _ := testRepo(t, repoFiles)
	s, err := Build(context.Background(), dir, sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	v := s.value()
	v["id"] = "add\nwidget"
	b, err := Canonical(v)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Parse(b); err == nil {
		t.Fatal("parse accepted an id with a newline")
	}
	if _, err := Load(context.Background(), "add\nwidget", Store{RepoDir: dir}); err == nil {
		t.Fatal("load accepted an id with a newline")
	}
	in := sampleInput()
	in.ID = "add\x00widget"
	if _, err := Build(context.Background(), dir, in); err == nil {
		t.Fatal("build accepted an id with NUL")
	}
	in = sampleInput()
	in.Criteria[0].Name = "unit\n"
	if _, err := Build(context.Background(), dir, in); err == nil {
		t.Fatal("build accepted a criterion name with a newline")
	}
}

func symlinkRepo(t *testing.T, links map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	for p, c := range map[string]string{"tests/real_test.sh": "real\n", "tests/sub/x.sh": "x\n", "README": "r\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for p, target := range links {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, p)); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "c")
	return dir
}

func TestDesignatedSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	ctx := context.Background()
	in := func(glob string) Input {
		return Input{ID: "s", Title: "S", Criteria: []CriterionInput{{Name: "u", Argv: []string{"true"}, Designated: []string{glob}}}}
	}
	// In-repo links pin the link and, through a chain, the file it reaches.
	dir := symlinkRepo(t, map[string]string{"tests/link_test.sh": "hop.sh", "tests/hop.sh": "real_test.sh"})
	s, err := Build(ctx, dir, in("tests/link_test.sh"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range s.DesignatedFiles {
		got = append(got, f.Path+":"+f.Mode)
	}
	if strings.Join(got, " ") != "tests/hop.sh:120000 tests/link_test.sh:120000 tests/real_test.sh:100644" {
		t.Fatalf("%v", got)
	}
	for name, target := range map[string]string{
		"absolute":       "/etc/passwd",
		"escapes":        "../../outside",
		"to a directory": "sub",
		"dangling":       "missing.sh",
	} {
		dir := symlinkRepo(t, map[string]string{"tests/bad_test.sh": target})
		if _, err := Build(ctx, dir, in("tests/bad_test.sh")); err == nil {
			t.Errorf("%s link accepted", name)
		}
	}
}

func TestValidateRefusesUncleanDesignatedPaths(t *testing.T) {
	base := Spec{Schema: Schema, ID: "t", Title: "T", BaseSHA: strings.Repeat("a", 40), BaseTree: strings.Repeat("b", 40),
		Criteria: []Criterion{{Name: "u", Argv: []string{"true"}, TimeoutS: 1}}}
	for _, p := range []string{"/abs", "../x", "a/../b", "./a", "a//b", "a/", "a\nb", ".."} {
		s := base
		s.DesignatedFiles = []DesignatedFile{{Path: p, Mode: "100644", GitBlob: strings.Repeat("c", 40), SHA256: strings.Repeat("d", 64)}}
		if err := s.Validate(); err == nil {
			t.Errorf("path %q accepted", p)
		}
	}
}

func TestParseInputRefusesDuplicateAndCaseVariantKeys(t *testing.T) {
	for _, doc := range []string{
		`{"id":"a","ID":"b","title":"t","criteria":[]}`,
		`{"Id":"a","title":"t","criteria":[]}`,
		`{"id":"a","id":"b","title":"t","criteria":[]}`,
		`{"id":"a","title":"t","criteria":[{"name":"u","Argv":["true"]}]}`,
		`{"id":"a","title":"t","criteria":[{"name":"u","argv":["true"],"name":"v"}]}`,
	} {
		if _, err := ParseInput([]byte(doc)); err == nil {
			t.Errorf("accepted %s", doc)
		}
	}
	if _, err := ParseInput([]byte(`{"id":"a","title":"T","criteria":[{"name":"u","argv":["True"]}]}`)); err != nil {
		t.Fatalf("values may have any case: %v", err)
	}
}

func TestIDsKeepTheirCase(t *testing.T) {
	for _, id := range []string{"T1", "Add-Widget", "01JD5Z8K3QW9V4X7Y2M6N0P1RS"} {
		if !ValidID(id) {
			t.Errorf("%q refused", id)
		}
	}
}

func TestBuildFromASubdirectory(t *testing.T) {
	dir, _ := testRepo(t, repoFiles)
	root, err := RepoRoot(context.Background(), filepath.Join(dir, "pkg", "a"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	if got, _ := filepath.EvalSymlinks(root); got != want {
		t.Fatalf("root %s, want %s", got, want)
	}
	a, err := Build(context.Background(), filepath.Join(dir, "pkg", "a"), sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Build(context.Background(), dir, sampleInput())
	if _, ha, _ := a.Hash(); func() string { _, hb, _ := b.Hash(); return hb }() != ha {
		t.Fatal("subdirectory build differs")
	}
}

func TestGoTestDesignatesTestsTestdataAndModule(t *testing.T) {
	paths := []string{"go.mod", "go.sum", "main.go", "a/a.go", "a/a_test.go", "a/testdata/in.txt",
		"a/b/b_test.go", "a/b/testdata/deep/x", "c/c_test.go", "testdata/root.txt", "x_test.go"}
	cases := map[string]string{
		"go test ./...":            "go.mod go.sum a/a_test.go a/testdata/in.txt a/b/b_test.go a/b/testdata/deep/x c/c_test.go testdata/root.txt x_test.go",
		"go test ./a":              "go.mod go.sum a/a_test.go a/testdata/in.txt",
		"go test -run X ./a/...":   "go.mod go.sum a/a_test.go a/testdata/in.txt a/b/b_test.go a/b/testdata/deep/x",
		"go test":                  "go.mod go.sum testdata/root.txt x_test.go",
		"go vet ./c":               "go.mod go.sum c/c_test.go",
		"go build ./...":           "",
		"go test example.com/x/..": "go.mod go.sum testdata/root.txt x_test.go",
	}
	for cmd, want := range cases {
		if got := strings.Join(goTestPaths(strings.Fields(cmd), paths), " "); got != want {
			t.Errorf("%s:\n got %q\nwant %q", cmd, got, want)
		}
	}
}

func TestBuildRefusesACriterionThatPinsNothing(t *testing.T) {
	dir, _ := testRepo(t, repoFiles)
	in := sampleInput()
	in.Criteria = []CriterionInput{{Name: "u", Argv: []string{"true"}}}
	if _, err := Build(context.Background(), dir, in); err == nil || !strings.Contains(err.Error(), "designates no files") {
		t.Fatalf("%v", err)
	}
}
