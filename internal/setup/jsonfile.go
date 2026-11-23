package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

// A settings file is decoded with UseNumber, so integers wider than a float64
// keep every digit. Its key order is read from the token stream and replayed
// on encode: keys that were in the file keep their place, and keys sdf adds
// follow in sorted order. Strings are encoded with SetEscapeHTML(false), so
// & < > stay as written. Indentation follows the file's first indented line
// (two spaces for a new file). Insignificant whitespace and \u escapes of
// printable characters are not kept byte for byte.

// keyOrder is the member order of one JSON object or the element orders of
// one array, as it appeared in the file.
type keyOrder struct {
	keys   []string
	fields map[string]*keyOrder
	items  []*keyOrder
}

func decodeSettings(body []byte) (map[string]any, *keyOrder, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return nil, nil, err
	}
	if root == nil {
		root = map[string]any{}
	}
	order, err := readKeyOrder(json.NewDecoder(bytes.NewReader(body)))
	if err != nil {
		return nil, nil, err
	}
	return root, order, nil
}

func readKeyOrder(dec *json.Decoder) (*keyOrder, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil, nil
	}
	order := &keyOrder{}
	switch delim {
	case '{':
		order.fields = map[string]*keyOrder{}
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := tok.(string)
			if !ok {
				return nil, fmt.Errorf("object key %v is not a string", tok)
			}
			child, err := readKeyOrder(dec)
			if err != nil {
				return nil, err
			}
			if _, dup := order.fields[key]; !dup {
				order.keys = append(order.keys, key)
			}
			order.fields[key] = child
		}
	case '[':
		for dec.More() {
			child, err := readKeyOrder(dec)
			if err != nil {
				return nil, err
			}
			order.items = append(order.items, child)
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return order, nil
}

func encodeSettings(root map[string]any, order *keyOrder, indent string) ([]byte, error) {
	var compact bytes.Buffer
	if err := encodeOrdered(&compact, root, order); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact.Bytes(), "", indent); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func encodeOrdered(buf *bytes.Buffer, value any, order *keyOrder) error {
	switch v := value.(type) {
	case map[string]any:
		buf.WriteByte('{')
		for i, key := range orderedKeys(v, order) {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeLeaf(buf, key); err != nil {
				return err
			}
			buf.WriteByte(':')
			var child *keyOrder
			if order != nil {
				child = order.fields[key]
			}
			if err := encodeOrdered(buf, v[key], child); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil
	case []any:
		buf.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			var child *keyOrder
			if order != nil && i < len(order.items) {
				child = order.items[i]
			}
			if err := encodeOrdered(buf, item, child); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	default:
		return encodeLeaf(buf, value)
	}
}

func orderedKeys(m map[string]any, order *keyOrder) []string {
	keys := make([]string, 0, len(m))
	seen := make(map[string]bool, len(m))
	if order != nil {
		for _, key := range order.keys {
			if _, ok := m[key]; ok && !seen[key] {
				keys = append(keys, key)
				seen[key] = true
			}
		}
	}
	var added []string
	for key := range m {
		if !seen[key] {
			added = append(added, key)
		}
	}
	sort.Strings(added)
	return append(keys, added...)
}

func encodeLeaf(buf *bytes.Buffer, value any) error {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return err
	}
	// Encode ends every value with a newline.
	buf.Truncate(buf.Len() - 1)
	return nil
}

// detectIndent returns the leading whitespace of the first indented line, or
// two spaces.
func detectIndent(body []byte) string {
	for _, line := range strings.Split(string(body), "\n")[1:] {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || len(trimmed) == len(line) {
			continue
		}
		return line[:len(line)-len(trimmed)]
	}
	return "  "
}

// jsonEqual compares two values by their JSON meaning, so a json.Number read
// from the file equals the int sdf would write, and maps compare by content.
func jsonEqual(a, b any) bool {
	na, errA := normalizeJSON(a)
	nb, errB := normalizeJSON(b)
	if errA != nil || errB != nil {
		return false
	}
	return reflect.DeepEqual(na, nb)
}

