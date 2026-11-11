package paper

import (
	"math"
	"testing"

	"piquant-live/internal/engine"
)

func TestExpectancyMatchesBreakevenFormula(t *testing.T) {
	var x ExpectancyState
	// 6 wins of +35 bps, 4 losses of -39 bps on $1000 notional: mean = (210 - 156) / 10 = 5.4 bps
	for i := 0; i < 6; i++ {
		x.Record("MAKER_EXIT", 3.5, 1000)
	}
	for i := 0; i < 4; i++ {
		x.Record("STOP_LOSS", -3.9, 1000)
	}
	if math.Abs(x.All.MeanBps-5.4) > 1e-9 || x.All.N != 10 || x.WinRatePct != 60 {
		t.Fatalf("all = %+v win=%v", x.All, x.WinRatePct)
	}
	if want := 39.0 / 74.0 * 100; math.Abs(x.BreakevenWinRatePct-want) > 1e-9 {
		t.Fatalf("breakeven = %v want %v", x.BreakevenWinRatePct, want)
	}
	if math.Abs(x.EdgeVsBreakevenPct-(60-39.0/74.0*100)) > 1e-9 {
		t.Fatalf("edge = %v", x.EdgeVsBreakevenPct)
	}
	if x.ByReason["STOP_LOSS"].N != 4 || math.Abs(x.ByReason["STOP_LOSS"].MeanBps+39) > 1e-9 {
		t.Fatalf("by reason = %+v", x.ByReason["STOP_LOSS"])
	}
	if x.NegativeEdgeConfirmed(10) {
		t.Fatal("positive mean must not trip the breaker")
	}
}

func TestExpectancyBreakerNeedsConfidenceAndCount(t *testing.T) {
	var x ExpectancyState
	for i := 0; i < 40; i++ { // 30% win rate at +35 / -39: clearly negative
		if i%10 < 3 {
			x.Record("MAKER_EXIT", 3.5, 1000)
		} else {
			x.Record("STOP_LOSS", -3.9, 1000)
		}
	}
	if x.NegativeEdgeConfirmed(0) {
		t.Fatal("minTrades 0 disables")
	}
	if x.NegativeEdgeConfirmed(50) {
		t.Fatal("must wait for minTrades")
	}
	if !x.NegativeEdgeConfirmed(40) || x.Upper95Bps >= 0 {
		t.Fatalf("expected confirmed negative edge, upper95=%v", x.Upper95Bps)
	}
	x.Record("x", math.NaN(), 1000)
	x.Record("x", 1, 0)
	if x.All.N != 40 {
		t.Fatal("invalid inputs must be ignored")
	}
}

func TestTakerExitRecordsExpectancyByReason(t *testing.T) {
	cfg := DefaultDryRunConfig(t.TempDir())
	cfg.Symbols = []string{"BTCUSDT"}
	e, err := NewDryRunEngine(cfg, engine.NewSPSCRing(16), nil)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.state.Positions["BTCUSDT"] = &Position{Symbol: "BTCUSDT", Size: 0.01, AvgEntryPrice: 100000, MarkPrice: 99600}
	e.takerExitLocked("BTCUSDT", 99600, 99601, 99600.5, "STOP_LOSS")
	st := e.state.Expectancy.ByReason["STOP_LOSS"]
	e.mu.Unlock()
	if st == nil || st.N != 1 || st.MeanBps > -40 {
		t.Fatalf("stop-loss expectancy = %+v (want one trade near -40 bps or worse)", st)
	}
}
