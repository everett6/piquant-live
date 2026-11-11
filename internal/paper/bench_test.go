package paper

import (
	"fmt"
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

// BenchmarkCoinbaseFeedEventPath measures the full ring-event path (Coinbase ticker -> depth + trade
// events -> processMarketEvent, including the L1 book lookup) as it runs on the Pi in us-futures mode.
func BenchmarkCoinbaseFeedEventPath(b *testing.B) {
	eng := benchEngine(b, "BTCUSDT")
	ring := engine.NewSPSCRing(1024)
	cb := NewCoinbaseStreamIngestor(DefaultCoinbaseStreamConfig(), ring, nil)
	cb.SetSymbolOrder([]string{"BTCUSDT"})
	eng.SetMarketFeed(cb)
	payloads := make([][]byte, 16)
	for i := range payloads {
		bid := 90000.0 + float64(i%7)
		payloads[i] = []byte(fmt.Sprintf(`{"type":"ticker","product_id":"BTC-USD","price":"%.2f","side":"%s",`+
			`"last_size":"0.01","best_bid":"%.2f","best_bid_size":"1.5","best_ask":"%.2f","best_ask_size":"2"}`,
			bid+0.01, map[bool]string{true: "buy", false: "sell"}[i%2 == 0], bid, bid+0.01))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cb.HandleStreamPayload(payloads[i%len(payloads)])
		for {
			ev, ok := ring.Pop()
			if !ok {
				break
			}
			eng.processMarketEvent(&ev)
		}
	}
}
