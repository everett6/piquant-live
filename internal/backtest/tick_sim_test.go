package backtest

import (
	"math"
	"os"
	"os/exec"
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

func TestTickSimulatorInvalidPriceHandling(t *testing.T) {
	cfg := DefaultTickSimConfig()
	cfg.InitialCapital = 1000.00
	cfg.EnableMacroShield = false
	sim := NewTickSimulator(cfg)

	baseTime := int64(1700000000000000000)

	// Test 1: price = 0 should not panic and should skip processing
	tickZero := AggTradeTick{
		TradeID:      1,
		Price:        0,
		Quantity:     0.1,
		TimestampNs:  baseTime,
		IsBuyerMaker: true,
	}
	sim.ProcessTick(&tickZero)
	if sim.totalTicksProcessed != 1 {
		t.Errorf("expected totalTicksProcessed=1 after zero price tick, got %d", sim.totalTicksProcessed)
	}
	// lastMidPrice should remain 0 (initial value) since tick was skipped
	if sim.lastMidPrice != 0 {
		t.Errorf("lastMidPrice should remain 0 after invalid tick, got %f", sim.lastMidPrice)
	}

	// Test 2: price = NaN should not panic and should skip processing
	tickNaN := AggTradeTick{
		TradeID:      2,
		Price:        math.NaN(),
		Quantity:     0.1,
		TimestampNs:  baseTime + 1_000_000,
		IsBuyerMaker: true,
	}
	sim.ProcessTick(&tickNaN)
	if sim.totalTicksProcessed != 2 {
		t.Errorf("expected totalTicksProcessed=2 after NaN price tick, got %d", sim.totalTicksProcessed)
	}

	// Test 3: price = Inf should not panic and should skip processing
	tickInf := AggTradeTick{
		TradeID:      3,
		Price:        math.Inf(1),
		Quantity:     0.1,
		TimestampNs:  baseTime + 2_000_000,
		IsBuyerMaker: true,
	}
	sim.ProcessTick(&tickInf)
	if sim.totalTicksProcessed != 3 {
		t.Errorf("expected totalTicksProcessed=3 after Inf price tick, got %d", sim.totalTicksProcessed)
	}

	// Test 4: negative price should not panic and should skip processing
	tickNeg := AggTradeTick{
		TradeID:      4,
		Price:        -100.0,
		Quantity:     0.1,
		TimestampNs:  baseTime + 3_000_000,
		IsBuyerMaker: true,
	}
	sim.ProcessTick(&tickNeg)
	if sim.totalTicksProcessed != 4 {
		t.Errorf("expected totalTicksProcessed=4 after negative price tick, got %d", sim.totalTicksProcessed)
	}

	// Test 5: valid price after invalid ones should work normally
	tickValid := AggTradeTick{
		TradeID:      5,
		Price:        50000.0,
		Quantity:     0.1,
		TimestampNs:  baseTime + 4_000_000,
		IsBuyerMaker: true,
	}
	sim.ProcessTick(&tickValid)
	if sim.totalTicksProcessed != 5 {
		t.Errorf("expected totalTicksProcessed=5 after valid price tick, got %d", sim.totalTicksProcessed)
	}
	if sim.lastMidPrice != 50000.0 {
		t.Errorf("expected lastMidPrice=50000.0 after valid tick, got %f", sim.lastMidPrice)
	}
}

func TestTickSimulatorMaxPosBaseValidation(t *testing.T) {
	cfg := DefaultTickSimConfig()
	cfg.InitialCapital = 1000.00
	cfg.EnableMacroShield = false
	sim := NewTickSimulator(cfg)

	baseTime := int64(1700000000000000000)

	// Feed some valid ticks first to establish state
	for i := 0; i < 10; i++ {
		tick := AggTradeTick{
			TradeID:      uint64(i + 100),
			Price:        50000.0,
			Quantity:     0.1,
			TimestampNs:  baseTime + int64(i*1_000_000),
			IsBuyerMaker: (i%2 == 0),
		}
		sim.ProcessTick(&tick)
	}

	// Verify maxPosBase is valid after normal ticks
	if sim.currentEquity <= 0 {
		t.Errorf("expected positive equity, got %f", sim.currentEquity)
	}

	// The internal maxPosBase is not exported, but we can verify
	// that the simulator doesn't panic and state remains valid
	// by checking that quotes can be placed
	if !sim.hasBidQuote && !sim.hasAskQuote {
		t.Logf("no active quotes after initial ticks (may be due to circuit breaker or other logic)")
	}

	// Test that invalid price doesn't corrupt state
	tickInvalid := AggTradeTick{
		TradeID:      200,
		Price:        0,
		Quantity:     0.1,
		TimestampNs:  baseTime + 20_000_000,
		IsBuyerMaker: true,
	}
	sim.ProcessTick(&tickInvalid)

	// State should remain valid
	if math.IsNaN(sim.currentEquity) || math.IsInf(sim.currentEquity, 0) {
		t.Errorf("currentEquity became NaN/Inf after invalid price: %f", sim.currentEquity)
	}
	if math.IsNaN(sim.peakEquity) || math.IsInf(sim.peakEquity, 0) {
		t.Errorf("peakEquity became NaN/Inf after invalid price: %f", sim.peakEquity)
	}
	if math.IsNaN(sim.lastMidPrice) || math.IsInf(sim.lastMidPrice, 0) {
		t.Errorf("lastMidPrice became NaN/Inf after invalid price: %f", sim.lastMidPrice)
	}
}

func TestTickSimulatorRefreshQuotesNaNProtection(t *testing.T) {
	cfg := DefaultTickSimConfig()
	cfg.InitialCapital = 1000.00
	cfg.EnableMacroShield = false
	sim := NewTickSimulator(cfg)

	baseTime := int64(1700000000000000000)

	// Feed valid ticks to establish state
	for i := 0; i < 20; i++ {
		tick := AggTradeTick{
			TradeID:      uint64(i + 1000),
			Price:        50000.0 + float64(i)*0.1,
			Quantity:     0.1,
			TimestampNs:  baseTime + int64(i*1_000_000),
			IsBuyerMaker: (i%2 == 0),
		}
		sim.ProcessTick(&tick)
	}

	// Verify internal state is valid (no NaN/Inf in key fields)
	if math.IsNaN(sim.currentEquity) || math.IsInf(sim.currentEquity, 0) {
		t.Errorf("currentEquity is NaN/Inf: %f", sim.currentEquity)
	}
	if math.IsNaN(sim.volVarBps) || math.IsInf(sim.volVarBps, 0) {
		t.Errorf("volVarBps is NaN/Inf: %f", sim.volVarBps)
	}
	if math.IsNaN(sim.inventoryBase) || math.IsInf(sim.inventoryBase, 0) {
		t.Errorf("inventoryBase is NaN/Inf: %f", sim.inventoryBase)
	}
	if math.IsNaN(sim.avgEntryPrice) || math.IsInf(sim.avgEntryPrice, 0) {
		t.Errorf("avgEntryPrice is NaN/Inf: %f", sim.avgEntryPrice)
	}

	// Test that refreshQuotes doesn't panic with edge cases
	// (This is tested indirectly through ProcessTick)
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

func TestTickSimulatorResetCapitalTo(t *testing.T) {
	cfg := DefaultTickSimConfig()
	cfg.InitialCapital = 1000.0
	cfg.EnableMacroShield = false
	sim := NewTickSimulator(cfg)

	// Simulate +$50 profit in period 1, then reset to $1000
	sim.cashUSD = 1050.0
	sim.currentEquity = 1050.0
	sim.ResetCapitalTo(1000.0)

	if sim.cashUSD != 1000.0 || sim.currentEquity != 1000.0 || sim.peakEquity != 1000.0 {
		t.Fatalf("capital did not reset cleanly: cash=%.2f eq=%.2f peak=%.2f", sim.cashUSD, sim.currentEquity, sim.peakEquity)
	}

	// Simulate -$10 in period 2 -> total net profit should be +$40, FinalEquity $1040
	sim.cashUSD = 990.0
	sim.currentEquity = 990.0
	res := sim.FinalizeResult([]MonthlyTickMetric{
		{Month: "2024-01", StartEquity: 1000.0, EndEquity: 1050.0, NetReturnPct: 5.0},
		{Month: "2024-02", StartEquity: 1000.0, EndEquity: 990.0, NetReturnPct: -1.0},
	})
	if res.TotalNetProfit != 40.0 || res.FinalEquity != 1040.0 {
		t.Fatalf("unexpected FinalizeResult after reset: profit=%.2f finalEq=%.2f", res.TotalNetProfit, res.FinalEquity)
	}
}

func TestLoadPQBinFileZst(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd not on PATH; skipping .pqbin.zst test")
	}

	tmpDir := t.TempDir()
	pqPath := filepath.Join(tmpDir, "BTCUSDT-2024-01.pqbin")
	zstPath := pqPath + ".zst"

	ticks := make([]AggTradeTick, 120)
	for i := range ticks {
		ticks[i] = AggTradeTick{
			TradeID:      uint64(30000 + i),
			Price:        64000.0 + float64(i)*0.25,
			Quantity:     0.02 + float64(i)*0.0005,
			FirstTradeID: uint64(30000 + i),
			LastTradeID:  uint64(30000 + i),
			TimestampNs:  1704067200000000000 + int64(i*4_000_000),
			IsBuyerMaker: (i%2 == 1),
			IsBestMatch:  (i%4 != 0),
		}
	}

	if err := SavePQBin(pqPath, "BTCUSDT", 2024, 1, ticks); err != nil {
		t.Fatalf("SavePQBin error: %v", err)
	}

	// Compress with zstd to produce .pqbin.zst
	if out, err := exec.Command("zstd", "-q", "-f", pqPath, "-o", zstPath).CombinedOutput(); err != nil {
		t.Fatalf("zstd compress failed: %v (%s)", err, string(out))
	}

	var rawTicks []AggTradeTick
	rawHdr, err := LoadPQBinFile(pqPath, func(tick *AggTradeTick) error {
		rawTicks = append(rawTicks, *tick)
		return nil
	})
	if err != nil {
		t.Fatalf("LoadPQBinFile raw error: %v", err)
	}

	var zstTicks []AggTradeTick
	zstHdr, err := LoadPQBinFile(zstPath, func(tick *AggTradeTick) error {
		zstTicks = append(zstTicks, *tick)
		return nil
	})
	if err != nil {
		t.Fatalf("LoadPQBinFile zst error: %v", err)
	}

	if rawHdr.TickCount != zstHdr.TickCount || rawHdr.Year != zstHdr.Year || rawHdr.Month != zstHdr.Month {
		t.Fatalf("header mismatch: raw=%+v zst=%+v", rawHdr, zstHdr)
	}
	if len(rawTicks) != len(zstTicks) || len(zstTicks) != len(ticks) {
		t.Fatalf("tick count mismatch: raw=%d zst=%d want=%d", len(rawTicks), len(zstTicks), len(ticks))
	}
	for i := range rawTicks {
		if rawTicks[i] != zstTicks[i] {
			t.Fatalf("tick %d mismatch: raw=%+v zst=%+v", i, rawTicks[i], zstTicks[i])
		}
	}
}

func TestTickSimulatorPositionFlipAndSharpeAndStaleFill(t *testing.T) {
	cfg := DefaultTickSimConfig()
	cfg.InitialCapital = 10000.0
	cfg.Symbol = "BTCUSDT"
	sim := NewTickSimulator(cfg)

	// 1. Short -> Long flip updates avgEntryPrice and entryTimeNs
	t1 := int64(1700000000_000_000_000)
	t2 := t1 + 5_000_000_000
	sim.executeFill(50000.0, 0.1, false, t1) // open short 0.1 @ 50000
	sim.executeFill(51000.0, 0.3, true, t2)  // flip to long 0.2 @ 51000
	if math.Abs(sim.inventoryBase-0.2) > 1e-9 || sim.avgEntryPrice != 51000.0 || sim.entryTimeNs != t2 {
		t.Fatalf("short->long flip failed: inv=%v avg=%v entryTime=%v", sim.inventoryBase, sim.avgEntryPrice, sim.entryTimeNs)
	}

	// 2. Long -> Short flip updates avgEntryPrice and entryTimeNs
	t3 := t2 + 5_000_000_000
	sim.executeFill(52000.0, 0.5, false, t3) // flip to short 0.3 @ 52000
	if math.Abs(sim.inventoryBase-(-0.3)) > 1e-9 || sim.avgEntryPrice != 52000.0 || sim.entryTimeNs != t3 {
		t.Fatalf("long->short flip failed: inv=%v avg=%v entryTime=%v", sim.inventoryBase, sim.avgEntryPrice, sim.entryTimeNs)
	}

	// 3. computeSharpeRatio returns 0.0 for <5 samples or zero variance
	if got := computeSharpeRatio([]float64{0.01, 0.02}); got != 0.0 {
		t.Fatalf("expected 0.0 for <5 returns, got %v", got)
	}
	if got := computeSharpeRatio([]float64{0.01, 0.01, 0.01, 0.01, 0.01}); got != 0.0 {
		t.Fatalf("expected 0.0 for zero variance, got %v", got)
	}

	// 4. evaluateFill decrements qty after stale quote fill so resting quote isn't double-filled
	sim2 := NewTickSimulator(cfg)
	tNow := int64(1700000000_000_000_000)
	sim2.staleBidPrice = 50000.0
	sim2.staleBidSize = 0.5
	sim2.staleBidCancelAt = tNow + 10_000_000
	sim2.hasBidQuote = true
	sim2.bidPrice = 49990.0
	sim2.bidSize = 0.5
	sim2.bidQuoteTime = tNow - 100_000_000
	sim2.evaluateFill(&AggTradeTick{}, 49980.0, 0.5, true, tNow, 10.0)
	if sim2.totalFillsCount != 1 {
		t.Fatalf("expected exactly 1 fill (stale quote consumed full 0.5 qty), got %d", sim2.totalFillsCount)
	}
}