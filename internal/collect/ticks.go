package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TickConfig controls the Coinbase tick recorder.
type TickConfig struct {
	Dir        string
	Endpoint   string        // default wss://ws-feed.exchange.coinbase.com (public, no key)
	Products   []string      // e.g. BTC-USD
	Depth      int           // book levels per side in each snapshot (default 10)
	BookEvery  time.Duration // default 1s
	FlushEvery time.Duration // default 60s
	MinFreeGB  float64       // stop writing below this much free disk (default 20)
}

// TradeRow is one Coinbase trade. Side is the TAKER side ("b" = buyer lifted the ask).
type TradeRow struct {
	T    int64   `json:"t"` // exchange time, unix ms
	P    float64 `json:"p"`
	S    float64 `json:"s"`
	Side string  `json:"side"`
	ID   int64   `json:"id"`
}

// BookRow is one top-of-book snapshot: [price, size] pairs, best first.
type BookRow struct {
	T   int64        `json:"t"` // local time, unix ms
	B   [][2]float64 `json:"b"`
	A   [][2]float64 `json:"a"`
	Upd int          `json:"upd"` // level changes applied since the previous snapshot
}

// TickStatus is written to <dir>/ticks_status.json every flush.
type TickStatus struct {
	UpdatedAt  time.Time        `json:"updated_at"`
	Connected  bool             `json:"connected"`
	Level2     bool             `json:"level2"`
	Reconnects int              `json:"reconnects"`
	Trades     map[string]int64 `json:"trades"`
	BookRows   map[string]int64 `json:"book_rows"`
	TradeGaps  map[string]int64 `json:"trade_id_gaps"`
	LastTrade  map[string]int64 `json:"last_trade_ms"`
	Paused     bool             `json:"writing_paused"`
	LastError  string           `json:"last_error,omitempty"`
}

type l2Book struct {
	bids, asks map[float64]float64
	ready      bool
	upd        int
}

// TickRecorder records every Coinbase trade and periodic order-book snapshots to daily gzip files:
// <dir>/ticks/<product>/YYYY-MM-DD.jsonl.gz and <dir>/book/<product>/YYYY-MM-DD.jsonl.gz.
type TickRecorder struct {
	cfg    TickConfig
	trades map[string]*dailyStore
	books  map[string]*dailyStore

	mu      sync.Mutex
	book    map[string]*l2Book
	lastID  map[string]int64
	status  TickStatus
	paused  bool
	noLevel bool // level2_batch refused by the server: record trades only
}

// NewTickRecorder creates the output directories.
func NewTickRecorder(cfg TickConfig) (*TickRecorder, error) {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "wss://ws-feed.exchange.coinbase.com"
	}
	if cfg.Depth <= 0 {
		cfg.Depth = 10
	}
	if cfg.BookEvery <= 0 {
		cfg.BookEvery = time.Second
	}
	if cfg.FlushEvery <= 0 {
		cfg.FlushEvery = time.Minute
	}
	if cfg.MinFreeGB <= 0 {
		cfg.MinFreeGB = 20
	}
	r := &TickRecorder{cfg: cfg, trades: map[string]*dailyStore{}, books: map[string]*dailyStore{},
		book: map[string]*l2Book{}, lastID: map[string]int64{},
		status: TickStatus{Trades: map[string]int64{}, BookRows: map[string]int64{}, TradeGaps: map[string]int64{},
			LastTrade: map[string]int64{}}}
	for _, p := range cfg.Products {
		var err error
		if r.trades[p], err = newDailyStore(filepath.Join(cfg.Dir, "ticks", sanitize(p))); err != nil {
			return nil, err
		}
		if r.books[p], err = newDailyStore(filepath.Join(cfg.Dir, "book", sanitize(p))); err != nil {
			return nil, err
		}
		r.book[p] = &l2Book{}
	}
	return r, nil
}

