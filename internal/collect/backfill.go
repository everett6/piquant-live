package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CandleRow is one 1-minute OHLCV candle.
type CandleRow struct {
	T      int64   `json:"t"` // unix seconds, candle open
	Open   float64 `json:"o"`
	High   float64 `json:"h"`
	Low    float64 `json:"l"`
	Close  float64 `json:"c"`
	Volume float64 `json:"v"`
}

// CoinbaseCandlesBase is the public Coinbase Exchange REST base URL (no key needed).
var CoinbaseCandlesBase = "https://api.exchange.coinbase.com"

// BackfillCoinbaseCandles fetches 1-minute candles for product (e.g. "BTC-USD") for each
// UTC day in [from, to) that has no file yet, newest day first, writing
// <dir>/candles/<product>/<day>.jsonl.gz. progress receives human-readable status.
func BackfillCoinbaseCandles(ctx context.Context, client *http.Client, dir, product string, from, to time.Time, progress func(string)) error {
	out := filepath.Join(dir, "candles", sanitize(product))
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	days := []time.Time{}
	for d := truncDay(to).Add(-24 * time.Hour); !d.Before(truncDay(from)); d = d.Add(-24 * time.Hour) {
		if !dayFileExists(out, d) {
			days = append(days, d)
		}
	}
	for i, day := range days {
		var rows []CandleRow
		for start := day; start.Before(day.Add(24 * time.Hour)); start = start.Add(300 * time.Minute) {
			end := start.Add(300 * time.Minute)
			if end.After(day.Add(24 * time.Hour)) {
				end = day.Add(24 * time.Hour)
			}
			batch, err := fetchCoinbaseCandles(ctx, client, product, start, end.Add(-time.Minute))
			if err != nil {
				return fmt.Errorf("%s %s: %w", product, start.Format(time.RFC3339), err)
			}
			rows = append(rows, batch...)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond): // stay well under public rate limits
			}
		}
		sort.Slice(rows, func(a, b int) bool { return rows[a].T < rows[b].T })
		store, _ := newDailyStore(out)
		for _, r := range rows {
			_ = store.Add(time.Unix(r.T, 0), r)
		}
		if len(rows) == 0 { // mark empty days so they are not refetched
			_ = appendGzip(filepath.Join(out, day.Format("2006-01-02")+".jsonl.gz"), nil)
		}
		if err := store.Flush(); err != nil {
			return err
		}
		if progress != nil {
			progress(fmt.Sprintf("%d/%d days (last %s, %d candles)", i+1, len(days), day.Format("2006-01-02"), len(rows)))
		}
	}
	if progress != nil {
		progress(fmt.Sprintf("complete (%d new days)", len(days)))
	}
	return nil
}

func truncDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// FetchCoinbaseDailyCandles returns up to 300 UTC daily candles for product in [start, end).
func FetchCoinbaseDailyCandles(ctx context.Context, client *http.Client, product string, start, end time.Time) ([]CandleRow, error) {
	return fetchCoinbaseCandlesGran(ctx, client, product, start, end, 86400)
}

// FetchCoinbase6hCandles returns 6-hour candles (at most 300 per request).
func FetchCoinbase6hCandles(ctx context.Context, client *http.Client, product string, start, end time.Time) ([]CandleRow, error) {
	return fetchCoinbaseCandlesGran(ctx, client, product, start, end, 21600)
}

func fetchCoinbaseCandles(ctx context.Context, client *http.Client, product string, start, end time.Time) ([]CandleRow, error) {
	return fetchCoinbaseCandlesGran(ctx, client, product, start, end, 60)
}

func fetchCoinbaseCandlesGran(ctx context.Context, client *http.Client, product string, start, end time.Time, gran int) ([]CandleRow, error) {
	u := fmt.Sprintf("%s/products/%s/candles?granularity=%d&start=%s&end=%s", CoinbaseCandlesBase,
		url.PathEscape(product), gran, start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339))
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		body, code, err := httpGet(ctx, client, u, 1<<20)
		if err == nil && code == http.StatusOK {
			var raw [][]float64 // [time, low, high, open, close, volume]
			if err := json.Unmarshal(body, &raw); err != nil {
				return nil, err
			}
			rows := make([]CandleRow, 0, len(raw))
			for _, r := range raw {
				if len(r) < 6 {
					continue
				}
				rows = append(rows, CandleRow{T: int64(r[0]), Low: r[1], High: r[2], Open: r[3], Close: r[4], Volume: r[5]})
			}
			return rows, nil
		}
		lastErr = fmt.Errorf("HTTP %d: %v", code, err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(2<<attempt) * time.Second):
		}
	}
	return nil, lastErr
}

