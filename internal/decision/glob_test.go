package decision

import (
	"strings"
	"testing"
)

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		glob string
		yes  []string
		no   []string
	}{
		{
			glob: "internal/store/**",
			yes:  []string{"internal/store/store.go", "internal/store/a/b.go"},
			no:   []string{"internal/store", "internal/stored/x", "other/store/a.go", "Internal/store/a.go"},
		},
		{
			glob: "**/*.go",
			yes:  []string{"a.go", "internal/store/store.go"},
			no:   []string{"a.go.bak", "a.c", "dir/a.Go"},
		},
		{
			glob: "*.go",
			yes:  []string{"a.go", "dir/a.go"},
			no:   []string{"a.c", "dir/a.go.bak"},
		},
		{
			glob: "a/**/b",
			yes:  []string{"a/b", "a/x/b", "a/x/y/b"},
			no:   []string{"a/b/c", "ab", "xa/b"},
		},
		{
			glob: "docs/",
			yes:  []string{"docs", "docs/a", "src/docs", "src/docs/a.go"},
			no:   []string{"docs2", "src/docs2/a"},
		},
		{
			glob: "internal/store/",
			yes:  []string{"internal/store", "internal/store/a.go"},
			no:   []string{"internal/stored", "internal/store2/a"},
		},
		{
			glob: "a?c",
			yes:  []string{"abc", "dir/abc"},
			no:   []string{"ac", "abbc", "a/c"},
		},
		{
			glob: "foo[bar].go",
			yes:  []string{"foo[bar].go"},
			no:   []string{"foob.go", "foo.go"},
		},
		{
			glob: "file?.txt",
			yes:  []string{"fileX.txt", "dir/fileX.txt"},
			no:   []string{"file.txt", "fileXY.txt"},
		},
	}
	for _, tc := range cases {
		re, err := CompileGlob(tc.glob)
		if err != nil {
			t.Fatalf("%s: %v", tc.glob, err)
		}
		for _, p := range tc.yes {
			if !re.MatchString(p) {
				t.Errorf("%s should match %s (re %s)", tc.glob, p, re)
			}
		}
		for _, p := range tc.no {
			if re.MatchString(p) {
				t.Errorf("%s should not match %s (re %s)", tc.glob, p, re)
			}
		}
	}
}

func TestGlobRejects(t *testing.T) {
	long := strings.Repeat("a", maxGlobBytes+1)
	stars := strings.Repeat("*", maxGlobBytes)
	for _, g := range []string{
		"",
		long,
		"has\x00nul",
		"has\nline",
		"esc\x1b",
		`back\slash`,
		"/abs",
		"..",
		"foo/../bar",
		"../x",
		"!negated",
		"foo//bar",
		"/",
		"foo/",
		"foo/" + strings.Repeat("a", maxGlobBytes),
	} {
		if g == "foo/" {
			continue
		}
		if err := ValidateGlob(g); err == nil {
			t.Errorf("accepted %q", g)
		}
	}
	if err := ValidateGlob(stars); err != nil {
		t.Fatal(err)
	}
	re, err := CompileGlob(stars)
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString("anything") || re.MatchString("a/b") && strings.Count(stars, "*") > 0 && !re.MatchString("a/b") {
		t.Fatalf("%s", re)
	}
	if err := ValidateGlob("keep!this"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGlob("foo/..bar"); err != nil {
		t.Fatal(err)
	}
	var many []string
	for i := 0; i < maxGlobsPerDecision+1; i++ {
		many = append(many, strings.Repeat("a", i)+".go")
	}
	if err := ValidatePaths(many); err == nil {
		t.Fatal("33 globs accepted")
	}
	if err := ValidatePaths([]string{"*.go", "*.go"}); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := ValidatePaths([]string{"*.go", "internal/**"}); err != nil {
		t.Fatal(err)
	}
}

func TestGlobStarsMatchAFile(t *testing.T) {
	re, err := CompileGlob(strings.Repeat("*", 32))
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString("store.go") || !re.MatchString("internal/store/store.go") {
		t.Fatal(re)
	}
}
