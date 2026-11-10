package paper

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestJournalRecoveryFloatDustDivisionByZero verifies that floating point residual dust
// (e.g. 1.38e-17 BTC) after position close does NOT trigger divide-by-zero or explode
// AvgEntryPrice to astronomical values upon journal recovery.
func TestJournalRecoveryFloatDustDivisionByZero(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "piquant_dust_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := DefaultDryRunConfig(tmpDir)
	journalPath := cfg.JournalFile
	_ = os.MkdirAll(filepath.Dir(journalPath), 0o755)

	// Create journal with an open BUY (0.0018 BTC) and a closing SELL (0.0018 BTC)
	// In IEEE 754 float64, slight arithmetic variance can leave 1.38e-17
	f, err := os.OpenFile(journalPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("failed to open journal file: %v", err)
	}

	r1 := TradeRecord{
		Timestamp:   time.Now().Add(-2 * time.Minute),
		Symbol:      "BTCUSDC",
		Side:        "BUY",
		Role:        "MAKER",
		Price:       70000.0,
		Size:        0.0018,
		ValueUSDT:   126.0,
		FeeUSDT:     0.0189,
		PnLRealized: -0.0189,
		Strategy:    "GLFT_Maker",
		DataSource:  "live",
	}
	b1, _ := json.Marshal(r1)
	_, _ = f.Write(append(b1, '\n'))

	r2 := TradeRecord{
		Timestamp:   time.Now().Add(-1 * time.Minute),
		Symbol:      "BTCUSDC",
		Side:        "SELL",
		Role:        "MAKER",
		Price:       70100.0,
		Size:        0.0018, // Matches size, closing to 0.0 with float dust
		ValueUSDT:   126.18,
		FeeUSDT:     0.0189,
		PnLRealized: 0.1611,
		Strategy:    "GLFT_Maker",
		DataSource:  "live",
	}
	b2, _ := json.Marshal(r2)
	_, _ = f.Write(append(b2, '\n'))
	_ = f.Close()

	eng, err := NewDryRunEngine(cfg, nil, nil)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	st := eng.State()
	pos := st.Positions["BTCUSDC"]
	if pos == nil {
		t.Fatalf("expected BTCUSDC position to exist")
	}

	if pos.Size != 0.0 {
		t.Fatalf("expected position size to be exactly 0.0, got %e", pos.Size)
	}
	if pos.AvgEntryPrice != 0.0 {
		t.Fatalf("expected AvgEntryPrice to be reset to 0.0, got %f (division by zero / dust explosion!)", pos.AvgEntryPrice)
	}
}

// TestEmergencyFlattenTakerFeeAccounting verifies that emergency flattening charges taker fees
// and records net realized PnL correctly in cash, positions, and journal.
func TestEmergencyFlattenTakerFeeAccounting(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "piquant_flatten_fee_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := DefaultDryRunConfig(tmpDir)
	cfg.CleanReset = true
	cfg.InitialCapital = 1000.0
	cfg.TakerFeeBps = 4.0 // 4 bps

	eng, err := NewDryRunEngine(cfg, nil, nil)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	// Manually set an open long position of 0.01 BTC at $70,000, mark price at $71,000
	eng.mu.Lock()
	pos := eng.state.Positions["BTCUSDC"]
	if pos == nil {
		pos = &Position{Symbol: "BTCUSDC"}
		eng.state.Positions["BTCUSDC"] = pos
	}
	pos.Size = 0.01
	pos.AvgEntryPrice = 70000.0
	pos.MarkPrice = 71000.0

	// Gross PnL = 0.01 * (71000 - 70000) = +10.0 USDT
	// Notional = 0.01 * 71000 = 710.0 USDT
	// Taker Fee = 710.0 * 0.0004 = 0.284 USDT
	// Net PnL = 10.0 - 0.284 = +9.716 USDT
	eng.emergencyFlattenLocked("TEST_EMERGENCY_HALT")
	eng.mu.Unlock()

	st := eng.State()
	expectedCash := 1000.0 + (10.0 - 0.284)
	if math.Abs(st.Cash-expectedCash) > 1e-4 {
		t.Fatalf("expected Cash to be %f after taker fee, got %f", expectedCash, st.Cash)
	}
	if pos.Size != 0.0 {
		t.Fatalf("expected position to be flattened to 0.0, got %f", pos.Size)
	}

	fills := eng.RecentFills()
	if len(fills) == 0 {
		t.Fatalf("expected emergency liquidation fill in blotter")
	}
	liqFill := fills[0]
	if liqFill.FeeUSDT <= 0 {
		t.Fatalf("expected non-zero FeeUSDT on emergency liquidation, got %f", liqFill.FeeUSDT)
	}
	if math.Abs(liqFill.FeeUSDT-0.284) > 1e-4 {
		t.Fatalf("expected FeeUSDT 0.284, got %f", liqFill.FeeUSDT)
	}
}

