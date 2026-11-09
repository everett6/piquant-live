package backtest

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTickSimulatorLifecycle(t *testing.T) {
	cfg := DefaultTickSimConfig()
	cfg.InitialCapital = 1000.00
	cfg.Symbol = "BTCUSDT"

	sim := NewTickSimulator(cfg)

	// Stream 500 synthetic ticks
	basePrice := 50000.00
	baseTime := int64(1700000000000000000)

	for i := 0; i < 500; i++ {
		tNs := baseTime + int64(i*8_000_000) // 8ms ticks
		price := basePrice + float64(i%20)*0.5 - float64(i%15)*0.3
		qty := 0.05 + float64(i%5)*0.01
		isBuyerMaker := (i%2 == 0)

		tick := AggTradeTick{
			TradeID:      uint64(1000 + i),
			Price:        price,
			Quantity:     qty,
			FirstTradeID: uint64(1000 + i),
			LastTradeID:  uint64(1000 + i),
			TimestampNs:  tNs,
			IsBuyerMaker: isBuyerMaker,
			IsBestMatch:  true,
		}

		sim.ProcessTick(&tick)
	}

	snap := sim.MonthlySnapshot("2024-01", 1000.00)
	if snap.TotalTicks != 500 {
		t.Errorf("expected 500 total ticks, got %d", snap.TotalTicks)
	}
	if snap.EndEquity <= 0 {
		t.Errorf("expected positive equity, got %f", snap.EndEquity)
	}

	res := sim.FinalizeResult([]MonthlyTickMetric{snap})
	if res.Symbol != "BTCUSDT" {
		t.Errorf("expected BTCUSDT symbol, got %s", res.Symbol)
	}
	if res.InitialCapital != 1000.00 {
		t.Errorf("expected 1000 initial capital, got %f", res.InitialCapital)
	}

	tmpDir := t.TempDir()
	mdPath, jsonPath, err := GenerateTickReports(tmpDir, res)
	if err != nil {
		t.Fatalf("GenerateTickReports failed: %v", err)
	}

	if _, err := os.Stat(mdPath); err != nil {
		t.Errorf("missing generated markdown report: %v", err)
	}
	if _, err := os.Stat(jsonPath); err != nil {
		t.Errorf("missing generated json report: %v", err)
	}
}

