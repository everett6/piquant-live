package guardrails

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

var (
	ErrKillSwitchTripped   = errors.New("guardrails: kill switch tripped (-2% daily drawdown limit reached)")
	ErrFatFingerPrice      = errors.New("guardrails: fat-finger price collar violated (>1.5% from inside touch)")
	ErrFatFingerQty        = errors.New("guardrails: fat-finger quantity limit violated")
	ErrGrossExposureLimit  = errors.New("guardrails: gross exposure limit exceeded")
	ErrNetExposureLimit    = errors.New("guardrails: net exposure limit exceeded")
	ErrConcentrationLimit  = errors.New("guardrails: asset concentration limit exceeded (>80%)")
	ErrRateLimitExceeded   = errors.New("guardrails: token bucket rate limit exceeded (>10 msgs/sec)")
	ErrTradingLocked       = errors.New("guardrails: trading is locked by risk administrator")
	ErrInvalidOrderParams  = errors.New("guardrails: invalid order parameters (NaN, Inf, or <= 0)")
)

// OrderRequest represents a pre-trade order candidate requiring compliance checks.
type OrderRequest struct {
	Symbol      string
	Side        string // "BUY" or "SELL"
	Price       float64
	Quantity    float64
	IsReduceOnly bool   // True if this order is closing existing inventory
}

// ExposureConfig holds limits for portfolio exposure and concentration.
type ExposureConfig struct {
	MaxGrossUSD        float64 // e.g. $10,000.00
	MaxNetUSD          float64 // e.g. $5,000.00
	MaxConcentrationPct float64 // e.g. 80.0% (single asset gross / equity)
	MaxOrderQtyUSD     float64 // e.g. $2,500.00 per single order
}

// GuardrailConfig defines all pre-trade parameters.
type GuardrailConfig struct {
	Exposure              ExposureConfig
	MaxDailyDrawdownPct   float64       // e.g. 2.0%
	FatFingerMaxDeviation float64       // e.g. 0.015 (1.5% or 150 bps)
	MaxOrderQty           float64       // e.g. 1.0 BTC
	RateLimitRate         float64       // messages per second, e.g. 10.0
	RateLimitCapacity     float64       // bucket burst capacity, e.g. 20.0
	ResetWindow           time.Duration // e.g. 24 * time.Hour
}

// DefaultGuardrailConfig returns institutional pre-trade risk guardrail parameters.
func DefaultGuardrailConfig() GuardrailConfig {
	return GuardrailConfig{
		Exposure: ExposureConfig{
			MaxGrossUSD:        25000.0,
			MaxNetUSD:          15000.0,
			MaxConcentrationPct: 80.0,
			MaxOrderQtyUSD:     5000.0,
		},
		MaxDailyDrawdownPct:   2.0,   // -2.0% daily max trailing drawdown kill switch
		FatFingerMaxDeviation: 0.015, // 1.5% (150 bps) price collar
		MaxOrderQty:           0.50,  // 0.50 BTC max clip
		RateLimitRate:         10.0,  // 10 orders/sec max
		RateLimitCapacity:     20.0,  // 20 burst capacity
		ResetWindow:           24 * time.Hour,
	}
}

// PreTradeGuard provides pre-trade compliance checks across exposure, drawdowns, rate limits, and fat fingers.
type PreTradeGuard struct {
	mu sync.RWMutex

	cfg GuardrailConfig

	// Kill switch & drawdown tracking
	peakEquity       float64
	currentEquity    float64
	killSwitchTripped bool
	killSwitchReason string
	lastPeakTime     time.Time
	tradingLocked    bool

	// Token bucket rate limiter
	tokens           float64
	lastTokenRefill  time.Time

	// Current portfolio snapshot for exposure checks
	positions        map[string]float64 // symbol -> signed size
	markPrices       map[string]float64 // symbol -> mark price
}

// NewPreTradeGuard creates a thread-safe institutional pre-trade guard.
func NewPreTradeGuard(cfg GuardrailConfig) *PreTradeGuard {
	if cfg.MaxDailyDrawdownPct <= 0 {
		cfg.MaxDailyDrawdownPct = 2.0
	}
	if cfg.FatFingerMaxDeviation <= 0 {
		cfg.FatFingerMaxDeviation = 0.015
	}
	if cfg.RateLimitRate <= 0 {
		cfg.RateLimitRate = 10.0
	}
	if cfg.RateLimitCapacity <= 0 {
		cfg.RateLimitCapacity = 20.0
	}

	now := time.Now()
	return &PreTradeGuard{
		cfg:             cfg,
		tokens:          cfg.RateLimitCapacity,
		lastTokenRefill: now,
		lastPeakTime:    now,
		positions:       make(map[string]float64),
		markPrices:      make(map[string]float64),
	}
}

