package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/MalteKiefer/ledgerline-cli/internal/config"
)

// acquireLock creates an exclusive <config>/<name>.lock holding the pid. A stale
// lock (pid no longer running) is reclaimed. release() removes it.
func acquireLock(name string) (func(), error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) && staleLock(path) {
			// Reclaim a stale lock, but only recurse after a *successful* remove:
			// if Remove keeps failing, recursing would spin forever.
			if rmErr := os.Remove(path); rmErr != nil {
				return nil, fmt.Errorf("removing stale lock %s: %w", path, rmErr)
			}
			return acquireLock(name)
		}
		return nil, fmt.Errorf("another instance holds %s (%s)", name, path)
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Close()
	return func() { _ = os.Remove(path) }, nil
}

// lockFilePath returns the path acquireLock(name) would use, for a best-effort
// existence check by callers that want to warn (not refuse) when a service
// instance already holds the lock.
func lockFilePath(name string) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".lock"), nil
}

// staleLock reports whether the pid in path is no longer alive.
func staleLock(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(string(trimNL(b)))
	if err != nil {
		return true
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	// signal 0 probes liveness on unix without actually signalling the process;
	// the brief's os.Signal(nil) draft does not compile/behave correctly here.
	return p.Signal(syscall.Signal(0)) != nil
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}
