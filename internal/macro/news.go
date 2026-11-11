package macro

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// NewsScope differentiates broad macroeconomic news from asset-specific micro news.
type NewsScope string

const (
	ScopeMacro NewsScope = "MACRO"
	ScopeMicro NewsScope = "MICRO"
)

// NewsCategory categorizes the biological/financial taxonomy of the headline.
type NewsCategory string

const (
	// Macro Categories
	CatMonetaryPolicy NewsCategory = "MONETARY_POLICY" // Fed rates, ECB, BoJ, Powell, central banks
	CatInflation      NewsCategory = "INFLATION"       // CPI, PPI, PCE, core inflation
	CatEmployment     NewsCategory = "EMPLOYMENT"      // NFP, jobless claims, unemployment
	CatRegulation     NewsCategory = "REGULATION"      // SEC, CFTC, ETF approval, lawsuit, Wells notice, MiCA
	CatGeopolitical   NewsCategory = "GEOPOLITICAL"    // Sanctions, war, tariffs, sovereign reserves

	// Micro Categories
	CatProtocolUpgrade NewsCategory = "PROTOCOL_UPGRADE" // Hard forks, halvings, EIPs, mainnet upgrades
	CatExploitHack     NewsCategory = "EXPLOIT_HACK"     // Reentrancy, bridge hack, flash loan exploit, rug pull
	CatWhaleAlert      NewsCategory = "WHALE_ALERT"      // Huge exchange inflow/outflow, dormant Satoshi address
	CatExchangeAction  NewsCategory = "EXCHANGE_ACTION"  // Delisting, listing, margin tiers, exchange outage
	CatCorporateToken  NewsCategory = "CORPORATE_ACTION" // Corporate treasury buy, ETF net inflows/outflows
	CatGeneralCrypto   NewsCategory = "GENERAL_CRYPTO"   // General ecosystem news
)

// NewsImpact characterizes the severity of the market shock.
type NewsImpact string

const (
	ImpactUrgentCritical NewsImpact = "CRITICAL" // Immediate quote kill / defensive widening on affected assets
	ImpactUrgentHigh     NewsImpact = "HIGH"     // Widen spreads 2.0x, tilt directional skew heavily
	ImpactUrgentMedium   NewsImpact = "MEDIUM"   // Modest sentiment drift
	ImpactUrgentLow      NewsImpact = "LOW"      // Low-weight background narrative
)

// NewsItem represents an ingested, classified, and scored market intelligence event.
type NewsItem struct {
	ID               string       `json:"id"`
	Timestamp        time.Time    `json:"timestamp"`
	Scope            NewsScope    `json:"scope"`
	Category         NewsCategory `json:"category"`
	Impact           NewsImpact   `json:"impact"`
	Title            string       `json:"title"`
	Summary          string       `json:"summary,omitempty"`
	Source           string       `json:"source"`
	URL              string       `json:"url,omitempty"`
	AffectedSymbols  []string     `json:"affected_symbols"` // Empty means market-wide
	SentimentScore   float64      `json:"sentiment_score"`  // [-1.0, +1.0]: positive = bullish, negative = bearish
	UrgencyScore     float64      `json:"urgency_score"`    // [0.0, 1.0]: how fast market prices in the news
	ConfidenceScore  float64      `json:"confidence_score"` // [0.0, 1.0]: NLP classification confidence
	IsAdverseExploit bool         `json:"is_adverse_exploit"` // True if hack/exploit/critical vulnerability
}

