package paper

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"piquant-live/internal/engine"
	"piquant-live/internal/experiment"
	"piquant-live/internal/macro"
	"piquant-live/internal/marketdata"
	"piquant-live/internal/models/common"
)

// DryRunConfig configures the 14-day autonomous paper money dry run.
type DryRunConfig struct {
	InitialCapital  float64       // Initial paper money in USDT (default: 100,000.00)
	TargetDuration  time.Duration // Target autonomous duration (default: 14 * 24 * time.Hour = 336h)
	Symbols         []string      // Instruments traded (default: BTCUSDT, ETHUSDT)
	MaxDrawdownPct  float64       // Max allowable drawdown % before circuit breaker halts (default: 2.0%)
	MakerFeeBps     float64       // Maker fee/rebate in bps (default: 0.0)
	TakerFeeBps     float64       // Taker fee in bps (default: 4.0)
	KylesLambda     float64       // Slippage coefficient on taker orders (bps per unit volume, default: 0.25)
	StateFile       string        // Path to checkpoint state (default: data/paper/dryrun_state.json)
	JournalFile     string        // Path to trade journal (default: data/paper/dryrun_journal.jsonl)
	CheckpointFreq  time.Duration // Interval to save state (default: 5 * time.Second)
	OrderLatency     time.Duration // Feed + order placement latency before maker order is active (default: 50ms)
	EnableAllModels   bool          // Re-enable models disabled by ablation gate in the ensemble (default: false)
	ExtendOnShutdown  bool          // Automatically extend target end time by offline duration upon shutdown/power-cycle recovery (default: true)
	FlattenOnShutdown bool          // Automatically liquidate all open positions to cash at current mark price on shutdown (default: true)
	WindowPreset      string        // Window preset: "7d" (last week), "30d" (last month), "14d" (2 weeks)
	BaseLeverage      float64       // Nominal base leverage multiplier (default: 2.5x)
	MaxLeverage       float64       // Maximum allowable leverage ceiling (default: 5.0x)
	EnableMacroShield bool          // Zero-cost macro calendar event shield (default: true)
	MacroSyncURL      string        // Optional custom URL for macro calendar sync (default: ForexFactory free feed)
	DataDir           string        // Root data directory (default: "data")
}

// DefaultDryRunConfig returns standard institutional configuration for the 2-week run.
func DefaultDryRunConfig(dataDir string) DryRunConfig {
	if dataDir == "" {
		dataDir = "data"
	}
	paperDir := filepath.Join(dataDir, "paper")
	return DryRunConfig{
		InitialCapital:    200.00,
		TargetDuration:    14 * 24 * time.Hour,
		Symbols:           []string{"BTCUSDT", "ETHUSDT"},
		MaxDrawdownPct:    2.0,
		MakerFeeBps:       1.5, // Realistic Binance Spot/Futures retail maker fee (1.5 bps = 0.015% cost per fill)
		TakerFeeBps:       4.0,
		KylesLambda:       0.25,
		StateFile:         filepath.Join(paperDir, "dryrun_state.json"),
		JournalFile:       filepath.Join(paperDir, "dryrun_journal.jsonl"),
		CheckpointFreq:    5 * time.Second,
		OrderLatency:      120 * time.Millisecond, // Realistic US-to-Tokyo exchange fiber latency (120ms)
		ExtendOnShutdown:  true,
		FlattenOnShutdown: true,
		BaseLeverage:      2.5,
		MaxLeverage:       5.0,
		EnableMacroShield: true,
		DataDir:           dataDir,
	}
}

// Position tracks an individual symbol's inventory and execution metrics.
type Position struct {
	Symbol          string  `json:"symbol"`
	Size            float64 `json:"size"`              // Quantity (positive: long, negative: short)
	AvgEntryPrice   float64 `json:"avg_entry_price"`   // Volume-weighted entry price
	MarkPrice       float64 `json:"mark_price"`        // Latest mid price
	UnrealizedPnL   float64 `json:"unrealized_pnl"`    // Mark-to-market PnL
	RealizedPnL     float64 `json:"realized_pnl"`      // Cumulative realized PnL
	TotalTrades     int64   `json:"total_trades"`      // Total completed fills
	MakerTrades     int64   `json:"maker_trades"`      // Maker fills
	TakerTrades     int64   `json:"taker_trades"`      // Taker fills
	TotalVolumeUSDT float64 `json:"total_volume_usdt"` // Cumulative traded notional
}

// TradeRecord represents an individual executed paper trade for the journal.
type TradeRecord struct {
	Timestamp   time.Time `json:"timestamp"`
	Symbol      string    `json:"symbol"`
	Side        string    `json:"side"` // "BUY" or "SELL"
	Role        string    `json:"role"` // "MAKER" or "TAKER"
	Price       float64   `json:"price"`
	Size        float64   `json:"size"`
	ValueUSDT   float64   `json:"value_usdt"`
	FeeUSDT     float64   `json:"fee_usdt"`
	EdgeBps     float64   `json:"edge_bps"`
	SlippageBps float64   `json:"slippage_bps"`
	PnLRealized float64   `json:"pnl_realized"`
	Strategy    string    `json:"strategy"`
	DataSource  string    `json:"data_source"` // "live" or "fallback"
}

// ActiveMakerOrder tracks an active resting limit maker order in the paper book.
type ActiveMakerOrder struct {
	Symbol     string    `json:"symbol"`
	Side       string    `json:"side"` // "BUY" or "SELL"
	Price      float64   `json:"price"`
	Size       float64   `json:"size"`
	PlacedAt   time.Time `json:"placed_at"`
	ActiveAt   time.Time `json:"active_at"` // PlacedAt + OrderLatency
	QueueAhead float64   `json:"queue_ahead"`
	Strategy   string    `json:"strategy"`
	EdgeBps    float64   `json:"edge_bps"`
}

// DryRunState is the checkpointable state of the 14-day paper trading engine.
type DryRunState struct {
	StartTime         time.Time            `json:"start_time"`
	TargetEndTime     time.Time            `json:"target_end_time"`
	LastCheckpoint    time.Time            `json:"last_checkpoint"`
	InitialCapital    float64              `json:"initial_capital"`
	Cash              float64              `json:"cash"`
	Equity            float64              `json:"equity"`
	PeakEquity        float64              `json:"peak_equity"`
	RealizedPnL       float64              `json:"realized_pnl"`
	UnrealizedPnL     float64              `json:"unrealized_pnl"`
	TotalReturnPct    float64              `json:"total_return_pct"`
	MaxDrawdownPct    float64              `json:"max_drawdown_pct"`
	CircuitHalted     bool                 `json:"circuit_halted"`
	CircuitHaltReason string               `json:"circuit_halt_reason"`
	TotalTrades       int64                `json:"total_trades"`
	WinningTrades     int64                `json:"winning_trades"`
	LosingTrades      int64                `json:"losing_trades"`
	WinRate           float64              `json:"win_rate"`
	SharpeRatio       float64              `json:"sharpe_ratio"`
	Positions         map[string]*Position `json:"positions"`
	DailyReturns      []float64            `json:"daily_returns"`
	DaysElapsed       int                  `json:"days_elapsed"`
	TargetDays        int                  `json:"target_days"`              // Total days in target evaluation window (e.g. 7, 14, 30)
	WindowPreset      string               `json:"window_preset"`            // "7d" (last week), "30d" (last month), "14d" (2 weeks)
	HoursRemaining    float64              `json:"hours_remaining"`
	DataSource             string               `json:"data_source"`              // "live" or "fallback"
	FeedDownSeconds        float64              `json:"feed_down_seconds"`        // Cumulative duration market feed was disconnected
	QuotingPaused          bool                 `json:"quoting_paused"`           // True when quoting/matching paused due to feed down
	OfflineDowntimeSeconds float64              `json:"offline_downtime_seconds"` // Cumulative time system was shut down / offline
	ShutdownCount          int                  `json:"shutdown_count"`           // Number of power-cycles/resumptions survived
	LastResumeTime         time.Time                     `json:"last_resume_time"`         // Timestamp of latest recovery after shutdown
	Leverage               DynamicLeverageState          `json:"leverage"`                 // Real-time automatic dynamic leverage and regime
	Macro                  macro.MacroState              `json:"macro"`                    // Zero-cost macro calendar event shield telemetry
	ActiveOrders           map[string][]ActiveMakerOrder `json:"active_orders,omitempty"`   // Currently resting maker limit orders in the book
}

// SymbolMicrostructure maintains isolated per-symbol order-book dynamics,
// 16-feature streaming extraction, VPIN toxicity, Kyle's Lambda, Hawkes intensity,
// and MicrostructureCircuitBreaker so multi-asset price/depth scales never cross-contaminate.
type SymbolMicrostructure struct {
	Symbol       string
	CircuitBrk   *experiment.MicrostructureCircuitBreaker
	VpinCalc     *common.VPINCalculator
	KyleEst      *common.KylesLambdaEstimator
	HawkesEst    *common.OnlineHawkesEstimator
	AdaHedge     *common.AdaHedgeEnsemble
	PrevPreds    [common.MaxEnsembleExperts]float64
	HasPrevPreds bool

	PrevBids    [10]marketdata.PriceLevel
	PrevAsks    [10]marketdata.PriceLevel
	NumPrevBids int
	NumPrevAsks int
	HasPrevBook bool

	PrevMid     float64
	Ofi1s       float64
	Ofi5s       float64
	Ofi30s      float64
	Mlofi1s     float64
	Mlofi5s     float64
	Ret1sBps    float64
	Ret5sBps    float64
	VolVarBps      float64
	TradeBuy5s     float64
	TradeSell5s    float64
	TradeRate1s    float64
	InventorySince time.Time
}

func newSymbolMicrostructure(symbol string, kylesLambda float64) *SymbolMicrostructure {
	if kylesLambda <= 0 {
		kylesLambda = 0.25
	}
	return &SymbolMicrostructure{
		Symbol:      symbol,
		CircuitBrk:  experiment.NewMicrostructureCircuitBreaker(1.0),
		VpinCalc:    common.NewVPINCalculator(25.0, 50), // $25k notional volume buckets
		KyleEst:     common.NewKylesLambdaEstimator(0.05, kylesLambda),
		HawkesEst:   common.NewOnlineHawkesEstimator(5.0, 12.0, 20.0),
		AdaHedge:    common.NewAdaHedgeEnsemble(7, 0.985),
		VolVarBps:   0.15,
		TradeRate1s: 4.0,
	}
}

