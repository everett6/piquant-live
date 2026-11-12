package paper

import (
	"testing"
	"time"

	"piquant-live/internal/engine"
)

func TestTickFilterRejectsPrintButFollowsRealMoves(t *testing.T) {
	var f tickFilter
	t0 := time.Unix(1_800_000_000, 0)
	if !f.accept(1175, t0) || !f.accept(1180, t0.Add(time.Second)) {
		t.Fatal("normal ticks rejected")
	}
	if f.accept(0.06, t0.Add(2*time.Second)) {
		t.Fatal("accepted a $0.06 print")
	}
	if !f.accept(1178, t0.Add(3*time.Second)) || f.last != 1178 {
		t.Fatal("did not resume after the print")
	}
	// A real 30% gap confirmed by three agreeing ticks is accepted on the third.
	got := []bool{f.accept(820, t0.Add(4*time.Second)), f.accept(818, t0.Add(5*time.Second)), f.accept(822, t0.Add(6*time.Second))}
	if got[0] || got[1] || !got[2] {
		t.Fatalf("confirmation sequence %v", got)
	}
	// After a long feed gap any price is taken as the new level.
	if !f.accept(400, t0.Add(10*time.Minute)) {
		t.Fatal("stale filter blocked a fresh price")
	}
}

func TestEngineIgnoresBadPrintForMarksAndKill(t *testing.T) {
	cfg := DefaultDryRunConfig(t.TempDir())
	cfg.Symbols = []string{"BTCUSDT"}
	cfg.MaxDrawdownPct = 40
	eng, err := NewDryRunEngine(cfg, engine.NewSPSCRing(1024), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Stop)
	eng.processMarketEvent(&engine.MarketEvent{SymbolID: 0, Price: 70000, Size: 0.01, Side: 1})
	eng.mu.Lock()
	pos := eng.state.Positions["BTCUSDT"]
	pos.Size, pos.AvgEntryPrice = 0.01, 70000
	eng.mu.Unlock()
	eng.processMarketEvent(&engine.MarketEvent{SymbolID: 0, Price: 0.06, Size: 0.01, Side: 2})
	st := eng.State()
	if st.CircuitHalted || st.Positions["BTCUSDT"].MarkPrice != 70000 || st.RejectedTicks != 1 {
		t.Fatalf("halted=%v mark=%v rejected=%d", st.CircuitHalted, st.Positions["BTCUSDT"].MarkPrice, st.RejectedTicks)
	}
}