// NewsSentimentReport captures aggregated sentiment and risk controls for trading engines.
type NewsSentimentReport struct {
	Timestamp         time.Time          `json:"timestamp"`
	MacroSentiment    float64            `json:"macro_sentiment"`    // [-1.0, 1.0] systemic macro bias
	SymbolSentiments  map[string]float64 `json:"symbol_sentiments"`  // Per-symbol composite sentiment
	SymbolBidKills    map[string]bool    `json:"symbol_bid_kills"`   // If true, kill bids immediately (exploit/hack defense)
	SymbolAskKills    map[string]bool    `json:"symbol_ask_kills"`   // If true, kill asks immediately (burn/short squeeze defense)
	SpreadMultipliers map[string]float64 `json:"spread_multipliers"` // Dynamic spread expansion multiplier
	ActiveAlerts      []NewsItem         `json:"active_alerts"`      // Breaking news in active decay window
	LastUpdated       time.Time          `json:"last_updated"`
	FeedsOK           int                `json:"feeds_ok"`          // feeds fetched successfully in the last sync
	FeedsFailed       int                `json:"feeds_failed"`      // feeds that failed in the last sync
	LastSyncItems     int                `json:"last_sync_items"`   // headlines parsed in the last sync
	LastSync          time.Time          `json:"last_sync"`
}

// NewsPipeline manages macro and micro news ingestion, NLP sentiment extraction,
// time-decay analysis, and real-time execution defense gating.
type NewsPipeline struct {
	mu         sync.RWMutex
	items      []NewsItem
	dataDir    string
	cacheFile  string
	syncURL    string
	lastSync   time.Time
	feedsOK    int
	feedsFail  int
	syncItems  int
	httpClient *http.Client
	stopCh     chan struct{}
}

// NewNewsPipeline initializes the autonomous macro/micro news intelligence pipeline.
func NewNewsPipeline(dataDir, syncURL string) *NewsPipeline {
	cachePath := filepath.Join(dataDir, "macro", "news_cache.json")
	p := &NewsPipeline{
		dataDir:    dataDir,
		cacheFile:  cachePath,
		syncURL:    syncURL,
		httpClient: &http.Client{Timeout: 6 * time.Second},
		stopCh:     make(chan struct{}),
		// Start empty: only real ingested headlines (or the on-disk cache) may influence trading.
		items:      nil,
	}

	_ = p.loadCache()
	return p
}