func normalizeJSON(v any) (any, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// planJSON reads path, applies merge, and returns the bytes that --write
// would put there. changed is false when merge reports no change. It does
// not touch the filesystem beyond reading path.
func planJSON(path string, merge func(map[string]any) (bool, error)) ([]byte, bool, error) {
	root, order, indent, changed, err := planMerge(path, merge)
	if err != nil {
		return nil, false, err
	}
	encoded, err := encodeSettings(root, order, indent)
	if err != nil {
		return nil, false, err
	}
	return encoded, changed, nil
}

func planMerge(path string, merge func(map[string]any) (bool, error)) (map[string]any, *keyOrder, string, bool, error) {
	root := map[string]any{}
	var order *keyOrder
	indent := "  "
	if body, err := os.ReadFile(path); err == nil {
		if !json.Valid(body) {
			return nil, nil, "", false, fmt.Errorf("%s is not JSON; it was not changed", path)
		}
		root, order, err = decodeSettings(body)
		if err != nil {
			return nil, nil, "", false, fmt.Errorf("%s: %w", path, err)
		}
		indent = detectIndent(body)
	} else if !os.IsNotExist(err) {
		return nil, nil, "", false, readErr(path, err)
	}
	changed, err := merge(root)
	if err != nil {
		return nil, nil, "", false, err
	}
	return root, order, indent, changed, nil
}

// previewJSON is the dry-run text for a JSON merge. It shows only the keys
// sdf manages (the paths in shape, with the merged values), never the rest of
// the file, which can hold API keys in env or mcpServers.
func previewJSON(path string, merge func(map[string]any) (bool, error), shape map[string]any) string {
	root, order, _, changed, err := planMerge(path, merge)
	if err != nil {
		return fmt.Sprintf("not written: %v. --write will refuse.\n", err)
	}
	if !changed {
		return path + " already has these values (not written)\n"
	}
	managed := restrictTo(root, shape)
	encoded, err := encodeSettings(managed, order, "  ")
	if err != nil {
		return fmt.Sprintf("not written: %v\n", err)
	}
	others := 0
	for key := range root {
		if _, ok := shape[key]; !ok {
			others++
		}
	}
	note := "Keys sdf does not manage are kept as they are and not shown."
	if others > 0 {
		note = fmt.Sprintf("%d other top-level key(s), and keys sdf does not manage inside these objects, are kept as they are and not shown.", others)
	}
	return fmt.Sprintf("not written (pass --write to update %s; an existing file is copied to a .sdf-bak sibling first).\nsdf would set:\n%s%s\n", path, encoded, note)
}

// restrictTo returns the parts of merged named by shape: an object in shape
// recurses, any other value takes merged's value whole.
func restrictTo(merged, shape map[string]any) map[string]any {
	out := map[string]any{}
	for key, want := range shape {
		have, ok := merged[key]
		if !ok {
			continue
		}
		wantMap, wantObj := want.(map[string]any)
		haveMap, haveObj := have.(map[string]any)
		if wantObj && haveObj {
			out[key] = restrictTo(haveMap, wantMap)
			continue
		}
		out[key] = have
	}
	return out
}

func mergeJSON(path string, write bool, merge func(map[string]any) (bool, error)) (string, error) {
	if !write {
		return "not written (pass --write to update " + path + "; an existing file is copied to a .sdf-bak sibling first)\n", nil
	}
	encoded, changed, err := planJSON(path, merge)
	if err != nil {
		return "", err
	}
	if !changed {
		return path + " already has these values\n", nil
	}
	fallback := os.FileMode(0o600)
	if strings.Contains(path, string(filepath.Separator)+".pi"+string(filepath.Separator)) {
		fallback = 0o644
	}
	return replaceFile(path, encoded, fallback)
}

// resolveTarget follows path's symlinks by hand, so a link to a file that
// does not exist yet (a stow-style dotfile) resolves to the file to create.
// A relative link is read relative to the link's directory.
func resolveTarget(path string) (string, error) {
	current := path
	for range 40 {
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return current, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			return current, nil
		}
		link, err := os.Readlink(current)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(link) {
			link = filepath.Join(filepath.Dir(current), link)
		}
		current = link
	}
	return "", fmt.Errorf("%s: too many levels of symbolic links", path)
}

