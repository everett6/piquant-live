package macro

import (
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
