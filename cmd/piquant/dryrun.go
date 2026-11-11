package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"piquant-live/internal/backtest"
	"piquant-live/internal/config"
	"piquant-live/internal/dashboard"
	"piquant-live/internal/discovery"
	"piquant-live/internal/engine"
	"piquant-live/internal/experiment"
	"piquant-live/internal/paper"
	"piquant-live/internal/secrets"
)

type dryRunFlags struct {
	serveFlags
	capital           float64
	durationH         float64
	window            string
	maxDD             float64
	baseLev           float64
	maxLev            float64
	symbols           string
	targetProfitBps   float64
	entryOffsetBps    float64
	stopLossBps       float64
	monthlyGainCap    float64
	spotOnly          bool
	taxRate           float64
	zeroFeeMaker      bool
	clipFraction      float64
	cleanReset        bool
	enableTaker       bool
	debug             bool
	statusOnly        bool
	reportOnly        bool
	watch             bool
	live              bool
	enableFallback    bool
	enableAllModels   bool
	extendOnShutdown  bool
	flattenOnShutdown bool
	wsURL             string
	enableCoinbase    bool
	coinbaseWS        string
	feedVenue         string
	expBreaker        int
	enableNews        bool
	region            string
	makerFeeBps       float64
	takerFeeBps       float64
	orderLatencyMs    float64
	maxHoldMin        float64
	monthlyTarget     float64
	targetWinRate     float64
	cloudParams       bool
}