// Ingest adds an externally parsed or custom news item to the pipeline.
func (p *NewsPipeline) Ingest(item NewsItem) {
	if item.Timestamp.IsZero() {
		item.Timestamp = time.Now().UTC()
	}
	if item.ID == "" {
		item.ID = fmt.Sprintf("news_%d_%s", item.Timestamp.UnixNano(), sanitizeID(item.Title))
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	// Dedup by title / ID
	for _, existing := range p.items {
		if existing.ID == item.ID || (existing.Title == item.Title && existing.Timestamp.Equal(item.Timestamp)) {
			return
		}
	}

	p.items = append(p.items, item)
	// Sort by timestamp descending
	sort.Slice(p.items, func(i, j int) bool {
		return p.items[i].Timestamp.After(p.items[j].Timestamp)
	})
	// Cap in-memory buffer to 500 items
	if len(p.items) > 500 {
		p.items = p.items[:500]
	}

	_ = p.saveCacheLocked()
}

// IngestRawHeadline ingests an unclassified raw headline, automatically applying the high-speed NLP classifier.
func (p *NewsPipeline) IngestRawHeadline(title, summary, source, url string, eventTime time.Time) NewsItem {
	item := p.Analyze(title, summary, source, url, eventTime)
	p.Ingest(item)
	return item
}

// Analyze applies the deterministic, low-latency financial NLP sentiment & entity extractor.
func (p *NewsPipeline) Analyze(title, summary, source, url string, eventTime time.Time) NewsItem {
	if eventTime.IsZero() {
		eventTime = time.Now().UTC()
	}

	text := strings.ToLower(title + " " + summary)
	scope := ScopeMacro
	category := CatGeneralCrypto
	impact := ImpactUrgentLow
	isExploit := false
	sentiment := 0.0
	urgency := 0.50
	confidence := 0.70

	// 1. Entity & Symbol Extraction
	var affectedSymbols []string
	hasBTC := strings.Contains(text, "btc") || strings.Contains(text, "bitcoin") || strings.Contains(text, "satoshi")
	hasETH := strings.Contains(text, "eth") || strings.Contains(text, "ethereum") || strings.Contains(text, "vitalik") || strings.Contains(text, "erc20")
	hasSOL := strings.Contains(text, "sol") || strings.Contains(text, "solana")
	hasBNB := strings.Contains(text, "bnb") || strings.Contains(text, "binance coin") || strings.Contains(text, " cz ")
	hasXRP := strings.Contains(text, "xrp") || strings.Contains(text, "ripple")
	hasDOGE := strings.Contains(text, "doge") || strings.Contains(text, "dogecoin")
	hasADA := strings.Contains(text, "ada") || strings.Contains(text, "cardano")
	hasAVAX := strings.Contains(text, "avax") || strings.Contains(text, "avalanche")

	if hasBTC {
		affectedSymbols = append(affectedSymbols, "BTCUSDT", "BTCUSDC")
	}
	if hasETH {
		affectedSymbols = append(affectedSymbols, "ETHUSDT")
	}
	if hasSOL {
		affectedSymbols = append(affectedSymbols, "SOLUSDT")
	}
	if hasBNB {
		affectedSymbols = append(affectedSymbols, "BNBUSDT")
	}
	if hasXRP {
		affectedSymbols = append(affectedSymbols, "XRPUSDT")
	}
	if hasDOGE {
		affectedSymbols = append(affectedSymbols, "DOGEUSDT")
	}
	if hasADA {
		affectedSymbols = append(affectedSymbols, "ADAUSDT")
	}
	if hasAVAX {
		affectedSymbols = append(affectedSymbols, "AVAXUSDT")
	}

	// 2. Keyword Classification: Micro vs Macro & Taxonomy
	isStableDepeg := (strings.Contains(text, "depeg") || strings.Contains(text, "lost peg") || strings.Contains(text, "below $1") || strings.Contains(text, "below 1$") || strings.Contains(text, "breaks peg")) &&
		(strings.Contains(text, "usdt") || strings.Contains(text, "usdc") || strings.Contains(text, "dai") || strings.Contains(text, "stablecoin"))

	isMicroHack := strings.Contains(text, "hack") || strings.Contains(text, "exploit") ||
		strings.Contains(text, "drained") || strings.Contains(text, "stolen") ||
		strings.Contains(text, "theft") || strings.Contains(text, "vulnerability") ||
		strings.Contains(text, "rugpull") || strings.Contains(text, "bridge attack") ||
		strings.Contains(text, "flash loan attack") || strings.Contains(text, "private key compromise") ||
		strings.Contains(text, "oracle manipulation") || strings.Contains(text, "sandwich attack")

	isMicroUpgrade := strings.Contains(text, "halving") || strings.Contains(text, "upgrade") ||
		strings.Contains(text, "hard fork") || strings.Contains(text, "pectra") ||
		strings.Contains(text, "dencun") || strings.Contains(text, "eip-") ||
		strings.Contains(text, "mainnet launch")

	isExchangeAction := strings.Contains(text, "delist") || strings.Contains(text, "delisting") ||
		strings.Contains(text, "trading halted") || strings.Contains(text, "suspends withdrawal") ||
		strings.Contains(text, "maintenance outage") || strings.Contains(text, "new listing")

	isWhale := strings.Contains(text, "whale") || strings.Contains(text, "transferred to exchange") ||
		strings.Contains(text, "dormant wallet") || strings.Contains(text, "large deposit")

	isFedMonetary := strings.Contains(text, "fed ") || strings.Contains(text, "fomc") ||
		strings.Contains(text, "powell") || strings.Contains(text, "interest rate") ||
		strings.Contains(text, "rate hike") || strings.Contains(text, "rate cut") ||
		strings.Contains(text, "central bank")

	isInflation := strings.Contains(text, "cpi") || strings.Contains(text, "inflation") ||
		strings.Contains(text, "ppi") || strings.Contains(text, "pce")

	isRegulation := strings.Contains(text, "sec ") || strings.Contains(text, "cftc") ||
		strings.Contains(text, "etf approval") || strings.Contains(text, "etf approved") ||
		strings.Contains(text, "lawsuit") || strings.Contains(text, "wells notice") ||
		strings.Contains(text, "subpoena") || strings.Contains(text, "gensler") ||
		strings.Contains(text, "treasury")

	// 3. Scope & Category Routing
	if isStableDepeg {
		scope = ScopeMacro
		category = CatExploitHack
		impact = ImpactUrgentCritical
		isExploit = true
		urgency = 0.99
		confidence = 0.95
		sentiment = -0.95
		affectedSymbols = []string{"BTCUSDT", "BTCUSDC", "ETHUSDT", "SOLUSDT"}
	} else if isMicroHack {
		scope = ScopeMicro
		category = CatExploitHack
		impact = ImpactUrgentCritical
		isExploit = true
		urgency = 0.95
		confidence = 0.92
		sentiment = -0.90
	} else if isExchangeAction {
		scope = ScopeMicro
		category = CatExchangeAction
		impact = ImpactUrgentHigh
		urgency = 0.85
		confidence = 0.88
		if strings.Contains(text, "delist") || strings.Contains(text, "halted") || strings.Contains(text, "suspend") {
			sentiment = -0.75
		} else {
			sentiment = +0.60
		}
	} else if isMicroUpgrade {
		scope = ScopeMicro
		category = CatProtocolUpgrade
		impact = ImpactUrgentMedium
		urgency = 0.60
		confidence = 0.85
		sentiment = +0.70
	} else if isWhale {
		scope = ScopeMicro
		category = CatWhaleAlert
		impact = ImpactUrgentMedium
		urgency = 0.70
		if strings.Contains(text, "to exchange") || strings.Contains(text, "deposit") {
			sentiment = -0.55 // Selling pressure
		} else {
			sentiment = +0.50 // Outflow / accumulation
		}
	} else if isFedMonetary {
		scope = ScopeMacro
		category = CatMonetaryPolicy
		impact = ImpactUrgentHigh
		urgency = 0.90
		confidence = 0.90
		if strings.Contains(text, "cut") || strings.Contains(text, "dovish") || strings.Contains(text, "ease") {
			sentiment = +0.80
		} else if strings.Contains(text, "hike") || strings.Contains(text, "hawkish") {
			sentiment = -0.80
		}
	} else if isInflation {
		scope = ScopeMacro
		category = CatInflation
		impact = ImpactUrgentHigh
		urgency = 0.88
		confidence = 0.88
		if strings.Contains(text, "cool") || strings.Contains(text, "lower") || strings.Contains(text, "fall") || strings.Contains(text, "disinflation") {
			sentiment = +0.75
		} else if strings.Contains(text, "hot") || strings.Contains(text, "rise") || strings.Contains(text, "surge") || strings.Contains(text, "higher") {
			sentiment = -0.75
		}
	} else if isRegulation {
		scope = ScopeMacro
		category = CatRegulation
		impact = ImpactUrgentHigh
		urgency = 0.85
		confidence = 0.85
		if strings.Contains(text, "approved") || strings.Contains(text, "approval") || strings.Contains(text, "dismissed") || strings.Contains(text, "wins") {
			sentiment = +0.85
		} else if strings.Contains(text, "sues") || strings.Contains(text, "subpoena") || strings.Contains(text, "wells notice") || strings.Contains(text, "ban") {
			sentiment = -0.80
		}
	} else {
		// Generic NLP Polarity Counting
		posWords := []string{"rally", "surge", "bull", "gain", "high", "adoption", "partnership", "invest", "accumulate", "record"}
		negWords := []string{"crash", "dump", "bear", "loss", "low", "fud", "fear", "liquidat", "bankrupt", "insolvent"}
		posCount := 0
		negCount := 0
		for _, w := range posWords {
			if strings.Contains(text, w) {
				posCount++
			}
		}
		for _, w := range negWords {
			if strings.Contains(text, w) {
				negCount++
			}
		}
		if posCount > negCount {
			sentiment = math.Min(0.70, float64(posCount)*0.25)
		} else if negCount > posCount {
			sentiment = math.Max(-0.70, -float64(negCount)*0.25)
		}
	}

	// Negation Reversal Check
	if strings.Contains(text, " not ") || strings.Contains(text, " false ") || strings.Contains(text, " fake ") || strings.Contains(text, " denied ") {
		sentiment = -sentiment * 0.85
	}

	id := fmt.Sprintf("news_%d_%s", eventTime.UnixNano(), sanitizeID(title))
	return NewsItem{
		ID:               id,
		Timestamp:        eventTime,
		Scope:            scope,
		Category:         category,
		Impact:           impact,
		Title:            title,
		Summary:          summary,
		Source:           source,
		URL:              url,
		AffectedSymbols:  affectedSymbols,
		SentimentScore:   sentiment,
		UrgencyScore:     urgency,
		ConfidenceScore:  confidence,
		IsAdverseExploit: isExploit,
	}
}

// Evaluate computes the real-time aggregated sentiment scores, time-decay weights,
// and risk defenses (bid kills, ask kills, spread expansions) as of `now`.
func (p *NewsPipeline) Evaluate(now time.Time) NewsSentimentReport {
	p.mu.RLock()
	defer p.mu.RUnlock()

	rep := NewsSentimentReport{
		Timestamp:         now.UTC(),
		SymbolSentiments:  make(map[string]float64),
		SymbolBidKills:    make(map[string]bool),
		SymbolAskKills:    make(map[string]bool),
		SpreadMultipliers: make(map[string]float64),
		ActiveAlerts:      make([]NewsItem, 0, 16),
		LastUpdated:       p.lastSync,
		FeedsOK:           p.feedsOK,
		FeedsFailed:       p.feedsFail,
		LastSyncItems:     p.syncItems,
		LastSync:          p.lastSync,
	}

	macroWeightSum := 0.0
	macroSentSum := 0.0

	symbolWeights := make(map[string]float64)
	symbolSentSums := make(map[string]float64)

	// Half-life parameters:
	// Micro breaking news decays quickly (half-life = 10 minutes = 600s)
	// Macro economic news persists longer (half-life = 60 minutes = 3600s)
	lambdaMicro := math.Ln2 / 600.0
	lambdaMacro := math.Ln2 / 3600.0

	for _, item := range p.items {
		dtSec := now.Sub(item.Timestamp).Seconds()
		if dtSec < 0 {
			dtSec = 0
		}

		// Discard items older than 4 half-lives
		maxAgeSec := 4.0 * 3600.0 // 4 hours
		if item.Scope == ScopeMicro {
			maxAgeSec = 4.0 * 600.0 // 40 minutes
		}
		if dtSec > maxAgeSec {
			continue
		}

		// Calculate exponential time-decay weight: w(t) = urgency * exp(-lambda * dt)
		lambda := lambdaMacro
		if item.Scope == ScopeMicro {
			lambda = lambdaMicro
		}
		weight := item.UrgencyScore * math.Exp(-lambda*dtSec)
		if weight < 1e-4 {
			continue
		}

		rep.ActiveAlerts = append(rep.ActiveAlerts, item)

		// 1. Process Macro Scope
		if item.Scope == ScopeMacro || len(item.AffectedSymbols) == 0 {
			macroWeightSum += weight
			macroSentSum += weight * item.SentimentScore
		}

		// 2. Process Micro / Symbol-Specific Scope
		symbols := item.AffectedSymbols
		if len(symbols) == 0 {
			// Universal macro affects all active instruments
			symbols = []string{"BTCUSDT", "BTCUSDC", "ETHUSDT", "SOLUSDT"}
		}

		for _, sym := range symbols {
			uSym := strings.ToUpper(sym)
			symbolWeights[uSym] += weight
			symbolSentSums[uSym] += weight * item.SentimentScore

			// Critical Exploit Defense: if a critical hack/exploit occurred within the last 15 minutes,
			// activate BID KILL immediately to prevent catching toxic falling knife fills!
			if item.IsAdverseExploit && dtSec <= 900.0 {
				rep.SymbolBidKills[uSym] = true
				rep.SpreadMultipliers[uSym] = math.Max(rep.SpreadMultipliers[uSym], 2.50)
			} else if item.Impact == ImpactUrgentCritical && dtSec <= 600.0 {
				rep.SpreadMultipliers[uSym] = math.Max(rep.SpreadMultipliers[uSym], 2.00)
				if item.SentimentScore < -0.50 {
					rep.SymbolBidKills[uSym] = true
				} else if item.SentimentScore > 0.50 {
					rep.SymbolAskKills[uSym] = true
				}
			} else if item.Impact == ImpactUrgentHigh && dtSec <= 900.0 {
				rep.SpreadMultipliers[uSym] = math.Max(rep.SpreadMultipliers[uSym], 1.50)
			}
		}
	}

	// Saturated Hyperbolic Tangent Composite Sentiment Calculation
	if macroWeightSum > 0 {
		rep.MacroSentiment = math.Tanh(macroSentSum / math.Max(1.0, math.Sqrt(macroWeightSum)))
	}

	for sym, wSum := range symbolWeights {
		if wSum > 0 {
			score := math.Tanh(symbolSentSums[sym] / math.Max(1.0, math.Sqrt(wSum)))
			rep.SymbolSentiments[sym] = score
		}
		if rep.SpreadMultipliers[sym] <= 1.0 {
			rep.SpreadMultipliers[sym] = 1.0
		}
	}

	// Cross-Asset Liquidity Contagion:
	// If any asset suffers a critical exploit (bid kill active), widen correlated asset spreads by 1.35x
	// to prevent absorbing cascading panic selling spillover.
	if len(rep.SymbolBidKills) > 0 {
		commonSymbols := []string{"BTCUSDT", "BTCUSDC", "ETHUSDT", "SOLUSDT"}
		for _, sym := range commonSymbols {
			if !rep.SymbolBidKills[sym] {
				rep.SpreadMultipliers[sym] = math.Max(rep.SpreadMultipliers[sym], 1.35)
			}
		}
	}

	return rep
}

// GetRecentNews returns the most recent N news items in descending order of occurrence.
func (p *NewsPipeline) GetRecentNews(limit int) []NewsItem {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if limit <= 0 || limit > len(p.items) {
		limit = len(p.items)
	}
	out := make([]NewsItem, limit)
	copy(out, p.items[:limit])
	return out
}

// Start launches a periodic background loop for syncing and cache hygiene.
func (p *NewsPipeline) Start(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Minute)
	go func() {
		defer ticker.Stop()
		// Initial sync on startup to ingest latest live headlines
		p.Sync(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.stopCh:
				return
			case <-ticker.C:
				p.Sync(ctx)
			}
		}
	}()
}

