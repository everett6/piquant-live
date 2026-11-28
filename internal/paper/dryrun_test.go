package paper

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"piquant-live/internal/engine"
	"piquant-live/internal/experiment"
	"piquant-live/internal/marketdata"
	"piquant-live/internal/models/common"
)

func TestNewDryRunEnginePreservesExplicitDuration(t *testing.T) {
	cfg := DefaultDryRunConfig(t.TempDir())
	cfg.WindowPreset = "7d"
	cfg.TargetDuration = 48*time.Hour + 30*time.Minute
	cfg.TargetDurationExplicit = true

	eng, err := NewDryRunEngine(cfg, nil, nil)
	if err != nil {
		t.Fatalf("NewDryRunEngine: %v", err)
	}
	if eng.cfg.TargetDuration != cfg.TargetDuration {
		t.Fatalf("explicit target duration changed: got %v want %v", eng.cfg.TargetDuration, cfg.TargetDuration)
	}
}

func TestDryRunEngineLifecycleAndCheckpoint(t *testing.T) {
	tempDir := t.TempDir()
	cfg := DefaultDryRunConfig(tempDir)
	cfg.CheckpointFreq = 10 * time.Millisecond
	cfg.TargetDuration = 14 * 24 * time.Hour

	ring := engine.NewSPSCRing(1024)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dryRun, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create dry run engine: %v", err)
	}

	dryRun.Start(ctx)

	// Verify initial state
	st := dryRun.State()
	if st.InitialCapital != 200.00 {
		t.Errorf("expected initial capital 200, got %v", st.InitialCapital)
	}
	if st.Equity != 200.00 {
		t.Errorf("expected equity 200, got %v", st.Equity)
	}
	if st.DaysElapsed < 1 || st.DaysElapsed > 14 {
		t.Errorf("expected days elapsed in [1, 14], got %v", st.DaysElapsed)
	}
	if st.HoursRemaining < 330 || st.HoursRemaining > 337 {
		t.Errorf("expected hours remaining ~336h, got %v", st.HoursRemaining)
	}

	// Push 20 ticks into the ring (depth and trades)
	nowNs := time.Now().UnixNano()
	for i := 0; i < 20; i++ {
		ring.Push(engine.MarketEvent{
			Timestamp: nowNs + int64(i*1000000),
			Sequence:  uint64(1000 + i),
			Price:     65000.0 + float64(i)*0.5,
			Size:      1.5,
			Side:      uint8(1 + (i % 2)),
			Kind:      uint8(i % 2),
			SymbolID:  0, // BTCUSDT
		})
	}

	// Give execution loop time to process
	time.Sleep(150 * time.Millisecond)

	stAfter := dryRun.State()
	t.Logf("Processed ticks: TotalTrades=%d RealizedPnL=%.2f Equity=%.2f Return=%.4f%%",
		stAfter.TotalTrades, stAfter.RealizedPnL, stAfter.Equity, stAfter.TotalReturnPct)

	// Stop engine and ensure checkpoint file exists
	dryRun.Stop()

	if _, err := os.Stat(cfg.StateFile); os.IsNotExist(err) {
		t.Fatalf("checkpoint state file %s does not exist", cfg.StateFile)
	}

	// Test seamless recovery / resume after restart/reboot
	dryRun2, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to recover dry run engine: %v", err)
	}

	stRecovered := dryRun2.State()
	if !stRecovered.StartTime.Equal(st.StartTime) {
		t.Errorf("expected recovered start time %v to match %v", stRecovered.StartTime, st.StartTime)
	}
	if stRecovered.TotalTrades != stAfter.TotalTrades {
		t.Errorf("expected recovered total trades %d to match %d", stRecovered.TotalTrades, stAfter.TotalTrades)
	}
	if stRecovered.Equity != stAfter.Equity {
		t.Errorf("expected recovered equity %.2f to match %.2f", stRecovered.Equity, stAfter.Equity)
	}
}

func TestCircuitBreakerEmergencyHalt(t *testing.T) {
	tempDir := t.TempDir()
	cfg := DefaultDryRunConfig(tempDir)
	cfg.MaxDrawdownPct = 2.0 // 2% limit

	ring := engine.NewSPSCRing(1024)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)

	dryRun, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create dry run engine: %v", err)
	}

	// Simulate long position with massive drop to trigger >2% max drawdown
	dryRun.mu.Lock()
	dryRun.state.Cash = 0
	dryRun.state.Equity = 100000.00
	dryRun.state.PeakEquity = 100000.00
	dryRun.state.Positions["BTCUSDT"] = &Position{
		Symbol:        "BTCUSDT",
		Size:          2.0,
		AvgEntryPrice: 50000.0,
	}
	dryRun.mu.Unlock()

	// Price drops by 1500 (from 50000 to 48500 -> 2.0 * -1500 = -3000 USDT loss = 3% drawdown)
	ev := engine.MarketEvent{
		Timestamp: time.Now().UnixNano(),
		Sequence:  101,
		Price:     48500.0,
		Size:      1.0,
		Side:      2,
		Kind:      0,
		SymbolID:  0,
	}
	dryRun.processMarketEvent(&ev)

	st := dryRun.State()
	if !st.CircuitHalted {
		t.Errorf("expected circuit breaker to be halted due to >2%% drawdown, but was not halted")
	}
	if st.MaxDrawdownPct < 2.0 {
		t.Errorf("expected MaxDrawdownPct >= 2.0%%, got %.2f%%", st.MaxDrawdownPct)
	}
	t.Logf("Circuit Breaker successfully triggered: Reason=%s (MaxDD=%.2f%%)", st.CircuitHaltReason, st.MaxDrawdownPct)
}

func TestContDeLarrardAndKylesLambdaExecution(t *testing.T) {
	tempDir := t.TempDir()
	cfg := DefaultDryRunConfig(tempDir)
	cfg.KylesLambda = 0.50

	ring := engine.NewSPSCRing(1024)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)

	dryRun, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create dry run: %v", err)
	}

	// Test Maker Fill with GLFT spread capture
	qd := common.QuoteDecision{
		ReservationBps: 0.50,
		HalfSpreadBps:  1.20,
		BidOffsetBps:   0.70,
		AskOffsetBps:   1.70,
	}
	dryRun.simulateMakerFill("BTCUSDT", 60000.0, qd, 1.20)

	st := dryRun.State()
	if st.TotalTrades != 1 {
		t.Fatalf("expected 1 maker trade, got %d", st.TotalTrades)
	}
	pos := st.Positions["BTCUSDT"]
	if pos.Size <= 0 {
		t.Errorf("expected positive pos size, got %v", pos.Size)
	}
	if pos.MakerTrades != 1 {
		t.Errorf("expected 1 maker trade in pos, got %d", pos.MakerTrades)
	}

	// Test Taker Fill with Kyle's Lambda slippage (close/reduce long position)
	dryRun.simulateTakerFill("BTCUSDT", 60000.0, -3.50)
	st2 := dryRun.State()
	if st2.TotalTrades != 2 {
		t.Fatalf("expected 2 total trades, got %d", st2.TotalTrades)
	}
	pos2 := st2.Positions["BTCUSDT"]
	if pos2.TakerTrades != 1 {
		t.Errorf("expected 1 taker trade in pos, got %d", pos2.TakerTrades)
	}

	fills := dryRun.RecentFills()
	if len(fills) != 2 {
		t.Fatalf("expected 2 recent fills, got %d", len(fills))
	}
	if fills[1].Role != "TAKER" || fills[1].SlippageBps <= 0 {
		t.Errorf("expected taker fill with positive Kyle's lambda slippage, got %+v", fills[1])
	}
}

func TestMarketStreamIngestorFallback(t *testing.T) {
	ring := engine.NewSPSCRing(1024)
	cfg := StreamConfig{
		Endpoint:       "wss://stream.binance.com:9443/invalid-path",
		Symbols:        []string{"BTCUSDT", "ETHUSDT"},
		EnableFallback: true,
	}

	stream := NewMarketStreamIngestor(cfg, ring)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream.Start(ctx)

	// Wait for fallback watchdog to generate synthetic ticks into ring
	time.Sleep(350 * time.Millisecond)

	stream.Stop()

	total, depths, trades, _, _, fallbacks, lastNs, _ := stream.Stats()
	if fallbacks == 0 || total == 0 {
		t.Errorf("expected fallback generator to generate events, got fallbacks=%d total=%d", fallbacks, total)
	}
	if lastNs == 0 {
		t.Errorf("expected lastNs > 0")
	}
	t.Logf("Fallback generator verified: Total=%d Depths=%d Trades=%d Fallbacks=%d", total, depths, trades, fallbacks)

	// Verify events were delivered to SPSC ring
	if ring.Len() == 0 {
		t.Errorf("expected ring to have received events")
	}
	popped := 0
	for {
		ev, ok := ring.Pop()
		if !ok {
			break
		}
		popped++
		if ev.Price <= 0 {
			t.Errorf("unexpected invalid price in ring: %v", ev.Price)
		}
	}
	if popped == 0 {
		t.Errorf("expected to pop events from ring")
	}
}

func TestTwelveHourShutdownRecovery(t *testing.T) {
	tempDir := t.TempDir()
	cfg := DefaultDryRunConfig(tempDir)
	cfg.InitialCapital = 100000.00
	cfg.TargetDuration = 336 * time.Hour
	cfg.ExtendOnShutdown = true

	ring := engine.NewSPSCRing(1024)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)

	dryRun, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create dry run engine: %v", err)
	}

	// 1. Process initial market ticks and simulated maker trades
	now := time.Now()
	for i := 0; i < 20; i++ {
		ev := engine.MarketEvent{
			Timestamp: now.Add(time.Duration(i) * time.Millisecond).UnixNano(),
			Sequence:  uint64(i + 1),
			Price:     65000.0 + float64(i%5),
			Size:      0.5,
			Side:      uint8(i % 2),
			Kind:      0, // Trade
			SymbolID:  0,
		}
		dryRun.processMarketEvent(&ev)
	}

	// Record pre-shutdown state
	dryRun.mu.Lock()
	dryRun.state.TotalTrades = 5
	dryRun.state.RealizedPnL = 42.80
	dryRun.state.Equity = 100042.80
	dryRun.state.Cash = 100042.80
	targetEndBefore := dryRun.state.TargetEndTime
	dryRun.mu.Unlock()

	// Checkpoint to disk
	if err := dryRun.saveCheckpoint(); err != nil {
		t.Fatalf("failed to save checkpoint: %v", err)
	}
	dryRun.Stop()

	// 2. Simulate 12-Hour Shutdown (Pi powered off for 12 hours)
	twelveHoursAgo := now.Add(-12 * time.Hour)
	rawBytes, err := os.ReadFile(cfg.StateFile)
	if err != nil {
		t.Fatalf("failed to read state file: %v", err)
	}
	var loadedState DryRunState
	if err := json.Unmarshal(rawBytes, &loadedState); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}
	loadedState.LastCheckpoint = twelveHoursAgo
	modifiedBytes, _ := json.MarshalIndent(loadedState, "", "  ")
	if err := os.WriteFile(cfg.StateFile, modifiedBytes, 0o644); err != nil {
		t.Fatalf("failed to write simulated 12h shutdown state: %v", err)
	}

	// 3. Boot up PiQuant after 12 hours offline
	dryRunResumed, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to resume engine after 12h shutdown: %v", err)
	}
	defer dryRunResumed.Stop()

	st := dryRunResumed.State()

	// Verify state continuity
	if st.InitialCapital != 100000.00 {
		t.Errorf("expected initial capital 100000.00, got %.2f", st.InitialCapital)
	}
	if st.TotalTrades != 5 {
		t.Errorf("expected 5 trades preserved, got %d", st.TotalTrades)
	}
	if st.RealizedPnL != 42.80 {
		t.Errorf("expected RealizedPnL 42.80 preserved, got %.2f", st.RealizedPnL)
	}
	if st.Equity != 100042.80 {
		t.Errorf("expected Equity 100042.80 preserved, got %.2f", st.Equity)
	}

	// Verify 12-hour offline recovery
	if st.ShutdownCount != 1 {
		t.Errorf("expected ShutdownCount=1, got %d", st.ShutdownCount)
	}
	if st.OfflineDowntimeSeconds < 11.9*3600 {
		t.Errorf("expected ~12h offline downtime recorded, got %.1fs", st.OfflineDowntimeSeconds)
	}
	if st.LastResumeTime.IsZero() {
		t.Errorf("expected LastResumeTime to be set")
	}

	// Verify TargetEndTime was extended by ~12 hours to guarantee full 14-day evaluation
	extendedDiff := st.TargetEndTime.Sub(targetEndBefore)
	if extendedDiff < time.Duration(11.9*float64(time.Hour)) || extendedDiff > time.Duration(12.1*float64(time.Hour)) {
		t.Errorf("expected TargetEndTime to be extended by ~12 hours, got diff=%v", extendedDiff)
	}

	// 4. Verify engine seamlessly processes new live market ticks after recovery
	newEv := engine.MarketEvent{
		Timestamp: time.Now().UnixNano(),
		Sequence:  2000,
		Price:     67000.0,
		Size:      1.0,
		Side:      1,
		Kind:      0,
		SymbolID:  0,
	}
	dryRunResumed.processMarketEvent(&newEv)

	stFinal := dryRunResumed.State()
	if stFinal.Positions["BTCUSDT"].MarkPrice != 67000.0 {
		t.Errorf("expected new mark price 67000.0, got %.2f", stFinal.Positions["BTCUSDT"].MarkPrice)
	}
}

