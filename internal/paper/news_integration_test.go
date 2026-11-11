package paper

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"piquant-live/internal/engine"
	"piquant-live/internal/macro"
)

func TestDryRunEngine_NewsExploitDefense(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "dryrun_news_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := DefaultDryRunConfig(tmpDir)
	cfg.StateFile = filepath.Join(tmpDir, "state.json")
	cfg.JournalFile = filepath.Join(tmpDir, "journal.jsonl")
	cfg.EnableNewsPipeline = true
	cfg.OrderLatency = 10 * time.Millisecond

	ring := engine.NewSPSCRing(1024)
	stream := NewMarketStreamIngestor(DefaultStreamConfig(), ring)

	dryRun, err := NewDryRunEngine(cfg, ring, stream)
	if err != nil {
		t.Fatalf("failed to create DryRunEngine: %v", err)
	}

	newsPipe := dryRun.NewsPipeline()
	if newsPipe == nil {
		t.Fatalf("expected non-nil NewsPipeline")
	}

	now := time.Now().UTC()
	// Ingest a critical exploit on BTC 2 minutes ago
	newsPipe.Ingest(macro.NewsItem{
		ID:               "exploit_btc_bridge_critical",
		Timestamp:        now.Add(-2 * time.Minute),
		Scope:            macro.ScopeMicro,
		Category:         macro.CatExploitHack,
		Impact:           macro.ImpactUrgentCritical,
		Title:            "Critical Multi-Sig Drain on Bitcoin Bridge: $250M Stolen",
		AffectedSymbols:  []string{"BTCUSDT"},
		SentimentScore:   -0.95,
		UrgencyScore:     0.95,
		ConfidenceScore:  0.95,
		IsAdverseExploit: true,
	})

	// Evaluate quotes for BTCUSDT
	dryRun.evaluateQuotesAndGating("BTCUSDT", 65000.0, now)

	// Verify that resting BUY maker orders are blocked (Bid Kill active)
	st := dryRun.State()
	if st.NewsSentiment == nil {
		t.Fatalf("expected st.NewsSentiment to be non-nil")
	}
	if !st.NewsSentiment.SymbolBidKills["BTCUSDT"] {
		t.Errorf("expected SymbolBidKills['BTCUSDT'] == true")
	}

	diag := dryRun.MFTDiagnostics()
	btcDiag, ok := diag["BTCUSDT"]
	if !ok {
		t.Fatalf("expected MFTDiagnostics for BTCUSDT")
	}
	if !btcDiag.NewsBidKill {
		t.Errorf("expected btcDiag.NewsBidKill == true")
	}
	if btcDiag.NewsSentimentScore >= 0 {
		t.Errorf("expected negative NewsSentimentScore, got %.2f", btcDiag.NewsSentimentScore)
	}

	// Verify no resting buy order was placed
	dryRun.mu.RLock()
	orders := dryRun.activeOrders["BTCUSDT"]
	for _, o := range orders {
		if o.Side == "BUY" && o.CancelingUntil.IsZero() {
			t.Errorf("expected no active uncancelled BUY order during exploit, found: %+v", o)
		}
	}
	dryRun.mu.RUnlock()
}
