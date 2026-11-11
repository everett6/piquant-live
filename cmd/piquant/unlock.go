package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"piquant-live/internal/paper"
)

// runUnlock is the human restart after a kill: it clears KILL_LOCK and un-halts the paper checkpoint.
// Stop the engine first (systemctl stop piquant), then unlock, then start it again.
func runUnlock(args []string) error {
	fs := flag.NewFlagSet("unlock", flag.ContinueOnError)
	dataDir := fs.String("data", "", "data directory (default $PIQUANT_DATA_DIR or ~/piquant-data)")
	state := fs.String("state", "", "checkpoint file (default <data>/paper/dryrun_state.json)")
	show := fs.Bool("status", false, "only show whether the engine is locked")
	if err := fs.Parse(args); err != nil {
		return err
	}
	sf := *state
	if sf == "" {
		d := *dataDir
		if d == "" {
			d = defaultDataDir()
		}
		sf = filepath.Join(d, "paper", "dryrun_state.json")
	}
	if *show {
		kl, err := paper.ReadKillLock(sf)
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("not locked:", paper.KillLockPath(sf), "absent")
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Printf("LOCKED since %s: %s (trigger %s, equity %.2f, peak %.2f)\n",
			kl.At.Format("2006-01-02 15:04:05Z"), kl.Reason, kl.Trigger, kl.Equity, kl.PeakEquity)
		return nil
	}
	res, err := paper.Unlock(sf)
	if err != nil {
		return err
	}
	if res.HadLock {
		fmt.Printf("removed kill lock from %s: %s\n", res.Lock.At.Format("2006-01-02 15:04:05Z"), res.Lock.Reason)
	} else {
		fmt.Println("no kill lock was present")
	}
	if len(res.Files) == 0 {
		fmt.Println("no checkpoint found at", sf)
		return nil
	}
	if res.WasHalted {
		fmt.Printf("cleared halt (%s)\n", res.HaltReason)
	}
	fmt.Printf("drawdown peak reset %.2f -> %.2f in %d file(s). Start the engine now.\n",
		res.OldPeak, res.Equity, len(res.Files))
	return nil
}