// DryRunEngine orchestrates the 14-day autonomous dry run.
type DryRunEngine struct {
	cfg     DryRunConfig
	ring    *engine.SPSCRing
	stream  *MarketStreamIngestor
	journal *os.File

	mu    sync.RWMutex
	state DryRunState

	// Resting passive maker orders and isolated microstructure state per symbol
	activeOrders map[string][]*ActiveMakerOrder
	symMicro     map[string]*SymbolMicrostructure
	lastTickTime time.Time

	// Models & Quantitative guards
	conformal     *common.SplitConformalCalibrator
	glftMM        *common.GLFTMarketMaker
	carteaMM      *common.CarteaJaimungalMM
	adaHedge      *common.AdaHedgeEnsemble
	circuitBrk    *experiment.MicrostructureCircuitBreaker
	driftGuard    *experiment.PageHinkleyDriftGuard
	vpinCalc      *common.VPINCalculator
	riskSafeguard *CircuitBreaker
	kyleEst       *common.KylesLambdaEstimator
	tcaTracker    *TCAMarkoutTracker
	sor           common.SmartOrderRouter
	hawkesEst     *common.OnlineHawkesEstimator
	leverageCtrl  *DynamicLeverageController
	macroGuardian *macro.MacroGuardian

	// Online tick tracking & autonomous cloud parameter hot-reload
	minHalfSpreadFloorBps float64
	lastCloudParamMod     time.Time
	recentFills           []TradeRecord
	running               atomic.Bool
	stopCh                chan struct{}
	wg                    sync.WaitGroup
}

// NewDryRunEngine constructs and recovers or initializes the paper trading engine.
func NewDryRunEngine(cfg DryRunConfig, ring *engine.SPSCRing, stream *MarketStreamIngestor) (*DryRunEngine, error) {
	if cfg.InitialCapital <= 0 {
		cfg.InitialCapital = 1000.00
	}
	switch strings.ToLower(strings.TrimSpace(cfg.WindowPreset)) {
	case "7d", "1w", "week", "last_week", "last-week":
		cfg.TargetDuration = 7 * 24 * time.Hour
		cfg.WindowPreset = "7d"
	case "30d", "1m", "month", "last_month", "last-month":
		cfg.TargetDuration = 30 * 24 * time.Hour
		cfg.WindowPreset = "30d"
	case "14d", "2w", "2week", "two_weeks":
		cfg.TargetDuration = 14 * 24 * time.Hour
		cfg.WindowPreset = "14d"
	default:
		if cfg.TargetDuration <= 0 {
			cfg.TargetDuration = 14 * 24 * time.Hour
		}
		if cfg.WindowPreset == "" {
			if cfg.TargetDuration == 7*24*time.Hour {
				cfg.WindowPreset = "7d"
			} else if cfg.TargetDuration == 30*24*time.Hour {
				cfg.WindowPreset = "30d"
			} else {
				cfg.WindowPreset = "14d"
			}
		}
	}
	if cfg.MaxDrawdownPct <= 0 {
		cfg.MaxDrawdownPct = 2.0
	}
	if len(cfg.Symbols) == 0 {
		cfg.Symbols = []string{"BTCUSDT", "ETHUSDT"}
	}
	if cfg.KylesLambda <= 0 {
		cfg.KylesLambda = 0.25
	}
	if cfg.CheckpointFreq <= 0 {
		cfg.CheckpointFreq = 5 * time.Second
	}

	if cfg.OrderLatency <= 0 {
		cfg.OrderLatency = 50 * time.Millisecond
	}

	levCfg := DefaultDynamicLeverageConfig()
	levCfg.MaxDrawdownPct = cfg.MaxDrawdownPct
	if cfg.BaseLeverage > 0 {
		levCfg.BaseLeverage = cfg.BaseLeverage
	}
	if cfg.MaxLeverage > 0 {
		levCfg.MaxLeverage = cfg.MaxLeverage
	}
	levCtrl := NewDynamicLeverageController(levCfg)

	symMicro := make(map[string]*SymbolMicrostructure, len(cfg.Symbols))
	for _, s := range cfg.Symbols {
		upper := strings.ToUpper(s)
		symMicro[upper] = newSymbolMicrostructure(upper, cfg.KylesLambda)
	}

	e := &DryRunEngine{
		cfg:                   cfg,
		ring:                  ring,
		stream:                stream,
		stopCh:                make(chan struct{}),
		conformal:             common.NewSplitConformalCalibrator(0.10),
		glftMM:                common.NewGLFTMarketMaker(1e-3, 0.05, cfg.MakerFeeBps),
		carteaMM:              common.NewCarteaJaimungalMM(0.05, 1.5, 2.0, 0.001, 0.01, 0.5, 60.0),
		adaHedge:              common.NewAdaHedgeEnsemble(7, 0.985),
		circuitBrk:            experiment.NewMicrostructureCircuitBreaker(1.0),
		driftGuard:            experiment.NewPageHinkleyDriftGuard(0.15, 12.0),
		vpinCalc:              common.NewVPINCalculator(5.0, 50),
		riskSafeguard:         NewCircuitBreaker(DefaultCircuitBreakerConfig()),
		kyleEst:               common.NewKylesLambdaEstimator(0.05, cfg.KylesLambda),
		tcaTracker:            NewTCAMarkoutTracker(),
		sor:                   common.NewSmartOrderRouter(),
		hawkesEst:             common.NewOnlineHawkesEstimator(5.0, 12.0, 20.0),
		leverageCtrl:          levCtrl,
		activeOrders:          make(map[string][]*ActiveMakerOrder),
		symMicro:              symMicro,
		minHalfSpreadFloorBps: 0.25,
		recentFills:           make([]TradeRecord, 0, 32),
	}
	if cfg.EnableMacroShield {
		dDir := cfg.DataDir
		if dDir == "" {
			dDir = filepath.Dir(filepath.Dir(cfg.StateFile))
		}
		e.macroGuardian = macro.NewMacroGuardian(dDir, cfg.MacroSyncURL)
	}
	e.maybeReloadCloudParams()

	// Attempt to restore existing state from checkpoint
	if err := e.loadCheckpoint(); err != nil {
		// Secondary persistence fallback: reconstruct complete state from append-only trade journal
		if recState, jErr := e.recoverStateFromJournal(); jErr == nil && recState != nil {
			e.state = *recState
			_ = e.saveCheckpoint() // Re-persist reconstructed clean checkpoint to disk
		} else {
			// Initialize fresh state if neither checkpoint nor journal exists
			now := time.Now()
			targetDays := int(cfg.TargetDuration.Hours() / 24.0)
			if targetDays <= 0 {
				targetDays = 14
			}
			e.state = DryRunState{
				StartTime:      now,
				TargetEndTime:  now.Add(cfg.TargetDuration),
				LastCheckpoint: now,
				InitialCapital: cfg.InitialCapital,
				Cash:           cfg.InitialCapital,
				Equity:         cfg.InitialCapital,
				PeakEquity:     cfg.InitialCapital,
				Positions:      make(map[string]*Position, len(cfg.Symbols)),
				DailyReturns:   make([]float64, 0, targetDays),
				TargetDays:     targetDays,
				WindowPreset:   cfg.WindowPreset,
				DataSource:     "live",
				Leverage:       levCtrl.Compute(10.0, 0.20, 0.50, 0.0, false),
			}
			for _, s := range cfg.Symbols {
				upper := strings.ToUpper(s)
				e.state.Positions[upper] = &Position{
					Symbol: upper,
				}
			}
		}
	} else {
		if e.state.DataSource == "" {
			e.state.DataSource = "live"
		}
		if e.state.TargetDays <= 0 {
			e.state.TargetDays = int(cfg.TargetDuration.Hours() / 24.0)
			if e.state.TargetDays <= 0 {
				e.state.TargetDays = 14
			}
		}
		if e.state.WindowPreset == "" {
			e.state.WindowPreset = cfg.WindowPreset
		}
	}

	// Ensure journal directory exists
	if cfg.JournalFile != "" {
		_ = os.MkdirAll(filepath.Dir(cfg.JournalFile), 0o755)
		f, err := os.OpenFile(cfg.JournalFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, fmt.Errorf("failed to open dry run journal %s: %w", cfg.JournalFile, err)
		}
		e.journal = f
	}

	if e.macroGuardian != nil {
		e.state.Macro = e.macroGuardian.Evaluate(time.Now())
	}

	return e, nil
}

// Start launches the strategy evaluation loop and periodic state checkpointing.
func (e *DryRunEngine) Start(ctx context.Context) {
	if e.running.Swap(true) {
		return
	}

	if e.macroGuardian != nil {
		e.macroGuardian.Start(ctx)
	}

	e.wg.Add(2)
	go e.runExecutionLoop(ctx)
	go e.runCheckpointLoop(ctx)
}

