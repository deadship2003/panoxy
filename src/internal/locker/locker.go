// Package locker provides flock-based mutual exclusion: write commands (deploy/install/
// sub import/sub del/upgrade/rollback/uninstall/mode) take the lock, read commands
// (status/log/check/sub list) do not.
package locker

import (
	"fmt"
	"os"
	"sync"
	"syscall"

	"github.com/deadship2003/panoxy/internal/constants"
)

type Locker struct {
	f  *os.File
	ok bool
	re bool // in-process re-entry (deploy -> install reuses the already-held lock)
}

var (
	mu     sync.Mutex
	locked bool
)

// Lock acquires the mutex; returns an error when already held (no waiting). In-process
// re-entry is allowed (install nested inside deploy).
func Lock(path string) (*Locker, error) {
	mu.Lock()
	if locked {
		mu.Unlock()
		return &Locker{re: true}, nil
	}
	mu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		// Lock file not writable (e.g. a read-only environment): degrade to no lock,
		// matching the bash-version behavior.
		fmt.Fprintf(os.Stderr, "[%s] WARN lock file unavailable (%v), continuing without a lock\n", constants.ProgName, err)
		return &Locker{}, nil
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another %s instance is running, please try again later", constants.ProgName)
	}
	mu.Lock()
	locked = true
	mu.Unlock()
	return &Locker{f: f, ok: true}, nil
}

func (l *Locker) Unlock() {
	if l == nil || l.re {
		return // a re-entrant holder does not unlock; the outermost holder releases
	}
	if !l.ok {
		return
	}
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
	l.ok = false
	mu.Lock()
	locked = false
	mu.Unlock()
}
