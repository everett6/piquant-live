package collect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"piquant-live/internal/engine"
	"piquant-live/internal/paper"
)

func TestAppendGzip_FailureLeavesFileIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "d.jsonl.gz")
	if err := appendGzip(path, []byte("{\"a\":1}\n")); err != nil {
		t.Fatal(err)
	}
	if err := appendGzip(filepath.Join(dir, "nope", "x.jsonl.gz"), []byte("x\n")); err == nil {
		t.Fatal("expected error")
	}
	if err := appendGzip(path, []byte("{\"a\":2}\n")); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, path); len(got) != 2 || got[1] != `{"a":2}` {
		t.Fatalf("got %v", got)
	}
}

func TestDailyStore_FlushFailureKeepsRowsBoundedAndRetries(t *testing.T) {
	dir := t.TempDir()
	s, _ := newDailyStore(filepath.Join(dir, "s"))
	day := time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC)
	_ = s.Add(day, map[string]int{"i": 1})
	if err := os.RemoveAll(s.dir); err != nil { // make the flush fail
		t.Fatal(err)
	}
	if err := s.Flush(); err == nil {
		t.Fatal("expected flush error")
	}
	_ = s.Add(day, map[string]int{"i": 2})
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, filepath.Join(s.dir, "2026-11-10.jsonl.gz"))
	if len(got) != 2 || got[0] != `{"i":1}` || got[1] != `{"i":2}` {
		t.Fatalf("rows not retried in order: %v", got)
	}

	// Over-cap pending data is dropped and counted rather than growing without bound.
	big := make([]byte, maxPendingBytes+1)
	for i := range big {
		big[i] = 'x'
	}
	big[len(big)-1] = '\n'
	s.mu.Lock()
	s.buf["2026-11-11"] = big
	s.mu.Unlock()
	_ = os.RemoveAll(s.dir)
	_ = s.Flush()
	s.mu.Lock()
	left := len(s.buf["2026-11-11"])
	s.mu.Unlock()
	if left != 0 || s.Dropped() != 1 {
		t.Fatalf("expected drop of oversized buffer, left=%d dropped=%d", left, s.Dropped())
	}
}

func TestCollectorSnapshotsContinueWhileFeedIsSlow(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	ring := engine.NewSPSCRing(64)
	ing := paper.NewMarketStreamIngestor(paper.StreamConfig{Symbols: []string{"BTCUSDT"}}, ring)
	c, err := New(Config{Dir: t.TempDir(), Symbols: []string{"BTCUSDT"}, Feeds: []string{srv.URL},
		SnapshotEvery: 10 * time.Millisecond, FlushEvery: time.Hour, NewsEvery: time.Hour}, ing, ring, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	ok := false
	for time.Now().Before(deadline) && !ok {
		time.Sleep(20 * time.Millisecond)
		c.mu.Lock()
		ok = c.status.LastPrices != nil // set by every snapshot, even with no data yet
		c.mu.Unlock()
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit after cancel")
	}
	if !ok {
		t.Fatal("snapshots were blocked by the slow news feed")
	}
}

func TestBackfillGDELTProgressWrittenAtomically(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"articles":[{"url":"https://a.test/1","title":"Bitcoin up","seendate":"20261110T100000Z"}]}`))
	}))
	defer srv.Close()
	oldBase, oldSp := GDELTBase, GDELTSpacing
	GDELTBase, GDELTSpacing = srv.URL, time.Millisecond
	defer func() { GDELTBase, GDELTSpacing = oldBase, oldSp }()
	to := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	if err := BackfillGDELT(context.Background(), srv.Client(), dir, "btc", "bitcoin", to.Add(-2*time.Hour), to, time.Hour, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gdelt", "btc", "progress.json.tmp")); err == nil {
		t.Fatal("temp progress file left behind")
	}
	b, err := os.ReadFile(filepath.Join(dir, "gdelt", "btc", "progress.json"))
	if err != nil || !json.Valid(b) {
		t.Fatalf("progress.json invalid: %v %q", err, b)
	}
}
