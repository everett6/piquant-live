package dataset

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const (
	tradeLine = `{"v":1,"venue":"binance","instrument":"BTCUSDT","type":"trade","send_ts":1000,"recv_ts":1001,"seq":7,"price":100,"qty":2,"side":"buy","trade_id":9}`
	snapLine  = `{"v":1,"venue":"binance","instrument":"BTCUSDT","type":"snapshot","send_ts":2000,"recv_ts":2001,"seq":8,"bids":[{"price":100,"qty":1},{"price":99.9,"qty":2}],"asks":[{"price":100.1,"qty":1}]}`
)

func readAll(t *testing.T, path string) ([]Record, Quality) {
	t.Helper()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var out []Record
	for {
		rec, err := r.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("Next: %v", err)
		}
		out = append(out, *rec)
	}
	return out, r.Quality()
}

func TestReadTradesAndSnapshots(t *testing.T) {
	path := writeFile(t, tradeLine, snapLine)
	recs, q := readAll(t, path)
	if len(recs) != 2 || q.Records != 2 || q.Rejected != 0 {
		t.Fatalf("records=%d quality=%+v", len(recs), q)
	}
	if recs[0].Type != TypeTrade || recs[0].Price != 100 || recs[0].Side != Buy || recs[0].Seq != 7 {
		t.Fatalf("trade = %+v", recs[0])
	}
	if recs[1].Type != TypeSnapshot || len(recs[1].Bids) != 2 || len(recs[1].Asks) != 1 {
		t.Fatalf("snapshot = %+v", recs[1])
	}
	if q.FirstRecv != 1001 || q.LastRecv != 2001 {
		t.Fatalf("time bounds = %d..%d", q.FirstRecv, q.LastRecv)
	}
}

// A malformed line is reported, not fatal: one corrupt record in a 4 GiB
// capture must not stop the replay.
func TestMalformedLinesAreRecordedAndSkipped(t *testing.T) {
	path := writeFile(t,
		tradeLine,
		`{"v":1,"venue":`,
		`{"v":2,"venue":"binance","instrument":"BTC","type":"heartbeat","recv_ts":5}`,
		`{"v":1,"venue":"binance","instrument":"BTCUSDT","type":"trade","recv_ts":5,"price":-1,"qty":1,"side":"buy"}`,
		`{"v":1,"venue":"binance","instrument":"BTCUSDT","type":"bogus","recv_ts":5}`,
		snapLine,
	)
	recs, q := readAll(t, path)
	if len(recs) != 2 || q.Records != 2 {
		t.Fatalf("accepted %d records, want 2", len(recs))
	}
	if q.Rejected != 4 || len(q.Rejections) != 4 {
		t.Fatalf("rejected = %d (%d kept)", q.Rejected, len(q.Rejections))
	}
	// Rejections carry their line numbers so the capture can be repaired.
	wantLines := []int{2, 3, 4, 5}
	for i, rej := range q.Rejections {
		if rej.Line != wantLines[i] {
			t.Fatalf("rejection %d on line %d, want %d (%s)", i, rej.Line, wantLines[i], rej.Err)
		}
	}
	if !strings.Contains(q.Describe(), "line 2") {
		t.Fatalf("Describe = %q", q.Describe())
	}
}

