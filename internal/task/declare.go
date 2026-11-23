package task

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// RepoTaskDir is where a declared task is written inside the repository,
// relative to its root. Committed on a protected branch, that file is the
// copy CI trusts.
const RepoTaskDir = ".sdf/tasks"

// ErrDeclaredDifferently is returned when a task id is already declared
// with a different hash. A task is never redeclared in place: declare a new
// id instead.
var ErrDeclaredDifferently = errors.New("task id is already declared with a different hash")

// Store is where a declaration goes. Every field is optional, but Declare
// refuses a Store with none set.
type Store struct {
	// RepoDir, when WriteRepo is set, receives RepoTaskDir/<id>.json. It is
	// always read for a conflicting copy when set.
	RepoDir   string
	WriteRepo bool
	// DataDir receives tasks/<id>.json, read-only, outside the repository.
	DataDir string
	// Ledger receives the canonical bytes under KindTask.
	Ledger Ledger
}

// Copy is one place a task was found or written.
type Copy struct {
	Where string // "ledger", or a file path
	Hash  string
	New   bool // written by this call
}

// Declared is the result of Declare.
type Declared struct {
	Spec      Spec
	Canonical []byte
	Hash      string
	Copies    []Copy
	Entry     *Entry
}

// RepoPath returns the path of id's task file under repoDir.
func RepoPath(repoDir, id string) string {
	return filepath.Join(repoDir, filepath.FromSlash(RepoTaskDir), id+".json")
}

// DataPath returns the path of id's task file under dataDir.
func DataPath(dataDir, id string) string {
	return filepath.Join(dataDir, "tasks", id+".json")
}

// DefaultDataDir is $XDG_DATA_HOME/sdf, else ~/.local/share/sdf.
func DefaultDataDir() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, "sdf"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "sdf"), nil
}

// Declare stores spec in every place st names. It first reads each place:
// if any already holds this id with a different hash, nothing is written
// and the error wraps ErrDeclaredDifferently. A place that already holds
// the same hash is left alone, so an identical declaration is a no-op.
func Declare(ctx context.Context, spec Spec, st Store) (Declared, error) {
	canonical, hash, err := spec.Hash()
	if err != nil {
		return Declared{}, err
	}
	if st.Ledger == nil && st.DataDir == "" && !(st.WriteRepo && st.RepoDir != "") {
		return Declared{}, errors.New("no place to store the task")
	}
	d := Declared{Spec: spec, Canonical: canonical, Hash: hash}

	conflict := func(where, have string) error {
		return fmt.Errorf("%w: %s %s holds %s, this declaration is %s", ErrDeclaredDifferently, spec.ID, where, have, hash)
	}

	ledgerHas := false
	if st.Ledger != nil {
		_, e, err := st.Ledger.Get(ctx, KindTask, spec.ID)
		switch {
		case errors.Is(err, ErrNotFound):
		case err != nil:
			return Declared{}, err
		case e.Hash != hash:
			return Declared{}, conflict("in the ledger", e.Hash)
		default:
			ledgerHas = true
			d.Entry = &e
		}
	}
	type file struct {
		path    string
		mode    fs.FileMode
		dirMode fs.FileMode
		exists  bool
	}
	var files []*file
	if st.RepoDir != "" {
		files = append(files, &file{path: RepoPath(st.RepoDir, spec.ID), mode: 0o644, dirMode: 0o755})
	}
	if st.DataDir != "" {
		files = append(files, &file{path: DataPath(st.DataDir, spec.ID), mode: 0o444, dirMode: 0o700})
	}
	for _, f := range files {
		have, err := readHash(f.path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return Declared{}, err
		}
		if have != hash {
			return Declared{}, conflict("at "+f.path, have)
		}
		f.exists = true
	}

	for _, f := range files {
		isRepo := st.RepoDir != "" && f.path == RepoPath(st.RepoDir, spec.ID)
		if f.exists {
			d.Copies = append(d.Copies, Copy{Where: f.path, Hash: hash})
			continue
		}
		if isRepo && !st.WriteRepo {
			continue
		}
		if err := writeOnce(f.path, append(append([]byte(nil), canonical...), '\n'), f.mode, f.dirMode); err != nil {
			return Declared{}, err
		}
		d.Copies = append(d.Copies, Copy{Where: f.path, Hash: hash, New: true})
	}
	if st.Ledger != nil {
		e, err := st.Ledger.Append(ctx, KindTask, spec.ID, canonical, hash)
		if errors.Is(err, ErrConflict) {
			return Declared{}, fmt.Errorf("%w: %s in the ledger", ErrDeclaredDifferently, spec.ID)
		}
		if err != nil {
			return Declared{}, err
		}
		d.Entry = &e
		d.Copies = append(d.Copies, Copy{Where: "ledger", Hash: hash, New: !ledgerHas})
	}
	return d, nil
}

func readHash(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	_, h, err := Parse(b)
	if err != nil {
		return "", fmt.Errorf("%s: %w", p, err)
	}
	return h, nil
}

// writeOnce creates p with data, all or nothing, and fails if p already
// exists. The bytes go to a temporary file in the same directory, are
// synced, and are then hard-linked to p, which refuses an existing p (a
// rename would replace it). A missing parent is created with dirMode.
func writeOnce(p string, data []byte, mode, dirMode fs.FileMode) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(p)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Link(name, p)
}

// Found is one copy of a task read by Load.
type Found struct {
	Where string
	Spec  Spec
	Hash  string
	Entry *Entry
}

// Load reads id from every place st names, checks each copy parses as
// canonical, and requires them to agree. It returns the copies it found;
// no copy at all is fs.ErrNotExist.
func Load(ctx context.Context, id string, st Store) ([]Found, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("id %q must match %s", id, idPattern)
	}
	var found []Found
	if st.Ledger != nil {
		b, e, err := st.Ledger.Get(ctx, KindTask, id)
		switch {
		case errors.Is(err, ErrNotFound):
		case err != nil:
			return nil, err
		default:
			s, h, err := Parse(b)
			if err != nil {
				return nil, fmt.Errorf("ledger: %w", err)
			}
			if h != e.Hash {
				return nil, fmt.Errorf("ledger: payload hashes to %s, entry says %s", h, e.Hash)
			}
			found = append(found, Found{Where: "ledger", Spec: s, Hash: h, Entry: &e})
		}
	}
	var paths []string
	if st.RepoDir != "" {
		paths = append(paths, RepoPath(st.RepoDir, id))
	}
	if st.DataDir != "" {
		paths = append(paths, DataPath(st.DataDir, id))
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		s, h, err := Parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if s.ID != id {
			return nil, fmt.Errorf("%s: holds task %q, not %q", p, s.ID, id)
		}
		found = append(found, Found{Where: p, Spec: s, Hash: h})
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("task %q: %w", id, fs.ErrNotExist)
	}
	for _, f := range found[1:] {
		if f.Hash != found[0].Hash {
			return found, fmt.Errorf("%w: %s at %s is %s, at %s is %s", ErrDeclaredDifferently, id, found[0].Where, found[0].Hash, f.Where, f.Hash)
		}
	}
	return found, nil
}
