package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Trade-gap backfill: finds trade-id gaps in the recorded ticks/<stream>/<day>.jsonl.gz files and fetches the
// missing trades from each venue's free public REST history. The fetched trades go to a SEPARATE tree,
// ticks_backfill/<stream>/<day>.jsonl.gz, with backfill=true on every row. The live tick files are read only.

// Public REST bases (no keys). Variables so tests can point them at a local server.
var (
	KrakenRESTBase    = "https://api.kraken.com"
	BinanceUSRESTBase = "https://api.binance.us"
)

// Minimum delay between two requests to the same venue, well under each venue's public limit
// (Coinbase ~10/s, Kraken ~1/s, Binance.US 1200 weight/min at ~25 weight per historicalTrades call).
var GapFillSpacing = map[string]time.Duration{
	"coinbase":  250 * time.Millisecond,
	"kraken":    1500 * time.Millisecond,
	"binanceus": 1500 * time.Millisecond,
}

// gapFillBackoff is the first wait after a rate-limit or server error; it doubles on each retry.
var gapFillBackoff = 2 * time.Second

// BackfillTradeRow is a trade fetched after the fact; it carries the same fields as TradeRow plus its origin.
type BackfillTradeRow struct {
	TradeRow
	Backfill bool   `json:"backfill"`
	Src      string `json:"src"` // e.g. coinbase-rest
}

// TradeGap is a run of missing trade ids: every id strictly between After and Before is absent.
type TradeGap struct {
	Stream  string `json:"stream"`
	Day     string `json:"day"` // day file holding the trade after the gap
	After   int64  `json:"after_id"`
	Before  int64  `json:"before_id"`
	AfterT  int64  `json:"after_t"` // unix ms of the trade before the gap
	BeforeT int64  `json:"before_t"`
}

// Missing is the number of trade ids in the gap.
func (g TradeGap) Missing() int64 { return g.Before - g.After - 1 }

// GapFillConfig controls one backfill run.
type GapFillConfig struct {
	Dir      string   // recorder dir holding ticks/
	OutDir   string   // default <Dir>/ticks_backfill; must be outside <Dir>/ticks
	Streams  []string // stream dirs under ticks/ (empty = all)
	From, To string   // inclusive YYYY-MM-DD day range of tick files to scan (empty = open)
	MaxGap   int64    // skip gaps with more missing ids than this (default 3,000,000)
	DryRun   bool     // find and report gaps, fetch nothing
	Client   *http.Client
	Progress func(string)
}

// GapFillStream summarises one stream of a run.
type GapFillStream struct {
	Gaps      int    `json:"gaps"`
	Missing   int64  `json:"missing"`
	Filled    int64  `json:"filled"`
	Skipped   int64  `json:"skipped_too_large"`
	Remaining int64  `json:"remaining"` // missing - filled: ids the venue did not return (or skipped/failed)
	Error     string `json:"error,omitempty"`
}

// GapFillReport is written to <OutDir>/backfill_report.json after a (non-dry) run.
type GapFillReport struct {
	RunAt   time.Time                 `json:"run_at"`
	From    string                    `json:"from,omitempty"`
	To      string                    `json:"to,omitempty"`
	DryRun  bool                      `json:"dry_run"`
	Streams map[string]*GapFillStream `json:"streams"`
	Gaps    []TradeGap                `json:"gaps"`
}

type idT struct{ id, t int64 }