func TestFuturesMarginFillAccounting(t *testing.T) {
	tempDir := t.TempDir()
	cfg := DefaultDryRunConfig(tempDir)
	cfg.InitialCapital = 200.00
	ring := engine.NewSPSCRing(1024)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)

	dryRun, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	dryRun.mu.Lock()
	// Open long 0.19 ETH @ $2238.89 (Notional ~$425.39 on $200 capital)
	dryRun.executeMakerFillLocked("ETHUSDT", 2238.89, 0.19, "BUY", 1.5, "glft")
	st := dryRun.state
	dryRun.mu.Unlock()

	// Cash should be initial capital minus 0 fee = 200.00 (NOT -$225!)
	if st.Cash < 199.0 || st.Cash > 201.0 {
		t.Errorf("Cash should remain ~200.00 on margin buy, got %.2f", st.Cash)
	}

	// Update mark tick at entry price
	dryRun.processMarketEvent(&engine.MarketEvent{
		SymbolID: 1,
		Price:    2238.89,
	})
	st2 := dryRun.State()
	if st2.Equity < 199.0 || st2.Equity > 201.0 {
		t.Errorf("Equity should remain ~200.00 at entry price, got %.2f", st2.Equity)
	}
	if st2.CircuitHalted {
		t.Errorf("Circuit breaker should NOT trip on leveraged open!")
	}
}

func TestMultiAssetMicrostructureIsolationAndClipSizing(t *testing.T) {
	tempDir := t.TempDir()
	cfg := DefaultDryRunConfig(tempDir)
	cfg.InitialCapital = 200.00
	ring := engine.NewSPSCRing(1024)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)

	// Populate BTC ($500k L5 depth) and ETH ($60k L5 depth) L2 books
	stream.booksMu.Lock()
	btcBook := &marketdata.L2OrderBook{
		NumBids: 5,
		NumAsks: 5,
	}
	ethBook := &marketdata.L2OrderBook{
		NumBids: 5,
		NumAsks: 5,
	}
	for i := 0; i < 5; i++ {
		btcBook.Bids[i] = marketdata.PriceLevel{Price: 71800.0 - float64(i), Size: 0.70}   // ~$250k bid side
		btcBook.Asks[i] = marketdata.PriceLevel{Price: 71801.0 + float64(i), Size: 0.70}   // ~$250k ask side
		ethBook.Bids[i] = marketdata.PriceLevel{Price: 2238.0 - float64(i)*0.1, Size: 2.5} // ~$28k bid side
		ethBook.Asks[i] = marketdata.PriceLevel{Price: 2238.2 + float64(i)*0.1, Size: 2.5} // ~$28k ask side
	}
	stream.books["BTCUSDT"] = btcBook
	stream.books["ETHUSDT"] = ethBook
	stream.booksMu.Unlock()

	dryRun, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	// Alternate BTC and ETH depth ticks within 5ms (previously triggered false depth_collapse_80pct)
	now := time.Now().UnixNano()
	for i := 0; i < 20; i++ {
		dryRun.processMarketEvent(&engine.MarketEvent{
			Timestamp: now + int64(i)*1_000_000,
			SymbolID:  0, // BTCUSDT
			Kind:      1, // Depth
			Price:     71800.5,
		})
		dryRun.processMarketEvent(&engine.MarketEvent{
			Timestamp: now + int64(i)*1_000_000 + 500_000,
			SymbolID:  1, // ETHUSDT
			Kind:      1, // Depth
			Price:     2238.1,
		})
	}

	st := dryRun.State()
	if st.CircuitHalted {
		t.Fatalf("Multi-asset BTC/ETH depth alternation must NOT trip CircuitHalted, got reason: %s", st.CircuitHaltReason)
	}

	clipBTC, maxBTC := dryRun.leverageCtrl.ComputeClipSizeForSymbol("BTCUSDT", 200.0, 2.5, 71800.5, 2)
	if clipBTC <= 0 || maxBTC <= clipBTC {
		t.Errorf("Expected valid clip and max inventory for BTCUSDT, got clip=%f max=%f", clipBTC, maxBTC)
	}
}

func TestQuantBugFixesVerification(t *testing.T) {
	tempDir := t.TempDir()
	cfg := DefaultDryRunConfig(tempDir)
	cfg.MakerFeeBps = 1.5
	cfg.TakerFeeBps = 4.0
	cfg.InitialCapital = 200.00
	cfg.StateFile = filepath.Join(tempDir, "state.json")

	ring := engine.NewSPSCRing(1024)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)

	dryRun, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create dry run engine: %v", err)
	}

	// 1. Verify Entry and Exit fills with net-of-fee profitability
	// BUY 0.001 BTC at 50,000.00 (maker)
	dryRun.mu.Lock()
	dryRun.executeMakerFillLocked("BTCUSDT", 50000.00, 0.001, "BUY", 1.95, "GLFT_Maker")
	pos := dryRun.state.Positions["BTCUSDT"]
	if pos.Size != 0.001 {
		t.Fatalf("expected pos size 0.001, got %f", pos.Size)
	}
	if pos.AvgEntryPrice != 50000.00 {
		t.Fatalf("expected avg entry 50000, got %f", pos.AvgEntryPrice)
	}
	// Initial open trade fee: 50 * 0.00015 = 0.0075 USDT
	openCash := dryRun.state.Cash
	if math.Abs(openCash-(200.00-0.0075)) > 1e-4 {
		t.Errorf("expected cash after open fee %f, got %f", 200.00-0.0075, openCash)
	}

	// SELL 0.001 BTC at 50,020.00 (maker) -> fully closing inventory
	// Gross profit: (50020 - 50000) * 0.001 = +0.0200 USDT
	// Exit fee: 50.02 * 0.00015 = 0.007503 USDT
	// Net round-trip PnL: 0.0200 - 0.0075 - 0.007503 = +0.004997 USDT
	dryRun.executeMakerFillLocked("BTCUSDT", 50020.00, 0.001, "SELL", 1.95, "GLFT_Maker")
	if pos.Size != 0.0 {
		t.Errorf("expected pos size 0.0 after full close, got %f", pos.Size)
	}
	if pos.AvgEntryPrice != 0.0 {
		t.Errorf("expected avg entry price reset to 0.0 on zero size, got %f", pos.AvgEntryPrice)
	}
	if dryRun.state.WinningTrades != 1 || dryRun.state.LosingTrades != 0 {
		t.Errorf("expected 1 win 0 loss, got wins=%d losses=%d", dryRun.state.WinningTrades, dryRun.state.LosingTrades)
	}
	if dryRun.state.WinRate != 100.0 {
		t.Errorf("expected 100%% win rate, got %f", dryRun.state.WinRate)
	}
	dryRun.mu.Unlock()

	// 2. Verify optimal_params.json gamma hot-reloading
	configDir := filepath.Join(tempDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	optPath := filepath.Join(configDir, "optimal_params.json")
	optJSON := `{"gamma": 0.0005, "min_half_spread_bps": 1.25, "clip_fraction": 0.25, "base_leverage": 2.0, "max_leverage_cap": 4.0}`
	if err := os.WriteFile(optPath, []byte(optJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	dryRun.maybeReloadCloudParams()
	if dryRun.glftMM.Gamma != 0.0005 {
		t.Errorf("expected reloaded gamma 0.0005, got %f", dryRun.glftMM.Gamma)
	}
	if dryRun.minHalfSpreadFloorBps != 1.25 {
		t.Errorf("expected reloaded minHalfSpreadFloorBps 1.25, got %f", dryRun.minHalfSpreadFloorBps)
	}

	// 3. Verify state recovery from append-only journal when state.json is wiped
	journalPath := filepath.Join(tempDir, "journal.jsonl")
	jLines := `{"timestamp":"2026-11-08T10:00:00Z","symbol":"BTCUSDT","side":"BUY","role":"MAKER","price":50000.0,"size":0.001,"value_usdt":50.0,"fee_usdt":0.0075,"pnl_realized":-0.0075}
{"timestamp":"2026-11-08T10:01:00Z","symbol":"BTCUSDT","side":"SELL","role":"MAKER","price":50020.0,"size":0.001,"value_usdt":50.02,"fee_usdt":0.007503,"pnl_realized":0.012497}
`
	if err := os.WriteFile(journalPath, []byte(jLines), 0o644); err != nil {
		t.Fatal(err)
	}

	// Wipe state file so engine must recover from journal
	_ = os.Remove(cfg.StateFile)
	cfg.JournalFile = journalPath

	dryRun2, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create recovered engine: %v", err)
	}

	st := dryRun2.State()
	if st.TotalTrades != 2 {
		t.Errorf("expected 2 recovered trades from journal, got %d", st.TotalTrades)
	}
	if st.WinningTrades != 1 {
		t.Errorf("expected 1 winning trade from journal, got %d", st.WinningTrades)
	}
	if st.Positions["BTCUSDT"].Size != 0.0 {
		t.Errorf("expected flat position after round trip, got %f", st.Positions["BTCUSDT"].Size)
	}
	expectedNetCash := 200.00 - 0.0075 + 0.012497
	if math.Abs(st.Cash-expectedNetCash) > 1e-4 {
		t.Errorf("expected cash %f, got %f", expectedNetCash, st.Cash)
	}
}

func BenchmarkProcessMarketEvent(b *testing.B) {
	tempDir := b.TempDir()
	cfg := DefaultDryRunConfig(tempDir)
	ring := engine.NewSPSCRing(1024)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)

	dryRun, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		b.Fatalf("failed to create dry run engine: %v", err)
	}

	ev := engine.MarketEvent{
		Timestamp: time.Now().UnixNano(),
		Sequence:  1000,
		Price:     50000.0,
		Size:      1.5,
		Side:      1,
		Kind:      1,
		SymbolID:  0,
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		ev.Sequence = uint64(i)
		dryRun.processMarketEvent(&ev)
	}
}

