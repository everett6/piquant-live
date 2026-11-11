package paper

import (
	"testing"

	"piquant-live/internal/engine"
)

// On a thin venue (Binance.US ~1.3 BTC/hour) coin-sized buckets never filled; notional buckets must.
func TestVPIN_UsesNotionalBucketsOnThinVenue(t *testing.T) {
	cfg := DefaultDryRunConfig(t.TempDir())
	cfg.Symbols = []string{"BTCUSDT"}
	eng, err := NewDryRunEngine(cfg, engine.NewSPSCRing(1024), nil)
	if err != nil {
		t.Fatal(err)
	}
	// 1,000 one-sided taker buys of 0.01 BTC at $70k = $700k notional (only 10 BTC in coins).
	for i := 0; i < 1000; i++ {
		eng.processMarketEvent(&engine.MarketEvent{Kind: 0, SymbolID: 0, Price: 70000, Size: 0.01, Side: 1,
			Timestamp: int64(i) * 1e9})
	}
	sm := eng.getOrCreateSymbolMicro("BTCUSDT")
	if v := sm.VpinCalc.VPIN(); v < 0.7 {
		t.Fatalf("expected one-sided flow to register as toxic (VPIN >= 0.7) after $700k, got %.3f", v)
	}
	if got := vpinNotionalK(70000, 0.01); got != 0.7 {
		t.Fatalf("notional conversion: got %v want 0.7 ($k)", got)
	}
}

func TestVPINBucketPerVenue(t *testing.T) {
	cfg := DefaultDryRunConfig(t.TempDir())
	if got := vpinBucketFor(cfg, "BTCUSDT"); got != vpinBucketUSDK {
		t.Fatalf("default bucket = %v", got)
	}
	cfg.VPINBucketUSDK = CoinbaseVPINBucketsUSDK
	if vpinBucketFor(cfg, "btcusdt") != 1000 || vpinBucketFor(cfg, "ETHUSDT") != 250 || vpinBucketFor(cfg, "XRPUSDT") != vpinBucketUSDK {
		t.Fatal("coinbase bucket mapping wrong")
	}
	cfg.Symbols = []string{"BTCUSDT"}
	e, err := NewDryRunEngine(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bv := e.symMicro["BTCUSDT"].VpinCalc.BucketVol; bv != 1000 {
		t.Fatalf("engine BTC VPIN bucket = %v, want 1000", bv)
	}
}
