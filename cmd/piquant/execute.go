package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"piquant-live/internal/alert"
	"piquant-live/internal/carry"
	"piquant-live/internal/execution"
	"piquant-live/internal/quant/guardrails"
	"piquant-live/internal/trend"
)

// PlannedOrder captures one asset leg's rebalance calculation prior to LiveRouter submission.
type PlannedOrder struct {
	Symbol         string  `json:"symbol"`
	Asset          string  `json:"asset"`
	Side           string  `json:"side,omitempty"`
	TargetPct      float64 `json:"target_pct"`
	TargetUSD      float64 `json:"target_usd"`
	CurrentQty     float64 `json:"current_qty"`
	TargetQty      float64 `json:"target_qty"`
	DeltaQty       float64 `json:"delta_qty"`
	OrderQty       float64 `json:"order_qty"`
	LimitPrice     float64 `json:"limit_price,omitempty"`
	EstNotionalUSD float64 `json:"est_notional_usd"`
	IsReduceOnly   bool    `json:"is_reduce_only"`
	SkippedReason  string  `json:"skipped_reason,omitempty"`
}

// ExecuteReport is the JSON summary emitted by `piquant execute`.
type ExecuteReport struct {
	TimestampUTC      string                       `json:"timestamp_utc"`
	DryRun            bool                         `json:"dry_run"`
	LiveArmed         bool                         `json:"live_armed"`
	Venue             string                       `json:"venue"`
	Portfolio         string                       `json:"portfolio"`
	BlendName         string                       `json:"blend_name"`
	NavUSD            float64                      `json:"nav_usd"`
	PeakNavUSD        float64                      `json:"peak_nav_usd"`
	DrawdownPct       float64                      `json:"drawdown_pct"`
	HWMPath           string                       `json:"hwm_path,omitempty"`
	PingPlan          *execution.PingExecutionPlan `json:"ping_plan,omitempty"`
	CredentialsOK     bool                         `json:"credentials_configured"`
	SimulatedBalances bool                         `json:"simulated_balances,omitempty"`
	Balances          map[string]float64           `json:"balances"`
	Quotes            map[string]execution.Quote   `json:"quotes,omitempty"`
	Reconcile         execution.ReconcileReport    `json:"reconcile"`
	Plans             []PlannedOrder               `json:"plans"`
	Results           []execution.ExecutionResult  `json:"results"`
	Errors            []string                     `json:"errors,omitempty"`
}

type executeOptions struct {
	dryRun            bool
	venue             string
	portfolio         string
	dir               string
	carryDir          string
	dtb3Path          string
	dataDir           string
	envPath           string
	journalPath       string
	maxDailyUSD       float64
	maxOrderUSD       float64
	maxQuoteAge       time.Duration
	navUSD            float64
	expectedEdgeBps   float64
	confirmLive       string
	alertEnv          string
	coinbaseBaseURL   string
	httpClient        *http.Client
	pingProbe         bool
	maxPingMs         float64
	orderTTL          time.Duration
	pingEndpoints     []execution.ExchangeEndpoint
	hwmPath           string
	maxDrawdownPct    float64
	roundTripFeeBps   float64
	spreadBps         float64
	safetyMarginBps   float64
	taxRatePct        float64
	orderPollInterval time.Duration
}

func runExecute(ctx context.Context, args []string) error {
	opts, err := parseExecuteOptions(args)
	if err != nil {
		return err
	}
	report, err := executeWithOptions(ctx, opts)
	if len(report.Venue) > 0 {
		if b, merr := json.MarshalIndent(report, "", " "); merr == nil {
			fmt.Println(string(b))
		}
	}
	return err
}

