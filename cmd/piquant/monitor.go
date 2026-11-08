package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"piquant-live/internal/paper"
)

// ANSI color and formatting codes
const (
	ansiReset      = "\033[0m"
	ansiBold       = "\033[1m"
	ansiDim        = "\033[2m"
	ansiUnderline  = "\033[4m"
	ansiClearScreen= "\033[H\033[2J"
	ansiHome       = "\033[H"
	ansiHideCursor = "\033[?25l"
	ansiShowCursor = "\033[?25h"
	ansiClearLine  = "\033[2K"

	// Foreground colors
	ansiBlack   = "\033[30m"
	ansiRed     = "\033[31m"
	ansiGreen   = "\033[32m"
	ansiYellow  = "\033[33m"
	ansiBlue    = "\033[34m"
	ansiMagenta = "\033[35m"
	ansiCyan    = "\033[36m"
	ansiWhite   = "\033[37m"
	ansiGray    = "\033[90m"

	// Bright foreground
	ansiBrightRed     = "\033[91m"
	ansiBrightGreen   = "\033[92m"
	ansiBrightYellow  = "\033[93m"
	ansiBrightBlue    = "\033[94m"
	ansiBrightMagenta = "\033[95m"
	ansiBrightCyan    = "\033[96m"
	ansiBrightWhite   = "\033[97m"

	// Background colors
	ansiBgDarkGray = "\033[100m"
	ansiBgBlue     = "\033[44m"
	ansiBgCyan     = "\033[46m"
)

type monitorFlags struct {
	dataDir     string
	stateFile   string
	journalFile string
	remoteURL   string
	token       string
	interval    time.Duration
	limit       int
	once        bool
}

func addMonitorFlags(fs *flag.FlagSet) *monitorFlags {
	f := &monitorFlags{}
	fs.StringVar(&f.dataDir, "data", "", "data directory where paper checkpoints live (default piquant-data)")
	fs.StringVar(&f.stateFile, "state", "", "explicit path to dryrun_state.json (overrides --data)")
	fs.StringVar(&f.journalFile, "journal", "", "explicit path to dryrun_journal.jsonl (overrides --data)")
	fs.StringVar(&f.remoteURL, "remote", "", "remote dashboard API URL, e.g. http://localhost:8080 or http://192.168.0.109:8080")
	fs.StringVar(&f.remoteURL, "addr", "", "alias for --remote")
	fs.StringVar(&f.token, "token", "", "bearer token for dashboard API authentication if required")
	fs.DurationVar(&f.interval, "interval", 500*time.Millisecond, "screen refresh interval (e.g. 250ms, 500ms, 1s)")
	fs.IntVar(&f.limit, "limit", 10, "number of recent executed trades to display in blotter")
	fs.BoolVar(&f.once, "once", false, "render single snapshot frame and exit immediately")
	return f
}

func runMonitor(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("top", flag.ContinueOnError)
	f := addMonitorFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	dDir := f.dataDir
	if dDir == "" {
		dDir = defaultDataDir()
	}

	statePath := f.stateFile
	if statePath == "" {
		statePath = filepath.Join(dDir, "paper", "dryrun_state.json")
	}

	journalPath := f.journalFile
	if journalPath == "" {
		journalPath = filepath.Join(dDir, "paper", "dryrun_journal.jsonl")
	}

	// Single snapshot mode
	if f.once {
		st, trades, err := fetchMonitorData(ctx, f.remoteURL, f.token, statePath, journalPath, f.limit)
		if err != nil {
			return err
		}
		termWidth := getTerminalWidth()
		var buf bytes.Buffer
		renderTerminalFrame(&buf, st, trades, termWidth, dDir, f.remoteURL)
		fmt.Print(buf.String())
		return nil
	}

	// Interactive live monitor loop
	restoreTTY := enableRawMode()
	defer restoreTTY()

	// Ensure cursor is restored on exit
	fmt.Print(ansiHideCursor)
	defer fmt.Print(ansiShowCursor + "\n")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	keyCh := make(chan byte, 16)
	go readKeypresses(keyCh)

	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()

	// Initial render
	fmt.Print(ansiClearScreen)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sigCh:
			return nil
		case k := <-keyCh:
			if k == 'q' || k == 'Q' || k == 3 || k == 27 { // 'q', 'Q', Ctrl+C (ETX), Esc
				return nil
			}
			if k == 'r' || k == 'R' {
				// Immediate force refresh
				fmt.Print(ansiClearScreen)
			}
		case <-ticker.C:
		}

		st, trades, err := fetchMonitorData(ctx, f.remoteURL, f.token, statePath, journalPath, f.limit)
		termWidth := getTerminalWidth()

		var buf bytes.Buffer
		buf.WriteString(ansiHome)
		if err != nil {
			renderWaitingFrame(&buf, err, termWidth, dDir, f.remoteURL)
		} else {
			renderTerminalFrame(&buf, st, trades, termWidth, dDir, f.remoteURL)
		}

		// Flush full frame in single atomic write to eliminate flicker
		_, _ = os.Stdout.Write(buf.Bytes())
	}
}

