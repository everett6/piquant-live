package collect

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestRecorder(t *testing.T) *TickRecorder {
	t.Helper()
	r, err := NewTickRecorder(TickConfig{Dir: t.TempDir(), Products: []string{"BTC-USD"}, Depth: 2, MinFreeGB: 0.001})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTopN(t *testing.T) {
	lv := map[float64]float64{100: 1, 101: 2, 99: 3, 102: 4, 98: 5}
	b := topN(lv, 3, true)
	a := topN(lv, 3, false)
	if len(b) != 3 || b[0][0] != 102 || b[1][0] != 101 || b[2][0] != 100 {
		t.Fatalf("bids %v", b)
	}
	if len(a) != 3 || a[0][0] != 98 || a[1][0] != 99 || a[2] != [2]float64{100, 1} {
		t.Fatalf("asks %v", a)
	}
	if got := topN(map[float64]float64{1: 1}, 3, true); len(got) != 1 {
		t.Fatalf("short book %v", got)
	}
}

func TestBookSnapshotAndUpdates(t *testing.T) {
	r := newTestRecorder(t)
	now := time.Unix(1700000000, 0)
	// updates before a snapshot are ignored
	_ = r.HandleMessage([]byte(`{"type":"l2update","product_id":"BTC-USD","changes":[["buy","50","1"]]}`), now)
	_ = r.HandleMessage([]byte(`{"type":"snapshot","product_id":"BTC-USD","bids":[["100.5","1.5"],["100","2"]],"asks":[["101","1"],["102","3"]]}`), now)
	_ = r.HandleMessage([]byte(`{"type":"l2update","product_id":"BTC-USD","changes":[["buy","100.8","0.7"],["sell","101","0"],["sell","101.5","4"]]}`), now)
	b := r.book["BTC-USD"]
	if _, ok := b.bids[50]; ok {
		t.Fatal("pre-snapshot update applied")
	}
	r.snapshotBooks(now)
	r.flush()
	var rows int
	err := ReadRows(filepath.Join(r.cfg.Dir, "book", "BTC-USD", "2023-11-14.jsonl.gz"), func(line []byte) error {
		rows++
		if !bytes.Contains(line, []byte(`"b":[[100.8,0.7],[100.5,1.5]]`)) || !bytes.Contains(line, []byte(`"a":[[101.5,4],[102,3]]`)) ||
			!bytes.Contains(line, []byte(`"upd":3`)) {
			t.Errorf("row %s", line)
		}
		return nil
	})
	if err != nil || rows != 1 {
		t.Fatalf("rows=%d err=%v", rows, err)
	}
	if _, err := os.Stat(filepath.Join(r.cfg.Dir, "ticks_status.json")); err != nil {
		t.Fatal(err)
	}
}

func TestMatchTakerSideAndGaps(t *testing.T) {
	r := newTestRecorder(t)
	now := time.Now()
	msgs := []string{
		`{"type":"match","product_id":"BTC-USD","trade_id":10,"time":"2023-11-14T22:13:20.5Z","price":"100","size":"0.5","side":"sell"}`,
		`{"type":"match","product_id":"BTC-USD","trade_id":13,"time":"2023-11-14T22:13:21Z","price":"99","size":"1","side":"buy"}`,
		`{"type":"last_match","product_id":"BTC-USD","trade_id":9,"price":"1","size":"1","side":"buy"}`,
		`{"type":"match","product_id":"ETH-USD","trade_id":1,"price":"1","size":"1","side":"buy"}`,
	}
	for _, m := range msgs {
		if err := r.HandleMessage([]byte(m), now); err != nil {
			t.Fatal(err)
		}
	}
	if r.status.Trades["BTC-USD"] != 2 || r.status.TradeGaps["BTC-USD"] != 2 {
		t.Fatalf("status %+v", r.status)
	}
	r.flush()
	var lines [][]byte
	_ = ReadRows(filepath.Join(r.cfg.Dir, "ticks", "BTC-USD", "2023-11-14.jsonl.gz"), func(l []byte) error {
		lines = append(lines, append([]byte(nil), l...))
		return nil
	})
	if len(lines) != 2 || !bytes.Contains(lines[0], []byte(`"t":1700000000500`)) ||
		!bytes.Contains(lines[0], []byte(`"side":"b"`)) || !bytes.Contains(lines[1], []byte(`"side":"s"`)) {
		t.Fatalf("lines %s", lines)
	}
}

func TestLevel2ErrorFallsBackToTrades(t *testing.T) {
	r := newTestRecorder(t)
	err := r.HandleMessage([]byte(`{"type":"error","message":"Failed to subscribe","reason":"level2_batch requires auth"}`), time.Now())
	if err == nil || !r.noLevel {
		t.Fatalf("err=%v noLevel=%v", err, r.noLevel)
	}
}

func frame(fin bool, op byte, payload []byte) []byte {
	h := op
	if fin {
		h |= 0x80
	}
	out := []byte{h}
	switch n := len(payload); {
	case n <= 125:
		out = append(out, byte(n))
	case n <= 0xFFFF:
		out = append(out, 126, byte(n>>8), byte(n))
	default:
		out = append(out, 127)
		out = binary.BigEndian.AppendUint64(out, uint64(n))
	}
	return append(out, payload...)
}

func TestReadFrameFragmentsAndSizes(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 70000)
	var buf bytes.Buffer
	buf.Write(frame(false, 0x1, []byte("hel")))
	buf.Write(frame(true, 0x0, []byte("lo")))
	buf.Write(frame(true, 0x1, big))
	fin, op, p, err := readFrame(&buf)
	if err != nil || fin || op != 1 || string(p) != "hel" {
		t.Fatal(fin, op, string(p), err)
	}
	fin, op, p, err = readFrame(&buf)
	if err != nil || !fin || op != 0 || string(p) != "lo" {
		t.Fatal(fin, op, string(p), err)
	}
	_, _, p, err = readFrame(&buf)
	if err != nil || len(p) != 70000 {
		t.Fatal(len(p), err)
	}
}

