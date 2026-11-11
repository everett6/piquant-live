package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"piquant-live/internal/engine"
	"piquant-live/internal/paper"
)

// Config controls the live collector.
type Config struct {
	Dir           string
	Symbols       []string
	Feeds         []string
	SnapshotEvery time.Duration // default 1s
	NewsEvery     time.Duration // default 2m
	FlushEvery    time.Duration // default 60s
	MinFreeGB     float64       // stop writing below this much free disk (default 20)
}

// PriceRow is one per-second snapshot for one symbol.
type PriceRow struct {
	T         int64   `json:"t"` // unix ms
	Sym       string  `json:"sym"`
	Bid       float64 `json:"bid,omitempty"`
	Ask       float64 `json:"ask,omitempty"`
	BidSz     float64 `json:"bid_sz,omitempty"`
	AskSz     float64 `json:"ask_sz,omitempty"`
	Bid5Sz    float64 `json:"bid5_sz,omitempty"` // size in top 5 bid levels
	Ask5Sz    float64 `json:"ask5_sz,omitempty"`
	Mid       float64 `json:"mid,omitempty"`
	Trades    int     `json:"n,omitempty"` // Binance.US trades this second
	Vol       float64 `json:"vol,omitempty"`
	BuyVol    float64 `json:"buy_vol,omitempty"`
	SellVol   float64 `json:"sell_vol,omitempty"`
	CBPrice   float64 `json:"cb,omitempty"` // Coinbase last trade
	CBBid     float64 `json:"cb_bid,omitempty"`
	CBAsk     float64 `json:"cb_ask,omitempty"`
	CBTrades  uint64  `json:"cb_n,omitempty"` // Coinbase trades this second
	CBVolUSD  float64 `json:"cb_vol,omitempty"`
	BookStale bool    `json:"stale,omitempty"`
}

