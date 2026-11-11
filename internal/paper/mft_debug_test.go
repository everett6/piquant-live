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
	// Round trip fee: 2 * 1.5 = 3.0 bps. Breakeven stop = 50,000 * (1 + (3.0 + 3.0)*1e-4) = 50,030.0
	t1 := baseTime.Add(10 * time.Second)
	eng.evaluateQuotesAndGating("BTCUSDT", 50150.0, t1)

	eng.mu.RLock()
	pos = eng.state.Positions["BTCUSDT"]
	expectedBEStop := entryPrice * (1.0 + (6.0 * 1e-4))
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
	// Verify that targetAsk drops to the touch / midPrice to immediately exit and lock in profit!
	t3 := baseTime.Add(45 * time.Second)
	eng.evaluateQuotesAndGating("BTCUSDT", 50140.0, t3)

	eng.mu.RLock()
	orders := eng.activeOrders["BTCUSDT"]
	var sellOrder *ActiveMakerOrder
	for _, o := range orders {
		if o.Side == "SELL" && o.CancelingUntil.IsZero() {
			sellOrder = o
			break
		}
	}
	if sellOrder == nil {
		eng.mu.RUnlock()
		t.Fatalf("expected active SELL exit order after trailing stop triggered")
	}
	if sellOrder.Price > 50150.0 {
		eng.mu.RUnlock()
		t.Fatalf("expected aggressive exit order near 50,140, got %.2f", sellOrder.Price)
	}
	eng.mu.RUnlock()
}

func TestMFT_TrailingStopRatchetShort(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.Symbols = []string{"ETHUSDT"}
	cfg.MakerFeeBps = 1.5

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
	// Round trip fee: 2 * 1.5 = 3.0 bps. Breakeven stop for short = 3000 * (1 - (3.0 + 3.0)*1e-4) = 2998.20
	t1 := baseTime.Add(10 * time.Second)
	eng.evaluateQuotesAndGating("ETHUSDT", 2991.0, t1)

	eng.mu.RLock()
	pos = eng.state.Positions["ETHUSDT"]
	expectedBEStop := entryPrice * (1.0 - (6.0 * 1e-4))
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
	// Verify that targetBid rises to the touch / midPrice to immediately cover and lock in profit!
	t3 := baseTime.Add(45 * time.Second)
	eng.evaluateQuotesAndGating("ETHUSDT", 2992.0, t3)

	eng.mu.RLock()
	orders := eng.activeOrders["ETHUSDT"]
	var buyOrder *ActiveMakerOrder
	for _, o := range orders {
		if o.Side == "BUY" && o.CancelingUntil.IsZero() {
			buyOrder = o
			break
		}
	}
	if buyOrder == nil {
		eng.mu.RUnlock()
		t.Fatalf("expected active BUY cover order after trailing stop triggered")
	}
	if buyOrder.Price < 2990.0 {
		eng.mu.RUnlock()
		t.Fatalf("expected aggressive cover order near 2992.0, got %.2f", buyOrder.Price)
	}
	eng.mu.RUnlock()
}