func parseExecuteOptions(args []string) (executeOptions, error) {
	fs := flag.NewFlagSet("execute", flag.ContinueOnError)
	home, _ := os.UserHomeDir()
	defaultData := defaultDataDir()

	dryRun := fs.Bool("dry-run", true, "simulate orders without submitting to the exchange (requires --dry-run=false + --confirm-live + PIQUANT_LIVE_ARM=YES for live)")
	venue := fs.String("venue", "coinbase-spot", "execution venue (coinbase-spot)")
	portfolio := fs.String("portfolio", "q42_dualspeed", "target portfolio: q42_dualspeed | q42_balanced | q44_bh_harvester | q44_barbell | s36_spot | s36_fut")
	dir := fs.String("dir", filepath.Join(home, "piquant-trend"), "trend state directory")
	carryDir := fs.String("carry-dir", filepath.Join(home, "piquant-collect", "carry"), "carry state directory")
	dtb3Path := fs.String("dtb3", filepath.Join(home, "piquant-data", "macro", "DTB3.csv"), "FRED DTB3 CSV path")
	dataDir := fs.String("data", defaultData, "data directory (default $PIQUANT_DATA_DIR or ~/piquant-data)")
	envPath := fs.String("env", execution.DefaultCoinbaseEnvPath(), "Coinbase credentials file (COINBASE_API_KEY, COINBASE_API_SECRET)")
	journalPath := fs.String("journal", "", "idempotent order journal JSONL path (default <data>/paper/order_journal.jsonl)")
	hwmPath := fs.String("hwm-path", "", "persistent high-water mark JSON path (default <data>/paper/exec_hwm.json)")
	maxDailyUSD := fs.Float64("max-daily-usd", 500, "maximum daily order notional USD")
	maxOrderUSD := fs.Float64("max-order-usd", 250, "maximum single order notional USD")
	maxQuoteAge := fs.Duration("max-quote-age", 2*time.Minute, "maximum allowed age of venue top-of-book quote")
	maxDrawdownPct := fs.Float64("max-drawdown-pct", 15.0, "maximum drawdown % from persisted peak NAV before tripping kill switch")
	navUSD := fs.Float64("nav", 1000, "target portfolio NAV USD (0 = derive from live venue balances)")
	expectedEdgeBps := fs.Float64("expected-edge-bps", 25, "expected strategy alpha edge in bps for fee hurdle check")
	roundTripFeeBps := fs.Float64("round-trip-fee-bps", 10.0, "round-trip venue fee hurdle in bps (e.g. Coinbase Advanced Trade intro 80-120 bps RT vs VIP <10-20 bps RT)")
	spreadBps := fs.Float64("spread-bps", 2.0, "expected bid-ask spread hurdle in bps")
	safetyMarginBps := fs.Float64("safety-margin-bps", 5.0, "minimum net alpha safety margin in bps")
	taxRatePct := fs.Float64("tax-rate-pct", 33.3, "estimated short-term tax rate percentage mapped to TaxDragFraction")
	pingProbe := fs.Bool("ping-probe", true, "probe venue ping latency before trading and abort if median RTT exceeds --max-ping-ms")
	maxPingMs := fs.Float64("max-ping-ms", 1500.0, "maximum allowed venue HTTP median ping latency in ms")
	orderTTL := fs.Duration("order-ttl", 0, "live post-only order TTL before auto-cancelling unfilled orders (0 = GTC; e.g. 15s)")
	confirmLive := fs.String("confirm-live", "", "live arming token (must equal ARM_LIVE_EXECUTION with PIQUANT_LIVE_ARM=YES)")
	alertEnv := addAlertFlag(fs)

	if err := fs.Parse(args); err != nil {
		return executeOptions{}, err
	}

	resolvedJournal := strings.TrimSpace(*journalPath)
	if resolvedJournal == "" {
		resolvedJournal = filepath.Join(*dataDir, "paper", "order_journal.jsonl")
	}
	resolvedHWM := strings.TrimSpace(*hwmPath)
	if resolvedHWM == "" {
		resolvedHWM = filepath.Join(*dataDir, "paper", "exec_hwm.json")
	}

	return executeOptions{
		dryRun:          *dryRun,
		venue:           *venue,
		portfolio:       *portfolio,
		dir:             *dir,
		carryDir:        *carryDir,
		dtb3Path:        *dtb3Path,
		dataDir:         *dataDir,
		envPath:         *envPath,
		journalPath:     resolvedJournal,
		hwmPath:         resolvedHWM,
		maxDailyUSD:     *maxDailyUSD,
		maxOrderUSD:     *maxOrderUSD,
		maxQuoteAge:     *maxQuoteAge,
		maxDrawdownPct:  *maxDrawdownPct,
		navUSD:          *navUSD,
		expectedEdgeBps: *expectedEdgeBps,
		roundTripFeeBps: *roundTripFeeBps,
		spreadBps:       *spreadBps,
		safetyMarginBps: *safetyMarginBps,
		taxRatePct:      *taxRatePct,
		pingProbe:       *pingProbe,
		maxPingMs:       *maxPingMs,
		orderTTL:        *orderTTL,
		confirmLive:     *confirmLive,
		alertEnv:        *alertEnv,
	}, nil
}

