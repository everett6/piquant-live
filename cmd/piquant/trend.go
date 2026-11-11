package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"piquant-live/internal/collect"
	"piquant-live/internal/trend"
)

// prereg6Date is the PREREG6 registration date: days up to and including it only seed the SMA.
const prereg6Date = "2026-11-11"

// runTrend runs the PREREG6 forward paper test of the SMA50 trend filter (T1 spot 1x, T2 futures 2x, BH).
// It is a standalone ledger: no orders, no effect on the paper engine.
func runTrend(args []string) error {
	fs := flag.NewFlagSet("trend", flag.ContinueOnError)
	home, _ := os.UserHomeDir()
	dir := fs.String("dir", filepath.Join(home, "piquant-trend"), "state directory")
	once := fs.Bool("once", false, "advance once and exit")
	report := fs.Bool("report", false, "print the scorecard and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := filepath.Join(*dir, "trend_state.json")
	st, err := trend.Load(path)
	if err != nil {
		return err
	}
	if *report {
		if st == nil {
			return fmt.Errorf("no state at %s", path)
		}
		return printTrend(st)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := &http.Client{Timeout: 20 * time.Second}
	for {
		st, err = advanceTrend(ctx, client, st, path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "[TREND]", err)
		}
		if *once {
			return err
		}
		next := time.Now().UTC().Truncate(24 * time.Hour).Add(24*time.Hour + 5*time.Minute)
		if err != nil {
			next = time.Now().Add(10 * time.Minute)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Until(next)):
		}
	}
}

func advanceTrend(ctx context.Context, client *http.Client, st *trend.State, path string) (*trend.State, error) {
	now := time.Now().UTC()
	rows, err := collect.FetchCoinbaseDailyCandles(ctx, client, "BTC-USD", now.AddDate(0, 0, -150), now)
	if err != nil {
		return st, fmt.Errorf("fetch daily candles: %w", err)
	}
	var pre, post []trend.Day
	for _, r := range rows {
		if time.Unix(r.T, 0).Add(24 * time.Hour).After(now) {
			continue // today's candle is not complete
		}
		d := trend.Day{Date: time.Unix(r.T, 0).UTC().Format("2006-01-02"), Close: r.Close}
		if d.Date <= prereg6Date {
			pre = append(pre, d)
		} else {
			post = append(post, d)
		}
	}
	if st == nil {
		st = trend.NewState(prereg6Date)
	}
	st.Seed(pre) // idempotent; also picks up the registration day once its candle completes
	n := st.Advance(post)
	if err := st.Save(path); err != nil {
		return st, err
	}
	fmt.Printf("[TREND] %s advanced %d day(s), last %s\n", now.Format(time.RFC3339), n, st.LastDate)
	return st, printTrend(st)
}

func printTrend(st *trend.State) error {
	b, err := json.MarshalIndent(st.Score(time.Now()), "", " ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