func addDryRunFlags(fs *flag.FlagSet) *dryRunFlags {
	f := &dryRunFlags{}
	fs.StringVar(&f.addr, "addr", ":8080", "listen address for the monitoring dashboard, e.g. :8080")
	fs.StringVar(&f.secretsDir, "secrets-dir", "", "secrets directory (default ~/.config/piquant)")
	fs.StringVar(&f.dataDir, "data", "", "data directory where paper checkpoints live")
	fs.StringVar(&f.logDir, "log-dir", "", "directory holding the service logs")
	fs.BoolVar(&f.noDiscover, "no-discovery", false, "do not announce or search for the peer board")
	fs.StringVar(&f.app, "app", version, "version string shown in the interface")

	fs.Float64Var(&f.capital, "capital", 200.00, "initial paper capital in USDT")
	fs.StringVar(&f.window, "window", "7d", "evaluation window preset: '7d' (last week), '30d' (last month), '14d' (2 weeks)")
	fs.Float64Var(&f.durationH, "duration-hours", 0, "target autonomous duration in hours (0 = infer from --window, e.g. 168h for 7d, 720h for 30d)")
	fs.Float64Var(&f.maxDD, "max-drawdown", 2.0, "maximum allowable drawdown percentage before circuit breaker halts")
	fs.Float64Var(&f.baseLev, "base-leverage", 2.5, "nominal base leverage multiplier (e.g. 2.5 for 2.5x, 3.5 for 3.5x to reach $20/mo on $200)")
	fs.Float64Var(&f.maxLev, "max-leverage", 3.5, "maximum allowable leverage ceiling")
	fs.StringVar(&f.symbols, "symbols", "BTCUSDC,ETHUSDT", "comma-separated trading symbols")
	fs.Float64Var(&f.targetProfitBps, "target-profit", 35.0, "net take-profit target in bps; decays to 80% of this while holding (default 35 -> 28 bps floor)")
	fs.Float64Var(&f.entryOffsetBps, "entry-offset", 2.5, "minimum maker entry distance from mid in bps (default: 2.5 bps = 0.025%)")
	fs.Float64Var(&f.stopLossBps, "stop-loss", 30.0, "stop loss in bps, executed as a taker exit (default 30, matched to the 28 bps target floor)")
	fs.Float64Var(&f.monthlyGainCap, "monthly-cap", 15.0, "maximum target gain percentage per month before entering capital preservation (default: 15.0%)")
	fs.BoolVar(&f.spotOnly, "spot-only", false, "spot-only cash account mode: disable naked shorting, long inventory only (default false)")
	fs.Float64Var(&f.taxRate, "tax-rate", 35.0, "estimated income/capital gains tax rate percentage for after-tax reserve (default: 35.0% for US/CA)")
	fs.BoolVar(&f.zeroFeeMaker, "zero-fee-btc", true, "zero-fee maker tier on BTC pairs (default true for Binance.US)")
	fs.Float64Var(&f.clipFraction, "clip-fraction", 0.45, "fraction of max allocation per quote clip (default: 0.45 = 45% for high capital turnover)")
	fs.BoolVar(&f.enableTaker, "enable-taker", false, "opt-in to tactical taker executions on high-conviction momentum (default false)")
	fs.BoolVar(&f.debug, "debug", false, "enable verbose real-time MFT decision matrix logging and deep telemetry")
	fs.BoolVar(&f.enableNews, "enable-news", true, "enable macro and micro news intelligence pipeline with time-decay & exploit defense (default true)")
	fs.BoolVar(&f.cleanReset, "reset", false, "reset cumulative historical win/loss stats and trade count on start")
	fs.BoolVar(&f.statusOnly, "status", false, "print terminal summary of active dry run and exit")
	fs.BoolVar(&f.reportOnly, "report", false, "generate and print institutional EOD risk & tear-sheet report and exit")
	fs.BoolVar(&f.watch, "watch", false, "attach live updating Bloomberg/HFT Terminal UI (TUI) monitor")
	fs.BoolVar(&f.live, "live", false, "alias for --watch")
	fs.BoolVar(&f.enableFallback, "fallback", false, "opt-in to local synthetic feed generation when offline (default false)")
	fs.BoolVar(&f.enableAllModels, "enable-all-models", false, "re-enable all registered models in ensemble regardless of ablation gate")
	fs.BoolVar(&f.extendOnShutdown, "extend-on-shutdown", true, "automatically extend target end time by offline duration upon shutdown/power-cycle recovery (default true)")
	fs.BoolVar(&f.flattenOnShutdown, "flatten-on-shutdown", true, "automatically liquidate all open positions to cash at current mark price on shutdown (default true)")
	fs.StringVar(&f.wsURL, "ws-url", "", "custom Binance public WebSocket URL (default auto)")
	fs.BoolVar(&f.enableCoinbase, "coinbase", true, "enable Coinbase institutional USD lead-lag signal ingestor (default true)")
	fs.StringVar(&f.coinbaseWS, "coinbase-ws", "", "custom Coinbase public WebSocket URL (default auto)")
	fs.IntVar(&f.expBreaker, "expectancy-breaker", 0, "stop new entries after N closed trades if mean net bps/trade is < 0 at the upper 95% bound (0 = off)")
	fs.StringVar(&f.feedVenue, "feed-venue", "auto", "primary market feed for fills and flow features: 'auto' (coinbase for us-futures, else binance), 'binance' or 'coinbase'")
	fs.StringVar(&f.region, "region", paper.RegionUS, "trading region rules: 'us' (Binance.US spot, 1x, long-only), 'us-futures' (US regulated nano futures, up to 5x, long/short) or 'global'")
	fs.Float64Var(&f.makerFeeBps, "maker-fee", -1, "override maker fee in bps (default: region schedule)")
	fs.Float64Var(&f.takerFeeBps, "taker-fee", -1, "override taker fee in bps (default: region schedule)")
	fs.Float64Var(&f.orderLatencyMs, "order-latency-ms", 0, "override order placement latency in ms (default: region measurement)")
	fs.Float64Var(&f.monthlyTarget, "monthly-target", 7.0, "goal controller monthly return target in % (steers leverage and selectivity; not guaranteed)")
	fs.Float64Var(&f.targetWinRate, "target-win-rate", 60.0, "goal controller target win rate in %")
	fs.BoolVar(&f.cloudParams, "cloud-params", true, "hot-reload optimal_params.json overrides (gamma, leverage, clip) from cloud/backtest tuning")
	fs.Float64Var(&f.maxHoldMin, "max-hold-minutes", 45, "time stop: exit a losing position held longer than this many minutes (0 disables)")
	return f
}

func runDryRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dry-run", flag.ContinueOnError)
	f := addDryRunFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	dDir := f.dataDir
	if dDir == "" {
		dDir = defaultDataDir()
	}

	stateFile := filepath.Join(dDir, "paper", "dryrun_state.json")

	// If --report is requested, print Markdown EOD tear-sheet report and return
	if f.reportOnly {
		return printDailyReport(stateFile, dDir)
	}

	// If --watch or --live is requested, launch live updating Terminal UI (TUI)
	if f.watch || f.live {
		var monitorArgs []string
		if f.dataDir != "" {
			monitorArgs = append(monitorArgs, "--data", f.dataDir)
		}
		return runMonitor(ctx, monitorArgs)
	}

	// If --status is requested, print terminal report and return
	if f.statusOnly {
		return printDryRunStatus(stateFile, f.debug)
	}

	// Exclusive file lock on dataDir prevents concurrent processes from clobbering state and accidental shutdown liquidations
	lockFile := filepath.Join(dDir, "paper", ".dryrun.lock")
	_ = os.MkdirAll(filepath.Dir(lockFile), 0o755)
	fl, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("failed to open lock file %s: %w", lockFile, err)
	}
	defer fl.Close()
	if err := syscall.Flock(int(fl.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another instance of piquant dryrun is already active on %s (use 'piquant top' to monitor)", dDir)
	}
	defer func() {
		_ = syscall.Flock(int(fl.Fd()), syscall.LOCK_UN)
		_ = os.Remove(lockFile)
	}()
	_ = fl.Truncate(0)
	_, _ = fl.WriteString(fmt.Sprintf("%d\n", os.Getpid()))

	store, err := openStore(f.secretsDir)
	if err != nil {
		return err
	}
	instance, _ := discovery.InstanceID(secretsDirOf(f.secretsDir))

	logDir := f.logDir
	if logDir == "" {
		logDir = defaultLogDir()
	}
	if lf, err := openLog(logDir, "dryrun.log"); err == nil {
		defer lf.Close()
	}

	// Setup ring buffer & public Binance market stream ingestor
	ring := engine.NewSPSCRing(4096)

	symList := strings.Split(f.symbols, ",")
	for i := range symList {
		symList[i] = strings.TrimSpace(strings.ToUpper(symList[i]))
	}

	region := strings.ToLower(strings.TrimSpace(f.region))
	if region != paper.RegionUS && region != paper.RegionUSFutures && region != paper.RegionGlobal {
		return fmt.Errorf("unknown --region %q (want us, us-futures or global)", f.region)
	}

	feed, err := resolveFeedVenue(f.feedVenue, region, f.wsURL)
	if err != nil {
		return err
	}
	coinbasePrimary := feed == "coinbase"
	var ingestor *paper.MarketStreamIngestor
	feedEndpoint := paper.DefaultCoinbaseStreamConfig().Endpoint
	if f.coinbaseWS != "" {
		feedEndpoint = f.coinbaseWS
	}
	if !coinbasePrimary {
		streamCfg := paper.DefaultStreamConfig()
		streamCfg.Symbols = symList
		streamCfg.EnableFallback = f.enableFallback
		switch {
		case f.wsURL != "":
			streamCfg.Endpoint = f.wsURL
		case region == paper.RegionUS || region == paper.RegionUSFutures:
			streamCfg.Endpoint = paper.BuildBinanceUSStreamURL(symList)
		default:
			streamCfg.Endpoint = paper.BuildBinanceStreamURL(symList)
		}
		feedEndpoint = streamCfg.Endpoint
		ingestor = paper.NewMarketStreamIngestor(streamCfg, ring)
		ingestor.Start(ctx)
		defer ingestor.Stop()
	}
	fmt.Printf("[FEED] primary market feed: %s\n", feed)

	// Initialize dry run engine with selected window preset
	paperCfg := paper.DefaultDryRunConfig(dDir)
	paperCfg.InitialCapital = f.capital
	paperCfg.WindowPreset = f.window
	if f.durationH > 0 {
		paperCfg.TargetDuration = time.Duration(f.durationH) * time.Hour
	} else {
		switch f.window {
		case "30d", "1m", "month":
			paperCfg.TargetDuration = 30 * 24 * time.Hour
		case "14d", "2w":
			paperCfg.TargetDuration = 14 * 24 * time.Hour
		case "7d", "1w":
			paperCfg.TargetDuration = 7 * 24 * time.Hour
		case "3d":
			paperCfg.TargetDuration = 3 * 24 * time.Hour
		case "1d":
			paperCfg.TargetDuration = 24 * time.Hour
		default:
			paperCfg.TargetDuration = 14 * 24 * time.Hour
		}
	}
	paperCfg.MaxDrawdownPct = f.maxDD
	paperCfg.ExpectancyBreakerMinTrades = f.expBreaker
	if coinbasePrimary {
		paperCfg.VPINBucketUSDK = paper.CoinbaseVPINBucketsUSDK // Binance.US-sized buckets read Coinbase flow as toxic
	}
	if f.baseLev > 0 {
		paperCfg.BaseLeverage = f.baseLev
	}
	if f.maxLev > 0 {
		paperCfg.MaxLeverage = f.maxLev
	}
	paperCfg.Symbols = symList
	if f.targetProfitBps > 0 {
		paperCfg.TargetProfitBps = f.targetProfitBps
	}
	if f.entryOffsetBps > 0 {
		paperCfg.EntryOffsetBps = f.entryOffsetBps
	}
	if f.stopLossBps > 0 {
		paperCfg.StopLossBps = f.stopLossBps
	}
	if f.monthlyGainCap > 0 {
		paperCfg.MonthlyGainCapPct = f.monthlyGainCap
	}
	if f.clipFraction > 0 {
		paperCfg.ClipFraction = f.clipFraction
	}
	paperCfg.SpotOnly = f.spotOnly
	paperCfg.TaxRatePct = f.taxRate
	paperCfg.ZeroFeeMaker = f.zeroFeeMaker
	paperCfg.CleanReset = f.cleanReset
	paperCfg.EnableTaker = f.enableTaker
	paperCfg.Debug = f.debug
	paperCfg.EnableAllModels = f.enableAllModels
	paperCfg.ExtendOnShutdown = f.extendOnShutdown
	paperCfg.FlattenOnShutdown = f.flattenOnShutdown
	paperCfg.EnableNewsPipeline = f.enableNews

	if region == paper.RegionUS && !f.spotOnly {
		fmt.Fprintln(os.Stderr, "note: --region us forces spot-only (US retail spot has no margin or shorting)")
	}
	if err := paper.ApplyRegionProfile(&paperCfg, region); err != nil {
		return err
	}
	if f.makerFeeBps >= 0 {
		paperCfg.MakerFeeBps = f.makerFeeBps
	}
	if f.takerFeeBps >= 0 {
		paperCfg.TakerFeeBps = f.takerFeeBps
	}
	paperCfg.MaxHoldMinutes = f.maxHoldMin
	paperCfg.MonthlyTargetPct = f.monthlyTarget
	paperCfg.TargetWinRatePct = f.targetWinRate
	paperCfg.DisableCloudParams = !f.cloudParams
	if f.orderLatencyMs > 0 {
		paperCfg.OrderLatency = time.Duration(f.orderLatencyMs * float64(time.Millisecond))
	}

	dryRunEngine, err := paper.NewDryRunEngine(paperCfg, ring, ingestor)
	if err != nil {
		return fmt.Errorf("failed to initialize dry run engine: %w", err)
	}
	dryRunEngine.SetAutonomousRecalibrator(backtest.RunAutonomousRecalibration)

	// Live venue latency probe drives order timing and the HFT/MFT/LFT strategy regime
	latMon := paper.NewLatencyMonitor(feedEndpoint, 15*time.Second)
	latMon.Start(ctx)
	dryRunEngine.SetLatencyMonitor(latMon)
	ordMon := paper.NewLatencyMonitor(paper.OrderEntryEndpoint(region), 15*time.Second)
	ordMon.Start(ctx)
	dryRunEngine.SetOrderLatencyMonitor(ordMon)

	// Initialize optional Coinbase institutional USD lead-lag signal ingestor
	if f.enableCoinbase || coinbasePrimary {
		cbCfg := paper.DefaultCoinbaseStreamConfig()
		if f.coinbaseWS != "" {
			cbCfg.Endpoint = f.coinbaseWS
		}
		cbProds := make([]string, 0, len(symList))
		for _, s := range symList {
			if strings.HasPrefix(s, "BTC") {
				cbProds = append(cbProds, "BTC-USD")
			} else if strings.HasPrefix(s, "ETH") {
				cbProds = append(cbProds, "ETH-USD")
			} else if strings.HasPrefix(s, "SOL") {
				cbProds = append(cbProds, "SOL-USD")
			}
		}
		if len(cbProds) > 0 {
			cbCfg.Products = cbProds
		}
		cbOracle := paper.NewCoinbaseSignalOracle(symList)
		// Exactly one producer feeds the SPSC ring: Coinbase when it is the primary feed, otherwise the
		// Binance ingestor (and Coinbase is only the cross-venue lead-lag oracle).
		var cbRing *engine.SPSCRing
		if coinbasePrimary {
			cbRing = ring
		}
		cbIngestor := paper.NewCoinbaseStreamIngestor(cbCfg, cbRing, cbOracle)
		if coinbasePrimary {
			cbIngestor.SetSymbolOrder(symList)
			dryRunEngine.SetMarketFeed(cbIngestor)
		}
		cbIngestor.Start(ctx)
		defer cbIngestor.Stop()

		dryRunEngine.SetCoinbaseOracle(cbOracle)
	}

	dryRunEngine.Start(ctx)
	defer dryRunEngine.Stop()

	// Start Announcer and Browser for zero-config peering
	var browser *discovery.Browser
	if !f.noDiscover {
		b := &discovery.Browser{Token: storeToken(store, secrets.PeerToken), SelfID: instance}
		if err := b.Listen(); err == nil {
			browser = b
			defer b.Close()
		}
	}
	port := parsePort(f.addr)
	if a := startAnnouncer(discovery.RoleEngine, port, storeToken(store, secrets.PeerToken), instance, f.app, browser); a != nil {
		defer a.Stop()
	}

	// Initialize local experiment manager so research engine endpoints work co-located
	_ = os.MkdirAll(dDir, 0o755)
	_ = experiment.GenerateStandardDatasets(dDir)

	expCfg, err := config.Load("configs/production.yaml")
	if err != nil {
		expCfg, _ = config.Load("configs/dev.yaml")
	}
	if expCfg == nil {
		expCfg = &config.Config{
			Symbols: []config.Symbol{
				{Name: "BTC", VenueSym: "BTCUSDT", Venue: "binance"},
				{Name: "ETH", VenueSym: "ETHUSDT", Venue: "binance"},
			},
			Targets: map[string][]string{
				"BTC": {"binance:BTCUSDT"},
				"ETH": {"binance:ETHUSDT"},
			},
		}
	}

	mgr, err := experiment.NewManager(dDir, expCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot initialize experiment manager: %v\n", err)
	}

	// Launch Dashboard Server exposing 2-week paper run, blotter and co-located engine
	srv, err := dashboard.New(dashboard.Options{
		Addr:        f.addr,
		Token:       storeToken(store, secrets.DashboardToken),
		PeerToken:   storeToken(store, secrets.PeerToken),
		Keys:        store,
		LogFiles:    logFiles(logDir),
		DataDirs:    dataDirs(f.dataDir),
		Role:        discovery.RoleEngine,
		App:         f.app,
		Discover:    browser,
		Instance:    instance,
		SessionTTL:  12 * time.Hour,
		ClusterMode: "pi5_only",
		Manager:     mgr,
		DryRun:      dryRunEngine,
	})
	if err != nil {
		return err
	}

	st := dryRunEngine.State()
	targetHours := int(st.TargetEndTime.Sub(st.StartTime).Hours())
	if targetHours <= 0 {
		targetHours = st.TargetDays * 24
	}
	fmt.Printf("\n=======================================================\n")
	fmt.Printf(" [PIQUANT] %d-DAY AUTONOMOUS PAPER DRY RUN INITIATED\n", st.TargetDays)
	fmt.Printf("=======================================================\n")
	fmt.Printf(" Window Preset:   %s (%d Days / %dh)\n", st.WindowPreset, st.TargetDays, targetHours)
	fmt.Printf(" Initial Capital: $%.2f USDT\n", st.InitialCapital)
	fmt.Printf(" Active Universe: %s\n", strings.Join(symList, ", "))
	fmt.Printf(" Region:          %s (feed %s, leverage %.1fx, maker %.2f / taker %.2f bps, latency %s)\n",
		strings.ToUpper(region), feedEndpoint[:strings.Index(feedEndpoint+"?", "?")], paperCfg.MaxLeverage,
		paperCfg.MakerFeeBps, paperCfg.TakerFeeBps, paperCfg.OrderLatency)
	fmt.Printf(" Max Drawdown Limit: %.2f%% (Circuit Breaker)\n", paperCfg.MaxDrawdownPct)
	fmt.Printf(" State Checkpoint: %s\n", paperCfg.StateFile)
	fmt.Printf(" Monitoring Dashboard: http://%s\n", listenHint(f.addr))
	fmt.Printf("=======================================================\n\n")

	return srv.ListenAndServe(ctx)
}

