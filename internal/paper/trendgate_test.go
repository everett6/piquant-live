package paper

import (
	"testing"
	"time"

	"piquant-live/internal/engine"
)

func TestTrendGateStates(t *testing.T) {
	now := time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)
	g := NewTrendGate(48 * time.Hour)
	if g.AllowLongEntry(now) || g.Snapshot(now).Regime != "UNKNOWN" {
		t.Fatal("unknown regime must block long entries")
	}
	g.Set(true, 75000, 70000, now)
	if !g.AllowLongEntry(now) || g.AllowShortEntry(now) || g.Snapshot(now).Regime != "UP" {
		t.Fatal("UP must allow longs only")
	}
	if g.AllowLongEntry(now.Add(49*time.Hour)) || g.Snapshot(now.Add(49*time.Hour)).Regime != "UNKNOWN" {
		t.Fatal("stale regime must block")
	}
	g.Set(false, 65000, 70000, now)
	if g.AllowLongEntry(now) || g.Snapshot(now).Regime != "DOWN" {
		t.Fatal("DOWN must block longs")
	}
}

func TestEngineTrendEntryAllowed(t *testing.T) {
	cfg := DefaultDryRunConfig(t.TempDir())
	cfg.Symbols = []string{"BTCUSDT"}
	e, err := NewDryRunEngine(cfg, engine.NewSPSCRing(16), nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if !e.trendEntryAllowed("BUY", now) || !e.trendEntryAllowed("SELL", now) {
		t.Fatal("no gate: both sides allowed")
	}
	g := NewTrendGate(48 * time.Hour)
	e.SetTrendGate(g)
	if e.trendEntryAllowed("BUY", now) || e.trendEntryAllowed("SELL", now) {
		t.Fatal("unknown regime: no entries")
	}
	g.Set(true, 2, 1, now)
	if !e.trendEntryAllowed("BUY", now) || e.trendEntryAllowed("SELL", now) {
		t.Fatal("UP: longs only")
	}
	g.Set(false, 1, 2, now)
	if e.trendEntryAllowed("BUY", now) || e.trendEntryAllowed("SELL", now) {
		t.Fatal("DOWN: no entries")
	}
}
