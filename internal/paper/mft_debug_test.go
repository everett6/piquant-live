package paper

import (
	"math"
	"testing"
	"time"

	"piquant-live/internal/engine"
)

func TestMFT_DiagnosticsAndMomentum(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.Symbols = []string{"BTCUSDT"}
	cfg.Debug = true

	ring := engine.NewSPSCRing(1024)
	eng, err := NewDryRunEngine(cfg, ring, nil)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	baseTime := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	prices := []float64{90000.0, 90010.0, 90025.0, 90050.0, 90040.0}

	for i, p := range prices {
		now := baseTime.Add(time.Duration(i*500) * time.Millisecond)
		eng.evaluateQuotesAndGating("BTCUSDT", p, now)
	}

	diags := eng.MFTDiagnostics()
	snap, ok := diags["BTCUSDT"]
	if !ok {
		t.Fatalf("expected MFT diagnostic snapshot for BTCUSDT")
	}

	if snap.MidPrice != 90040.0 {
		t.Errorf("expected MidPrice 90040.0, got %f", snap.MidPrice)
	}
	if snap.KalmanPrice <= 0 || math.IsNaN(snap.KalmanPrice) {
		t.Errorf("expected positive valid KalmanPrice, got %f", snap.KalmanPrice)
	}
	if snap.DominantHorizon == "" {
		t.Errorf("expected non-empty dominant horizon")
	}
	if snap.QuotingMode == "" {
		t.Errorf("expected non-empty quoting mode")
	}

	st := eng.State()
	if st.MFTDiagnostics == nil || st.MFTDiagnostics["BTCUSDT"].Symbol != "BTCUSDT" {
		t.Errorf("expected State() to include MFTDiagnostics")
	}
}

func TestMFT_TrailingStopRatchetLong(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.Symbols = []string{"BTCUSDT"}
	cfg.MakerFeeBps = 1.5
	cfg.TargetProfitBps = 40.0 // breakeven lock arms at 0.7 x target = 28 bps, so +30 bps triggers Stage 1

	ring := engine.NewSPSCRing(1024)
	eng, err := NewDryRunEngine(cfg, ring, nil)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	baseTime := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	entryPrice := 50000.0
	qty := 0.05

	// 1. Simulate Long Entry Fill
	eng.mu.Lock()
	eng.executeMakerFillLocked("BTCUSDT", entryPrice, qty, "BUY", 2.0, "Test_Entry")
	pos := eng.state.Positions["BTCUSDT"]
	if pos.Size != qty || pos.AvgEntryPrice != entryPrice {
		eng.mu.Unlock()
		t.Fatalf("unexpected position after entry: %+v", pos)
	}
	if pos.PeakPrice != entryPrice {
		eng.mu.Unlock()
		t.Fatalf("expected initial PeakPrice %f, got %f", entryPrice, pos.PeakPrice)
	}
	eng.mu.Unlock()

	// 2. Market rallies by +30 bps (50,000 * 1.0030 = 50,150) -> triggers Stage 1 (>= 25 bps)
	// Stop exits are taker: maker 1.5 + taker 4.0 + 1.0 slippage = 6.5 bps; breakeven stop = 50,000 * (1 + (6.5 + 3.0)*1e-4) = 50,047.5
	t1 := baseTime.Add(10 * time.Second)
	eng.evaluateQuotesAndGating("BTCUSDT", 50150.0, t1)

	eng.mu.RLock()
	pos = eng.state.Positions["BTCUSDT"]
	expectedBEStop := entryPrice * (1.0 + (9.5 * 1e-4))
	if math.Abs(pos.TrailingStop-expectedBEStop) > 0.1 {
		eng.mu.RUnlock()
		t.Fatalf("expected Stage 1 trailing stop ~%.2f, got %.2f", expectedBEStop, pos.TrailingStop)
	}
	if pos.PeakPrice != 50150.0 {
		eng.mu.RUnlock()
		t.Fatalf("expected PeakPrice 50150.0, got %.2f", pos.PeakPrice)
	}
	eng.mu.RUnlock()

	// 3. Market extends swing rally to +60 bps (50,000 * 1.0060 = 50,300) -> triggers Stage 2 (>= 45 bps)
	// Profit lock = 50,000 + (50,300 - 50,000)*0.50 = 50,150.0
	t2 := baseTime.Add(30 * time.Second)
	eng.evaluateQuotesAndGating("BTCUSDT", 50300.0, t2)

	eng.mu.RLock()
	pos = eng.state.Positions["BTCUSDT"]
	expectedProfitLock := 50150.0
	if math.Abs(pos.TrailingStop-expectedProfitLock) > 0.1 {
		eng.mu.RUnlock()
		t.Fatalf("expected Stage 2 trailing stop ~%.2f, got %.2f", expectedProfitLock, pos.TrailingStop)
	}
	eng.mu.RUnlock()

	// 4. Market reverses down through trailing stop (price drops to 50,140 <= 50,150)
	// Trailing-stop exits now cross the spread as a taker order: the position must be closed
	// immediately (no resting maker exit), booked as a winning round trip.
	t3 := baseTime.Add(45 * time.Second)
	eng.evaluateQuotesAndGating("BTCUSDT", 50140.0, t3)
	assertTakerTrailingExit(t, eng, "BTCUSDT", "SELL")
}

