package main

import (
	"path/filepath"
	"testing"
	"time"

	"piquant-live/internal/trend"
)

func TestRunTrendReportWithBlend(t *testing.T) {
	dir := t.TempDir()
	st := trend.NewState("2026-11-11")
	t0, _ := time.Parse("2006-01-02", "2026-09-20")
	var seq []trend.Day
	for i := 0; i < 60; i++ {
		seq = append(seq, trend.Day{Date: t0.AddDate(0, 0, i).Format("2006-01-02"), Close: 100 + float64(i)})
	}
	st.Seed(seq[:51])
	st.SeedETH(seq[:51])
	st.Advance(seq[51:])
	st.AdvanceETH(seq[51:])
	if err := st.Save(filepath.Join(dir, "trend_state.json")); err != nil {
		t.Fatal(err)
	}
	if err := runTrend([]string{"-dir", dir, "-carry-dir", t.TempDir(), "-dtb3", filepath.Join(t.TempDir(), "missing.csv"), "-report", "-blend"}); err != nil {
		t.Fatalf("runTrend --report --blend failed: %v", err)
	}
}
