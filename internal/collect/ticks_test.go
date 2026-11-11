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
