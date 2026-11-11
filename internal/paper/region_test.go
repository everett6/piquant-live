package paper

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"piquant-live/internal/engine"
)

func TestApplyRegionProfileUS(t *testing.T) {
	cfg := DefaultDryRunConfig(t.TempDir())
	cfg.Symbols = []string{"BTCUSDT", "ethusdt"}
	cfg.BaseLeverage, cfg.MaxLeverage = 2.5, 3.5
	cfg.TickSizes = map[string]float64{"BTCUSDT": 0.05} // explicit override must survive
	if err := ApplyRegionProfile(&cfg, "US"); err != nil {
		t.Fatal(err)
	}
	if !cfg.SpotOnly || cfg.BaseLeverage != 1 || cfg.MaxLeverage != 1 {
		t.Errorf("US must be spot-only 1x, got spot=%v base=%v max=%v", cfg.SpotOnly, cfg.BaseLeverage, cfg.MaxLeverage)
	}
	if cfg.MakerFeeBps != USMakerFeeBps || cfg.TakerFeeBps != USTakerFeeBps || cfg.OrderLatency != USOrderLatency {
		t.Errorf("unexpected US fees/latency: %v %v %v", cfg.MakerFeeBps, cfg.TakerFeeBps, cfg.OrderLatency)
	}
	if cfg.TickSizes["BTCUSDT"] != 0.05 || cfg.TickSizes["ETHUSDT"] != 0.01 {
		t.Errorf("unexpected tick sizes: %v", cfg.TickSizes)
	}

	ring := engine.NewSPSCRing(64)
	eng, err := NewDryRunEngine(cfg, ring, NewMarketStreamIngestor(DefaultStreamConfig(), ring))
	if err != nil {
		t.Fatal(err)
	}
	if got := eng.tickSize("ethusdt"); got != 0.01 {
		t.Errorf("engine tick for ETHUSDT = %v, want 0.01", got)
	}
	if got := eng.tickSize("SOLUSDT"); got != symbolTickSize("SOLUSDT") {
		t.Errorf("unconfigured symbol should use default tick, got %v", got)
	}
}

func TestApplyRegionProfileGlobalAndUnknown(t *testing.T) {
	cfg := DefaultDryRunConfig(t.TempDir())
	before := cfg.MaxLeverage
	if err := ApplyRegionProfile(&cfg, "global"); err != nil || cfg.MaxLeverage != before || cfg.SpotOnly {
		t.Errorf("global must leave cfg unchanged (err=%v)", err)
	}
	if err := ApplyRegionProfile(&cfg, "mars"); err == nil {
		t.Error("expected error for unknown region")
	}
}

func TestBuildBinanceUSStreamURL(t *testing.T) {
	got := BuildBinanceUSStreamURL([]string{"BTCUSDT", " ethusdt "})
	want := "wss://stream.binance.us:9443/stream?streams=btcusdt@depth20@100ms/btcusdt@trade/ethusdt@depth20@100ms/ethusdt@trade"
	if got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
	if !strings.HasPrefix(BuildBinanceStreamURL(nil), "wss://data-stream.binance.vision:9443/stream?streams=btcusdt@") {
		t.Error("global URL changed unexpectedly")
	}
}

// Opt-in network test: TEST_LIVE_STREAM=1 go test -run LiveBinanceUS ./internal/paper
func TestLiveBinanceUSStream(t *testing.T) {
	if testing.Short() || os.Getenv("TEST_LIVE_STREAM") != "1" {
		t.Skip("set TEST_LIVE_STREAM=1 to run the live Binance.US stream test")
	}
	syms := []string{"BTCUSDT", "BTCUSDC", "ETHUSDT"}
	cfg := DefaultStreamConfig()
	cfg.Symbols = syms
	cfg.Endpoint = BuildBinanceUSStreamURL(syms)
	ring := engine.NewSPSCRing(8192)
	ing := NewMarketStreamIngestor(cfg, ring)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ing.Start(ctx)
	defer ing.Stop()
	go func() { // drain the ring so the producer never drops
		for ctx.Err() == nil {
			if _, ok := ring.Pop(); !ok {
				time.Sleep(time.Millisecond)
			}
		}
	}()
	<-ctx.Done()
	total, depths, trades, gaps, recons, _, _ := ing.Stats()
	t.Logf("events=%d depths=%d trades=%d gaps=%d reconnects=%d", total, depths, trades, gaps, recons)
	for _, s := range syms {
		b, ok := ing.GetL2Book(s)
		mid, _ := b.MidPrice()
		t.Logf("%s book=%v bid=%.2f ask=%.2f mid=%.2f", s, ok, b.Bids[0].Price, b.Asks[0].Price, mid)
		if !ok || mid <= 0 {
			t.Errorf("%s: no live Binance.US book", s)
		}
	}
	if depths == 0 {
		t.Fatal("no depth updates from Binance.US")
	}
}
