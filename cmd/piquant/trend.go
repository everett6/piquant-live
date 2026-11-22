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
	"sync"
	"syscall"
	"time"

	"piquant-live/internal/carry"
	"piquant-live/internal/collect"
	"piquant-live/internal/execution"
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
	dir := fs.String("dir", defaultTrendDir(), "state directory")
	carryDir := fs.String("carry-dir", filepath.Join(home, "piquant-collect", "carry"), "carry state directory for S36 blend")
	dtb3Path := fs.String("dtb3", filepath.Join(home, "piquant-data", "macro", "DTB3.csv"), "FRED DTB3 CSV path")
	once := fs.Bool("once", false, "advance once and exit")
	report := fs.Bool("report", false, "print the scorecard and exit")
	blend := fs.Bool("blend", false, "include S36_qd_carry_blend composite scorecard + ping execution plan")
	pingSample := fs.Bool("ping", false, "measure live exchange ping for S36 execution guidance (only with --blend)")
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
		if *blend {
			return printTrendWithBlend(context.Background(), st, *carryDir, *dtb3Path, *pingSample)
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
	client := execution.NewOptimizedHTTPClient(20 * time.Second)
	for {
		st, err = advanceTrend(ctx, client, st, path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "[TREND]", err)
		} else if *blend {
			if perr := printTrendWithBlend(ctx, st, *carryDir, *dtb3Path, *pingSample); perr != nil {
				fmt.Fprintln(os.Stderr, "[TREND]", perr)
			}
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
	dailyStart := now.AddDate(0, 0, -299)
	g0 := now.Add(-299 * 6 * time.Hour)

	var (
		rows, ethRows, solRows, gb, ge []collect.CandleRow
		err, ethErr, solErr, gbErr, geErr error
		wg                             sync.WaitGroup
	)
	wg.Add(5)
	go func() {
		defer wg.Done()
		rows, err = collect.FetchCoinbaseDailyCandles(ctx, client, "BTC-USD", dailyStart, now)
	}()
	go func() {
		defer wg.Done()
		ethRows, ethErr = collect.FetchCoinbaseDailyCandles(ctx, client, "ETH-USD", dailyStart, now)
	}()
	go func() {
		defer wg.Done()
		solRows, solErr = collect.FetchCoinbaseDailyCandles(ctx, client, "SOL-USD", dailyStart, now)
	}()
	go func() {
		defer wg.Done()
		gb, gbErr = collect.FetchCoinbase6hCandles(ctx, client, "BTC-USD", g0, now)
	}()
	go func() {
		defer wg.Done()
		ge, geErr = collect.FetchCoinbase6hCandles(ctx, client, "ETH-USD", g0, now)
	}()
	wg.Wait()

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
	if ethErr == nil {
		epre, epost := splitDays(ethRows, now)
		st.SeedETH(epre)
		st.AdvanceETH(epost)
		// PREREG16 D2 needs SMA200: extend both histories backwards (no ledger is touched).
		st.Backfill(append(pre, post...), append(epre, epost...))
	}
	// PREREG51/56/57 tri-asset sleeve: SOL daily candles fetched and stepped after BTC and ETH.
	if solErr == nil {
		spre, spost := splitDays(solRows, now)
		st.SeedSOL(spre)
		st.AdvanceSOL(spost)
		st.BackfillSOL(append(spre, spost...))
	} else {
		fmt.Fprintln(os.Stderr, "[TREND] fetch SOL daily candles:", solErr)
	}
	// PREREG30 E4: four clocks on 6-hour bars. A failed fetch is retried on the next pass; bars are never skipped.
	if gbErr == nil && geErr == nil {
		if k := st.AdvanceGrid(gridBars(gb, now), gridBars(ge, now)); k > 0 {
			fmt.Printf("[TREND] four-clock ledger advanced %d six-hour bar(s)\n", k)
		}
	} else {
		fmt.Fprintln(os.Stderr, "[TREND] fetch 6h candles:", gbErr, geErr)
	}
	// Non-fatal daily equities/macro refresh when ~/piquant-data/equities is present.
	if eqDir := trend.DefaultEquitiesDir(); eqDir != "" {
		if info, serr := os.Stat(eqDir); serr == nil && info.IsDir() {
			rctx, rcancel := context.WithTimeout(ctx, 15*time.Second)
			if added, rerr := collect.RefreshEquitiesCSVs(rctx, client, eqDir, trend.MacroEquityTickers); rerr != nil {
				fmt.Fprintln(os.Stderr, "[TREND] refresh equities:", rerr)
			} else if added > 0 {
				fmt.Printf("[TREND] refreshed %d daily macro/equities bar(s) in %s\n", added, eqDir)
			}
			rcancel()
		}
	}
	if err := st.Save(path); err != nil {
		return st, err
	}
	fmt.Printf("[TREND] %s advanced %d day(s), last %s (eth last %s, sol last %s)\n", now.Format(time.RFC3339), n, st.LastDate, st.ETHLast, st.SOLLast)
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
		d := trend.Day{
			Date:  time.Unix(r.T, 0).UTC().Format("2006-01-02"),
			Close: r.Close,
			Open:  r.Open,
			High:  r.High,
			Low:   r.Low,
		}
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
	client := execution.NewOptimizedHTTPClient(20 * time.Second)
	path := filepath.Join(dir, "trend_state.json")
	st, err := trend.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[TREND] load:", err)
	}
	for {
		trendMu.Lock() // the emergency watch writes the same file
		if fresh, lerr := trend.Load(path); lerr == nil && fresh != nil {
			st = fresh // pick up manual edits such as `piquant trend --reset`
		}
		st, err = advanceTrend(ctx, client, st, path)
		trendMu.Unlock()
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

func printTrendWithBlend(ctx context.Context, st *trend.State, carryDir, dtb3Path string, probePing bool) error {
	var cs *carry.State
	if loaded, err := carry.Load(filepath.Join(carryDir, "carry_state.json")); err == nil {
		cs = loaded
	}
	rates, _ := trend.LoadDTB3Rates(dtb3Path)
	var planPtr *execution.PingExecutionPlan
	if probePing {
		venues := execution.ProbeEndpoints(ctx, execution.DefaultEndpoints, 2, 3*time.Second)
		p := execution.PlanForLatency(venues, time.Now().UTC())
		planPtr = &p
	}
	var blends []trend.BlendSummary
	if bs, ok := trend.ComputeS36Blend(st, cs, rates, "S36_safe_qd_spot_1x", "S36_qd_carry_blend_spot", planPtr); ok {
		blends = append(blends, bs)
	}
	if bf, ok := trend.ComputeS36Blend(st, cs, rates, "S36_safe_qd_fut_1x", "S36_qd_carry_blend_fut", planPtr); ok {
		blends = append(blends, bf)
	}
	q42Blends := trend.ComputeQ42Blends(st, cs, rates, planPtr)
	q44Blends := trend.ComputeQ44Blends(st, cs, rates, planPtr)
	q51Blends := trend.ComputeQ51Blends(st, cs, rates, planPtr)
	q56Blends := trend.ComputeQ56Blends(st, cs, rates, planPtr)
	q57Blends := trend.ComputeQ57Blends(st, cs, rates, planPtr)
	payload := struct {
		Scorecard []trend.Summary      `json:"scorecard"`
		S36Blend  []trend.BlendSummary `json:"s36_qd_carry_blend,omitempty"`
		Q42Blends []trend.BlendSummary `json:"q42_expected_dd_blends,omitempty"`
		Q44Blends []trend.BlendSummary `json:"q44_vol_harvester_blends,omitempty"`
		Q51Blends []trend.BlendSummary `json:"q51_volparity_blends,omitempty"`
		Q56Blends []trend.BlendSummary `json:"q56_quantum_fast_blends,omitempty"`
		Q57Blends []trend.BlendSummary `json:"q57_hrp_hurst_blends,omitempty"`
	}{
		Scorecard: st.Score(time.Now()),
		S36Blend:  blends,
		Q42Blends: q42Blends,
		Q44Blends: q44Blends,
		Q51Blends: q51Blends,
		Q56Blends: q56Blends,
		Q57Blends: q57Blends,
	}
	b, err := json.MarshalIndent(payload, "", " ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