// TestQuantizePriceArbitraryTickSizes verifies that non-power-of-ten tick sizes
// (e.g. 0.05, 0.02, 0.005) do not suffer decimal precision truncation.
func TestQuantizePriceArbitraryTickSizes(t *testing.T) {
	cases := []struct {
		price    float64
		tick     float64
		roundUp  bool
		expected float64
	}{
		{price: 74000.03, tick: 0.05, roundUp: true, expected: 74000.05},
		{price: 74000.03, tick: 0.05, roundUp: false, expected: 74000.00},
		{price: 74000.07, tick: 0.05, roundUp: false, expected: 74000.05},
		{price: 74000.015, tick: 0.02, roundUp: true, expected: 74000.02},
		{price: 74000.015, tick: 0.02, roundUp: false, expected: 74000.00},
		{price: 3450.1234, tick: 0.01, roundUp: true, expected: 3450.13},
		{price: 3450.1234, tick: 0.01, roundUp: false, expected: 3450.12},
		{price: 0.12345, tick: 0.0001, roundUp: true, expected: 0.1235},
		{price: 74000.4, tick: 0.10, roundUp: true, expected: 74000.40},
		{price: 74000.41, tick: 0.10, roundUp: false, expected: 74000.40},
	}

	for _, c := range cases {
		got := quantizePrice(c.price, c.tick, c.roundUp)
		if math.Abs(got-c.expected) > 1e-6 {
			t.Errorf("quantizePrice(%.5f, %.4f, %v): expected %.5f, got %.5f", c.price, c.tick, c.roundUp, c.expected, got)
		}
	}
}

// TestTCAMarkoutConcurrentRace tests high-concurrency access to TCAMarkoutTracker
// with simultaneous RecordFill, OnTick, and Snapshot across 16 goroutines.
func TestTCAMarkoutConcurrentRace(t *testing.T) {
	tracker := NewTCAMarkoutTracker()
	var wg sync.WaitGroup

	nowNs := time.Now().UnixNano()

	// 8 writers adding fills
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				tracker.RecordFill(uint64(workerID*100+j), "BTCUSDT", 1, 50000.0+float64(j), 50000.0, nowNs)
				time.Sleep(100 * time.Microsecond)
			}
		}(i)
	}

	// 4 workers simulating incoming market ticks
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				tracker.OnTick("BTCUSDT", 50005.0+float64(j), nowNs+int64(j)*1_000_000_000)
				time.Sleep(150 * time.Microsecond)
			}
		}()
	}

	// 4 workers reading snapshot concurrently (simulating dashboard HTTP polling)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				snap := tracker.Snapshot()
				_ = snap.TotalFillsAnalyzed
				time.Sleep(120 * time.Microsecond)
			}
		}()
	}

	wg.Wait()
}