func TestMacroGuardianEventHaltAndSpreadScaling(t *testing.T) {
	tempDir := t.TempDir()
	cfg := DefaultDryRunConfig(tempDir)
	cfg.EnableMacroShield = true

	ring := engine.NewSPSCRing(1024)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)

	engine, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create dry run engine: %v", err)
	}

	// 1. Test during active high-impact FOMC event (2026-11-05 19:00:00 UTC)
	// Event Halt phase: T - 2m to T + 5m -> quotes MUST be pulled, AllowQuoting = false
	fomcTime := time.Date(2026, 11, 5, 19, 0, 0, 0, time.UTC)
	engine.evaluateQuotesAndGating("BTCUSDT", 75000.0, fomcTime)

	st := engine.State()
	if st.Macro.PhaseName != "EVENT_HALT" {
		t.Errorf("expected phase EVENT_HALT during FOMC release, got %s", st.Macro.PhaseName)
	}
	if st.Macro.AllowQuoting {
		t.Errorf("expected AllowQuoting=false during EVENT_HALT, got true")
	}
	if len(st.ActiveOrders["BTCUSDT"]) > 0 {
		t.Errorf("expected zero active resting orders during EVENT_HALT, got %d", len(st.ActiveOrders["BTCUSDT"]))
	}
	if !st.QuotingPaused {
		t.Errorf("expected QuotingPaused=true during EVENT_HALT")
	}

	// 2. Test during Pre-Event Defense (T - 10m before FOMC)
	preEventTime := time.Date(2026, 11, 5, 18, 50, 0, 0, time.UTC)
	engine.evaluateQuotesAndGating("BTCUSDT", 75000.0, preEventTime)

	stPre := engine.State()
	if stPre.Macro.PhaseName != "PRE_EVENT_DEFENSE" {
		t.Errorf("expected phase PRE_EVENT_DEFENSE, got %s", stPre.Macro.PhaseName)
	}
	if !stPre.Macro.AllowQuoting {
		t.Errorf("expected AllowQuoting=true in pre-event defense")
	}
	if stPre.Macro.SpreadMultiplier != 2.5 {
		t.Errorf("expected spread multiplier 2.5x, got %f", stPre.Macro.SpreadMultiplier)
	}
	if stPre.Macro.LeverageMultiplier != 0.50 {
		t.Errorf("expected leverage multiplier 0.50x, got %f", stPre.Macro.LeverageMultiplier)
	}

	// 3. Test during Nominal Conditions (> 15m away, e.g. 2 hours before)
	nominalTime := time.Date(2026, 11, 5, 17, 0, 0, 0, time.UTC)
	engine.evaluateQuotesAndGating("BTCUSDT", 75000.0, nominalTime)

	stNom := engine.State()
	if stNom.Macro.PhaseName != "NOMINAL" {
		t.Errorf("expected phase NOMINAL, got %s", stNom.Macro.PhaseName)
	}
	if !stNom.Macro.AllowQuoting {
		t.Errorf("expected AllowQuoting=true in nominal phase")
	}
	if stNom.Macro.SpreadMultiplier != 1.0 {
		t.Errorf("expected spread multiplier 1.0x, got %f", stNom.Macro.SpreadMultiplier)
	}
}

func TestBreakevenFloorAndProfitLockingRatchet(t *testing.T) {
	tempDir := t.TempDir()
	cfg := DefaultDryRunConfig(tempDir)
	cfg.MakerFeeBps = 1.5 // 3.0 bps round trip

	ring := engine.NewSPSCRing(1024)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)

	engine, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create dry run engine: %v", err)
	}

	// 1. Manually establish a LONG position in ETHUSDT at $2230.00
	engine.mu.Lock()
	engine.state.Positions["ETHUSDT"] = &Position{
		Symbol:        "ETHUSDT",
		Size:          0.020,
		AvgEntryPrice: 2230.00,
		MarkPrice:     2230.00,
	}
	engine.mu.Unlock()

	// 2. Evaluate quotes when midPrice drops slightly to $2229.50 (normal 2 bps noise against position)
	now := time.Now()
	engine.evaluateQuotesAndGating("ETHUSDT", 2229.50, now)

	st := engine.State()
	orders := st.ActiveOrders["ETHUSDT"]
	var sellOrd *ActiveMakerOrder
	for _, o := range orders {
		if o.Side == "SELL" {
			sellOrd = &o
			break
		}
	}
	if sellOrd == nil {
		t.Fatalf("expected active SELL order for ETHUSDT")
	}

	// Min profit ask = 2230.00 * (1.0 + (3.0 + 12.5)*1e-4) = 2233.456 -> rounded up to 2233.46
	minExpectedAsk := 2233.45
	if sellOrd.Price < minExpectedAsk {
		t.Errorf("expected sell order price >= %f (breakeven floor), got %f", minExpectedAsk, sellOrd.Price)
	}

	// 3. Test Profit-Locking: midPrice surges to $2240.00 (> 2230 + 15.5 bps profit threshold)
	engine.evaluateQuotesAndGating("ETHUSDT", 2240.00, now)
	stSurge := engine.State()
	var surgeSell *ActiveMakerOrder
	for _, o := range stSurge.ActiveOrders["ETHUSDT"] {
		if o.Side == "SELL" {
			surgeSell = &o
			break
		}
	}
	if surgeSell == nil {
		t.Fatalf("expected active SELL order during surge")
	}
	if surgeSell.Price < 2233.45 {
		t.Errorf("expected surge sell price >= 2233.45, got %f", surgeSell.Price)
	}
}

func TestUnconditionalExitQuotingAndFullPositionSizing(t *testing.T) {
	tempDir := t.TempDir()
	cfg := DefaultDryRunConfig(tempDir)

	ring := engine.NewSPSCRing(1024)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)

	engine, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create dry run engine: %v", err)
	}

	// 1. Establish an open LONG position of 0.035 ETH (larger than standard clip)
	targetLongQty := 0.035
	engine.mu.Lock()
	engine.state.Positions["ETHUSDT"] = &Position{
		Symbol:        "ETHUSDT",
		Size:          targetLongQty,
		AvgEntryPrice: 2230.00,
		MarkPrice:     2230.00,
	}
	engine.mu.Unlock()

	now := time.Now()
	engine.evaluateQuotesAndGating("ETHUSDT", 2230.00, now)

	st := engine.State()
	var exitSellOrd *ActiveMakerOrder
	for _, o := range st.ActiveOrders["ETHUSDT"] {
		if o.Side == "SELL" {
			exitSellOrd = &o
			break
		}
	}
	if exitSellOrd == nil {
		t.Fatalf("expected active SELL exit order for open long position")
	}
	if math.Abs(exitSellOrd.Size-targetLongQty) > 1e-6 {
		t.Errorf("expected exit SELL order size to match full open inventory (%f), got %f", targetLongQty, exitSellOrd.Size)
	}

	// 2. Establish an open SHORT position of -0.0045 BTC
	targetShortQty := 0.0045
	engine.mu.Lock()
	engine.state.Positions["BTCUSDT"] = &Position{
		Symbol:        "BTCUSDT",
		Size:          -targetShortQty,
		AvgEntryPrice: 76000.00,
		MarkPrice:     76000.00,
	}
	engine.mu.Unlock()

	engine.evaluateQuotesAndGating("BTCUSDT", 76000.00, now)

	stBTC := engine.State()
	var exitBuyOrd *ActiveMakerOrder
	for _, o := range stBTC.ActiveOrders["BTCUSDT"] {
		if o.Side == "BUY" {
			exitBuyOrd = &o
			break
		}
	}
	if exitBuyOrd == nil {
		t.Fatalf("expected active BUY exit order for open short position")
	}
	if math.Abs(exitBuyOrd.Size-targetShortQty) > 1e-6 {
		t.Errorf("expected exit BUY order size to match full open short inventory (%f), got %f", targetShortQty, exitBuyOrd.Size)
	}
}

func TestCleanResetPurgesLegacyStats(t *testing.T) {
	tempDir := t.TempDir()
	paperDir := filepath.Join(tempDir, "paper")
	_ = os.MkdirAll(paperDir, 0o755)

	statePath := filepath.Join(paperDir, "dryrun_state.json")
	journalPath := filepath.Join(paperDir, "dryrun_journal.jsonl")

	// Write mock contaminated legacy state
	mockState := DryRunState{
		WinningTrades:  4,
		LosingTrades:   106,
		TotalTrades:    110,
		WinRate:        3.636,
		RealizedPnL:    -3.50,
		InitialCapital: 200.0,
		Equity:         196.50,
		Cash:           196.50,
		PeakEquity:     200.0,
		MaxDrawdownPct: 1.75,
		Positions: map[string]*Position{
			"BTCUSDT": {Symbol: "BTCUSDT", Size: 0.001, AvgEntryPrice: 75000},
		},
	}
	raw, _ := json.Marshal(mockState)
	_ = os.WriteFile(statePath, raw, 0o644)
	_ = os.WriteFile(journalPath, []byte("{\"fill\":\"legacy\"}\n"), 0o644)

	cfg := DefaultDryRunConfig(tempDir)
	cfg.CleanReset = true
	ring := engine.NewSPSCRing(128)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)

	dryRun, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create engine with CleanReset: %v", err)
	}

	st := dryRun.State()
	if st.TotalTrades != 0 || st.WinningTrades != 0 || st.LosingTrades != 0 {
		t.Errorf("expected trade counts to be 0, got Total=%d, Win=%d, Loss=%d", st.TotalTrades, st.WinningTrades, st.LosingTrades)
	}
	if st.WinRate != 100.0 {
		t.Errorf("expected WinRate 100.0, got %f", st.WinRate)
	}
	if st.RealizedPnL != 0.0 {
		t.Errorf("expected RealizedPnL 0.0, got %f", st.RealizedPnL)
	}
	if st.Cash != 200.0 || st.Equity != 200.0 {
		t.Errorf("expected Cash and Equity 200.0, got Cash=%f, Equity=%f", st.Cash, st.Equity)
	}
	if len(st.Positions) != 0 {
		t.Errorf("expected Positions to be empty after CleanReset, got %d positions", len(st.Positions))
	}
	if _, err := os.Stat(journalPath + ".legacy"); err != nil {
		t.Errorf("expected legacy journal backup file to exist: %v", err)
	}
}

func TestAdaptiveMicrostructureFormulasAndProfitDecayRatchet(t *testing.T) {
	tempDir := t.TempDir()
	cfg := DefaultDryRunConfig(tempDir)
	cfg.OrderLatency = 5 * time.Millisecond          // low-latency (HFT) regime: entry offsets use the 1.0x profile
	cfg.BuyThreshold, cfg.SellThreshold = 0.40, 0.60 // quoting-mechanics test: permissive entry gate (default 0.65/0.35 rejects neutral-conviction opens)
	cfg.EntryOffsetBps = 7.5
	cfg.TargetProfitBps = 12.5

	ring := engine.NewSPSCRing(128)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)
	engine, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	now := time.Now()
	// Converge the latency EWMA into the low-latency (HFT) regime so offsets use the 1.0x profile
	for i := 0; i < 60; i++ {
		engine.quantOrch.RecordLatencyUpdate(5*time.Millisecond, 0, now)
	}

	// 1. Test Volatility-Adaptive Entry Offset:
	// BTC should receive 0.90x tighter spread, ETH should receive 1.15x wider spread
	engine.evaluateQuotesAndGating("BTCUSDT", 75000.00, now)
	engine.evaluateQuotesAndGating("ETHUSDT", 2200.00, now)

	st := engine.State()
	btcOrders := st.ActiveOrders["BTCUSDT"]
	ethOrders := st.ActiveOrders["ETHUSDT"]

	if len(btcOrders) < 2 || len(ethOrders) < 2 {
		t.Fatalf("expected active 2-sided quotes for BTC and ETH")
	}

	// BTC expected entry spread: 7.5 * 0.90 = 6.75 bps
	// BTC Bid = 75000 * (1 - 6.75*1e-4) = 74949.375 -> floor to 74949.37
	var btcBid *ActiveMakerOrder
	for _, o := range btcOrders {
		if o.Side == "BUY" {
			btcBid = &o
			break
		}
	}
	if btcBid == nil {
		t.Fatalf("missing BTC bid")
	}
	expectedBtcBid := 75000.00 * (1.0 - 6.75*1e-4)
	if math.Abs(btcBid.Price-expectedBtcBid) > 1.0 {
		t.Errorf("expected BTC bid ~%f (6.75 bps), got %f", expectedBtcBid, btcBid.Price)
	}

	// ETH expected entry spread: 7.5 * 1.15 = 8.625 bps
	// ETH Bid = 2200 * (1 - 8.625*1e-4) = 2198.1025 -> floor to 2198.10
	var ethBid *ActiveMakerOrder
	for _, o := range ethOrders {
		if o.Side == "BUY" {
			ethBid = &o
			break
		}
	}
	if ethBid == nil {
		t.Fatalf("missing ETH bid")
	}
	expectedEthBid := 2200.00 * (1.0 - 8.625*1e-4)
	if math.Abs(ethBid.Price-expectedEthBid) > 0.10 {
		t.Errorf("expected ETH bid ~%f (8.625 bps), got %f", expectedEthBid, ethBid.Price)
	}

	// 2. Test Dynamic Profit Decay Ratchet:
	// Establish LONG ETH position at $2200.00
	engine.mu.Lock()
	engine.state.Positions["ETHUSDT"] = &Position{
		Symbol:        "ETHUSDT",
		Size:          0.020,
		AvgEntryPrice: 2200.00,
		MarkPrice:     2200.00,
	}
	sm := engine.symMicro["ETHUSDT"]
	sm.InventorySince = now.Add(-1800 * time.Second) // 30 minutes of holding
	engine.mu.Unlock()

	// Evaluate after 30m of holding:
	// decayFactor = exp(-1800 / 900) = exp(-2) ≈ 0.1353
	// floor = 0.80 * 12.5 = 10.0 bps (TargetFloorFrac); targetProfit = 10 + 2.5 * 0.1353 ≈ 10.34 bps net
	// minProfitAsk = 2200 * (1 + (3.0 + 10.34)*1e-4) ≈ 2202.93
	engine.evaluateQuotesAndGating("ETHUSDT", 2200.00, now)
	stDecay := engine.State()
	var exitAsk *ActiveMakerOrder
	for _, o := range stDecay.ActiveOrders["ETHUSDT"] {
		if o.Side == "SELL" && o.CancelingUntil.IsZero() {
			exitAsk = &o
			break
		}
	}
	if exitAsk == nil {
		t.Fatalf("missing ETH exit ask after 30m holding")
	}

	// Net edge must be >= the 10.0 bps floor and have decayed well below the 12.5 bps base target
	netEdgeBps := (exitAsk.Price-2200.00)/2200.00*1e4 - 3.0 // subtracting 3.0 bps fee
	if netEdgeBps < 10.0 {
		t.Errorf("expected net edge >= 10.0 bps floor, got %f", netEdgeBps)
	}
	if netEdgeBps > 11.0 {
		t.Errorf("expected net edge to have decayed towards floor (< 11.0 bps), got %f", netEdgeBps)
	}
}