// UpdatePortfolioState updates the internal portfolio equity and positions used for exposure and kill switch evaluations.
func (g *PreTradeGuard) UpdatePortfolioState(equity, peakEquity float64, positions map[string]float64, marks map[string]float64, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.currentEquity = equity
	if peakEquity > g.peakEquity {
		g.peakEquity = peakEquity
		g.lastPeakTime = now
	}
	if equity > g.peakEquity || g.peakEquity == 0 {
		g.peakEquity = equity
		g.lastPeakTime = now
	}

	// 24h peak reset if configured
	if g.cfg.ResetWindow > 0 && now.Sub(g.lastPeakTime) > g.cfg.ResetWindow {
		g.peakEquity = equity
		g.lastPeakTime = now
	}

	// Update positions copy
	g.positions = make(map[string]float64, len(positions))
	for k, v := range positions {
		g.positions[k] = v
	}

	// Update mark prices copy
	g.markPrices = make(map[string]float64, len(marks))
	for k, v := range marks {
		g.markPrices[k] = v
	}

	// Check Kill-Switch Drawdown
	if g.peakEquity > 0 {
		ddPct := (g.peakEquity - g.currentEquity) / g.peakEquity * 100.0
		if ddPct >= g.cfg.MaxDailyDrawdownPct && !g.killSwitchTripped {
			g.killSwitchTripped = true
			g.killSwitchReason = fmt.Sprintf("Trailing peak-to-trough drawdown reached %.2f%% (limit: %.2f%%)", ddPct, g.cfg.MaxDailyDrawdownPct)
		}
	}
}

// ValidateOrder performs pre-trade verification for an incoming order slice or quote.
// bestBid and bestAsk provide the current inside touch for fat-finger collar checks.
func (g *PreTradeGuard) ValidateOrder(ord OrderRequest, bestBid, bestAsk float64, now time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	// 1. Sanity Checks: NaN, Inf, non-positive
	if math.IsNaN(ord.Price) || math.IsInf(ord.Price, 0) || ord.Price <= 0 ||
		math.IsNaN(ord.Quantity) || math.IsInf(ord.Quantity, 0) || ord.Quantity <= 0 {
		return ErrInvalidOrderParams
	}

	// 2. Kill-Switch & Trading Lock
	if g.tradingLocked {
		return ErrTradingLocked
	}
	if g.killSwitchTripped {
		// Only reduce-only orders (closing open inventory) are permitted once kill-switch trips!
		if !ord.IsReduceOnly {
			return fmt.Errorf("%w: %s", ErrKillSwitchTripped, g.killSwitchReason)
		}
	}

	// 3. Token-Bucket Rate Limiter
	if err := g.checkAndConsumeRateLimit(now); err != nil {
		return err
	}

	// 4. Fat-Finger Collar
	// A) Max Order Qty
	if g.cfg.MaxOrderQty > 0 && ord.Quantity > g.cfg.MaxOrderQty {
		return fmt.Errorf("%w: qty %.4f > max %.4f", ErrFatFingerQty, ord.Quantity, g.cfg.MaxOrderQty)
	}
	// B) Max Order USD
	orderUSD := ord.Price * ord.Quantity
	if g.cfg.Exposure.MaxOrderQtyUSD > 0 && orderUSD > g.cfg.Exposure.MaxOrderQtyUSD {
		return fmt.Errorf("%w: notional $%.2f > max $%.2f", ErrFatFingerQty, orderUSD, g.cfg.Exposure.MaxOrderQtyUSD)
	}
	// C) Price Deviation Collar (1.5% from inside touch)
	collar := g.cfg.FatFingerMaxDeviation
	if ord.Side == "BUY" {
		// Buying way above inside ask is aggressive or fat-finger
		refPrice := bestAsk
		if refPrice <= 0 {
			refPrice = bestBid
		}
		if refPrice > 0 {
			dev := (ord.Price - refPrice) / refPrice
			if dev > collar {
				return fmt.Errorf("%w: buy price %.2f is +%.2f%% above ask %.2f (collar: %.2f%%)",
					ErrFatFingerPrice, ord.Price, dev*100.0, refPrice, collar*100.0)
			}
		}
	} else if ord.Side == "SELL" {
		// Selling way below inside bid is aggressive or fat-finger
		refPrice := bestBid
		if refPrice <= 0 {
			refPrice = bestAsk
		}
		if refPrice > 0 {
			dev := (refPrice - ord.Price) / refPrice
			if dev > collar {
				return fmt.Errorf("%w: sell price %.2f is -%.2f%% below bid %.2f (collar: %.2f%%)",
					ErrFatFingerPrice, ord.Price, dev*100.0, refPrice, collar*100.0)
			}
		}
	}

	// If reduce-only order, bypass gross/net exposure checks because it is de-risking
	if ord.IsReduceOnly {
		return nil
	}

	// 5. Dynamic Exposure & Concentration Limits
	return g.validateExposureLimits(ord)
}