// GDELTRow is one article headline from the GDELT DOC 2.0 API.
type GDELTRow struct {
	SeenAt  time.Time `json:"seen_at"`
	Query   string    `json:"query"`
	Title   string    `json:"title"`
	URL     string    `json:"url"`
	Domain  string    `json:"domain,omitempty"`
	Country string    `json:"country,omitempty"`
	Lang    string    `json:"lang,omitempty"`
}

// GDELTBase is the GDELT DOC 2.0 API endpoint.
var GDELTBase = "https://api.gdeltproject.org/api/v2/doc/doc"

// GDELTRetryBase is the backoff unit after a throttle notice or HTTP error.
var GDELTRetryBase = 15 * time.Second

// GDELTSpacing is the minimum delay between GDELT requests (their limit is 1 per 5s).
var GDELTSpacing = 6 * time.Second

// BackfillGDELT fetches English headlines matching query in fixed windows over
// [from, to), resuming from <dir>/gdelt/<label>/progress.json.
func BackfillGDELT(ctx context.Context, client *http.Client, dir, label, query string, from, to time.Time, window time.Duration, progress func(string)) error {
	out := filepath.Join(dir, "gdelt", sanitize(label))
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	progPath := filepath.Join(out, "progress.json")
	done := map[string]bool{}
	if b, err := os.ReadFile(progPath); err == nil {
		_ = json.Unmarshal(b, &done)
	}
	store, _ := newDailyStore(out)
	var windows []time.Time
	for w := to.Add(-window).UTC().Truncate(window); !w.Before(from); w = w.Add(-window) {
		if !done[w.Format(time.RFC3339)] {
			windows = append(windows, w)
		}
	}
	total := 0
	for i, w := range windows {
		rows, err := fetchGDELTWindow(ctx, client, query, w, w.Add(window))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if progress != nil {
				progress(fmt.Sprintf("window %s failed, will retry next run: %v", w.Format(time.RFC3339), err))
			}
		} else {
			for _, r := range rows {
				r.Query = label
				_ = store.Add(r.SeenAt, r)
			}
			total += len(rows)
			if err := store.Flush(); err != nil {
				return err
			}
			done[w.Format(time.RFC3339)] = true
			if b, err := json.Marshal(done); err == nil {
				// Atomic replace: a crash mid-write must not corrupt the resume state
				// (a bad progress.json would re-fetch and duplicate every window).
				tmp := progPath + ".tmp"
				if os.WriteFile(tmp, b, 0o644) == nil {
					_ = os.Rename(tmp, progPath)
				}
			}
		}
		if progress != nil && (i%10 == 0 || i == len(windows)-1) {
			progress(fmt.Sprintf("%d/%d windows, %d headlines this run", i+1, len(windows), total))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(GDELTSpacing):
		}
	}
	if progress != nil {
		progress(fmt.Sprintf("complete (%d headlines this run)", total))
	}
	return nil
}

func fetchGDELTWindow(ctx context.Context, client *http.Client, query string, start, end time.Time) ([]GDELTRow, error) {
	q := url.Values{}
	q.Set("query", query+" sourcelang:english")
	q.Set("mode", "artlist")
	q.Set("format", "json")
	q.Set("maxrecords", "250")
	q.Set("sort", "datedesc")
	q.Set("startdatetime", start.UTC().Format("20060102150405"))
	q.Set("enddatetime", end.UTC().Format("20060102150405"))
	u := GDELTBase + "?" + q.Encode()
	for attempt := 0; attempt < 4; attempt++ {
		body, code, err := httpGet(ctx, client, u, 8<<20)
		trimmed := strings.TrimSpace(string(body))
		if err == nil && code == http.StatusOK && strings.HasPrefix(trimmed, "{") {
			var resp struct {
				Articles []struct {
					URL      string `json:"url"`
					Title    string `json:"title"`
					SeenDate string `json:"seendate"`
					Domain   string `json:"domain"`
					Language string `json:"language"`
					Country  string `json:"sourcecountry"`
				} `json:"articles"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return nil, err
			}
			rows := make([]GDELTRow, 0, len(resp.Articles))
			for _, a := range resp.Articles {
				t, err := time.Parse("20060102T150405Z", a.SeenDate)
				if err != nil || strings.TrimSpace(a.Title) == "" {
					continue
				}
				rows = append(rows, GDELTRow{SeenAt: t, Title: cleanText(a.Title, 400), URL: a.URL, Domain: a.Domain, Country: a.Country, Lang: a.Language})
			}
			return rows, nil
		}
		if err == nil && code == http.StatusOK && trimmed == "" {
			return nil, nil // no articles in window
		}
		// Throttle notice (plain text) or HTTP error: back off and retry
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(GDELTRetryBase * time.Duration(attempt+1)):
		}
	}
	return nil, fmt.Errorf("GDELT unavailable for %s", start.Format(time.RFC3339))
}