// Status is written to <dir>/status.json every flush.
type Status struct {
	StartedAt     time.Time          `json:"started_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
	PriceRows     uint64             `json:"price_rows"`
	NewsRows      uint64             `json:"news_rows"`
	FeedsOK       int                `json:"feeds_ok"`
	FeedsFailed   []string           `json:"feeds_failed,omitempty"`
	BinanceUSLive bool               `json:"binance_us_live"`
	CoinbaseLive  bool               `json:"coinbase_live"`
	FreeDiskGB    float64            `json:"free_disk_gb"`
	WritingPaused bool               `json:"writing_paused"`
	Backfill      map[string]string  `json:"backfill,omitempty"`
	LastPrices    map[string]float64 `json:"last_prices,omitempty"`
}

type tradeAgg struct {
	n              int
	vol, buy, sell float64
}

// Collector records live prices and news.
type Collector struct {
	cfg    Config
	ing    *paper.MarketStreamIngestor
	ring   *engine.SPSCRing
	cb     *paper.CoinbaseSignalOracle
	prices *dailyStore
	news   *dailyStore
	client *http.Client

	mu       sync.Mutex
	agg      map[string]*tradeAgg
	cbPrev   map[string]paper.CoinbaseSymbolSignal
	status   Status
	backfill map[string]string
}

// New builds a collector over running ingestors.
func New(cfg Config, ing *paper.MarketStreamIngestor, ring *engine.SPSCRing, cb *paper.CoinbaseSignalOracle) (*Collector, error) {
	if cfg.SnapshotEvery <= 0 {
		cfg.SnapshotEvery = time.Second
	}
	if cfg.NewsEvery <= 0 {
		cfg.NewsEvery = 2 * time.Minute
	}
	if cfg.FlushEvery <= 0 {
		cfg.FlushEvery = time.Minute
	}
	if cfg.MinFreeGB <= 0 {
		cfg.MinFreeGB = 20
	}
	if len(cfg.Feeds) == 0 {
		cfg.Feeds = DefaultFeeds
	}
	prices, err := newDailyStore(filepath.Join(cfg.Dir, "prices"))
	if err != nil {
		return nil, err
	}
	news, err := newDailyStore(filepath.Join(cfg.Dir, "news"))
	if err != nil {
		return nil, err
	}
	return &Collector{
		cfg: cfg, ing: ing, ring: ring, cb: cb, prices: prices, news: news,
		client:   &http.Client{Timeout: 15 * time.Second},
		agg:      map[string]*tradeAgg{},
		cbPrev:   map[string]paper.CoinbaseSymbolSignal{},
		status:   Status{StartedAt: time.Now().UTC()},
		backfill: map[string]string{},
	}, nil
}

// SetBackfillStatus records progress text for a backfill job (shown in status.json).
func (c *Collector) SetBackfillStatus(job, text string) {
	c.mu.Lock()
	c.backfill[job] = text
	c.mu.Unlock()
}

// Run blocks until ctx is cancelled, then flushes.
func (c *Collector) Run(ctx context.Context) {
	go c.drainTrades(ctx)
	// News polling runs on its own goroutine: slow or dead feeds (15s timeout each) must
	// never delay the 1-second price snapshots.
	var newsDone sync.WaitGroup
	newsDone.Add(1)
	go func() {
		defer newsDone.Done()
		c.newsLoop(ctx)
	}()

	snap := time.NewTicker(c.cfg.SnapshotEvery)
	flush := time.NewTicker(c.cfg.FlushEvery)
	defer snap.Stop()
	defer flush.Stop()
	for {
		select {
		case <-ctx.Done():
			newsDone.Wait()
			c.flushAll()
			return
		case now := <-snap.C:
			c.snapshot(now)
		case <-flush.C:
			c.flushAll()
		}
	}
}

func (c *Collector) newsLoop(ctx context.Context) {
	tracker := newNewsTracker(filepath.Join(c.cfg.Dir, "news"))
	c.pollNews(ctx, tracker)
	t := time.NewTicker(c.cfg.NewsEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.pollNews(ctx, tracker)
			tracker.prune(time.Now())
		}
	}
}

// drainTrades consumes the ingestor ring and aggregates trades per symbol.
func (c *Collector) drainTrades(ctx context.Context) {
	batch := make([]engine.MarketEvent, 256)
	for ctx.Err() == nil {
		n := c.ring.PopBatch(batch)
		if n == 0 {
			time.Sleep(2 * time.Millisecond)
			continue
		}
		c.mu.Lock()
		for _, ev := range batch[:n] {
			if ev.Kind != 0 || ev.Size <= 0 {
				continue
			}
			sym := c.ing.SymbolForID(ev.SymbolID)
			if sym == "" {
				continue
			}
			a := c.agg[sym]
			if a == nil {
				a = &tradeAgg{}
				c.agg[sym] = a
			}
			a.n++
			a.vol += ev.Size
			switch ev.Side {
			case 1:
				a.buy += ev.Size
			case 2:
				a.sell += ev.Size
			}
		}
		c.mu.Unlock()
	}
}

func (c *Collector) snapshot(now time.Time) {
	var cbState paper.CoinbaseSignalState
	if c.cb != nil {
		cbState = c.cb.Snapshot()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status.WritingPaused {
		c.agg = map[string]*tradeAgg{}
		return
	}
	last := map[string]float64{}
	_, _, _, _, _, _, lastEventNs := c.ing.Stats()
	for _, sym := range c.cfg.Symbols {
		row := PriceRow{T: now.UnixMilli(), Sym: sym}
		if book, ok := c.ing.GetL2Book(sym); ok && book != nil {
			if mid, ok := book.MidPrice(); ok && mid > 0 {
				row.Mid = mid
				row.Bid, row.BidSz = book.Bids[0].Price, book.Bids[0].Size
				row.Ask, row.AskSz = book.Asks[0].Price, book.Asks[0].Size
				for i := 0; i < 5; i++ {
					row.Bid5Sz += book.Bids[i].Size
					row.Ask5Sz += book.Asks[i].Size
				}
			}
		}
		if lastEventNs > 0 && now.Sub(time.Unix(0, lastEventNs)) > 5*time.Second {
			row.BookStale = true
		}
		if a := c.agg[sym]; a != nil {
			row.Trades, row.Vol, row.BuyVol, row.SellVol = a.n, a.vol, a.buy, a.sell
		}
		if sig, ok := cbState.Symbols[paper.NormalizeToUniverseSymbol(sym)]; ok && sig != nil {
			row.CBPrice, row.CBBid, row.CBAsk = sig.CoinbasePrice, sig.BestBid, sig.BestAsk
			prev, had := c.cbPrev[sym]
			if had && sig.TradesCount >= prev.TradesCount {
				row.CBTrades = sig.TradesCount - prev.TradesCount
				row.CBVolUSD = sig.TotalVolumeUSDT - prev.TotalVolumeUSDT
			}
			c.cbPrev[sym] = *sig
		}
		if row.Mid == 0 && row.CBPrice == 0 {
			continue // nothing to record yet
		}
		last[sym] = row.Mid
		_ = c.prices.Add(now, row)
	}
	c.agg = map[string]*tradeAgg{}
	c.status.LastPrices = last
}

func (c *Collector) pollNews(ctx context.Context, tracker *newsTracker) {
	ok, failed := 0, []string{}
	for _, feed := range c.cfg.Feeds {
		body, code, err := httpGet(ctx, c.client, feed, 4<<20)
		if err != nil || code != http.StatusOK {
			failed = append(failed, fmt.Sprintf("%s (%v %d)", feed, err, code))
			continue
		}
		now := time.Now().UTC()
		rows, err := parseRSS(body, feed, now)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", feed, err))
			continue
		}
		ok++
		for _, r := range rows {
			if tracker.fresh(r.ID, now) {
				_ = c.news.Add(now, r)
			}
		}
	}
	c.mu.Lock()
	c.status.FeedsOK, c.status.FeedsFailed = ok, failed
	c.mu.Unlock()
}

func freeGB(dir string) float64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return -1
	}
	return float64(st.Bavail) * float64(st.Bsize) / 1e9
}

func (c *Collector) flushAll() {
	free := freeGB(c.cfg.Dir)
	c.mu.Lock()
	c.status.FreeDiskGB = free
	c.status.WritingPaused = free >= 0 && free < c.cfg.MinFreeGB
	c.mu.Unlock()
	_ = c.prices.Flush()
	_ = c.news.Flush()

	c.mu.Lock()
	c.status.UpdatedAt = time.Now().UTC()
	c.status.PriceRows = c.prices.Total()
	c.status.NewsRows = c.news.Total()
	c.status.BinanceUSLive = c.ing != nil && c.ing.IsConnected()
	c.status.CoinbaseLive = c.cb != nil && c.cb.IsConnected()
	c.status.Backfill = map[string]string{}
	for k, v := range c.backfill {
		c.status.Backfill[k] = v
	}
	st := c.status
	c.mu.Unlock()
	if b, err := json.MarshalIndent(st, "", "  "); err == nil {
		tmp := filepath.Join(c.cfg.Dir, "status.json.tmp")
		if os.WriteFile(tmp, b, 0o644) == nil {
			_ = os.Rename(tmp, filepath.Join(c.cfg.Dir, "status.json"))
		}
	}
}
