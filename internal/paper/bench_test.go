package paper

import (
	"testing"
	"time"

	"piquant-live/internal/engine"
)

func benchEngine(b *testing.B, syms ...string) *DryRunEngine {
	b.Helper()
	cfg := DefaultDryRunConfig(b.TempDir())
	cfg.Symbols = syms
	eng, err := NewDryRunEngine(cfg, engine.NewSPSCRing(1024), nil)
	if err != nil {
		b.Fatal(err)
	}
	return eng
}

// BenchmarkEvaluateQuotesAndGating measures the per-tick hot path with a
// synthetic price walk on one symbol.
func BenchmarkEvaluateQuotesAndGating(b *testing.B) {
	eng := benchEngine(b, "BTCUSDT")
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	px := 90000.0
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		px += float64((i*7)%11-5) * 0.5
		eng.evaluateQuotesAndGating("BTCUSDT", px, base.Add(time.Duration(i)*200*time.Millisecond))
	}
}

// BenchmarkEvaluateWithPosition ticks while a long position is open (exit path).
func BenchmarkEvaluateWithPosition(b *testing.B) {
	eng := benchEngine(b, "BTCUSDT")
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	eng.mu.Lock()
	eng.executeMakerFillLocked("BTCUSDT", 90000, 0.01, "BUY", 2.0, "Bench_Entry")
	eng.mu.Unlock()
	px := 90000.0
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		px += float64((i*7)%11-5) * 0.5
		eng.evaluateQuotesAndGating("BTCUSDT", px, base.Add(time.Duration(i)*200*time.Millisecond))
	}
}
