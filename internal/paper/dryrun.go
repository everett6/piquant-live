package paper

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"piquant-live/internal/quant/alpha"
	"piquant-live/internal/quant/guardrails"
	"piquant-live/internal/quant/orchestrator"
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
	TargetProfitBps   float64       // Net target profit margin per swing in bps (default: 25.0 bps = 0.25%)
	EntryOffsetBps    float64       // Minimum maker entry distance from mid in bps (default: 12.5 bps = 0.125%)
	StopLossBps       float64       // Structural invalidation stop loss in bps (default: 75.0 bps = 0.75%)
	CleanReset        bool          // Reset cumulative historical win/loss stats on start
	EnableTaker       bool          // Opt-in to tactical taker executions on high-conviction momentum (default: false)
	MonthlyGainCapPct float64       // Soft ceiling for monthly profit target; triggers capital preservation when reached (default: 15.0%)
	SpotOnly          bool          // Spot-only cash account mode: disable naked shorting, long inventory only (default: false)
	TaxRatePct        float64       // Estimated income/capital gains tax rate percentage (default: 35.0% for US/CA)
	ZeroFeeMaker      bool          // Zero-fee tier on major pairs (e.g. Binance.US BTC/USD, BTC/USDT, BTC/USDC)
	ClipFraction      float64       // Fraction of max symbol allocation per quote clip (default: 0.45 = 45%)
	BuyThreshold      float64       // Directional conviction entry threshold (default: 0.65; dynamic up to 0.72 on thin bids)
	SellThreshold     float64       // Directional conviction short entry threshold (default: 0.35; dynamic down to 0.28 on heavy bids)
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
		MaxLeverage:       3.5,
		EnableMacroShield: true,
		DataDir:           dataDir,
		TargetProfitBps:   50.0, // Quant hedge fund swing target (>= 50 bps per swing, completely eliminating fee drag)
		EntryOffsetBps:    2.5,  // Patient maker entry at inside touch/queue level
		StopLossBps:       60.0, // Structural stop-loss protecting against crashes
		MonthlyGainCapPct: 15.0, // Hard ceiling for monthly profit target (preserves banked gains)
		SpotOnly:          false,
		TaxRatePct:        35.0, // Default 35% estimated tax reserve for US/CA
		ZeroFeeMaker:      false, // Default standard maker fee (1.5 bps); opt-in to zero-fee BTC maker via --zero-fee-btc
		ClipFraction:      0.45,  // 45% clip sizing for small spot cash accounts ($50-$75 per clip on $200 capital)
		BuyThreshold:      0.65,  // Directional entry threshold (dynamic up to 0.72 on thin bids)
		SellThreshold:     0.35,  // Short entry threshold (dynamic down to 0.28 on heavy bids)
		CleanReset:        false,
		EnableTaker:       false,
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
	Symbol          string    `json:"symbol"`
	Side            string    `json:"side"` // "BUY" or "SELL"
	Price           float64   `json:"price"`
	Size            float64   `json:"size"`
	PlacedAt        time.Time `json:"placed_at"`
	ActiveAt        time.Time `json:"active_at"` // PlacedAt + OrderLatency
	QueueAhead      float64   `json:"queue_ahead"`
	Strategy        string    `json:"strategy"`
	EdgeBps         float64   `json:"edge_bps"`
	FairValue       float64   `json:"fair_value,omitempty"`
	TargetInventory float64   `json:"target_inventory,omitempty"`
	CancelingUntil  time.Time `json:"canceling_until,omitempty"` // Cancel propagation transit delay (cancel race)
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
	ScalpingState          map[string]ScalpingRegimeState `json:"scalping_state,omitempty"` // Per-symbol scalping viability & regime
	SelfAdaptation         SelfAdaptationState           `json:"self_adaptation"`          // Autonomous parameter backtesting & hot-reloading
	Coinbase               *CoinbaseSignalState          `json:"coinbase_signal,omitempty"` // Real-time institutional lead-lag oracle
	SpotOnly               bool                          `json:"spot_only"`                 // Cash-only spot account mode (no naked shorting)
	TaxRatePct             float64                       `json:"tax_rate_pct"`              // Estimated income/capital gains tax rate (e.g. 35.0%)
	EstimatedTaxUSDT       float64                       `json:"estimated_tax_usdt"`        // Cumulative estimated tax reserve
	AfterTaxEquity         float64                       `json:"after_tax_equity"`          // Equity net of estimated tax reserve
	AfterTaxReturnPct      float64                       `json:"after_tax_return_pct"`      // Post-tax return percentage
	QuantOrchestrator      *orchestrator.OrchestratorReport `json:"quant_orchestrator,omitempty"` // Latency-adaptive multi-strategy orchestration
}

// ScalpingRegimeState reports per-symbol scalping viability and mode (Scalp vs Patient Swing).
type ScalpingRegimeState struct {
	ScalpingEnabled bool    `json:"scalping_enabled"`
	Regime          string  `json:"regime"` // "ACTIVE_SCALP", "PATIENT_SWING", "DISABLED_LEAK_GUARD"
	Reason          string  `json:"reason"`
	ConsecutiveLoss int     `json:"consecutive_losses"`
	RollingWinRate  float64 `json:"rolling_win_rate"`
	RollingPnlUSDT  float64 `json:"rolling_pnl_usdt"`
	TargetInventory  float64 `json:"target_inventory"`
	FairValue        float64 `json:"fair_value"`
	MultiFactorScore float64 `json:"multi_factor_score,omitempty"`
	MarketRegime     string  `json:"market_regime,omitempty"`
}

// SelfAdaptationState reports autonomous parameter backtesting and hot-reloading progress.
type SelfAdaptationState struct {
	LastRecalibration  time.Time `json:"last_recalibration"`
	RecalibrationCount int       `json:"recalibration_count"`
	Status             string    `json:"status"` // "MONITORING_CALM", "LEAKAGE_DETECTED", "AUTONOMOUS_TUNING", "HOT_RELOADED"
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
	TradeRate1s      float64
	InventorySince   time.Time
	BidCooldownUntil time.Time
	AskCooldownUntil time.Time

	// Multi-Horizon Quant Hedge Fund Alpha Tracking
	MidHistory []MidPricePoint
	Ret1mBps   float64
	Ret5mBps   float64
	Ret15mBps  float64
	AtrBps     float64
	OBI        float64 // Order book depth imbalance: (bid5 - ask5) / (bid5 + ask5)
	Conviction float64 // Quant directional conviction score in [0.0, 1.0]

	// Delta-Neutral Funding Carry Tracker
	FundingTracker *common.FundingCarryTracker

	// Tribeca Institutional Fair Value & Target Inventory
	FairValueEngine *common.TribecaFairValueEngine
	LatestFairValue float64
	LatestTargetInv float64
	top5BidsP       [5]float64
	top5BidsS       [5]float64
	top5AsksP       [5]float64
	top5AsksS       [5]float64

	// QuantMuse Cross-Sectional Multi-Factor Model
	MultiFactor       *alpha.MultiFactorModel
	LatestMultiFactor alpha.MultiFactorScore

	// Self-Improving Scalping Defense
	ScalpingDisabled       bool
	ScalpingDisabledReason string
	ConsecutiveLosses      int
	RecentPnlWindow        []float64
}

// MidPricePoint stores a historical mid price snapshot for multi-horizon return calculations.
type MidPricePoint struct {
	Time time.Time
	Mid  float64
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
		AtrBps:      35.0, // Baseline 15-minute ATR volatility in bps
		Conviction:  0.50, // Neutral 50/50 baseline
		TradeRate1s: 4.0,
		FundingTracker: common.NewFundingCarryTracker(0.05, 0.0001),
		FairValueEngine: common.NewTribecaFairValueEngine(common.FairValueMicroPrice, 0.10, 1.5, 0.02),
		MultiFactor:     alpha.NewMultiFactorModel(100),
	}
}

// EvaluateScalpingViability checks whether high-frequency scalping is profitable in the current regime.
// If rolling win rate is low, net PnL is negative, or order book toxicity is high, scalping is disabled.
func (sm *SymbolMicrostructure) EvaluateScalpingViability(tca *TCAMarkoutTracker) (bool, string) {
	if len(sm.RecentPnlWindow) >= 3 {
		sumPnL := 0.0
		losses := 0
		for _, p := range sm.RecentPnlWindow {
			sumPnL += p
			if p < 0 {
				losses++
			}
		}
		winRate := float64(len(sm.RecentPnlWindow)-losses) / float64(len(sm.RecentPnlWindow))

		// 3+ consecutive losses or low rolling win rate with negative net PnL
		if sm.ConsecutiveLosses >= 3 {
			return false, fmt.Sprintf("%d consecutive losses: tight scalping halted", sm.ConsecutiveLosses)
		}
		if len(sm.RecentPnlWindow) >= 5 && winRate < 0.35 && sumPnL < 0 {
			return false, fmt.Sprintf("Rolling win rate low (%.0f%%) with negative net PnL ($%.2f)", winRate*100.0, sumPnL)
		}

		// Adverse TCA markout (resting maker orders being systematically adverse-selected)
		if tca != nil {
			tcaSnap := tca.Snapshot()
			if tcaSnap.TotalFillsAnalyzed >= 4 && tcaSnap.MeanMarkout1sBps < -1.0 {
				return false, fmt.Sprintf("Toxic adverse markout (%.2f bps < -1.0 bps)", tcaSnap.MeanMarkout1sBps)
			}
		}
	}

	// Extreme order flow toxicity or cascade risk
	if sm.VpinCalc != nil && sm.VpinCalc.VPIN() > 0.70 {
		return false, "High VPIN order flow toxicity (>0.70)"
	}
	if sm.HawkesEst != nil && sm.HawkesEst.IsCascadeRisk() {
		return false, "Hawkes self-exciting cascade event"
	}

	return true, ""
}

// AutonomousRecalibrator defines an injected function that runs backtest grid search and persists optimal parameters.
type AutonomousRecalibrator func(ctx context.Context, symbol string, currentCapital float64, dataDir string) error

