package normalize

import (
	"errors"
	"testing"

	"piquant-live/internal/config"
	"piquant-live/internal/dataset"
	"piquant-live/internal/event"
)

func testConfig() *config.Config {
	return &config.Config{
		Symbols: []config.Symbol{{Name: "BTC"}, {Name: "ETH"}},
		Targets: map[string][]string{
			"BTC": {"binance:BTCUSDT", "coinbase:BTC-USD", "kraken:XBT/USD"},
			"ETH": {"binance:ETHUSDT", "coinbase:ETH-USD"},
		},
	}
}

func mustNormalizer(t *testing.T, venue string) *Normalizer {
	t.Helper()
	n, err := New(venue, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func baseRecord(venue, instrument string, typ dataset.RecordType) *dataset.Record {
	return &dataset.Record{
		Version: dataset.Version, Venue: venue, Instrument: instrument, Type: typ,
		SendTS: 1e18, RecvTS: 1e18 + 1,
	}
}

func TestTradeConversion(t *testing.T) {
	n := mustNormalizer(t, Binance)
	rec := baseRecord(Binance, "BTCUSDT", dataset.TypeTrade)
	rec.Price, rec.Qty, rec.Side, rec.Seq, rec.TradeID = 65000.25, 0.5, dataset.Buy, 1, 42
	env, err := n.Envelope(rec)
	if err != nil {
		t.Fatal(err)
	}
	if env.Kind != event.KindTrade || env.Symbol != config.SymbolID("BTC") || env.Venue != config.VenueID("binance") {
		t.Fatalf("envelope = %+v", env)
	}
	if env.Trade.Price != event.PriceInt(65000.25) || env.Trade.Qty != event.QtyInt(0.5) {
		t.Fatalf("trade = %+v", env.Trade)
	}
	if env.Trade.Side != event.SideBuy || env.Trade.TradeID != 42 {
		t.Fatalf("trade side/id = %v/%d", env.Trade.Side, env.Trade.TradeID)
	}
	if env.RecvTime != 1e18+1 || env.SendTime != 1e18 {
		t.Fatalf("times = %d/%d", env.SendTime, env.RecvTime)
	}
	// Sell side.
	rec.Side, rec.Seq = dataset.Sell, 2
	env, err = n.Envelope(rec)
	if err != nil || env.Trade.Side != event.SideSell {
		t.Fatalf("sell: %v %+v", err, env)
	}
}

func TestDepthConversion(t *testing.T) {
	n := mustNormalizer(t, Binance)
	rec := baseRecord(Binance, "BTCUSDT", dataset.TypeDepth)
	rec.Seq, rec.Side = 5, dataset.Sell
	rec.Asks = []dataset.Level{{Price: 100.1, Qty: 2}, {Price: 100.2, Qty: 0}}
	env, err := n.Envelope(rec)
	if err != nil {
		t.Fatal(err)
	}
	if env.Kind != event.KindDepth || env.Depth.Side != event.SideSell || env.Depth.NUpdates != 2 {
		t.Fatalf("depth = %+v", env.Depth)
	}
	if env.Depth.Updates[0].Price != event.PriceInt(100.1) || env.Depth.Updates[1].Qty != 0 {
		t.Fatalf("levels = %+v", env.Depth.Updates[:2])
	}
}

func TestSnapshotConversion(t *testing.T) {
	n := mustNormalizer(t, Coinbase)
	rec := baseRecord(Coinbase, "BTC-USD", dataset.TypeSnapshot)
	rec.Seq = 9
	rec.Bids = []dataset.Level{{Price: 100, Qty: 1}, {Price: 99.9, Qty: 2}}
	rec.Asks = []dataset.Level{{Price: 100.1, Qty: 1}}
	env, err := n.Envelope(rec)
	if err != nil {
		t.Fatal(err)
	}
	if env.Kind != event.KindSnapshot || env.Snap.NBids != 2 || env.Snap.NAsks != 1 {
		t.Fatalf("snapshot = %+v", env.Snap)
	}
	if env.Venue != config.VenueID("coinbase") {
		t.Fatalf("venue = %d", env.Venue)
	}
}

// Kraken instruments are spelled differently and must be mapped by config.
func TestKrakenInstrumentMapping(t *testing.T) {
	n := mustNormalizer(t, Kraken)
	rec := baseRecord(Kraken, "XBT/USD", dataset.TypeTrade)
	rec.Price, rec.Qty, rec.Side, rec.Seq = 100, 1, dataset.Sell, 1
	env, err := n.Envelope(rec)
	if err != nil {
		t.Fatal(err)
	}
	if env.Symbol != config.SymbolID("BTC") || env.Venue != config.VenueID("kraken") {
		t.Fatalf("symbol/venue = %d/%d", env.Symbol, env.Venue)
	}
}

func TestUnknownVenueAndSymbol(t *testing.T) {
	if _, err := New("ftx", testConfig()); err == nil {
		t.Fatal("unsupported venue accepted")
	}
	// A venue the config has no targets for cannot be built.
	cfg := testConfig()
	cfg.Targets = map[string][]string{"BTC": {"binance:BTCUSDT"}}
	if _, err := New(Kraken, cfg); err == nil {
		t.Fatal("normalizer built for a venue with no configured instruments")
	}
	n := mustNormalizer(t, Binance)
	rec := baseRecord(Binance, "DOGEUSDT", dataset.TypeTrade)
	rec.Price, rec.Qty, rec.Side, rec.Seq = 1, 1, dataset.Buy, 1
	_, err := n.Envelope(rec)
	var re *RejectError
	if !errors.As(err, &re) || re.Kind != KindUnknownSymbol {
		t.Fatalf("err = %v, want unknown-symbol", err)
	}
	n.CountReject(err)
	if n.Rejections()[KindUnknownSymbol] != 1 {
		t.Fatalf("rejections = %v", n.Rejections())
	}
	// A record from another venue is refused rather than misread.
	rec = baseRecord(Coinbase, "BTCUSDT", dataset.TypeTrade)
	rec.Price, rec.Qty, rec.Side, rec.Seq = 1, 1, dataset.Buy, 1
	if _, err := n.Envelope(rec); err == nil {
		t.Fatal("record from another venue accepted")
	}
}

func TestTimestampRules(t *testing.T) {
	n := mustNormalizer(t, Binance)
	seq := uint64(0)
	mk := func(send, recv int64) *dataset.Record {
		rec := baseRecord(Binance, "BTCUSDT", dataset.TypeTrade)
		seq++
		rec.SendTS, rec.RecvTS, rec.Seq = send, recv, seq
		rec.Price, rec.Qty, rec.Side = 1, 1, dataset.Buy
		return rec
	}
	// Missing local timestamp: the venue time is used.
	env, err := n.Envelope(mk(1e18, 0))
	if err != nil || env.RecvTime != 1e18 {
		t.Fatalf("recv fallback: %v %+v", err, env)
	}
	// Missing venue timestamp: the local time is used for both.
	env, err = n.Envelope(mk(0, 1e18))
	if err != nil || env.SendTime != 1e18 {
		t.Fatalf("send fallback: %v %+v", err, env)
	}
	// Neither: rejected.
	if _, err := n.Envelope(mk(0, 0)); err == nil {
		t.Fatal("record with no timestamp accepted")
	}
	// A seconds-resolution timestamp is a units error, not a 1969 print.
	_, err = n.Envelope(mk(0, 1_700_000_000))
	var re *RejectError
	if !errors.As(err, &re) || re.Kind != KindBadTimestamp {
		t.Fatalf("err = %v, want bad-timestamp", err)
	}
}

// Binance repeats are dropped by the documented sequence rule; a gap is not a
// rejection there, because the book's own audit handles it.
func TestBinanceSequenceRules(t *testing.T) {
	n := mustNormalizer(t, Binance)
	rec := baseRecord(Binance, "BTCUSDT", dataset.TypeTrade)
	rec.Price, rec.Qty, rec.Side = 1, 1, dataset.Buy
	for _, seq := range []uint64{10, 11, 11, 9} {
		rec.Seq = seq
		env, err := n.Envelope(rec)
		if err != nil {
			t.Fatalf("seq %d: %v", seq, err)
		}
		wantDup := seq == 11 && env == nil || seq == 9
		if wantDup != (env == nil) {
			t.Fatalf("seq %d: envelope=%v, want duplicate=%v", seq, env != nil, wantDup)
		}
	}
	if n.Duplicates() != 2 {
		t.Fatalf("duplicates = %d, want 2", n.Duplicates())
	}
	if len(n.SequenceGaps()) != 0 {
		t.Fatalf("binance gaps = %v, want none", n.SequenceGaps())
	}
}

func TestCoinbaseSequenceRules(t *testing.T) {
	n := mustNormalizer(t, Coinbase)
	rec := baseRecord(Coinbase, "BTC-USD", dataset.TypeTrade)
	rec.Price, rec.Qty, rec.Side = 1, 1, dataset.Buy
	rec.Seq = 5
	if _, err := n.Envelope(rec); err != nil {
		t.Fatal(err)
	}
	// An exact repeat is a duplicate.
	rec.Seq = 5
	if env, err := n.Envelope(rec); err != nil || env != nil {
		t.Fatalf("repeat: env=%v err=%v", env, err)
	}
	// A jump is a counted gap but still delivered: the book decides.
	rec.Seq = 9
	if env, err := n.Envelope(rec); err != nil || env == nil {
		t.Fatalf("gap: env=%v err=%v", env, err)
	}
	if got := n.SequenceGaps()[config.SymbolID("BTC")]; got != 1 {
		t.Fatalf("gaps = %v", n.SequenceGaps())
	}
	// An out-of-order message is delivered and counted, so the book can mark
	// itself stale instead of quietly accepting it.
	rec.Seq = 7
	if env, err := n.Envelope(rec); err != nil || env == nil {
		t.Fatalf("out of order: env=%v err=%v", env, err)
	}
	if got := n.SequenceGaps()[config.SymbolID("BTC")]; got != 2 {
		t.Fatalf("gaps = %v, want 2", n.SequenceGaps())
	}
}

func TestMalformedValuesRejected(t *testing.T) {
	n := mustNormalizer(t, Binance)
	rec := baseRecord(Binance, "BTCUSDT", dataset.TypeTrade)
	rec.Seq, rec.Qty, rec.Side = 1, 1, dataset.Buy
	for _, price := range []float64{0, -1, 1e12} {
		rec.Price = price
		if _, err := n.Envelope(rec); err == nil {
			t.Fatalf("price %g accepted", price)
		}
	}
	rec.Price = 1
	rec.Qty = 1e10
	if _, err := n.Envelope(rec); err == nil {
		t.Fatal("huge size accepted")
	}
}

func TestZeroSequenceRejected(t *testing.T) {
	n := mustNormalizer(t, Binance)
	rec := baseRecord(Binance, "BTCUSDT", dataset.TypeTrade)
	rec.Price, rec.Qty, rec.Side = 1, 1, dataset.Buy
	if _, err := n.Envelope(rec); err == nil {
		t.Fatal("record with no sequence accepted")
	}
}

func TestHeartbeatSkipped(t *testing.T) {
	n := mustNormalizer(t, Binance)
	if env, err := n.Envelope(baseRecord(Binance, "BTCUSDT", dataset.TypeHeartbeat)); env != nil || err != nil {
		t.Fatalf("heartbeat: %v %v", env, err)
	}
}

func TestDepthTooDeepIsTruncatedNotRejected(t *testing.T) {
	n := mustNormalizer(t, Binance)
	rec := baseRecord(Binance, "BTCUSDT", dataset.TypeDepth)
	rec.Seq, rec.Side = 1, dataset.Buy
	for i := 0; i < event.MaxLevels+5; i++ {
		rec.Bids = append(rec.Bids, dataset.Level{Price: 100 - float64(i)*0.1, Qty: 1})
	}
	env, err := n.Envelope(rec)
	if err != nil {
		t.Fatal(err)
	}
	if env.Depth.NUpdates != event.MaxLevels {
		t.Fatalf("updates = %d, want %d", env.Depth.NUpdates, event.MaxLevels)
	}
}

func TestSnapshotShapeRejected(t *testing.T) {
	n := mustNormalizer(t, Binance)
	rec := baseRecord(Binance, "BTCUSDT", dataset.TypeSnapshot)
	rec.Seq = 1
	rec.Bids = []dataset.Level{{Price: 99, Qty: 1}, {Price: 100, Qty: 1}}
	rec.Asks = []dataset.Level{{Price: 101, Qty: 1}}
	_, err := n.Envelope(rec)
	var re *RejectError
	if !errors.As(err, &re) || re.Kind != KindSnapshotShape {
		t.Fatalf("err = %v, want snapshot-shape", err)
	}
}

func TestInstrumentKeyIsVenueAware(t *testing.T) {
	if instrumentKey(Binance, " btcusdt ") != "BTCUSDT" {
		t.Fatal("binance key")
	}
	if instrumentKey(Coinbase, "btc-usd") != "BTC-USD" {
		t.Fatal("coinbase key")
	}
}
