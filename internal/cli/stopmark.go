package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"time"
)

// stopAlreadyReminded reports whether id is already in the marker file.
// A missing file is not reminded. An unreadable file fails open (true, err)
// so the caller does not block.
func stopAlreadyReminded(path, id string) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, stopMarkerMax))
	if err != nil {
		return true, err
	}
	return stopMarkerHas(b, id), nil
}

// markStopReminded records id in path, creating the file at mode 0600.
// fresh is true when this call appended the id. A lock that cannot be taken
// within stopRemindLockWait returns an error so the caller lets the agent stop.
func markStopReminded(path, id string) (fresh bool, err error) {
	lock, err := stopLock(path + ".lock")
	if err != nil {
		return false, err
	}
	defer func() {
		_ = lock.Close()
		_ = os.Remove(lock.Name())
	}()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return false, err
	}
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	// The marker is bounded. Past stopMarkerMax it starts over, so an old
	// session could be reminded once more, which is cheaper than an
	// unbounded file or a partial read.
	if info.Size()+int64(len(id))+1 > stopMarkerMax {
		if err := f.Truncate(0); err != nil {
			return false, err
		}
	} else {
		b, err := io.ReadAll(f)
		if err != nil {
			return false, err
		}
		if stopMarkerHas(b, id) {
			return false, nil
		}
	}
	// O_APPEND: the id always lands at the end, after a whole line.
	if _, err := f.Write([]byte(id + "\n")); err != nil {
		return false, err
	}
	return true, nil
}

// stopMarkerMax caps the marker file (about 28k UUID session ids).
const stopMarkerMax = 1 << 20

func stopMarkerHas(b []byte, id string) bool {
	for _, line := range bytes.Split(b, []byte("\n")) {
		if string(line) == id {
			return true
		}
	}
	return false
}

func stopLock(path string) (*os.File, error) {
	deadline := time.Now().Add(stopRemindLockWait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, statErr := os.Stat(path)
		if statErr == nil && time.Since(info.ModTime()) > 2*time.Second {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}