// DryRunEngine orchestrates the 14-day autonomous dry run.
type DryRunEngine struct {
	cfg       DryRunConfig
	ring      *engine.SPSCRing
	stream    *MarketStreamIngestor
	journal   *os.File
	journalCh chan []byte
	journalWg sync.WaitGroup

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

	// Autonomous self-improving recalibration
	recalibrator    AutonomousRecalibrator
	recalibrating   atomic.Bool
	lastAutoRecalib time.Time

	// Real-time Coinbase institutional lead-lag signal oracle
	cbOracle *CoinbaseSignalOracle

	// Quant hedge fund latency orchestrator and pre-trade guardrails
	quantOrch     *orchestrator.LatencyAdaptiveOrchestrator
	preTradeGuard *guardrails.PreTradeGuard
}

// atomicWriteFile safely writes data to a temporary file, fsyncs data to media, renames,
// and fsyncs the parent directory to guarantee crash-consistency on ext4 (microSD / NVMe).
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(dir, "atomic-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}

	// Guarantee ext4 parent directory dentry is committed to physical storage
	if df, err := os.Open(dir); err == nil {
		_ = df.Sync()
		_ = df.Close()
	}
	return nil
}

// sanitizeJournalTail validates that the journal file ends cleanly with a newline.
// If a sudden power cut severed a JSON line write mid-sentence, it truncates the partial
// unparseable bytes back to the last valid newline so append operations never fuse records.
func sanitizeJournalTail(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil || stat.Size() == 0 {
		return err
	}

	readLen := int64(4096)
	if stat.Size() < readLen {
		readLen = stat.Size()
	}

	buf := make([]byte, readLen)
	offset := stat.Size() - readLen
	if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
		return err
	}

	// Clean trailing nulls and whitespace
	if len(buf) > 0 && buf[len(buf)-1] == '\n' {
		return nil
	}

	// File was truncated mid-line on power cut or contains trailing null bytes.
	// Find the last clean newline character.
	lastNL := bytes.LastIndexByte(buf, '\n')
	if lastNL == -1 {
		// Entire tail has no newline; truncate file to start fresh
		return f.Truncate(0)
	}

	validSize := offset + int64(lastNL) + 1
	if validSize < stat.Size() {
		return f.Truncate(validSize)
	}
	return nil
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
	if cfg.TargetProfitBps <= 0 {
		cfg.TargetProfitBps = 50.0
	}
	if cfg.EntryOffsetBps <= 0 {
		cfg.EntryOffsetBps = 2.5
	}
	if cfg.StopLossBps <= 0 {
		cfg.StopLossBps = 60.0
	}
	if cfg.MonthlyGainCapPct <= 0 {
		cfg.MonthlyGainCapPct = 15.0
	}
	if cfg.ClipFraction <= 0 {
		cfg.ClipFraction = 0.45
	}
	if cfg.BuyThreshold <= 0 {
		cfg.BuyThreshold = 0.65
	}
	if cfg.SellThreshold <= 0 {
		cfg.SellThreshold = 0.35
	}

	if cfg.SpotOnly {
		cfg.BaseLeverage = 1.0
		cfg.MaxLeverage = 1.0
	}

	levCfg := DefaultDynamicLeverageConfig()
	levCfg.MaxDrawdownPct = cfg.MaxDrawdownPct
	if cfg.BaseLeverage > 0 {
		levCfg.BaseLeverage = cfg.BaseLeverage
	}
	if cfg.MaxLeverage > 0 {
		levCfg.MaxLeverage = cfg.MaxLeverage
	}
	if cfg.ClipFraction > 0 {
		levCfg.ClipFraction = cfg.ClipFraction
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

	guardCfg := guardrails.DefaultGuardrailConfig()
	guardCfg.MaxDailyDrawdownPct = cfg.MaxDrawdownPct
	maxLev := cfg.MaxLeverage
	if maxLev <= 0 {
		maxLev = 3.5
	}
	if cfg.InitialCapital > 0 {
		buyingPower := cfg.InitialCapital * maxLev
		guardCfg.Exposure.MaxGrossUSD = math.Max(25000.0, buyingPower*1.50)
		guardCfg.Exposure.MaxNetUSD = math.Max(15000.0, buyingPower*1.20)
		guardCfg.Exposure.MaxOrderQtyUSD = math.Max(5000.0, buyingPower*0.85)
		guardCfg.Exposure.MaxConcentrationPct = math.Max(80.0, 100.0*maxLev)
	}
	guardCfg.RateLimitRate = 200.0
	guardCfg.RateLimitCapacity = 500.0
	e.quantOrch = orchestrator.NewLatencyAdaptiveOrchestrator(orchestrator.DefaultOrchestratorConfig())
	e.preTradeGuard = guardrails.NewPreTradeGuard(guardCfg)

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

	// Clean reset option: clears contaminated legacy trade counts, win rate, realized PnL, positions, and archives old journal
	if cfg.CleanReset {
		e.state.WinningTrades = 0
		e.state.LosingTrades = 0
		e.state.TotalTrades = 0
		e.state.WinRate = 100.0
		e.state.RealizedPnL = 0.0
		e.state.InitialCapital = cfg.InitialCapital
		e.state.Equity = cfg.InitialCapital
		e.state.Cash = cfg.InitialCapital
		e.state.PeakEquity = cfg.InitialCapital
		e.state.MaxDrawdownPct = 0.0
		e.state.TotalReturnPct = 0.0
		e.state.DailyReturns = nil
		e.state.Positions = make(map[string]*Position)
		if cfg.JournalFile != "" {
			if _, err := os.Stat(cfg.JournalFile); err == nil {
				_ = os.Rename(cfg.JournalFile, cfg.JournalFile+".legacy")
			}
		}
		_ = e.saveCheckpoint()
	}

	// Ensure journal directory exists and start asynchronous background writer
	if cfg.JournalFile != "" {
		_ = sanitizeJournalTail(cfg.JournalFile)
		_ = os.MkdirAll(filepath.Dir(cfg.JournalFile), 0o755)
		f, err := os.OpenFile(cfg.JournalFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, fmt.Errorf("failed to open dry run journal %s: %w", cfg.JournalFile, err)
		}
		e.journal = f
		e.journalCh = make(chan []byte, 1024)
		e.journalWg.Add(1)
		go e.runJournalWriter()
	}

	if e.macroGuardian != nil {
		e.state.Macro = e.macroGuardian.Evaluate(time.Now())
	}
	if e.quantOrch != nil {
		rep := e.quantOrch.Report(time.Now())
		e.state.QuantOrchestrator = &rep
	}
	e.updateTaxReservesLocked()

	return e, nil
}

// makerFeeBps returns the effective exchange maker fee for a given instrument.
func (e *DryRunEngine) makerFeeBps(symbol string) float64 {
	if e.cfg.ZeroFeeMaker {
		u := strings.ToUpper(symbol)
		if strings.HasPrefix(u, "BTC") {
			return 0.0 // 0% fee on Binance.US Tier 0 BTC pairs
		}
	}
	return e.cfg.MakerFeeBps
}

// updateTaxReservesLocked recalculates estimated tax liabilities and post-tax returns.
// Caller must hold e.mu.
func (e *DryRunEngine) updateTaxReservesLocked() {
	e.state.SpotOnly = e.cfg.SpotOnly
	e.state.TaxRatePct = e.cfg.TaxRatePct
	if e.cfg.TaxRatePct > 0 {
		if e.state.RealizedPnL > 0 {
			e.state.EstimatedTaxUSDT = e.state.RealizedPnL * (e.cfg.TaxRatePct * 0.01)
		} else {
			e.state.EstimatedTaxUSDT = 0.0
		}
		e.state.AfterTaxEquity = e.state.Equity - e.state.EstimatedTaxUSDT
		if e.state.InitialCapital > 0 {
			e.state.AfterTaxReturnPct = (e.state.AfterTaxEquity - e.state.InitialCapital) / e.state.InitialCapital * 100.0
		}
	} else {
		e.state.AfterTaxEquity = e.state.Equity
		e.state.AfterTaxReturnPct = e.state.TotalReturnPct
	}
}

// Start launches the strategy evaluation loop, periodic state checkpointing, and independent feed watchdog.
func (e *DryRunEngine) Start(ctx context.Context) {
	if e.running.Swap(true) {
		return
	}

	if e.macroGuardian != nil {
		e.macroGuardian.Start(ctx)
	}

	e.wg.Add(3)
	go e.runExecutionLoop(ctx)
	go e.runCheckpointLoop(ctx)
	go e.runFeedWatchdog(ctx)
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
		e.emergencyFlattenLocked("Shutdown_Auto_TS_Flatten")
	}
	e.mu.Unlock()

	_ = e.saveCheckpoint()
	if e.journalCh != nil {
		close(e.journalCh)
		e.journalWg.Wait()
		e.journalCh = nil
		e.journal = nil
	} else if e.journal != nil {
		_ = e.journal.Close()
		e.journal = nil
	}
}

