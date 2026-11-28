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
	"sync"
	"time"

	"piquant-live/internal/alert"
	"piquant-live/internal/execution"
	"piquant-live/internal/quant/guardrails"
	"piquant-live/internal/trend"
	"piquant-live/internal/trendcache"
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
	safetyMarginBps    float64
	taxRatePct         float64
	rebalanceBufferPct float64
	orderPollInterval  time.Duration
}

func runExecute(ctx context.Context, args []string) error {
	opts, err := parseExecuteOptions(args)
	if err != nil {
		return err
	}
	report, err := executeWithOptions(ctx, opts)
	if len(report.Venue) > 0 {
		b, merr := json.MarshalIndent(report, "", " ")
		if merr != nil {
			return errors.Join(err, fmt.Errorf("marshal execute report: %w", merr))
		}
		fmt.Println(string(b))
	}
	return err
}

var knownPortfolioSelectors = map[string]struct{}{
	"":                                      {},
	"q42":                                   {},
	"q42_dualspeed":                         {},
	"q42_dualspeed_60_40_carry_spot":        {},
	"q42_balanced":                          {},
	"q42_balanced_50_50_carry_spot":         {},
	"q44":                                   {},
	"q44_bh_harvester":                      {},
	"q44_bh_vol_harvester_1x_2p5x_spot":     {},
	"q44_barbell":                           {},
	"q44_barbell_1x_bh_2p5x_dualspeed_spot": {},
	"q51":                                   {},
	"q51_volparity":                         {},
	"q51_btc_eth_sol_volparity_spot":        {},
	"q56":                                   {},
	"q56_quantum_fast":                      {},
	"q56_quantum_fast_carry_spot":           {},
	"q57":                                   {},
	"q57_hrp_hurst":                         {},
	"q57_hrp_conformal":                     {},
	"q57_hrp_hurst_conformal":               {},
	"q57_hrp_hurst_conformal_carry_spot":    {},
	"s36":                                   {},
	"s36_spot":                              {},
	"s36_qd_carry_blend_spot":               {},
	"s36_fut":                               {},
	"s36_qd_carry_blend_fut":                {},
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func validateExecuteOptions(opts executeOptions) error {
	key := strings.ToLower(strings.TrimSpace(opts.portfolio))
	if _, ok := knownPortfolioSelectors[key]; !ok {
		return fmt.Errorf("unknown portfolio %q (supported: q42_dualspeed, q42_balanced, q44_bh_harvester, q44_barbell, q51_volparity, q56_quantum_fast, q57_hrp_hurst, s36_spot, s36_fut)", opts.portfolio)
	}
	if !isFinite(opts.maxDailyUSD) || opts.maxDailyUSD <= 0 {
		return fmt.Errorf("--max-daily-usd must be a finite value > 0 (got %v)", opts.maxDailyUSD)
	}
	if !isFinite(opts.maxOrderUSD) || opts.maxOrderUSD <= 0 {
		return fmt.Errorf("--max-order-usd must be a finite value > 0 (got %v)", opts.maxOrderUSD)
	}
	if !isFinite(opts.taxRatePct) || opts.taxRatePct < 0 || opts.taxRatePct > 100 {
		return fmt.Errorf("--tax-rate-pct must be in [0, 100] (got %v)", opts.taxRatePct)
	}
	if !isFinite(opts.maxDrawdownPct) || opts.maxDrawdownPct <= 0 || opts.maxDrawdownPct > 100 {
		return fmt.Errorf("--max-drawdown-pct must be in (0, 100] (got %v)", opts.maxDrawdownPct)
	}
	if opts.orderTTL < 0 {
		return fmt.Errorf("--order-ttl must be >= 0 (got %v)", opts.orderTTL)
	}
	if !isFinite(opts.navUSD) || opts.navUSD < 0 {
		return fmt.Errorf("--nav must be a finite value >= 0 (got %v)", opts.navUSD)
	}
	if opts.maxQuoteAge < 0 {
		return fmt.Errorf("--max-quote-age must be >= 0 (got %v)", opts.maxQuoteAge)
	}
	if !isFinite(opts.expectedEdgeBps) {
		return fmt.Errorf("--expected-edge-bps must be finite (got %v)", opts.expectedEdgeBps)
	}
	if !isFinite(opts.roundTripFeeBps) || opts.roundTripFeeBps < 0 {
		return fmt.Errorf("--round-trip-fee-bps must be a finite value >= 0 (got %v)", opts.roundTripFeeBps)
	}
	if !isFinite(opts.spreadBps) || opts.spreadBps < 0 {
		return fmt.Errorf("--spread-bps must be a finite value >= 0 (got %v)", opts.spreadBps)
	}
	if !isFinite(opts.safetyMarginBps) || opts.safetyMarginBps < 0 {
		return fmt.Errorf("--safety-margin-bps must be a finite value >= 0 (got %v)", opts.safetyMarginBps)
	}
	if !isFinite(opts.rebalanceBufferPct) || opts.rebalanceBufferPct < 0 || opts.rebalanceBufferPct > 100 {
		return fmt.Errorf("--rebalance-buffer-pct must be in [0, 100] (got %v)", opts.rebalanceBufferPct)
	}
	if !isFinite(opts.maxPingMs) || opts.maxPingMs < 0 {
		return fmt.Errorf("--max-ping-ms must be a finite value >= 0 (got %v)", opts.maxPingMs)
	}
	return nil
}

func normalizeQuoteMid(q execution.Quote) execution.Quote {
	if (!isFinite(q.Mid) || q.Mid <= 0) && isFinite(q.Bid) && isFinite(q.Ask) && q.Bid > 0 && q.Ask > 0 {
		q.Mid = 0.5 * (q.Bid + q.Ask)
	}
	return q
}

func parseExecuteOptions(args []string) (executeOptions, error) {
	fs := flag.NewFlagSet("execute", flag.ContinueOnError)
	home, _ := os.UserHomeDir()
	defaultData := defaultDataDir()

	dryRun := fs.Bool("dry-run", true, "simulate orders without submitting to the exchange (requires --dry-run=false + --confirm-live + PIQUANT_LIVE_ARM=YES for live)")
	venue := fs.String("venue", "coinbase-spot", "execution venue (coinbase-spot)")
	portfolio := fs.String("portfolio", "q42_dualspeed", "target portfolio: q42_dualspeed | q42_balanced | q44_bh_harvester | q44_barbell | q51_volparity | q56_quantum_fast | q57_hrp_hurst | s36_spot | s36_fut")
	dir := fs.String("dir", defaultTrendDir(), "trend state directory")
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
	rebalanceBufferPct := fs.Float64("rebalance-buffer-pct", 0.0, "skip non-exit rebalances when |target_pct - current_pct| < buffer_pct (e.g. 3.0)")
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

	opts := executeOptions{
		dryRun:             *dryRun,
		venue:              *venue,
		portfolio:          *portfolio,
		dir:                *dir,
		carryDir:           *carryDir,
		dtb3Path:           *dtb3Path,
		dataDir:            *dataDir,
		envPath:            *envPath,
		journalPath:        resolvedJournal,
		hwmPath:            resolvedHWM,
		maxDailyUSD:        *maxDailyUSD,
		maxOrderUSD:        *maxOrderUSD,
		maxQuoteAge:        *maxQuoteAge,
		maxDrawdownPct:     *maxDrawdownPct,
		navUSD:             *navUSD,
		expectedEdgeBps:    *expectedEdgeBps,
		roundTripFeeBps:    *roundTripFeeBps,
		spreadBps:          *spreadBps,
		safetyMarginBps:    *safetyMarginBps,
		taxRatePct:         *taxRatePct,
		rebalanceBufferPct: *rebalanceBufferPct,
		pingProbe:          *pingProbe,
		maxPingMs:          *maxPingMs,
		orderTTL:           *orderTTL,
		confirmLive:        *confirmLive,
		alertEnv:           *alertEnv,
	}
	if err := validateExecuteOptions(opts); err != nil {
		return executeOptions{}, err
	}
	return opts, nil
}

func executeWithOptions(ctx context.Context, opts executeOptions) (ExecuteReport, error) {
	if err := validateExecuteOptions(opts); err != nil {
		return ExecuteReport{}, err
	}
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
		allFailed := true
		for _, v := range venues {
			if v.Successes > 0 {
				allFailed = false
				break
			}
		}
		if allFailed || plan.BestHTTPMedianMs <= 0 || (maxPingMs > 0 && plan.BestHTTPMedianMs > maxPingMs) {
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

	// Use shared trend cache for consistent caching across CLI tools
	trendCache := trendcache.NewTrendCache()

	st, err := trendCache.GetTrendState(filepath.Join(opts.dir, "trend_state.json"))
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
	cState, cerr := trendCache.GetCarryState(filepath.Join(opts.carryDir, "carry_state.json"))
	if cerr != nil && !errors.Is(cerr, os.ErrNotExist) {
		return ExecuteReport{
			TimestampUTC: time.Now().UTC().Format(time.RFC3339),
			DryRun:       opts.dryRun,
			LiveArmed:    liveArmed,
			Venue:        venueName,
			Portfolio:    opts.portfolio,
			HWMPath:      hwmPath,
			PingPlan:     pingPlan,
			Errors:       []string{fmt.Sprintf("load carry state: %v", cerr)},
		}, cerr
	}
	rates, rerr := trendCache.GetDTB3Rates(opts.dtb3Path)
	if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return ExecuteReport{
			TimestampUTC: time.Now().UTC().Format(time.RFC3339),
			DryRun:       opts.dryRun,
			LiveArmed:    liveArmed,
			Venue:        venueName,
			Portfolio:    opts.portfolio,
			HWMPath:      hwmPath,
			PingPlan:     pingPlan,
			Errors:       []string{fmt.Sprintf("load DTB3 rates: %v", rerr)},
		}, rerr
	}
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
		httpClient = execution.NewOptimizedHTTPClient(10 * time.Second)
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
				"SOL": 0,
			},
		}
	}

	host, _ := os.Hostname()
	alertCfg, aerr := alert.LoadEnv(opts.alertEnv)
	if aerr != nil && !errors.Is(aerr, os.ErrNotExist) && !opts.dryRun {
		err = fmt.Errorf("load alert env %s: %w", opts.alertEnv, aerr)
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
	alerter := &alert.Alerter{
		Cfg:      alertCfg,
		Source:   host,
		Cooldown: time.Hour,
		Logf:     func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) },
	}

	// Fetch account balances and initial venue quotes concurrently (batching product_ids on Coinbase when supported).
	quoteSyms := []string{"BTC-USD", "ETH-USD"}
	if blend.TargetSOLPortPct > 0 {
		quoteSyms = append(quoteSyms, "SOL-USD")
	}
	var (
		balances                               map[string]float64
		balErr, btcErr, ethErr, solErr         error
		btcQuote, ethQuote, solQuote           execution.Quote
		hasBTCQuote, hasETHQuote, hasSOLQuote  bool
		fetchWG                                sync.WaitGroup
	)
	fetchWG.Add(2)
	go func() {
		defer fetchWG.Done()
		balances, balErr = venueClient.GetBalances(ctx)
	}()
	go func() {
		defer fetchWG.Done()
		if batchClient, ok := venueClient.(execution.BatchQuoteClient); ok {
			qs, qerr := batchClient.GetQuotes(ctx, quoteSyms)
			if qerr == nil {
				btcQuote, hasBTCQuote = qs["BTC-USD"]
				ethQuote, hasETHQuote = qs["ETH-USD"]
				solQuote, hasSOLQuote = qs["SOL-USD"]
				if hasBTCQuote && hasETHQuote && (blend.TargetSOLPortPct <= 0 || hasSOLQuote) {
					return
				}
			}
		}
		var qwg sync.WaitGroup
		if !hasBTCQuote {
			qwg.Add(1)
			go func() { defer qwg.Done(); btcQuote, btcErr = venueClient.GetQuote(ctx, "BTC-USD"); hasBTCQuote = btcErr == nil }()
		}
		if !hasETHQuote {
			qwg.Add(1)
			go func() { defer qwg.Done(); ethQuote, ethErr = venueClient.GetQuote(ctx, "ETH-USD"); hasETHQuote = ethErr == nil }()
		}
		if blend.TargetSOLPortPct > 0 && !hasSOLQuote {
			qwg.Add(1)
			go func() { defer qwg.Done(); solQuote, solErr = venueClient.GetQuote(ctx, "SOL-USD"); hasSOLQuote = solErr == nil }()
		}
		qwg.Wait()
	}()
	fetchWG.Wait()

	if balErr != nil {
		err = fmt.Errorf("fetch %s balances: %w", venueClient.Name(), balErr)
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
	if btcErr != nil || !hasBTCQuote {
		if btcErr == nil {
			btcErr = errors.New("missing BTC-USD quote")
		}
		err = fmt.Errorf("fetch BTC-USD quote: %w", btcErr)
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
	if ethErr != nil || !hasETHQuote {
		if ethErr == nil {
			ethErr = errors.New("missing ETH-USD quote")
		}
		err = fmt.Errorf("fetch ETH-USD quote: %w", ethErr)
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

	needSOL := blend.TargetSOLPortPct > 0 || balances["SOL"] > 0
	if needSOL && !hasSOLQuote {
		if solErr == nil {
			solQuote, solErr = venueClient.GetQuote(ctx, "SOL-USD")
		}
		if solErr != nil {
			err = fmt.Errorf("fetch SOL-USD quote: %w", solErr)
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
	}

	btcQuote = normalizeQuoteMid(btcQuote)
	ethQuote = normalizeQuoteMid(ethQuote)
	if needSOL {
		solQuote = normalizeQuoteMid(solQuote)
	}

	nav := opts.navUSD
	if nav <= 0 {
		nav = balances["USD"] + balances["BTC"]*btcQuote.Mid + balances["ETH"]*ethQuote.Mid
		if needSOL {
			nav += balances["SOL"] * solQuote.Mid
		}
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
	posMap := map[string]float64{
		"BTC-USD": balances["BTC"],
		"ETH-USD": balances["ETH"],
	}
	pxMap := map[string]float64{
		"BTC-USD": btcQuote.Mid,
		"ETH-USD": ethQuote.Mid,
	}
	if needSOL {
		posMap["SOL-USD"] = balances["SOL"]
		pxMap["SOL-USD"] = solQuote.Mid
	}
	guard.UpdatePortfolioState(
		nav,
		hwm.PeakNavUSD,
		posMap,
		pxMap,
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

	quotesMap := map[string]execution.Quote{
		"BTC-USD": btcQuote,
		"ETH-USD": ethQuote,
	}
	if needSOL {
		quotesMap["SOL-USD"] = solQuote
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
		Quotes:            quotesMap,
		Plans:             []PlannedOrder{},
		Results:           []execution.ExecutionResult{},
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
		if needSOL {
			expectedBalances["SOL"] = balances["SOL"]
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
	if needSOL {
		legs = append(legs, struct {
			asset     string
			symbol    string
			targetPct float64
			quote     execution.Quote
		}{asset: "SOL", symbol: "SOL-USD", targetPct: blend.TargetSOLPortPct, quote: solQuote})
	}

	availUSD := balances["USD"]
	var submitErrs []error
	for _, leg := range legs {
		leg.quote = normalizeQuoteMid(leg.quote)
		targetUSD := nav * (leg.targetPct / 100.0)
		currentQty := balances[leg.asset]
		if !isFinite(leg.quote.Mid) || leg.quote.Mid <= 0 {
			plan := PlannedOrder{
				Symbol:        leg.symbol,
				Asset:         leg.asset,
				TargetPct:     leg.targetPct,
				TargetUSD:     roundCents(targetUSD),
				CurrentQty:    currentQty,
				SkippedReason: "invalid_quote_mid",
			}
			report.Plans = append(report.Plans, plan)
			qErr := fmt.Errorf("%s: invalid quote mid %.6f", leg.symbol, leg.quote.Mid)
			report.Errors = append(report.Errors, qErr.Error())
			submitErrs = append(submitErrs, qErr)
			continue
		}
		currentPct := 0.0
		if nav > 0 {
			currentPct = (currentQty * leg.quote.Mid / nav) * 100.0
		}
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
		if opts.rebalanceBufferPct > 0 && leg.targetPct > 0 && math.Abs(leg.targetPct-currentPct) < opts.rebalanceBufferPct {
			plan.SkippedReason = fmt.Sprintf("within_rebalance_buffer (delta %.2f%% < %.2f%%)", math.Abs(leg.targetPct-currentPct), opts.rebalanceBufferPct)
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

func (d *dryRunSpotVenue) GetQuotes(ctx context.Context, symbols []string) (map[string]execution.Quote, error) {
	return d.quoteClient.GetQuotes(ctx, symbols)
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
