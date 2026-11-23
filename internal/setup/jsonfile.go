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
	root := map[string]any{}
	var order *keyOrder
	indent := "  "
	if body, err := os.ReadFile(path); err == nil {
		if !json.Valid(body) {
			return nil, false, fmt.Errorf("%s is not JSON; it was not changed", path)
		}
		root, order, err = decodeSettings(body)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", path, err)
		}
		indent = detectIndent(body)
	} else if !os.IsNotExist(err) {
		return nil, false, err
	}
	changed, err := merge(root)
	if err != nil {
		return nil, false, err
	}
	encoded, err := encodeSettings(root, order, indent)
	if err != nil {
		return nil, false, err
	}
	return encoded, changed, nil
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

// replaceFile backs up an existing path, then writes body atomically. A
// symlinked path is written through: the link stays and its target gets the
// new bytes. The target's permission bits are kept; a new file gets fallback.
// If the write fails, the temp file and the backup made for it are removed and
// path is left as it was.
func replaceFile(path string, body []byte, fallback os.FileMode) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
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
	return wrote(path, backup), nil
}

// renameFile is os.Rename; tests replace it to make a write fail.
var renameFile = os.Rename

func writeAtomic(path string, body []byte, fallback os.FileMode) error {
	target := path
	if info, err := os.Lstat(path); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		target = resolved
	}
	mode := fileMode(target, fallback)
	dir := filepath.Dir(target)
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