func TestSelfImprovingScalpingDefenseAndRecalibration(t *testing.T) {
	tempDir := t.TempDir()
	cfg := DefaultDryRunConfig(tempDir)
	cfg.BuyThreshold, cfg.SellThreshold = 0.40, 0.60 // quoting-mechanics test: permissive entry gate (default 0.65/0.35 rejects neutral-conviction opens)
	cfg.EntryOffsetBps = 7.5
	cfg.TargetProfitBps = 12.5

	ring := engine.NewSPSCRing(128)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)
	engine, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	recalibratedCh := make(chan string, 1)
	engine.SetAutonomousRecalibrator(func(ctx context.Context, symbol string, currentCapital float64, dataDir string) error {
		// Mock backtest recalibration: write optimal_params.json
		optConfig := map[string]any{
			"symbol":              symbol,
			"gamma":               0.0025,
			"min_half_spread_bps": 3.50,
			"clip_fraction":       0.15,
			"base_leverage":       1.50,
			"max_leverage_cap":    2.50,
		}
		raw, _ := json.Marshal(optConfig)
		_ = os.WriteFile(filepath.Join(tempDir, "optimal_params.json"), raw, 0644)
		recalibratedCh <- symbol
		return nil
	})

	now := time.Now()

	// 1. Initial State: Scalping should be active, normal tight spread
	sm := engine.symMicro["BTCUSDT"]
	if sm.ScalpingDisabled {
		t.Errorf("expected scalping to be enabled initially")
	}

	// 2. Simulate 3 consecutive closing losses to trigger money leakage defense
	engine.mu.Lock()
	engine.state.Positions["BTCUSDT"] = &Position{
		Symbol:        "BTCUSDT",
		Size:          0.001,
		AvgEntryPrice: 75000.00,
		MarkPrice:     74950.00,
	}
	// Execute closing sell at a loss
	engine.executeMakerFillLocked("BTCUSDT", 74900.00, 0.001, "SELL", 5.0, "TEST")

	engine.state.Positions["BTCUSDT"] = &Position{
		Symbol:        "BTCUSDT",
		Size:          0.001,
		AvgEntryPrice: 75000.00,
		MarkPrice:     74950.00,
	}
	engine.executeMakerFillLocked("BTCUSDT", 74900.00, 0.001, "SELL", 5.0, "TEST")

	engine.state.Positions["BTCUSDT"] = &Position{
		Symbol:        "BTCUSDT",
		Size:          0.001,
		AvgEntryPrice: 75000.00,
		MarkPrice:     74950.00,
	}
	engine.executeMakerFillLocked("BTCUSDT", 74900.00, 0.001, "SELL", 5.0, "TEST")
	engine.mu.Unlock()

	// 3. Verify Scalping is now DISABLED
	if !sm.ScalpingDisabled {
		t.Fatalf("expected scalping to be disabled after 3 consecutive losses, but got false")
	}
	if sm.ConsecutiveLosses != 3 {
		t.Errorf("expected 3 consecutive losses, got %d", sm.ConsecutiveLosses)
	}

	// 4. Verify quotes are widened to Patient Swing mode (>= 18.0 bps)
	engine.evaluateQuotesAndGating("BTCUSDT", 75000.00, now)
	st := engine.State()
	orders := st.ActiveOrders["BTCUSDT"]
	if len(orders) == 0 {
		t.Fatalf("expected resting orders for BTCUSDT")
	}
	for _, o := range orders {
		offsetBps := math.Abs(o.Price-75000.00) / 75000.00 * 1e4
		if offsetBps < 17.9 {
			t.Errorf("expected patient swing spread >= 18.0 bps when scalping disabled, got %.2f bps", offsetBps)
		}
	}

	// 5. Verify autonomous recalibrator was triggered and hot-reloads new parameters
	select {
	case sym := <-recalibratedCh:
		if sym != "BTCUSDT" {
			t.Errorf("expected recalibration for BTCUSDT, got %s", sym)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for autonomous recalibration to be triggered")
	}

	// Wait briefly for hot reload
	time.Sleep(100 * time.Millisecond)
	engine.maybeReloadCloudParams()

	if engine.minHalfSpreadFloorBps != 3.50 {
		t.Errorf("expected hot-reloaded minHalfSpreadFloorBps 3.50, got %.2f", engine.minHalfSpreadFloorBps)
	}

	// 6. Test Extreme Leakage Guard (ConsecutiveLosses >= 4): opening entries must be completely halted!
	sm.ConsecutiveLosses = 4
	engine.evaluateQuotesAndGating("BTCUSDT", 75000.00, now.Add(time.Second))
	stHalt := engine.State()
	activeOrders := stHalt.ActiveOrders["BTCUSDT"]
	// Should contain no new opening quotes (only canceling existing)
	var activeOpen int
	for _, o := range activeOrders {
		if o.CancelingUntil.IsZero() {
			activeOpen++
		}
	}
	if activeOpen > 0 {
		t.Errorf("expected 0 active opening quotes when ConsecutiveLosses >= 4, got %d", activeOpen)
	}
}

func TestCoinbaseLeadLagOracleIntegrationInDryRun(t *testing.T) {
	tempDir := t.TempDir()
	cfg := DefaultDryRunConfig(tempDir)
	ring := engine.NewSPSCRing(1024)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)

	dryRun, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create dry run engine: %v", err)
	}

	cbOracle := NewCoinbaseSignalOracle([]string{"BTCUSDT"})
	dryRun.SetCoinbaseOracle(cbOracle)

	now := time.Now()

	// 1. Initial State: Coinbase is aligned with Binance at 75000.00
	cbOracle.UpdateTick("BTCUSDT", 75000.00, 74999.00, 75001.00, 0.5, true, now.Add(-1*time.Second))
	dryRun.evaluateQuotesAndGating("BTCUSDT", 75000.00, now)

	st1 := dryRun.State()
	if st1.Coinbase == nil {
		t.Fatalf("expected Coinbase signal state in dry run snapshot")
	}
	if btcSig := st1.Coinbase.Symbols["BTCUSDT"]; btcSig == nil || btcSig.CoinbasePrice != 75000.00 {
		t.Errorf("expected btcSig with 75000.00, got %+v", btcSig)
	}

	// 2. Simulate US Institutional Bullish Surge on Coinbase:
	// Coinbase leaps from 75000 to 75040 (+5.3 bps basis surge) with high positive velocity
	cbOracle.UpdateTick("BTCUSDT", 75040.00, 75039.00, 75041.00, 2.0, true, now.Add(200*time.Millisecond))

	// Binance mid price is still 75000.00
	dryRun.evaluateQuotesAndGating("BTCUSDT", 75000.00, now.Add(250*time.Millisecond))
	st2 := dryRun.State()
	btcSig2 := st2.Coinbase.Symbols["BTCUSDT"]
	if btcSig2 == nil {
		t.Fatalf("expected btcSig2 in snapshot")
	}
	if btcSig2.BasisBps <= 0 {
		t.Errorf("expected positive BasisBps on Coinbase surge, got %f", btcSig2.BasisBps)
	}
	if !btcSig2.IsAdverseAsk {
		t.Errorf("expected IsAdverseAsk true during strong Coinbase upward surge, got false")
	}

	// Resting sell opening orders must be canceled or barred from placement during adverse ask surge!
	orders := st2.ActiveOrders["BTCUSDT"]
	for _, o := range orders {
		if o.Side == "SELL" && o.CancelingUntil.IsZero() {
			t.Errorf("expected active SELL opening quote to be canceled or absent when IsAdverseAsk is true")
		}
	}
}

func TestUSDCMarketMakingAndVolatilityAdaptiveQuoting(t *testing.T) {
	// 1. Verify tick sizes for USDC pairs
	if tick := symbolTickSize("BTCUSDC"); tick != 0.10 {
		t.Errorf("expected BTCUSDC tick size 0.10, got %f", tick)
	}
	if tick := symbolTickSize("ETHUSDC"); tick != 0.01 {
		t.Errorf("expected ETHUSDC tick size 0.01, got %f", tick)
	}
	if tick := symbolTickSize("SOLUSDC"); tick != 0.01 {
		t.Errorf("expected SOLUSDC tick size 0.01, got %f", tick)
	}

	// 2. Verify dynamic Binance stream URL builder for USDC pairs
	streamURL := BuildBinanceStreamURL([]string{"BTCUSDC", "ETHUSDC"})
	if !strings.Contains(streamURL, "btcusdc@depth20@100ms") || !strings.Contains(streamURL, "ethusdc@trade") {
		t.Errorf("expected USDC streams in URL, got %s", streamURL)
	}

	// 3. Test USDC market making quote placement with volatility adaptation
	tempDir := t.TempDir()
	cfg := DefaultDryRunConfig(tempDir)
	cfg.BuyThreshold, cfg.SellThreshold = 0.40, 0.60 // quoting-mechanics test: permissive entry gate (default 0.65/0.35 rejects neutral-conviction opens)
	cfg.Symbols = []string{"BTCUSDC", "ETHUSDC"}
	cfg.EntryOffsetBps = 7.5
	ring := engine.NewSPSCRing(1024)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)

	dryRun, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create dry run engine: %v", err)
	}

	cbOracle := NewCoinbaseSignalOracle([]string{"BTCUSDC"})
	dryRun.SetCoinbaseOracle(cbOracle)

	now := time.Now()
	cbOracle.UpdateTick("BTCUSDT", 75000.00, 74999.00, 75001.00, 1.0, true, now)

	// Simulate high volatility variance on BTCUSDC (0.80 VolVarBps vs 0.40 baseline)
	sm := dryRun.symMicro["BTCUSDC"]
	sm.VolVarBps = 0.80

	dryRun.evaluateQuotesAndGating("BTCUSDC", 75000.00, now)

	st := dryRun.State()
	orders := st.ActiveOrders["BTCUSDC"]
	if len(orders) == 0 {
		t.Fatalf("expected active orders for BTCUSDC")
	}

	for _, o := range orders {
		offsetBps := math.Abs(o.Price-75000.00) / 75000.00 * 1e4
		// In USDC with 0.80 vol: effectiveEntry = 7.5 * 0.90 * 1.25 * sqrt(0.80/0.40) ≈ 11.9 bps
		if offsetBps < 10.0 {
			t.Errorf("expected wide volatility-adaptive quote offset >= 10.0 bps for volatile USDC pair, got %.2f bps", offsetBps)
		}
	}

	// 4. Test accelerated inventory dampening under volatility spike
	dryRun.mu.Lock()
	dryRun.state.Positions["BTCUSDC"] = &Position{
		Symbol:        "BTCUSDC",
		Size:          0.001,
		AvgEntryPrice: 75000.00,
		MarkPrice:     75000.00,
	}
	dryRun.mu.Unlock()

	dryRun.evaluateQuotesAndGating("BTCUSDC", 75000.00, now.Add(time.Second))
	stInv := dryRun.State()
	invOrders := stInv.ActiveOrders["BTCUSDC"]
	var sellQuote *ActiveMakerOrder
	for _, o := range invOrders {
		if o.Side == "SELL" && o.CancelingUntil.IsZero() {
			sellQuote = &o
			break
		}
	}
	if sellQuote == nil {
		t.Fatalf("expected resting inventory closing SELL quote for BTCUSDC")
	}
	// Holding long inventory: sell order size must match open inventory (0.001) to return to cash
	if math.Abs(sellQuote.Size-0.001) > 1e-6 {
		t.Errorf("expected inventory exit size 0.001, got %f", sellQuote.Size)
	}
}

