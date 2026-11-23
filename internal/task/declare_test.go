package task

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/paulpham157/sdf/internal/ledger/ledgertest"
)

func writeFile(dir, p, content string) error {
	return os.WriteFile(filepath.Join(dir, filepath.FromSlash(p)), []byte(content), 0o644)
}

func TestDeclareIsWriteOnceAndIdempotent(t *testing.T) {
	ctx := context.Background()
	dir, _ := testRepo(t, repoFiles)
	data := t.TempDir()
	l := ledgertest.New(t)
	st := Store{RepoDir: dir, WriteRepo: true, DataDir: data, Ledger: l}

	s, err := Build(ctx, dir, sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	d, err := Declare(ctx, s, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Copies) != 3 || !d.Copies[0].New || !d.Copies[1].New || !d.Copies[2].New || d.Entry == nil {
		t.Fatalf("%+v", d.Copies)
	}
	repoFile := RepoPath(dir, s.ID)
	if repoFile != filepath.Join(dir, ".sdf", "tasks", "add-widget.json") {
		t.Fatal(repoFile)
	}
	b, err := os.ReadFile(repoFile)
	if err != nil || string(b) != string(d.Canonical)+"\n" {
		t.Fatalf("repo file %q %v", b, err)
	}
	info, err := os.Stat(DataPath(data, s.ID))
	if err != nil || info.Mode().Perm() != 0o444 {
		t.Fatalf("data file %v %v", info, err)
	}
	if runtime.GOOS != "windows" {
		if di, err := os.Stat(filepath.Dir(DataPath(data, s.ID))); err != nil || di.Mode().Perm() != 0o700 {
			t.Fatalf("data dir %v %v", di, err)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".sdf", "tasks", ".tmp-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
	if err := writeOnce(repoFile, []byte("x"), 0o644, 0o755); err == nil {
		t.Fatal("writeOnce replaced an existing file")
	}

	// Identical declaration: nothing new, same hash, ledger entry unchanged.
	again, err := Declare(ctx, s, st)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range again.Copies {
		if c.New || c.Hash != d.Hash {
			t.Fatalf("%+v", again.Copies)
		}
	}
	if *again.Entry != *d.Entry || ledgertest.Entries(t, l) != 2 {
		t.Fatal("ledger changed on an identical declaration")
	}

	// Same id, different content: refused, nothing rewritten.
	in := sampleInput()
	in.Title = "Something else"
	other, err := Build(ctx, dir, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Declare(ctx, other, st); !errors.Is(err, ErrDeclaredDifferently) {
		t.Fatalf("redeclare: %v", err)
	}
	b2, _ := os.ReadFile(repoFile)
	if string(b2) != string(b) || ledgertest.Entries(t, l) != 2 {
		t.Fatal("conflicting declaration wrote something")
	}

	found, err := Load(ctx, s.ID, st)
	if err != nil || len(found) != 3 || found[0].Hash != d.Hash {
		t.Fatalf("load %+v %v", found, err)
	}
}

func TestDeclareRefusesWhenAnyOneCopyDiffers(t *testing.T) {
	ctx := context.Background()
	dir, _ := testRepo(t, repoFiles)
	s, err := Build(ctx, dir, sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	in := sampleInput()
	in.Objective = "different"
	other, err := Build(ctx, dir, in)
	if err != nil {
		t.Fatal(err)
	}
	for name, first := range map[string]func(t *testing.T) Store{
		"repo file": func(t *testing.T) Store { return Store{RepoDir: dir, WriteRepo: true} },
		"data dir":  func(t *testing.T) Store { return Store{DataDir: t.TempDir()} },
		"ledger":    func(t *testing.T) Store { return Store{Ledger: ledgertest.New(t)} },
	} {
		t.Run(name, func(t *testing.T) {
			st := first(t)
			if _, err := Declare(ctx, s, st); err != nil {
				t.Fatal(err)
			}
			if _, err := Declare(ctx, other, st); !errors.Is(err, ErrDeclaredDifferently) {
				t.Fatalf("%v", err)
			}
			if st.RepoDir != "" {
				_ = os.RemoveAll(filepath.Join(dir, ".sdf"))
			}
		})
	}

	// A conflicting repo file blocks the ledger write too, even without --write.
	if _, err := Declare(ctx, s, Store{RepoDir: dir, WriteRepo: true}); err != nil {
		t.Fatal(err)
	}
	l := ledgertest.New(t)
	if _, err := Declare(ctx, other, Store{RepoDir: dir, Ledger: l}); !errors.Is(err, ErrDeclaredDifferently) {
		t.Fatalf("%v", err)
	}
	if ledgertest.Entries(t, l) != 0 {
		t.Fatal("ledger written despite a conflicting repo file")
	}
}

func TestLoadReportsMissingAndTampered(t *testing.T) {
	ctx := context.Background()
	dir, _ := testRepo(t, repoFiles)
	if _, err := Load(ctx, "nope", Store{RepoDir: dir}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%v", err)
	}
	s, _ := Build(ctx, dir, sampleInput())
	if _, err := Declare(ctx, s, Store{RepoDir: dir, WriteRepo: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(RepoPath(dir, s.ID), []byte(`{"id":"add-widget"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(ctx, s.ID, Store{RepoDir: dir}); err == nil {
		t.Fatal("tampered file loaded")
	}
}

func TestDeclareRefusesACaseVariantID(t *testing.T) {
	ctx := context.Background()
	dir, _ := testRepo(t, repoFiles)
	in := sampleInput()
	in.ID = "Feat-1"
	s, err := Build(ctx, dir, in)
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	if _, err := Declare(ctx, s, Store{RepoDir: dir, WriteRepo: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := Declare(ctx, s, Store{DataDir: data}); err != nil {
		t.Fatal(err)
	}
	in.ID = "feat-1"
	lower, err := Build(ctx, dir, in)
	if err != nil {
		t.Fatal(err)
	}
	for name, st := range map[string]Store{
		"repo":     {RepoDir: dir, WriteRepo: true},
		"data dir": {DataDir: data},
	} {
		if _, err := Declare(ctx, lower, st); !errors.Is(err, ErrIDCaseCollision) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The same id, same case, is still the idempotent path.
	if _, err := Declare(ctx, s, Store{RepoDir: dir, WriteRepo: true, DataDir: data}); err != nil {
		t.Fatal(err)
	}
}
