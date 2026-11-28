//go:build unix

package collect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrBackfillRunning means another backfill-gaps process holds the output directory's lock.
var ErrBackfillRunning = errors.New("another backfill-gaps run is writing to this directory")

// lockBackfillDir takes an exclusive, non-blocking flock on <out>/.backfill.lock. Two runs at once would both
// see the same gap and append the same trades (2026-11-27: 217,942 duplicate BTC rows). The kernel drops the
// lock if the process dies, so a killed run never leaves a stale lock behind.
func lockBackfillDir(out string) (func(), error) {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(out, ".backfill.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBackfillRunning
		}
		return nil, fmt.Errorf("lock %s: %w", f.Name(), err)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