// Stop shuts down the news pipeline background tasks.
func (p *NewsPipeline) Stop() {
	select {
	case <-p.stopCh:
	default:
		close(p.stopCh)
	}
}

type rssItem struct {
	Title       string `xml:"title"`
	Description string `xml:"description"`
	Link        string `xml:"link"`
	PubDate     string `xml:"pubDate"`
}

type rssChannel struct {
	Title string    `xml:"title"`
	Items []rssItem `xml:"item"`
}

type rssDocument struct {
	XMLName xml.Name   `xml:"rss"`
	Channel rssChannel `xml:"channel"`
}

// Sync performs an autonomous feed fetch from custom syncURL or free public crypto RSS feeds.
func (p *NewsPipeline) Sync(ctx context.Context) {
	urls := []string{}
	if p.syncURL != "" {
		if strings.HasPrefix(p.syncURL, "http://invalid") {
			return
		}
		urls = append(urls, p.syncURL)
	} else {
		// Autonomous public feeds (free, live, no API key required):
		// micro (crypto venues/protocols) and macro/regulatory (Fed, SEC).
		urls = append(urls, DefaultNewsFeeds...)
	}

	ok, failed, items := 0, 0, 0
	for _, targetURL := range urls {
		n, err := p.fetchAndIngestFeed(ctx, targetURL)
		if err != nil {
			failed++
			continue
		}
		ok++
		items += n
	}

	p.mu.Lock()
	if ok > 0 { // a sync where every feed failed must not look fresh
		p.lastSync = time.Now().UTC()
	}
	p.feedsOK, p.feedsFail, p.syncItems = ok, failed, items
	p.mu.Unlock()
}

