package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"piquant-live/internal/alert"
	"piquant-live/internal/collect"
	"piquant-live/internal/execution"
	"piquant-live/internal/trend"
)

// trendMu serialises every load-change-save of trend_state.json inside the engine process: the hourly ledger update
// (runTrendCore) and the minute-by-minute emergency check (runEmergencyWatch).
var trendMu sync.Mutex

// runEmergencyWatch checks every ledger once a minute against live Coinbase spot prices and flattens any ledger at
// its emergency drawdown (trend.EmergencyCheck). A failed price fetch is logged and retried at the next minute; no
// ledger is touched without both prices.
func runEmergencyWatch(ctx context.Context, dir string, al *alert.Alerter) {
	client := execution.NewOptimizedHTTPClient(10 * time.Second)
	path := filepath.Join(dir, "trend_state.json")
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		var (
			pxB, pxE, pxS    float64
			errB, errE, errS error
			wg               sync.WaitGroup
		)
		wg.Add(3)
		go func() { defer wg.Done(); pxB, errB = spotPrice(ctx, client, "BTC-USD") }()
		go func() { defer wg.Done(); pxE, errE = spotPrice(ctx, client, "ETH-USD") }()
		go func() { defer wg.Done(); pxS, errS = spotPrice(ctx, client, "SOL-USD") }()
		wg.Wait()
		if errB != nil || errE != nil {
			fmt.Fprintln(os.Stderr, "[GUARD] price fetch:", errB, errE)
			continue
		}
		if errS != nil {
			pxS = 0
		}
		if fired := emergencyPassAll(path, pxB, pxE, pxS, time.Now()); len(fired) > 0 {
			msg := strings.Join(fired, "\n")
			fmt.Println("[GUARD] emergency stop\n" + msg)
			if al != nil {
				al.Send("trend-emergency", "PiQuant: emergency stop on a paper ledger", msg)
			}
		}
	}
}

// emergencyPass loads the state, runs the check and saves only when a ledger was stopped.
func emergencyPass(path string, pxB, pxE float64, now time.Time) []string {
	return emergencyPassAll(path, pxB, pxE, 0, now)
}

func emergencyPassAll(path string, pxB, pxE, pxS float64, now time.Time) []string {
	trendMu.Lock()
	defer trendMu.Unlock()
	st, err := trend.Load(path)
	if err != nil || st == nil {
		return nil
	}
	fired := st.EmergencyCheckAll(pxB, pxE, pxS, now)
	if len(fired) > 0 {
		if err := st.Save(path); err != nil {
			fmt.Fprintln(os.Stderr, "[GUARD] save:", err)
		}
	}
	return fired
}

// spotPrice reads the last trade price from Coinbase Exchange's public ticker.
func spotPrice(ctx context.Context, client *http.Client, product string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, collect.CoinbaseCandlesBase+"/products/"+product+"/ticker", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "piquant")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s ticker: HTTP %d", product, resp.StatusCode)
	}
	var t struct {
		Price string `json:"price"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return 0, err
	}
	p, err := strconv.ParseFloat(t.Price, 64)
	if err != nil || p <= 0 {
		return 0, fmt.Errorf("%s ticker: bad price %q", product, t.Price)
	}
	return p, nil
}