// readDayIDs returns the (id, t) of every trade in the given files, sorted by id, duplicates removed.
func readDayIDs(paths ...string) ([]idT, error) {
	var out []idT
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		err := ReadRows(p, func(line []byte) error {
			var r struct {
				T  int64 `json:"t"`
				ID int64 `json:"id"`
			}
			if json.Unmarshal(line, &r) == nil && r.ID > 0 {
				out = append(out, idT{r.ID, r.T})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	uniq := out[:0]
	for i, x := range out {
		if i == 0 || x.id != out[i-1].id {
			uniq = append(uniq, x)
		}
	}
	return uniq, nil
}

// FindTradeGaps lists the id gaps of one stream over the day files in [from, to] (inclusive, empty = open).
// Trades already in the backfill tree count as present, so a rerun only finds what is still missing. A gap across
// midnight is found when both calendar-consecutive day files exist.
func FindTradeGaps(ticksDir, backfillDir, stream, from, to string) ([]TradeGap, error) {
	files, _ := filepath.Glob(filepath.Join(ticksDir, stream, "*.jsonl.gz"))
	sort.Strings(files)
	var gaps []TradeGap
	var prev *idT
	var prevDay time.Time
	for _, f := range files {
		day := strings.TrimSuffix(filepath.Base(f), ".jsonl.gz")
		d, err := time.Parse("2006-01-02", day)
		if err != nil || (from != "" && day < from) || (to != "" && day > to) {
			continue
		}
		ids, err := readDayIDs(f, filepath.Join(backfillDir, stream, day+".jsonl.gz"))
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			continue
		}
		if prev != nil && d.Sub(prevDay) == 24*time.Hour && ids[0].id > prev.id+1 {
			gaps = append(gaps, TradeGap{stream, day, prev.id, ids[0].id, prev.t, ids[0].t})
		}
		for i := 1; i < len(ids); i++ {
			if ids[i].id > ids[i-1].id+1 {
				gaps = append(gaps, TradeGap{stream, day, ids[i-1].id, ids[i].id, ids[i-1].t, ids[i].t})
			}
		}
		last := ids[len(ids)-1]
		prev, prevDay = &last, d
	}
	return gaps, nil
}

// venueOf maps a stream dir name to its venue and REST symbol.
func venueOf(stream string) (venue, symbol string) {
	switch {
	case strings.HasPrefix(stream, "binanceus-"):
		return "binanceus", strings.TrimPrefix(stream, "binanceus-")
	case strings.HasPrefix(stream, "kraken-"):
		return "kraken", strings.ReplaceAll(strings.TrimPrefix(stream, "kraken-"), "-", "")
	default:
		return "coinbase", stream
	}
}

// restGetter does paced GETs with retry on rate limits and server errors.
type restGetter struct {
	client  *http.Client
	spacing time.Duration
	last    time.Time
}

func (g *restGetter) get(ctx context.Context, u string, out any) (http.Header, error) {
	backoff := gapFillBackoff
	for attempt := 0; ; attempt++ {
		if wait := g.spacing - time.Since(g.last); wait > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		g.last = time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "piquant-backfill")
		resp, err := g.client.Do(req)
		retry := err != nil
		var body []byte
		if err == nil {
			body, err = io.ReadAll(io.LimitReader(resp.Body, 32<<20))
			resp.Body.Close()
			retry = err != nil || resp.StatusCode == 429 || resp.StatusCode == 418 || resp.StatusCode >= 500
			if !retry && resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("GET %s: HTTP %d: %.200s", u, resp.StatusCode, body)
			}
		}
		if retry {
			if attempt >= 6 {
				if err == nil {
					err = fmt.Errorf("HTTP %d", resp.StatusCode)
				}
				return nil, fmt.Errorf("GET %s: giving up: %w", u, err)
			}
			wait := backoff
			if resp != nil {
				if s, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && s > 0 {
					wait = time.Duration(s) * time.Second
				}
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			backoff *= 2
			continue
		}
		if err := json.Unmarshal(body, out); err != nil {
			return nil, fmt.Errorf("GET %s: %w", u, err)
		}
		return resp.Header, nil
	}
}

// fetchGap calls emit for every trade the venue returns with After < id < Before. emit receives pages as they
// arrive, so a long gap is written incrementally.
func fetchGap(ctx context.Context, g *restGetter, venue, symbol string, gap TradeGap, emit func([]TradeRow) error) error {
	switch venue {
	case "coinbase":
		return fetchCoinbaseGap(ctx, g, symbol, gap, emit)
	case "kraken":
		return fetchKrakenGap(ctx, g, symbol, gap, emit)
	case "binanceus":
		return fetchBinanceGap(ctx, g, symbol, gap, emit)
	}
	return fmt.Errorf("unknown venue %q", venue)
}

// Coinbase Exchange: /products/{id}/trades?after=X returns up to 1000 trades with id < X, newest first.
// side is the maker side, so the taker is the opposite (same mapping as the websocket recorder).
func fetchCoinbaseGap(ctx context.Context, g *restGetter, product string, gap TradeGap, emit func([]TradeRow) error) error {
	cursor := gap.Before
	for cursor > gap.After+1 {
		var page []struct {
			TradeID int64  `json:"trade_id"`
			Side    string `json:"side"`
			Size    string `json:"size"`
			Price   string `json:"price"`
			Time    string `json:"time"`
		}
		u := fmt.Sprintf("%s/products/%s/trades?limit=1000&after=%d", CoinbaseCandlesBase, url.PathEscape(product), cursor)
		if _, err := g.get(ctx, u, &page); err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		var rows []TradeRow
		lowest := cursor
		for _, x := range page {
			if x.TradeID < lowest {
				lowest = x.TradeID
			}
			if x.TradeID <= gap.After || x.TradeID >= gap.Before {
				continue
			}
			p, err1 := strconv.ParseFloat(x.Price, 64)
			s, err2 := strconv.ParseFloat(x.Size, 64)
			t, err3 := time.Parse(time.RFC3339Nano, x.Time)
			if err1 != nil || err2 != nil || err3 != nil {
				continue
			}
			side := "b"
			if x.Side == "buy" {
				side = "s"
			}
			rows = append(rows, TradeRow{T: t.UnixMilli(), P: p, S: s, Side: side, ID: x.TradeID})
		}
		if err := emit(rows); err != nil {
			return err
		}
		if lowest >= cursor {
			return nil // no progress
		}
		cursor = lowest
	}
	return nil
}

// Kraken: /0/public/Trades?pair=X&since=<ns> returns up to 1000 trades after the timestamp, oldest first, as
// [price, volume, time, side(taker b/s), type, misc, trade_id]; "last" is the cursor for the next page.
func fetchKrakenGap(ctx context.Context, g *restGetter, pair string, gap TradeGap, emit func([]TradeRow) error) error {
	since := (gap.AfterT - 1000) * int64(time.Millisecond) // 1 s early: ms rounding, filtered by id below
	for {
		var resp struct {
			Error  []string                   `json:"error"`
			Result map[string]json.RawMessage `json:"result"`
		}
		u := fmt.Sprintf("%s/0/public/Trades?pair=%s&since=%d&count=1000", KrakenRESTBase, url.QueryEscape(pair), since)
		if _, err := g.get(ctx, u, &resp); err != nil {
			return err
		}
		if len(resp.Error) > 0 {
			return fmt.Errorf("kraken %s: %s", pair, strings.Join(resp.Error, "; "))
		}
		var next int64
		var raw [][]any
		for k, v := range resp.Result {
			if k == "last" {
				var s string
				if json.Unmarshal(v, &s) == nil {
					next, _ = strconv.ParseInt(s, 10, 64)
				}
				continue
			}
			_ = json.Unmarshal(v, &raw)
		}
		if len(raw) == 0 {
			return nil
		}
		var rows []TradeRow
		var highest int64
		for _, x := range raw {
			if len(x) < 7 {
				continue
			}
			id := int64(toFloat(x[6]))
			if id > highest {
				highest = id
			}
			if id <= gap.After || id >= gap.Before {
				continue
			}
			side := "b"
			if s, _ := x[3].(string); s == "s" {
				side = "s"
			}
			ts := toFloat(x[2])
			rows = append(rows, TradeRow{T: int64(ts*1000 + 0.5), P: toFloat(x[0]), S: toFloat(x[1]), Side: side, ID: id})
		}
		if err := emit(rows); err != nil {
			return err
		}
		if highest >= gap.Before-1 || next <= since {
			return nil
		}
		since = next
	}
}

// Binance.US: /api/v3/historicalTrades?fromId=X returns up to 1000 trades with id >= X, oldest first (no key).
func fetchBinanceGap(ctx context.Context, g *restGetter, symbol string, gap TradeGap, emit func([]TradeRow) error) error {
	from := gap.After + 1
	for from < gap.Before {
		var page []struct {
			ID           int64  `json:"id"`
			Price        string `json:"price"`
			Qty          string `json:"qty"`
			Time         int64  `json:"time"`
			IsBuyerMaker bool   `json:"isBuyerMaker"`
		}
		u := fmt.Sprintf("%s/api/v3/historicalTrades?symbol=%s&fromId=%d&limit=1000", BinanceUSRESTBase, url.QueryEscape(symbol), from)
		if _, err := g.get(ctx, u, &page); err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		var rows []TradeRow
		highest := from - 1
		for _, x := range page {
			if x.ID > highest {
				highest = x.ID
			}
			if x.ID <= gap.After || x.ID >= gap.Before {
				continue
			}
			p, err1 := strconv.ParseFloat(x.Price, 64)
			s, err2 := strconv.ParseFloat(x.Qty, 64)
			if err1 != nil || err2 != nil {
				continue
			}
			side := "b"
			if x.IsBuyerMaker {
				side = "s"
			}
			rows = append(rows, TradeRow{T: x.Time, P: p, S: s, Side: side, ID: x.ID})
		}
		if err := emit(rows); err != nil {
			return err
		}
		if highest < from {
			return nil
		}
		from = highest + 1
	}
	return nil
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

// BackfillTradeGaps finds and (unless DryRun) fills trade-id gaps for every configured stream.
func BackfillTradeGaps(ctx context.Context, cfg GapFillConfig) (*GapFillReport, error) {
	ticksDir := filepath.Clean(filepath.Join(cfg.Dir, "ticks"))
	if cfg.OutDir == "" {
		cfg.OutDir = filepath.Join(cfg.Dir, "ticks_backfill")
	}
	out := filepath.Clean(cfg.OutDir)
	if out == ticksDir || strings.HasPrefix(out, ticksDir+string(filepath.Separator)) {
		return nil, errors.New("backfill output must be outside the live ticks/ tree")
	}
	if cfg.MaxGap <= 0 {
		cfg.MaxGap = 3_000_000
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 30 * time.Second}
	}
	progress := cfg.Progress
	if progress == nil {
		progress = func(string) {}
	}
	streams := cfg.Streams
	if len(streams) == 0 {
		entries, err := os.ReadDir(ticksDir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				streams = append(streams, e.Name())
			}
		}
	}
	rep := &GapFillReport{RunAt: time.Now().UTC(), From: cfg.From, To: cfg.To, DryRun: cfg.DryRun,
		Streams: map[string]*GapFillStream{}}
	getters := map[string]*restGetter{}
	for _, stream := range streams {
		st := &GapFillStream{}
		rep.Streams[stream] = st
		gaps, err := FindTradeGaps(ticksDir, out, stream, cfg.From, cfg.To)
		if err != nil {
			st.Error = err.Error()
			continue
		}
		rep.Gaps = append(rep.Gaps, gaps...)
		venue, symbol := venueOf(stream)
		for _, gp := range gaps {
			st.Gaps++
			st.Missing += gp.Missing()
		}
		progress(fmt.Sprintf("%s: %d gaps, %d missing trades", stream, st.Gaps, st.Missing))
		if cfg.DryRun {
			st.Remaining = st.Missing
			continue
		}
		g := getters[venue]
		if g == nil {
			g = &restGetter{client: cfg.Client, spacing: GapFillSpacing[venue]}
			getters[venue] = g
		}
		store, err := newDailyStore(filepath.Join(out, sanitize(stream)))
		if err != nil {
			return nil, err
		}
		src := venue + "-rest"
		for _, gp := range gaps {
			if gp.Missing() > cfg.MaxGap {
				st.Skipped += gp.Missing()
				progress(fmt.Sprintf("%s: skipping gap %d..%d (%d ids > max %d)", stream, gp.After, gp.Before, gp.Missing(), cfg.MaxGap))
				continue
			}
			seen := map[int64]bool{}
			pages := 0
			err := fetchGap(ctx, g, venue, symbol, gp, func(rows []TradeRow) error {
				sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
				for _, r := range rows {
					if seen[r.ID] || r.P <= 0 || r.S <= 0 {
						continue
					}
					seen[r.ID] = true
					if err := store.Add(time.UnixMilli(r.T), BackfillTradeRow{TradeRow: r, Backfill: true, Src: src}); err != nil {
						return err
					}
				}
				if pages++; pages%20 == 0 { // durable progress on long gaps; a rerun resumes from what is on disk
					return store.Flush()
				}
				return nil
			})
			if ferr := store.Flush(); err == nil {
				err = ferr
			}
			st.Filled += int64(len(seen))
			if err != nil {
				st.Error = err.Error()
				progress(fmt.Sprintf("%s: gap %d..%d failed: %v", stream, gp.After, gp.Before, err))
				if ctx.Err() != nil {
					break
				}
				continue
			}
			progress(fmt.Sprintf("%s: gap %d..%d filled %d of %d", stream, gp.After, gp.Before, len(seen), gp.Missing()))
		}
		st.Remaining = st.Missing - st.Filled
	}
	if !cfg.DryRun {
		if b, err := json.MarshalIndent(rep, "", "  "); err == nil {
			tmp := filepath.Join(out, "backfill_report.json.tmp")
			if os.MkdirAll(out, 0o755) == nil && os.WriteFile(tmp, b, 0o644) == nil {
				_ = os.Rename(tmp, filepath.Join(out, "backfill_report.json"))
			}
		}
	}
	return rep, ctx.Err()
}