func TestTickSimulatorMacroNewsShield(t *testing.T) {
	cfg := DefaultTickSimConfig()
	cfg.InitialCapital = 1000.00
	cfg.Symbol = "BTCUSDT"
	cfg.EnableMacroShield = true
	cfg.DataDir = t.TempDir()

	sim := NewTickSimulator(cfg)

	// CPI Event: 2024-01-11T13:30:00Z
	cpiTime := time.Date(2024, 1, 11, 13, 30, 0, 0, time.UTC).UnixNano()

	// 1. Nominal time (30 minutes before event: 13:00:00Z): quotes should be placed
	nominalTime := cpiTime - int64(30*time.Minute)
	for i := 0; i < 10; i++ {
		tNs := nominalTime + int64(i*1_000_000_000)
		tick := AggTradeTick{
			TradeID:      uint64(2000 + i),
			Price:        46000.00,
			Quantity:     0.1,
			TimestampNs:  tNs,
			IsBuyerMaker: (i%2 == 0),
		}
		sim.ProcessTick(&tick)
	}

	if !sim.hasBidQuote && !sim.hasAskQuote {
		t.Errorf("expected active quotes during nominal macro phase")
	}

	// 2. High-impact Event Halt time (1 minute before CPI release: 13:29:00Z):
	// Quotes must be cancelled / pulled to avoid knee-jerk slippage
	haltTime := cpiTime - int64(1*time.Minute)
	for i := 0; i < 10; i++ {
		tNs := haltTime + int64(i*1_000_000_000)
		tick := AggTradeTick{
			TradeID:      uint64(3000 + i),
			Price:        46050.00,
			Quantity:     0.1,
			TimestampNs:  tNs,
			IsBuyerMaker: (i%2 == 0),
		}
		sim.ProcessTick(&tick)
	}

	if sim.hasBidQuote || sim.hasAskQuote {
		t.Errorf("expected quotes to be pulled during high-impact macro event halt")
	}

	if sim.macroEventsEvaded == 0 {
		t.Errorf("expected macroEventsEvaded > 0, got %d", sim.macroEventsEvaded)
	}

	snap := sim.MonthlySnapshot("2024-01", 1000.00)
	res := sim.FinalizeResult([]MonthlyTickMetric{snap})
	if res.MacroEventsEvaded == 0 {
		t.Errorf("expected finalized MacroEventsEvaded > 0, got %d", res.MacroEventsEvaded)
	}

	// 3. Side-by-side Macro Shock Replay Comparison:
	// Simulate an aggressive CPI flash crash wick: price dumps $1,500 in 5 seconds
	cfgUnshielded := cfg
	cfgUnshielded.EnableMacroShield = false
	simUnshielded := NewTickSimulator(cfgUnshielded)

	cfgShielded := cfg
	cfgShielded.EnableMacroShield = true
	simShielded := NewTickSimulator(cfgShielded)

	// Feed pre-event ticks at 13:26:00 (5m before event, inside PRE_EVENT_DEFENSE window where quotes exist)
	preEventTime := cpiTime - int64(4*time.Minute)
	for i := 0; i < 20; i++ {
		tNs := preEventTime + int64(i*500_000_000)
		tick := AggTradeTick{
			TradeID:      uint64(4000 + i),
			Price:        46000.00,
			Quantity:     0.05,
			TimestampNs:  tNs,
			IsBuyerMaker: (i%2 == 0),
		}
		simUnshielded.ProcessTick(&tick)
		simShielded.ProcessTick(&tick)
	}

	// Cross into EVENT_HALT at 13:28:30 (90s before CPI): quotes must be pulled and event counted as evaded
	haltEntryTime := cpiTime - int64(90*time.Second)
	for i := 0; i < 5; i++ {
		tNs := haltEntryTime + int64(i*500_000_000)
		tick := AggTradeTick{
			TradeID:      uint64(4500 + i),
			Price:        46010.00,
			Quantity:     0.05,
			TimestampNs:  tNs,
			IsBuyerMaker: (i%2 == 0),
		}
		simUnshielded.ProcessTick(&tick)
		simShielded.ProcessTick(&tick)
	}

	t.Logf("Before Macro Print (at 13:29:40 UTC):")
	t.Logf("  Unshielded hasBidQuote: %v (price=%.2f) | Shielded hasBidQuote: %v",
		simUnshielded.hasBidQuote, simUnshielded.bidPrice, simShielded.hasBidQuote)

	// At 13:30:00 (exact CPI print), an informed seller sweeps down to 44,500
	dumpTick := AggTradeTick{
		TradeID:      5000,
		Price:        44500.00,
		Quantity:     10.0,
		TimestampNs:  cpiTime,
		IsBuyerMaker: true, // Aggressive seller hitting bids
	}
	simUnshielded.ProcessTick(&dumpTick)
	simShielded.ProcessTick(&dumpTick)

	// Shielded bot evaded quotes during event halt; unshielded bot got filled
	t.Logf("Macro News Shock Simulation Result:")
	t.Logf("  Unshielded: Equity=$%.2f | TakerFees=$%.2f | Fills=%d | Max Drawdown=%.2f%%",
		simUnshielded.currentEquity, simUnshielded.takerFeesUSD, simUnshielded.totalFillsCount, simUnshielded.maxDrawdownPct)
	t.Logf("  Shielded:   Equity=$%.2f | TakerFees=$%.2f | Fills=%d | Max Drawdown=%.2f%% (Events Evaded: %d)",
		simShielded.currentEquity, simShielded.takerFeesUSD, simShielded.totalFillsCount, simShielded.maxDrawdownPct, simShielded.macroEventsEvaded)

	if simShielded.inventoryBase != 0 {
		t.Errorf("expected shielded simulator to hold 0 inventory during macro shock, got %f", simShielded.inventoryBase)
	}
	if simShielded.maxDrawdownPct > 0.05 {
		t.Errorf("expected shielded simulator max drawdown <= 0.05%%, got %.2f%%", simShielded.maxDrawdownPct)
	}
}

func BenchmarkTickSimulator_ProcessTick(b *testing.B) {
	cfg := DefaultTickSimConfig()
	sim := NewTickSimulator(cfg)

	tick := AggTradeTick{
		TradeID:      10001,
		Price:        50000.00,
		Quantity:     0.05,
		FirstTradeID: 10001,
		LastTradeID:  10001,
		TimestampNs:  1700000000000000000,
		IsBuyerMaker: true,
		IsBestMatch:  true,
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		tick.TradeID = uint64(i + 1)
		tick.TimestampNs += 8_000_000 // +8ms
		sim.ProcessTick(&tick)
	}
}

func TestPQBinStreamRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	pqPath := filepath.Join(tmpDir, "test-stream.pqbin")

	ticks := make([]AggTradeTick, 100)
	for i := 0; i < 100; i++ {
		ticks[i] = AggTradeTick{
			TradeID:      uint64(20000 + i),
			Price:        65000.0 + float64(i)*0.1,
			Quantity:     0.01 + float64(i)*0.001,
			FirstTradeID: uint64(20000 + i),
			LastTradeID:  uint64(20000 + i),
			TimestampNs:  1704067200000000000 + int64(i*5000000),
			IsBuyerMaker: (i%3 == 0),
			IsBestMatch:  true,
		}
	}

	if err := SavePQBin(pqPath, "BTCUSDT", 2024, 1, ticks); err != nil {
		t.Fatalf("SavePQBin error: %v", err)
	}

	count := 0
	_, err := LoadPQBinFile(pqPath, func(tick *AggTradeTick) error {
		if tick.TradeID != ticks[count].TradeID {
			t.Errorf("tick %d ID mismatch: %d vs %d", count, tick.TradeID, ticks[count].TradeID)
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("LoadPQBinFile error: %v", err)
	}
	if count != 100 {
		t.Errorf("expected 100 ticks streamed, got %d", count)
	}
}
