//go:build !unix

package collect

import "errors"

// ErrBackfillRunning means another backfill-gaps process holds the output directory's lock.
var ErrBackfillRunning = errors.New("another backfill-gaps run is writing to this directory")

// lockBackfillDir is a no-op where flock is unavailable.
func lockBackfillDir(string) (func(), error) { return func() {}, nil }
