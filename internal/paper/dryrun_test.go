package paper

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"piquant-live/internal/engine"
	"piquant-live/internal/marketdata"
	"piquant-live/internal/models/common"
)

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

	// Test Taker Fill with Kyle's Lambda slippage
	dryRun.simulateTakerFill("BTCUSDT", 60000.0, 3.50)
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

	total, depths, trades, _, _, fallbacks, lastNs := stream.Stats()
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
		btcBook.Bids[i] = marketdata.PriceLevel{Price: 71800.0 - float64(i), Size: 0.70} // ~$250k bid side
		btcBook.Asks[i] = marketdata.PriceLevel{Price: 71801.0 + float64(i), Size: 0.70} // ~$250k ask side
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