func executeWithOptions(ctx context.Context, opts executeOptions) (ExecuteReport, error) {
	// Safety check FIRST: live mode requires both CLI token and environment arming flag.
	liveArmed := !opts.dryRun &&
		opts.confirmLive == execution.LiveArmConfirmToken &&
		os.Getenv(execution.LiveArmEnvVar) == execution.LiveArmEnvValue
	if !opts.dryRun && !liveArmed {
		return ExecuteReport{}, execution.ErrLiveNotArmed
	}

	venueName := strings.ToLower(strings.TrimSpace(opts.venue))
	if venueName == "coinbase" {
		venueName = "coinbase-spot"
	}
	if venueName != "coinbase-spot" {
		return ExecuteReport{}, fmt.Errorf("unsupported execution venue %q (supported: coinbase-spot)", opts.venue)
	}

	creds := execution.LoadCoinbaseCredentials(opts.envPath)
	if !opts.dryRun && !creds.Ready() {
		return ExecuteReport{}, fmt.Errorf("coinbase live mode requires configured API credentials (set COINBASE_API_KEY/COINBASE_API_SECRET or %s)", opts.envPath)
	}

	hwmPath := strings.TrimSpace(opts.hwmPath)
	if hwmPath == "" && strings.TrimSpace(opts.dataDir) != "" {
		hwmPath = filepath.Join(opts.dataDir, "paper", "exec_hwm.json")
	}

	var pingPlan *execution.PingExecutionPlan
	if opts.pingProbe {
		endpoints := opts.pingEndpoints
		if len(endpoints) == 0 {
			endpoints = []execution.ExchangeEndpoint{
				{Venue: "coinbase", URL: "https://api.exchange.coinbase.com/time"},
			}
		}
		venues := execution.ProbeEndpoints(ctx, endpoints, 2, 3*time.Second)
		plan := execution.PlanForLatency(venues, time.Now().UTC())
		pingPlan = &plan
		maxPingMs := opts.maxPingMs
		if maxPingMs <= 0 {
			maxPingMs = 1500.0
		}
		allFailed := true
		for _, v := range venues {
			if v.Successes > 0 {
				allFailed = false
				break
			}
		}
		if allFailed || plan.BestHTTPMedianMs <= 0 || plan.BestHTTPMedianMs > maxPingMs {
			var err error
			if allFailed {
				err = fmt.Errorf("%w: all %d ping probe endpoint(s) failed", execution.ErrHighLatency, len(endpoints))
			} else {
				err = fmt.Errorf("%w: %s median latency %.2fms > max %.2fms", execution.ErrHighLatency, plan.BestVenue, plan.BestHTTPMedianMs, maxPingMs)
			}
			return ExecuteReport{
				TimestampUTC: time.Now().UTC().Format(time.RFC3339),
				DryRun:       opts.dryRun,
				LiveArmed:    liveArmed,
				Venue:        venueName,
				Portfolio:    opts.portfolio,
				HWMPath:      hwmPath,
				PingPlan:     pingPlan,
				Errors:       []string{err.Error()},
			}, err
		}
	}

	st, err := trend.Load(filepath.Join(opts.dir, "trend_state.json"))
	if err != nil {
		return ExecuteReport{
			TimestampUTC: time.Now().UTC().Format(time.RFC3339),
			DryRun:       opts.dryRun,
			LiveArmed:    liveArmed,
			Venue:        venueName,
			Portfolio:    opts.portfolio,
			HWMPath:      hwmPath,
			PingPlan:     pingPlan,
			Errors:       []string{err.Error()},
		}, err
	}
	if st == nil {
		st = trend.NewState("")
	}
	cState, _ := carry.Load(filepath.Join(opts.carryDir, "carry_state.json"))
	rates, _ := trend.LoadDTB3Rates(opts.dtb3Path)
	blend, ok := trend.SelectBlend(st, cState, rates, opts.portfolio, pingPlan)
	if !ok {
		err := fmt.Errorf("portfolio %q unavailable in trend state", opts.portfolio)
		return ExecuteReport{
			TimestampUTC: time.Now().UTC().Format(time.RFC3339),
			DryRun:       opts.dryRun,
			LiveArmed:    liveArmed,
			Venue:        venueName,
			Portfolio:    opts.portfolio,
			HWMPath:      hwmPath,
			PingPlan:     pingPlan,
			Errors:       []string{err.Error()},
		}, err
	}

	httpClient := opts.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	cbClient := execution.NewCoinbaseSpotClient(creds, opts.coinbaseBaseURL, httpClient)

	simNav := opts.navUSD
	if simNav <= 0 {
		simNav = 1000.0
	}
	var venueClient execution.VenueClient = cbClient
	simulatedBalances := false
	if opts.dryRun && !creds.Ready() {
		simulatedBalances = true
		venueClient = &dryRunSpotVenue{
			quoteClient: cbClient,
			balances: map[string]float64{
				"USD": simNav,
				"BTC": 0,
				"ETH": 0,
			},
		}
	}

	host, _ := os.Hostname()
	alertCfg, _ := alert.LoadEnv(opts.alertEnv)
	alerter := &alert.Alerter{
		Cfg:      alertCfg,
		Source:   host,
		Cooldown: time.Hour,
		Logf:     func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) },
	}

	balances, err := venueClient.GetBalances(ctx)
	if err != nil {
		err = fmt.Errorf("fetch %s balances: %w", venueClient.Name(), err)
		return ExecuteReport{
			TimestampUTC: time.Now().UTC().Format(time.RFC3339),
			DryRun:       opts.dryRun,
			LiveArmed:    liveArmed,
			Venue:        venueClient.Name(),
			Portfolio:    opts.portfolio,
			BlendName:    blend.Name,
			HWMPath:      hwmPath,
			PingPlan:     pingPlan,
			Errors:       []string{err.Error()},
		}, err
	}

	btcQuote, err := venueClient.GetQuote(ctx, "BTC-USD")
	if err != nil {
		err = fmt.Errorf("fetch BTC-USD quote: %w", err)
		return ExecuteReport{
			TimestampUTC: time.Now().UTC().Format(time.RFC3339),
			DryRun:       opts.dryRun,
			LiveArmed:    liveArmed,
			Venue:        venueClient.Name(),
			Portfolio:    opts.portfolio,
			BlendName:    blend.Name,
			HWMPath:      hwmPath,
			PingPlan:     pingPlan,
			Errors:       []string{err.Error()},
		}, err
	}
	ethQuote, err := venueClient.GetQuote(ctx, "ETH-USD")
	if err != nil {
		err = fmt.Errorf("fetch ETH-USD quote: %w", err)
		return ExecuteReport{
			TimestampUTC: time.Now().UTC().Format(time.RFC3339),
			DryRun:       opts.dryRun,
			LiveArmed:    liveArmed,
			Venue:        venueClient.Name(),
			Portfolio:    opts.portfolio,
			BlendName:    blend.Name,
			HWMPath:      hwmPath,
			PingPlan:     pingPlan,
			Errors:       []string{err.Error()},
		}, err
	}

	nav := opts.navUSD
	if nav <= 0 {
		nav = balances["USD"] + balances["BTC"]*btcQuote.Mid + balances["ETH"]*ethQuote.Mid
		if nav <= 0 && opts.dryRun {
			nav = 1000.0
		}
	}
	if nav <= 0 {
		err = fmt.Errorf("cannot determine positive portfolio NAV (got %.2f)", nav)
		return ExecuteReport{
			TimestampUTC: time.Now().UTC().Format(time.RFC3339),
			DryRun:       opts.dryRun,
			LiveArmed:    liveArmed,
			Venue:        venueClient.Name(),
			Portfolio:    opts.portfolio,
			BlendName:    blend.Name,
			HWMPath:      hwmPath,
			PingPlan:     pingPlan,
			Errors:       []string{err.Error()},
		}, err
	}

	now := time.Now().UTC()
	hwm := execution.HighWaterMarkState{PeakNavUSD: nav, LastNavUSD: nav}
	if hwmPath != "" {
		hwm, err = execution.UpdateAndSaveHighWaterMark(hwmPath, nav, now)
		if err != nil {
			err = fmt.Errorf("update high-water mark %s: %w", hwmPath, err)
			return ExecuteReport{
				TimestampUTC: now.Format(time.RFC3339),
				DryRun:       opts.dryRun,
				LiveArmed:    liveArmed,
				Venue:        venueClient.Name(),
				Portfolio:    opts.portfolio,
				BlendName:    blend.Name,
				NavUSD:       roundCents(nav),
				HWMPath:      hwmPath,
				PingPlan:     pingPlan,
				Errors:       []string{err.Error()},
			}, err
		}
	}
	drawdownPct := 0.0
	if hwm.PeakNavUSD > 0 && nav < hwm.PeakNavUSD {
		drawdownPct = (1.0 - nav/hwm.PeakNavUSD) * 100.0
	}

	maxDrawdownPct := opts.maxDrawdownPct
	if maxDrawdownPct <= 0 {
		maxDrawdownPct = 15.0
	}
	guardCfg := guardrails.DefaultGuardrailConfig()
	guardCfg.MaxDailyDrawdownPct = maxDrawdownPct
	if opts.maxOrderUSD > 0 {
		guardCfg.Exposure.MaxOrderQtyUSD = opts.maxOrderUSD
	}
	guardCfg.Exposure.MaxGrossUSD = math.Max(25000.0, nav*3.0)
	guardCfg.Exposure.MaxNetUSD = math.Max(15000.0, nav*3.0)
	guardCfg.MaxOrderQty = math.Max(100.0, opts.maxOrderUSD)
	guard := guardrails.NewPreTradeGuard(guardCfg)
	guard.UpdatePortfolioState(
		nav,
		hwm.PeakNavUSD,
		map[string]float64{
			"BTC-USD": balances["BTC"],
			"ETH-USD": balances["ETH"],
		},
		map[string]float64{
			"BTC-USD": btcQuote.Mid,
			"ETH-USD": ethQuote.Mid,
		},
		now,
	)
	if tripped, reason, _, _, _, _ := guard.Status(); tripped {
		alerter.Send("drawdown", "Execute drawdown kill switch tripped", reason)
	}

	routerCfg := execution.DefaultRouterConfig()
	routerCfg.DryRun = opts.dryRun
	routerCfg.LiveArmToken = opts.confirmLive
	routerCfg.KillLockStatePath = filepath.Join(opts.dataDir, "paper", "dryrun_state.json")
	if opts.maxQuoteAge > 0 {
		routerCfg.MaxQuoteAge = opts.maxQuoteAge
	}
	if pingPlan != nil && pingPlan.StaleBookGuardMs > 0 {
		guardAge := time.Duration(pingPlan.StaleBookGuardMs) * time.Millisecond
		if guardAge < 30*time.Second {
			guardAge = 30 * time.Second
		}
		if routerCfg.MaxQuoteAge <= 0 || guardAge < routerCfg.MaxQuoteAge {
			routerCfg.MaxQuoteAge = guardAge
		}
	}
	routerCfg.MaxDailyNotionalUSD = opts.maxDailyUSD
	routerCfg.OrderTTL = opts.orderTTL
	routerCfg.OrderPollInterval = opts.orderPollInterval
	routerCfg.JournalPath = opts.journalPath
	taxDrag := opts.taxRatePct / 100.0
	if taxDrag < 0 {
		taxDrag = 0
	}
	routerCfg.FeeHurdle = guardrails.FeeHurdleSpec{
		RoundTripFeeBps: opts.roundTripFeeBps,
		SpreadBps:       opts.spreadBps,
		SlippageBps:     0.0,
		SafetyMarginBps: opts.safetyMarginBps,
		TaxDragFraction: taxDrag,
	}
	routerCfg.OnAlert = func(key, subject, body string) {
		alerter.Send(key, subject, body)
	}

	router, err := execution.NewLiveRouter(routerCfg, guard, venueClient)
	if err != nil {
		return ExecuteReport{
			TimestampUTC: now.Format(time.RFC3339),
			DryRun:       opts.dryRun,
			LiveArmed:    liveArmed,
			Venue:        venueClient.Name(),
			Portfolio:    opts.portfolio,
			BlendName:    blend.Name,
			NavUSD:       roundCents(nav),
			PeakNavUSD:   roundCents(hwm.PeakNavUSD),
			DrawdownPct:  roundCents(drawdownPct),
			HWMPath:      hwmPath,
			PingPlan:     pingPlan,
			Errors:       []string{err.Error()},
		}, err
	}

	report := ExecuteReport{
		TimestampUTC:      now.Format(time.RFC3339),
		DryRun:            opts.dryRun,
		LiveArmed:         liveArmed,
		Venue:             venueClient.Name(),
		Portfolio:         opts.portfolio,
		BlendName:         blend.Name,
		NavUSD:            roundCents(nav),
		PeakNavUSD:        roundCents(hwm.PeakNavUSD),
		DrawdownPct:       roundCents(drawdownPct),
		HWMPath:           hwmPath,
		PingPlan:          pingPlan,
		CredentialsOK:     creds.Ready(),
		SimulatedBalances: simulatedBalances,
		Balances:          balances,
		Quotes: map[string]execution.Quote{
			"BTC-USD": btcQuote,
			"ETH-USD": ethQuote,
		},
		Plans:   []PlannedOrder{},
		Results: []execution.ExecutionResult{},
	}

	expectedBalances, expErr := loadExpectedBalancesIfPresent(filepath.Join(opts.dataDir, "paper", "expected_balances.json"))
	if expErr != nil {
		report.Errors = append(report.Errors, expErr.Error())
		return report, expErr
	}
	if expectedBalances == nil {
		expectedBalances = map[string]float64{
			"USD": balances["USD"],
			"BTC": balances["BTC"],
			"ETH": balances["ETH"],
		}
	}

	recReport, recErr := router.Reconcile(ctx, expectedBalances)
	report.Reconcile = recReport
	if recErr != nil {
		report.Errors = append(report.Errors, recErr.Error())
		return report, recErr
	}

	legs := []struct {
		asset     string
		symbol    string
		targetPct float64
		quote     execution.Quote
	}{
		{asset: "BTC", symbol: "BTC-USD", targetPct: blend.TargetBTCPortPct, quote: btcQuote},
		{asset: "ETH", symbol: "ETH-USD", targetPct: blend.TargetETHPortPct, quote: ethQuote},
	}

	availUSD := balances["USD"]
	var submitErrs []error
	for _, leg := range legs {
		targetUSD := nav * (leg.targetPct / 100.0)
		currentQty := balances[leg.asset]
		targetQty := targetUSD / leg.quote.Mid
		deltaQty := targetQty - currentQty

		plan := PlannedOrder{
			Symbol:     leg.symbol,
			Asset:      leg.asset,
			TargetPct:  leg.targetPct,
			TargetUSD:  roundCents(targetUSD),
			CurrentQty: currentQty,
			TargetQty:  targetQty,
			DeltaQty:   deltaQty,
		}

		if math.Abs(deltaQty) <= 1e-9 {
			plan.SkippedReason = "on_target"
			report.Plans = append(report.Plans, plan)
			continue
		}

		side := "BUY"
		orderQty := deltaQty
		isReduceOnly := false
		if deltaQty < 0 {
			side = "SELL"
			orderQty = -deltaQty
			isReduceOnly = currentQty > 0
			if orderQty > currentQty {
				orderQty = currentQty
			}
		}

		limitPx := execution.PassiveLimitPrice(side, leg.quote, routerCfg.PostOnlyOffsetBps)
		if side == "BUY" && limitPx > 0 {
			if opts.maxOrderUSD > 0 && orderQty*limitPx > opts.maxOrderUSD {
				orderQty = (opts.maxOrderUSD * 0.999) / limitPx
			}
			if orderQty*limitPx > availUSD {
				orderQty = math.Max(0, (availUSD*0.999)/limitPx)
			}
		}

		estNotional := orderQty * limitPx
		plan.Side = side
		plan.OrderQty = orderQty
		plan.LimitPrice = limitPx
		plan.EstNotionalUSD = roundCents(estNotional)
		plan.IsReduceOnly = isReduceOnly

		if estNotional < routerCfg.MinOrderUSD || orderQty <= 0 {
			plan.SkippedReason = fmt.Sprintf("order notional $%.2f below min $%.2f", estNotional, routerCfg.MinOrderUSD)
			report.Plans = append(report.Plans, plan)
			continue
		}

		report.Plans = append(report.Plans, plan)
		res, err := router.SubmitTargetOrder(ctx, leg.symbol, side, orderQty, opts.expectedEdgeBps, isReduceOnly)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s %s: %v", leg.symbol, side, err))
			submitErrs = append(submitErrs, fmt.Errorf("%s %s: %w", leg.symbol, side, err))
			continue
		}
		if side == "BUY" && !res.SkippedDuplicate {
			spentUSD := res.NotionalUSD
			if res.CancelledOnTTL {
				spentUSD = res.Receipt.FilledQty * res.Receipt.Price
			}
			availUSD = math.Max(0, availUSD-spentUSD)
		}
		report.Results = append(report.Results, res)
	}

	if len(submitErrs) > 0 {
		return report, errors.Join(submitErrs...)
	}
	return report, nil
}