func TestValidationRules(t *testing.T) {
	base := Record{Version: Version, Venue: "binance", Instrument: "BTCUSDT", RecvTS: 1}
	cases := map[string]struct {
		mut  func(*Record)
		want string
	}{
		"missing venue":  {func(r *Record) { r.Venue = "" }, "venue"},
		"missing symbol": {func(r *Record) { r.Instrument = "" }, "instrument"},
		"no timestamp":   {func(r *Record) { r.RecvTS, r.SendTS = 0, 0 }, "timestamp"},
		"negative ts":    {func(r *Record) { r.RecvTS = -1 }, "negative"},
		"bad version":    {func(r *Record) { r.Version = 9 }, "version"},
		"unknown type":   {func(r *Record) { r.Type = "x" }, "record type"},
		"trade price":    {func(r *Record) { r.Type, r.Price, r.Qty, r.Side = TypeTrade, 0, 1, Buy }, "price/qty"},
		"trade side":     {func(r *Record) { r.Type, r.Price, r.Qty = TypeTrade, 1, 1 }, "side"},
		"depth side":     {func(r *Record) { r.Type = TypeDepth }, "side"},
		"depth empty":    {func(r *Record) { r.Type, r.Side = TypeDepth, Buy }, "no levels"},
		"depth negative": {func(r *Record) { r.Type, r.Side, r.Bids = TypeDepth, Buy, []Level{{Price: 1, Qty: -1}} }, "negative qty"},
		"snapshot empty": {func(r *Record) { r.Type = TypeSnapshot }, "empty side"},
		"snapshot crossed": {func(r *Record) {
			r.Type = TypeSnapshot
			r.Bids, r.Asks = []Level{{Price: 101, Qty: 1}}, []Level{{Price: 100, Qty: 1}}
		}, "crossed"},
		"snapshot unsorted": {func(r *Record) {
			r.Type = TypeSnapshot
			r.Bids = []Level{{Price: 99, Qty: 1}, {Price: 100, Qty: 1}}
			r.Asks = []Level{{Price: 101, Qty: 1}}
		}, "descending"},
		"snapshot ask order": {func(r *Record) {
			r.Type = TypeSnapshot
			r.Bids = []Level{{Price: 99, Qty: 1}}
			r.Asks = []Level{{Price: 101, Qty: 1}, {Price: 100, Qty: 1}}
		}, "ascending"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := base
			tc.mut(&r)
			err := r.Validate()
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want it to mention %q", err, tc.want)
			}
		})
	}
	hb := Record{Version: Version, Venue: "v", Instrument: "i", Type: TypeHeartbeat, SendTS: 1}
	if err := hb.Validate(); err != nil {
		t.Fatalf("heartbeat with only a send timestamp rejected: %v", err)
	}
	// A zero-size level is legal (it means "level absent" in a snapshot).
	lvl := Record{Version: Version, Venue: "v", Instrument: "i", Type: TypeSnapshot, RecvTS: 1,
		Bids: []Level{{Price: 100, Qty: 0}}, Asks: []Level{{Price: 101, Qty: 1}}}
	if err := lvl.Validate(); err != nil {
		t.Fatalf("zero-size level rejected: %v", err)
	}
}

// A capture cut short by a power failure ends in a partial line. That is
// reported as a rejection, and every complete record before it is readable.
func TestTruncatedTailIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cut.jsonl")
	body := tradeLine + "\n" + snapLine + "\n" + `{"v":1,"venue":"binance","inst`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	recs, q := readAll(t, path)
	if len(recs) != 2 || q.Rejected != 1 {
		t.Fatalf("records=%d rejected=%d", len(recs), q.Rejected)
	}
}

func TestReorderedTimestampsCounted(t *testing.T) {
	late := strings.Replace(tradeLine, `"recv_ts":1001`, `"recv_ts":5000`, 1)
	early := strings.Replace(tradeLine, `"recv_ts":1001`, `"recv_ts":4000`, 1)
	recs, q := readAll(t, writeFile(t, late, early))
	if len(recs) != 2 || q.Reordered != 1 {
		t.Fatalf("records=%d reordered=%d", len(recs), q.Reordered)
	}
	// The bounds stay the maximum seen, not the last record's timestamp.
	if q.LastRecv != 5000 {
		t.Fatalf("LastRecv = %d", q.LastRecv)
	}
}

func TestWriteThenReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := Record{Venue: "coinbase", Instrument: "BTC-USD", Type: TypeTrade,
		SendTS: 10, RecvTS: 11, Seq: 3, Price: 100.25, Qty: 0.5, Side: Sell, TradeID: 4}
	if err := w.Write(&rec); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(&Record{Venue: "x", Instrument: "y", Type: TypeTrade, RecvTS: 1}); err == nil {
		t.Fatal("invalid record written")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	recs, q := readAll(t, path)
	if len(recs) != 1 || q.Rejected != 0 {
		t.Fatalf("records=%d rejected=%d", len(recs), q.Rejected)
	}
	if got := recs[0]; got.Version != Version || got.Price != 100.25 || got.Side != Sell || got.Seq != 3 {
		t.Fatalf("round trip = %+v", got)
	}
	sum, err := Checksum(path)
	if err != nil || len(sum) != 64 {
		t.Fatalf("checksum = %q, %v", sum, err)
	}
	// The checksum is stable and identifies the bytes.
	again, _ := Checksum(path)
	if again != sum {
		t.Fatal("checksum not stable")
	}
}

func TestOpenMissingFile(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("opened a missing file")
	}
	if _, err := Checksum(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("checksummed a missing file")
	}
}

func BenchmarkReadTrade(b *testing.B) {
	path := filepath.Join(b.TempDir(), "bench.jsonl")
	body := strings.Repeat(tradeLine+"\n", 10000)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := Open(path)
		if err != nil {
			b.Fatal(err)
		}
		n := 0
		for {
			if _, err := r.Next(); err != nil {
				break
			}
			n++
		}
		r.Close()
		if n != 10000 {
			b.Fatalf("read %d", n)
		}
	}
}
