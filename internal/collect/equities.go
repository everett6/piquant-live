package collect

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// YahooChartBase is the public Yahoo Finance v8 daily chart base URL (overridable in tests).
var YahooChartBase = "https://query1.finance.yahoo.com/v8/finance/chart"

type yahooBar struct {
	Date   string
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume int64
}

// RefreshEquitiesCSVs fetches trailing daily OHLCV bars from Yahoo Finance for each ticker and
// atomically appends any newly completed UTC dates (strictly < todayUTC and > existing last date)
// into <dir>/<TICKER>.csv. Returns the total number of rows appended across all tickers.
func RefreshEquitiesCSVs(ctx context.Context, client *http.Client, dir string, tickers []string) (int, error) {
	if strings.TrimSpace(dir) == "" || len(tickers) == 0 {
		return 0, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	todayUTC := time.Now().UTC().Format("2006-01-02")

	clean := make([]string, 0, len(tickers))
	seen := make(map[string]struct{}, len(tickers))
	for _, t := range tickers {
		sym := strings.ToUpper(strings.TrimSpace(t))
		if sym == "" {
			continue
		}
		if _, dup := seen[sym]; dup {
			continue
		}
		seen[sym] = struct{}{}
		clean = append(clean, sym)
	}

	jobs := make(chan string, len(clean))
	for _, sym := range clean {
		jobs <- sym
	}
	close(jobs)

	workers := 4
	if len(clean) < workers {
		workers = len(clean)
	}
	if workers < 1 {
		workers = 1
	}

	var mu sync.Mutex
	var totalAppended int
	var firstErr error
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for sym := range jobs {
				if ctx.Err() != nil {
					return
				}
				n, err := refreshOneEquityCSV(ctx, client, dir, sym, todayUTC)
				mu.Lock()
				totalAppended += n
				if err != nil && firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return totalAppended, firstErr
}

func refreshOneEquityCSV(ctx context.Context, client *http.Client, dir, sym, todayUTC string) (int, error) {
	u := fmt.Sprintf("%s/%s?range=1mo&interval=1d", strings.TrimRight(YahooChartBase, "/"), url.PathEscape(sym))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", sym, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return 0, fmt.Errorf("%s read: %w", sym, err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s: HTTP %d", sym, resp.StatusCode)
	}

	var payload struct {
		Chart struct {
			Result []struct {
				Timestamp  []int64 `json:"timestamp"`
				Indicators struct {
					Quote []struct {
						Open   []*float64 `json:"open"`
						High   []*float64 `json:"high"`
						Low    []*float64 `json:"low"`
						Close  []*float64 `json:"close"`
						Volume []*float64 `json:"volume"`
					} `json:"quote"`
				} `json:"indicators"`
			} `json:"result"`
		} `json:"chart"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, fmt.Errorf("%s decode: %w", sym, err)
	}
	if len(payload.Chart.Result) == 0 || len(payload.Chart.Result[0].Indicators.Quote) == 0 {
		return 0, nil
	}
	res := payload.Chart.Result[0]
	q := res.Indicators.Quote[0]
	nTs := len(res.Timestamp)
	bars := make([]yahooBar, 0, nTs)
	for i := 0; i < nTs; i++ {
		if i >= len(q.Close) || q.Close[i] == nil {
			continue
		}
		cVal := *q.Close[i]
		if math.IsNaN(cVal) || math.IsInf(cVal, 0) || cVal <= 0 {
			continue
		}
		dateStr := time.Unix(res.Timestamp[i], 0).UTC().Format("2006-01-02")
		if dateStr >= todayUTC {
			continue // ignore incomplete intraday bar
		}
		valOr := func(arr []*float64, fallback float64) float64 {
			if i < len(arr) && arr[i] != nil && !math.IsNaN(*arr[i]) && !math.IsInf(*arr[i], 0) && *arr[i] > 0 {
				return *arr[i]
			}
			return fallback
		}
		var vol int64
		if i < len(q.Volume) && q.Volume[i] != nil && !math.IsNaN(*q.Volume[i]) && *q.Volume[i] > 0 {
			vol = int64(*q.Volume[i])
		}
		bars = append(bars, yahooBar{
			Date:   dateStr,
			Open:   valOr(q.Open, cVal),
			High:   valOr(q.High, cVal),
			Low:    valOr(q.Low, cVal),
			Close:  cVal,
			Volume: vol,
		})
	}
	if len(bars) == 0 {
		return 0, nil
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].Date < bars[j].Date })
	dedup := make([]yahooBar, 0, len(bars))
	for _, b := range bars {
		if len(dedup) > 0 && dedup[len(dedup)-1].Date == b.Date {
			dedup[len(dedup)-1] = b
		} else {
			dedup = append(dedup, b)
		}
	}

	csvPath := filepath.Join(dir, sym+".csv")
	existingBytes, err := os.ReadFile(csvPath)
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}

	var lastDate string
	if len(existingBytes) > 0 {
		sc := bufio.NewScanner(strings.NewReader(string(existingBytes)))
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(strings.ToLower(line), "date,") {
				continue
			}
			if len(line) >= 10 {
				d := line[:10]
				if d > lastDate {
					lastDate = d
				}
			}
		}
	}

	var toAppend []yahooBar
	for _, b := range dedup {
		if b.Date > lastDate {
			toAppend = append(toAppend, b)
		}
	}
	if len(toAppend) == 0 {
		return 0, nil
	}

	var out strings.Builder
	if len(existingBytes) > 0 {
		out.Write(existingBytes)
		if existingBytes[len(existingBytes)-1] != '\n' {
			out.WriteByte('\n')
		}
	} else {
		out.WriteString("date,open,high,low,close,volume\n")
	}
	for _, b := range toAppend {
		out.WriteString(b.Date)
		out.WriteString(" 00:00:00+00:00,")
		out.WriteString(strconv.FormatFloat(b.Open, 'g', -1, 64))
		out.WriteByte(',')
		out.WriteString(strconv.FormatFloat(b.High, 'g', -1, 64))
		out.WriteByte(',')
		out.WriteString(strconv.FormatFloat(b.Low, 'g', -1, 64))
		out.WriteByte(',')
		out.WriteString(strconv.FormatFloat(b.Close, 'g', -1, 64))
		out.WriteByte(',')
		out.WriteString(strconv.FormatInt(b.Volume, 10))
		out.WriteByte('\n')
	}

	tmpPath := csvPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(out.String()), 0o644); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpPath, csvPath); err != nil {
		_ = os.Remove(tmpPath)
		return 0, err
	}
	return len(toAppend), nil
}
