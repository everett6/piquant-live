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
	"piquant-live/internal/paper"
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
	reset := fs.String("reset", "", "restart one variant flat at equity 1 (e.g. after its kill switch) and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *reset != "" {
		path := filepath.Join(*dir, "trend_state.json")
		st, err := trend.Load(path)
		if err != nil || st == nil {
			return fmt.Errorf("no state in %s: %v", *dir, err)
		}
		if err := st.Reset(*reset); err != nil {
			return err
		}
		fmt.Printf("[TREND] %s reset to flat, equity 1\n", *reset)
		return st.Save(path)
	}
	if *report {
		st, err := trend.Load(filepath.Join(*dir, "trend_state.json"))
		if err != nil {
			return err
		}
		if st == nil {
			return fmt.Errorf("no state in %s", *dir)
		}
		return printTrend(st)
	}
	path := filepath.Join(*dir, "trend_state.json")
	st, err := trend.Load(path)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := &http.Client{Timeout: 20 * time.Second}
	for {
		st, err = advanceTrend(ctx, client, st, path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "[TREND]", err)
		} else if perr := printTrend(st); perr != nil {
			fmt.Fprintln(os.Stderr, "[TREND]", perr)
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
	rows, err := collect.FetchCoinbaseDailyCandles(ctx, client, "BTC-USD", now.AddDate(0, 0, -299), now)
	if err != nil {
		return st, fmt.Errorf("fetch daily candles: %w", err)
	}
	pre, post := splitDays(rows, now)
	if st == nil {
		st = trend.NewState(prereg6Date)
	}
	st.Seed(pre) // idempotent; also picks up the registration day once its candle completes
	n := st.Advance(post)
	// PREREG11 pair variant: ETH sleeve, stepped after the BTC day it shares.
	ethRows, ethErr := collect.FetchCoinbaseDailyCandles(ctx, client, "ETH-USD", now.AddDate(0, 0, -299), now)
	if ethErr == nil {
		epre, epost := splitDays(ethRows, now)
		st.SeedETH(epre)
		st.AdvanceETH(epost)
		// PREREG16 D2 needs SMA200: extend both histories backwards (no ledger is touched).
		st.Backfill(append(pre, post...), append(epre, epost...))
	}
	// PREREG30 E4: four clocks on 6-hour bars. A failed fetch is retried on the next pass; bars are never skipped.
	g0 := now.Add(-299 * 6 * time.Hour)
	gb, gbErr := collect.FetchCoinbase6hCandles(ctx, client, "BTC-USD", g0, now)
	ge, geErr := collect.FetchCoinbase6hCandles(ctx, client, "ETH-USD", g0, now)
	if gbErr == nil && geErr == nil {
		if k := st.AdvanceGrid(gridBars(gb, now), gridBars(ge, now)); k > 0 {
			fmt.Printf("[TREND] four-clock ledger advanced %d six-hour bar(s)\n", k)
		}
	} else {
		fmt.Fprintln(os.Stderr, "[TREND] fetch 6h candles:", gbErr, geErr)
	}
	if err := st.Save(path); err != nil {
		return st, err
	}
	fmt.Printf("[TREND] %s advanced %d day(s), last %s (eth last %s)\n", now.Format(time.RFC3339), n, st.LastDate, st.ETHLast)
	if ethErr != nil {
		return st, fmt.Errorf("fetch ETH daily candles: %w", ethErr)
	}
	return st, nil
}

// gridBars keeps completed 6-hour candles.
func gridBars(rows []collect.CandleRow, now time.Time) []trend.Bar {
	var out []trend.Bar
	for _, r := range rows {
		if !time.Unix(r.T, 0).Add(6 * time.Hour).After(now) {
			out = append(out, trend.Bar{T: r.T, Close: r.Close})
		}
	}
	return out
}

// splitDays keeps completed daily candles and splits them at the registration date (seed vs forward days).
func splitDays(rows []collect.CandleRow, now time.Time) (pre, post []trend.Day) {
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
	return pre, post
}

// runTrendCore keeps the 2x/1x trend ledger current and pushes the daily regime into the engine's trend gate,
// hourly, inside the dryrun process (no extra service). Errors are logged and retried.
func runTrendCore(ctx context.Context, dir string, gate *paper.TrendGate) {
	client := &http.Client{Timeout: 20 * time.Second}
	path := filepath.Join(dir, "trend_state.json")
	st, err := trend.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[TREND] load:", err)
	}
	for {
		if fresh, lerr := trend.Load(path); lerr == nil && fresh != nil {
			st = fresh // pick up manual edits such as `piquant trend --reset`
		}
		st, err = advanceTrend(ctx, client, st, path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "[TREND]", err)
		} else if up, c, m, ok := st.Regime(50); ok && gate != nil {
			gate.Set(up, c, m, time.Now())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

func printTrend(st *trend.State) error {
	b, err := json.MarshalIndent(st.Score(time.Now()), "", " ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