// Stop terminates the engine, cancels all resting orders, optionally auto-liquidates open inventory to cash, and checkpoints state.
func (e *DryRunEngine) Stop() {
	if !e.running.Swap(false) {
		return
	}
	close(e.stopCh)
	e.wg.Wait()

	if e.macroGuardian != nil {
		e.macroGuardian.Stop()
	}

	e.mu.Lock()
	// 1. Cancel all resting maker orders on shutdown
	for s := range e.activeOrders {
		e.activeOrders[s] = nil
	}

	// 2. Auto TS (Trailing Stop / Liquidation) on Shutdown:
	// If configured (default true), close any open inventory at current MarkPrice to convert all unrealized PnL to cash
	if e.cfg.FlattenOnShutdown {
		for sym, pos := range e.state.Positions {
			if pos != nil && pos.Size != 0 && pos.MarkPrice > 0 {
				closePrice := pos.MarkPrice
				qty := math.Abs(pos.Size)
				valUSDT := closePrice * qty
				realizedPnL := 0.0

				if pos.Size > 0 { // Long position liquidated
					realizedPnL = pos.Size * (closePrice - pos.AvgEntryPrice)
				} else { // Short position covered
					realizedPnL = (-pos.Size) * (pos.AvgEntryPrice - closePrice)
				}
				e.state.Cash += realizedPnL

				pos.RealizedPnL += realizedPnL
				pos.UnrealizedPnL = 0
				pos.Size = 0
				pos.TotalTrades++

				e.state.RealizedPnL += realizedPnL
				e.state.TotalTrades++
				if realizedPnL > 0 {
					e.state.WinningTrades++
				} else if realizedPnL < 0 {
					e.state.LosingTrades++
				}

				// Record liquidation fill in journal/blotter
				fillRec := TradeRecord{
					Timestamp:   time.Now().UTC(),
					Symbol:      sym,
					Side:        "SHUTDOWN_AUTO_LIQUIDATE",
					Role:        "TAKER_SAFETY",
					Price:       closePrice,
					Size:        qty,
					ValueUSDT:   valUSDT,
					FeeUSDT:     0.0,
					EdgeBps:     0.0,
					SlippageBps: 0.0,
					PnLRealized: realizedPnL,
					Strategy:    "Shutdown_Auto_TS_Flatten",
					DataSource:  "live",
				}
				e.recentFills = append([]TradeRecord{fillRec}, e.recentFills...)
				if len(e.recentFills) > 64 {
					e.recentFills = e.recentFills[:64]
				}
				if e.journal != nil {
					if b, err := json.Marshal(fillRec); err == nil {
						_, _ = e.journal.Write(append(b, '\n'))
					}
				}
			}
		}
		// Reset unrealized PnL and lock equity into cash
		e.state.UnrealizedPnL = 0
		e.state.Equity = e.state.Cash
	}
	e.mu.Unlock()

	_ = e.saveCheckpoint()
	if e.journal != nil {
		_ = e.journal.Close()
	}
}

// State returns a thread-safe snapshot of the current 14-day dry run state.
func (e *DryRunEngine) State() DryRunState {
	e.mu.RLock()
	defer e.mu.RUnlock()

	cp := e.state
	// Deep copy positions
	cp.Positions = make(map[string]*Position, len(e.state.Positions))
	for k, v := range e.state.Positions {
		posCopy := *v
		cp.Positions[k] = &posCopy
	}
	cp.DailyReturns = append([]float64(nil), e.state.DailyReturns...)

	// Deep copy active resting maker orders for UI & telemetry
	cp.ActiveOrders = make(map[string][]ActiveMakerOrder, len(e.activeOrders))
	for sym, ords := range e.activeOrders {
		if len(ords) > 0 {
			copies := make([]ActiveMakerOrder, len(ords))
			for i, o := range ords {
				copies[i] = *o
			}
			cp.ActiveOrders[sym] = copies
		}
	}

	// Update computed timing fields
	now := time.Now()
	elapsed := now.Sub(cp.StartTime)
	totalDays := cp.TargetDays
	if totalDays <= 0 {
		totalDays = int(e.cfg.TargetDuration.Hours() / 24.0)
		if totalDays <= 0 {
			totalDays = 14
		}
		cp.TargetDays = totalDays
	}
	if cp.WindowPreset == "" {
		cp.WindowPreset = e.cfg.WindowPreset
	}
	cp.DaysElapsed = int(elapsed.Hours()/24.0) + 1
	if cp.DaysElapsed > totalDays {
		cp.DaysElapsed = totalDays
	}
	remaining := cp.TargetEndTime.Sub(now)
	if remaining < 0 {
		remaining = 0
	}
	cp.HoursRemaining = remaining.Hours()

	if cp.Leverage.EffectiveLeverage == 0 && e.leverageCtrl != nil {
		cp.Leverage = e.leverageCtrl.Compute(10.0, 0.20, 0.50, 0.0, cp.CircuitHalted)
	}

	if cp.Macro.PhaseName == "" && e.macroGuardian != nil {
		cp.Macro = e.macroGuardian.Evaluate(now)
	}

	return cp
}

// RecentFills returns the most recent executed trades for the blotter.
func (e *DryRunEngine) RecentFills() []TradeRecord {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]TradeRecord, len(e.recentFills))
	copy(out, e.recentFills)
	return out
}

// StreamStats returns real-time event counts from the public WebSocket ingestor.
func (e *DryRunEngine) StreamStats() (total, depths, trades, gaps, reconnects, fallbacks uint64, lastNs int64) {
	if e.stream != nil {
		return e.stream.Stats()
	}
	return 0, 0, 0, 0, 0, 0, 0
}

// runCheckpointLoop periodically persists state to dryrun_state.json and hot-reloads cloud optimal params.
func (e *DryRunEngine) runCheckpointLoop(ctx context.Context) {
	defer e.wg.Done()
	ticker := time.NewTicker(e.cfg.CheckpointFreq)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case <-ticker.C:
			e.maybeReloadCloudParams()
			_ = e.saveCheckpoint()
		}
	}
}

// cloudOptimalParams represents the JSON schema written by cloud/parallel grid search optimization.
type cloudOptimalParams struct {
	Gamma            float64 `json:"gamma"`
	BaseLeverage     float64 `json:"base_leverage"`
	MaxLeverageCap   float64 `json:"max_leverage_cap"`
	MinHalfSpreadBps float64 `json:"min_half_spread_bps"`
	ClipFraction     float64 `json:"clip_fraction"`
}

// maybeReloadCloudParams hot-reloads optimal parameters from cloud optimization without interrupting trading.
func (e *DryRunEngine) maybeReloadCloudParams() {
	candidates := []string{"/home/everett/piquant-data/config/optimal_params.json"}
	if e.cfg.StateFile != "" {
		candidates = append(candidates,
			filepath.Join(filepath.Dir(e.cfg.StateFile), "../config/optimal_params.json"),
			filepath.Join(filepath.Dir(e.cfg.StateFile), "config/optimal_params.json"),
			filepath.Join(filepath.Dir(e.cfg.StateFile), "optimal_params.json"),
		)
	}

	var targetPath string
	var modTime time.Time
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			targetPath = p
			modTime = info.ModTime()
			break
		}
	}
	if targetPath == "" {
		return
	}

	e.mu.RLock()
	lastMod := e.lastCloudParamMod
	e.mu.RUnlock()
	if !modTime.After(lastMod) {
		return
	}

	raw, err := os.ReadFile(targetPath)
	if err != nil {
		return
	}
	var params cloudOptimalParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return
	}

	e.mu.Lock()
	e.lastCloudParamMod = modTime
	if params.Gamma > 0 && e.glftMM != nil {
		e.glftMM.Gamma = params.Gamma
	}
	if params.MinHalfSpreadBps > 0 {
		e.minHalfSpreadFloorBps = params.MinHalfSpreadBps
	}
	if e.leverageCtrl != nil {
		e.leverageCtrl.UpdateCloudParams(params.BaseLeverage, params.MaxLeverageCap, params.ClipFraction)
	}
	e.mu.Unlock()
}

// saveCheckpoint serializes DryRunState to JSON.
func (e *DryRunEngine) saveCheckpoint() error {
	e.mu.Lock()
	now := time.Now()
	e.state.LastCheckpoint = now

	// Protect against backward time jump before NTP sync on Raspberry Pi
	if !e.state.StartTime.IsZero() && now.After(e.state.StartTime) {
		elapsed := now.Sub(e.state.StartTime)
		maxDays := e.state.TargetDays
		if maxDays <= 0 {
			maxDays = int(e.cfg.TargetDuration.Hours() / 24.0)
			if maxDays <= 0 {
				maxDays = 14
			}
			e.state.TargetDays = maxDays
		}
		e.state.DaysElapsed = int(elapsed.Hours()/24.0) + 1
		if e.state.DaysElapsed > maxDays {
			e.state.DaysElapsed = maxDays
		}
		rem := e.state.TargetEndTime.Sub(now)
		if rem < 0 {
			rem = 0
		}
		e.state.HoursRemaining = rem.Hours()

		// Daily returns tracking at day rollover
		dayIndex := int(elapsed.Hours() / 24.0)
		if dayIndex > len(e.state.DailyReturns) && dayIndex <= maxDays {
			prevEquity := e.state.InitialCapital
			if len(e.state.DailyReturns) > 0 {
				eq := e.state.InitialCapital
				for _, r := range e.state.DailyReturns {
					eq *= (1.0 + r/100.0)
				}
				prevEquity = eq
			}
			if prevEquity > 0 {
				dayRet := ((e.state.Equity - prevEquity) / prevEquity) * 100.0
				e.state.DailyReturns = append(e.state.DailyReturns, dayRet)
			}
		}

		// Recompute continuous annualized Sharpe ratio if daily returns are recorded
		if len(e.state.DailyReturns) >= 2 {
			n := float64(len(e.state.DailyReturns))
			var sum, sumSq float64
			for _, r := range e.state.DailyReturns {
				sum += r
				sumSq += r * r
			}
			mean := sum / n
			variance := sumSq/n - mean*mean
			if variance > 1e-12 {
				e.state.SharpeRatio = (mean / math.Sqrt(variance)) * math.Sqrt(365.0)
			}
		}
	}

	// Capture active resting orders snapshot into checkpoint
	e.state.ActiveOrders = make(map[string][]ActiveMakerOrder, len(e.activeOrders))
	for sym, ords := range e.activeOrders {
		if len(ords) > 0 {
			copies := make([]ActiveMakerOrder, len(ords))
			for i, o := range ords {
				copies[i] = *o
			}
			e.state.ActiveOrders[sym] = copies
		}
	}

	if e.macroGuardian != nil {
		e.state.Macro = e.macroGuardian.Evaluate(now)
	}

	data, err := json.MarshalIndent(e.state, "", "  ")
	e.mu.Unlock()
	if err != nil {
		return err
	}

	if e.cfg.StateFile == "" {
		return nil
	}

	_ = os.MkdirAll(filepath.Dir(e.cfg.StateFile), 0o755)
	tmpFile := e.cfg.StateFile + ".tmp"
	f, err := os.OpenFile(tmpFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	// Flush kernel page cache to physical storage (SD card / NVMe) to prevent 0x00 null byte corruption on power loss
	_ = f.Sync()
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmpFile, e.cfg.StateFile)
}

