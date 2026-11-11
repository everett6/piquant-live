package macro

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestNewsPipeline_EmbeddedFallback(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "news_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pipeline := NewNewsPipeline(tmpDir, "")
	recent := pipeline.GetRecentNews(10)
	if len(recent) == 0 {
		t.Fatalf("expected embedded fallback news items, got 0")
	}

	foundMacro := false
	foundMicro := false
	for _, it := range recent {
		if it.Scope == ScopeMacro {
			foundMacro = true
		}
		if it.Scope == ScopeMicro {
			foundMicro = true
		}
	}
	if !foundMacro || !foundMicro {
		t.Fatalf("expected both macro and micro news items in fallback set")
	}
}

func TestNewsPipeline_Analyze_NLP(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "news_nlp_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pipeline := NewNewsPipeline(tmpDir, "")
	now := time.Now().UTC()

	// 1. Critical Exploit on Solana
	exploitItem := pipeline.Analyze(
		"Major Solana Cross-Chain Bridge Exploit: $120M Drained by Hacker",
		"Protocol contracts were drained using a flash loan attack vulnerability.",
		"DeFi Security",
		"https://example.com/exploit",
		now,
	)
	if exploitItem.Scope != ScopeMicro {
		t.Errorf("expected ScopeMicro, got %s", exploitItem.Scope)
	}
	if exploitItem.Category != CatExploitHack {
		t.Errorf("expected CatExploitHack, got %s", exploitItem.Category)
	}
	if exploitItem.Impact != ImpactUrgentCritical {
		t.Errorf("expected ImpactUrgentCritical, got %s", exploitItem.Impact)
	}
	if !exploitItem.IsAdverseExploit {
		t.Errorf("expected IsAdverseExploit == true")
	}
	if exploitItem.SentimentScore >= -0.5 {
		t.Errorf("expected deeply negative sentiment, got %.2f", exploitItem.SentimentScore)
	}
	foundSOL := false
	for _, sym := range exploitItem.AffectedSymbols {
		if sym == "SOLUSDT" {
			foundSOL = true
		}
	}
	if !foundSOL {
		t.Errorf("expected SOLUSDT in affected symbols, got %v", exploitItem.AffectedSymbols)
	}

	// 2. Protocol Upgrade on Ethereum
	upgradeItem := pipeline.Analyze(
		"Ethereum Core Devs Announce Pectra Mainnet Upgrade Launch Date",
		"All node validators report green tests for upcoming EIP enhancements.",
		"EthResearch",
		"https://example.com/pectra",
		now,
	)
	if upgradeItem.Scope != ScopeMicro {
		t.Errorf("expected ScopeMicro, got %s", upgradeItem.Scope)
	}
	if upgradeItem.Category != CatProtocolUpgrade {
		t.Errorf("expected CatProtocolUpgrade, got %s", upgradeItem.Category)
	}
	if upgradeItem.SentimentScore <= 0 {
		t.Errorf("expected positive sentiment, got %.2f", upgradeItem.SentimentScore)
	}

	// 3. Fed Dovish Monetary Policy (Macro)
	fedItem := pipeline.Analyze(
		"Fed Signals Aggressive Rate Cut as Core CPI Inflation Cools",
		"Powell noted economic progress toward target with dovish ease stance.",
		"Bloomberg",
		"https://example.com/fed",
		now,
	)
	if fedItem.Scope != ScopeMacro {
		t.Errorf("expected ScopeMacro, got %s", fedItem.Scope)
	}
	if fedItem.Category != CatMonetaryPolicy {
		t.Errorf("expected CatMonetaryPolicy, got %s", fedItem.Category)
	}
	if fedItem.SentimentScore <= 0.5 {
		t.Errorf("expected strong positive sentiment for dovish cut, got %.2f", fedItem.SentimentScore)
	}
}

func TestNewsPipeline_ExploitBidKill_And_SpreadMultiplier(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "news_eval_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pipeline := NewNewsPipeline(tmpDir, "")
	now := time.Now().UTC()

	// Ingest a critical exploit on SOL 2 minutes ago
	exploitTime := now.Add(-2 * time.Minute)
	pipeline.IngestRawHeadline(
		"Solana Lending Protocol Exploit: $50M Stolen in Reentrancy Attack",
		"Security researchers confirm bridge drained and funds moved.",
		"CertiK Alert",
		"https://example.com/certik",
		exploitTime,
	)

	report := pipeline.Evaluate(now)

	if !report.SymbolBidKills["SOLUSDT"] {
		t.Errorf("expected SymbolBidKills['SOLUSDT'] == true during active exploit")
	}
	if mult, ok := report.SpreadMultipliers["SOLUSDT"]; !ok || mult < 2.50 {
		t.Errorf("expected SpreadMultiplier >= 2.50 for SOLUSDT, got %.2f", mult)
	}
	if report.SymbolBidKills["BTCUSDT"] {
		t.Errorf("expected SymbolBidKills['BTCUSDT'] == false for Solana-specific exploit")
	}

	// Fast-forward 20 minutes (beyond 15m exploit kill window)
	futureTime := now.Add(20 * time.Minute)
	reportFuture := pipeline.Evaluate(futureTime)
	if reportFuture.SymbolBidKills["SOLUSDT"] {
		t.Errorf("expected SymbolBidKills['SOLUSDT'] == false after 20 minutes elapsed")
	}
}