// TestMonthlyGainCapPreservationAndNoiseScratchElimination verifies:
// 1. When cumulative profit reaches the 15% monthly cap, quotes are widened to >= 25 bps to preserve banked gains.
// 2. Normal natural market oscillations (e.g. 5 bps adverse after 3 minutes) do NOT panic-scratch at a loss.
func TestMonthlyGainCapPreservationAndNoiseScratchElimination(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "piquant_monthly_cap_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := DefaultDryRunConfig(tmpDir)
	cfg.CleanReset = true
	cfg.InitialCapital = 200.00
	cfg.MonthlyGainCapPct = 15.0 // 15% monthly target ceiling
	cfg.TargetProfitBps = 18.0   // 18 bps target profit (~10% monthly average)
	cfg.EntryOffsetBps = 5.5

	eng, err := NewDryRunEngine(cfg, nil, nil)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	// 1. Test normal quoting when below monthly cap
	now := time.Now()
	eng.evaluateQuotesAndGating("BTCUSDT", 75000.0, now)

	eng.mu.Lock()
	orders := eng.activeOrders["BTCUSDT"]
	eng.mu.Unlock()

	if len(orders) == 0 {
		t.Fatalf("expected active orders placed under normal operation")
	}

	// 2. Test Monthly Gain Cap preservation mode
	// Simulate hitting +15.5% return in current evaluation window
	eng.mu.Lock()
	eng.state.TotalReturnPct = 15.5
	eng.state.Equity = 231.0
	eng.mu.Unlock()

	// Advance time past cancel latency so superseded orders are completely pruned
	eng.evaluateQuotesAndGating("BTCUSDT", 75000.0, now.Add(500*time.Millisecond))
	eng.evaluateQuotesAndGating("BTCUSDT", 75000.0, now.Add(1*time.Second))

	eng.mu.Lock()
	ordersCap := eng.activeOrders["BTCUSDT"]
	eng.mu.Unlock()

	activeFound := false
	for _, o := range ordersCap {
		if !o.CancelingUntil.IsZero() {
			continue // Skip in-flight canceling order
		}
		activeFound = true
		offsetBps := math.Abs(o.Price-75000.0) / 75000.0 * 1e4
		if offsetBps < 24.5 {
			t.Errorf("expected widened quote >= 25 bps under Monthly Gain Cap mode, got %.2f bps", offsetBps)
		}
	}
	if !activeFound {
		t.Fatalf("expected active quotes placed under Monthly Gain Cap mode")
	}

	// 3. Test that 5 bps adverse drift after 5 minutes does NOT panic-scratch
	eng.mu.Lock()
	eng.state.TotalReturnPct = 5.0 // reset back below 15%
	pos := eng.state.Positions["BTCUSDT"]
	if pos == nil {
		pos = &Position{Symbol: "BTCUSDT"}
		eng.state.Positions["BTCUSDT"] = pos
	}
	pos.Size = 0.001
	pos.AvgEntryPrice = 75000.0
	sm := eng.getOrCreateSymbolMicro("BTCUSDT")
	sm.InventorySince = now.Add(-5 * time.Minute) // Held for 5 minutes
	eng.mu.Unlock()

	// Mid price drifted 5 bps adverse ($74,962.50)
	adverseMid := 75000.0 * (1.0 - 5.0*1e-4)
	eng.evaluateQuotesAndGating("BTCUSDT", adverseMid, now.Add(2*time.Second))

	eng.mu.Lock()
	ordersHold := eng.activeOrders["BTCUSDT"]
	eng.mu.Unlock()

	for _, o := range ordersHold {
		if o.Side == "SELL" && o.CancelingUntil.IsZero() {
			// Ask price must still be ABOVE entry price (banking profit), not scratched below entry!
			if o.Price < 75000.0 {
				t.Fatalf("expected closing ask to remain profitable (>= entry $75,000), got scratch at $%.2f", o.Price)
			}
		}
	}
}

