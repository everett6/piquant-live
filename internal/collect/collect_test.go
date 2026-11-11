package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func readAll(t *testing.T, path string) []string {
	t.Helper()
	var lines []string
	if err := ReadRows(path, func(b []byte) error { lines = append(lines, string(b)); return nil }); err != nil {
		t.Fatalf("ReadRows: %v", err)
	}
	return lines
}

func TestDailyStore_AppendsMembersAndToleratesTruncation(t *testing.T) {
	dir := t.TempDir()
	s, err := newDailyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		_ = s.Add(day, map[string]int{"i": i})
		if err := s.Flush(); err != nil { // one gzip member per flush
			t.Fatal(err)
		}
	}
	_ = s.Add(day.Add(24*time.Hour), map[string]int{"i": 99})
	_ = s.Flush()
	path := filepath.Join(dir, "2026-11-10.jsonl.gz")
	if got := readAll(t, path); len(got) != 3 || got[2] != `{"i":2}` {
		t.Fatalf("got %v", got)
	}
	if s.Total() != 4 {
		t.Fatalf("total %d", s.Total())
	}
	// Simulate a crash mid-write: append a partial gzip member.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.Write([]byte{0x1f, 0x8b, 0x08, 0x00})
	f.Close()
	if got := readAll(t, path); len(got) != 3 {
		t.Fatalf("expected earlier rows to survive truncation, got %v", got)
	}
}

const sampleRSS = `<?xml version="1.0"?><rss><channel><title>Feed</title>
<item><title>Bitcoin &amp; ETFs rally</title><link>https://x.test/a</link><pubDate>Mon, 10 Nov 2026 10:00:00 GMT</pubDate><description>&lt;p&gt;Flows up&lt;/p&gt;</description></item>
<item><title>Fed holds rates</title><link>https://x.test/b</link></item>
</channel></rss>`

func TestParseRSSAndDedup(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	rows, err := parseRSS([]byte(sampleRSS), "https://x.test/rss", now)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if rows[0].Title != "Bitcoin & ETFs rally" || strings.Contains(rows[0].Summary, "<p>") {
		t.Fatalf("unexpected cleaning: %+v", rows[0])
	}
	if rows[0].PublishedAt.IsZero() || rows[0].ID == rows[1].ID {
		t.Fatalf("bad time/id: %+v", rows)
	}
	tr := newNewsTracker(t.TempDir())
	if !tr.fresh(rows[0].ID, now) || tr.fresh(rows[0].ID, now) {
		t.Fatal("tracker should accept once then reject duplicates")
	}
}

func TestBackfillCoinbaseCandles_WritesDaysAndResumes(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		start, _ := time.Parse(time.RFC3339, r.URL.Query().Get("start"))
		// newest first, like Coinbase
		fmt.Fprintf(w, "[[%d,1,3,2,2.5,10],[%d,1,3,2,2.5,10]]", start.Add(time.Minute).Unix(), start.Unix())
	}))
	defer srv.Close()
	old := CoinbaseCandlesBase
	CoinbaseCandlesBase = srv.URL
	defer func() { CoinbaseCandlesBase = old }()

	dir := t.TempDir()
	to := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	from := to.Add(-48 * time.Hour)
	if err := BackfillCoinbaseCandles(context.Background(), srv.Client(), dir, "BTC-USD", from, to, nil); err != nil {
		t.Fatal(err)
	}
	first := calls.Load()
	if first != 10 { // 2 days x ceil(1440/300)=5 requests
		t.Fatalf("expected 10 requests, got %d", first)
	}
	lines := readAll(t, filepath.Join(dir, "candles", "BTC-USD", "2026-11-09.jsonl.gz"))
	if len(lines) != 10 {
		t.Fatalf("expected 10 candles, got %d", len(lines))
	}
	var a, b CandleRow
	_ = json.Unmarshal([]byte(lines[0]), &a)
	_ = json.Unmarshal([]byte(lines[1]), &b)
	if a.T >= b.T || a.Open != 2 || a.Low != 1 || a.High != 3 || a.Close != 2.5 {
		t.Fatalf("candles not sorted/parsed: %+v %+v", a, b)
	}
	if err := BackfillCoinbaseCandles(context.Background(), srv.Client(), dir, "BTC-USD", from, to, nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != first {
		t.Fatal("completed days must not be refetched")
	}
}

func TestBackfillGDELT_RetriesThrottleAndResumes(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			fmt.Fprint(w, "Please limit requests to one every 5 seconds")
			return
		}
		fmt.Fprint(w, `{"articles":[{"url":"https://n.test/1","title":"Bitcoin hits high","seendate":"20261109T120000Z","domain":"n.test","language":"English","sourcecountry":"US"}]}`)
	}))
	defer srv.Close()
	oldBase, oldSpace, oldRetry := GDELTBase, GDELTSpacing, GDELTRetryBase
	GDELTBase, GDELTSpacing, GDELTRetryBase = srv.URL, time.Millisecond, time.Millisecond
	defer func() { GDELTBase, GDELTSpacing, GDELTRetryBase = oldBase, oldSpace, oldRetry }()

	dir := t.TempDir()
	to := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	from := to.Add(-6 * time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := BackfillGDELT(ctx, srv.Client(), dir, "crypto", "bitcoin", from, to, 3*time.Hour, nil); err != nil {
		t.Fatal(err)
	}
	n := calls.Load()
	if n != 3 { // 1 throttled + 2 windows
		t.Fatalf("expected 3 calls, got %d", n)
	}
	lines := readAll(t, filepath.Join(dir, "gdelt", "crypto", "2026-11-09.jsonl.gz"))
	if len(lines) != 2 || !strings.Contains(lines[0], `"query":"crypto"`) {
		t.Fatalf("unexpected rows %v", lines)
	}
	if err := BackfillGDELT(ctx, srv.Client(), dir, "crypto", "bitcoin", from, to, 3*time.Hour, nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != n {
		t.Fatal("completed windows must not be refetched")
	}
}
