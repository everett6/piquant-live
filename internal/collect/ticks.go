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

// TickConfig controls the multi-venue tick recorder (public market data only, no keys).
type TickConfig struct {
	Dir        string
	Endpoint   string        // Coinbase, default wss://ws-feed.exchange.coinbase.com
	Products   []string      // Coinbase products, e.g. BTC-USD (stored as ticks/BTC-USD, book/BTC-USD)
	BinanceUS  []string      // Binance.US symbols, e.g. BTCUSDT (stored as binanceus-BTCUSDT)
	Kraken     []string      // Kraken v2 symbols, e.g. BTC/USD (stored as kraken-BTC-USD)
	BinanceURL string        // default wss://stream.binance.us:9443/stream
	KrakenURL  string        // default wss://ws.kraken.com/v2
	Depth      int           // book levels per side in each snapshot (default 10)
	BookEvery  time.Duration // default 1s
	FlushEvery time.Duration // default 60s
	MinFreeGB  float64       // stop writing below this much free disk (default 20)
}

// TradeRow is one trade. Side is the TAKER side ("b" = buyer lifted the ask).
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

// TickStatus is written to <dir>/ticks_status.json every flush. Per-venue maps are keyed by venue name,
// per-stream maps by store key (BTC-USD, binanceus-BTCUSDT, kraken-BTC-USD, ...).
type TickStatus struct {
	UpdatedAt  time.Time         `json:"updated_at"`
	Connected  map[string]bool   `json:"connected"`
	Level2     bool              `json:"level2"` // Coinbase level2_batch accepted
	Reconnects map[string]int    `json:"reconnects"`
	Trades     map[string]int64  `json:"trades"`
	BookRows   map[string]int64  `json:"book_rows"`
	TradeGaps  map[string]int64  `json:"trade_id_gaps"`
	LastTrade  map[string]int64  `json:"last_trade_ms"`
	Paused     bool              `json:"writing_paused"`
	LastError  map[string]string `json:"last_error,omitempty"`
}

type l2Book struct {
	bids, asks map[float64]float64
	ready      bool
	upd        int
}

// tickVenue is one websocket source: where to connect, what to send, how to parse, which streams it owns.
type tickVenue struct {
	name   string
	url    string
	subs   func(r *TickRecorder) []string
	handle func(r *TickRecorder, raw []byte, now time.Time) error
	keys   []string
}

// TickRecorder records every trade and periodic order-book snapshots, per venue and product, to daily gzip
// files: <dir>/ticks/<key>/YYYY-MM-DD.jsonl.gz and <dir>/book/<key>/YYYY-MM-DD.jsonl.gz.
type TickRecorder struct {
	cfg    TickConfig
	venues []*tickVenue
	trades map[string]*dailyStore
	books  map[string]*dailyStore

	mu      sync.Mutex
	book    map[string]*l2Book
	lastID  map[string]int64
	status  TickStatus
	paused  bool
	noLevel bool // Coinbase level2_batch refused by the server: record its trades only
}

func binanceKey(sym string) string { return "binanceus-" + strings.ToUpper(sym) }
func krakenKey(sym string) string  { return "kraken-" + strings.ReplaceAll(sym, "/", "-") }