func printDryRunStatus(stateFile string, showDebug ...bool) error {
	isDebug := false
	if len(showDebug) > 0 {
		isDebug = showDebug[0]
	}
	_ = isDebug
	data, err := os.ReadFile(stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("No active 2-week dry run checkpoint found at %s.\n", stateFile)
			fmt.Println("Run 'piquant dry-run' to start the 14-day autonomous paper money run.")
			return nil
		}
		return fmt.Errorf("failed to read state file: %w", err)
	}

	var st paper.DryRunState
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("corrupted state file %s: %w", stateFile, err)
	}

	now := time.Now()
	rem := st.TargetEndTime.Sub(now)
	if rem < 0 {
		rem = 0
	}

	ds := st.DataSource
	if ds == "" {
		ds = "live"
	}
	if st.QuotingPaused {
		ds += " (QUOTING PAUSED)"
	}

	totDays := st.TargetDays
	if totDays <= 0 {
		totDays = 14
	}
	presetName := st.WindowPreset
	if presetName == "" {
		presetName = fmt.Sprintf("%dd", totDays)
	}

	fmt.Println("=======================================================")
	fmt.Printf(" PIQUANT %d-DAY (%s) AUTONOMOUS DRY RUN STATUS REPORT\n", totDays, strings.ToUpper(presetName))
	fmt.Println("=======================================================")
	fmt.Printf(" Status:          %s\n", pick(!st.CircuitHalted, "ACTIVE (OK)", "CIRCUIT BREAKER HALTED"))
	fmt.Printf(" Window Preset:   %s (%d days)\n", presetName, totDays)
	if st.SpotOnly {
		fmt.Printf(" Trading Mode:    SPOT-ONLY (Cash Account - No Margin / No Naked Shorting)\n")
	}
	fmt.Printf(" Data Source:     %s\n", ds)
	fmt.Printf(" Feed Down Time:  %.1fs\n", st.FeedDownSeconds)
	fmt.Printf(" Progress:        Day %d of %d (%.1f hours remaining)\n", st.DaysElapsed, totDays, rem.Hours())
	fmt.Printf(" Start Time:      %s\n", st.StartTime.Format(time.RFC3339))
	fmt.Printf(" Target End:      %s\n", st.TargetEndTime.Format(time.RFC3339))
	fmt.Printf(" Initial Capital: $%.2f USDT\n", st.InitialCapital)
	fmt.Printf(" Current Equity:  $%.2f USDT (%+.2f%%)\n", st.Equity, st.TotalReturnPct)
	if st.TaxRatePct > 0 {
		fmt.Printf(" Tax Reserve:     $%.2f USDT (%.1f%% estimated US/CA tax liability)\n", st.EstimatedTaxUSDT, st.TaxRatePct)
		fmt.Printf(" Post-Tax Equity: $%.2f USDT (Net Return: %+.2f%%)\n", st.AfterTaxEquity, st.AfterTaxReturnPct)
	}
	fmt.Printf(" Cash:            $%.2f USDT\n", st.Cash)
	fmt.Printf(" Realized PnL:    $%.2f USDT\n", st.RealizedPnL)
	fmt.Printf(" Unrealized PnL:  $%.2f USDT\n", st.UnrealizedPnL)
	fmt.Printf(" Peak Equity:     $%.2f USDT\n", st.PeakEquity)
	fmt.Printf(" Max Drawdown:    %.2f%%\n", st.MaxDrawdownPct)
	fmt.Printf(" Total Trades:    %d (Win Rate: %.1f%%)\n", st.TotalTrades, st.WinRate)

	if st.ShutdownCount > 0 {
		fmt.Printf(" Shutdown Recovery: %d resumes (%.1fh compensated downtime)\n", st.ShutdownCount, st.OfflineDowntimeSeconds/3600.0)
		if !st.LastResumeTime.IsZero() {
			fmt.Printf(" Last Resumed:    %s\n", st.LastResumeTime.Format(time.RFC3339))
		}
	}

	if st.CircuitHalted {
		fmt.Printf(" Circuit Halt:    %s\n", st.CircuitHaltReason)
	}
	if g := st.Goal; g.MonthlyTargetPct > 0 {
		fmt.Println("-------------------------------------------------------")
		fmt.Printf(" Goal Controller: %s\n", g.Status)
		fmt.Printf("   Target %.1f%%/mo | Return %+.2f%% vs pace %+.2f%% (gap %+.2f%%) | Projected %+.1f%%/mo\n",
			g.MonthlyTargetPct, g.ReturnPct, g.ExpectedByNowPct, g.PaceGapPct, g.ProjectedMonthlyPct)
		fmt.Printf("   Rolling: %d trades | win %.1f%% (target %.0f%%) | expectancy %+.2f bps\n",
			g.RollingTrades, g.RollingWinRatePct, g.TargetWinRatePct, g.RollingExpectancyBps)
		fmt.Printf("   Adjusters: leverage x%.2f | conviction %+.2f | %d adjustments\n",
			g.LeverageMultiplier, g.ConvictionAdjust, g.Adjustments)
		if g.BenchmarkStartPrice > 0 {
			fmt.Printf("   vs %s buy&hold: %+.2f%% (excess %+.2f%%)\n", g.BenchmarkSymbol, g.BenchmarkReturnPct, g.ExcessReturnPct)
		}
	}
	if l := st.Latency; l.Samples > 0 || l.EffectiveMs > 0 {
		fmt.Printf(" Latency:         feed %s p90 %.1fms | orders %s p90 %.1fms -> reaction %.1fms | regime %s\n",
			l.Host, l.P90Ms, l.OrderHost, l.OrderP90Ms, l.EffectiveMs, l.Mode)
	}

	if len(st.MFTDiagnostics) > 0 {
		fmt.Println("-------------------------------------------------------")
		fmt.Println(" Medium-Frequency Trading (MFT) Decision Matrix:")
		for sym, diag := range st.MFTDiagnostics {
			fmt.Printf("   [%s] Mid: $%.2f | Kalman: $%.2f (Vel: %+.2f bps/s) | Mode: %s\n",
				sym, diag.MidPrice, diag.KalmanPrice, diag.KalmanVelocityBpsPerSec, diag.QuotingMode)
			fmt.Printf("     Horizon Returns: 30s=%+.1fbps 5m=%+.1fbps 15m=%+.1fbps 1h=%+.1fbps | TrendScore: %+.2f\n",
				diag.Ret30sBps, diag.Ret5mBps, diag.Ret15mBps, diag.Ret1hBps, diag.TrendScore)
			fmt.Printf("     Strategy Blend:  Passive=%+.2f OBI=%+.2f Carry=%+.2f Trend=%+.2f -> Alpha: %+.2f\n",
				diag.PassiveScore, diag.OBIMomentumScore, diag.FundingCarryScore, diag.TrendScore, diag.BlendedAlpha)
			fmt.Printf("     Dominant Horizon: %s | Conviction: %.1f%%\n",
				diag.DominantHorizon, diag.DirectionalConviction*100.0)
			fmt.Printf("     Target Inventory: %+.4f (Current: %+.4f, Dev: %+.4f)\n",
				diag.TargetInventory, diag.CurrentInventory, diag.InvDeviation)
			if diag.TrailingStopPrice > 0 {
				fmt.Printf("     Trailing Ratchet: Stop=$%.2f Peak=$%.2f (Dist: %.1f bps)\n",
					diag.TrailingStopPrice, diag.PeakPositionPrice, diag.ActiveStopDistanceBps)
			}
			riskStatus := "PASS"
			if !diag.PreTradeRiskPassed {
				riskStatus = "FAIL (GATED)"
			}
			fmt.Printf("     Microstructure:  VPIN=%.2f Hawkes=%.2f CarryAPR=%.1f%% RiskCheck=%s\n",
				diag.VPIN, diag.HawkesRatio, diag.FundingAPR*100.0, riskStatus)
		}
	}

	fmt.Println("-------------------------------------------------------")
	fmt.Println(" Open Inventory Positions:")
	for sym, pos := range st.Positions {
		stopInfo := ""
		if pos.TrailingStop > 0 {
			stopInfo = fmt.Sprintf(" Stop=$%.2f Peak=$%.2f", pos.TrailingStop, pos.PeakPrice)
		}
		holdInfo := ""
		if pos.HoldingSec > 0 {
			holdInfo = fmt.Sprintf(" Hold=%.0fs", pos.HoldingSec)
		}
		fmt.Printf("   %s: Size=%.4f Entry=$%.2f Mark=$%.2f UnrealizedPnL=$%.2f Trades=%d%s%s\n",
			sym, pos.Size, pos.AvgEntryPrice, pos.MarkPrice, pos.UnrealizedPnL, pos.TotalTrades, stopInfo, holdInfo)
	}
	fmt.Println("=======================================================")
	return nil
}