// loadCheckpoint deserializes state from JSON and handles shutdown gap recovery.
func (e *DryRunEngine) loadCheckpoint() error {
	if e.cfg.StateFile == "" {
		return os.ErrNotExist
	}
	data, err := os.ReadFile(e.cfg.StateFile)
	if err != nil {
		return err
	}

	cleanData := bytes.Trim(data, "\x00\r\n \t")
	if len(cleanData) == 0 {
		// File was zeroed out by power-loss before dirty pages reached flash
		_ = os.Rename(e.cfg.StateFile, e.cfg.StateFile+".corrupted")
		return os.ErrNotExist
	}

	var st DryRunState
	if err := json.Unmarshal(cleanData, &st); err != nil {
		_ = os.Rename(e.cfg.StateFile, e.cfg.StateFile+".corrupted")
		return os.ErrNotExist
	}

	if st.Positions == nil {
		st.Positions = make(map[string]*Position)
	}

	now := time.Now()
	// Detect shutdown gap and recover seamlessly
	if !st.LastCheckpoint.IsZero() {
		if now.Before(st.LastCheckpoint) {
			// System clock warped backwards (Raspberry Pi without RTC booted before NTP sync).
			// Keep existing target end time and hours remaining intact until NTP catches up.
		} else {
			offlineGap := now.Sub(st.LastCheckpoint)
			// If offline for more than 30s (e.g. 12-hour power-off, reboot, or transport):
			if offlineGap > 30*time.Second {
				st.OfflineDowntimeSeconds += offlineGap.Seconds()
				st.FeedDownSeconds += offlineGap.Seconds()
				st.ShutdownCount++
				st.LastResumeTime = now

				// If configured (default true), extend target end time by the offline duration
				// so the user gets the FULL 336 hours of active live market evaluation!
				if e.cfg.ExtendOnShutdown && !st.TargetEndTime.IsZero() {
					st.TargetEndTime = st.TargetEndTime.Add(offlineGap)
				}
				rem := st.TargetEndTime.Sub(now)
				if rem < 0 {
					rem = 0
				}
				st.HoursRemaining = rem.Hours()
			}
		}
	}

	e.mu.Lock()
	e.state = st
	e.mu.Unlock()

	// Pre-load recent fills from journal tail so in-memory blotter is warm on startup
	if e.cfg.JournalFile != "" {
		if jf, err := os.Open(e.cfg.JournalFile); err == nil {
			defer jf.Close()
			scanner := bufio.NewScanner(jf)
			var rFills []TradeRecord
			for scanner.Scan() {
				line := bytes.Trim(scanner.Bytes(), "\x00\r\n \t")
				if len(line) == 0 {
					continue
				}
				var rec TradeRecord
				if err := json.Unmarshal(line, &rec); err == nil && rec.Symbol != "" {
					rFills = append(rFills, rec)
					if len(rFills) > 60 {
						rFills = rFills[len(rFills)-30:]
					}
				}
			}
			if len(rFills) > 30 {
				rFills = rFills[len(rFills)-30:]
			}
			e.mu.Lock()
			e.recentFills = rFills
			e.mu.Unlock()
		}
	}

	return nil
}

// recoverStateFromJournal replays dryrun_journal.jsonl to reconstruct complete state if checkpoint was lost or corrupted.
func (e *DryRunEngine) recoverStateFromJournal() (*DryRunState, error) {
	if e.cfg.JournalFile == "" {
		return nil, os.ErrNotExist
	}
	f, err := os.Open(e.cfg.JournalFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var records []TradeRecord
	for scanner.Scan() {
		line := bytes.Trim(scanner.Bytes(), "\x00\r\n \t")
		if len(line) == 0 {
			continue
		}
		var rec TradeRecord
		if err := json.Unmarshal(line, &rec); err == nil && rec.Symbol != "" {
			records = append(records, rec)
		}
	}
	if len(records) == 0 {
		return nil, os.ErrNotExist
	}

	targetDays := int(e.cfg.TargetDuration.Hours() / 24.0)
	if targetDays <= 0 {
		targetDays = 14
	}

	st := &DryRunState{
		StartTime:      records[0].Timestamp,
		TargetEndTime:  records[0].Timestamp.Add(e.cfg.TargetDuration),
		LastCheckpoint: records[len(records)-1].Timestamp,
		InitialCapital: e.cfg.InitialCapital,
		Cash:           e.cfg.InitialCapital,
		Equity:         e.cfg.InitialCapital,
		PeakEquity:     e.cfg.InitialCapital,
		Positions:      make(map[string]*Position, len(e.cfg.Symbols)),
		DailyReturns:   make([]float64, 0, targetDays),
		TargetDays:     targetDays,
		WindowPreset:   e.cfg.WindowPreset,
		DataSource:     "live",
	}

	for _, s := range e.cfg.Symbols {
		u := strings.ToUpper(s)
		st.Positions[u] = &Position{Symbol: u}
	}

	for _, r := range records {
		pos := st.Positions[r.Symbol]
		if pos == nil {
			pos = &Position{Symbol: r.Symbol}
			st.Positions[r.Symbol] = pos
		}

		st.Cash += r.PnLRealized
		st.RealizedPnL += r.PnLRealized
		st.TotalTrades++
		pos.TotalTrades++
		pos.RealizedPnL += r.PnLRealized
		pos.TotalVolumeUSDT += r.ValueUSDT

		if r.Role == "MAKER" {
			pos.MakerTrades++
		} else {
			pos.TakerTrades++
		}

		oldSize := pos.Size
		isClosing := false
		if r.Side == "BUY" && oldSize < -1e-8 {
			isClosing = true
		} else if r.Side == "SELL" && oldSize > 1e-8 {
			isClosing = true
		} else if strings.Contains(r.Side, "LIQUIDATE") {
			isClosing = true
		}

		if r.Side == "BUY" {
			newSize := pos.Size + r.Size
			if newSize > 0 {
				if pos.Size >= 0 {
					pos.AvgEntryPrice = (pos.AvgEntryPrice*pos.Size + r.Price*r.Size) / newSize
				} else {
					pos.AvgEntryPrice = r.Price
				}
			} else if newSize == 0 {
				pos.AvgEntryPrice = 0.0
			}
			pos.Size = newSize
		} else if r.Side == "SELL" {
			newSize := pos.Size - r.Size
			if newSize < 0 {
				if pos.Size <= 0 {
					pos.AvgEntryPrice = (pos.AvgEntryPrice*(-pos.Size) + r.Price*r.Size) / (-newSize)
				} else {
					pos.AvgEntryPrice = r.Price
				}
			} else if newSize == 0 {
				pos.AvgEntryPrice = 0.0
			}
			pos.Size = newSize
		}

		if isClosing {
			if r.PnLRealized > 0 {
				st.WinningTrades++
			} else {
				st.LosingTrades++
			}
		}
	}

	if st.TotalTrades > 0 {
		closed := st.WinningTrades + st.LosingTrades
		if closed > 0 {
			st.WinRate = float64(st.WinningTrades) / float64(closed) * 100.0
		}
	}

	st.Equity = st.Cash
	if st.Equity > st.PeakEquity {
		st.PeakEquity = st.Equity
	}

	now := time.Now()
	if !st.LastCheckpoint.IsZero() && now.After(st.LastCheckpoint) {
		gap := now.Sub(st.LastCheckpoint)
		if gap > 30*time.Second {
			st.OfflineDowntimeSeconds += gap.Seconds()
			st.FeedDownSeconds += gap.Seconds()
			st.ShutdownCount++
			st.LastResumeTime = now
			if e.cfg.ExtendOnShutdown && !st.TargetEndTime.IsZero() {
				st.TargetEndTime = st.TargetEndTime.Add(gap)
			}
		}
	}

	// Keep recent fills for memory blotter
	numRec := len(records)
	if numRec > 30 {
		e.recentFills = append([]TradeRecord(nil), records[numRec-30:]...)
	} else {
		e.recentFills = append([]TradeRecord(nil), records...)
	}

	return st, nil
}

// runExecutionLoop consumes MarketEvents from SPSCRing and evaluates quantitative signals.
func (e *DryRunEngine) runExecutionLoop(ctx context.Context) {
	defer e.wg.Done()

	var eventCount uint64

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		default:
		}

		ev, ok := e.ring.Pop()
		if !ok {
			// Ring is empty; yield processor briefly
			time.Sleep(50 * time.Microsecond)
			continue
		}

		eventCount++
		e.processMarketEvent(&ev)
	}
}

// ProcessMarketEvent processes an individual market event through pricing and order matching.
func (e *DryRunEngine) ProcessMarketEvent(ev *engine.MarketEvent) {
	e.processMarketEvent(ev)
}

func (e *DryRunEngine) getOrCreateSymbolMicro(symbol string) *SymbolMicrostructure {
	if e.symMicro == nil {
		e.symMicro = make(map[string]*SymbolMicrostructure)
	}
	sm, ok := e.symMicro[symbol]
	if !ok || sm == nil {
		sm = newSymbolMicrostructure(symbol, e.cfg.KylesLambda)
		e.symMicro[symbol] = sm
	}
	return sm
}