func loadExpectedBalancesIfPresent(path string) (map[string]float64, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read expected balances %s: %w", path, err)
	}
	var out map[string]float64
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("parse expected balances %s: %w", path, err)
	}
	return out, nil
}

func roundCents(v float64) float64 {
	return math.Round(v*100.0) / 100.0
}

type dryRunSpotVenue struct {
	quoteClient *execution.CoinbaseSpotClient
	balances    map[string]float64
}

func (d *dryRunSpotVenue) Name() string {
	return d.quoteClient.Name()
}

func (d *dryRunSpotVenue) GetBalances(context.Context) (map[string]float64, error) {
	cp := make(map[string]float64, len(d.balances))
	for k, v := range d.balances {
		cp[k] = v
	}
	return cp, nil
}

func (d *dryRunSpotVenue) GetQuote(ctx context.Context, symbol string) (execution.Quote, error) {
	return d.quoteClient.GetQuote(ctx, symbol)
}

func (d *dryRunSpotVenue) PlaceLimitOrder(context.Context, execution.LimitOrderRequest) (execution.OrderReceipt, error) {
	return execution.OrderReceipt{}, errors.New("dry-run simulated venue cannot submit live orders")
}

func (d *dryRunSpotVenue) CancelOrder(context.Context, string) error {
	return nil
}

func (d *dryRunSpotVenue) GetOrder(_ context.Context, orderID string) (execution.OrderReceipt, error) {
	return execution.OrderReceipt{OrderID: orderID, Status: "DRY_RUN", DryRun: true}, nil
}
