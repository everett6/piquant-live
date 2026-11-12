package paper

import (
	"errors"
	"os"
	"testing"
)

func newLockEngine(t *testing.T, dir string) *DryRunEngine {
	t.Helper()
	cfg := DefaultDryRunConfig(dir)
	cfg.InitialCapital = 1000
	eng, err := NewDryRunEngine(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Stop)
	return eng
}

func TestKillLockSurvivesRestartUntilUnlock(t *testing.T) {
	dir := t.TempDir()
	eng := newLockEngine(t, dir)
	sf := eng.cfg.StateFile

	// A shutdown flatten is not a kill: no lock.
	eng.mu.Lock()
	eng.emergencyFlattenLocked("Shutdown_Auto_TS_Flatten")
	eng.mu.Unlock()
	if _, err := ReadKillLock(sf); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shutdown flatten wrote a lock: %v", err)
	}

	eng.mu.Lock()
	eng.state.Equity, eng.state.PeakEquity = 500, 1000
	eng.state.CircuitHalted = true
	eng.state.CircuitHaltReason = "test drawdown"
	eng.emergencyFlattenLocked("Circuit_Breaker_Drawdown_Breach")
	eng.mu.Unlock()
	kl, err := ReadKillLock(sf)
	if err != nil || kl.Reason != "test drawdown" || kl.PeakEquity != 1000 {
		t.Fatalf("lock = %+v, %v", kl, err)
	}
	if err := eng.saveCheckpoint(); err != nil {
		t.Fatal(err)
	}

	// Even a checkpoint that lost the halt starts halted while the lock exists.
	if _, err := Unlock(sf); err != nil { // clears halt in the file...
		t.Fatal(err)
	}
	if err := writeKillLock(sf, kl); err != nil { // ...but the lock is put back
		t.Fatal(err)
	}
	if st := newLockEngine(t, dir).State(); !st.CircuitHalted || st.CircuitHaltReason != "test drawdown" {
		t.Fatalf("restart with lock: halted=%v reason=%q", st.CircuitHalted, st.CircuitHaltReason)
	}

	res, err := Unlock(sf)
	if err != nil || !res.HadLock || len(res.Files) == 0 {
		t.Fatalf("unlock = %+v, %v", res, err)
	}
	if _, err := ReadKillLock(sf); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("lock still present after unlock")
	}
	st := newLockEngine(t, dir).State()
	if st.CircuitHalted || st.PeakEquity != st.Equity {
		t.Fatalf("after unlock: halted=%v peak=%v equity=%v", st.CircuitHalted, st.PeakEquity, st.Equity)
	}
}

func TestHaltedCheckpointWithoutLockGetsLocked(t *testing.T) {
	dir := t.TempDir()
	eng := newLockEngine(t, dir)
	eng.mu.Lock()
	eng.state.CircuitHalted = true
	eng.state.CircuitHaltReason = "old halt"
	eng.mu.Unlock()
	if err := eng.saveCheckpoint(); err != nil {
		t.Fatal(err)
	}
	newLockEngine(t, dir)
	if kl, err := ReadKillLock(eng.cfg.StateFile); err != nil || kl.Reason != "old halt" {
		t.Fatalf("lock = %+v, %v", kl, err)
	}
}
