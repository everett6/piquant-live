package paper

import (
	"testing"

	"piquant-live/internal/engine"
)

func TestCoinbasePrimaryFeedPushesBookThenTradeInEngineOrder(t *testing.T) {
	ring := engine.NewSPSCRing(16)
	ing := NewCoinbaseStreamIngestor(DefaultCoinbaseStreamConfig(), ring, nil)
	ing.SetSymbolOrder([]string{"ETHUSDT", "BTCUSDT"}) // BTC is index 1 here, not the hardcoded 0
	ing.HandleStreamPayload([]byte(`{"type":"ticker","product_id":"BTC-USD","price":"100010","side":"buy",` +
		`"last_size":"0.5","best_bid":"100000","best_bid_size":"2","best_ask":"100010","best_ask_size":"3"}`))

	depth, ok := ring.Pop()
	if !ok || depth.Kind != 1 || depth.SymbolID != 1 || depth.Price != 100005 {
		t.Fatalf("depth event = %+v ok=%v", depth, ok)
	}
	trade, ok := ring.Pop()
	if !ok || trade.Kind != 0 || trade.SymbolID != 1 || trade.Side != 1 || trade.Size != 0.5 {
		t.Fatalf("trade event = %+v ok=%v", trade, ok)
	}
	book, ok := ing.GetL2Book("btcusdt")
	if !ok || book.NumBids != 1 || book.Bids[0].Size != 2 || book.Asks[0].Price != 100010 || book.Asks[0].Size != 3 {
		t.Fatalf("book = %+v ok=%v", book, ok)
	}
	var feed MarketFeed = ing // compile-time: Coinbase ingestor satisfies the engine feed interface
	if feed.IsFallback() {
		t.Fatal("coinbase feed never reports fallback")
	}
}

func TestCoinbasePrimaryFeedDropsUnconfiguredSymbols(t *testing.T) {
	ring := engine.NewSPSCRing(16)
	ing := NewCoinbaseStreamIngestor(DefaultCoinbaseStreamConfig(), ring, nil)
	ing.SetSymbolOrder([]string{"BTCUSDT"})
	ing.HandleStreamPayload([]byte(`{"type":"ticker","product_id":"SOL-USD","price":"150","side":"sell",` +
		`"last_size":"1","best_bid":"149.9","best_ask":"150"}`))
	if ev, ok := ring.Pop(); ok {
		t.Fatalf("unexpected event for unconfigured symbol: %+v", ev)
	}
}

func TestNewDryRunEngineNilStreamLeavesFeedNil(t *testing.T) {
	e, err := NewDryRunEngine(DefaultDryRunConfig(t.TempDir()), engine.NewSPSCRing(16), nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.stream != nil {
		t.Fatal("typed-nil stream leaked into MarketFeed interface")
	}
}