// processMarketEvent executes hot-path pricing and order matching.
func (e *DryRunEngine) processMarketEvent(ev *engine.MarketEvent) {
	symbol := "BTCUSDT"
	if int(ev.SymbolID) < len(e.cfg.Symbols) {
		symbol = strings.ToUpper(e.cfg.Symbols[ev.SymbolID])
	} else if ev.SymbolID == 1 {
		symbol = "ETHUSDT"
	}

	price := ev.Price
	if price <= 0 {
		return
	}

	now := time.Now()

	// Check feed liveness & fallback status
	isFallback := false
	if e.stream != nil && e.stream.IsFallback() {
		isFallback = true
	}

	e.mu.Lock()
	if isFallback {
		e.state.DataSource = "fallback"
		e.state.QuotingPaused = true
		if !e.lastTickTime.IsZero() {
			diff := now.Sub(e.lastTickTime).Seconds()
			if diff > 0 && diff < 60 {
				e.state.FeedDownSeconds += diff
			}
		}
		e.lastTickTime = now
		e.mu.Unlock()
		// In dry-run mode, synthetic fallback must NEVER drive paper PnL:
		// while on fallback, pause quoting/fills, and keep marks frozen.
		return
	}

	e.state.DataSource = "live"
	e.state.QuotingPaused = false
	if !e.lastTickTime.IsZero() {
		// When transitioning back to live, time tracking continues
	}
	e.lastTickTime = now

	// 1. Update mark price and mark-to-market equity for live ticks
	pos, ok := e.state.Positions[symbol]
	if !ok {
		pos = &Position{Symbol: symbol}
		e.state.Positions[symbol] = pos
	}
	pos.MarkPrice = price
	if pos.Size != 0 {
		pos.UnrealizedPnL = pos.Size * (price - pos.AvgEntryPrice)
	} else {
		pos.UnrealizedPnL = 0
	}

	// Recompute total portfolio equity
	var totalUnrealized float64
	for _, p := range e.state.Positions {
		totalUnrealized += p.UnrealizedPnL
	}
	e.state.UnrealizedPnL = totalUnrealized
	e.state.Equity = e.state.Cash + totalUnrealized

	if e.state.Equity > e.state.PeakEquity {
		e.state.PeakEquity = e.state.Equity
	}

	// Check hard portfolio max drawdown circuit breaker
	if e.state.PeakEquity > 0 {
		dd := (e.state.PeakEquity - e.state.Equity) / e.state.PeakEquity * 100.0
		if dd > e.state.MaxDrawdownPct {
			e.state.MaxDrawdownPct = dd
		}
		if dd >= e.cfg.MaxDrawdownPct && !e.state.CircuitHalted {
			e.state.CircuitHalted = true
			e.state.CircuitHaltReason = fmt.Sprintf("Emergency Circuit Breaker: Max Drawdown breached (%.2f%% >= %.2f%%)", dd, e.cfg.MaxDrawdownPct)
			for sym := range e.activeOrders {
				e.activeOrders[sym] = nil
			}
		}
	}

	if e.state.InitialCapital > 0 {
		e.state.TotalReturnPct = (e.state.Equity - e.state.InitialCapital) / e.state.InitialCapital * 100.0
	}
	sm := e.getOrCreateSymbolMicro(symbol)

	// Update TCA multi-horizon markouts on incoming live market tick
	if e.tcaTracker != nil {
		e.tcaTracker.OnTick(symbol, price, now.UnixNano())
	}
	isHalted := e.state.CircuitHalted
	e.mu.Unlock()

	// 2. Hard Portfolio Circuit Breaker Check
	if isHalted {
		return
	}

	// 3. Trade event (ev.Kind == 0):
	// Realistic order fill matching:
	// A resting maker order fills only when observed trades at or through its price
	// consume the queue ahead (hftbacktest fill realism).
	if ev.Kind == 0 {
		isBuyerMaker := (ev.Side == 1)
		// Update per-symbol microstructure estimators (prevents BTC/ETH price cross-contamination)
		if sm.VpinCalc != nil {
			sm.VpinCalc.UpdateTrade(price, ev.Size, isBuyerMaker)
		}
		if sm.KyleEst != nil {
			sm.KyleEst.UpdateTrade(price, ev.Size, isBuyerMaker)
		}
		if sm.HawkesEst != nil {
			sm.HawkesEst.Update(ev.Timestamp, !isBuyerMaker, ev.Size)
		}
		// Maintain rolling trade flow imbalance & arrival rate
		e.mu.Lock()
		sm.TradeRate1s = 0.92*sm.TradeRate1s + 1.0
		if !isBuyerMaker { // Taker buy
			sm.TradeBuy5s = 0.96*sm.TradeBuy5s + ev.Size
			sm.TradeSell5s = 0.96 * sm.TradeSell5s
		} else { // Taker sell
			sm.TradeSell5s = 0.96*sm.TradeSell5s + ev.Size
			sm.TradeBuy5s = 0.96 * sm.TradeBuy5s
		}
		e.mu.Unlock()

		// Also keep global diagnostic estimators updated
		if e.vpinCalc != nil {
			e.vpinCalc.UpdateTrade(price, ev.Size, isBuyerMaker)
		}
		if e.kyleEst != nil {
			e.kyleEst.UpdateTrade(price, ev.Size, isBuyerMaker)
		}
		if e.hawkesEst != nil {
			e.hawkesEst.Update(ev.Timestamp, !isBuyerMaker, ev.Size)
		}
		e.matchMakerOrders(symbol, price, ev.Size, ev.Side, now)
		return
	}

	// 4. Depth update event (ev.Kind == 1):
	// Update order book metrics, evaluate queue dynamics and maintain resting quotes
	e.evaluateQuotesAndGating(symbol, price, now)
}

// matchMakerOrders matches active resting maker orders against observed trade prints.
// A maker order fills only when trades at or through its price deplete the queue ahead.
func (e *DryRunEngine) matchMakerOrders(symbol string, tradePrice float64, tradeSize float64, tradeSide uint8, now time.Time) {
	e.mu.Lock()
	orders := e.activeOrders[symbol]
	if len(orders) == 0 {
		e.mu.Unlock()
		return
	}

	var remaining []*ActiveMakerOrder
	for _, ord := range orders {
		// Feed + order latency: resting order does not participate in matching before ActiveAt
		if now.Before(ord.ActiveAt) {
			remaining = append(remaining, ord)
			continue
		}

		filled := false
		// Strict hftbacktest fill realism:
		// 1. Price-through tolerance is 1 tick ($0.01), NOT 1.0 bps ($7.50).
		// 2. Trade aggressor must be on the opposite side of our resting limit order:
		//    - Resting BUY bid only fills against SELLER-initiated trades (tradeSide == 1 -> isBuyerMaker == true).
		//    - Resting SELL ask only fills against BUYER-initiated trades (tradeSide == 2 -> isBuyerMaker == false).
		tickTol := 0.01
		if ord.Side == "BUY" && (tradeSide == 1 || tradePrice < ord.Price-tickTol) {
			if tradePrice < ord.Price-tickTol {
				// Market swept completely through our bid price level
				filled = true
			} else if math.Abs(tradePrice-ord.Price) <= tickTol {
				ord.QueueAhead -= tradeSize
				if ord.QueueAhead <= 0 {
					filled = true
				}
			}
		} else if ord.Side == "SELL" && (tradeSide == 2 || tradePrice > ord.Price+tickTol) {
			if tradePrice > ord.Price+tickTol {
				// Market swept completely through our ask price level
				filled = true
			} else if math.Abs(tradePrice-ord.Price) <= tickTol {
				ord.QueueAhead -= tradeSize
				if ord.QueueAhead <= 0 {
					filled = true
				}
			}
		}

		if filled {
			e.executeMakerFillLocked(symbol, ord.Price, ord.Size, ord.Side, ord.EdgeBps, ord.Strategy)
		} else {
			remaining = append(remaining, ord)
		}
	}
	e.activeOrders[symbol] = remaining
	e.mu.Unlock()
}

