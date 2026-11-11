package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"piquant-live/internal/tax"
)

func TestTaxCLISummaryAndExport(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "tax_cli_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	paperDir := filepath.Join(tmpDir, "paper")
	if err := os.MkdirAll(paperDir, 0o755); err != nil {
		t.Fatalf("failed to create paper dir: %v", err)
	}

	journalFile := filepath.Join(paperDir, "dryrun_journal.jsonl")
	f, err := os.Create(journalFile)
	if err != nil {
		t.Fatalf("failed to create journal file: %v", err)
	}

	t1 := time.Date(2026, time.February, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, time.February, 2, 12, 0, 0, 0, time.UTC)

	tr1 := tax.RawTrade{
		Timestamp: t1,
		Symbol:    "BTCUSDT",
		Side:      "BUY",
		Price:     50000.0,
		Size:      0.002,
		FeeUSDT:   0.015,
	}
	tr2 := tax.RawTrade{
		Timestamp: t2,
		Symbol:    "BTCUSDT",
		Side:      "SELL",
		Price:     50500.0,
		Size:      0.002,
		FeeUSDT:   0.01515,
	}

	b1, _ := json.Marshal(tr1)
	b2, _ := json.Marshal(tr2)
	f.Write(append(b1, '\n'))
	f.Write(append(b2, '\n'))
	f.Close()

	ctx := context.Background()

	// 1. Test CSV file export flag
	exportCSVPath := filepath.Join(tmpDir, "form8949_test.csv")
	err = runTax(ctx, []string{"-data", tmpDir, "-year", "2026", "-export", exportCSVPath})
	if err != nil {
		t.Fatalf("runTax failed with export: %v", err)
	}

	content, err := os.ReadFile(exportCSVPath)
	if err != nil {
		t.Fatalf("failed to read exported CSV: %v", err)
	}
	if !strings.Contains(string(content), "Description of Property") {
		t.Fatalf("exported CSV missing header")
	}
	if !strings.Contains(string(content), "0.0020 BTC") {
		t.Fatalf("exported CSV missing trade row: %s", string(content))
	}

	// 2. Test default terminal report execution
	err = runTax(ctx, []string{"-data", tmpDir, "-year", "2026", "-table"})
	if err != nil {
		t.Fatalf("runTax failed for table: %v", err)
	}
}
