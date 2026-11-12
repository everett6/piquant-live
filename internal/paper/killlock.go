package paper

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// KillLockName is the file written next to the checkpoint when a circuit breaker trips. While it exists the
// engine starts halted, whatever the checkpoint says. Only `piquant unlock` (a human) removes it.
const KillLockName = "KILL_LOCK"

// KillHook, if set, is called (on its own goroutine) each time a new kill lock is written, e.g. to email the owner.
var KillHook func(KillLock)

// KillLock records why and when the engine was killed.
type KillLock struct {
	Reason     string    `json:"reason"`
	Trigger    string    `json:"trigger"`
	At         time.Time `json:"at"`
	Equity     float64   `json:"equity"`
	PeakEquity float64   `json:"peak_equity"`
}

// KillLockPath returns the lock path for a checkpoint file.
func KillLockPath(stateFile string) string {
	return filepath.Join(filepath.Dir(stateFile), KillLockName)
}

// ReadKillLock returns the lock, or os.ErrNotExist when the engine is not locked.
func ReadKillLock(stateFile string) (KillLock, error) {
	var kl KillLock
	b, err := os.ReadFile(KillLockPath(stateFile))
	if err != nil {
		return kl, err
	}
	if err := json.Unmarshal(b, &kl); err != nil {
		// A damaged lock still locks.
		kl.Reason = "unreadable kill lock: " + err.Error()
	}
	return kl, nil
}

func writeKillLock(stateFile string, kl KillLock) error {
	b, err := json.MarshalIndent(kl, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(KillLockPath(stateFile), append(b, '\n'))
}

// writeKillLockLocked persists the lock for the current halt. Caller MUST hold e.mu.
func (e *DryRunEngine) writeKillLockLocked(trigger string) {
	if e.cfg.StateFile == "" {
		return
	}
	if _, err := os.Stat(KillLockPath(e.cfg.StateFile)); err == nil {
		return // keep the first reason
	}
	kl := KillLock{
		Reason:     e.state.CircuitHaltReason,
		Trigger:    trigger,
		At:         time.Now().UTC(),
		Equity:     e.state.Equity,
		PeakEquity: e.state.PeakEquity,
	}
	if err := writeKillLock(e.cfg.StateFile, kl); err != nil {
		fmt.Fprintf(os.Stderr, "[KILL] could not write kill lock: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "[KILL] engine halted and locked (%s). Restart needs: piquant unlock\n", kl.Reason)
	if KillHook != nil {
		go KillHook(kl)
	}
}

// enforceKillLock runs at startup: a lock forces the halt; a halted checkpoint without a lock gets one.
func (e *DryRunEngine) enforceKillLock() {
	if e.cfg.StateFile == "" {
		return
	}
	kl, err := ReadKillLock(e.cfg.StateFile)
	switch {
	case err == nil:
		e.state.CircuitHalted = true
		if e.state.CircuitHaltReason == "" {
			e.state.CircuitHaltReason = kl.Reason
		}
		fmt.Fprintf(os.Stderr, "[KILL] kill lock present since %s (%s); staying halted. Run 'piquant unlock' "+
			"with the engine stopped to resume.\n", kl.At.Format(time.RFC3339), kl.Reason)
	case errors.Is(err, os.ErrNotExist) && e.state.CircuitHalted:
		e.writeKillLockLocked("startup_halted_checkpoint")
	}
}

// UnlockResult reports what `piquant unlock` changed.
type UnlockResult struct {
	Lock       KillLock
	HadLock    bool
	Files      []string // checkpoint files rewritten
	Equity     float64
	OldPeak    float64
	WasHalted  bool
	HaltReason string
}

// Unlock clears a kill: removes the lock and un-halts the checkpoint (and its .bak), resetting the drawdown peak
// to current equity so the breaker does not re-trip on the old drop. Run it only while the engine is stopped:
// a running engine re-saves its halted state, and on the next start that halt is locked again.
func Unlock(stateFile string) (UnlockResult, error) {
	var res UnlockResult
	kl, err := ReadKillLock(stateFile)
	if err == nil {
		res.Lock, res.HadLock = kl, true
	} else if !errors.Is(err, os.ErrNotExist) {
		return res, err
	}
	for _, p := range []string{stateFile, stateFile + ".bak"} {
		b, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return res, err
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			return res, fmt.Errorf("%s: %w", p, err)
		}
		var halted bool
		var reason string
		var eq, peak float64
		_ = json.Unmarshal(m["circuit_halted"], &halted)
		_ = json.Unmarshal(m["circuit_halt_reason"], &reason)
		_ = json.Unmarshal(m["equity"], &eq)
		_ = json.Unmarshal(m["peak_equity"], &peak)
		if p == stateFile {
			res.WasHalted, res.HaltReason, res.Equity, res.OldPeak = halted, reason, eq, peak
		}
		m["circuit_halted"] = json.RawMessage("false")
		m["circuit_halt_reason"] = json.RawMessage(`""`)
		if eq > 0 {
			m["peak_equity"], _ = json.Marshal(eq)
		}
		out, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return res, err
		}
		if err := writeFileAtomic(p, out); err != nil {
			return res, err
		}
		res.Files = append(res.Files, p)
	}
	if res.HadLock {
		if err := os.Remove(KillLockPath(stateFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return res, err
		}
	}
	return res, nil
}

func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