// evaluateQuotesAndGating calculates order book signals and maintains resting maker quotes.
func (e *DryRunEngine) evaluateQuotesAndGating(symbol string, midPrice float64, now time.Time) {
	e.mu.Lock()
	sm := e.getOrCreateSymbolMicro(symbol)
	e.mu.Unlock()

	var spreadBps float64 = 0.80
	var top5Depth float64 = 150000.0
	var imbL1 float64 = 0.0
	var imbL5 float64 = 0.0
	var imbL20 float64 = 0.0
	var microDev float64 = 0.0
	var queueRatio float64 = 0.50
	var bestBid float64 = midPrice * 0.9999
	var bestAsk float64 = midPrice * 1.0001
	var bidSize float64 = 1.5
	var askSize float64 = 1.5

	var curBids [10]marketdata.PriceLevel
	var curAsks [10]marketdata.PriceLevel
	hasLiveBook := false

	if e.stream != nil {
		if book, hasBook := e.stream.GetL2Book(symbol); hasBook && book != nil && book.NumBids > 0 && book.NumAsks > 0 {
			hasLiveBook = true
			b0 := book.Bids[0]
			a0 := book.Asks[0]
			bestBid = b0.Price
			bestAsk = a0.Price
			bidSize = b0.Size
			askSize = a0.Size
			if bestAsk > bestBid && midPrice > 0 {
				spreadBps = (bestAsk - bestBid) / midPrice * 1e4
			}
			totSize := b0.Size + a0.Size
			if totSize > 0 {
				imbL1 = (b0.Size - a0.Size) / totSize
				queueRatio = b0.Size / totSize
				microPrice := (bestBid*a0.Size + bestAsk*b0.Size) / totSize
				if bestAsk > bestBid {
					microDev = (microPrice - midPrice) / (bestAsk - bestBid)
				}
			}

			var sumBid5, sumAsk5 float64
			var sumTop5 float64
			for i := 0; i < 5 && i < book.NumBids; i++ {
				curBids[i].Price = book.Bids[i].Price
				curBids[i].Size = book.Bids[i].Size
				sumBid5 += book.Bids[i].Size
				sumTop5 += book.Bids[i].Size * book.Bids[i].Price
			}
			for i := 0; i < 5 && i < book.NumAsks; i++ {
				curAsks[i].Price = book.Asks[i].Price
				curAsks[i].Size = book.Asks[i].Size
				sumAsk5 += book.Asks[i].Size
				sumTop5 += book.Asks[i].Size * book.Asks[i].Price
			}
			if sumBid5+sumAsk5 > 0 {
				imbL5 = (sumBid5 - sumAsk5) / (sumBid5 + sumAsk5)
			}
			if sumTop5 > 0 {
				top5Depth = sumTop5
			}

			var sumBid20, sumAsk20 float64
			for i := 0; i < 20 && i < book.NumBids; i++ {
				sumBid20 += book.Bids[i].Size
			}
			for i := 0; i < 20 && i < book.NumAsks; i++ {
				sumAsk20 += book.Asks[i].Size
			}
			if sumBid20+sumAsk20 > 0 {
				imbL20 = (sumBid20 - sumAsk20) / (sumBid20 + sumAsk20)
			}
		}
	}

	// Real-time Cont-Kukanov-Stoikov OFI & Xu-Gould-Howison Multi-Level OFI extraction
	e.mu.Lock()
	var tickOFI1, tickMLOFI float64
	if hasLiveBook && sm.HasPrevBook {
		weights := [5]float64{1.0, 0.7408, 0.5488, 0.4066, 0.3012} // exp(-0.3 * m)
		for m := 0; m < 5; m++ {
			var eBid, eAsk float64
			if curBids[m].Price > sm.PrevBids[m].Price {
				eBid = curBids[m].Size
			} else if curBids[m].Price == sm.PrevBids[m].Price {
				eBid = curBids[m].Size - sm.PrevBids[m].Size
			} else {
				eBid = -sm.PrevBids[m].Size
			}

			if curAsks[m].Price < sm.PrevAsks[m].Price {
				eAsk = curAsks[m].Size
			} else if curAsks[m].Price == sm.PrevAsks[m].Price {
				eAsk = curAsks[m].Size - sm.PrevAsks[m].Size
			} else {
				eAsk = -sm.PrevAsks[m].Size
			}

			levelOFI := eBid - eAsk
			normDenom := curBids[m].Size + curAsks[m].Size + 1e-6
			normLevelOFI := levelOFI / normDenom
			if m == 0 {
				tickOFI1 = normLevelOFI
			}
			tickMLOFI += weights[m] * normLevelOFI
		}
	}
	if hasLiveBook {
		sm.PrevBids = curBids
		sm.PrevAsks = curAsks
		sm.HasPrevBook = true
	}

	sm.Ofi1s = 0.85*sm.Ofi1s + 0.15*tickOFI1
	sm.Ofi5s = 0.96*sm.Ofi5s + 0.04*tickOFI1
	sm.Ofi30s = 0.992*sm.Ofi30s + 0.008*tickOFI1
	sm.Mlofi1s = 0.85*sm.Mlofi1s + 0.15*tickMLOFI
	sm.Mlofi5s = 0.96*sm.Mlofi5s + 0.04*tickMLOFI

	// Real-time tick return & EWMA realized volatility
	var tickRetBps float64
	if sm.PrevMid > 0 && midPrice > 0 {
		tickRetBps = (midPrice - sm.PrevMid) / sm.PrevMid * 1e4
	}
	sm.PrevMid = midPrice
	sm.Ret1sBps = 0.85*sm.Ret1sBps + 0.15*tickRetBps
	sm.Ret5sBps = 0.96*sm.Ret5sBps + 0.04*tickRetBps
	sm.VolVarBps = math.Sqrt(0.98*sm.VolVarBps*sm.VolVarBps + 0.02*tickRetBps*tickRetBps)
	if sm.VolVarBps < 0.25 {
		sm.VolVarBps = 0.25
	}
	sm.TradeRate1s *= 0.95
	tradeTot5s := sm.TradeBuy5s + sm.TradeSell5s
	tradeImb5s := 0.0
	if tradeTot5s > 1e-8 {
		tradeImb5s = (sm.TradeBuy5s - sm.TradeSell5s) / tradeTot5s
	}

	// Per-symbol VPIN & Microstructure Circuit Breaker Check (Soft Quoting Gate)
	liveVpin := sm.VpinCalc.VPIN()
	softGatePaused, _ := sm.CircuitBrk.CheckWithTime(spreadBps, top5Depth, liveVpin, now.UnixNano())
	e.mu.Unlock()

	// Check dynamic portfolio risk safeguard for hard drawdown trip only
	if e.riskSafeguard != nil {
		e.mu.RLock()
		currEq := e.state.Equity
		currDD := e.state.MaxDrawdownPct
		e.mu.RUnlock()
		rState, rReason := e.riskSafeguard.OnTick(currEq, spreadBps, liveVpin, now)
		if rState == StateTripped && currDD >= e.cfg.MaxDrawdownPct {
			e.mu.Lock()
			e.state.CircuitHalted = true
			e.state.CircuitHaltReason = rReason
			e.activeOrders[symbol] = nil
			e.mu.Unlock()
			return
		}
	}

	// If per-symbol microstructure gate detects a transient book anomaly (e.g. 500ms depth flicker or wide spread),
	// temporarily pull resting quotes on this symbol without permanently bricking the engine.
	if softGatePaused {
		e.mu.Lock()
		e.activeOrders[symbol] = nil
		e.mu.Unlock()
		return
	}

	// Check Macro Event Shield (Zero-cost economic release defense)
	var macroSt macro.MacroState
	if e.macroGuardian != nil {
		macroSt = e.macroGuardian.Evaluate(now)
		e.mu.Lock()
		e.state.Macro = macroSt
		if !macroSt.AllowQuoting {
			// PhaseEventHalt: High-impact release (CPI, FOMC, NFP) occurring right now.
			// Pull resting maker quotes immediately to avoid severe adverse selection / toxic fill slippage.
			e.activeOrders[symbol] = nil
			e.state.QuotingPaused = true
			e.mu.Unlock()
			return
		}
		e.mu.Unlock()
	} else {
		macroSt.SpreadMultiplier = 1.0
		macroSt.LeverageMultiplier = 1.0
		macroSt.AllowQuoting = true
	}

	// Populate full 16-feature microstructure vector for current tick
	var x [common.FeatureCount]float64
	x[0] = imbL1
	x[1] = imbL5
	x[2] = imbL20
	x[3] = microDev
	x[4] = spreadBps
	x[5] = sm.Ofi1s * 5.0
	x[6] = sm.Ofi5s * 5.0
	x[7] = sm.Ofi30s * 5.0
	x[8] = sm.Mlofi1s * 3.0
	x[9] = sm.Mlofi5s * 3.0
	x[10] = sm.VolVarBps
	x[11] = sm.Ret1sBps
	x[12] = sm.Ret5sBps
	x[13] = tradeImb5s
	x[14] = sm.TradeRate1s
	x[15] = queueRatio

	// Dynamic Leverage Controller: automatic regime & exposure scaling
	hkRatio := 0.55
	if sm.HawkesEst != nil {
		hkRatio = sm.HawkesEst.BranchingRatio()
	}
	e.mu.RLock()
	peakEq := e.state.PeakEquity
	currEq := e.state.Equity
	isHalted := e.state.CircuitHalted
	e.mu.RUnlock()

	ddPct := 0.0
	if peakEq > 0 {
		ddPct = (peakEq - currEq) / peakEq * 100.0
	}
	levState := e.leverageCtrl.Compute(spreadBps, liveVpin, hkRatio, ddPct, isHalted)

	// Compute institutional clip size and max symbol inventory cap
	clipQty, maxInvQty := e.leverageCtrl.ComputeClipSizeForSymbol(symbol, currEq, levState.EffectiveLeverage, midPrice, len(e.cfg.Symbols))
	if macroSt.LeverageMultiplier > 0 && macroSt.LeverageMultiplier < 1.0 {
		clipQty *= macroSt.LeverageMultiplier
		maxInvQty *= macroSt.LeverageMultiplier
	}
	if clipQty <= 0 {
		clipQty = 0.001
		maxInvQty = 0.005
	}

	e.mu.Lock()
	e.state.Leverage = levState
	pos := e.state.Positions[symbol]
	currentInv := 0.0
	if pos != nil {
		currentInv = pos.Size
	}
	if math.Abs(currentInv) > 1e-8 {
		if sm.InventorySince.IsZero() {
			sm.InventorySince = now
		}
	} else {
		sm.InventorySince = time.Time{}
	}
	holdSec := 0.0
	if !sm.InventorySince.IsZero() {
		holdSec = now.Sub(sm.InventorySince).Seconds()
	}
	e.mu.Unlock()

	// Normalize inventory into [-5.0, +5.0] clip units so GLFT & Cartea-Jaimungal inventory risk aversion
	// scales identically across high-price (BTC) and low-price (ETH/SOL) contracts.
	normInv := 0.0
	if maxInvQty > 0 {
		normInv = (currentInv / maxInvQty) * 5.0
		if normInv > 5.0 {
			normInv = 5.0
		} else if normInv < -5.0 {
			normInv = -5.0
		}
	}

	// GLFT & Cartea-Jaimungal reservation prices with normalized inventory
	qd := e.glftMM.Quote(&x, normInv)
	_, _, carteaRes := e.carteaMM.ComputeQuotes(midPrice, normInv, 0.0, 30.0, x[5])

	// Live 7-expert AdaHedge ensemble updated with realized tick return
	carteaSignal := 0.0
	if e.cfg.EnableAllModels || !experiment.DefaultDisabledModels["cartea_jaimungal"] {
		carteaSignal = (carteaRes - midPrice) / midPrice * 1e4
	}
	currPreds := [common.MaxEnsembleExperts]float64{
		0.45*x[8] + 0.35*x[0]*spreadBps,             // Expert 0: MLOFI Ridge
		0.50*x[5] + 0.30*x[6] + 0.20*x[3]*spreadBps, // Expert 1: Rough Signature OFI
		qd.ReservationBps,                           // Expert 2: GLFT Queue-Reactive
		0.60*x[3]*spreadBps + 0.40*x[1],             // Expert 3: Stoikov Microprice
		-0.35 * x[12],                               // Expert 4: Short-Horizon Mean Reversion
		0.55*x[13] + 0.25*x[5],                      // Expert 5: Hawkes Signed Trade Flow
		carteaSignal,                                // Expert 6: Cartea-Jaimungal
		0,
	}
	e.mu.Lock()
	var metaPred float64
	if sm.HasPrevPreds {
		metaPred, _ = sm.AdaHedge.UpdateAndCombine(tickRetBps, sm.PrevPreds, currPreds)
		_, _ = e.adaHedge.UpdateAndCombine(tickRetBps, sm.PrevPreds, currPreds)
	} else {
		metaPred = qd.ReservationBps
	}
	sm.PrevPreds = currPreds
	sm.HasPrevPreds = true

	floorBps := e.minHalfSpreadFloorBps
	if floorBps <= 0 {
		floorBps = 0.25
	}
	var tcaPenaltyBps float64
	if e.tcaTracker != nil {
		tcaSnap := e.tcaTracker.Snapshot()
		if tcaSnap.TotalFillsAnalyzed >= 4 && tcaSnap.MeanMarkout1sBps < -0.25 {
			tcaPenaltyBps = math.Min(1.25, math.Abs(tcaSnap.MeanMarkout1sBps)*0.5)
		}
	}
	e.mu.Unlock()

	// Proportional inventory skew restoring force:
	// When short (currentInv < 0), shifts reservation price higher to pull buy quote closer to mid
	// and push sell quote further out.
	var invSkewBps float64
	if maxInvQty > 0 {
		invFraction := currentInv / maxInvQty // [-1.0, 1.0]
		invSkewBps = invFraction * 2.5        // +/- 2.5 bps restoring force
	}

	// Blend AdaHedge online meta-prediction with GLFT reservation skew
	blendedSkewBps := 0.65*(qd.ReservationBps-invSkewBps) + 0.35*metaPred

	// Ornstein-Uhlenbeck Inventory Holding-Age Urgency: accelerate mean-reverting exits after 75s
	if holdSec > 75.0 {
		urgencyBps := math.Min(1.50, (holdSec-75.0)*0.02)
		if currentInv > 0 {
			blendedSkewBps -= urgencyBps
		} else if currentInv < 0 {
			blendedSkewBps += urgencyBps
		}
	}

	bidOffsetBps := math.Max(floorBps, qd.HalfSpreadBps-blendedSkewBps) + tcaPenaltyBps
	askOffsetBps := math.Max(floorBps, qd.HalfSpreadBps+blendedSkewBps) + tcaPenaltyBps

	// Apply Macro Event Shield spread scaling (e.g. 2.5x pre-event, 1.5x post-event drift)
	if macroSt.SpreadMultiplier > 1.0 {
		bidOffsetBps *= macroSt.SpreadMultiplier
		askOffsetBps *= macroSt.SpreadMultiplier
	}

	// Volatility-Adaptive Fee Floor (Avellaneda-Stoikov & GLFT):
	// In calm markets (vol <= 0.40 bps), floor is tight (fee + 0.45 bps) for maximum fill turnover.
	// In volatile markets, floor scales with volatility so liquidity provision is compensated with wider margins.
	feeFloorBps := math.Max(floorBps, e.cfg.MakerFeeBps+0.45)
	if sm.VolVarBps > 0.40 {
		volScale := math.Min(2.5, sm.VolVarBps/0.40)
		feeFloorBps *= volScale
	}
	if bidOffsetBps < feeFloorBps {
		bidOffsetBps = feeFloorBps
	}
	if askOffsetBps < feeFloorBps {
		askOffsetBps = feeFloorBps
	}

	targetBid := midPrice - (bidOffsetBps * midPrice * 1e-4)
	targetAsk := midPrice + (askOffsetBps * midPrice * 1e-4)

	// Institutional Breakeven Floor & Profit-Locking Ratchet:
	// When holding open inventory, the closing quote must mathematically guarantee net profitability
	// after accounting for round-trip maker fees (2 * MakerFeeBps) and target net profit margin (0.50 bps).
	// An exit is ONLY allowed below break-even if adverse breakout stop-loss (> 15 bps drawdown) triggers.
	if pos != nil && math.Abs(pos.Size) > 1e-8 && pos.AvgEntryPrice > 0 {
		minNetEdgeBps := 0.50
		roundTripFeeBps := 2.0 * e.cfg.MakerFeeBps

		if pos.Size > 0 { // Holding LONG position
			minProfitAsk := pos.AvgEntryPrice * (1.0 + (roundTripFeeBps+minNetEdgeBps)*1e-4)
			adverseDrawdownBps := (pos.AvgEntryPrice - midPrice) / pos.AvgEntryPrice * 1e4

			if adverseDrawdownBps > 15.0 {
				// Severe adverse trend: stop-loss triggered to protect capital against runaway crashes
				if bestAsk > 0 {
					targetAsk = bestAsk
				}
			} else {
				// Normal market oscillation: quote at or above minProfitAsk
				if targetAsk < minProfitAsk {
					targetAsk = minProfitAsk
				}
				// Profit-locking ratchet: if market surged into confirmed net profit, take inside ask
				if bestAsk >= minProfitAsk {
					targetAsk = bestAsk
				}
			}
		} else if pos.Size < 0 { // Holding SHORT position
			maxProfitBid := pos.AvgEntryPrice * (1.0 - (roundTripFeeBps+minNetEdgeBps)*1e-4)
			adverseDrawdownBps := (midPrice - pos.AvgEntryPrice) / pos.AvgEntryPrice * 1e4

			if adverseDrawdownBps > 15.0 {
				// Severe adverse trend: stop-loss triggered to protect capital
				if bestBid > 0 {
					targetBid = bestBid
				}
			} else {
				// Normal market oscillation: cover at or below maxProfitBid
				if targetBid > maxProfitBid {
					targetBid = maxProfitBid
				}
				// Profit-locking ratchet: if market dumped into confirmed net profit, take inside bid
				if bestBid <= maxProfitBid && bestBid > 0 {
					targetBid = bestBid
				}
			}
		}
	}

	// Clamp to the touch so resting quotes remain passive maker orders (never cross into taker territory).
	if bestBid > 0 && targetBid > bestBid {
		targetBid = bestBid
	}
	if bestAsk > 0 && targetAsk < bestAsk {
		targetAsk = bestAsk
	}

	// Quantize to valid exchange price tick size ($0.01 for BTCUSDT and ETHUSDT)
	targetBid = math.Floor(targetBid*100.0) / 100.0
	targetAsk = math.Ceil(targetAsk*100.0) / 100.0

	e.mu.Lock()
	defer e.mu.Unlock()

	orders := e.activeOrders[symbol]
	var keptOrders []*ActiveMakerOrder
	hasBuy := false
	hasSell := false
	for _, ord := range orders {
		target := targetBid
		if ord.Side == "SELL" {
			target = targetAsk
		}
		// Preserve FIFO queue priority: only cancel & replace if target price moved > 2.5 bps.
		// Avoid cancelling orders solely due to time elapsed, preserving queue position.
		devBps := math.Abs(ord.Price-target) / target * 1e4
		if devBps > 2.5 {
			continue // cancel stale order
		}
		keptOrders = append(keptOrders, ord)
		if ord.Side == "BUY" {
			hasBuy = true
		}
		if ord.Side == "SELL" {
			hasSell = true
		}
	}
	orders = keptOrders

	latency := e.cfg.OrderLatency
	if latency <= 0 {
		latency = 120 * time.Millisecond
	}

	// Place resting BUY maker quote at the BACK of the real L1 queue
	// If currently short (currentInv < 0), this is an inventory COVER order: unconditionally allowed!
	// If currently flat or long (currentInv >= 0), this is opening/accumulating: apply risk gating.
	allowBuy := !hasBuy
	buySize := clipQty
	if currentInv < 0 {
		// Closing short inventory: order size matches entire open short to return cleanly to cash equilibrium
		buySize = math.Abs(currentInv)
	} else {
		// Opening / accumulating long inventory
		if currentInv+clipQty > maxInvQty || blendedSkewBps <= -3.5 || currentInv >= 2.0*clipQty {
			allowBuy = false
		}
		if currentInv > 0 && (blendedSkewBps < -1.25 || sm.Ret5sBps < -1.5) {
			allowBuy = false // Toxic cascade defense: do not stack long inventory into a falling knife
		}
	}
	if allowBuy {
		queueEst := bidSize
		if bestBid > 0 && targetBid < bestBid*0.9999 {
			queueEst = math.Max(buySize, bidSize*0.50)
		}
		orders = append(orders, &ActiveMakerOrder{
			Symbol:     symbol,
			Side:       "BUY",
			Price:      targetBid,
			Size:       buySize,
			PlacedAt:   now,
			ActiveAt:   now.Add(latency),
			QueueAhead: queueEst,
			Strategy:   "GLFT_QueueReactive_Maker",
			EdgeBps:    bidOffsetBps,
		})
	}

	// Place resting SELL maker quote at the BACK of the real L1 queue
	// If currently long (currentInv > 0), this is an inventory EXIT order: unconditionally allowed!
	// If currently flat or short (currentInv <= 0), this is opening/accumulating: apply risk gating.
	allowSell := !hasSell
	sellSize := clipQty
	if currentInv > 0 {
		// Closing long inventory: order size matches entire open long to return cleanly to cash equilibrium
		sellSize = math.Abs(currentInv)
	} else {
		// Opening / accumulating short inventory
		if currentInv-clipQty < -maxInvQty || blendedSkewBps >= 3.5 || currentInv <= -2.0*clipQty {
			allowSell = false
		}
		if currentInv < 0 && (blendedSkewBps > 1.25 || sm.Ret5sBps > 1.5) {
			allowSell = false // Toxic cascade defense: do not stack short inventory into a surging knife
		}
	}
	if allowSell {
		queueEst := askSize
		if bestAsk > 0 && targetAsk > bestAsk*1.0001 {
			queueEst = math.Max(sellSize, askSize*0.50)
		}
		orders = append(orders, &ActiveMakerOrder{
			Symbol:     symbol,
			Side:       "SELL",
			Price:      targetAsk,
			Size:       sellSize,
			PlacedAt:   now,
			ActiveAt:   now.Add(latency),
			QueueAhead: queueEst,
			Strategy:   "GLFT_QueueReactive_Maker",
			EdgeBps:    askOffsetBps,
		})
	}
	e.activeOrders[symbol] = orders

	// Tactical Taker Execution:
	// In HFT market making, taker orders are strictly restricted to extreme momentum dislocations (>= 15.0 bps)
	// that clear double round-trip cost hurdles, preventing fee bleed on high-frequency noise.
	if math.Abs(metaPred) >= 15.0 && e.conformal.PassesConformalGate(metaPred, x[10], (e.cfg.TakerFeeBps+e.cfg.MakerFeeBps)*2.0) {
		e.simulateTakerFillLocked(symbol, midPrice, metaPred)
	}
}