// Run streams until ctx is cancelled, reconnecting with backoff, then flushes.
func (r *TickRecorder) Run(ctx context.Context) {
	go r.streamLoop(ctx)
	snap := time.NewTicker(r.cfg.BookEvery)
	flush := time.NewTicker(r.cfg.FlushEvery)
	defer snap.Stop()
	defer flush.Stop()
	for {
		select {
		case <-ctx.Done():
			r.flush()
			return
		case now := <-snap.C:
			r.snapshotBooks(now)
		case <-flush.C:
			r.flush()
		}
	}
}

func (r *TickRecorder) streamLoop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := r.streamOnce(ctx)
		r.mu.Lock()
		r.status.Connected = false
		if ctx.Err() == nil {
			r.status.Reconnects++
		}
		if err != nil && ctx.Err() == nil {
			r.status.LastError = err.Error()
		}
		for _, b := range r.book {
			b.ready = false // a fresh snapshot is required after any disconnect
		}
		r.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > 5*time.Minute {
			backoff = time.Second
		}
		log.Printf("[TICKS] stream ended (%v); reconnecting in %s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

func (r *TickRecorder) streamOnce(ctx context.Context) error {
	ws, err := dialWS(ctx, r.cfg.Endpoint)
	if err != nil {
		return err
	}
	defer ws.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = ws.Close()
		case <-stop:
		}
	}()
	r.mu.Lock()
	channels := `"matches","level2_batch"`
	if r.noLevel {
		channels = `"matches"`
	}
	r.status.Level2 = !r.noLevel
	r.mu.Unlock()
	q := make([]string, len(r.cfg.Products))
	for i, p := range r.cfg.Products {
		q[i] = strconv.Quote(p)
	}
	if err := ws.WriteText(fmt.Sprintf(`{"type":"subscribe","product_ids":[%s],"channels":[%s]}`,
		strings.Join(q, ","), channels)); err != nil {
		return err
	}
	r.mu.Lock()
	r.status.Connected = true
	r.mu.Unlock()
	for {
		msg, err := ws.ReadMessage(30 * time.Second)
		if err != nil {
			return err
		}
		if err := r.HandleMessage(msg, time.Now()); err != nil {
			return err
		}
	}
}

type cbMsg struct {
	Type      string      `json:"type"`
	ProductID string      `json:"product_id"`
	Time      string      `json:"time"`
	TradeID   int64       `json:"trade_id"`
	Price     string      `json:"price"`
	Size      string      `json:"size"`
	Side      string      `json:"side"`
	Bids      [][2]string `json:"bids"`
	Asks      [][2]string `json:"asks"`
	Changes   [][3]string `json:"changes"`
	Message   string      `json:"message"`
	Reason    string      `json:"reason"`
}

// HandleMessage applies one feed message. It returns an error only when the stream must be restarted.
func (r *TickRecorder) HandleMessage(raw []byte, now time.Time) error {
	var m cbMsg
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil // skip malformed rows
	}
	switch m.Type {
	case "match":
		return r.onMatch(&m, now)
	case "snapshot":
		r.onSnapshot(&m)
	case "l2update":
		r.onUpdate(&m)
	case "error":
		r.mu.Lock()
		defer r.mu.Unlock()
		if !r.noLevel && strings.Contains(strings.ToLower(m.Message+m.Reason), "level2") {
			r.noLevel = true
		}
		return fmt.Errorf("feed error: %s %s", m.Message, m.Reason)
	}
	return nil
}

func (r *TickRecorder) onMatch(m *cbMsg, now time.Time) error {
	st, ok := r.trades[m.ProductID]
	if !ok {
		return nil
	}
	p, err1 := strconv.ParseFloat(m.Price, 64)
	s, err2 := strconv.ParseFloat(m.Size, 64)
	if err1 != nil || err2 != nil {
		return nil
	}
	t := now
	if et, err := time.Parse(time.RFC3339Nano, m.Time); err == nil {
		t = et
	}
	side := "b" // Coinbase reports the maker side; the taker is the opposite
	if m.Side == "buy" {
		side = "s"
	}
	r.mu.Lock()
	if last := r.lastID[m.ProductID]; last > 0 && m.TradeID > last+1 {
		r.status.TradeGaps[m.ProductID] += m.TradeID - last - 1
	}
	if m.TradeID > r.lastID[m.ProductID] {
		r.lastID[m.ProductID] = m.TradeID
	}
	paused := r.paused
	if !paused {
		r.status.Trades[m.ProductID]++
	}
	r.status.LastTrade[m.ProductID] = t.UnixMilli()
	r.mu.Unlock()
	if paused {
		return nil
	}
	return st.Add(t, TradeRow{T: t.UnixMilli(), P: p, S: s, Side: side, ID: m.TradeID})
}

