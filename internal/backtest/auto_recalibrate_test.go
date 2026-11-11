package backtest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// With no tick data, recalibration must fail loudly and write no parameter file.
func TestRunAutonomousRecalibrationNoTickDataWritesNothing(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil { // keep relative fallbacks from finding repo data
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()

	err := RunAutonomousRecalibration(context.Background(), "NOSUCHSYM", 200, dir)
	if !errors.Is(err, ErrNoTickData) {
		t.Fatalf("expected ErrNoTickData, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "config", "optimal_params.json")); !os.IsNotExist(statErr) {
		t.Fatalf("expected no params file to be written, stat err = %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "data", "reports", "optimal_params.json")); !os.IsNotExist(statErr) {
		t.Fatalf("expected no relative params file to be written, stat err = %v", statErr)
	}
}