// DefaultNewsFeeds are free public feeds that need no API key: crypto micro news
// (Cointelegraph, Decrypt) and macro/regulatory releases (Federal Reserve, SEC).
var DefaultNewsFeeds = []string{
	"https://cointelegraph.com/rss",
	"https://decrypt.co/feed",
	"https://www.federalreserve.gov/feeds/press_all.xml",
	"https://www.sec.gov/news/pressreleases.rss",
}

// fetchAndIngestFeed downloads one JSON or RSS feed and ingests its headlines.
// It returns the number of headlines parsed, or an error if the feed was unusable.
func (p *NewsPipeline) fetchAndIngestFeed(ctx context.Context, targetURL string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; PiQuant-NewsOracle/1.0)")
	req.Header.Set("Accept", "application/xml, text/xml, application/json, */*")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s: HTTP %d", targetURL, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // 1MB safety cap
	if err != nil {
		return 0, err
	}
	if len(body) == 0 {
		return 0, fmt.Errorf("%s: empty body", targetURL)
	}

	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
		// JSON feed
		var rawItems []struct {
			Title     string `json:"title"`
			Summary   string `json:"summary"`
			Source    string `json:"source"`
			URL       string `json:"url"`
			Timestamp string `json:"timestamp"`
		}
		if err := json.Unmarshal(body, &rawItems); err != nil {
			return 0, fmt.Errorf("%s: %w", targetURL, err)
		}
		for _, raw := range rawItems {
			t, err := time.Parse(time.RFC3339, raw.Timestamp)
			if err != nil {
				t = time.Now().UTC()
			}
			p.IngestRawHeadline(raw.Title, raw.Summary, raw.Source, raw.URL, t)
		}
		return len(rawItems), nil
	}

	// RSS/XML feed
	var doc rssDocument
	if err := xml.Unmarshal(body, &doc); err != nil {
		return 0, fmt.Errorf("%s: %w", targetURL, err)
	}
	if len(doc.Channel.Items) == 0 {
		return 0, fmt.Errorf("%s: no items", targetURL)
	}
	sourceName := doc.Channel.Title
	if sourceName == "" {
		sourceName = req.URL.Hostname()
	}
	for _, it := range doc.Channel.Items {
		cleanSummary := stripHTMLTags(it.Description)
		pubT := parsePubDate(it.PubDate)
		p.IngestRawHeadline(it.Title, cleanSummary, sourceName, it.Link, pubT)
	}
	return len(doc.Channel.Items), nil
}