func newMultiRecorder(t *testing.T) *TickRecorder {
	t.Helper()
	r, err := NewTickRecorder(TickConfig{Dir: t.TempDir(), BinanceUS: []string{"BTCUSDT"}, Kraken: []string{"BTC/USD"},
		Depth: 2, MinFreeGB: 0.001})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBinanceTradeAndDepth(t *testing.T) {
	r := newMultiRecorder(t)
	now := time.Now()
	msgs := []string{
		`{"stream":"btcusdt@trade","data":{"e":"trade","p":"100.5","q":"0.2","T":1700000000000,"m":true,"t":7}}`,
		`{"stream":"btcusdt@trade","data":{"e":"trade","p":"100.6","q":"0.1","T":1700000000100,"m":false,"t":9}}`,
		`{"stream":"btcusdt@depth10@1000ms","data":{"bids":[["100.4","1"],["100.3","2"],["100.2","3"]],"asks":[["100.6","1"],["100.7","2"]]}}`,
	}
	for _, m := range msgs {
		if err := r.handleBinance([]byte(m), now); err != nil {
			t.Fatal(err)
		}
	}
	k := "binanceus-BTCUSDT"
	if r.status.Trades[k] != 2 || r.status.TradeGaps[k] != 1 {
		t.Fatalf("status %+v", r.status)
	}
	r.snapshotBooks(time.Unix(1700000000, 0))
	r.flush()
	var trades, books [][]byte
	_ = ReadRows(filepath.Join(r.cfg.Dir, "ticks", k, "2023-11-14.jsonl.gz"), func(l []byte) error {
		trades = append(trades, append([]byte(nil), l...))
		return nil
	})
	_ = ReadRows(filepath.Join(r.cfg.Dir, "book", k, "2023-11-14.jsonl.gz"), func(l []byte) error {
		books = append(books, append([]byte(nil), l...))
		return nil
	})
	if len(trades) != 2 || !bytes.Contains(trades[0], []byte(`"side":"s"`)) || !bytes.Contains(trades[1], []byte(`"side":"b"`)) {
		t.Fatalf("trades %s", trades)
	}
	if len(books) != 1 || !bytes.Contains(books[0], []byte(`"b":[[100.4,1],[100.3,2]]`)) || !bytes.Contains(books[0], []byte(`"upd":1`)) {
		t.Fatalf("books %s", books)
	}
}

func TestKrakenTradeAndBookTruncation(t *testing.T) {
	r := newMultiRecorder(t)
	now := time.Now()
	msgs := []string{
		`{"channel":"trade","type":"update","data":[{"symbol":"BTC/USD","side":"sell","price":100.1,"qty":0.5,"trade_id":11,"timestamp":"2023-11-14T22:13:20Z"},{"symbol":"BTC/USD","side":"buy","price":100.2,"qty":0.1,"trade_id":12,"timestamp":"2023-11-14T22:13:21Z"}]}`,
		`{"channel":"book","type":"snapshot","data":[{"symbol":"BTC/USD","bids":[{"price":100.0,"qty":1},{"price":99.9,"qty":2}],"asks":[{"price":100.2,"qty":1},{"price":100.3,"qty":2}]}]}`,
		// a better bid pushes 99.9 past depth 2 (Kraken does not send its delete); then 100.05 is removed
		`{"channel":"book","type":"update","data":[{"symbol":"BTC/USD","bids":[{"price":100.05,"qty":3}],"asks":[]}]}`,
		`{"channel":"book","type":"update","data":[{"symbol":"BTC/USD","bids":[{"price":100.05,"qty":0},{"price":99.8,"qty":4}],"asks":[]}]}`,
	}
	for _, m := range msgs {
		if err := r.handleKraken([]byte(m), now); err != nil {
			t.Fatal(err)
		}
	}
	k := "kraken-BTC-USD"
	b := r.book[k]
	if _, stale := b.bids[99.9]; stale {
		t.Fatal("level beyond depth was kept")
	}
	top := topN(b.bids, 2, true)
	if len(top) != 2 || top[0][0] != 100.0 || top[1][0] != 99.8 {
		t.Fatalf("bids %v", top)
	}
	if r.status.Trades[k] != 2 {
		t.Fatalf("trades %d", r.status.Trades[k])
	}
	if err := r.handleKraken([]byte(`{"method":"subscribe","success":false,"error":"bad symbol"}`), now); err == nil {
		t.Fatal("subscribe failure ignored")
	}
}