// NewTickRecorder creates the output directories for every configured stream.
func NewTickRecorder(cfg TickConfig) (*TickRecorder, error) {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "wss://ws-feed.exchange.coinbase.com"
	}
	if cfg.BinanceURL == "" {
		cfg.BinanceURL = "wss://stream.binance.us:9443/stream"
	}
	if cfg.KrakenURL == "" {
		cfg.KrakenURL = "wss://ws.kraken.com/v2"
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
		status: TickStatus{Connected: map[string]bool{}, Reconnects: map[string]int{}, Trades: map[string]int64{},
			BookRows: map[string]int64{}, TradeGaps: map[string]int64{}, LastTrade: map[string]int64{},
			LastError: map[string]string{}}}
	if len(cfg.Products) > 0 {
		r.venues = append(r.venues, &tickVenue{name: "coinbase", url: cfg.Endpoint, subs: coinbaseSubs,
			handle: (*TickRecorder).HandleMessage, keys: cfg.Products})
	}
	if len(cfg.BinanceUS) > 0 {
		var streams, keys []string
		for _, s := range cfg.BinanceUS {
			l := strings.ToLower(s)
			streams = append(streams, l+"@trade", l+"@depth10@1000ms")
			keys = append(keys, binanceKey(s))
		}
		r.venues = append(r.venues, &tickVenue{name: "binanceus", url: cfg.BinanceURL + "?streams=" + strings.Join(streams, "/"),
			subs: func(*TickRecorder) []string { return nil }, handle: (*TickRecorder).handleBinance, keys: keys})
	}
	if len(cfg.Kraken) > 0 {
		var keys []string
		for _, s := range cfg.Kraken {
			keys = append(keys, krakenKey(s))
		}
		r.venues = append(r.venues, &tickVenue{name: "kraken", url: cfg.KrakenURL, subs: krakenSubs,
			handle: (*TickRecorder).handleKraken, keys: keys})
	}
	for _, v := range r.venues {
		for _, k := range v.keys {
			var err error
			if r.trades[k], err = newDailyStore(filepath.Join(cfg.Dir, "ticks", sanitize(k))); err != nil {
				return nil, err
			}
			if r.books[k], err = newDailyStore(filepath.Join(cfg.Dir, "book", sanitize(k))); err != nil {
				return nil, err
			}
			r.book[k] = &l2Book{}
		}
	}
	return r, nil
}

