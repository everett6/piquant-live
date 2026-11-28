package macro

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestNewsEvaluateCacheInvalidatesOnIngest(t *testing.T) {
	p := NewNewsPipeline(t.TempDir(), "")
	now := time.Now().UTC()
	before := p.Evaluate(now)
	if len(before.ActiveAlerts) != 0 {
		t.Fatalf("fresh pipeline has alerts: %d", len(before.ActiveAlerts))
	}
	p.IngestRawHeadline("Major crypto exchange hacked, $400 million in bitcoin stolen", "", "test", "", now)
	after := p.Evaluate(now.Add(10 * time.Millisecond)) // inside the cache TTL
	if len(after.ActiveAlerts) == 0 {
		t.Fatal("cached report survived an ingest: new headline not visible")
	}
	again := p.Evaluate(now.Add(20 * time.Millisecond))
	if len(again.ActiveAlerts) != len(after.ActiveAlerts) {
		t.Fatal("cache returned a different report without a change")
	}
}

func TestNewsLoadCacheModTimeAndUnparseableDatesSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "macro", "news_cache.json")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2026, 11, 2, 8, 30, 0, 0, time.UTC)
	if err := os.Chtimes(cachePath, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	p := NewNewsPipeline(tmpDir, "")
	if !p.lastSync.Equal(mtime) {
		t.Fatalf("expected lastSync=%s from cache mtime, got %s", mtime, p.lastSync)
	}

	if !parsePubDate("").IsZero() || !parsePubDate("not-a-date").IsZero() {
		t.Fatal("expected parsePubDate to return zero time on empty/invalid input")
	}

	rss := `<?xml version="1.0"?><rss><channel><title>Test</title>
<item><title>Bad date</title><pubDate>not-a-date</pubDate></item>
<item><title>Empty date</title><pubDate></pubDate></item>
<item><title>Valid headline</title><pubDate>Tue, 10 Nov 2026 19:00:00 +0000</pubDate></item>
</channel></rss>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(rss))
	}))
	defer srv.Close()
	n, err := p.fetchAndIngestFeed(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchAndIngestFeed error: %v", err)
	}
	if n != 1 || len(p.GetRecentNews(10)) != 1 {
		t.Fatalf("expected 1 valid item ingested, got n=%d len=%d", n, len(p.GetRecentNews(10)))
	}
}

func TestNewsConcurrentIngestAtomicCache(t *testing.T) {
	p := NewNewsPipeline(t.TempDir(), "")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			p.IngestRawHeadline(fmt.Sprintf("Bitcoin headline %d", idx), "", "test", "", time.Now().UTC())
		}(i)
	}
	wg.Wait()
	if got := len(p.GetRecentNews(20)); got != 8 {
		t.Fatalf("expected 8 ingested items, got %d", got)
	}
}