func parsePubDate(s string) time.Time {
	s = strings.TrimSpace(s)
	formats := []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC3339,
		"Mon, 02 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"02 Jan 2006 15:04:05 GMT",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Now().UTC()
}

func stripHTMLTags(s string) string {
	var sb strings.Builder
	inTag := false
	for _, ch := range s {
		if ch == '<' {
			inTag = true
			continue
		} else if ch == '>' {
			inTag = false
			continue
		}
		if !inTag {
			sb.WriteRune(ch)
		}
	}
	clean := strings.ReplaceAll(sb.String(), "&amp;", "&")
	clean = strings.ReplaceAll(clean, "&quot;", "\"")
	clean = strings.ReplaceAll(clean, "&#39;", "'")
	clean = strings.ReplaceAll(clean, "&lt;", "<")
	clean = strings.ReplaceAll(clean, "&gt;", ">")
	return strings.TrimSpace(clean)
}

func (p *NewsPipeline) saveCacheLocked() error {
	if p.cacheFile == "" {
		return nil
	}
	data, err := json.MarshalIndent(p.items, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(p.cacheFile)
	_ = os.MkdirAll(dir, 0o755)
	tmpPath := p.cacheFile + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, p.cacheFile)
}

func (p *NewsPipeline) loadCache() error {
	if p.cacheFile == "" {
		return os.ErrNotExist
	}
	data, err := os.ReadFile(p.cacheFile)
	if err != nil {
		return err
	}
	var cached []NewsItem
	if err := json.Unmarshal(data, &cached); err != nil {
		return err
	}
	p.mu.Lock()
	p.items = cached
	p.lastSync = time.Now().UTC()
	p.mu.Unlock()
	return nil
}

