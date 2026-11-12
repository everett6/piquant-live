package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"piquant-live/internal/alert"
	"piquant-live/internal/carry"
	"piquant-live/internal/collect"
)

// carryRegistered is the close the forward ledger opens at (PREREG19 result, 2026-11-11).
const carryRegistered = "2026-11-11"

func carryGet(ctx context.Context, client *http.Client, u string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return b, resp.StatusCode, err
}

// dailyCandles maps YYYY-MM-DD to the Coinbase daily candle for [start, end).
func dailyCandles(ctx context.Context, client *http.Client, product string, start, end time.Time) (map[string]collect.CandleRow, error) {
	rows, err := collect.FetchCoinbaseDailyCandles(ctx, client, product, start, end)
	if err != nil {
		return nil, err
	}
	out := map[string]collect.CandleRow{}
	for _, r := range rows {
		out[time.Unix(r.T, 0).UTC().Format("2006-01-02")] = r
	}
	return out, nil
}

// advanceCarry opens the ledger if needed, then applies every completed month whose funding files are published.
// It returns the state and the number of days added.
func advanceCarry(ctx context.Context, client *http.Client, path string, now time.Time) (*carry.State, int, error) {
	today := now.UTC().Format("2006-01-02")
	st, err := carry.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		if today <= carryRegistered {
			return nil, 0, nil
		}
		day, _ := time.Parse("2006-01-02", carryRegistered)
		closes := map[string]float64{}
		for _, c := range carry.Coins {
			m, err := dailyCandles(ctx, client, c.Product, day, day.AddDate(0, 0, 1))
			if err != nil {
				return nil, 0, err
			}
			r, ok := m[carryRegistered]
			if !ok {
				return nil, 0, fmt.Errorf("no %s close for %s", c.Product, carryRegistered)
			}
			closes[c.Name] = r.Close
		}
		st = carry.New(carryRegistered, closes)
		if err := st.Save(path); err != nil {
			return nil, 0, err
		}
	} else if err != nil {
		return nil, 0, err
	}
	added := 0
	for {
		last, _ := time.Parse("2006-01-02", st.LastDate)
		next := last.AddDate(0, 0, 1)
		monthStart := time.Date(next.Year(), next.Month(), 1, 0, 0, 0, 0, time.UTC)
		monthEnd := monthStart.AddDate(0, 1, 0)
		if monthEnd.Format("2006-01-02") > today {
			return st, added, nil // month not complete yet
		}
		month := monthStart.Format("2006-01")
		fund := map[string]map[string][2]float64{}
		cand := map[string]map[string]collect.CandleRow{}
		for _, c := range carry.Coins {
			b, code, err := carryGet(ctx, client, carry.FundingURL(c.Symbol, month))
			if err != nil {
				return st, added, err
			}
			if code == http.StatusNotFound {
				return st, added, nil // not published yet
			}
			if code != http.StatusOK {
				return st, added, fmt.Errorf("funding %s %s: HTTP %d", c.Symbol, month, code)
			}
			rates, err := carry.ParseFundingZip(b)
			if err != nil {
				return st, added, fmt.Errorf("funding %s %s: %w", c.Symbol, month, err)
			}
			fund[c.Name] = carry.DailyFunding(rates)
			if cand[c.Name], err = dailyCandles(ctx, client, c.Product, next, monthEnd); err != nil {
				return st, added, err
			}
		}
		for d := next; d.Before(monthEnd); d = d.AddDate(0, 0, 1) {
			ds := d.Format("2006-01-02")
			in := map[string]carry.DayInput{}
			for _, c := range carry.Coins {
				f, ok := fund[c.Name][ds]
				r, ok2 := cand[c.Name][ds]
				if !ok || !ok2 {
					return st, added, fmt.Errorf("carry: %s data missing for %s", c.Name, ds)
				}
				in[c.Name] = carry.DayInput{Close: r.Close, High: r.High, FundPos: f[0], FundNeg: f[1]}
			}
			if err := st.Step(ds, in); err != nil {
				return st, added, err
			}
			added++
		}
		if err := st.Save(path); err != nil {
			return st, added, err
		}
	}
}

// runCarryLoop advances the ledger every 6 hours inside the collector and emails each new month.
func runCarryLoop(ctx context.Context, dir string, al *alert.Alerter) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Printf("[CARRY] %v\n", err)
		return
	}
	path := filepath.Join(dir, "carry_state.json")
	client := &http.Client{Timeout: 60 * time.Second}
	for {
		st, n, err := advanceCarry(ctx, client, path, time.Now())
		if err != nil {
			fmt.Printf("[CARRY] %v\n", err)
		}
		if st != nil && n > 0 {
			fmt.Printf("[CARRY] +%d days: %s\n", n, st.Summary())
			al.Send("carry-"+st.LastDate, "Carry ledger month closed", st.Summary())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(6 * time.Hour):
		}
	}
}

// runCarry: piquant carry [--dir d] [--once]. Prints the ledger, advancing it first with --once.
func runCarry(args []string) error {
	fs := flag.NewFlagSet("carry", flag.ContinueOnError)
	home, _ := os.UserHomeDir()
	dir := fs.String("dir", filepath.Join(home, "piquant-collect", "carry"), "carry ledger directory")
	once := fs.Bool("once", false, "advance with any newly published months, then print")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := filepath.Join(*dir, "carry_state.json")
	if *once {
		if err := os.MkdirAll(*dir, 0o755); err != nil {
			return err
		}
		if _, _, err := advanceCarry(context.Background(), &http.Client{Timeout: 60 * time.Second}, path,
			time.Now()); err != nil {
			return err
		}
	}
	st, err := carry.Load(path)
	if err != nil {
		return err
	}
	fmt.Println(st.Summary())
	return nil
}
