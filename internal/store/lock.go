package store

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ErrLocked is returned when another sdf process holds a database's lock
// file in a conflicting mode.
var ErrLocked = errors.New("locked by another sdf process")

// FileLock is an advisory lock on <db>.lock. sdf serve holds it shared for
// as long as it runs (several serves on one file are fine); sdf redact
// holds it exclusive while it rewrites the file, so neither starts while
// the other runs. The OS drops it when the process exits, however it exits.
type FileLock struct {
	f *os.File
}

// LockPath is the lock file next to a database.
func LockPath(dbPath string) string { return dbPath + ".lock" }

// LockForServe takes the shared lock (sdf serve).
func LockForServe(dbPath string) (*FileLock, error) { return takeLock(dbPath, false) }

// LockForScrub takes the exclusive lock (sdf redact).
func LockForScrub(dbPath string) (*FileLock, error) { return takeLock(dbPath, true) }

func takeLock(dbPath string, exclusive bool) (*FileLock, error) {
	f, err := os.OpenFile(LockPath(dbPath), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f, exclusive); err != nil {
		_ = f.Close()
		return nil, err
	}
	if exclusive {
		// The pid is only for messages; the lock is what counts.
		_ = f.Truncate(0)
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return &FileLock{f: f}, nil
}

// Release drops the lock.
func (l *FileLock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// LockHolder describes who holds a lock, for an error message: the pid
// sdf redact wrote, when there is one.
func LockHolder(dbPath string) string {
	raw, err := os.ReadFile(LockPath(dbPath))
	if err != nil {
		return ""
	}
	if pid := strings.TrimSpace(string(raw)); pid != "" {
		return fmt.Sprintf(" (pid %s)", pid)
	}
	return ""
}