// fetchMonitorData pulls state & journal either over HTTP or from local disk files.
func fetchMonitorData(ctx context.Context, remoteURL, token, statePath, journalPath string, limit int) (*paper.DryRunState, []paper.TradeRecord, error) {
	if remoteURL != "" {
		return fetchRemoteData(ctx, remoteURL, token, limit)
	}
	return fetchLocalData(statePath, journalPath, limit)
}

func fetchLocalData(statePath, journalPath string, limit int) (*paper.DryRunState, []paper.TradeRecord, error) {
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("no active dry run checkpoint found at %s", statePath)
		}
		return nil, nil, fmt.Errorf("reading checkpoint: %w", err)
	}

	cleanData := bytes.Trim(data, "\x00\r\n \t")
	if len(cleanData) == 0 {
		return nil, nil, fmt.Errorf("state checkpoint is temporarily empty (syncing)")
	}

	var st paper.DryRunState
	if err := json.Unmarshal(cleanData, &st); err != nil {
		return nil, nil, fmt.Errorf("checkpoint unmarshal error: %w", err)
	}

	// Read tail of journal
	trades := readJournalTail(journalPath, limit)
	return &st, trades, nil
}

func fetchRemoteData(ctx context.Context, remoteURL, token string, limit int) (*paper.DryRunState, []paper.TradeRecord, error) {
	baseURL := strings.TrimRight(remoteURL, "/")
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}

	client := &http.Client{Timeout: 1500 * time.Millisecond}

	// 1. Fetch State
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/api/paper/dryrun", nil)
	if err != nil {
		return nil, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("remote API unreachable (%s): %w", baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("remote API error %s (status %d)", baseURL+"/api/paper/dryrun", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("reading remote state: %w", err)
	}

	var respWrapper struct {
		State       *paper.DryRunState  `json:"state"`
		RecentFills []paper.TradeRecord `json:"recent_fills"`
	}
	var st *paper.DryRunState
	var trades []paper.TradeRecord

	if err := json.Unmarshal(bodyBytes, &respWrapper); err == nil && respWrapper.State != nil && respWrapper.State.InitialCapital > 0 {
		st = respWrapper.State
		trades = respWrapper.RecentFills
	} else {
		var directSt paper.DryRunState
		if err := json.Unmarshal(bodyBytes, &directSt); err == nil && directSt.InitialCapital > 0 {
			st = &directSt
		} else {
			return nil, nil, fmt.Errorf("decoding remote state: unrecognized format")
		}
	}

	// Reverse trades so newest fills appear at the top of the blotter
	if len(trades) > 1 && trades[0].Timestamp.Before(trades[len(trades)-1].Timestamp) {
		for i, j := 0, len(trades)-1; i < j; i, j = i+1, j-1 {
			trades[i], trades[j] = trades[j], trades[i]
		}
	}

	return st, trades, nil
}

// readJournalTail reads the last N non-empty records from dryrun_journal.jsonl.
func readJournalTail(journalPath string, limit int) []paper.TradeRecord {
	if limit <= 0 {
		limit = 10
	}
	f, err := os.Open(journalPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var ring []paper.TradeRecord
	for scanner.Scan() {
		line := bytes.Trim(scanner.Bytes(), "\x00\r\n \t")
		if len(line) == 0 {
			continue
		}
		var rec paper.TradeRecord
		if err := json.Unmarshal(line, &rec); err == nil && rec.Symbol != "" {
			ring = append(ring, rec)
			if len(ring) > limit*2 {
				ring = ring[len(ring)-limit:]
			}
		}
	}

	if len(ring) > limit {
		ring = ring[len(ring)-limit:]
	}

	// Reverse so newest fills appear at the top of the blotter
	for i, j := 0, len(ring)-1; i < j; i, j = i+1, j-1 {
		ring[i], ring[j] = ring[j], ring[i]
	}
	return ring
}

// renderTerminalFrame draws the complete institutional Bloomberg/HFT UI.
func renderTerminalFrame(w io.Writer, st *paper.DryRunState, trades []paper.TradeRecord, width int, dataDir, remoteURL string) {
	if width < 80 {
		width = 80
	}
	if width > 130 {
		width = 130
	}

	now := time.Now()
	boardInfo := detectHardwareModel()

	// Status badge
	statusText := ansiBrightGreen + ansiBold + "ACTIVE (OK)" + ansiReset
	if st.CircuitHalted {
		statusText = ansiBrightRed + ansiBold + "CIRCUIT HALTED" + ansiReset
	} else if st.QuotingPaused {
		statusText = ansiBrightYellow + ansiBold + "QUOTING PAUSED" + ansiReset
	}

	feedSource := "BINANCE LIVE (WS)"
	if st.DataSource != "" && st.DataSource != "live" {
		feedSource = strings.ToUpper(st.DataSource)
	}

	// 1. HEADER BOX
	border := strings.Repeat("═", width-2)
	fmt.Fprintf(w, "%s╔%s╗%s\n", ansiCyan, border, ansiReset)

	title := fmt.Sprintf(" PIQUANT HFT RESEARCH ENGINE  │  %s", boardInfo)
	fmt.Fprintf(w, "%s║%s %s %s║%s\n", ansiCyan, ansiReset, padRightVisible(ansiBold+ansiBrightWhite+title+ansiReset, width-4), ansiCyan, ansiReset)

	clockStr := fmt.Sprintf(" Feed: %-18s │ Status: %s │ UTC: %s", feedSource, statusText, now.UTC().Format("15:04:05"))
	fmt.Fprintf(w, "%s║%s %s %s║%s\n", ansiCyan, ansiReset, padRightVisible(clockStr, width-4), ansiCyan, ansiReset)
	fmt.Fprintf(w, "%s╚%s╝%s\n", ansiCyan, border, ansiReset)

	// 2. EVALUATION WINDOW & PROGRESS BAR
	targetDays := st.TargetDays
	if targetDays <= 0 {
		targetDays = 7
	}
	presetName := strings.ToUpper(st.WindowPreset)
	if presetName == "" {
		presetName = fmt.Sprintf("%dD", targetDays)
	}

	var pctElapsed float64
	rem := st.TargetEndTime.Sub(now)
	if rem < 0 {
		rem = 0
	}
	totDuration := st.TargetEndTime.Sub(st.StartTime)
	if totDuration > 0 {
		elapsed := now.Sub(st.StartTime)
		pctElapsed = math.Max(0.0, math.Min(100.0, float64(elapsed)/float64(totDuration)*100.0))
	}

	barWidth := width - 42
	if barWidth < 20 {
		barWidth = 20
	}
	filledChars := int(float64(barWidth) * (pctElapsed / 100.0))
	if filledChars > barWidth {
		filledChars = barWidth
	}
	bar := strings.Repeat("█", filledChars) + strings.Repeat("░", barWidth-filledChars)

	fmt.Fprintf(w, "%s[%s]%s Horizon: %s%s (%d Days)%s  [%s%s%s] %s%5.1f%%%s  (Day %d of %d)\n",
		ansiCyan, presetName, ansiReset,
		ansiBold, presetName, targetDays, ansiReset,
		ansiBrightCyan, bar, ansiReset,
		ansiBold+ansiBrightYellow, pctElapsed, ansiReset,
		st.DaysElapsed, targetDays,
	)

	fmt.Fprintf(w, "%s  Remaining:%s %s%.1fh%s  │ %sCompensated Offline Gap:%s %s%.1fh%s  │ %sResumptions:%s %s%d%s  │ %sFeed Down:%s %s%.1fs%s\n",
		ansiGray, ansiReset, ansiBrightWhite, rem.Hours(), ansiReset,
		ansiGray, ansiReset, ansiBrightWhite, st.OfflineDowntimeSeconds/3600.0, ansiReset,
		ansiGray, ansiReset, ansiBrightWhite, st.ShutdownCount, ansiReset,
		ansiGray, ansiReset, ansiBrightWhite, st.FeedDownSeconds, ansiReset,
	)
	fmt.Println()

	// 3. FINANCIAL PNL & RISK METRICS MATRIX
	thinBorder := strings.Repeat("─", width-2)
	fmt.Fprintf(w, "%s┌%s┐%s\n", ansiBlue, thinBorder, ansiReset)

	colW := (width - 10) / 4
	cell := func(s string) string {
		return padRightVisible(s, colW)
	}

	retColor := ansiBrightGreen
	if st.TotalReturnPct < 0 {
		retColor = ansiBrightRed
	} else if st.TotalReturnPct == 0 {
		retColor = ansiBrightWhite
	}

	ddColor := ansiBrightGreen
	if st.MaxDrawdownPct > 1.5 {
		ddColor = ansiBrightRed
	} else if st.MaxDrawdownPct > 0.5 {
		ddColor = ansiBrightYellow
	}

	rPnLColor := ansiBrightGreen
	if st.RealizedPnL < 0 {
		rPnLColor = ansiBrightRed
	}

	uPnLColor := ansiBrightGreen
	if st.UnrealizedPnL < 0 {
		uPnLColor = ansiBrightRed
	}

	fmt.Fprintf(w, "%s│%s %s %s│%s %s %s│%s %s %s│%s %s %s│%s\n",
		ansiBlue, ansiReset, cell(fmt.Sprintf("Initial:  $%.2f", st.InitialCapital)),
		ansiBlue, ansiReset, cell(fmt.Sprintf("Cash:     $%.2f", st.Cash)),
		ansiBlue, ansiReset, cell(fmt.Sprintf("Realized: %s$%+.2f%s", rPnLColor, st.RealizedPnL, ansiReset)),
		ansiBlue, ansiReset, cell(fmt.Sprintf("Win Rate: %s%.1f%%%s", ansiBrightYellow, st.WinRate, ansiReset)),
		ansiBlue, ansiReset,
	)

	fmt.Fprintf(w, "%s│%s %s %s│%s %s %s│%s %s %s│%s %s %s│%s\n",
		ansiBlue, ansiReset, cell(fmt.Sprintf("Equity:   %s$%.2f%s", ansiBold+ansiBrightWhite, st.Equity, ansiReset)),
		ansiBlue, ansiReset, cell(fmt.Sprintf("Peak:     $%.2f", st.PeakEquity)),
		ansiBlue, ansiReset, cell(fmt.Sprintf("Unrealized:%s$%+.2f%s", uPnLColor, st.UnrealizedPnL, ansiReset)),
		ansiBlue, ansiReset, cell(fmt.Sprintf("Sharpe:   %s%.2f%s", ansiBrightWhite, st.SharpeRatio, ansiReset)),
		ansiBlue, ansiReset,
	)

	fmt.Fprintf(w, "%s│%s %s %s│%s %s %s│%s %s %s│%s %s %s│%s\n",
		ansiBlue, ansiReset, cell(fmt.Sprintf("Return:   %s%+.2f%%%s", retColor+ansiBold, st.TotalReturnPct, ansiReset)),
		ansiBlue, ansiReset, cell(fmt.Sprintf("Max DD:   %s%.2f%%%s", ddColor, st.MaxDrawdownPct, ansiReset)),
		ansiBlue, ansiReset, cell(fmt.Sprintf("DD Cap:   %s2.00%%%s", ansiGray, ansiReset)),
		ansiBlue, ansiReset, cell(fmt.Sprintf("Fills:    %s%d%s", ansiBrightCyan, st.TotalTrades, ansiReset)),
		ansiBlue, ansiReset,
	)

	fmt.Fprintf(w, "%s└%s┘%s\n", ansiBlue, thinBorder, ansiReset)

	// 4. MICROSTRUCTURE & DYNAMIC LEVERAGE TELEMETRY
	lev := st.Leverage
	regimeBadge := ansiBrightGreen + ansiBold + "CALM_MAKER" + ansiReset
	switch lev.RegimeName {
	case "TOXIC_DEFENSE":
		regimeBadge = ansiBrightYellow + ansiBold + "TOXIC_DEFENSE" + ansiReset
	case "CASCADE_ALERT":
		regimeBadge = ansiBrightRed + ansiBold + "CASCADE_ALERT" + ansiReset
	case "HIGH_VOLATILITY":
		regimeBadge = ansiBrightMagenta + ansiBold + "HIGH_VOLATILITY" + ansiReset
	case "DRAWDOWN_PRESERVE":
		regimeBadge = ansiBrightRed + ansiBold + "DRAWDOWN_PRESERVE" + ansiReset
	}

	effLev := lev.EffectiveLeverage
	if effLev <= 0 {
		effLev = 2.5
	}
	maxLev := lev.MaxLeverage
	if maxLev <= 0 {
		maxLev = 5.0
	}

	fmt.Fprintf(w, "%s[MICROSTRUCTURE TELEMETRY]%s Regime: %s │ Eff Leverage: %s%4.2fx%s (Cap: %.1fx) │ Vol: %.2fx │ Toxicity: %.2f │ Hawkes: %.2f\n",
		ansiCyan, ansiReset,
		regimeBadge,
		ansiBold+ansiBrightWhite, effLev, ansiReset,
		maxLev,
		lev.VolFactor, lev.ToxicityFactor, lev.CascadeFactor,
	)
	if lev.RegimeDetail != "" {
		fmt.Fprintf(w, "%s  Detail:%s %s%s%s\n", ansiGray, ansiReset, ansiDim, lev.RegimeDetail, ansiReset)
	}
	fmt.Println()

	// 5. OPEN POSITIONS TABLE
	fmt.Fprintf(w, "%s┌─ ACTIVE PORTFOLIO INVENTORY %s┐%s\n", ansiYellow, strings.Repeat("─", width-32), ansiReset)
	fmt.Fprintf(w, "%s  %-10s %-8s %-12s %-14s %-14s %-16s %-8s %-10s%s\n",
		ansiBold+ansiWhite, "SYMBOL", "SIDE", "SIZE", "ENTRY PRICE", "MARK PRICE", "UNREALIZED PNL", "TRADES", "NOTIONAL", ansiReset)

	if len(st.Positions) == 0 {
		fmt.Fprintf(w, "%s  No open positions (100%% Cash Equilibrium)%s\n", ansiGray, ansiReset)
	} else {
		for sym, pos := range st.Positions {
			side := ansiGray + "FLAT" + ansiReset
			notional := math.Abs(pos.Size) * pos.MarkPrice
			if pos.Size > 1e-6 {
				side = ansiBrightGreen + ansiBold + "LONG" + ansiReset
			} else if pos.Size < -1e-6 {
				side = ansiBrightMagenta + ansiBold + "SHORT" + ansiReset
			}

			pnlCol := ansiGray
			if pos.UnrealizedPnL > 0.0001 {
				pnlCol = ansiBrightGreen
			} else if pos.UnrealizedPnL < -0.0001 {
				pnlCol = ansiBrightRed
			}

			fmt.Fprintf(w, "  %-10s %-17s %-12.4f $%-13.2f $%-13.2f %s$%+15.2f%s %-8d $%-9.2f\n",
				sym, side, pos.Size, pos.AvgEntryPrice, pos.MarkPrice,
				pnlCol, pos.UnrealizedPnL, ansiReset,
				pos.TotalTrades, notional,
			)
		}
	}
	fmt.Fprintf(w, "%s└%s┘%s\n", ansiYellow, thinBorder, ansiReset)

	// 5.5 ACTIVE RESTING LIMIT ORDERS TABLE (L1/L2 BOOK)
	var allActiveOrders []paper.ActiveMakerOrder
	for _, ords := range st.ActiveOrders {
		allActiveOrders = append(allActiveOrders, ords...)
	}

	fmt.Fprintf(w, "%s┌─ ACTIVE RESTING LIMIT ORDERS (L1/L2 BOOK) %s┐%s\n", ansiCyan, strings.Repeat("─", width-44), ansiReset)
	fmt.Fprintf(w, "%s  %-10s %-6s %-14s %-10s %-14s %-12s %-8s %-20s%s\n",
		ansiBold+ansiWhite, "SYMBOL", "SIDE", "PRICE", "SIZE", "QUEUE AHEAD", "EDGE", "AGE", "STRATEGY", ansiReset)

	if len(allActiveOrders) == 0 {
		fmt.Fprintf(w, "%s  No active resting orders (Quotes paused / Gating)%s\n", ansiGray, ansiReset)
	} else {
		for _, ord := range allActiveOrders {
			sideCol := ansiBrightGreen + "BUY" + ansiReset
			if ord.Side == "SELL" {
				sideCol = ansiBrightMagenta + "SELL" + ansiReset
			}
			ageSec := now.Sub(ord.PlacedAt).Seconds()
			if ageSec < 0 {
				ageSec = 0
			}
			fmt.Fprintf(w, "  %-10s %-15s $%-13.2f %-10.4f %-14.4f %+7.2fbps %-7.1fs %-20s\n",
				ord.Symbol, sideCol, ord.Price, ord.Size, ord.QueueAhead, ord.EdgeBps, ageSec, ord.Strategy)
		}
	}
	fmt.Fprintf(w, "%s└%s┘%s\n", ansiCyan, thinBorder, ansiReset)

	// 6. RECENT EXECUTION BLOTTER
	fmt.Fprintf(w, "%s┌─ RECENT EXECUTIONS (BLOTTER) %s┐%s\n", ansiMagenta, strings.Repeat("─", width-33), ansiReset)
	fmt.Fprintf(w, "%s  %-9s %-9s %-6s %-7s %-12s %-9s %-10s %-8s %-12s %-18s%s\n",
		ansiBold+ansiWhite, "TIME(UTC)", "SYMBOL", "SIDE", "ROLE", "PRICE", "SIZE", "VALUE($)", "FEE($)", "NET PNL($)", "STRATEGY", ansiReset)

	if len(trades) == 0 {
		fmt.Fprintf(w, "%s  No fills recorded yet. Listening to book trades...%s\n", ansiGray, ansiReset)
	} else {
		for i, t := range trades {
			if i >= 8 {
				break
			}
			sideStr := ansiBrightGreen + "BUY" + ansiReset
			if t.Side == "SELL" {
				sideStr = ansiBrightMagenta + "SELL" + ansiReset
			} else if strings.Contains(t.Side, "LIQUIDATE") {
				sideStr = ansiBrightRed + "LIQ" + ansiReset
			}

			pnlStr := ansiGray + "$ 0.0000" + ansiReset
			if t.PnLRealized > 0.00001 {
				pnlStr = fmt.Sprintf("%s$%+.4f%s", ansiBrightGreen, t.PnLRealized, ansiReset)
			} else if t.PnLRealized < -0.00001 {
				pnlStr = fmt.Sprintf("%s$%+.4f%s", ansiBrightRed, t.PnLRealized, ansiReset)
			}

			strat := t.Strategy
			if len(strat) > 18 {
				strat = strat[:18]
			}

			timeStr := t.Timestamp.Format("15:04:05")
			fmt.Fprintf(w, "  %-9s %-9s %-15s %-7s $%-11.2f %-9.4f $%-9.2f $%-7.4f %-21s %-18s\n",
				timeStr, t.Symbol, sideStr, t.Role, t.Price, t.Size, t.ValueUSDT, t.FeeUSDT, pnlStr, strat)
		}
	}
	fmt.Fprintf(w, "%s└%s┘%s\n", ansiMagenta, thinBorder, ansiReset)

	// 7. FOOTER / SHORTCUT BAR
	sourceLoc := dataDir
	if remoteURL != "" {
		sourceLoc = remoteURL
	}
	fmt.Fprintf(w, "%s[q] Quit  [r] Force Refresh  │ Auto-refresh: 500ms │ Target: %s%s\n",
		ansiDim, sourceLoc, ansiReset)
}

func renderWaitingFrame(w io.Writer, err error, width int, dataDir, remoteURL string) {
	border := strings.Repeat("═", width-2)
	fmt.Fprintf(w, "%s╔%s╗%s\n", ansiBrightYellow, border, ansiReset)
	fmt.Fprintf(w, "%s║  PIQUANT QUANTITATIVE TERMINAL  │  CONNECTING TO ENGINE...%s║\n",
		ansiBrightYellow, strings.Repeat(" ", width-62))
	fmt.Fprintf(w, "%s╚%s╝%s\n\n", ansiBrightYellow, border, ansiReset)

	fmt.Fprintf(w, "%sStatus:%s %sWAITING FOR ACTIVE DRY RUN CHECKPOINT...%s\n\n",
		ansiBold, ansiReset, ansiBrightYellow, ansiReset)
	fmt.Fprintf(w, "%sNotice:%s %v\n\n", ansiGray, ansiReset, err)

	fmt.Fprintf(w, "To launch or resume the 14-day autonomous dry run engine:\n")
	fmt.Fprintf(w, "  %spiquant dry-run --capital 200 --window 7d%s\n\n", ansiBrightCyan, ansiReset)

	target := dataDir
	if remoteURL != "" {
		target = remoteURL
	}
	fmt.Fprintf(w, "%sPolling [%s] every 500ms...  Press [q] to exit.%s\n", ansiDim, target, ansiReset)
}

// stripANSI removes ANSI escape sequences so visible character length can be computed.
func stripANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if (s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z') {
				inEscape = false
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// padRightVisible pads a string with spaces until its visible length matches width.
func padRightVisible(s string, width int) string {
	vLen := len(stripANSI(s))
	if vLen >= width {
		return s
	}
	return s + strings.Repeat(" ", width-vLen)
}

// padLeftVisible pads a string on the left until its visible length matches width.
func padLeftVisible(s string, width int) string {
	vLen := len(stripANSI(s))
	if vLen >= width {
		return s
	}
	return strings.Repeat(" ", width-vLen) + s
}
func detectHardwareModel() string {
	if dt, err := os.ReadFile("/proc/device-tree/model"); err == nil && len(dt) > 0 {
		m := strings.TrimRight(string(dt), "\x00\r\n \t")
		if m != "" {
			return m
		}
	}
	// Fallback to runtime arch & hostname
	host, _ := os.Hostname()
	if host == "" {
		host = "pi5"
	}
	return fmt.Sprintf("%s (%s/%s)", host, runtime.GOOS, runtime.GOARCH)
}

// getTerminalWidth returns current terminal columns using TIOCGWINSZ or default 100.
func getTerminalWidth() int {
	type winsize struct {
		Row    uint16
		Col    uint16
		Xpixel uint16
		Ypixel uint16
	}
	var ws winsize
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, uintptr(os.Stdout.Fd()), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if err == 0 && ws.Col > 0 {
		return int(ws.Col)
	}
	return 100
}

// enableRawMode puts the terminal in cbreak/no-echo mode so single keystrokes like 'q' can be read.
func enableRawMode() func() {
	_ = exec.Command("stty", "cbreak", "-echo").Run()
	return func() {
		_ = exec.Command("stty", "-cbreak", "echo").Run()
	}
}

// readKeypresses reads single bytes from stdin without blocking the main event loop.
func readKeypresses(ch chan<- byte) {
	buf := make([]byte, 16)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		for i := 0; i < n; i++ {
			ch <- buf[i]
		}
	}
}
