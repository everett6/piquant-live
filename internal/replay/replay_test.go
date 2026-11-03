package replay

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"piquant-live/internal/config"
	"piquant-live/internal/dataset"
	"piquant-live/internal/event"
	"piquant-live/internal/orderbook"
)

func testConfig() *config.Config {
	return &config.Config{
		Symbols: []config.Symbol{{Name: "BTC"}, {Name: "ETH"}},
		Targets: map[string][]string{
			"BTC": {"binance:BTCUSDT", "coinbase:BTC-USD"},
			"ETH": {"binance:ETHUSDT"},
		},
	}
}

func writeCapture(t *testing.T, recs ...*dataset.Record) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	w, err := dataset.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if err := w.Write(r); err != nil {
			t.Fatalf("write %+v: %v", r, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func ts(n int64) int64 { return 1_700_000_000_000_000_000 + n }

func snapRec(venue, instrument string, seq uint64, n int64, bid, ask float64) *dataset.Record {
	return &dataset.Record{
		Version: dataset.Version, Venue: venue, Instrument: instrument, Type: dataset.TypeSnapshot,
		SendTS: ts(n), RecvTS: ts(n), Seq: seq,
		Bids: []dataset.Level{{Price: bid, Qty: 1}, {Price: bid - 0.1, Qty: 2}},
		Asks: []dataset.Level{{Price: ask, Qty: 1}, {Price: ask + 0.1, Qty: 2}},
	}
}

func depthRec(venue, instrument string, seq uint64, n int64, side dataset.Side, levels ...dataset.Level) *dataset.Record {
	rec := &dataset.Record{
		Version: dataset.Version, Venue: venue, Instrument: instrument, Type: dataset.TypeDepth,
		SendTS: ts(n), RecvTS: ts(n), Seq: seq, Side: side,
	}
	if side == dataset.Buy {
		rec.Bids = levels
	} else {
		rec.Asks = levels
	}
	return rec
}

func tradeRec(venue, instrument string, seq uint64, n int64, px, qty float64, side dataset.Side) *dataset.Record {
	return &dataset.Record{
		Version: dataset.Version, Venue: venue, Instrument: instrument, Type: dataset.TypeTrade,
		SendTS: ts(n), RecvTS: ts(n), Seq: seq, Price: px, Qty: qty, Side: side,
	}
}

func run(t *testing.T, path string, opts Options) ([]Step, Stats) {
	t.Helper()
	if opts.Config == nil {
		opts.Config = testConfig()
	}
	r, err := Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var steps []Step
	for {
		st, err := r.Next()
		if errors.Is(err, ErrFinished) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		steps = append(steps, st)
	}
	return steps, r.Stats()
}

func TestReplayDeliversEventsInFileOrder(t *testing.T) {
	path := writeCapture(t,
		snapRec("binance", "BTCUSDT", 1, 0, 100, 100.1),
		depthRec("binance", "BTCUSDT", 2, 1, dataset.Buy, dataset.Level{Price: 100, Qty: 3}),
		tradeRec("binance", "BTCUSDT", 3, 2, 100.05, 0.5, dataset.Buy),
	)
	steps, stats := run(t, path, Options{})
	if len(steps) != 3 || stats.Delivered != 3 {
		t.Fatalf("delivered %d events (%d in stats)", len(steps), stats.Delivered)
	}
	wantKinds := []event.Kind{event.KindSnapshot, event.KindDepth, event.KindTrade}
	for i, st := range steps {
		if st.Env.Kind != wantKinds[i] || !st.Applied {
			t.Fatalf("step %d = %v applied=%v", i, st.Env.Kind, st.Applied)
		}
	}
	if st := stats.Books[0]; st.Status != orderbook.StatusLive || st.Ready != true || st.Updates != 1 || st.Trades != 1 {
		t.Fatalf("book = %+v", st)
	}
	if st := steps[2].Book; st.LastTrade.Qty != event.QtyInt(0.5) {
		t.Fatalf("trade not applied to the book: %+v", st.LastTrade)
	}
	if len(stats.Books) != 1 || stats.Books[0].Instrument != "BTCUSDT" {
		t.Fatalf("books = %+v", stats.Books)
	}
}

// The same file replayed twice must produce identical events and identical
// audit counters. This is the property every experiment ID depends on.
func TestReplayIsDeterministic(t *testing.T) {
	path := writeCapture(t,
		snapRec("binance", "BTCUSDT", 1, 0, 100, 100.1),
		depthRec("binance", "BTCUSDT", 2, 1, dataset.Buy, dataset.Level{Price: 100, Qty: 3}),
		depthRec("binance", "BTCUSDT", 4, 2, dataset.Sell, dataset.Level{Price: 100.1, Qty: 2}), // gap
		snapRec("binance", "BTCUSDT", 5, 3, 100, 100.1),
		tradeRec("binance", "BTCUSDT", 6, 4, 100.05, 1, dataset.Sell),
	)
	a, sa := run(t, path, Options{})
	b, sb := run(t, path, Options{})
	if len(a) != len(b) {
		t.Fatalf("event counts %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Env != b[i].Env || a[i].Applied != b[i].Applied {
			t.Fatalf("event %d differs:\n%+v\n%+v", i, a[i].Env, b[i].Env)
		}
	}
	if sa.Delivered != sb.Delivered || sa.Checksum != sb.Checksum || fmt.Sprint(sa.Books) != fmt.Sprint(sb.Books) {
		t.Fatalf("stats differ:\n%+v\n%+v", sa, sb)
	}
	// Reset must reproduce the run inside one Replay handle too.
	r, err := Open(path, Options{Config: testConfig()})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var first []event.Envelope
	for {
		st, err := r.Next()
		if err != nil {
			break
		}
		first = append(first, st.Env)
	}
	if err := r.Reset(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(first); i++ {
		st, err := r.Next()
		if err != nil {
			t.Fatalf("after reset, event %d: %v", i, err)
		}
		if st.Env != first[i] {
			t.Fatalf("after reset, event %d differs", i)
		}
	}
	if st, err := r.Next(); !errors.Is(err, ErrFinished) {
		t.Fatalf("after reset, extra event %+v err=%v", st, err)
	}
}

// A book that refuses an event on its own audit does not stop the replay, and
// the refusal is visible in the report.
func TestSequenceGapIsVisibleButNotFatal(t *testing.T) {
	path := writeCapture(t,
		snapRec("binance", "BTCUSDT", 1, 0, 100, 100.1),
		depthRec("binance", "BTCUSDT", 5, 1, dataset.Buy, dataset.Level{Price: 100, Qty: 3}),
		snapRec("binance", "BTCUSDT", 9, 2, 100, 100.1),
		depthRec("binance", "BTCUSDT", 10, 3, dataset.Buy, dataset.Level{Price: 100, Qty: 4}),
	)
	steps, stats := run(t, path, Options{})
	if len(steps) != 4 {
		t.Fatalf("delivered %d steps, want 4", len(steps))
	}
	if steps[1].Applied {
		t.Fatal("gap event accepted by the book")
	}
	if stats.RejectedByBook != 1 {
		t.Fatalf("RejectedByBook = %d", stats.RejectedByBook)
	}
	bk := stats.Books[0]
	if bk.Gaps != 1 || bk.Snapshots != 2 || bk.Updates != 1 || bk.Status != orderbook.StatusLive {
		t.Fatalf("book = %+v", bk)
	}
}

// Records the normaliser cannot interpret are counted by cause and skipped.
func TestRejectionsAreCountedByCause(t *testing.T) {
	bad := tradeRec("binance", "BTCUSDT", 2, 1, 100, 1, dataset.Buy)
	bad.Price = 0 // invalid in the record's own validation, so write it raw
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	lines := []string{
		mustJSON(t, snapRec("binance", "BTCUSDT", 1, 0, 100, 100.1)),
		mustJSON(t, tradeRec("binance", "DOGEUSDT", 2, 1, 1, 1, dataset.Buy)), // unconfigured symbol
		mustJSON(t, tradeRec("binance", "BTCUSDT", 3, 2, 1, 1, dataset.Buy)),
		mustJSON(t, &dataset.Record{Version: dataset.Version, Venue: "kraken", Instrument: "XBT/USD",
			Type: dataset.TypeTrade, RecvTS: ts(3), Seq: 1, Price: 1, Qty: 1, Side: dataset.Buy}), // venue not configured
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	steps, stats := run(t, path, Options{})
	if len(steps) != 2 {
		t.Fatalf("delivered %d, want 2", len(steps))
	}
	if stats.Rejected != 2 || stats.Rejects["unknown-symbol"] != 2 {
		t.Fatalf("rejections = %d %v", stats.Rejected, stats.Rejects)
	}
	if !strings.Contains(stats.Summary(), "unknown-symbol=2") {
		t.Fatalf("summary = %q", stats.Summary())
	}
}

func mustJSON(t *testing.T, rec *dataset.Record) string {
	t.Helper()
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Two venues, one symbol: each book is independent and both are reported.
func TestCrossVenueBooksAreIndependent(t *testing.T) {
	path := writeCapture(t,
		snapRec("binance", "BTCUSDT", 1, 0, 100, 100.1),
		snapRec("coinbase", "BTC-USD", 1, 0, 100.02, 100.12),
		depthRec("binance", "BTCUSDT", 2, 1, dataset.Buy, dataset.Level{Price: 100, Qty: 5}),
		depthRec("coinbase", "BTC-USD", 2, 1, dataset.Sell, dataset.Level{Price: 100.12, Qty: 4}),
	)
	steps, stats := run(t, path, Options{})
	if len(steps) != 4 || len(stats.Books) != 2 {
		t.Fatalf("steps=%d books=%+v", len(steps), stats.Books)
	}
	if stats.Books[0].Venue != "binance" || stats.Books[1].Venue != "coinbase" {
		t.Fatalf("books = %+v", stats.Books)
	}
	if steps[2].Book == steps[3].Book {
		t.Fatal("venues share a book")
	}
	if q := steps[2].Book.Levels[orderbook.Bids].Q[0]; q != event.QtyInt(5) {
		t.Fatalf("binance bid = %v", event.QtyFloat(q))
	}
	if q := steps[3].Book.Levels[orderbook.Asks].Q[0]; q != event.QtyInt(4) {
		t.Fatalf("coinbase ask = %v", event.QtyFloat(q))
	}
}

func TestMaxBooks(t *testing.T) {
	path := writeCapture(t,
		snapRec("binance", "BTCUSDT", 1, 0, 100, 100.1),
		snapRec("binance", "ETHUSDT", 1, 0, 10, 10.01),
	)
	r, err := Open(path, Options{Config: testConfig(), MaxBooks: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// The first book opens; the second trips the bound and is counted as a
	// rejection rather than silently sharing a book.
	_, stats := drain(t, r)
	if stats.Rejected != 1 || len(stats.Books) != 1 {
		t.Fatalf("rejected=%d books=%+v", stats.Rejected, stats.Books)
	}
}

func drain(t *testing.T, r *Replay) ([]Step, Stats) {
	t.Helper()
	var steps []Step
	for {
		st, err := r.Next()
		if errors.Is(err, ErrFinished) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		steps = append(steps, st)
	}
	return steps, r.Stats()
}

// Speed paces the replay against the capture's receive timestamps; at speed 0
// it never sleeps.
func TestPacing(t *testing.T) {
	path := writeCapture(t,
		snapRec("binance", "BTCUSDT", 1, 0, 100, 100.1),
		tradeRec("binance", "BTCUSDT", 2, 20_000_000, 100, 1, dataset.Buy), // 20 ms later
	)
	start := time.Now()
	run(t, path, Options{Speed: 1000})
	if el := time.Since(start); el > 10*time.Millisecond {
		t.Fatalf("speed 1000 of a 20 ms gap took %v", el)
	}
	start = time.Now()
	run(t, path, Options{Speed: 10})
	if el := time.Since(start); el < 1*time.Millisecond {
		t.Fatalf("speed 10 of a 20 ms gap took %v, expected ~2 ms", el)
	}
}

func TestChecksumIdentifiesTheBytes(t *testing.T) {
	path := writeCapture(t, tradeRec("binance", "BTCUSDT", 1, 0, 1, 1, dataset.Buy))
	_, stats := run(t, path, Options{})
	sum, err := dataset.Checksum(path)
	if err != nil || stats.Checksum != sum || len(stats.Checksum) != 64 {
		t.Fatalf("checksum %q vs %q (%v)", stats.Checksum, sum, err)
	}
	if !strings.Contains(stats.Summary(), stats.Checksum[:16]) {
		t.Fatalf("summary = %q", stats.Summary())
	}
}

func TestUnknownVenueRejected(t *testing.T) {
	path := writeCapture(t, tradeRec("ftx", "BTC", 1, 0, 1, 1, dataset.Buy))
	_, stats := run(t, path, Options{})
	if stats.Delivered != 0 || stats.Rejected != 1 {
		t.Fatalf("delivered=%d rejected=%d", stats.Delivered, stats.Rejected)
	}
}

func TestOpenRequiresConfig(t *testing.T) {
	path := writeCapture(t, tradeRec("binance", "BTCUSDT", 1, 0, 1, 1, dataset.Buy))
	if _, err := Open(path, Options{}); err == nil {
		t.Fatal("replay opened without a config")
	}
	if _, err := Open(filepath.Join(t.TempDir(), "missing.jsonl"), Options{Config: testConfig()}); err == nil {
		t.Fatal("replay opened a missing file")
	}
}

// A duplicate delivery is recognised and reported, not double-counted as flow.
func TestDuplicatesReported(t *testing.T) {
	path := writeCapture(t,
		snapRec("binance", "BTCUSDT", 1, 0, 100, 100.1),
		tradeRec("binance", "BTCUSDT", 2, 1, 100, 1, dataset.Buy),
		tradeRec("binance", "BTCUSDT", 2, 1, 100, 1, dataset.Buy),
	)
	steps, stats := run(t, path, Options{})
	if len(steps) != 2 || stats.Duplicates != 1 || stats.Rejected != 0 {
		t.Fatalf("steps=%d duplicates=%d rejected=%d", len(steps), stats.Duplicates, stats.Rejected)
	}
	if steps[1].Book.Trades != 1 {
		t.Fatalf("duplicate trade applied: %d trades", steps[1].Book.Trades)
	}
}
