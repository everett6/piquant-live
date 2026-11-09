package paper

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"piquant-live/internal/engine"
)

// TestFeedWatchdogCancelOnDisconnect verifies that when WiFi drops or stream stalls:
// 1. The independent Feed Watchdog detects staleness in <= 1500ms.
// 2. Resting maker orders are instantly purged (Cancel-On-Disconnect).
// 3. QuotingPaused is asserted and DataSource is marked disconnected.
// 4. FeedDownSeconds accumulates accurately.
// 5. When the feed reconnects with a gapped market tick, resting quotes are not filled at stale prices.
func TestFeedWatchdogCancelOnDisconnect(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultDryRunConfig(tmpDir)
	cfg.StateFile = filepath.Join(tmpDir, "dryrun_state.json")
	cfg.JournalFile = filepath.Join(tmpDir, "dryrun_journal.jsonl")
	cfg.Symbols = []string{"BTCUSDT"}

	ring := engine.NewSPSCRing(1024)
	e, err := NewDryRunEngine(cfg, ring, nil)
	if err != nil {
		t.Fatalf("Failed to instantiate engine: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e.Start(ctx)
	defer e.Stop()

	// 1. Prime the engine with an initial live event
	now := time.Now()
	e.ProcessMarketEvent(&engine.MarketEvent{
		Timestamp: now.UnixNano(),
		Price:     65000.0,
		Size:      1.0,
		Kind:      1, // Depth
		SymbolID:  0,
	})

	// Inject a resting maker quote
	e.mu.Lock()
	e.activeOrders["BTCUSDT"] = []*ActiveMakerOrder{
		{
			Symbol:   "BTCUSDT",
			Side:     "BUY",
			Price:    64990.0,
			Size:     0.01,
			ActiveAt: now.Add(-100 * time.Millisecond),
		},
	}
	e.mu.Unlock()

	// Verify order is initially resting
	e.mu.Lock()
	if len(e.activeOrders["BTCUSDT"]) == 0 {
		e.mu.Unlock()
		t.Fatalf("Expected resting maker order to be registered")
	}
	e.mu.Unlock()

	// 2. Simulate 2.0s of WiFi silence (no events popping, stream down)
	e.mu.Lock()
	e.lastTickTime = time.Now().Add(-2 * time.Second)
	e.mu.Unlock()

	// Allow watchdog to tick (ticking every 100ms)
	time.Sleep(250 * time.Millisecond)

	// 3. Verify Cancel-On-Disconnect purged resting quotes
	e.mu.Lock()
	orders := e.activeOrders["BTCUSDT"]
	paused := e.state.QuotingPaused
	source := e.state.DataSource
	downSec := e.state.FeedDownSeconds
	e.mu.Unlock()

	if len(orders) != 0 {
		t.Fatalf("Expected resting orders to be purged by watchdog, got %d orders", len(orders))
	}
	if !paused {
		t.Fatalf("Expected QuotingPaused to be true during outage")
	}
	if source != "disconnected" {
		t.Fatalf("Expected DataSource to be 'disconnected', got '%s'", source)
	}
	if downSec <= 0 {
		t.Fatalf("Expected FeedDownSeconds to accumulate, got %.2f", downSec)
	}

	// 4. Simulate WiFi Reconnection with a gapped market tick (BTC swept down to $60,000)
	// Stale resting order at $64,990 must NOT fill!
	reconnectTime := time.Now()
	tradeEv := engine.MarketEvent{
		Timestamp: reconnectTime.UnixNano(),
		Price:     60000.0,
		Size:      2.0,
		Side:      2, // Aggressive seller
		Kind:      0, // Trade
		SymbolID:  0,
	}

	e.ProcessMarketEvent(&tradeEv)

	// Verify no stale fill was executed against old quotes
	e.mu.Lock()
	trades := e.state.TotalTrades
	posSize := 0.0
	if pos, ok := e.state.Positions["BTCUSDT"]; ok {
		posSize = pos.Size
	}
	e.mu.Unlock()

	if trades != 0 || posSize != 0 {
		t.Fatalf("Catastrophic Stale Fill: Resting order executed against gapped reconnect tick! trades=%d posSize=%.4f", trades, posSize)
	}
}

// TestPowerCutCheckpointAndJournalRecovery verifies ext4 crash resilience:
// 1. Dual-slot checkpointing (.bak recovery if primary is zeroed/corrupted).
// 2. Journal tail sanitization on cold-boot (truncates partial torn records).
// 3. NTP clock backward jump protection.
func TestPowerCutCheckpointAndJournalRecovery(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "dryrun_state.json")
	journalPath := filepath.Join(tmpDir, "dryrun_journal.jsonl")

	cfg := DefaultDryRunConfig(tmpDir)
	cfg.StateFile = statePath
	cfg.JournalFile = journalPath

	// 1. Create engine and save valid initial state
	ring := engine.NewSPSCRing(1024)
	e1, err := NewDryRunEngine(cfg, ring, nil)
	if err != nil {
		t.Fatalf("Failed to initialize engine 1: %v", err)
	}

	e1.mu.Lock()
	e1.state.Cash = 10500.0
	e1.state.Equity = 10500.0
	e1.state.RealizedPnL = 500.0
	e1.mu.Unlock()

	if err := e1.saveCheckpoint(); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	// Save a second time to ensure .bak exists
	if err := e1.saveCheckpoint(); err != nil {
		t.Fatalf("Failed to save second checkpoint: %v", err)
	}

	bakPath := statePath + ".bak"
	if _, err := os.Stat(bakPath); err != nil {
		t.Fatalf("Expected .bak backup checkpoint file to exist: %v", err)
	}

	// 2. Simulate sudden power loss during write:
	// Fill primary dryrun_state.json with 0x00 null bytes (standard ext4 allocated unwritten extent on flash)
	nullBytes := make([]byte, 512)
	if err := os.WriteFile(statePath, nullBytes, 0o644); err != nil {
		t.Fatalf("Failed to simulate null corruption: %v", err)
	}

	// 3. Simulate sudden power loss tearing a trade append in dryrun_journal.jsonl
	// Incomplete partial JSON record without newline
	partialRecord := `{"timestamp":"2026-11-09T06:00:00Z","symbol":"BTCUSDC","price":67500`
	if err := os.WriteFile(journalPath, []byte(partialRecord), 0o644); err != nil {
		t.Fatalf("Failed to write partial journal: %v", err)
	}

	// 4. Initialize Engine 2 (Cold-Boot Recovery)
	e2, err := NewDryRunEngine(cfg, ring, nil)
	if err != nil {
		t.Fatalf("Failed to recover engine 2 after power cut: %v", err)
	}

	// Verify state recovered seamlessly from .bak
	e2.mu.Lock()
	recoveredCash := e2.state.Cash
	recoveredPnL := e2.state.RealizedPnL
	e2.mu.Unlock()

	if recoveredCash != 10500.0 || recoveredPnL != 500.0 {
		t.Fatalf("State recovery failed! Expected Cash=10500 PnL=500, got Cash=%.2f PnL=%.2f", recoveredCash, recoveredPnL)
	}

	// 5. Append a new trade record to verify journal append does not fuse onto torn line
	newTrade := TradeRecord{
		Timestamp:   time.Now().UTC(),
		Symbol:      "BTCUSDC",
		Side:        "BUY",
		Role:        "MAKER",
		Price:       68000.0,
		Size:        0.01,
		ValueUSDT:   680.0,
		Strategy:    "Test_Recovery",
		DataSource:  "live",
	}
	b, err := json.Marshal(newTrade)
	if err != nil {
		t.Fatalf("Failed to marshal new trade: %v", err)
	}

	e2.journalCh <- append(b, '\n')
	time.Sleep(100 * time.Millisecond)
	e2.Stop()

	// Read journal and verify it parses cleanly line by line
	jData, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("Failed to read journal: %v", err)
	}

	var parsed TradeRecord
	if err := json.Unmarshal(jData, &parsed); err != nil {
		t.Fatalf("Journal file was corrupted by fused append! Raw contents:\n%s\nErr: %v", string(jData), err)
	}
	if parsed.Symbol != "BTCUSDC" || parsed.Price != 68000.0 {
		t.Fatalf("Parsed journal record mismatch: %+v", parsed)
	}
}