func (g *PreTradeGuard) checkAndConsumeRateLimit(now time.Time) error {
	elapsed := now.Sub(g.lastTokenRefill).Seconds()
	if elapsed > 0 {
		g.tokens = math.Min(g.cfg.RateLimitCapacity, g.tokens+elapsed*g.cfg.RateLimitRate)
		g.lastTokenRefill = now
	}

	if g.tokens < 1.0 {
		return ErrRateLimitExceeded
	}

	g.tokens -= 1.0
	return nil
}

func (g *PreTradeGuard) validateExposureLimits(ord OrderRequest) error {
	// Project new position if order were to execute
	delta := ord.Quantity
	if ord.Side == "SELL" {
		delta = -ord.Quantity
	}

	mark := g.markPrices[ord.Symbol]
	if mark <= 0 {
		mark = ord.Price
	}

	var totalGrossUSD float64
	var totalNetUSD float64
	var candidateAssetGrossUSD float64

	for sym, pos := range g.positions {
		pMark := g.markPrices[sym]
		if pMark <= 0 {
			pMark = ord.Price
		}
		posSize := pos
		if sym == ord.Symbol {
			posSize += delta
			candidateAssetGrossUSD = math.Abs(posSize) * pMark
		}
		notional := posSize * pMark
		totalGrossUSD += math.Abs(notional)
		totalNetUSD += notional
	}

	// If candidate symbol was not previously in positions map
	if _, exists := g.positions[ord.Symbol]; !exists {
		notional := delta * mark
		totalGrossUSD += math.Abs(notional)
		totalNetUSD += notional
		candidateAssetGrossUSD = math.Abs(notional)
	}

	// A) Gross Exposure Limit
	if g.cfg.Exposure.MaxGrossUSD > 0 && totalGrossUSD > g.cfg.Exposure.MaxGrossUSD {
		return fmt.Errorf("%w: projected gross $%.2f > limit $%.2f", ErrGrossExposureLimit, totalGrossUSD, g.cfg.Exposure.MaxGrossUSD)
	}

	// B) Net Exposure Limit
	if g.cfg.Exposure.MaxNetUSD > 0 && math.Abs(totalNetUSD) > g.cfg.Exposure.MaxNetUSD {
		return fmt.Errorf("%w: projected net $%.2f > limit $%.2f", ErrNetExposureLimit, math.Abs(totalNetUSD), g.cfg.Exposure.MaxNetUSD)
	}

	// C) Concentration Limit
	if g.cfg.Exposure.MaxConcentrationPct > 0 && g.currentEquity > 0 {
		concPct := (candidateAssetGrossUSD / g.currentEquity) * 100.0
		if concPct > g.cfg.Exposure.MaxConcentrationPct {
			return fmt.Errorf("%w: %s concentration %.1f%% > limit %.1f%%",
				ErrConcentrationLimit, ord.Symbol, concPct, g.cfg.Exposure.MaxConcentrationPct)
		}
	}

	return nil
}

// TripKillSwitch manually activates the emergency kill switch.
func (g *PreTradeGuard) TripKillSwitch(reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.killSwitchTripped = true
	g.killSwitchReason = reason
}

// ResetKillSwitch restores trading operation after an administrator audit.
func (g *PreTradeGuard) ResetKillSwitch() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.killSwitchTripped = false
	g.killSwitchReason = ""
	g.peakEquity = g.currentEquity
	g.lastPeakTime = time.Now()
}

// LockTrading prevents any new orders from being placed.
func (g *PreTradeGuard) LockTrading(locked bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tradingLocked = locked
}

// Status returns current pre-trade guard metrics.
func (g *PreTradeGuard) Status() (isTripped bool, reason string, isLocked bool, peakEq, currEq float64, availableTokens float64) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.killSwitchTripped, g.killSwitchReason, g.tradingLocked, g.peakEquity, g.currentEquity, g.tokens
}