// simulateMakerFill provides direct maker fill simulation for testing.
func (e *DryRunEngine) simulateMakerFill(symbol string, midPrice float64, qd common.QuoteDecision, edgeBps float64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	side := "BUY"
	orderPrice := midPrice - (qd.BidOffsetBps * midPrice * 1e-4)
	effLev := 2.5
	if e.state.Leverage.EffectiveLeverage > 0 {
		effLev = e.state.Leverage.EffectiveLeverage
	}
	qty := 0.01
	if e.leverageCtrl != nil && midPrice > 0 {
		qty = e.leverageCtrl.ComputeOrderSize(e.state.Equity, effLev, midPrice, len(e.cfg.Symbols))
	}
	if qty <= 0 {
		qty = 0.001
	}
	if qd.ReservationBps < 0 {
		side = "SELL"
		orderPrice = midPrice + (qd.AskOffsetBps * midPrice * 1e-4)
	}

	e.executeMakerFillLocked(symbol, orderPrice, qty, side, edgeBps, "GLFT_Maker_Test")
}

// simulateTakerFill provides direct taker fill simulation for testing.
func (e *DryRunEngine) simulateTakerFill(symbol string, midPrice float64, alphaBps float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.simulateTakerFillLocked(symbol, midPrice, alphaBps)
}