func TestNewsPipeline_TimeDecay(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "news_decay_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pipeline := NewNewsPipeline(tmpDir, "")
	now := time.Now().UTC()

	// Ingest high-impact macro news 1 minute ago
	pipeline.Ingest(NewsItem{
		ID:              "macro_test_rate_cut",
		Timestamp:       now.Add(-1 * time.Minute),
		Scope:           ScopeMacro,
		Category:        CatMonetaryPolicy,
		Impact:          ImpactUrgentHigh,
		Title:           "Emergency Rate Cut Approved",
		SentimentScore:  0.80,
		UrgencyScore:    0.90,
		ConfidenceScore: 0.95,
	})

	repImmediate := pipeline.Evaluate(now)
	macroScoreImm := repImmediate.MacroSentiment
	if macroScoreImm <= 0.40 {
		t.Errorf("expected high immediate macro sentiment, got %.2f", macroScoreImm)
	}

	// 2 hours later, macro score should have decayed substantially
	rep2Hours := pipeline.Evaluate(now.Add(2 * time.Hour))
	if rep2Hours.MacroSentiment >= macroScoreImm {
		t.Errorf("expected decayed macro sentiment after 2 hours (was %.2f, now %.2f)", macroScoreImm, rep2Hours.MacroSentiment)
	}
}

func TestNewsPipeline_CachePersistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "news_cache_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	p1 := NewNewsPipeline(tmpDir, "")
	item := NewsItem{
		ID:              "custom_test_item",
		Timestamp:       time.Now().UTC(),
		Scope:           ScopeMicro,
		Category:        CatProtocolUpgrade,
		Impact:          ImpactUrgentMedium,
		Title:           "Custom Test Item for Disk Persistence",
		AffectedSymbols: []string{"BTCUSDT"},
		SentimentScore:  0.65,
		UrgencyScore:    0.80,
		ConfidenceScore: 0.90,
	}
	p1.Ingest(item)

	// Verify file exists on disk
	cacheFile := filepath.Join(tmpDir, "macro", "news_cache.json")
	if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
		t.Fatalf("cache file %s was not created", cacheFile)
	}

	// New pipeline loading from same dir should have item
	p2 := NewNewsPipeline(tmpDir, "")
	recent := p2.GetRecentNews(10)
	found := false
	for _, it := range recent {
		if it.ID == "custom_test_item" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find custom_test_item after reloading cache")
	}
}

func TestNewsPipeline_ConcurrentRace(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "news_concurrency_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pipeline := NewNewsPipeline(tmpDir, "")
	var wg sync.WaitGroup

	now := time.Now().UTC()

	// Spawn readers
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = pipeline.Evaluate(now)
				_ = pipeline.GetRecentNews(5)
			}
		}()
	}

	// Spawn writers
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				pipeline.IngestRawHeadline(
					"Headline from worker",
					"Summary text here with btc and eth",
					"TestFeed",
					"http://test.com",
					now,
				)
			}
		}(i)
	}

	wg.Wait()
}

func TestNewsPipeline_StablecoinDepeg(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "news_depeg_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pipeline := NewNewsPipeline(tmpDir, "")
	now := time.Now().UTC()

	item := pipeline.Analyze(
		"Market Shock: USDT Breaks Peg to $0.94 Amid Heavy Redemptions",
		"Tether reserves questioned as stablecoin liquidity pool experiences massive imbalance.",
		"Wire",
		"https://example.com/depeg",
		now,
	)

	if item.Scope != ScopeMacro {
		t.Errorf("expected ScopeMacro for stablecoin depeg, got %s", item.Scope)
	}
	if item.Impact != ImpactUrgentCritical {
		t.Errorf("expected ImpactUrgentCritical, got %s", item.Impact)
	}
	if !item.IsAdverseExploit {
		t.Errorf("expected IsAdverseExploit == true")
	}
	if item.SentimentScore > -0.80 {
		t.Errorf("expected deeply negative sentiment, got %.2f", item.SentimentScore)
	}
	if len(item.AffectedSymbols) == 0 {
		t.Errorf("expected all major symbols affected by stablecoin depeg")
	}
}

func TestNewsPipeline_ContagionWidening(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "news_contagion_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pipeline := NewNewsPipeline(tmpDir, "")
	now := time.Now().UTC()

	// Exploit only on SOL
	pipeline.Ingest(NewsItem{
		ID:               "sol_hack_only",
		Timestamp:        now.Add(-2 * time.Minute),
		Scope:            ScopeMicro,
		Category:         CatExploitHack,
		Impact:           ImpactUrgentCritical,
		Title:            "Solana Core Protocol Flash Loan Hack: $75M Drained",
		AffectedSymbols:  []string{"SOLUSDT"},
		SentimentScore:   -0.95,
		UrgencyScore:     0.95,
		ConfidenceScore:  0.95,
		IsAdverseExploit: true,
	})

	rep := pipeline.Evaluate(now)
	if !rep.SymbolBidKills["SOLUSDT"] {
		t.Errorf("expected SymbolBidKills['SOLUSDT'] == true")
	}
	// BTCUSDT is not hacked, but should receive contagion spread widening (>= 1.35x)
	btcMult, ok := rep.SpreadMultipliers["BTCUSDT"]
	if !ok || btcMult < 1.35 {
		t.Errorf("expected contagion spread multiplier >= 1.35 on BTCUSDT during exploit, got %.2f", btcMult)
	}
}
