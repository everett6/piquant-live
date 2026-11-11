package macro

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testRSS = `<?xml version="1.0"?><rss><channel><title>Fed Press</title>
<item><title>Federal Reserve issues FOMC statement: rate cut of 25 basis points</title><description>policy</description><link>https://x/1</link><pubDate>Tue, 10 Nov 2026 19:00:00 +0000</pubDate></item>
<item><title>SEC charges crypto exchange in lawsuit</title><description>enforcement</description><link>https://x/2</link><pubDate>Tue, 10 Nov 2026 18:00:00 +0000</pubDate></item>
</channel></rss>`

func TestSyncReportsFeedHealthAndIngestsMacro(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(testRSS)) }))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }))
	defer bad.Close()

	p := NewNewsPipeline(t.TempDir(), "")
	saved := DefaultNewsFeeds
	DefaultNewsFeeds = []string{good.URL, bad.URL}
	defer func() { DefaultNewsFeeds = saved }()

	p.Sync(context.Background())
	p.Sync(context.Background()) // re-sync must not duplicate headlines

	rep := p.Evaluate(p.lastSync)
	if rep.FeedsOK != 1 || rep.FeedsFailed != 1 || rep.LastSyncItems != 2 {
		t.Fatalf("feed health = ok %d failed %d items %d", rep.FeedsOK, rep.FeedsFailed, rep.LastSyncItems)
	}
	items := p.GetRecentNews(10)
	if len(items) != 2 {
		t.Fatalf("want 2 deduplicated headlines, got %d", len(items))
	}
	cats := map[NewsCategory]bool{}
	for _, it := range items {
		cats[it.Category] = true
	}
	if !cats[CatMonetaryPolicy] || !cats[CatRegulation] {
		t.Fatalf("macro/regulatory headlines not categorized: %v", cats)
	}
}