func TestAgileZeroFeeMakerQuotingAndTrailingProfitRatchet(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.BuyThreshold, cfg.SellThreshold = 0.40, 0.60 // quoting-mechanics test: permissive entry gate (default 0.65/0.35 rejects neutral-conviction opens)
	cfg.ZeroFeeMaker = true
	cfg.SpotOnly = true
	cfg.EntryOffsetBps = 1.75
	cfg.TargetProfitBps = 14.0
	cfg.ClipFraction = 0.45
	cfg.Symbols = []string{"BTCUSDT", "ETHUSDT"}

	ring := engine.NewSPSCRing(1024)
	streamCfg := DefaultStreamConfig()
	streamCfg.Symbols = cfg.Symbols
	stream := NewMarketStreamIngestor(streamCfg, ring)

	dryRun, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create dry run engine: %v", err)
	}

	// 1. Verify maker fee differentiation
	if btcFee := dryRun.makerFeeBps("BTCUSDT"); btcFee != 0.0 {
		t.Errorf("expected 0.0 bps maker fee for BTCUSDT under ZeroFeeMaker, got %.2f", btcFee)
	}
	if ethFee := dryRun.makerFeeBps("ETHUSDT"); ethFee != 1.5 {
		t.Errorf("expected 1.5 bps maker fee for ETHUSDT, got %.2f", ethFee)
	}

	// 2. Test agile entry quoting on zero-fee BTCUSDT
	now := time.Now()
	dryRun.evaluateQuotesAndGating("BTCUSDT", 75000.00, now)

	st := dryRun.State()
	orders := st.ActiveOrders["BTCUSDT"]
	if len(orders) == 0 {
		t.Fatalf("expected active orders for BTCUSDT")
	}
	var buyQuote *ActiveMakerOrder
	for _, o := range orders {
		if o.Side == "BUY" {
			buyQuote = &o
			break
		}
	}
	if buyQuote == nil {
		t.Fatalf("expected resting BUY quote for BTCUSDT")
	}
	bidOffsetBps := (75000.00 - buyQuote.Price) / 75000.00 * 1e4
	// For BTCUSDT with entryOffset 1.75 * 0.90 = 1.575 bps, offset should be around 1.5 - 2.5 bps, NOT 6.2 bps!
	if bidOffsetBps > 3.0 {
		t.Errorf("expected agile quote offset <= 3.0 bps for zero-fee BTC, got %.2f bps", bidOffsetBps)
	}

	// 3. Test dynamic trailing profit ratchet on exit
	// Inject a long position at 75000.00
	dryRun.mu.Lock()
	dryRun.state.Positions["BTCUSDT"] = &Position{
		Symbol:        "BTCUSDT",
		Size:          0.005,
		AvgEntryPrice: 75000.00,
		MarkPrice:     75200.00,
	}
	dryRun.mu.Unlock()

	sm := dryRun.symMicro["BTCUSDT"]
	sm.Ret5sBps = 3.5

	dryRun.evaluateQuotesAndGating("BTCUSDT", 75200.00, now.Add(2*time.Second))
	stRally := dryRun.State()
	rallyOrders := stRally.ActiveOrders["BTCUSDT"]
	var exitAsk *ActiveMakerOrder
	for _, o := range rallyOrders {
		if o.Side == "SELL" && o.CancelingUntil.IsZero() {
			exitAsk = &o
			break
		}
	}
	if exitAsk == nil {
		t.Fatalf("expected resting SELL exit quote for BTCUSDT")
	}
	// Exit quote should be at or above min profit (entry 75000 * (1 + 14 bps) = 75105)
	if exitAsk.Price < 75105.00 {
		t.Errorf("expected exit quote >= min profit price 75105, got %.2f", exitAsk.Price)
	}
}

func TestPageHinkleyConceptDriftQuoteWidening(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.BuyThreshold, cfg.SellThreshold = 0.40, 0.60 // quoting-mechanics test: permissive entry gate (default 0.65/0.35 rejects neutral-conviction opens)
	cfg.Symbols = []string{"BTCUSDT"}

	ring := engine.NewSPSCRing(1024)
	streamCfg := DefaultStreamConfig()
	streamCfg.Symbols = cfg.Symbols
	stream := NewMarketStreamIngestor(streamCfg, ring)

	dryRun, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create dry run engine: %v", err)
	}

	now := time.Now()
	// Warm up drift guard with normal small residuals
	for i := 0; i < 20; i++ {
		now = now.Add(100 * time.Millisecond)
		dryRun.evaluateQuotesAndGating("BTCUSDT", 75000.00+float64(i)*0.01, now)
	}

	// Verify quote spread under calm conditions
	st0 := dryRun.State()
	orders0 := st0.ActiveOrders["BTCUSDT"]
	if len(orders0) == 0 {
		t.Fatalf("expected orders")
	}
	var calmBuy *ActiveMakerOrder
	for _, o := range orders0 {
		if o.Side == "BUY" {
			calmBuy = &o
			break
		}
	}
	if calmBuy == nil {
		t.Fatalf("expected calm buy quote")
	}

	// Now simulate structural concept drift / high residual shock
	for i := 0; i < 25; i++ {
		now = now.Add(100 * time.Millisecond)
		// Large unexpected price jump that violates historical model prediction
		dryRun.evaluateQuotesAndGating("BTCUSDT", 75000.00+float64(i)*25.0, now)
	}

	stDrift := dryRun.State()
	ordersDrift := stDrift.ActiveOrders["BTCUSDT"]
	var driftBuy *ActiveMakerOrder
	for _, o := range ordersDrift {
		if o.Side == "BUY" && o.CancelingUntil.IsZero() {
			driftBuy = &o
			break
		}
	}
	if driftBuy != nil {
		// Buy edge during drift must be widened to defend against adverse selection
		if driftBuy.EdgeBps < calmBuy.EdgeBps {
			t.Errorf("expected drift quote edge >= calm quote edge, got %v vs %v", driftBuy.EdgeBps, calmBuy.EdgeBps)
		}
	}
}

func TestQuantHedgeFundAlphaAndVolatilityKellySizing(t *testing.T) {
	tempDir := t.TempDir()
	cfg := DefaultDryRunConfig(tempDir)
	cfg.TargetProfitBps = 50.0 // Quant hedge fund swing target
	cfg.StopLossBps = 60.0
	cfg.EntryOffsetBps = 2.5

	ring := engine.NewSPSCRing(128)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)
	eng, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	// 1. Verify Volatility-Targeted Kelly Position Sizing
	clipCalm, maxCalm := eng.leverageCtrl.ComputeVolatilityKellySize("BTCUSDT", 200.0, 75000.0, 35.0, 60.0, 1)
	clipVolatile, maxVolatile := eng.leverageCtrl.ComputeVolatilityKellySize("BTCUSDT", 200.0, 75000.0, 90.0, 60.0, 1)

	if clipCalm <= 0 || maxCalm <= 0 {
		t.Fatalf("expected positive calm clip and max allocation, got clip=%f max=%f", clipCalm, maxCalm)
	}
	if clipVolatile <= 0 || maxVolatile <= 0 {
		t.Fatalf("expected positive volatile clip and max allocation, got clip=%f max=%f", clipVolatile, maxVolatile)
	}
	// Under elevated volatility (90 bps ATR vs 35 bps baseline), inverse volatility scaling MUST downsize clip
	if clipVolatile > clipCalm {
		t.Errorf("expected volatile clip (%f) <= calm clip (%f) under inverse ATR scaling", clipVolatile, clipCalm)
	}

	now := time.Now()

	// 2. Verify Dynamic OBI-Conditioned Entry Thresholds
	// Simulate order book with thin bids (OBI = -0.35)
	eng.mu.Lock()
	sm := eng.symMicro["BTCUSDT"]
	if sm == nil {
		sm = newSymbolMicrostructure("BTCUSDT", 0.25)
		eng.symMicro["BTCUSDT"] = sm
	}
	sm.OBI = -0.35
	sm.Ret5mBps = -12.0
	sm.Conviction = 0.67 // Conviction (0.67) exceeds base 0.65, but is BELOW thin-bid threshold 0.72!
	sm.ConvictionOverride = 0.67
	eng.mu.Unlock()

	eng.evaluateQuotesAndGating("BTCUSDT", 75000.00, now)

	st := eng.State()
	orders := st.ActiveOrders["BTCUSDT"]
	for _, o := range orders {
		if o.Side == "BUY" && o.CancelingUntil.IsZero() {
			t.Fatalf("thin bid depth (OBI=-0.35) with sub-0.72 conviction should NOT place BUY quote, got: %+v", o)
		}
	}

	// Now provide high conviction (0.75 >= 0.72) with thin bids: BUY quote should be allowed!
	eng.mu.Lock()
	sm.Conviction = 0.75
	sm.ConvictionOverride = 0.75
	sm.Ret5mBps = 5.0
	sm.Ret1mBps = 4.0
	eng.mu.Unlock()

	eng.evaluateQuotesAndGating("BTCUSDT", 75000.00, now.Add(500*time.Millisecond))
	stHigh := eng.State()
	hasHighConvictionBuy := false
	for _, o := range stHigh.ActiveOrders["BTCUSDT"] {
		if o.Side == "BUY" && o.CancelingUntil.IsZero() {
			hasHighConvictionBuy = true
			break
		}
	}
	if !hasHighConvictionBuy {
		t.Fatalf("expected BUY quote placed with high conviction (0.75 >= 0.72)")
	}

	// 3. Verify Asymmetric Hysteresis Exit & Patient Swing Profit Ratchet
	// Establish LONG position at $75,000
	eng.mu.Lock()
	eng.state.Positions["BTCUSDT"] = &Position{
		Symbol:        "BTCUSDT",
		Size:          clipCalm,
		AvgEntryPrice: 75000.00,
		MarkPrice:     75000.00,
	}
	sm.InventorySince = now
	sm.Conviction = 0.52 // Moderate neutral conviction: must NOT exit on noise!
	sm.ConvictionOverride = 0.52
	sm.OBI = 0.0
	sm.Ret5mBps = 0.0
	eng.mu.Unlock()

	// Price drops slightly by 10 bps (normal consolidation noise):
	eng.evaluateQuotesAndGating("BTCUSDT", 74925.00, now.Add(10*time.Second))
	stNoise := eng.State()
	for _, o := range stNoise.ActiveOrders["BTCUSDT"] {
		if o.Side == "SELL" && o.CancelingUntil.IsZero() {
			// Must quote at full target profit (+50 bps), NOT panic selling at inside touch
			if o.Price < 75000.00*(1.0+0.0025) {
				t.Errorf("noise dip should hold for profit target (>= +25 bps gross), got sell price %f vs entry %f", o.Price, 75000.00)
			}
		}
	}

	// Now simulate decisive alpha invalidation:
	// Adverse drawdown 25 bps, Conviction collapsed to 0.32 (< 0.38), OBI < -0.25, Ret5mBps < -10.0
	eng.mu.Lock()
	sm.Conviction = 0.32
	sm.ConvictionOverride = 0.32
	sm.OBI = -0.30
	sm.Ret5mBps = -15.0
	eng.mu.Unlock()

	eng.evaluateQuotesAndGating("BTCUSDT", 74812.50, now.Add(20*time.Second))
	stInvalid := eng.State()
	hasInvalidationExit := false
	for _, o := range stInvalid.ActiveOrders["BTCUSDT"] {
		if o.Side == "SELL" && o.CancelingUntil.IsZero() {
			hasInvalidationExit = true
			break
		}
	}
	if !hasInvalidationExit {
		t.Fatalf("expected invalidation exit SELL quote placed upon decisive alpha breakdown")
	}
}