// Run streams every venue until ctx is cancelled, reconnecting with backoff, then flushes.
func (r *TickRecorder) Run(ctx context.Context) {
	for _, v := range r.venues {
		go r.streamLoop(ctx, v)
	}
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

func (r *TickRecorder) streamLoop(ctx context.Context, v *tickVenue) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := r.streamOnce(ctx, v)
		r.mu.Lock()
		r.status.Connected[v.name] = false
		if ctx.Err() == nil {
			r.status.Reconnects[v.name]++
			if err != nil {
				r.status.LastError[v.name] = err.Error()
			}
		}
		for _, k := range v.keys {
			r.book[k].ready = false // a fresh snapshot is required after any disconnect
		}
		r.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > 5*time.Minute {
			backoff = time.Second
		}
		log.Printf("[TICKS] %s stream ended (%v); reconnecting in %s", v.name, err, backoff)
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

func (r *TickRecorder) streamOnce(ctx context.Context, v *tickVenue) error {
	ws, err := dialWS(ctx, v.url)
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
	for _, s := range v.subs(r) {
		if err := ws.WriteText(s); err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.status.Connected[v.name] = true
	r.mu.Unlock()
	for {
		msg, err := ws.ReadMessage(30 * time.Second)
		if err != nil {
			return err
		}
		if err := v.handle(r, msg, time.Now()); err != nil {
			return err
		}
	}
}

// ---------- shared book and trade bookkeeping ----------

// addTrade stores one trade for a stream key and tracks trade-id gaps.
func (r *TickRecorder) addTrade(key string, t time.Time, p, s float64, side string, id int64) error {
	st, ok := r.trades[key]
	if !ok || p <= 0 || s <= 0 {
		return nil
	}
	r.mu.Lock()
	if last := r.lastID[key]; last > 0 && id > last+1 {
		r.status.TradeGaps[key] += id - last - 1
	}
	if id > r.lastID[key] {
		r.lastID[key] = id
	}
	paused := r.paused
	if !paused {
		r.status.Trades[key]++
	}
	r.status.LastTrade[key] = t.UnixMilli()
	r.mu.Unlock()
	if paused {
		return nil
	}
	return st.Add(t, TradeRow{T: t.UnixMilli(), P: p, S: s, Side: side, ID: id})
}

// setBook replaces a stream's book with a full snapshot. periodic marks venues that stream whole top-N
// snapshots (Binance.US depth10), where each snapshot counts as one update; otherwise the count restarts.
func (r *TickRecorder) setBook(key string, bids, asks map[float64]float64, periodic bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.book[key]; ok {
		upd := 0
		if periodic && b.ready {
			upd = b.upd + 1
		} else if periodic {
			upd = 1
		}
		b.bids, b.asks, b.ready, b.upd = bids, asks, true, upd
	}
}

// applyLevel sets (or deletes, when s <= 0) one price level of a ready book. Caller holds r.mu.
func (b *l2Book) applyLevel(bid bool, p, s float64) {
	side := b.asks
	if bid {
		side = b.bids
	}
	if s <= 0 {
		delete(side, p)
	} else {
		side[p] = s
	}
	b.upd++
}

// truncate keeps only the best n levels of one side (venues that stream a fixed depth expect this).
func truncate(levels map[float64]float64, n int, bids bool) {
	if len(levels) <= n {
		return
	}
	keep := map[float64]bool{}
	for _, l := range topN(levels, n, bids) {
		keep[l[0]] = true
	}
	for p := range levels {
		if !keep[p] {
			delete(levels, p)
		}
	}
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

// ---------- Coinbase (Exchange feed: matches + level2_batch) ----------

func coinbaseSubs(r *TickRecorder) []string {
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
	return []string{fmt.Sprintf(`{"type":"subscribe","product_ids":[%s],"channels":[%s]}`, strings.Join(q, ","), channels)}
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

// HandleMessage applies one Coinbase feed message. It returns an error only when the stream must restart.
func (r *TickRecorder) HandleMessage(raw []byte, now time.Time) error {
	var m cbMsg
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil // skip malformed rows
	}
	switch m.Type {
	case "match":
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
		return r.addTrade(m.ProductID, t, p, s, side, m.TradeID)
	case "snapshot":
		r.setBook(m.ProductID, parseLevels(m.Bids), parseLevels(m.Asks), false)
	case "l2update":
		r.mu.Lock()
		defer r.mu.Unlock()
		b, ok := r.book[m.ProductID]
		if !ok || !b.ready {
			return nil
		}
		for _, c := range m.Changes {
			p, err1 := strconv.ParseFloat(c[1], 64)
			s, err2 := strconv.ParseFloat(c[2], 64)
			if err1 == nil && err2 == nil {
				b.applyLevel(c[0] == "buy", p, s)
			}
		}
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

// ---------- Binance.US (combined stream: <sym>@trade + <sym>@depth10@1000ms) ----------

type bnMsg struct {
	Stream string `json:"stream"`
	Data   struct {
		P    string      `json:"p"`
		Q    string      `json:"q"`
		T    int64       `json:"T"`
		M    bool        `json:"m"` // buyer is maker, so the taker sold
		ID   int64       `json:"t"`
		Bids [][2]string `json:"bids"`
		Asks [][2]string `json:"asks"`
	} `json:"data"`
}

func (r *TickRecorder) handleBinance(raw []byte, now time.Time) error {
	var m bnMsg
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	sym, kind, ok := strings.Cut(m.Stream, "@")
	if !ok {
		return nil
	}
	key := binanceKey(sym)
	switch {
	case kind == "trade":
		p, err1 := strconv.ParseFloat(m.Data.P, 64)
		s, err2 := strconv.ParseFloat(m.Data.Q, 64)
		if err1 != nil || err2 != nil {
			return nil
		}
		side := "b"
		if m.Data.M {
			side = "s"
		}
		t := now
		if m.Data.T > 0 {
			t = time.UnixMilli(m.Data.T)
		}
		return r.addTrade(key, t, p, s, side, m.Data.ID)
	case strings.HasPrefix(kind, "depth"):
		r.setBook(key, parseLevels(m.Data.Bids), parseLevels(m.Data.Asks), true)
	}
	return nil
}

// ---------- Kraken (v2: trade + book depth 10) ----------

func krakenSubs(r *TickRecorder) []string {
	q := make([]string, len(r.cfg.Kraken))
	for i, s := range r.cfg.Kraken {
		q[i] = strconv.Quote(s)
	}
	syms := strings.Join(q, ",")
	return []string{
		fmt.Sprintf(`{"method":"subscribe","params":{"channel":"trade","symbol":[%s]}}`, syms),
		fmt.Sprintf(`{"method":"subscribe","params":{"channel":"book","symbol":[%s],"depth":%d}}`, syms, r.cfg.Depth),
	}
}

type krLevel struct {
	Price float64 `json:"price"`
	Qty   float64 `json:"qty"`
}

type krMsg struct {
	Channel string `json:"channel"`
	Type    string `json:"type"`
	Method  string `json:"method"`
	Success *bool  `json:"success"`
	Error   string `json:"error"`
	Data    []struct {
		Symbol    string    `json:"symbol"`
		Side      string    `json:"side"` // trade: the taker side
		Price     float64   `json:"price"`
		Qty       float64   `json:"qty"`
		TradeID   int64     `json:"trade_id"`
		Timestamp string    `json:"timestamp"`
		Bids      []krLevel `json:"bids"`
		Asks      []krLevel `json:"asks"`
	} `json:"data"`
}

func (r *TickRecorder) handleKraken(raw []byte, now time.Time) error {
	var m krMsg
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	if m.Method == "subscribe" && m.Success != nil && !*m.Success {
		return fmt.Errorf("kraken subscribe failed: %s", m.Error)
	}
	switch m.Channel {
	case "trade":
		for _, d := range m.Data {
			t := now
			if et, err := time.Parse(time.RFC3339Nano, d.Timestamp); err == nil {
				t = et
			}
			side := "b"
			if d.Side == "sell" {
				side = "s"
			}
			if err := r.addTrade(krakenKey(d.Symbol), t, d.Price, d.Qty, side, d.TradeID); err != nil {
				return err
			}
		}
	case "book":
		for _, d := range m.Data {
			key := krakenKey(d.Symbol)
			if m.Type == "snapshot" {
				bids, asks := map[float64]float64{}, map[float64]float64{}
				for _, l := range d.Bids {
					if l.Qty > 0 {
						bids[l.Price] = l.Qty
					}
				}
				for _, l := range d.Asks {
					if l.Qty > 0 {
						asks[l.Price] = l.Qty
					}
				}
				r.setBook(key, bids, asks, false)
				continue
			}
			r.mu.Lock()
			if b, ok := r.book[key]; ok && b.ready {
				for _, l := range d.Bids {
					b.applyLevel(true, l.Price, l.Qty)
				}
				for _, l := range d.Asks {
					b.applyLevel(false, l.Price, l.Qty)
				}
				truncate(b.bids, r.cfg.Depth, true) // levels pushed past the subscribed depth are not deleted by Kraken
				truncate(b.asks, r.cfg.Depth, false)
			}
			r.mu.Unlock()
		}
	}
	return nil
}

// ---------- snapshots and flushing ----------

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
		for k, b := range r.book {
			if !b.ready || len(b.bids) == 0 || len(b.asks) == 0 {
				continue
			}
			rows = append(rows, pending{r.books[k], BookRow{T: now.UnixMilli(), B: topN(b.bids, r.cfg.Depth, true),
				A: topN(b.asks, r.cfg.Depth, false), Upd: b.upd}})
			b.upd = 0
			r.status.BookRows[k]++
		}
	}
	r.mu.Unlock()
	for _, x := range rows {
		_ = x.st.Add(now, x.row)
	}
}

func (r *TickRecorder) flush() {
	free := freeGB(r.cfg.Dir)
	for k := range r.trades {
		_ = r.trades[k].Flush()
		_ = r.books[k].Flush()
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