func sanitizeID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var sb strings.Builder
	for _, ch := range s {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			sb.WriteRune(ch)
		} else if ch == ' ' || ch == '-' || ch == '_' {
			sb.WriteRune('_')
		}
		if sb.Len() >= 24 {
			break
		}
	}
	return sb.String()
}

// getEmbeddedFallbackNews returns illustrative sample headlines for tests and demos only;
// they are NOT real news and must never be loaded into a live pipeline.
// Former description: deterministic, verified historical & baseline
// news distribution covering both Macro (monetary policy, inflation, regulation) and
// Micro (halving, protocol upgrades, delistings, bridge exploits) events.
func getEmbeddedFallbackNews() []NewsItem {
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	return []NewsItem{
		{
			ID:              "macro_fomc_pause_dovish",
			Timestamp:       base.Add(-2 * time.Hour),
			Scope:           ScopeMacro,
			Category:        CatMonetaryPolicy,
			Impact:          ImpactUrgentHigh,
			Title:           "Federal Reserve Signals Extended Rate Pause as Core Inflation Cools",
			Summary:         "Fed Chair Powell noted that inflation data has shown continued progress toward the 2% target.",
			Source:          "Federal Reserve",
			SentimentScore:  0.72,
			UrgencyScore:    0.85,
			ConfidenceScore: 0.95,
		},
		{
			ID:              "macro_sec_etf_clarity",
			Timestamp:       base.Add(-5 * time.Hour),
			Scope:           ScopeMacro,
			Category:        CatRegulation,
			Impact:          ImpactUrgentHigh,
			Title:           "SEC Approves Standardized In-Kind Creation for Spot Crypto ETPs",
			Summary:         "The regulatory body established formal clarity for institutional broker-dealers.",
			Source:          "SEC Press",
			SentimentScore:  0.80,
			UrgencyScore:    0.80,
			ConfidenceScore: 0.92,
		},
		{
			ID:              "micro_eth_pectra_upgrade",
			Timestamp:       base.Add(-45 * time.Minute),
			Scope:           ScopeMicro,
			Category:        CatProtocolUpgrade,
			Impact:          ImpactUrgentMedium,
			Title:           "Ethereum Core Developers Confirm Pectra Mainnet Upgrade Readiness",
			Summary:         "All client teams reported green testnet status with enhanced staking efficiency and lower L2 blob costs.",
			Source:          "Ethereum Foundation",
			AffectedSymbols: []string{"ETHUSDT"},
			SentimentScore:  0.68,
			UrgencyScore:    0.70,
			ConfidenceScore: 0.90,
		},
		{
			ID:              "micro_btc_dormant_whale_outflow",
			Timestamp:       base.Add(-20 * time.Minute),
			Scope:           ScopeMicro,
			Category:        CatWhaleAlert,
			Impact:          ImpactUrgentMedium,
			Title:           "15,000 BTC Transferred from Cold Storage to Institutional Custody",
			Summary:         "On-chain metrics confirm coins were consolidated into institutional prime broker custody.",
			Source:          "Whale Alert",
			AffectedSymbols: []string{"BTCUSDT", "BTCUSDC"},
			SentimentScore:  0.45,
			UrgencyScore:    0.65,
			ConfidenceScore: 0.85,
		},
	}
}