// emergencyFlattenLocked liquidates all open inventory at current mark price and converts unrealized PnL to cash.
// Caller MUST hold e.mu.
func (e *DryRunEngine) emergencyFlattenLocked(reason string) {
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
			feeUSDT := valUSDT * (e.cfg.TakerFeeBps * 1e-4)
			netRealizedPnL := realizedPnL - feeUSDT
			e.state.Cash += netRealizedPnL

			pos.RealizedPnL += netRealizedPnL
			pos.UnrealizedPnL = 0
			pos.Size = 0
			pos.TotalTrades++
			pos.TakerTrades++
			pos.TotalVolumeUSDT += valUSDT

			e.state.RealizedPnL += netRealizedPnL
			e.state.TotalTrades++
			if netRealizedPnL > 0 {
				e.state.WinningTrades++
			} else if netRealizedPnL < 0 {
				e.state.LosingTrades++
			}

			// Record liquidation fill in journal/blotter with proper standard BUY/SELL side
			liqSide := "SELL"
			if pos.Size < 0 {
				liqSide = "BUY"
			}
			fillRec := TradeRecord{
				Timestamp:   time.Now().UTC(),
				Symbol:      sym,
				Side:        liqSide,
				Role:        "TAKER_SAFETY",
				Price:       closePrice,
				Size:        qty,
				ValueUSDT:   valUSDT,
				FeeUSDT:     feeUSDT,
				EdgeBps:     0.0,
				SlippageBps: 0.0,
				PnLRealized: netRealizedPnL,
				Strategy:    "EMERGENCY_PROTECTIVE_LIQUIDATE: " + reason,
				DataSource:  e.state.DataSource,
			}
			e.recentFills = append([]TradeRecord{fillRec}, e.recentFills...)
			if len(e.recentFills) > 64 {
				e.recentFills = e.recentFills[:64]
			}
			if e.journalCh != nil {
				if b, err := json.Marshal(fillRec); err == nil {
					select {
					case e.journalCh <- append(b, '\n'):
					default:
					}
				}
			}
		}
	}
	// Reset unrealized PnL and lock equity into cash
	e.state.UnrealizedPnL = 0
	e.state.Equity = e.state.Cash
	e.updateTaxReservesLocked()
}

// runJournalWriter provides non-blocking, asynchronous serialization of trade records to the journal file
// with periodic fsync flushes to physical storage to guard against power-cut data loss.
func (e *DryRunEngine) runJournalWriter() {
	defer e.journalWg.Done()
	if e.journal == nil {
		return
	}
	syncTicker := time.NewTicker(2 * time.Second)
	defer syncTicker.Stop()

	for {
		select {
		case line, ok := <-e.journalCh:
			if !ok {
				_ = e.journal.Sync()
				_ = e.journal.Close()
				return
			}
			_, _ = e.journal.Write(line)
		case <-syncTicker.C:
			_ = e.journal.Sync()
		}
	}
}

// runFeedWatchdog independently monitors market feed liveness and executes Cancel-On-Disconnect
// to purge resting maker orders whenever the feed drops, stalls, or disconnects.
func (e *DryRunEngine) runFeedWatchdog(ctx context.Context) {
	defer e.wg.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case <-ticker.C:
			e.checkFeedLiveness()
		}
	}
}

