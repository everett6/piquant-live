package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"piquant-live/internal/paper"
)

func TestMonitorRenderFrame(t *testing.T) {
	now := time.Now()
	st := &paper.DryRunState{
		StartTime:         now.Add(-24 * time.Hour),
		TargetEndTime:     now.Add(6 * 24 * time.Hour),
		LastCheckpoint:    now,
		InitialCapital:    200.00,
		Cash:              199.65,
		Equity:            199.40,
		PeakEquity:        200.00,
		RealizedPnL:       -0.34,
		UnrealizedPnL:     -0.26,
		TotalReturnPct:    -0.30,
		MaxDrawdownPct:    0.30,
		TotalTrades:       34,
		WinningTrades:     5,
		LosingTrades:      11,
		WinRate:           31.2,
		SharpeRatio:       1.45,
		DaysElapsed:       2,
		TargetDays:        7,
		WindowPreset:      "7d",
		HoursRemaining:    144.0,
		DataSource:        "live",
		FeedDownSeconds:   0.0,
		Positions: map[string]*paper.Position{
			"BTCUSDT": {
				Symbol:        "BTCUSDT",
				Size:          -0.0018,
				AvgEntryPrice: 75839.01,
				MarkPrice:     75943.26,
				UnrealizedPnL: -0.19,
				TotalTrades:   10,
			},
			"ETHUSDT": {
				Symbol:        "ETHUSDT",
				Size:          0.0200,
				AvgEntryPrice: 2232.85,
				MarkPrice:     2236.31,
				UnrealizedPnL: 0.07,
				TotalTrades:   14,
			},
		},
		Leverage: paper.DynamicLeverageState{
			EffectiveLeverage: 1.92,
			TargetLeverage:    1.92,
			MaxLeverage:       3.00,
			VolFactor:         1.60,
			ToxicityFactor:    0.59,
			CascadeFactor:     1.00,
			RegimeName:        "TOXIC_DEFENSE",
			RegimeDetail:      "Informed toxic flow (VPIN 0.61): leverage dialed down to 1.9x",
		},
		ActiveOrders: map[string][]paper.ActiveMakerOrder{
			"BTCUSDT": {
				{
					Symbol:     "BTCUSDT",
					Side:       "BUY",
					Price:      75905.12,
					Size:       0.0009,
					PlacedAt:   now.Add(-10 * time.Second),
					QueueAhead: 0.85,
					Strategy:   "GLFT_QueueReactive",
					EdgeBps:    1.95,
				},
			},
		},
	}

	trades := []paper.TradeRecord{
		{
			Timestamp:   now.Add(-2 * time.Minute),
			Symbol:      "BTCUSDT",
			Side:        "BUY",
			Role:        "MAKER",
			Price:       75919.19,
			Size:        0.0009,
			ValueUSDT:   68.33,
			FeeUSDT:     0.0102,
			PnLRealized: -0.0824,
			Strategy:    "GLFT_QueueReactive",
		},
		{
			Timestamp:   now.Add(-5 * time.Minute),
			Symbol:      "ETHUSDT",
			Side:        "SELL",
			Role:        "MAKER",
			Price:       2233.69,
			Size:        0.0100,
			ValueUSDT:   22.34,
			FeeUSDT:     0.0034,
			PnLRealized: 0.0032,
			Strategy:    "GLFT_QueueReactive",
		},
	}

	var buf bytes.Buffer
	renderTerminalFrame(&buf, st, trades, 100, "/tmp/data", "")
	rawOut := buf.String()
	cleanOut := stripANSI(rawOut)

	// Verify key UI sections are rendered
	checks := []string{
		"PIQUANT HFT RESEARCH ENGINE",
		"ACTIVE (OK)",
		"BINANCE LIVE (WS)",
		"Horizon: 7D (7 Days)",
		"Initial:  $200.00",
		"Cash:     $199.65",
		"Realized: $-0.34",
		"Win Rate: 31.2%",
		"TOXIC_DEFENSE",
		"Eff Leverage: 1.92x",
		"BTCUSDT",
		"ETHUSDT",
		"ACTIVE PORTFOLIO INVENTORY",
		"ACTIVE RESTING LIMIT ORDERS",
		"QUEUE AHEAD",
		"RECENT EXECUTIONS (BLOTTER)",
		"[q] Quit",
	}

	for _, check := range checks {
		if !strings.Contains(cleanOut, check) {
			t.Errorf("expected clean output to contain %q, but was missing.\nOutput:\n%s", check, cleanOut)
		}
	}
}

func TestMonitorWaitingFrame(t *testing.T) {
	var buf bytes.Buffer
	err := os.ErrNotExist
	renderWaitingFrame(&buf, err, 90, "/tmp/piquant-data", "")
	out := buf.String()

	if !strings.Contains(out, "CONNECTING TO ENGINE") {
		t.Errorf("expected waiting frame header, got:\n%s", out)
	}
	if !strings.Contains(out, "WAITING FOR ACTIVE DRY RUN CHECKPOINT") {
		t.Errorf("expected waiting notice, got:\n%s", out)
	}
}

func TestMonitorReadJournalTail(t *testing.T) {
	tmpDir := t.TempDir()
	journalFile := filepath.Join(tmpDir, "test_journal.jsonl")

	f, err := os.Create(journalFile)
	if err != nil {
		t.Fatalf("failed to create test journal: %v", err)
	}

	// Write 15 trades with empty lines and null bytes
	for i := 1; i <= 15; i++ {
		rec := paper.TradeRecord{
			Timestamp:   time.Now().Add(time.Duration(i) * time.Minute),
			Symbol:      "BTCUSDT",
			Side:        "BUY",
			Role:        "MAKER",
			Price:       75000.0 + float64(i)*10.0,
			Size:        0.001,
			ValueUSDT:   75.0,
			FeeUSDT:     0.01,
			PnLRealized: 0.05,
			Strategy:    "TestStrat",
		}
		data, _ := json.Marshal(rec)
		_, _ = f.Write(data)
		_, _ = f.WriteString("\n")
		if i == 5 {
			_, _ = f.WriteString("\x00\x00\x00\n") // simulated null-byte line
		}
		if i == 10 {
			_, _ = f.WriteString("\n   \n") // empty lines
		}
	}
	f.Close()

	tail := readJournalTail(journalFile, 5)
	if len(tail) != 5 {
		t.Fatalf("expected 5 trades in tail, got %d", len(tail))
	}

	// Newest should be first (reversed order for blotter display)
	if tail[0].Price < tail[4].Price {
		t.Errorf("expected newest fill with highest price first, got %v vs %v", tail[0].Price, tail[4].Price)
	}
}

func TestMonitorFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	f := addMonitorFlags(fs)

	args := []string{
		"--data", "/custom/data",
		"--interval", "250ms",
		"--limit", "25",
		"--once",
	}

	if err := fs.Parse(args); err != nil {
		t.Fatalf("flag parse error: %v", err)
	}

	if f.dataDir != "/custom/data" {
		t.Errorf("expected dataDir /custom/data, got %s", f.dataDir)
	}
	if f.interval != 250*time.Millisecond {
		t.Errorf("expected interval 250ms, got %v", f.interval)
	}
	if f.limit != 25 {
		t.Errorf("expected limit 25, got %d", f.limit)
	}
	if !f.once {
		t.Errorf("expected once=true, got %v", f.once)
	}
}