func TestTribecaTargetInventoryAndFairValueIntegration(t *testing.T) {
	tempDir := t.TempDir()
	cfg := DefaultDryRunConfig(tempDir)
	cfg.OrderLatency = 10 * time.Millisecond

	ring := engine.NewSPSCRing(128)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)
	eng, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	now := time.Now()
	eng.mu.Lock()
	sm := eng.symMicro["BTCUSDT"]
	if sm == nil {
		sm = newSymbolMicrostructure("BTCUSDT", 0.25)
		eng.symMicro["BTCUSDT"] = sm
	}
	sm.Conviction = 0.70 // Bullish directional conviction
	sm.ConvictionOverride = 0.70
	eng.mu.Unlock()

	eng.evaluateQuotesAndGating("BTCUSDT", 75000.00, now)

	st := eng.State()
	orders := st.ActiveOrders["BTCUSDT"]
	if len(orders) == 0 {
		t.Fatalf("expected active orders for BTCUSDT")
	}

	// Verify FairValue and TargetInventory populated on orders
	var buyOrder *ActiveMakerOrder
	for _, o := range orders {
		if o.Side == "BUY" {
			buyOrder = &o
			break
		}
	}
	if buyOrder == nil {
		t.Fatalf("expected active BUY order")
	}
	if buyOrder.FairValue <= 0 {
		t.Errorf("expected positive FairValue, got %f", buyOrder.FairValue)
	}
	if buyOrder.TargetInventory <= 0 {
		t.Errorf("expected positive TargetInventory for bullish conviction 0.70, got %f", buyOrder.TargetInventory)
	}

	// Verify ScalpingState contains TargetInventory and FairValue
	scalpSt, ok := st.ScalpingState["BTCUSDT"]
	if !ok {
		t.Fatalf("missing ScalpingState for BTCUSDT")
	}
	if scalpSt.TargetInventory <= 0 {
		t.Errorf("expected positive TargetInventory in ScalpingState, got %f", scalpSt.TargetInventory)
	}
	if scalpSt.FairValue <= 0 {
		t.Errorf("expected positive FairValue in ScalpingState, got %f", scalpSt.FairValue)
	}
}

