package paper

import (
	"math"
	"testing"
	"time"

	"piquant-live/internal/engine"
)

func TestFastParseCoinbaseTicker(t *testing.T) {
	raw := []byte(`{"type":"ticker","sequence":139021537219,"product_id":"BTC-USD","price":"74910.46","open_24h":"75753.64","volume_24h":"4671.23494267","low_24h":"73477","high_24h":"76094.99","volume_30d":"174980.14509405","best_bid":"74910.45","best_bid_size":"0.03608072","best_ask":"74910.46","best_ask_size":"0.07608485","side":"buy","time":"2026-11-09T04:15:04.337220Z","trade_id":1117362273,"last_size":"0.00540543"}`)

	sym, price, bestBid, bestAsk, lastSize, isBuy, ok := FastParseCoinbaseTicker(raw)
	if !ok {
		t.Fatalf("expected FastParseCoinbaseTicker to succeed")
	}
	if sym != "BTCUSDT" {
		t.Errorf("expected BTCUSDT, got %s", sym)
	}
	if math.Abs(price-74910.46) > 1e-4 {
		t.Errorf("expected price 74910.46, got %f", price)
	}
	if math.Abs(bestBid-74910.45) > 1e-4 {
		t.Errorf("expected bestBid 74910.45, got %f", bestBid)
	}
	if math.Abs(bestAsk-74910.46) > 1e-4 {
		t.Errorf("expected bestAsk 74910.46, got %f", bestAsk)
	}
	if math.Abs(lastSize-0.00540543) > 1e-6 {
		t.Errorf("expected lastSize 0.00540543, got %f", lastSize)
	}
	if !isBuy {
		t.Errorf("expected isBuy true, got false")
	}

	// Test ETH-USD sell
	rawETH := []byte(`{"type":"ticker","sequence":104816080907,"product_id":"ETH-USD","price":"2142.31","best_bid":"2142.31","best_ask":"2142.56","side":"sell","last_size":"4.95452788"}`)
	symE, priceE, bestBidE, bestAskE, lastSizeE, isBuyE, okE := FastParseCoinbaseTicker(rawETH)
	if !okE || symE != "ETHUSDT" {
		t.Fatalf("expected ETHUSDT parse, got %s (ok=%v)", symE, okE)
	}
	if math.Abs(priceE-2142.31) > 1e-4 {
		t.Errorf("expected price 2142.31, got %f", priceE)
	}
	if math.Abs(bestBidE-2142.31) > 1e-4 || math.Abs(bestAskE-2142.56) > 1e-4 {
		t.Errorf("expected bid/ask 2142.31/2142.56, got %f/%f", bestBidE, bestAskE)
	}
	if math.Abs(lastSizeE-4.95452788) > 1e-6 {
		t.Errorf("expected lastSize 4.95452788, got %f", lastSizeE)
	}
	if isBuyE {
		t.Errorf("expected isBuy false for sell, got true")
	}
}

func TestNormalizeToUniverseSymbol(t *testing.T) {
	cases := map[string]string{
		"BTC-USD":  "BTCUSDT",
		"ETH-USD":  "ETHUSDT",
		"SOL-USD":  "SOLUSDT",
		"BTC-USDT": "BTCUSDT",
		"BTCUSDT":  "BTCUSDT",
	}
	for in, expected := range cases {
		out := NormalizeToUniverseSymbol(in)
		if out != expected {
			t.Errorf("for %s, expected %s, got %s", in, expected, out)
		}
	}
}

func TestCoinbaseSignalOracleLeadLagAndAdverseFlow(t *testing.T) {
	oracle := NewCoinbaseSignalOracle([]string{"BTCUSDT"})
	now := time.Now()

	// 1. Initial Tick: Coinbase at 75000.00
	oracle.UpdateTick("BTCUSDT", 75000.00, 74999.00, 75001.00, 0.1, true, now.Add(-1*time.Second))

	// 2. Second Tick (1 second later): Coinbase leaps up to 75030.00 (+4.0 bps surge)
	oracle.UpdateTick("BTCUSDT", 75030.00, 75029.00, 75031.00, 0.5, true, now)

	// 3. Evaluate against Binance mid price of 75000.00 (Binance hasn't reacted yet!)
	sig := oracle.EvaluateAgainstBinance("BTCUSDT", 75000.00, now)

	if sig.CoinbasePrice != 75030.00 {
		t.Errorf("expected CoinbasePrice 75030.00, got %f", sig.CoinbasePrice)
	}
	if sig.BasisBps <= 0 {
		t.Errorf("expected positive BasisBps when Coinbase leads upwards, got %f", sig.BasisBps)
	}
	if sig.Velocity1sBps <= 0 {
		t.Errorf("expected positive Velocity1sBps, got %f", sig.Velocity1sBps)
	}
	if sig.LeadLagSkewBps <= 0 {
		t.Errorf("expected positive LeadLagSkewBps, got %f", sig.LeadLagSkewBps)
	}
	// Rapid surge up: IsAdverseAsk must be true to prevent being short-swept
	if !sig.IsAdverseAsk {
		t.Errorf("expected IsAdverseAsk true during upward institutional surge, got false")
	}
	if sig.IsAdverseBid {
		t.Errorf("expected IsAdverseBid false during upward surge, got true")
	}

	// 4. Stale signal expiration test: after 6 seconds with no ticks, skew should clear
	staleSig := oracle.EvaluateAgainstBinance("BTCUSDT", 75000.00, now.Add(6*time.Second))
	if staleSig.LeadLagSkewBps != 0 || staleSig.IsAdverseAsk {
		t.Errorf("expected stale signals to clear, got skew=%f adverseAsk=%v", staleSig.LeadLagSkewBps, staleSig.IsAdverseAsk)
	}
}

func TestCoinbaseStreamIngestorPushToRing(t *testing.T) {
	ring := engine.NewSPSCRing(64)
	cfg := DefaultCoinbaseStreamConfig()
	ingestor := NewCoinbaseStreamIngestor(cfg, ring, nil)

	rawTicker := []byte(`{"type":"ticker","sequence":1001,"product_id":"BTC-USD","price":"75100.50","best_bid":"75100.00","best_ask":"75101.00","side":"buy","last_size":"0.25"}`)
	ingestor.HandleStreamPayload(rawTicker)

	ev, ok := ring.Pop()
	if !ok {
		t.Fatalf("expected MarketEvent popped from ring")
	}
	if ev.Price != 75100.50 {
		t.Errorf("expected price 75100.50, got %f", ev.Price)
	}
	if ev.Size != 0.25 {
		t.Errorf("expected size 0.25, got %f", ev.Size)
	}
	if ev.Flags&VenueCoinbase == 0 {
		t.Errorf("expected VenueCoinbase flag set, got %d", ev.Flags)
	}
	if ev.SymbolID != 0 {
		t.Errorf("expected BTCUSDT symbolID 0, got %d", ev.SymbolID)
	}

	snap := ingestor.Oracle().Snapshot()
	btcSig := snap.Symbols["BTCUSDT"]
	if btcSig == nil || btcSig.CoinbasePrice != 75100.50 {
		t.Errorf("expected oracle updated with BTC price, got %+v", btcSig)
	}
}