// TestBackwardClockJumpProtection verifies that if the Pi 5 boots without RTC battery
// and system clock is in the past, checkpoint does not overwrite LastCheckpoint or corrupt metrics.
func TestBackwardClockJumpProtection(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "dryrun_state.json")

	cfg := DefaultDryRunConfig(tmpDir)
	cfg.StateFile = statePath

	ring := engine.NewSPSCRing(1024)
	e, err := NewDryRunEngine(cfg, ring, nil)
	if err != nil {
		t.Fatalf("Failed to init engine: %v", err)
	}

	// Simulate future checkpoint saved before reboot
	validFuture := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	e.mu.Lock()
	e.state.LastCheckpoint = validFuture
	e.state.StartTime = validFuture.Add(-24 * time.Hour)
	e.state.TargetDays = 14
	e.state.DaysElapsed = 2
	e.mu.Unlock()

	// Save checkpoint with current time (which is before validFuture)
	if err := e.saveCheckpoint(); err != nil {
		t.Fatalf("Failed to save checkpoint: %v", err)
	}

	// Read state and verify LastCheckpoint was NOT overwritten with an earlier timestamp
	savedData, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("Failed to read state: %v", err)
	}

	var savedSt DryRunState
	if err := json.Unmarshal(savedData, &savedSt); err != nil {
		t.Fatalf("Failed to unmarshal saved state: %v", err)
	}

	if savedSt.LastCheckpoint.Before(validFuture) {
		t.Fatalf("Clock warp vulnerability: LastCheckpoint was corrupted with past time! got: %v, expected: %v", savedSt.LastCheckpoint, validFuture)
	}
}