// A failed recalibration must back off for recalibFailureBackoff instead of
// retrying on the 3-minute cooldown, and must not report HOT_RELOADED.
func TestAutoRecalibrationFailureBacksOff(t *testing.T) {
	cfg := DefaultDryRunConfig(t.TempDir())
	ring := engine.NewSPSCRing(128)
	eng, err := NewDryRunEngine(cfg, ring, NewMarketStreamIngestor(DefaultStreamConfig(), ring))
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	calls := make(chan struct{}, 4)
	eng.SetAutonomousRecalibrator(func(ctx context.Context, symbol string, c float64, d string) error {
		calls <- struct{}{}
		return errors.New("no tick data")
	})

	eng.mu.Lock()
	eng.maybeTriggerAutoRecalibrationLocked("BTCUSDT")
	eng.mu.Unlock()
	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("recalibrator was not called")
	}
	deadline := time.Now().Add(2 * time.Second)
	for eng.recalibrating.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	eng.mu.Lock()
	status := eng.state.SelfAdaptation.Status
	backoff := eng.recalibBackoff
	eng.lastAutoRecalib = time.Now().Add(-10 * time.Minute) // 3-minute cooldown has passed
	eng.maybeTriggerAutoRecalibrationLocked("BTCUSDT")
	eng.mu.Unlock()

	if !strings.HasPrefix(status, "RECALIB_ERROR") {
		t.Errorf("expected RECALIB_ERROR status, got %q", status)
	}
	if time.Until(backoff) < recalibFailureBackoff-time.Minute {
		t.Errorf("expected backoff ~%s, got %s", recalibFailureBackoff, time.Until(backoff))
	}
	select {
	case <-calls:
		t.Error("recalibrator retried during failure backoff")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRunJournalWriterBatchSync(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.Symbols = []string{"BTCUSDT"}
	ring := engine.NewSPSCRing(1024)
	streamCfg := DefaultStreamConfig()
	streamCfg.Symbols = cfg.Symbols
	stream := NewMarketStreamIngestor(streamCfg, ring)

	eng, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("NewDryRunEngine: %v", err)
	}
	defer func() {
		if eng.journalCh != nil {
			close(eng.journalCh)
			eng.journalWg.Wait()
		}
	}()

	// Queue a burst of 5 records and verify they are synced immediately (<500ms, no 2s ticker wait)
	eng.mu.Lock()
	for i := 0; i < 5; i++ {
		eng.appendTradeRecord(TradeRecord{
			Timestamp: time.Now().UTC(),
			Symbol:    "BTCUSDT",
			Side:      "BUY",
			Role:      "MAKER",
			Price:     50000,
			Size:      0.01,
			ValueUSDT: 500,
		})
	}
	eng.mu.Unlock()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if eng.journalSyncCount.Load() >= 1 {
			data, rerr := os.ReadFile(cfg.JournalFile)
			if rerr == nil && strings.Count(string(data), "\n") == 5 {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	data, _ := os.ReadFile(cfg.JournalFile)
	t.Fatalf("expected 5 synced journal lines within 500ms, syncs=%d lines=%d", eng.journalSyncCount.Load(), strings.Count(string(data), "\n"))
}

func TestRecomputeEquityLockedAcrossFillsAndFunding(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.InitialCapital = 10000.0
	cfg.TaxRatePct = 35.0
	cfg.MakerFeeBps = 2.0
	cfg.TakerFeeBps = 5.0
	cfg.FundingBpsPerHour = 10.0
	cfg.Symbols = []string{"BTCUSDT"}

	ring := engine.NewSPSCRing(1024)
	streamCfg := DefaultStreamConfig()
	streamCfg.Symbols = cfg.Symbols
	stream := NewMarketStreamIngestor(streamCfg, ring)

	eng, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("NewDryRunEngine: %v", err)
	}
	defer func() {
		if eng.journalCh != nil {
			close(eng.journalCh)
			eng.journalWg.Wait()
		}
	}()

	eng.mu.Lock()
	pos := eng.state.Positions["BTCUSDT"]
	pos.MarkPrice = 51000.0
	sm := eng.getOrCreateSymbolMicro("BTCUSDT")
	sm.InventorySince = time.Now().Add(-5 * time.Minute)

	// 1. Maker buy opens long 1.0 @ 50000 while MarkPrice=51000 -> UnrealizedPnL=1000
	eng.executeMakerFillLocked("BTCUSDT", 50000.0, 1.0, "BUY", 1.0, "test")
	if math.Abs(eng.state.UnrealizedPnL-1000.0) > 1e-6 {
		t.Fatalf("expected UnrealizedPnL=1000, got %.6f", eng.state.UnrealizedPnL)
	}
	if math.Abs(eng.state.Equity-(eng.state.Cash+1000.0)) > 1e-6 {
		t.Fatalf("expected Equity=Cash+1000, got %.6f vs %.6f", eng.state.Equity, eng.state.Cash+1000.0)
	}

	// 2. Maker sell crosses zero (1.0 -> -0.5) -> InventorySince must reset
	eng.executeMakerFillLocked("BTCUSDT", 51000.0, 1.5, "SELL", 1.0, "test")
	if !sm.InventorySince.IsZero() {
		t.Fatalf("expected InventorySince reset on zero-cross, got %v", sm.InventorySince)
	}
	if eng.state.EstimatedTaxUSDT <= 0 {
		t.Fatalf("expected positive EstimatedTaxUSDT after profitable close, got %.6f", eng.state.EstimatedTaxUSDT)
	}

	// 3. Accrue funding updates Equity and Tax
	sm.InventorySince = time.Now().Add(-time.Minute)
	eng.lastFundingAccrual = time.Now().Add(-time.Hour)
	eqBeforeFunding := eng.state.Equity
	eng.accrueFundingLocked(time.Now())
	if eng.state.Equity >= eqBeforeFunding {
		t.Fatalf("expected Equity to decrease after funding accrual: before=%.6f after=%.6f", eqBeforeFunding, eng.state.Equity)
	}

	// 4. Taker exit closes remaining short -> UnrealizedPnL=0, Equity==Cash
	eng.takerExitLocked("BTCUSDT", 50990.0, 51010.0, 51000.0, "STOP_LOSS")
	if eng.state.UnrealizedPnL != 0 {
		t.Fatalf("expected UnrealizedPnL=0 after takerExitLocked, got %.6f", eng.state.UnrealizedPnL)
	}
	if math.Abs(eng.state.Equity-eng.state.Cash) > 1e-9 {
		t.Fatalf("expected Equity==Cash after close, got %.6f vs %.6f", eng.state.Equity, eng.state.Cash)
	}

	// 5. Simulate taker fill updates Equity/UnrealizedPnL and resets InventorySince when hitting zero
	pos.MarkPrice = 50000.0
	eng.simulateTakerFillLocked("BTCUSDT", 50000.0, 5.0)
	if math.Abs(eng.state.Equity-(eng.state.Cash+eng.state.UnrealizedPnL)) > 1e-6 {
		t.Fatalf("expected Equity=Cash+UnrealizedPnL after simulateTakerFillLocked")
	}
	eng.mu.Unlock()
}

func TestRecoverStateFromJournalProRataAndScratch(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.InitialCapital = 10000.0
	cfg.Symbols = []string{"BTCUSDT"}

	ring := engine.NewSPSCRing(1024)
	streamCfg := DefaultStreamConfig()
	streamCfg.Symbols = cfg.Symbols
	stream := NewMarketStreamIngestor(streamCfg, ring)

	eng, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("NewDryRunEngine: %v", err)
	}
	if eng.journalCh != nil {
		close(eng.journalCh)
		eng.journalWg.Wait()
		eng.journalCh = nil
	}

	t0 := time.Now().Add(-10 * time.Minute).UTC()
	recs := []TradeRecord{
		// Open long 2.0 @ 100, fee=2.0
		{Timestamp: t0, Symbol: "BTCUSDT", Side: "BUY", Role: "MAKER", Price: 100, Size: 2.0, ValueUSDT: 200, FeeUSDT: 2.0, PnLRealized: -2.0, JournalSeq: 1},
		// Close 1.0 @ 102, fee=1.0 -> gross=2.0, net=1.0, entryShare=1.0 -> roundTrip=0.0 (scratch!)
		{Timestamp: t0.Add(time.Minute), Symbol: "BTCUSDT", Side: "SELL", Role: "MAKER", Price: 102, Size: 1.0, ValueUSDT: 102, FeeUSDT: 1.0, PnLRealized: 1.0, JournalSeq: 2},
		// Close remaining 1.0 @ 105, fee=1.0 -> gross=5.0, net=4.0, entryShare=1.0 -> roundTrip=3.0 (win!)
		{Timestamp: t0.Add(2 * time.Minute), Symbol: "BTCUSDT", Side: "SELL", Role: "MAKER", Price: 105, Size: 1.0, ValueUSDT: 105, FeeUSDT: 1.0, PnLRealized: 4.0, JournalSeq: 3},
	}
	var buf strings.Builder
	for _, r := range recs {
		b, _ := json.Marshal(r)
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(cfg.JournalFile, []byte(buf.String()), 0o644); err != nil {
		t.Fatalf("write journal: %v", err)
	}

	st, err := eng.recoverStateFromJournal()
	if err != nil {
		t.Fatalf("recoverStateFromJournal: %v", err)
	}
	if st.WinningTrades != 1 || st.LosingTrades != 0 {
		t.Fatalf("expected 1 win and 0 losses (1 scratch), got wins=%d losses=%d", st.WinningTrades, st.LosingTrades)
	}
	if st.LastJournalSeq != 3 {
		t.Fatalf("expected LastJournalSeq=3, got %d", st.LastJournalSeq)
	}
}

func TestStaleCheckpointReplaysNewerJournalRecords(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.InitialCapital = 10000.0
	cfg.Symbols = []string{"BTCUSDT"}

	t0 := time.Now().Add(-10 * time.Minute).UTC()
	staleState := DryRunState{
		StartTime:            t0,
		TargetEndTime:        t0.Add(14 * 24 * time.Hour),
		LastCheckpoint:       t0.Add(time.Minute),
		InitialCapital:       10000.0,
		Cash:                 10050.0,
		Equity:               10050.0,
		PeakEquity:           10050.0,
		RealizedPnL:          50.0,
		TotalTrades:          1,
		WinningTrades:        1,
		Positions:            map[string]*Position{"BTCUSDT": {Symbol: "BTCUSDT"}},
		LastJournalSeq:       1,
		LastJournalTimestamp: t0.Add(time.Minute),
	}
	stateBytes, _ := json.MarshalIndent(staleState, "", "  ")
	if err := os.MkdirAll(filepath.Dir(cfg.StateFile), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(cfg.StateFile, stateBytes, 0o644); err != nil {
		t.Fatalf("write stale checkpoint: %v", err)
	}

	recs := []TradeRecord{
		{Timestamp: t0.Add(time.Minute), Symbol: "BTCUSDT", Side: "SELL", Role: "MAKER", Price: 100, Size: 1.0, ValueUSDT: 100, FeeUSDT: 0, PnLRealized: 50.0, JournalSeq: 1},
		{Timestamp: t0.Add(2 * time.Minute), Symbol: "BTCUSDT", Side: "BUY", Role: "MAKER", Price: 100, Size: 1.0, ValueUSDT: 100, FeeUSDT: 1.0, PnLRealized: -1.0, JournalSeq: 2},
		{Timestamp: t0.Add(3 * time.Minute), Symbol: "BTCUSDT", Side: "SELL", Role: "MAKER", Price: 120, Size: 1.0, ValueUSDT: 120, FeeUSDT: 1.0, PnLRealized: 19.0, JournalSeq: 3},
	}
	var buf strings.Builder
	for _, r := range recs {
		b, _ := json.Marshal(r)
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(cfg.JournalFile, []byte(buf.String()), 0o644); err != nil {
		t.Fatalf("write journal: %v", err)
	}

	ring := engine.NewSPSCRing(1024)
	streamCfg := DefaultStreamConfig()
	streamCfg.Symbols = cfg.Symbols
	stream := NewMarketStreamIngestor(streamCfg, ring)

	eng, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("NewDryRunEngine: %v", err)
	}
	defer func() {
		if eng.journalCh != nil {
			close(eng.journalCh)
			eng.journalWg.Wait()
		}
	}()

	st := eng.State()
	if st.TotalTrades != 3 {
		t.Fatalf("expected TotalTrades=3 after replaying newer journal records, got %d", st.TotalTrades)
	}
	if math.Abs(st.Cash-10068.0) > 1e-6 {
		t.Fatalf("expected Cash=10068.0 (10050 - 1 + 19), got %.6f", st.Cash)
	}
	if st.LastJournalSeq != 3 {
		t.Fatalf("expected LastJournalSeq=3, got %d", st.LastJournalSeq)
	}
}

func TestSpotOnlyUnencumberedCashMultiSymbol(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.InitialCapital = 1000.0
	cfg.SpotOnly = true
	cfg.Symbols = []string{"BTCUSDT", "ETHUSDT"}

	ring := engine.NewSPSCRing(1024)
	streamCfg := DefaultStreamConfig()
	streamCfg.Symbols = cfg.Symbols
	stream := NewMarketStreamIngestor(streamCfg, ring)

	eng, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("NewDryRunEngine: %v", err)
	}
	defer func() {
		if eng.journalCh != nil {
			close(eng.journalCh)
			eng.journalWg.Wait()
		}
	}()

	eng.mu.Lock()
	// Open long in BTCUSDT: 0.01 @ 60000 = $600 cost basis
	eng.state.Positions["BTCUSDT"] = &Position{
		Symbol:        "BTCUSDT",
		Size:          0.01,
		AvgEntryPrice: 60000.0,
	}
	// Resting BUY in ETHUSDT: 0.1 @ 3000 = $300 encumbered
	eng.activeOrders["ETHUSDT"] = []*ActiveMakerOrder{
		{Symbol: "ETHUSDT", Side: "BUY", Price: 3000.0, Size: 0.1},
	}
	avail := eng.unencumberedSpotCashLocked(nil)
	eng.mu.Unlock()

	if math.Abs(avail-100.0) > 1e-6 {
		t.Fatalf("expected unencumberedSpotCash=100.0 (1000 - 600 - 300), got %.6f", avail)
	}
}

func TestReplayJournalSinceCursorSameTimestamp(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.InitialCapital = 10000.0
	cfg.Symbols = []string{"BTCUSDT"}

	t0 := time.Now().Add(-5 * time.Minute).UTC()
	// Checkpoint saved at t0 with 0 trades (LastJournalSeq == 0)
	cp := DryRunState{
		StartTime:      t0,
		TargetEndTime:  t0.Add(14 * 24 * time.Hour),
		LastCheckpoint: t0,
		InitialCapital: 10000.0,
		Cash:           10000.0,
		Equity:         10000.0,
		PeakEquity:     10000.0,
		Positions:      map[string]*Position{"BTCUSDT": {Symbol: "BTCUSDT"}},
	}
	if err := os.MkdirAll(filepath.Dir(cfg.StateFile), 0o755); err != nil {
		t.Fatal(err)
	}
	cpBytes, _ := json.MarshalIndent(cp, "", "  ")
	if err := os.WriteFile(cfg.StateFile, cpBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	// Two fills at exact same timestamp t0 with JournalSeq 1 and 2
	recs := []TradeRecord{
		{Timestamp: t0, Symbol: "BTCUSDT", Side: "BUY", Role: "MAKER", Price: 100, Size: 1.0, ValueUSDT: 100, FeeUSDT: 1.0, PnLRealized: -1.0, JournalSeq: 1},
		{Timestamp: t0, Symbol: "BTCUSDT", Side: "SELL", Role: "MAKER", Price: 110, Size: 1.0, ValueUSDT: 110, FeeUSDT: 1.0, PnLRealized: 9.0, JournalSeq: 2},
	}
	var buf strings.Builder
	for _, r := range recs {
		b, _ := json.Marshal(r)
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(cfg.JournalFile, []byte(buf.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	ring := engine.NewSPSCRing(1024)
	streamCfg := DefaultStreamConfig()
	streamCfg.Symbols = cfg.Symbols
	stream := NewMarketStreamIngestor(streamCfg, ring)

	eng, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("NewDryRunEngine: %v", err)
	}
	defer func() {
		if eng.journalCh != nil {
			close(eng.journalCh)
			eng.journalWg.Wait()
		}
	}()

	st := eng.State()
	if st.TotalTrades != 2 || st.LastJournalSeq != 2 {
		t.Fatalf("expected TotalTrades=2 LastJournalSeq=2 for same-timestamp fills, got trades=%d seq=%d", st.TotalTrades, st.LastJournalSeq)
	}
}

func TestDryRunEngineStopCancelsMacroAndNews(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.Symbols = []string{"BTCUSDT"}
	cfg.EnableMacroShield = true
	cfg.EnableNewsPipeline = true
	cfg.MacroSyncURL = "http://127.0.0.1:0/macro"
	cfg.NewsSyncURL = "http://127.0.0.1:0/news"

	ring := engine.NewSPSCRing(1024)
	streamCfg := DefaultStreamConfig()
	streamCfg.Symbols = cfg.Symbols
	stream := NewMarketStreamIngestor(streamCfg, ring)

	eng, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("NewDryRunEngine: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eng.Start(ctx)
	eng.Stop()
}

func TestDryRunEngineStateSkipsNilPosition(t *testing.T) {
	eng := &DryRunEngine{
		state: DryRunState{
			Positions: map[string]*Position{
				"BTCUSDT": nil,
				"ETHUSDT": {Symbol: "ETHUSDT", Size: 1.5},
			},
		},
	}
	st := eng.State()
	if _, ok := st.Positions["BTCUSDT"]; ok {
		t.Fatalf("expected nil position BTCUSDT to be skipped in State() snapshot")
	}
	if p := st.Positions["ETHUSDT"]; p == nil || p.Size != 1.5 {
		t.Fatalf("expected ETHUSDT position size 1.5, got %+v", p)
	}
}

func TestRunExecutionLoopNilRing(t *testing.T) {
	eng := &DryRunEngine{
		stopCh: make(chan struct{}),
	}
	eng.wg.Add(1)
	done := make(chan struct{})
	go func() {
		eng.runExecutionLoop(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runExecutionLoop did not return immediately when ring == nil")
	}
}

func TestLoadCheckpointEmptyPrimaryWithoutBakReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	if err := os.MkdirAll(filepath.Dir(cfg.StateFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.StateFile, []byte("  \n\x00 "), 0o644); err != nil {
		t.Fatal(err)
	}

	eng := &DryRunEngine{cfg: cfg}
	if err := eng.loadCheckpoint(); err == nil {
		t.Fatal("expected loadCheckpoint to fail on empty primary without .bak")
	}

	// Also when .bak exists but is empty
	if err := os.WriteFile(cfg.StateFile, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.StateFile+".bak", []byte("\x00\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := eng.loadCheckpoint(); err == nil {
		t.Fatal("expected loadCheckpoint to fail when both primary and .bak are empty")
	}
}

func TestCheckpointPersistAndRestoreRuntimeState(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.Symbols = []string{"BTCUSDT"}
	cfg.InitialCapital = 10000.0

	ring := engine.NewSPSCRing(1024)
	streamCfg := DefaultStreamConfig()
	streamCfg.Symbols = cfg.Symbols
	stream := NewMarketStreamIngestor(streamCfg, ring)

	eng1, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("NewDryRunEngine 1: %v", err)
	}
	accrualAt := time.Now().Add(-3 * time.Hour).UTC().Truncate(time.Second)
	eng1.mu.Lock()
	eng1.lastFundingAccrual = accrualAt
	eng1.state.PeakEquity = 12500.0
	eng1.state.Equity = 12000.0
	eng1.state.Cash = 12000.0
	eng1.state.Positions["BTCUSDT"] = &Position{
		Symbol:        "BTCUSDT",
		Size:          0.1,
		AvgEntryPrice: 50000.0,
		MarkPrice:     50000.0,
		HoldingSec:    120.0,
	}
	sm1 := eng1.getOrCreateSymbolMicro("BTCUSDT")
	sm1.ScalpingDisabled = true
	sm1.ScalpingDisabledReason = "consecutive losses"
	sm1.ConsecutiveLosses = 4
	eng1.mu.Unlock()

	if err := eng1.saveCheckpoint(); err != nil {
		t.Fatalf("saveCheckpoint: %v", err)
	}
	if eng1.journalCh != nil {
		close(eng1.journalCh)
		eng1.journalWg.Wait()
		eng1.journalCh = nil
	}

	eng2, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("NewDryRunEngine 2: %v", err)
	}
	defer func() {
		if eng2.journalCh != nil {
			close(eng2.journalCh)
			eng2.journalWg.Wait()
		}
	}()

	eng2.mu.RLock()
	sm2 := eng2.symMicro["BTCUSDT"]
	gotAccrual := eng2.lastFundingAccrual
	eng2.mu.RUnlock()

	if !gotAccrual.Equal(accrualAt) {
		t.Fatalf("lastFundingAccrual mismatch: got %v, want %v", gotAccrual, accrualAt)
	}
	if sm2 == nil {
		t.Fatal("expected symMicro[BTCUSDT] to be restored")
	}
	if !sm2.ScalpingDisabled || sm2.ScalpingDisabledReason != "consecutive losses" || sm2.ConsecutiveLosses != 4 {
		t.Fatalf("scalping state not restored: disabled=%v reason=%q losses=%d",
			sm2.ScalpingDisabled, sm2.ScalpingDisabledReason, sm2.ConsecutiveLosses)
	}
	if sm2.InventorySince.IsZero() {
		t.Fatal("expected InventorySince to be reconstructed from HoldingSec")
	}
	if eng2.riskSafeguard.PeakEquity != 12500.0 {
		t.Fatalf("expected riskSafeguard.PeakEquity=12500.0, got %.2f", eng2.riskSafeguard.PeakEquity)
	}
	if math.Abs(eng2.riskSafeguard.CurrentDrawdownPct-4.0) > 1e-6 {
		t.Fatalf("expected riskSafeguard.CurrentDrawdownPct=4.0, got %.4f", eng2.riskSafeguard.CurrentDrawdownPct)
	}
}

func TestSimulateTakerFillTracksEntryFeesAndOutcome(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.Symbols = []string{"BTCUSDT"}
	cfg.InitialCapital = 10000.0
	cfg.TakerFeeBps = 5.0
	cfg.KylesLambda = 0.0

	ring := engine.NewSPSCRing(1024)
	eng, err := NewDryRunEngine(cfg, ring, nil)
	if err != nil {
		t.Fatalf("NewDryRunEngine: %v", err)
	}
	defer func() {
		if eng.journalCh != nil {
			close(eng.journalCh)
			eng.journalWg.Wait()
		}
	}()

	eng.mu.Lock()
	eng.simulateTakerFillLocked("BTCUSDT", 50000.0, 10.0)
	pos := eng.state.Positions["BTCUSDT"]
	if pos.Size <= 0 || pos.EntryFeesUSDT <= 0 {
		eng.mu.Unlock()
		t.Fatalf("expected open position with EntryFeesUSDT > 0, got size=%v entryFees=%v", pos.Size, pos.EntryFeesUSDT)
	}
	// Close the position via opposite taker fill at higher price
	eng.simulateTakerFillLocked("BTCUSDT", 51000.0, -10.0)
	eng.mu.Unlock()

	st := eng.State()
	if st.Expectancy.All.N != 1 {
		t.Fatalf("expected Expectancy.All.N == 1 after taker close, got %d", st.Expectancy.All.N)
	}
	if st.Positions["BTCUSDT"].Size == 0 && st.Positions["BTCUSDT"].EntryFeesUSDT != 0 {
		t.Fatalf("expected EntryFeesUSDT == 0 when flat, got %v", st.Positions["BTCUSDT"].EntryFeesUSDT)
	}
}

func TestFeedDownSecondsNoDoubleCount(t *testing.T) {
	ring := engine.NewSPSCRing(1024)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)
	eng := &DryRunEngine{
		stream:       stream,
		activeOrders: make(map[string][]*ActiveMakerOrder),
		lastTickTime: time.Now(),
		state: DryRunState{
			DataSource:    "fallback",
			QuotingPaused: true,
		},
	}
	eng.checkFeedLiveness()
	if eng.state.FeedDownSeconds != 0 {
		t.Fatalf("expected FeedDownSeconds == 0 when fallback ticks are fresh, got %v", eng.state.FeedDownSeconds)
	}
}

func TestEvaluateQuotesAndGatingConcurrentStateRace(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.Symbols = []string{"BTCUSDT"}
	cfg.InitialCapital = 10000.0

	ring := engine.NewSPSCRing(1024)
	eng, err := NewDryRunEngine(cfg, ring, nil)
	if err != nil {
		t.Fatalf("NewDryRunEngine: %v", err)
	}
	defer func() {
		if eng.journalCh != nil {
			close(eng.journalCh)
			eng.journalWg.Wait()
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_ = eng.State()
			time.Sleep(100 * time.Microsecond)
		}
	}()

	for i := 0; i < 50; i++ {
		ev := &engine.MarketEvent{
			SymbolID: 0,
			Price:    50000.0 + float64(i)*0.5,
			Size:     0.1,
		}
		eng.ProcessMarketEvent(ev)
	}
	<-done
}

func TestFillLockedInitializesNilPosition(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.Symbols = []string{"BTCUSDT"}
	cfg.InitialCapital = 10000.0

	ring := engine.NewSPSCRing(1024)
	eng, err := NewDryRunEngine(cfg, ring, nil)
	if err != nil {
		t.Fatalf("NewDryRunEngine: %v", err)
	}
	defer func() {
		if eng.journalCh != nil {
			close(eng.journalCh)
			eng.journalWg.Wait()
		}
	}()

	eng.mu.Lock()
	delete(eng.state.Positions, "BTCUSDT")
	eng.executeMakerFillLocked("BTCUSDT", 50000.0, 0.01, "BUY", 2.0, "test")
	if eng.state.Positions["BTCUSDT"] == nil || math.Abs(eng.state.Positions["BTCUSDT"].Size-0.01) > 1e-9 {
		eng.mu.Unlock()
		t.Fatalf("expected initialized BTCUSDT position after executeMakerFillLocked")
	}

	delete(eng.state.Positions, "BTCUSDT")
	eng.simulateTakerFillLocked("BTCUSDT", 50000.0, 10.0)
	if eng.state.Positions["BTCUSDT"] == nil || eng.state.Positions["BTCUSDT"].Size <= 0 {
		eng.mu.Unlock()
		t.Fatalf("expected initialized BTCUSDT position after simulateTakerFillLocked")
	}
	eng.mu.Unlock()
}

func TestSimulateTakerFillGuards(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.Symbols = []string{"BTCUSDT"}
	cfg.InitialCapital = 1000.0
	cfg.SpotOnly = true

	ring := engine.NewSPSCRing(1024)
	eng, err := NewDryRunEngine(cfg, ring, nil)
	if err != nil {
		t.Fatalf("NewDryRunEngine: %v", err)
	}
	defer func() {
		if eng.journalCh != nil {
			close(eng.journalCh)
			eng.journalWg.Wait()
		}
	}()

	eng.mu.Lock()
	// 1. SpotOnly blocks shorting when flat
	eng.simulateTakerFillLocked("BTCUSDT", 50000.0, -10.0)
	if eng.state.TotalTrades != 0 {
		eng.mu.Unlock()
		t.Fatalf("expected SpotOnly to block shorting when flat, got %d trades", eng.state.TotalTrades)
	}

	// 2. BidCooldownUntil blocks opening BUY
	eng.symMicro["BTCUSDT"].BidCooldownUntil = time.Now().Add(10 * time.Second)
	eng.simulateTakerFillLocked("BTCUSDT", 50000.0, 10.0)
	if eng.state.TotalTrades != 0 {
		eng.mu.Unlock()
		t.Fatalf("expected BidCooldownUntil to block BUY, got %d trades", eng.state.TotalTrades)
	}
	eng.symMicro["BTCUSDT"].BidCooldownUntil = time.Time{}

	// 3. SpotOnly blocks BUY when unencumbered cash is insufficient
	eng.state.Cash = 1.0
	eng.simulateTakerFillLocked("BTCUSDT", 50000.0, 10.0)
	if eng.state.TotalTrades != 0 {
		eng.mu.Unlock()
		t.Fatalf("expected insufficient spot cash to block BUY, got %d trades", eng.state.TotalTrades)
	}

	// 4. Valid BUY then SELL clamps to long size (no flip short in SpotOnly)
	eng.state.Cash = 1000.0
	eng.recomputeEquityLocked()
	eng.simulateTakerFillLocked("BTCUSDT", 50000.0, 10.0)
	if eng.state.Positions["BTCUSDT"].Size <= 0 {
		eng.mu.Unlock()
		t.Fatalf("expected positive long size after valid BUY")
	}
	eng.simulateTakerFillLocked("BTCUSDT", 50000.0, -10.0)
	if eng.state.Positions["BTCUSDT"].Size < 0 {
		eng.mu.Unlock()
		t.Fatalf("expected SpotOnly SELL not to flip short, got size=%v", eng.state.Positions["BTCUSDT"].Size)
	}
	eng.mu.Unlock()
}

type mockL1Feed struct {
	book marketdata.L2OrderBook
}

func (m *mockL1Feed) IsConnected() bool { return true }
func (m *mockL1Feed) IsFallback() bool  { return false }
func (m *mockL1Feed) Stats() (uint64, uint64, uint64, uint64, uint64, uint64, int64, int64) {
	now := time.Now().UnixNano()
	return 1, 1, 0, 0, 0, 0, now, now
}
func (m *mockL1Feed) GetL2Book(symbol string) (*marketdata.L2OrderBook, bool) {
	cp := m.book
	return &cp, true
}
func (m *mockL1Feed) CopyL2Book(symbol string, dst *marketdata.L2OrderBook) bool {
	*dst = m.book
	return true
}

func TestL1OnlyCircuitBreakerDoesNotWipeNormalSpreads(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.Symbols = []string{"BTC-USD"}
	cfg.InitialCapital = 200.0
	cfg.SpotOnly = true

	// 7 bps spread on L1-only book
	feed := &mockL1Feed{}
	feed.book.SetSnapshot(1,
		[]marketdata.PriceLevel{{Price: 50000.0, Size: 1.0}},
		[]marketdata.PriceLevel{{Price: 50035.0, Size: 1.0}},
	)

	ring := engine.NewSPSCRing(1024)
	eng, err := NewDryRunEngine(cfg, ring, nil)
	if err != nil {
		t.Fatalf("NewDryRunEngine: %v", err)
	}
	eng.stream = feed
	defer func() {
		if eng.journalCh != nil {
			close(eng.journalCh)
			eng.journalWg.Wait()
		}
	}()

	eng.ProcessMarketEvent(&engine.MarketEvent{SymbolID: 0, Price: 50017.5, Size: 2.0})

	// Simulate 90% L1 depth flicker within 500ms at 7 bps spread
	feed.book.SetSnapshot(2,
		[]marketdata.PriceLevel{{Price: 50000.0, Size: 0.1}},
		[]marketdata.PriceLevel{{Price: 50035.0, Size: 0.1}},
	)
	eng.ProcessMarketEvent(&engine.MarketEvent{SymbolID: 0, Price: 50017.5, Size: 0.2})

	eng.mu.RLock()
	sm := eng.symMicro["BTC-USD"]
	state := sm.CircuitBrk.State
	eng.mu.RUnlock()
	if state == experiment.StateHalted {
		t.Fatalf("expected L1-only circuit breaker not to halt on normal 7 bps spread / L1 depth flicker")
	}
}

func TestClipQtyReflooredAboveMinNotional(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.Symbols = []string{"BTC-USD"}
	cfg.InitialCapital = 200.0
	cfg.SpotOnly = true
	cfg.BuyThreshold = 0.01 // allow BUY quoting easily
	cfg.EnableMacroShield = false
	cfg.EnableNewsPipeline = false

	feed := &mockL1Feed{}
	feed.book.SetSnapshot(1,
		[]marketdata.PriceLevel{{Price: 50000.0, Size: 1.0}},
		[]marketdata.PriceLevel{{Price: 50035.0, Size: 1.0}},
	)

	ring := engine.NewSPSCRing(1024)
	eng, err := NewDryRunEngine(cfg, ring, nil)
	if err != nil {
		t.Fatalf("NewDryRunEngine: %v", err)
	}
	eng.stream = feed
	defer func() {
		if eng.journalCh != nil {
			close(eng.journalCh)
			eng.journalWg.Wait()
		}
	}()

	eng.mu.Lock()
	eng.state.Goal.LeverageMultiplier = 0.25 // scales clipQty below $5.00 before re-flooring
	eng.mu.Unlock()

	eng.evaluateQuotesAndGating("BTC-USD", 50017.5, time.Now())

	eng.mu.RLock()
	orders := eng.activeOrders["BTC-USD"]
	eng.mu.RUnlock()
	if len(orders) == 0 {
		t.Fatalf("expected resting quote to be placed after re-flooring clipQty to >= $5.00")
	}
	for _, ord := range orders {
		if ord.Size*ord.Price < 5.0 {
			t.Fatalf("expected re-floored notional >= $5.00, got %.4f", ord.Size*ord.Price)
		}
	}
}
