package model

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// ErrLocked is returned when another live process holds the lock.
var ErrLocked = errors.New("locked by another process")

// Lock is an exclusive, pid-stamped lock file. A lock whose pid is dead is
// treated as stale and taken over, so a crashed run never wedges a job.
type Lock struct{ path string }

// Acquire takes <dir>/<name>.lock.
func Acquire(dir, name string) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, name+".lock")
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
			f.Close()
			return &Lock{path: p}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil, ErrLocked
		}
		var pid int
		fmt.Sscanf(string(b), "%d", &pid)
		if pid > 0 && processAlive(pid) {
			return nil, fmt.Errorf("%w (pid %d)", ErrLocked, pid)
		}
		os.Remove(p) // stale
	}
	return nil, ErrLocked
}

// Release removes the lock file.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	return os.Remove(l.path)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil
}

var _ = strconv.Itoa
