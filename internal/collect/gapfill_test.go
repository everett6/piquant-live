package collect

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// synthetic venue: trade id i happens at base + i seconds, price 100+i, size 1, taker buy for even ids.
var gfBase = time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC).UnixMilli()

func gfTime(id int64) int64 { return gfBase + (id-1000)*1000 }

func writeLive(t *testing.T, dir, stream string, ids []int64) {
	t.Helper()
	st, err := newDailyStore(filepath.Join(dir, "ticks", stream))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		ts := gfTime(id)
		if err := st.Add(time.UnixMilli(ts), TradeRow{T: ts, P: 100 + float64(id), S: 1, Side: "b", ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
}

func idRange(a, b int64, skip ...[2]int64) []int64 {
	var out []int64
next:
	for i := a; i <= b; i++ {
		for _, s := range skip {
			if i >= s[0] && i <= s[1] {
				continue next
			}
		}
		out = append(out, i)
	}
	return out
}

// fakeVenues serves Coinbase, Kraken and Binance.US trade history for ids 1000..maxID (all streams share ids).
func fakeVenues(t *testing.T, maxID int64, limit int, hits *int64, fail429 *int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(hits, 1)
		if fail429 != nil && atomic.AddInt64(fail429, -1) >= 0 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/products/BTC-USD/trades":
			after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
			var page []map[string]string
			start := after - 1
			if start > maxID {
				start = maxID
			}
			for id := start; id >= 1000 && len(page) < limit; id-- {
				side := "sell" // maker sold -> taker bought
				if id%2 == 1 {
					side = "buy"
				}
				page = append(page, map[string]string{"trade_id": strconv.FormatInt(id, 10), "side": side,
					"price": fmt.Sprint(100 + id), "size": "1",
					"time": time.UnixMilli(gfTime(id)).UTC().Format(time.RFC3339Nano)})
			}
			// trade_id is a number in the real API
			out := make([]map[string]any, len(page))
			for i, p := range page {
				id, _ := strconv.ParseInt(p["trade_id"], 10, 64)
				out[i] = map[string]any{"trade_id": id, "side": p["side"], "price": p["price"], "size": p["size"], "time": p["time"]}
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.URL.Path == "/0/public/Trades":
			if q.Get("pair") != "BTCUSD" {
				_ = json.NewEncoder(w).Encode(map[string]any{"error": []string{"EQuery:Unknown asset pair"}})
				return
			}
			since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
			var rows [][]any
			last := since
			for id := int64(1000); id <= maxID && len(rows) < limit; id++ {
				ns := gfTime(id) * int64(time.Millisecond)
				if ns <= since {
					continue
				}
				side := "s"
				if id%2 == 0 {
					side = "b"
				}
				rows = append(rows, []any{fmt.Sprint(100 + id), "1", float64(ns) / 1e9, side, "l", "", id})
				last = ns
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"error": []string{},
				"result": map[string]any{"XXBTZUSD": rows, "last": strconv.FormatInt(last, 10)}})
		case r.URL.Path == "/api/v3/historicalTrades":
			from, _ := strconv.ParseInt(q.Get("fromId"), 10, 64)
			var out []map[string]any
			for id := from; id <= maxID && len(out) < limit; id++ {
				out = append(out, map[string]any{"id": id, "price": fmt.Sprint(100 + id), "qty": "1",
					"time": gfTime(id), "isBuyerMaker": id%2 == 1})
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func pointAt(t *testing.T, srv *httptest.Server) {
	t.Helper()
	oc, ok, ob, osp, obo := CoinbaseCandlesBase, KrakenRESTBase, BinanceUSRESTBase, GapFillSpacing, gapFillBackoff
	CoinbaseCandlesBase, KrakenRESTBase, BinanceUSRESTBase = srv.URL, srv.URL, srv.URL
	GapFillSpacing = map[string]time.Duration{}
	gapFillBackoff = time.Millisecond
	t.Cleanup(func() {
		CoinbaseCandlesBase, KrakenRESTBase, BinanceUSRESTBase, GapFillSpacing, gapFillBackoff = oc, ok, ob, osp, obo
	})
}

func fileHashes(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(p)
			out[p] = sha256.Sum256(b)
		}
		return nil
	})
	return out
}

func readBackfill(t *testing.T, path string) []BackfillTradeRow {
	t.Helper()
	var rows []BackfillTradeRow
	if err := ReadRows(path, func(line []byte) error {
		var r BackfillTradeRow
		if err := json.Unmarshal(line, &r); err != nil {
			return err
		}
		rows = append(rows, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

func TestFindTradeGapsWithinAndAcrossDays(t *testing.T) {
	dir := t.TempDir()
	// day 1 (2026-11-19): ids 1000..1100 minus 1010..1019; ids map to seconds, so 86400 ids per day.
	// Put a gap across midnight: last id of day 1 is 1000+86399-5, first of day 2 is 1000+86400+5.
	end1 := int64(1000 + 86399)
	writeLive(t, dir, "BTC-USD", idRange(1000, end1-5, [2]int64{1010, 1019}))
	writeLive(t, dir, "BTC-USD", idRange(end1+6, end1+50))
	gaps, _, err := FindTradeGaps(filepath.Join(dir, "ticks"), filepath.Join(dir, "ticks_backfill"), "BTC-USD", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 2 {
		t.Fatalf("gaps = %+v, want 2", gaps)
	}
	if gaps[0].After != 1009 || gaps[0].Before != 1020 || gaps[0].Missing() != 10 || gaps[0].Day != "2026-11-19" {
		t.Errorf("within-day gap = %+v", gaps[0])
	}
	if gaps[1].After != end1-5 || gaps[1].Before != end1+6 || gaps[1].Day != "2026-11-20" {
		t.Errorf("midnight gap = %+v", gaps[1])
	}
	// the day range filter drops day 2 and with it the midnight gap
	gaps, _, _ = FindTradeGaps(filepath.Join(dir, "ticks"), filepath.Join(dir, "ticks_backfill"), "BTC-USD", "", "2026-11-19")
	if len(gaps) != 1 {
		t.Errorf("to-filtered gaps = %+v, want 1", gaps)
	}
}

func TestBackfillTradeGapsAllVenues(t *testing.T) {
	dir := t.TempDir()
	var hits int64
	srv := fakeVenues(t, 6000, 100, &hits, nil) // small pages force pagination
	pointAt(t, srv)
	// 3 streams, the same id layout: a 350-id gap (several pages) and a 1-id gap.
	layout := idRange(1000, 3000, [2]int64{1200, 1549}, [2]int64{2000, 2000})
	for _, s := range []string{"BTC-USD", "kraken-BTC-USD", "binanceus-BTCUSDT"} {
		writeLive(t, dir, s, layout)
	}
	before := fileHashes(t, filepath.Join(dir, "ticks"))

	dry, err := BackfillTradeGaps(context.Background(), GapFillConfig{Dir: dir, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if hits != 0 || dry.Streams["BTC-USD"].Missing != 351 {
		t.Fatalf("dry run: hits=%d report=%+v", hits, dry.Streams["BTC-USD"])
	}
	if _, err := os.Stat(filepath.Join(dir, "ticks_backfill")); err == nil {
		t.Error("dry run created the backfill tree")
	}

	rep, err := BackfillTradeGaps(context.Background(), GapFillConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"BTC-USD", "kraken-BTC-USD", "binanceus-BTCUSDT"} {
		st := rep.Streams[s]
		if st.Gaps != 2 || st.Missing != 351 || st.Filled != 351 || st.Remaining != 0 || st.Error != "" {
			t.Errorf("%s report = %+v", s, st)
		}
		rows := readBackfill(t, filepath.Join(dir, "ticks_backfill", s, "2026-11-19.jsonl.gz"))
		if len(rows) != 351 || rows[0].ID != 1200 || rows[349].ID != 1549 || rows[350].ID != 2000 {
			t.Fatalf("%s: %d rows, first %+v", s, len(rows), rows[0])
		}
		for _, r := range rows {
			wantSide := "s"
			if r.ID%2 == 0 {
				wantSide = "b"
			}
			if !r.Backfill || r.Src == "" || r.T != gfTime(r.ID) || r.P != 100+float64(r.ID) || r.S != 1 || r.Side != wantSide {
				t.Fatalf("%s: bad row %+v (want side %s)", s, r, wantSide)
			}
		}
	}
	// the live files are byte-for-byte unchanged
	after := fileHashes(t, filepath.Join(dir, "ticks"))
	if len(after) != len(before) {
		t.Fatalf("live file count changed: %d -> %d", len(before), len(after))
	}
	for p, h := range before {
		if after[p] != h {
			t.Errorf("live file changed: %s", p)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "ticks_backfill", "backfill_report.json")); err != nil {
		t.Error("no report written")
	}

	// a rerun finds nothing left and makes no requests
	hits = 0
	rep, err = BackfillTradeGaps(context.Background(), GapFillConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if hits != 0 || rep.Streams["BTC-USD"].Gaps != 0 {
		t.Errorf("rerun: hits=%d report=%+v", hits, rep.Streams["BTC-USD"])
	}
}

func TestBackfillRetriesRateLimitAndReportsUnfillable(t *testing.T) {
	dir := t.TempDir()
	var hits int64
	fails := int64(2)
	srv := fakeVenues(t, 1100, 1000, &hits, &fails) // the venue only knows ids up to 1100
	pointAt(t, srv)
	writeLive(t, dir, "BTC-USD", idRange(1000, 1050, [2]int64{1020, 1029}))
	writeLive(t, dir, "BTC-USD", []int64{1200}) // 1051..1199: venue history ends at 1100
	rep, err := BackfillTradeGaps(context.Background(), GapFillConfig{Dir: dir, Streams: []string{"BTC-USD"}})
	if err != nil {
		t.Fatal(err)
	}
	st := rep.Streams["BTC-USD"]
	if st.Missing != 10+149 || st.Filled != 10+50 || st.Remaining != 99 || st.Error != "" {
		t.Errorf("report = %+v", st)
	}
	if hits < 3 {
		t.Errorf("hits = %d, want the two 429s retried", hits)
	}
}

func TestBackfillSkipsLargeGapsAndRefusesLiveDir(t *testing.T) {
	dir := t.TempDir()
	writeLive(t, dir, "BTC-USD", []int64{1000, 5000})
	var hits int64
	pointAt(t, fakeVenues(t, 6000, 1000, &hits, nil))
	rep, err := BackfillTradeGaps(context.Background(), GapFillConfig{Dir: dir, MaxGap: 100})
	if err != nil {
		t.Fatal(err)
	}
	if st := rep.Streams["BTC-USD"]; st.Skipped != 3999 || st.Filled != 0 || hits != 0 {
		t.Errorf("report = %+v hits=%d", st, hits)
	}
	for _, out := range []string{filepath.Join(dir, "ticks"), filepath.Join(dir, "ticks", "x")} {
		if _, err := BackfillTradeGaps(context.Background(), GapFillConfig{Dir: dir, OutDir: out}); err == nil {
			t.Errorf("out=%s accepted", out)
		}
	}
}

func TestVenueOf(t *testing.T) {
	for in, want := range map[string][2]string{
		"BTC-USD":           {"coinbase", "BTC-USD"},
		"kraken-SOL-USD":    {"kraken", "SOLUSD"},
		"binanceus-ETHUSDT": {"binanceus", "ETHUSDT"},
	} {
		if v, s := venueOf(in); v != want[0] || s != want[1] {
			t.Errorf("venueOf(%s) = %s %s", in, v, s)
		}
	}
}

func TestFindTradeGapsSurvivesDamagedMembers(t *testing.T) {
	dir := t.TempDir()
	writeLive(t, dir, "BTC-USD", idRange(1000, 1099))
	path := filepath.Join(dir, "ticks", "BTC-USD", "2026-11-19.jsonl.gz")
	// a corrupt member in the middle (bad header bytes), then a good member, then zero padding (hard reset)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte{0x1f, 0x8b, 0x08, 0, 0xde, 0xad, 0xbe, 0xef, 1, 2, 3})
	_ = f.Close()
	writeLive(t, dir, "BTC-USD", idRange(1100, 1199, [2]int64{1150, 1159}))
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.Write(make([]byte, 1983))
	_ = f.Close()

	gaps, warn, err := FindTradeGaps(filepath.Join(dir, "ticks"), filepath.Join(dir, "ticks_backfill"), "BTC-USD", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 || gaps[0].After != 1149 || gaps[0].Before != 1160 {
		t.Errorf("gaps = %+v, want only 1149..1160 (no false gap from the damaged member)", gaps)
	}
	if len(warn) != 1 {
		t.Errorf("warnings = %v, want one", warn)
	}
}