// checkWritable refuses a target that exists and that we may not write. It
// opens the file write-only without truncating, so nothing changes. Root
// passes that open on any mode, so a file with no write bit at all (0400,
// 0444) is refused too: the owner marked it read-only on purpose. Whether
// the file exists or not, the directories the write needs are probed (see
// checkDirs), so a missing file under an unwritable directory is refused
// here too. As root, a symlink that resolves outside the agent's config dir
// and $HOME is refused (replaceFile only warns about it otherwise), so root
// is not steered into writing an arbitrary file.
func checkWritable(path string) error {
	target, err := resolveTarget(path)
	if err != nil {
		return err
	}
	if msg := linkEscape(path, target); msg != "" && runningAsRoot() {
		return fmt.Errorf("%s; refusing to write through it as root (run sdf as the user who owns it, or point the link inside $HOME); it was not changed", msg)
	}
	info, err := os.Stat(target)
	if os.IsNotExist(err) {
		return checkDirs(path, target)
	}
	if err != nil {
		return readErr(path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file; it was not changed", path)
	}
	if perm := info.Mode().Perm(); perm == 0 {
		return fmt.Errorf("%s has no permissions at all (mode 0000); fix its permissions (chmod u+rw) and rerun; it was not changed", path)
	} else if perm&0o222 == 0 {
		return fmt.Errorf("%s is read-only (mode %04o); it was not changed", path, perm)
	}
	f, err := os.OpenFile(target, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("%s is not writable (%v); it was not changed", path, trimPathErr(err))
	}
	if err := f.Close(); err != nil {
		return err
	}
	return checkDirs(path, target)
}

func trimPathErr(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// checkAllWritable runs checkWritable on every file one init writes, before
// it writes any, so a read-only second file, or a missing one under an
// unwritable directory, does not leave the first one changed.
//
// This is a precheck, not a lock: between it and the rename another process
// can change a mode, a directory, or a link (TOCTOU). The write still fails
// safely then (temp file and backup removed, target unchanged), but a file
// written earlier in the same init stays written.
func checkAllWritable(paths ...string) error {
	for _, path := range paths {
		if err := checkWritable(path); err != nil {
			return err
		}
	}
	return nil
}

// replaceFile backs up an existing path, then writes body atomically. A
// symlinked path is written through: the link stays and its target gets the
// new bytes; a link to a missing file creates that file. The target's
// permission bits are kept, and its owner and group when the process may set
// them; a new file gets fallback. A read-only target is refused before
// anything is written. If the write fails, the temp file and the backup made
// for it are removed and path is left as it was.
//
// The rename gives the target a new inode, so other hard links to the old
// file keep the old bytes, and extended attributes and ACLs are not copied.
func replaceFile(path string, body []byte, fallback os.FileMode) (string, error) {
	if err := checkWritable(path); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	target, err := resolveTarget(path)
	if err != nil {
		return "", err
	}
	warning := ""
	if msg := linkEscape(path, target); msg != "" {
		warning = "warning: " + msg + "; sdf wrote through it\n"
	}
	links := hardLinks(path)
	backup, err := copyAside(path)
	if err != nil {
		return "", err
	}
	if err := writeAtomic(path, body, fallback); err != nil {
		if backup != "" {
			_ = os.Remove(backup)
		}
		return "", err
	}
	text := wrote(path, backup) + warning
	if links > 1 {
		text += fmt.Sprintf("note: %s had %d hard links; the other links still hold the old contents\n", path, links)
	}
	return text, nil
}

// renameFile is os.Rename; tests replace it to make a write fail.
var renameFile = os.Rename

func writeAtomic(path string, body []byte, fallback os.FileMode) error {
	target, err := resolveTarget(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	mode := fileMode(target, fallback)
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".sdf-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	keepOwner(tmp, target)
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := renameFile(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// copyAside copies an existing path (through a symlink) to path.sdf-bak. An
// existing backup is never overwritten: the next free name is
// path.sdf-bak.<UTC timestamp>, then that name with -2, -3, and so on.
func copyAside(path string) (string, error) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	mode := fileMode(path, 0o600)
	stamp := path + ".sdf-bak." + time.Now().UTC().Format("20060102T150405Z")
	candidates := []string{path + ".sdf-bak", stamp}
	for n := 2; n < 100; n++ {
		candidates = append(candidates, fmt.Sprintf("%s-%d", stamp, n))
	}
	for _, dest := range candidates {
		f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := f.Write(body); err != nil {
			_ = f.Close()
			_ = os.Remove(dest)
			return "", err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			_ = os.Remove(dest)
			return "", err
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(dest)
			return "", err
		}
		return dest, nil
	}
	return "", fmt.Errorf("%s: no free .sdf-bak name; remove old backups", path)
}

func fileMode(path string, fallback os.FileMode) os.FileMode {
	info, err := os.Stat(path)
	if err != nil {
		return fallback
	}
	return info.Mode().Perm()
}