func printDailyReport(stateFile, dDir string) error {
	var st paper.DryRunState
	if data, err := os.ReadFile(stateFile); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &st)
	} else {
		// Provide default initialized institutional baseline if no state file exists yet
		st = paper.DryRunState{
			StartTime:      time.Now().Add(-24 * time.Hour),
			TargetEndTime:  time.Now().Add(13 * 24 * time.Hour),
			InitialCapital: 100000.00,
			Cash:           100000.00,
			Equity:         100000.00,
			PeakEquity:     100000.00,
			DaysElapsed:    1,
			HoursRemaining: 312.0,
			DataSource:     "live",
		}
	}

	gen := paper.NewDailyReportGenerator()
	now := time.Now()
	report := gen.GenerateReport(st, now)

	// Save to reports dir if possible
	reportsDir := filepath.Join(dDir, "paper", "reports")
	_ = gen.SaveReport(st, reportsDir, now)

	fmt.Print(report)
	return nil
}

// resolveFeedVenue picks the primary market feed. US futures execute on Coinbase Derivatives, so 'auto'
// uses Coinbase spot (same venue family, deep USD flow) instead of Binance.US (<1% of US volume).
func resolveFeedVenue(flag, region, wsURL string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(flag)); v {
	case "binance", "coinbase":
		return v, nil
	case "", "auto":
		if region == paper.RegionUSFutures && wsURL == "" {
			return "coinbase", nil
		}
		return "binance", nil
	default:
		return "", fmt.Errorf("unknown --feed-venue %q (want auto, binance or coinbase)", flag)
	}
}