func parseLevels(rows [][2]string) map[float64]float64 {
	out := make(map[float64]float64, len(rows))
	for _, x := range rows {
		p, err1 := strconv.ParseFloat(x[0], 64)
		s, err2 := strconv.ParseFloat(x[1], 64)
		if err1 == nil && err2 == nil && s > 0 {
			out[p] = s
		}
	}
	return out
}

func (r *TickRecorder) onSnapshot(m *cbMsg) {
	bids, asks := parseLevels(m.Bids), parseLevels(m.Asks)
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.book[m.ProductID]
	if !ok {
		return
	}
	b.bids, b.asks, b.ready, b.upd = bids, asks, true, 0
}

func (r *TickRecorder) onUpdate(m *cbMsg) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.book[m.ProductID]
	if !ok || !b.ready {
		return
	}
	for _, c := range m.Changes {
		p, err1 := strconv.ParseFloat(c[1], 64)
		s, err2 := strconv.ParseFloat(c[2], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		side := b.asks
		if c[0] == "buy" {
			side = b.bids
		}
		if s <= 0 {
			delete(side, p)
		} else {
			side[p] = s
		}
		b.upd++
	}
}

// topN returns the best n levels: highest prices first for bids, lowest first for asks. O(len(levels)*n).
func topN(levels map[float64]float64, n int, bids bool) [][2]float64 {
	out := make([][2]float64, 0, n)
	for p, s := range levels {
		i := len(out)
		for i > 0 && ((bids && p > out[i-1][0]) || (!bids && p < out[i-1][0])) {
			i--
		}
		if i >= n {
			continue
		}
		if len(out) < n {
			out = append(out, [2]float64{})
		}
		copy(out[i+1:], out[i:len(out)-1])
		out[i] = [2]float64{p, s}
	}
	return out
}

func (r *TickRecorder) snapshotBooks(now time.Time) {
	type pending struct {
		st  *dailyStore
		row BookRow
	}
	var rows []pending
	r.mu.Lock()
	if !r.paused {
		for p, b := range r.book {
			if !b.ready || len(b.bids) == 0 || len(b.asks) == 0 {
				continue
			}
			rows = append(rows, pending{r.books[p], BookRow{T: now.UnixMilli(), B: topN(b.bids, r.cfg.Depth, true),
				A: topN(b.asks, r.cfg.Depth, false), Upd: b.upd}})
			b.upd = 0
			r.status.BookRows[p]++
		}
	}
	r.mu.Unlock()
	for _, x := range rows {
		_ = x.st.Add(now, x.row)
	}
}

func (r *TickRecorder) flush() {
	free := freeGB(r.cfg.Dir)
	for _, p := range r.cfg.Products {
		_ = r.trades[p].Flush()
		_ = r.books[p].Flush()
	}
	r.mu.Lock()
	r.paused = free >= 0 && free < r.cfg.MinFreeGB
	r.status.Paused = r.paused
	r.status.UpdatedAt = time.Now().UTC()
	b, err := json.MarshalIndent(r.status, "", "  ")
	r.mu.Unlock()
	if err == nil {
		tmp := filepath.Join(r.cfg.Dir, "ticks_status.json.tmp")
		if os.WriteFile(tmp, b, 0o644) == nil {
			_ = os.Rename(tmp, filepath.Join(r.cfg.Dir, "ticks_status.json"))
		}
	}
}