func (e *DryRunEngine) checkFeedLiveness() {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()
	isLiveConnected := true
	if e.stream != nil {
		isLiveConnected = e.stream.IsConnected()
	}

	feedStale := e.lastTickTime.IsZero() || now.Sub(e.lastTickTime) > 1500*time.Millisecond
	if !isLiveConnected || feedStale {
		if !e.state.QuotingPaused {
			e.state.QuotingPaused = true
			e.state.DataSource = "disconnected"
			// Cancel-On-Disconnect: immediately purge all resting maker orders so
			// no stale quotes execute if the connection drops.
			for sym := range e.activeOrders {
				e.activeOrders[sym] = nil
			}
		}
		e.state.FeedDownSeconds += 0.10
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

	if e.cbOracle != nil {
		cbSnap := e.cbOracle.Snapshot()
		cp.Coinbase = &cbSnap
	}

	// Populate per-symbol scalping regime state for UI & telemetry observability
	cp.ScalpingState = make(map[string]ScalpingRegimeState, len(e.symMicro))
	for sym, sm := range e.symMicro {
		regime := "ACTIVE_SCALP"
		if sm.ScalpingDisabled {
			if sm.ConsecutiveLosses >= 4 {
				regime = "DISABLED_LEAK_GUARD"
			} else {
				regime = "PATIENT_SWING"
			}
		}
		winRate := 0.0
		sumPnL := 0.0
		if len(sm.RecentPnlWindow) > 0 {
			losses := 0
			for _, p := range sm.RecentPnlWindow {
				sumPnL += p
				if p < 0 {
					losses++
				}
			}
			winRate = float64(len(sm.RecentPnlWindow)-losses) / float64(len(sm.RecentPnlWindow)) * 100.0
		}
		cp.ScalpingState[sym] = ScalpingRegimeState{
			ScalpingEnabled:  !sm.ScalpingDisabled,
			Regime:           regime,
			Reason:           sm.ScalpingDisabledReason,
			ConsecutiveLoss:  sm.ConsecutiveLosses,
			RollingWinRate:   winRate,
			RollingPnlUSDT:   sumPnL,
			TargetInventory:  sm.LatestTargetInv,
			FairValue:        sm.LatestFairValue,
			MultiFactorScore: sm.LatestMultiFactor.CompositeScore,
			MarketRegime:     string(sm.LatestMultiFactor.Regime),
		}
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
	if e.cfg.DataDir != "" {
		candidates = append(candidates,
			filepath.Join(e.cfg.DataDir, "config/optimal_params.json"),
			filepath.Join(e.cfg.DataDir, "optimal_params.json"),
		)
	}
	if e.cfg.StateFile != "" {
		candidates = append(candidates,
			filepath.Join(filepath.Dir(e.cfg.StateFile), "../config/optimal_params.json"),
			filepath.Join(filepath.Dir(e.cfg.StateFile), "config/optimal_params.json"),
			filepath.Join(filepath.Dir(e.cfg.StateFile), "optimal_params.json"),
			filepath.Join(filepath.Dir(filepath.Dir(e.cfg.StateFile)), "optimal_params.json"),
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
	if !math.IsNaN(params.Gamma) && !math.IsInf(params.Gamma, 0) && params.Gamma > 0 && e.glftMM != nil {
		gamma := params.Gamma
		if gamma < 0.0001 {
			gamma = 0.0001
		} else if gamma > 0.05 {
			gamma = 0.05
		}
		e.glftMM.Gamma = gamma
	}
	if !math.IsNaN(params.MinHalfSpreadBps) && !math.IsInf(params.MinHalfSpreadBps, 0) && params.MinHalfSpreadBps > 0 {
		mhs := params.MinHalfSpreadBps
		if mhs < 0.5 {
			mhs = 0.5
		} else if mhs > 10.0 {
			mhs = 10.0
		}
		e.minHalfSpreadFloorBps = mhs
	}
	if e.leverageCtrl != nil {
		e.leverageCtrl.UpdateCloudParams(params.BaseLeverage, params.MaxLeverageCap, params.ClipFraction)
	}
	e.mu.Unlock()
}

// SetAutonomousRecalibrator configures the autonomous backtest grid search runner.
func (e *DryRunEngine) SetAutonomousRecalibrator(fn AutonomousRecalibrator) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.recalibrator = fn
}

// SetCoinbaseOracle configures the real-time institutional USD lead-lag oracle.
func (e *DryRunEngine) SetCoinbaseOracle(oracle *CoinbaseSignalOracle) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cbOracle = oracle
}

// maybeTriggerAutoRecalibrationLocked asynchronously executes a fast walk-forward backtest grid search
// and hot-reloads optimal parameters when money leakage or consecutive losses occur.
// Caller must hold e.mu.
func (e *DryRunEngine) maybeTriggerAutoRecalibrationLocked(symbol string) {
	recal := e.recalibrator
	last := e.lastAutoRecalib

	if recal == nil || e.recalibrating.Load() {
		return
	}
	now := time.Now()
	if now.Sub(last) < 3*time.Minute {
		return // Cooldown: at most once every 3 minutes
	}

	e.recalibrating.Store(true)
	e.lastAutoRecalib = now
	e.state.SelfAdaptation.Status = "AUTONOMOUS_TUNING"

	capVal := e.state.Equity
	dDir := e.cfg.DataDir

	go func() {
		defer e.recalibrating.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		err := recal(ctx, symbol, capVal, dDir)
		if err != nil {
			fmt.Printf("[AUTO-ADAPT] %s: Recalibration error: %v\n", symbol, err)
			e.mu.Lock()
			e.state.SelfAdaptation.Status = "RECALIB_ERROR: " + err.Error()
			e.mu.Unlock()
			return
		}

		// Hot reload the newly written parameters immediately
		e.maybeReloadCloudParams()

		e.mu.Lock()
		e.state.SelfAdaptation.Status = "HOT_RELOADED"
		e.state.SelfAdaptation.LastRecalibration = time.Now()
		e.state.SelfAdaptation.RecalibrationCount++
		if sm, ok := e.symMicro[symbol]; ok {
			sm.ConsecutiveLosses = 0
			sm.RecentPnlWindow = nil
		}
		e.mu.Unlock()
		fmt.Printf("[AUTO-ADAPT] %s: Successfully auto-recalibrated and hot-reloaded new optimal parameters!\n", symbol)
	}()
}

// saveCheckpoint serializes DryRunState to JSON.
func (e *DryRunEngine) saveCheckpoint() error {
	e.mu.Lock()
	now := time.Now()

	// Guard against un-synced clock on Raspberry Pi cold boot without RTC battery (fake-hwclock / 1970)
	minSanityDate := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clockValid := now.After(minSanityDate) && (e.state.LastCheckpoint.IsZero() || !now.Before(e.state.LastCheckpoint))

	if clockValid {
		e.state.LastCheckpoint = now
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

	// Populate per-symbol scalping regime state for observability
	e.state.ScalpingState = make(map[string]ScalpingRegimeState, len(e.symMicro))
	for sym, sm := range e.symMicro {
		regime := "ACTIVE_SCALP"
		if sm.ScalpingDisabled {
			if sm.ConsecutiveLosses >= 4 {
				regime = "DISABLED_LEAK_GUARD"
			} else {
				regime = "PATIENT_SWING"
			}
		}
		winRate := 0.0
		sumPnL := 0.0
		if len(sm.RecentPnlWindow) > 0 {
			losses := 0
			for _, p := range sm.RecentPnlWindow {
				sumPnL += p
				if p < 0 {
					losses++
				}
			}
			winRate = float64(len(sm.RecentPnlWindow)-losses) / float64(len(sm.RecentPnlWindow)) * 100.0
		}
		e.state.ScalpingState[sym] = ScalpingRegimeState{
			ScalpingEnabled:  !sm.ScalpingDisabled,
			Regime:           regime,
			Reason:           sm.ScalpingDisabledReason,
			ConsecutiveLoss:  sm.ConsecutiveLosses,
			RollingWinRate:   winRate,
			RollingPnlUSDT:   sumPnL,
			TargetInventory:  sm.LatestTargetInv,
			FairValue:        sm.LatestFairValue,
			MultiFactorScore: sm.LatestMultiFactor.CompositeScore,
			MarketRegime:     string(sm.LatestMultiFactor.Regime),
		}
	}
	if e.state.SelfAdaptation.Status == "" {
		e.state.SelfAdaptation.Status = "MONITORING_CALM"
	}
	if e.cbOracle != nil {
		cbSnap := e.cbOracle.Snapshot()
		e.state.Coinbase = &cbSnap
	}

	data, err := json.MarshalIndent(e.state, "", "  ")
	e.mu.Unlock()
	if err != nil {
		return err
	}

	if e.cfg.StateFile == "" {
		return nil
	}

	// Rotate existing valid checkpoint to .bak for dual-slot crash safety
	if _, err := os.Stat(e.cfg.StateFile); err == nil {
		_ = os.Rename(e.cfg.StateFile, e.cfg.StateFile+".bak")
	}

	// Write atomically with fsync and directory sync
	return atomicWriteFile(e.cfg.StateFile, data, 0o644)
}

// loadCheckpoint deserializes state from JSON and handles shutdown gap recovery.
func (e *DryRunEngine) loadCheckpoint() error {
	if e.cfg.StateFile == "" {
		return os.ErrNotExist
	}

	var data []byte
	var err error

	data, err = os.ReadFile(e.cfg.StateFile)
	if err != nil && os.IsNotExist(err) {
		// Primary state file not found; attempt .bak fallback
		data, err = os.ReadFile(e.cfg.StateFile + ".bak")
	}
	if err != nil {
		return err
	}

	cleanData := bytes.Trim(data, "\x00\r\n \t")
	var st DryRunState
	var unmarshalErr error
	if len(cleanData) > 0 {
		unmarshalErr = json.Unmarshal(cleanData, &st)
	}

	if len(cleanData) == 0 || unmarshalErr != nil {
		// Primary checkpoint file was zeroed or corrupted (e.g. power-loss mid-write).
		// Attempt recovery from .bak secondary slot
		bakData, bakErr := os.ReadFile(e.cfg.StateFile + ".bak")
		if bakErr == nil {
			cleanBak := bytes.Trim(bakData, "\x00\r\n \t")
			if len(cleanBak) > 0 {
				if err := json.Unmarshal(cleanBak, &st); err == nil {
					unmarshalErr = nil
					_ = os.Rename(e.cfg.StateFile, e.cfg.StateFile+".corrupted")
				}
			}
		}
		if unmarshalErr != nil {
			_ = os.Rename(e.cfg.StateFile, e.cfg.StateFile+".corrupted")
			return os.ErrNotExist
		}
	}

	if st.Positions == nil {
		st.Positions = make(map[string]*Position)
	}

	// Discard resting orders from checkpoint: old orders from prior process run are stale.
	st.ActiveOrders = nil

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
			if math.Abs(newSize) < 1e-7 {
				newSize = 0.0
				pos.AvgEntryPrice = 0.0
			} else if newSize > 0 {
				if pos.Size >= 0 {
					pos.AvgEntryPrice = (pos.AvgEntryPrice*pos.Size + r.Price*r.Size) / newSize
				} else {
					pos.AvgEntryPrice = r.Price
				}
			}
			pos.Size = newSize
		} else if r.Side == "SELL" {
			newSize := pos.Size - r.Size
			if math.Abs(newSize) < 1e-7 {
				newSize = 0.0
				pos.AvgEntryPrice = 0.0
			} else if newSize < 0 {
				if pos.Size <= 0 {
					pos.AvgEntryPrice = (pos.AvgEntryPrice*(-pos.Size) + r.Price*r.Size) / (-newSize)
				} else {
					pos.AvgEntryPrice = r.Price
				}
			}
			pos.Size = newSize
		} else if strings.Contains(r.Side, "LIQUIDATE") {
			pos.Size = 0.0
			pos.AvgEntryPrice = 0.0
		}

		if isClosing {
			if r.PnLRealized > 0 {
				st.WinningTrades++
			} else {
				st.LosingTrades++
			}
		}

		st.Equity = st.Cash
		if st.Equity > st.PeakEquity {
			st.PeakEquity = st.Equity
		}
		if st.PeakEquity > 0 {
			dd := (st.PeakEquity - st.Equity) / st.PeakEquity * 100.0
			if dd > st.MaxDrawdownPct {
				st.MaxDrawdownPct = dd
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

	// If resuming from disconnected/paused state or feed staleness, purge resting orders so
	// the first tick post-reconnection NEVER fills stale orders.
	if e.state.QuotingPaused || (!e.lastTickTime.IsZero() && now.Sub(e.lastTickTime) > 1500*time.Millisecond) {
		for sym := range e.activeOrders {
			e.activeOrders[sym] = nil
		}
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
			e.emergencyFlattenLocked("Circuit_Breaker_Drawdown_Breach")
		}
	}

	if e.state.InitialCapital > 0 {
		e.state.TotalReturnPct = (e.state.Equity - e.state.InitialCapital) / e.state.InitialCapital * 100.0
	}
	e.updateTaxReservesLocked()
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
		isBuyerMaker := (ev.Side == 2)
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
	if e.state.QuotingPaused || (!e.lastTickTime.IsZero() && now.Sub(e.lastTickTime) > 1500*time.Millisecond) {
		e.activeOrders[symbol] = nil
		e.mu.Unlock()
		return
	}
	orders := e.activeOrders[symbol]
	if len(orders) == 0 {
		e.mu.Unlock()
		return
	}

	var remaining []*ActiveMakerOrder
	for _, ord := range orders {
		// Cancel propagation delay: if order was canceled and cancel transit duration has passed, prune it
		if !ord.CancelingUntil.IsZero() && now.After(ord.CancelingUntil) {
			continue
		}

		// Feed + order latency: resting order does not participate in matching before ActiveAt
		if now.Before(ord.ActiveAt) {
			remaining = append(remaining, ord)
			continue
		}

		filled := false
		// Strict hftbacktest fill realism:
		// 1. Price-through tolerance is half a tick, calibrated per symbol (e.g. $0.051 for BTCUSDT, $0.0051 for ETHUSDT).
		// 2. Trade aggressor must be on the opposite side of our resting limit order:
		//    - Resting BUY bid fills against SELLER-initiated trades (tradeSide == 2, isBuyerMaker == true) or downward price sweep.
		//    - Resting SELL ask fills against BUYER-initiated trades (tradeSide == 1, isBuyerMaker == false) or upward price sweep.
		tick := symbolTickSize(symbol)
		tickTol := tick * 0.51
		if ord.Side == "BUY" && (tradeSide == 2 || tradePrice < ord.Price-tickTol) {
			if tradePrice < ord.Price-tickTol {
				// Market swept completely through our bid price level
				filled = true
			} else if math.Abs(tradePrice-ord.Price) <= tickTol {
				ord.QueueAhead -= tradeSize
				if ord.QueueAhead <= 0 {
					filled = true
				}
			}
		} else if ord.Side == "SELL" && (tradeSide == 1 || tradePrice > ord.Price+tickTol) {
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

	// Multi-Horizon Quant Alpha Tracking (1m, 5m, 15m returns and 15m ATR)
	if midPrice > 0 {
		nHist := len(sm.MidHistory)
		if nHist == 0 || now.Sub(sm.MidHistory[nHist-1].Time) >= 500*time.Millisecond {
			sm.MidHistory = append(sm.MidHistory, MidPricePoint{Time: now, Mid: midPrice})
		}
		cutoff20m := now.Add(-20 * time.Minute)
		trimIdx := 0
		for trimIdx < len(sm.MidHistory) && sm.MidHistory[trimIdx].Time.Before(cutoff20m) {
			trimIdx++
		}
		if trimIdx > 0 {
			sm.MidHistory = sm.MidHistory[trimIdx:]
		}

		t1m := now.Add(-1 * time.Minute)
		t5m := now.Add(-5 * time.Minute)
		t15m := now.Add(-15 * time.Minute)
		var p1m, p5m, p15m float64
		high15m := midPrice
		low15m := midPrice

		for i := len(sm.MidHistory) - 1; i >= 0; i-- {
			pt := sm.MidHistory[i]
			if pt.Mid > high15m {
				high15m = pt.Mid
			}
			if pt.Mid < low15m {
				low15m = pt.Mid
			}
			if p1m == 0 && !pt.Time.After(t1m) {
				p1m = pt.Mid
			}
			if p5m == 0 && !pt.Time.After(t5m) {
				p5m = pt.Mid
			}
			if p15m == 0 && !pt.Time.After(t15m) {
				p15m = pt.Mid
			}
		}

		if p1m > 0 {
			sm.Ret1mBps = (midPrice - p1m) / p1m * 1e4
		} else if sm.PrevMid > 0 {
			sm.Ret1mBps = sm.Ret5sBps * 3.5
		}
		if p5m > 0 {
			sm.Ret5mBps = (midPrice - p5m) / p5m * 1e4
		} else {
			sm.Ret5mBps = sm.Ret1mBps * 1.8
		}
		if p15m > 0 {
			sm.Ret15mBps = (midPrice - p15m) / p15m * 1e4
		} else {
			sm.Ret15mBps = sm.Ret5mBps * 1.4
		}

		atrSpread := high15m - low15m
		if atrSpread > 0 && midPrice > 0 {
			calcAtr := (atrSpread / midPrice) * 1e4
			if calcAtr >= 10.0 {
				sm.AtrBps = 0.95*sm.AtrBps + 0.05*calcAtr
			}
		}
		if sm.AtrBps < 15.0 {
			sm.AtrBps = 15.0
		}
	}
	if hasLiveBook {
		sm.OBI = imbL5
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
			for s := range e.activeOrders {
				e.activeOrders[s] = nil
			}
			e.emergencyFlattenLocked("Circuit_Breaker_RiskGate_Tripped")
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
	posSnapshot := make(map[string]float64)
	marksSnapshot := make(map[string]float64)
	for s, p := range e.state.Positions {
		if p != nil {
			posSnapshot[s] = p.Size
			marksSnapshot[s] = p.MarkPrice
		}
	}
	e.mu.RUnlock()
	marksSnapshot[symbol] = midPrice

	liveFundingAPR := 0.0
	if sm.FundingTracker != nil {
		perpRef := midPrice
		if e.cbOracle != nil {
			cbSig := e.cbOracle.EvaluateAgainstBinance(symbol, midPrice, now)
			if cbSig.CoinbasePrice > 0 {
				perpRef = cbSig.CoinbasePrice
			}
		}
		sm.FundingTracker.UpdatePrices(perpRef, midPrice, now.UnixNano())
		liveFundingAPR = sm.FundingTracker.AnnualizedAPR()
	}

	engineLatency := e.cfg.OrderLatency
	if engineLatency <= 0 {
		engineLatency = 120 * time.Millisecond
	}
	if e.quantOrch != nil {
		e.quantOrch.RecordLatencyUpdate(engineLatency, liveFundingAPR, now)
	}
	if e.preTradeGuard != nil {
		e.preTradeGuard.UpdatePortfolioState(currEq, peakEq, posSnapshot, marksSnapshot, now)
		if isTripped, tripReason, _, _, _, _ := e.preTradeGuard.Status(); isTripped && !isHalted {
			e.mu.Lock()
			e.state.CircuitHalted = true
			e.state.CircuitHaltReason = "PreTradeGuard: " + tripReason
			for s := range e.activeOrders {
				e.activeOrders[s] = nil
			}
			e.emergencyFlattenLocked("PreTradeGuard_KillSwitch_Tripped")
			e.mu.Unlock()
			return
		}
	}

	ddPct := 0.0
	if peakEq > 0 {
		ddPct = (peakEq - currEq) / peakEq * 100.0
	}
	levState := e.leverageCtrl.Compute(spreadBps, liveVpin, hkRatio, ddPct, isHalted)

	// Compute institutional volatility-targeted Fractional Kelly clip size and max symbol inventory cap
	clipQty, maxInvQty := e.leverageCtrl.ComputeVolatilityKellySize(symbol, currEq, midPrice, sm.AtrBps, e.cfg.StopLossBps, len(e.cfg.Symbols))
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
		if math.Abs(pos.Size) < 1e-7 {
			pos.Size = 0.0
			pos.AvgEntryPrice = 0.0
		}
		currentInv = pos.Size
	}
	if math.Abs(currentInv) > 1e-7 {
		if sm.InventorySince.IsZero() {
			sm.InventorySince = now
		}
	} else {
		sm.InventorySince = time.Time{}
	}
	e.mu.Unlock()

	// Tribeca Institutional Dynamic Target Inventory (q*):
	// Sized by directional alpha conviction and high funding carry APR.
	targetInv := 0.0
	if sm.FairValueEngine != nil {
		if maxInvQty > 0 {
			sm.FairValueEngine.MaxInventoryCap = maxInvQty
		}
		targetInv = sm.FairValueEngine.ComputeTargetInventory(sm.Conviction, liveFundingAPR, e.cfg.SpotOnly)
	}

	// Inventory discrepancy relative to institutional target inventory q* (Tribeca model):
	// When on target (currentInv == targetInv), restoring skew is ZERO, allowing the fund
	// to hold structural swing positions for days/weeks without fee churn!
	invDeviation := currentInv - targetInv
	normInv := 0.0
	if maxInvQty > 0 {
		normInv = (invDeviation / maxInvQty) * 5.0
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
	// Anti-overfitting: clamp individual expert outputs to realistic 1-tick bounds [-15, +15] bps
	// to prevent rogue outlier feature spikes from corrupting the online ensemble weights.
	for i := 0; i < common.MaxEnsembleExperts; i++ {
		if currPreds[i] > 15.0 {
			currPreds[i] = 15.0
		} else if currPreds[i] < -15.0 {
			currPreds[i] = -15.0
		}
	}

	e.mu.Lock()
	var metaPred float64
	if sm.HasPrevPreds {
		metaPred, _ = sm.AdaHedge.UpdateAndCombine(tickRetBps, sm.PrevPreds, currPreds)
		_, _ = e.adaHedge.UpdateAndCombine(tickRetBps, sm.PrevPreds, currPreds)
	} else {
		metaPred = qd.ReservationBps
	}

	var driftWidenBps float64
	if e.driftGuard != nil && sm.HasPrevPreds {
		resBps := tickRetBps - metaPred
		var featSq float64
		for _, fv := range x {
			featSq += fv * fv
		}
		featNorm := math.Sqrt(featSq)
		driftDetected, stat, thresh := e.driftGuard.Update(resBps, featNorm)
		if driftDetected {
			// Statistical concept drift detected: live market distribution has departed from training distribution
			// (overfitting to prior regime). Widen quotes defensively and trigger autonomous walk-forward recalibration.
			driftWidenBps = math.Min(2.5, (stat-thresh)*0.20)
			e.maybeTriggerAutoRecalibrationLocked(symbol)
		}
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

	// Proportional inventory skew restoring force relative to institutional target q*:
	// In Avellaneda-Stoikov, inventory risk scales with gamma * sigma^2.
	// When currentInv == targetInv, invFraction is ZERO, so restoring force is ZERO!
	var invSkewBps float64
	if maxInvQty > 0 {
		invFraction := invDeviation / maxInvQty // [-1.0, 1.0] relative to target q*
		if invFraction > 1.0 {
			invFraction = 1.0
		} else if invFraction < -1.0 {
			invFraction = -1.0
		}
		volMult := 1.0
		if sm.VolVarBps > 0.40 {
			volMult = math.Min(2.0, sm.VolVarBps/0.40)
		}
		invSkewBps = invFraction * 2.5 * volMult // Volatility-scaled inventory restoring force relative to target
	}

	// Blend AdaHedge online meta-prediction with GLFT reservation skew and Coinbase institutional lead-lag skew
	var cbSig CoinbaseSymbolSignal
	if e.cbOracle != nil {
		cbSig = e.cbOracle.EvaluateAgainstBinance(symbol, midPrice, now)
	}
	blendedSkewBps := 0.60*(qd.ReservationBps-invSkewBps) + 0.25*metaPred + 0.15*cbSig.LeadLagSkewBps

	// Multi-Horizon Directional Conviction score P(Up) in [0.0, 1.0]
	// Combines 1m, 5m, 15m return momentum, Coinbase institutional lead-lag drift,
	// AdaHedge meta-ensemble prediction, Order Book Imbalance (OBI), and QuantMuse Multi-Factor.
	var mfScore alpha.MultiFactorScore
	if sm.MultiFactor != nil {
		mfScore = sm.MultiFactor.Update(midPrice, bidSize+askSize, liveVpin, spreadBps, now)
		sm.LatestMultiFactor = mfScore
	}

	zMom := 0.20*(sm.Ret1mBps/15.0) + 0.45*(sm.Ret5mBps/25.0) + 0.35*(sm.Ret15mBps/40.0)
	zLeadLag := cbSig.LeadLagSkewBps / 3.0
	if zLeadLag > 2.5 {
		zLeadLag = 2.5
	} else if zLeadLag < -2.5 {
		zLeadLag = -2.5
	}
	zMeta := metaPred / 4.0
	if zMeta > 2.5 {
		zMeta = 2.5
	} else if zMeta < -2.5 {
		zMeta = -2.5
	}
	zOBI := sm.OBI / 0.40
	if zOBI > 2.5 {
		zOBI = 2.5
	} else if zOBI < -2.5 {
		zOBI = -2.5
	}
	zMF := mfScore.CompositeScore * 2.0
	if zMF > 2.5 {
		zMF = 2.5
	} else if zMF < -2.5 {
		zMF = -2.5
	}
	zTotal := 0.25*zMom + 0.20*zLeadLag + 0.20*zMeta + 0.15*zOBI + 0.20*zMF
	convictionUp := 1.0 / (1.0 + math.Exp(-1.8*zTotal))
	if sm.Conviction > 0 && math.Abs(sm.Conviction-0.50) > 1e-4 {
		convictionUp = sm.Conviction
	}
	if convictionUp > 0.99 {
		convictionUp = 0.99
	} else if convictionUp < 0.01 {
		convictionUp = 0.01
	}
	sm.Conviction = convictionUp
	if sm.FairValueEngine != nil {
		targetInv = sm.FairValueEngine.ComputeTargetInventory(sm.Conviction, liveFundingAPR, e.cfg.SpotOnly)
		invDeviation = currentInv - targetInv
	}

	bidOffsetBps := math.Max(floorBps, qd.HalfSpreadBps-blendedSkewBps) + tcaPenaltyBps + driftWidenBps
	askOffsetBps := math.Max(floorBps, qd.HalfSpreadBps+blendedSkewBps) + tcaPenaltyBps + driftWidenBps

	// Multi-Factor Toxic Regime Defense (QuantMuse):
	// If multi-factor classifier flags toxic order flow, widen quotes defensively
	if mfScore.Regime == alpha.RegimeHighVolToxic {
		toxicWidenBps := math.Max(2.0, spreadBps*0.50)
		bidOffsetBps += toxicWidenBps
		askOffsetBps += toxicWidenBps
	}

	// Apply Macro Event Shield spread scaling (e.g. 2.5x pre-event, 1.5x post-event drift)
	if macroSt.SpreadMultiplier > 1.0 {
		bidOffsetBps *= macroSt.SpreadMultiplier
		askOffsetBps *= macroSt.SpreadMultiplier
	}

	// Volatility-Adaptive Entry Spread (Optimal Microstructure Formula):
	// Entry quotes are placed at asset-calibrated liquidity discount levels:
	// BTC (deeper order book, lower vol) quotes tighter (0.90x),
	// ETH (wider order book, higher vol) quotes wider (1.15x).
	// Both scale with real-time volatility variance sm.VolVarBps.
	entrySpreadBps := e.cfg.EntryOffsetBps
	if entrySpreadBps <= 0 {
		entrySpreadBps = 1.75
	}
	symMultiplier := 1.0
	uSym := strings.ToUpper(symbol)
	if strings.Contains(uSym, "BTC") {
		symMultiplier = 0.90
	} else if strings.Contains(uSym, "ETH") {
		symMultiplier = 1.15
	}
	// USDC order books are thinner with higher jump variance: scale entry spread floor by 1.25x
	// to capture wider natural spreads (3.5 - 8.0 bps) while maintaining identical accuracy and win rate.
	if strings.HasSuffix(uSym, "USDC") {
		symMultiplier *= 1.25
	}
	effectiveEntryBps := entrySpreadBps * symMultiplier
	if sm.VolVarBps > 0.40 {
		volScale := math.Min(2.5, math.Sqrt(sm.VolVarBps/0.40))
		effectiveEntryBps *= volScale
	}
	// Dynamic Hurdle: ensure fee floor covers round-trip maker fee if applicable.
	// On Binance.US zero-fee BTC pairs (makerFee == 0), min hurdle is agile (1.25 bps).
	// On standard fee-bearing pairs (makerFee > 0), hurdle guarantees fee clearance (2.0 * makerFee + 1.25 bps).
	curMakerFee := e.makerFeeBps(symbol)
	minHurdleBps := 1.25
	if curMakerFee > 0 {
		minHurdleBps = (2.0 * curMakerFee) + 1.25
	}
	feeFloorBps := math.Max(floorBps, math.Max(minHurdleBps, effectiveEntryBps))
	if bidOffsetBps < feeFloorBps {
		bidOffsetBps = feeFloorBps
	}
	if askOffsetBps < feeFloorBps {
		askOffsetBps = feeFloorBps
	}

	// Self-Improving Scalping Defense:
	// If scalping ain't working (leakage / adverse markouts / consecutive losses):
	// Disable tight micro-scalping entirely and transition to Patient Swing Liquidity Provision!
	if sm.ScalpingDisabled {
		swingOffsetBps := math.Max(18.0, feeFloorBps*2.5)
		if bidOffsetBps < swingOffsetBps {
			bidOffsetBps = swingOffsetBps
		}
		if askOffsetBps < swingOffsetBps {
			askOffsetBps = swingOffsetBps
		}
	}

	// Monthly Gain Cap Enforcement:
	// If cumulative return in current evaluation window has reached target ceiling (default 15.0%),
	// enter Capital Preservation Mode: widen quotes and scale clip size down to strictly preserve banked gains!
	gainCapPct := e.cfg.MonthlyGainCapPct
	if gainCapPct <= 0 {
		gainCapPct = 15.0
	}
	if e.state.TotalReturnPct >= gainCapPct {
		capOffsetBps := math.Max(25.0, feeFloorBps*3.5)
		if bidOffsetBps < capOffsetBps {
			bidOffsetBps = capOffsetBps
		}
		if askOffsetBps < capOffsetBps {
			askOffsetBps = capOffsetBps
		}
		clipQty *= 0.25 // Reduce clip size by 75%
	}

	var top5BidsPrice, top5BidsSize []float64
	var top5AsksPrice, top5AsksSize []float64
	if hasLiveBook {
		for i := 0; i < 5 && curBids[i].Price > 0; i++ {
			sm.top5BidsP[i] = curBids[i].Price
			sm.top5BidsS[i] = curBids[i].Size
		}
		for i := 0; i < 5 && curAsks[i].Price > 0; i++ {
			sm.top5AsksP[i] = curAsks[i].Price
			sm.top5AsksS[i] = curAsks[i].Size
		}
		top5BidsPrice = sm.top5BidsP[:5]
		top5BidsSize = sm.top5BidsS[:5]
		top5AsksPrice = sm.top5AsksP[:5]
		top5AsksSize = sm.top5AsksS[:5]
	}
	fairVal := midPrice
	if sm.FairValueEngine != nil {
		cbPrice := 0.0
		if cbSig.CoinbasePrice > 0 {
			cbPrice = cbSig.CoinbasePrice
		}
		fv := sm.FairValueEngine.ComputeFairValue(
			bestBid, bestAsk, bidSize, askSize,
			top5BidsPrice, top5BidsSize,
			top5AsksPrice, top5AsksSize,
			cbPrice,
		)
		if fv > 0 && !math.IsNaN(fv) && !math.IsInf(fv, 0) {
			fairVal = fv
		}
	}
	sm.LatestFairValue = fairVal
	sm.LatestTargetInv = targetInv

	centerPrice := midPrice
	if fairVal > 0 && !math.IsNaN(fairVal) && !math.IsInf(fairVal, 0) {
		centerPrice = fairVal
	}
	targetBid := centerPrice - (bidOffsetBps * centerPrice * 1e-4)
	targetAsk := centerPrice + (askOffsetBps * centerPrice * 1e-4)

	// Institutional Breakeven Floor & Dynamic Profit Decay Ratchet:
	// When holding open inventory, the closing quote guarantees net profitability
	// after covering all round-trip maker fees (2 * MakerFeeBps).
	// A dynamic exponential decay ratchet starts quoting at base target profit (default 50.0 bps)
	// to capture swing momentum, then smoothly relaxes over holding time (30m half-life)
	// towards guaranteed net profit floor (+25.0 bps net = 28.0 bps gross),
	// ensuring positions never get trapped during quiet consolidation while ALWAYS banking green profits.
	// An exit is ONLY allowed below break-even if structural stop-loss or confirmed toxic flow triggers.
	if pos != nil && math.Abs(pos.Size) > 1e-8 && pos.AvgEntryPrice > 0 {
		baseTargetProfitBps := e.cfg.TargetProfitBps
		if baseTargetProfitBps <= 0 {
			baseTargetProfitBps = 50.0
		}
		if sm.VolVarBps > 0.40 {
			baseTargetProfitBps *= math.Min(2.0, math.Sqrt(sm.VolVarBps/0.40))
		}
		stopLossBps := e.cfg.StopLossBps
		if stopLossBps <= 0 {
			stopLossBps = 60.0
		}
		roundTripFeeBps := 2.0 * e.makerFeeBps(symbol)

		minNetFloorBps := math.Max(6.0, baseTargetProfitBps*0.50)
		targetProfitBps := minNetFloorBps
		holdSec := 0.0
		if !sm.InventorySince.IsZero() {
			holdSec = now.Sub(sm.InventorySince).Seconds()
			decayFactor := math.Exp(-holdSec / 900.0) // 15-minute half-life decay
			targetProfitBps = minNetFloorBps + (baseTargetProfitBps-minNetFloorBps)*decayFactor
		} else {
			targetProfitBps = baseTargetProfitBps
		}

		if pos.Size > 0 { // Holding LONG position
			minProfitAsk := pos.AvgEntryPrice * (1.0 + (roundTripFeeBps+targetProfitBps)*1e-4)
			adverseDrawdownBps := (pos.AvgEntryPrice - midPrice) / pos.AvgEntryPrice * 1e4

			// Asymmetric Hysteresis Exit Conditions:
			// 1. Structural stop loss >= 60.0 bps
			// 2. Confirmed toxic cascade (VPIN > 0.75 or Hawkes >= 0.85) with adverse drawdown > 25.0 bps
			// 3. Decisive structural invalidation (Conviction < 0.38 && OBI < -0.25 && Ret5mBps < -10.0 bps)
			if adverseDrawdownBps > stopLossBps {
				// Structural invalidation: stop-loss triggered to protect capital against sustained crashes
				if bestAsk > 0 {
					targetAsk = bestAsk
				}
				sm.BidCooldownUntil = now.Add(45 * time.Second)
			} else if adverseDrawdownBps > 25.0 && ((sm.VpinCalc != nil && sm.VpinCalc.VPIN() > 0.75) || (sm.HawkesEst != nil && sm.HawkesEst.BranchingRatio() >= 0.85)) {
				// Informed Toxic Cascade Defense: exit passively at touch only when order flow is demonstrably toxic
				if bestAsk > 0 {
					targetAsk = bestAsk
				} else {
					targetAsk = midPrice
				}
				sm.BidCooldownUntil = now.Add(45 * time.Second)
			} else if adverseDrawdownBps > 20.0 && convictionUp < 0.38 && sm.OBI < -0.25 && sm.Ret5mBps < -10.0 {
				// Decisive multi-horizon alpha invalidation (asymmetric hysteresis exit)
				if bestAsk > 0 {
					targetAsk = bestAsk
				} else {
					targetAsk = midPrice
				}
				sm.BidCooldownUntil = now.Add(45 * time.Second)
			} else {
				// Normal market oscillation / swing holding: quote at or above minProfitAsk
				if targetAsk < minProfitAsk {
					targetAsk = minProfitAsk
				}
				// Profit-locking ratchet: if market surged into confirmed net profit:
				// In calm or neutral flow, capture the inside ask immediately.
				// In strong upward momentum (blendedSkewBps > 1.0 or conviction > 0.65), trail the ask higher with momentum skew
				// to capture outsized runner profits (up to 100-120 bps) instead of exiting prematurely!
				if bestAsk >= minProfitAsk {
					targetAsk = bestAsk
					if blendedSkewBps > 1.0 || convictionUp > 0.65 {
						skewBonus := math.Max(blendedSkewBps, (convictionUp-0.5)*10.0)
						momentumRunnerAsk := bestAsk * (1.0 + (skewBonus * 0.50 * 1e-4))
						if momentumRunnerAsk > targetAsk {
							targetAsk = momentumRunnerAsk
						}
					}
				}
			}
		} else if pos.Size < 0 { // Holding SHORT position
			maxProfitBid := pos.AvgEntryPrice * (1.0 - (roundTripFeeBps+targetProfitBps)*1e-4)
			adverseDrawdownBps := (midPrice - pos.AvgEntryPrice) / pos.AvgEntryPrice * 1e4

			if adverseDrawdownBps > stopLossBps {
				// Structural invalidation: stop-loss triggered to protect capital
				if bestBid > 0 {
					targetBid = bestBid
				}
				sm.AskCooldownUntil = now.Add(45 * time.Second)
			} else if adverseDrawdownBps > 25.0 && ((sm.VpinCalc != nil && sm.VpinCalc.VPIN() > 0.75) || (sm.HawkesEst != nil && sm.HawkesEst.BranchingRatio() >= 0.85)) {
				// Informed Toxic Cascade Defense: exit passively at touch only when order flow is demonstrably toxic
				if bestBid > 0 {
					targetBid = bestBid
				} else {
					targetBid = midPrice
				}
				sm.AskCooldownUntil = now.Add(45 * time.Second)
			} else if adverseDrawdownBps > 20.0 && convictionUp > 0.62 && sm.OBI > 0.25 && sm.Ret5mBps > 10.0 {
				// Decisive multi-horizon alpha invalidation for short
				if bestBid > 0 {
					targetBid = bestBid
				} else {
					targetBid = midPrice
				}
				sm.AskCooldownUntil = now.Add(45 * time.Second)
			} else {
				// Normal market oscillation / swing holding: cover at or below maxProfitBid
				if targetBid > maxProfitBid {
					targetBid = maxProfitBid
				}
				// Profit-locking ratchet: if market dumped into confirmed net profit:
				// In calm or neutral flow, capture the inside bid immediately.
				// In strong downward momentum (blendedSkewBps < -1.0 or conviction < 0.35), trail the bid lower with momentum skew
				// to capture runner profits instead of covering prematurely!
				if bestBid <= maxProfitBid && bestBid > 0 {
					targetBid = bestBid
					if blendedSkewBps < -1.0 || convictionUp < 0.35 {
						skewBonus := math.Max(math.Abs(blendedSkewBps), (0.5-convictionUp)*10.0)
						momentumRunnerBid := bestBid * (1.0 - (skewBonus * 0.50 * 1e-4))
						if momentumRunnerBid < targetBid && momentumRunnerBid > 0 {
							targetBid = momentumRunnerBid
						}
					}
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

	// Quantize to valid exchange price tick size per symbol
	tick := symbolTickSize(symbol)
	targetBid = quantizePrice(targetBid, tick, false)
	targetAsk = quantizePrice(targetAsk, tick, true)

	e.mu.Lock()
	defer e.mu.Unlock()

	orders := e.activeOrders[symbol]
	latency := e.cfg.OrderLatency
	if latency <= 0 {
		latency = 120 * time.Millisecond
	}
	cancelLatency := latency / 2 // Half-RTT cancel message transit to matching engine

	var keptOrders []*ActiveMakerOrder
	var cancelingBuy *ActiveMakerOrder
	var cancelingSell *ActiveMakerOrder
	hasBuy := false
	hasSell := false
	for _, ord := range orders {
		// In-flight cancel already dispatched: keep alive until cancel confirmation arrives
		// Cap in-flight canceling orders to at most 1 per side to prevent stale quote accumulation
		if !ord.CancelingUntil.IsZero() {
			if now.Before(ord.CancelingUntil) {
				if ord.Side == "BUY" && cancelingBuy == nil {
					cancelingBuy = ord
					keptOrders = append(keptOrders, ord)
				} else if ord.Side == "SELL" && cancelingSell == nil {
					cancelingSell = ord
					keptOrders = append(keptOrders, ord)
				}
			}
			continue
		}

		target := targetBid
		if ord.Side == "SELL" {
			target = targetAsk
		}
		// When holding inventory, exit order must match full position size and be priced at or above minProfitAsk
		if (currentInv > 0 && ord.Side == "SELL" && math.Abs(ord.Size-math.Abs(currentInv)) > 1e-6) ||
			(currentInv < 0 && ord.Side == "BUY" && math.Abs(ord.Size-math.Abs(currentInv)) > 1e-6) {
			ord.CancelingUntil = now.Add(cancelLatency)
			keptOrders = append(keptOrders, ord)
			continue
		}
		// In spot-only mode, if flat or short, cancel any resting SELL quote immediately
		if e.cfg.SpotOnly && currentInv <= 0 && ord.Side == "SELL" {
			ord.CancelingUntil = now.Add(cancelLatency)
			keptOrders = append(keptOrders, ord)
			continue
		}
		// In spot-only mode, if unencumbered cash is insufficient for resting BUY quote, cancel immediately
		if e.cfg.SpotOnly && ord.Side == "BUY" && currentInv >= 0 && e.state.Cash < ord.Size*ord.Price {
			ord.CancelingUntil = now.Add(cancelLatency)
			keptOrders = append(keptOrders, ord)
			continue
		}
		// Toxic momentum pull: cancel resting opening quotes if flow becomes adverse or invalidation triggers
		if ord.Side == "SELL" && currentInv <= 0 {
			if sm.Ret5sBps > 4.5 || blendedSkewBps > 3.0 || cbSig.IsAdverseAsk || convictionUp > 0.60 || (sm.OBI > 0.20 && convictionUp > 0.28) {
				ord.CancelingUntil = now.Add(cancelLatency)
				keptOrders = append(keptOrders, ord)
				continue
			}
		}
		if ord.Side == "BUY" && currentInv >= 0 {
			if sm.Ret5sBps < -4.5 || blendedSkewBps < -3.0 || cbSig.IsAdverseBid || convictionUp < 0.40 || (sm.OBI < -0.20 && convictionUp < 0.72) {
				ord.CancelingUntil = now.Add(cancelLatency)
				keptOrders = append(keptOrders, ord)
				continue
			}
		}
		// Extreme leakage circuit: immediately cancel resting opening quotes if 4+ consecutive losses
		if sm.ScalpingDisabled && sm.ConsecutiveLosses >= 4 {
			if (ord.Side == "SELL" && currentInv <= 0) || (ord.Side == "BUY" && currentInv >= 0) {
				ord.CancelingUntil = now.Add(cancelLatency)
				keptOrders = append(keptOrders, ord)
				continue
			}
		}
		if target <= 0 || math.IsNaN(target) || math.IsInf(target, 0) {
			ord.CancelingUntil = now.Add(cancelLatency)
			keptOrders = append(keptOrders, ord)
			continue
		}
		// Preserve FIFO queue priority: only cancel & replace if target price moved > 2.5 bps.
		devBps := math.Abs(ord.Price-target) / target * 1e4
		if devBps > 2.5 {
			ord.CancelingUntil = now.Add(cancelLatency)
			keptOrders = append(keptOrders, ord)
			continue
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

	// Dynamic OBI-Conditioned Entry Thresholds:
	// Base directional conviction required: 0.65 (quant hedge fund conviction gate).
	// If bid depth is thin (sm.OBI < -0.20), raise threshold to 0.72 to avoid catching falling knives.
	// If bid depth is thick / strong buyer support (sm.OBI > 0.25), allow entry at 0.62.
	buyThreshold := e.cfg.BuyThreshold
	if buyThreshold <= 0 {
		buyThreshold = 0.65
	}
	if sm.OBI < -0.20 {
		buyThreshold = 0.72
	} else if sm.OBI > 0.25 {
		buyThreshold = 0.62
	}

	sellThreshold := e.cfg.SellThreshold
	if sellThreshold <= 0 {
		sellThreshold = 0.35
	}
	if sm.OBI > 0.20 {
		sellThreshold = 0.28
	} else if sm.OBI < -0.25 {
		sellThreshold = 0.38
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
		if blendedSkewBps < -2.0 || sm.Ret5sBps < -3.0 || cbSig.IsAdverseBid || (!sm.BidCooldownUntil.IsZero() && now.Before(sm.BidCooldownUntil)) {
			allowBuy = false // Toxic cascade defense: do not catch falling knife whether flat or long!
		}
		if convictionUp < 0.40 {
			allowBuy = false // Decisive bearish invalidation: never buy into confirmed downward momentum
		}
		if sm.OBI < -0.20 && convictionUp < buyThreshold {
			allowBuy = false // Order book collapse defense: thin bid depth requires >= 0.72 conviction
		}
		if currentInv > 0 && convictionUp < buyThreshold {
			allowBuy = false // Position accumulation gate: adding to existing long requires >= buyThreshold
		}
		if currentInv == 0 && e.quantOrch != nil && e.quantOrch.Report(now).CurrentMode != orchestrator.ModeLowLatency && convictionUp < 0.48 {
			allowBuy = false // Structural wave gate: do not open new long without positive conviction
		}
		if sm.ScalpingDisabled && sm.ConsecutiveLosses >= 4 {
			allowBuy = false // Extreme leakage circuit: halt opening new long positions until recalibrated!
		}
		// Spot-only cash account: buying requires having sufficient unencumbered cash!
		if e.cfg.SpotOnly && e.state.Cash < buySize*targetBid {
			allowBuy = false
		}
	}
	if allowBuy {
		queueEst := bidSize
		if bestBid > 0 && targetBid < bestBid*0.9999 {
			queueEst = math.Max(buySize, bidSize*0.50)
		}
		if e.preTradeGuard != nil {
			isReduce := currentInv < 0
			ordReq := guardrails.OrderRequest{
				Symbol:       symbol,
				Side:         "BUY",
				Price:        targetBid,
				Quantity:     buySize,
				IsReduceOnly: isReduce,
			}
			if err := e.preTradeGuard.ValidateOrder(ordReq, bestBid, bestAsk, now); err != nil {
				allowBuy = false
			}
		}
		if allowBuy {
			stratName := "GLFT_QueueReactive_Maker"
			if e.quantOrch != nil {
				rep := e.quantOrch.Report(now)
				stratName = string(rep.CurrentMode)
			}
			orders = append(orders, &ActiveMakerOrder{
				Symbol:          symbol,
				Side:            "BUY",
				Price:           targetBid,
				Size:            buySize,
				PlacedAt:        now,
				ActiveAt:        now.Add(latency),
				QueueAhead:      queueEst,
				Strategy:        stratName,
				EdgeBps:         bidOffsetBps,
				FairValue:       fairVal,
				TargetInventory: targetInv,
			})
		}
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
		if e.cfg.SpotOnly {
			// Spot-only cash account: naked shorting is strictly prohibited! Cannot sell if flat or short.
			allowSell = false
		} else {
			if currentInv-clipQty < -maxInvQty || blendedSkewBps >= 3.5 || currentInv <= -2.0*clipQty {
				allowSell = false
			}
			if blendedSkewBps > 2.0 || sm.Ret5sBps > 3.0 || cbSig.IsAdverseAsk || (!sm.AskCooldownUntil.IsZero() && now.Before(sm.AskCooldownUntil)) {
				allowSell = false // Toxic cascade defense: do not short into surging knife whether flat or short!
			}
			if convictionUp > 0.60 {
				allowSell = false // Decisive bullish invalidation: never short into confirmed upward momentum
			}
			if sm.OBI > 0.20 && convictionUp > sellThreshold {
				allowSell = false // Order book surge defense: thin ask depth requires <= 0.28 conviction
			}
			if currentInv < 0 && convictionUp > sellThreshold {
				allowSell = false // Position accumulation gate: adding to existing short requires <= sellThreshold
			}
			if currentInv == 0 && e.quantOrch != nil && e.quantOrch.Report(now).CurrentMode != orchestrator.ModeLowLatency && convictionUp > 0.52 {
				allowSell = false // Structural wave gate: do not open new short without negative conviction
			}
			if sm.ScalpingDisabled && sm.ConsecutiveLosses >= 4 {
				allowSell = false // Extreme leakage circuit: halt opening new short positions until recalibrated!
			}
		}
	}
	if allowSell {
		queueEst := askSize
		if bestAsk > 0 && targetAsk > bestAsk*1.0001 {
			queueEst = math.Max(sellSize, askSize*0.50)
		}
		if e.preTradeGuard != nil {
			isReduce := currentInv > 0
			ordReq := guardrails.OrderRequest{
				Symbol:       symbol,
				Side:         "SELL",
				Price:        targetAsk,
				Quantity:     sellSize,
				IsReduceOnly: isReduce,
			}
			if err := e.preTradeGuard.ValidateOrder(ordReq, bestBid, bestAsk, now); err != nil {
				allowSell = false
			}
		}
		if allowSell {
			stratName := "GLFT_QueueReactive_Maker"
			if e.quantOrch != nil {
				rep := e.quantOrch.Report(now)
				stratName = string(rep.CurrentMode)
			}
			orders = append(orders, &ActiveMakerOrder{
				Symbol:          symbol,
				Side:            "SELL",
				Price:           targetAsk,
				Size:            sellSize,
				PlacedAt:        now,
				ActiveAt:        now.Add(latency),
				QueueAhead:      queueEst,
				Strategy:        stratName,
				EdgeBps:         askOffsetBps,
				FairValue:       fairVal,
				TargetInventory: targetInv,
			})
		}
	}
	e.activeOrders[symbol] = orders
	if e.quantOrch != nil {
		rep := e.quantOrch.Report(now)
		e.state.QuantOrchestrator = &rep
	}

	// Tactical Taker Execution:
	// Only permitted if explicitly opted in via EnableTaker configuration (default: false in pure MM).
	// In pure market making, taker orders are disabled by default to eliminate taker fee drag (4 bps)
	// and avoid crossing into adverse market momentum.
	if e.cfg.EnableTaker && math.Abs(metaPred) >= 15.0 && e.conformal.PassesConformalGate(metaPred, x[10], (e.cfg.TakerFeeBps+e.cfg.MakerFeeBps)*2.0) {
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
	feeUSDT := valUSDT * (e.makerFeeBps(symbol) * 1e-4)

	pos := e.state.Positions[symbol]
	realizedPnL := 0.0
	isClosing := false

	if side == "BUY" {
		if pos.Size < -1e-7 { // Closing short
			isClosing = true
			closeQty := math.Min(-pos.Size, qty)
			realizedPnL = closeQty * (pos.AvgEntryPrice - orderPrice)
		}
		newSize := pos.Size + qty
		if math.Abs(newSize) < 1e-7 {
			newSize = 0.0
			pos.AvgEntryPrice = 0.0
		} else if newSize > 0 {
			if pos.Size >= 0 {
				pos.AvgEntryPrice = (pos.AvgEntryPrice*pos.Size + orderPrice*qty) / newSize
			} else {
				pos.AvgEntryPrice = orderPrice
			}
		}
		pos.Size = newSize
	} else {
		if pos.Size > 1e-7 { // Closing long
			isClosing = true
			closeQty := math.Min(pos.Size, qty)
			realizedPnL = closeQty * (orderPrice - pos.AvgEntryPrice)
		}
		newSize := pos.Size - qty
		if math.Abs(newSize) < 1e-7 {
			newSize = 0.0
			pos.AvgEntryPrice = 0.0
		} else if newSize < 0 {
			if pos.Size <= 0 {
				pos.AvgEntryPrice = (pos.AvgEntryPrice*(-pos.Size) + orderPrice*qty) / (-newSize)
			} else {
				pos.AvgEntryPrice = orderPrice
			}
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
	if isClosing { // Round-trip closing fill
		sm := e.symMicro[symbol]
		if sm != nil {
			if netRealizedPnL > 0 {
				e.state.WinningTrades++
				sm.ConsecutiveLosses = 0
			} else {
				e.state.LosingTrades++
				sm.ConsecutiveLosses++
			}
			sm.RecentPnlWindow = append(sm.RecentPnlWindow, netRealizedPnL)
			if len(sm.RecentPnlWindow) > 10 {
				sm.RecentPnlWindow = sm.RecentPnlWindow[1:]
			}

			// Autonomous Scalping Viability Evaluation:
			// If tight scalping is leaking money or suffering adverse markouts, stop scalping entirely
			viable, reason := sm.EvaluateScalpingViability(e.tcaTracker)
			if !viable {
				if !sm.ScalpingDisabled {
					sm.ScalpingDisabled = true
					sm.ScalpingDisabledReason = reason
					fmt.Printf("[SELF-IMPROVING] %s: Scalping disabled (%s) -> Transitioned to Patient Swing Liquidity Provision.\n", symbol, reason)
				}
				// Money leakage detected: trigger autonomous walk-forward backtest recalibration!
				e.maybeTriggerAutoRecalibrationLocked(symbol)
			} else if sm.ScalpingDisabled && sm.ConsecutiveLosses == 0 && (sm.VpinCalc == nil || sm.VpinCalc.VPIN() < 0.55) {
				sm.ScalpingDisabled = false
				sm.ScalpingDisabledReason = ""
				fmt.Printf("[SELF-IMPROVING] %s: Market conditions normalized -> Resuming standard two-sided GLFT scalping.\n", symbol)
			}
		} else {
			if netRealizedPnL > 0 {
				e.state.WinningTrades++
			} else {
				e.state.LosingTrades++
			}
		}
	}

	if closed := e.state.WinningTrades + e.state.LosingTrades; closed > 0 {
		e.state.WinRate = float64(e.state.WinningTrades) / float64(closed) * 100.0
	} else if e.state.TotalTrades > 0 {
		e.state.WinRate = float64(e.state.WinningTrades) / float64(e.state.TotalTrades) * 100.0
	}
	e.updateTaxReservesLocked()

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
	isClosing := false

	if side == "BUY" {
		if pos.Size < -1e-7 {
			isClosing = true
			closeQty := math.Min(-pos.Size, qty)
			realizedPnL = closeQty * (pos.AvgEntryPrice - execPrice)
		}
		newSize := pos.Size + qty
		if math.Abs(newSize) < 1e-7 {
			newSize = 0.0
			pos.AvgEntryPrice = 0.0
		} else if newSize > 0 {
			if pos.Size >= 0 {
				pos.AvgEntryPrice = (pos.AvgEntryPrice*pos.Size + execPrice*qty) / newSize
			} else {
				pos.AvgEntryPrice = execPrice
			}
		}
		pos.Size = newSize
	} else {
		if pos.Size > 1e-7 {
			isClosing = true
			closeQty := math.Min(pos.Size, qty)
			realizedPnL = closeQty * (execPrice - pos.AvgEntryPrice)
		}
		newSize := pos.Size - qty
		if math.Abs(newSize) < 1e-7 {
			newSize = 0.0
			pos.AvgEntryPrice = 0.0
		} else if newSize < 0 {
			if pos.Size <= 0 {
				pos.AvgEntryPrice = (pos.AvgEntryPrice*(-pos.Size) + execPrice*qty) / (-newSize)
			} else {
				pos.AvgEntryPrice = execPrice
			}
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
	if isClosing {
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
	e.updateTaxReservesLocked()

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

	// Append to JSONL journal asynchronously via non-blocking channel
	if e.journalCh != nil {
		line, err := json.Marshal(rec)
		if err == nil {
			select {
			case e.journalCh <- append(line, '\n'):
			default:
				// Buffer full fallback: drop non-critical journal line to preserve event-loop latency
			}
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

// symbolTickSize returns the exchange price tick size for a given instrument.
func symbolTickSize(symbol string) float64 {
	u := strings.ToUpper(symbol)
	switch {
	case strings.HasPrefix(u, "BTC"):
		return 0.10
	case strings.HasPrefix(u, "ETH"), strings.HasPrefix(u, "SOL"):
		return 0.01
	case strings.HasPrefix(u, "DOGE"):
		return 0.0001
	default:
		return 0.01
	}
}

// quantizePrice rounds a price to the exchange tick grid without IEEE-754 floating point artifacts.
func quantizePrice(price float64, tick float64, roundUp bool) float64 {
	if tick <= 0 {
		tick = 0.01
	}
	var steps float64
	if roundUp {
		steps = math.Ceil(price/tick - 1e-9)
	} else {
		steps = math.Floor(price/tick + 1e-9)
	}
	quantized := steps * tick
	decimals := int(math.Max(0, math.Ceil(-math.Log10(tick)-1e-9)))
	p10 := math.Pow10(decimals)
	return math.Round(quantized*p10) / p10
}