// executeMakerFillLocked executes a passive maker order fill with spread capture. Caller must hold e.mu.
func (e *DryRunEngine) executeMakerFillLocked(symbol string, orderPrice, qty float64, side string, edgeBps float64, strategy string) {
	valUSDT := orderPrice * qty
	feeUSDT := valUSDT * (e.cfg.MakerFeeBps * 1e-4)

	pos := e.state.Positions[symbol]
	realizedPnL := 0.0

	if side == "BUY" {
		if pos.Size < 0 { // Closing short
			closeQty := math.Min(-pos.Size, qty)
			realizedPnL = closeQty * (pos.AvgEntryPrice - orderPrice)
		}
		newSize := pos.Size + qty
		if newSize > 0 {
			if pos.Size >= 0 {
				pos.AvgEntryPrice = (pos.AvgEntryPrice*pos.Size + orderPrice*qty) / newSize
			} else {
				pos.AvgEntryPrice = orderPrice
			}
		} else if newSize == 0 {
			pos.AvgEntryPrice = 0.0
		}
		pos.Size = newSize
	} else {
		if pos.Size > 0 { // Closing long
			closeQty := math.Min(pos.Size, qty)
			realizedPnL = closeQty * (orderPrice - pos.AvgEntryPrice)
		}
		newSize := pos.Size - qty
		if newSize < 0 {
			if pos.Size <= 0 {
				pos.AvgEntryPrice = (pos.AvgEntryPrice*(-pos.Size) + orderPrice*qty) / (-newSize)
			} else {
				pos.AvgEntryPrice = orderPrice
			}
		} else if newSize == 0 {
			pos.AvgEntryPrice = 0.0
		}
		pos.Size = newSize
	}

	// Net PnL after exchange maker fee so WinRate and RealizedPnL reflect real net-of-fee profitability
	netRealizedPnL := realizedPnL - feeUSDT
	e.state.Cash += netRealizedPnL

	pos.RealizedPnL += netRealizedPnL
	pos.TotalTrades++
	pos.MakerTrades++
	pos.TotalVolumeUSDT += valUSDT
	if e.riskSafeguard != nil {
		e.riskSafeguard.OnFill(netRealizedPnL, pos.Size)
	}
	if e.tcaTracker != nil {
		sideInt := int8(1)
		if side == "SELL" {
			sideInt = -1
		}
		mid := pos.MarkPrice
		if mid <= 0 {
			mid = orderPrice
		}
		e.tcaTracker.RecordFill(uint64(e.state.TotalTrades), symbol, sideInt, orderPrice, mid, time.Now().UnixNano())
	}

	e.state.RealizedPnL += netRealizedPnL
	e.state.TotalTrades++
	if realizedPnL != 0 { // Round-trip closing fill
		if netRealizedPnL > 0 {
			e.state.WinningTrades++
		} else {
			e.state.LosingTrades++
		}
	}

	if closed := e.state.WinningTrades + e.state.LosingTrades; closed > 0 {
		e.state.WinRate = float64(e.state.WinningTrades) / float64(closed) * 100.0
	} else if e.state.TotalTrades > 0 {
		e.state.WinRate = float64(e.state.WinningTrades) / float64(e.state.TotalTrades) * 100.0
	}

	dataSource := e.state.DataSource
	if dataSource == "" {
		dataSource = "live"
	}

	rec := TradeRecord{
		Timestamp:   time.Now().UTC(),
		Symbol:      symbol,
		Side:        side,
		Role:        "MAKER",
		Price:       orderPrice,
		Size:        qty,
		ValueUSDT:   valUSDT,
		FeeUSDT:     feeUSDT,
		EdgeBps:     edgeBps,
		SlippageBps: 0.0,
		PnLRealized: netRealizedPnL,
		Strategy:    strategy,
		DataSource:  dataSource,
	}

	e.appendTradeRecord(rec)
}

// simulateTakerFillLocked simulates an aggressive taker order. Caller must hold e.mu.
func (e *DryRunEngine) simulateTakerFillLocked(symbol string, midPrice float64, alphaBps float64) {
	side := "BUY"
	effLev := 2.5
	if e.state.Leverage.EffectiveLeverage > 0 {
		effLev = e.state.Leverage.EffectiveLeverage
	}
	qty := 0.005
	if e.leverageCtrl != nil && midPrice > 0 {
		qty = e.leverageCtrl.ComputeOrderSize(e.state.Equity, effLev*0.5, midPrice, len(e.cfg.Symbols))
	}
	if qty <= 0 {
		qty = 0.001
	}
	if alphaBps < 0 {
		side = "SELL"
	}

	// Kyle's lambda slippage: slippage = lambda * sqrt(order_volume)
	slippageBps := e.cfg.KylesLambda * math.Sqrt(qty)
	execPrice := midPrice * (1.0 + slippageBps*1e-4)
	if side == "SELL" {
		execPrice = midPrice * (1.0 - slippageBps*1e-4)
	}

	valUSDT := execPrice * qty
	feeUSDT := valUSDT * (e.cfg.TakerFeeBps * 1e-4)

	pos := e.state.Positions[symbol]
	realizedPnL := 0.0

	if side == "BUY" {
		if pos.Size < 0 {
			closeQty := math.Min(-pos.Size, qty)
			realizedPnL = closeQty * (pos.AvgEntryPrice - execPrice)
		}
		newSize := pos.Size + qty
		if newSize > 0 {
			if pos.Size >= 0 {
				pos.AvgEntryPrice = (pos.AvgEntryPrice*pos.Size + execPrice*qty) / newSize
			} else {
				pos.AvgEntryPrice = execPrice
			}
		} else if newSize == 0 {
			pos.AvgEntryPrice = 0.0
		}
		pos.Size = newSize
	} else {
		if pos.Size > 0 {
			closeQty := math.Min(pos.Size, qty)
			realizedPnL = closeQty * (execPrice - pos.AvgEntryPrice)
		}
		newSize := pos.Size - qty
		if newSize < 0 {
			if pos.Size <= 0 {
				pos.AvgEntryPrice = (pos.AvgEntryPrice*(-pos.Size) + execPrice*qty) / (-newSize)
			} else {
				pos.AvgEntryPrice = execPrice
			}
		} else if newSize == 0 {
			pos.AvgEntryPrice = 0.0
		}
		pos.Size = newSize
	}

	netRealizedPnL := realizedPnL - feeUSDT
	e.state.Cash += netRealizedPnL

	pos.RealizedPnL += netRealizedPnL
	pos.TotalTrades++
	pos.TakerTrades++
	pos.TotalVolumeUSDT += valUSDT
	if e.riskSafeguard != nil {
		e.riskSafeguard.OnFill(netRealizedPnL, pos.Size)
	}
	if e.tcaTracker != nil {
		sideInt := int8(1)
		if side == "SELL" {
			sideInt = -1
		}
		e.tcaTracker.RecordFill(uint64(e.state.TotalTrades), symbol, sideInt, execPrice, midPrice, time.Now().UnixNano())
	}

	e.state.RealizedPnL += netRealizedPnL
	e.state.TotalTrades++
	if realizedPnL != 0 {
		if netRealizedPnL > 0 {
			e.state.WinningTrades++
		} else {
			e.state.LosingTrades++
		}
	}

	if closed := e.state.WinningTrades + e.state.LosingTrades; closed > 0 {
		e.state.WinRate = float64(e.state.WinningTrades) / float64(closed) * 100.0
	} else if e.state.TotalTrades > 0 {
		e.state.WinRate = float64(e.state.WinningTrades) / float64(e.state.TotalTrades) * 100.0
	}

	dataSource := e.state.DataSource
	if dataSource == "" {
		dataSource = "live"
	}

	rec := TradeRecord{
		Timestamp:   time.Now().UTC(),
		Symbol:      symbol,
		Side:        side,
		Role:        "TAKER",
		Price:       execPrice,
		Size:        qty,
		ValueUSDT:   valUSDT,
		FeeUSDT:     feeUSDT,
		EdgeBps:     alphaBps,
		SlippageBps: slippageBps,
		PnLRealized: netRealizedPnL,
		Strategy:    "AdaHedge_Conformal_Taker",
		DataSource:  dataSource,
	}

	e.appendTradeRecord(rec)
}

func (e *DryRunEngine) appendTradeRecord(rec TradeRecord) {
	if len(e.recentFills) >= 30 {
		e.recentFills = e.recentFills[1:]
	}
	e.recentFills = append(e.recentFills, rec)

	// Append to JSONL journal
	if e.journal != nil {
		line, err := json.Marshal(rec)
		if err == nil {
			_, _ = e.journal.Write(append(line, '\n'))
		}
	}
}

// RiskSafeguard returns the dynamic circuit breaker engine.
func (e *DryRunEngine) RiskSafeguard() *CircuitBreaker {
	return e.riskSafeguard
}

// KylesLambda returns the real-time Kyle's Lambda estimator.
func (e *DryRunEngine) KylesLambda() *common.KylesLambdaEstimator {
	return e.kyleEst
}

// VPIN returns the online VPIN toxicity estimator.
func (e *DryRunEngine) VPIN() *common.VPINCalculator {
	return e.vpinCalc
}

// TCAMarkout returns the institutional post-trade transaction cost analysis tracker.
func (e *DryRunEngine) TCAMarkout() *TCAMarkoutTracker {
	return e.tcaTracker
}