func TestMFT_TrailingStopRatchetShort(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.Symbols = []string{"ETHUSDT"}
	cfg.MakerFeeBps = 1.5
	cfg.TargetProfitBps = 40.0 // breakeven lock arms at 0.7 x target = 28 bps, so +30 bps triggers Stage 1

	ring := engine.NewSPSCRing(1024)
	eng, err := NewDryRunEngine(cfg, ring, nil)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	baseTime := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	entryPrice := 3000.0
	qty := 1.0

	// 1. Simulate Short Entry Fill
	eng.mu.Lock()
	eng.executeMakerFillLocked("ETHUSDT", entryPrice, qty, "SELL", 2.0, "Test_Short_Entry")
	pos := eng.state.Positions["ETHUSDT"]
	if pos.Size != -qty || pos.AvgEntryPrice != entryPrice {
		eng.mu.Unlock()
		t.Fatalf("unexpected position after short entry: %+v", pos)
	}
	if pos.PeakPrice != entryPrice {
		eng.mu.Unlock()
		t.Fatalf("expected initial PeakPrice %f, got %f", entryPrice, pos.PeakPrice)
	}
	eng.mu.Unlock()

	// 2. Market drops by +30 bps (3000 * (1 - 0.0030) = 2991.0) -> triggers Stage 1 (>= 25 bps)
	// Stop exits are taker: 6.5 bps cost; breakeven stop for short = 3000 * (1 - (6.5 + 3.0)*1e-4) = 2997.15
	t1 := baseTime.Add(10 * time.Second)
	eng.evaluateQuotesAndGating("ETHUSDT", 2991.0, t1)

	eng.mu.RLock()
	pos = eng.state.Positions["ETHUSDT"]
	expectedBEStop := entryPrice * (1.0 - (9.5 * 1e-4))
	if math.Abs(pos.TrailingStop-expectedBEStop) > 0.05 {
		eng.mu.RUnlock()
		t.Fatalf("expected Stage 1 trailing stop ~%.2f, got %.2f", expectedBEStop, pos.TrailingStop)
	}
	eng.mu.RUnlock()

	// 3. Market extends dump to +60 bps (3000 * (1 - 0.0060) = 2982.0) -> triggers Stage 2 (>= 45 bps)
	// Profit lock = 3000 - (3000 - 2982)*0.50 = 2991.0
	t2 := baseTime.Add(30 * time.Second)
	eng.evaluateQuotesAndGating("ETHUSDT", 2982.0, t2)

	eng.mu.RLock()
	pos = eng.state.Positions["ETHUSDT"]
	expectedProfitLock := 2991.0
	if math.Abs(pos.TrailingStop-expectedProfitLock) > 0.05 {
		eng.mu.RUnlock()
		t.Fatalf("expected Stage 2 trailing stop ~%.2f, got %.2f", expectedProfitLock, pos.TrailingStop)
	}
	eng.mu.RUnlock()

	// 4. Market bounces through trailing stop (price rises to 2992.0 >= 2991.0)
	// Trailing-stop covers now cross the spread as a taker order.
	t3 := baseTime.Add(45 * time.Second)
	eng.evaluateQuotesAndGating("ETHUSDT", 2992.0, t3)
	assertTakerTrailingExit(t, eng, "ETHUSDT", "BUY")
}

// assertTakerTrailingExit checks a trailing stop closed the position with a taker fill,
// cancelled resting quotes, and counted a winning round trip.
func assertTakerTrailingExit(t *testing.T, eng *DryRunEngine, symbol, side string) {
	t.Helper()
	eng.mu.RLock()
	defer eng.mu.RUnlock()
	if pos := eng.state.Positions[symbol]; pos == nil || pos.Size != 0 || pos.TrailingStop != 0 || pos.PeakPrice != 0 {
		t.Fatalf("expected flat position with cleared stop state after trailing exit, got %+v", pos)
	}
	if len(eng.recentFills) == 0 {
		t.Fatal("expected a taker exit fill")
	}
	last := eng.recentFills[len(eng.recentFills)-1]
	if last.Role != "TAKER" || last.Strategy != "EXIT_TRAILING_STOP" || last.Side != side {
		t.Fatalf("unexpected exit fill: %+v", last)
	}
	if last.PnLRealized <= 0 || eng.state.WinningTrades != 1 || eng.state.LosingTrades != 0 {
		t.Fatalf("expected one winning round trip, got pnl=%.4f wins=%d losses=%d",
			last.PnLRealized, eng.state.WinningTrades, eng.state.LosingTrades)
	}
	if len(eng.activeOrders[symbol]) != 0 {
		t.Fatalf("expected resting quotes cancelled, got %d", len(eng.activeOrders[symbol]))
	}
}
