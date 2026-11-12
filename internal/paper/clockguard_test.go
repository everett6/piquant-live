package paper

import (
	"testing"
	"time"
)

type fakeClock struct{ ok bool }

func (f *fakeClock) Status() (bool, string) {
	if f.ok {
		return true, ""
	}
	return false, "clock off by 400ms (limit 250ms)"
}

func TestClockGuardBlocksEntries(t *testing.T) {
	e := &DryRunEngine{}
	now := time.Now()
	if !e.trendEntryAllowed("BUY", now) {
		t.Fatal("no guard should allow")
	}
	fc := &fakeClock{ok: false}
	e.SetClockGuard(fc)
	if e.trendEntryAllowed("BUY", now) || e.trendEntryAllowed("SELL", now) {
		t.Fatal("bad clock must block entries")
	}
	fc.ok = true
	if !e.trendEntryAllowed("BUY", now) {
		t.Fatal("verified clock should allow")
	}
}
