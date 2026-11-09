package paper

import (
	"fmt"
	"math"
	"strings"
	"sync"
)

// DynamicLeverageConfig defines risk parameters for automated leverage scaling.
type DynamicLeverageConfig struct {
	MinLeverage    float64 `json:"min_leverage"`     // Minimum allowable leverage (default: 0.5x, 0.0x on halt)
	MaxLeverage    float64 `json:"max_leverage"`     // Maximum leverage ceiling (default: 5.0x for $1k capital)
	BaseLeverage   float64 `json:"base_leverage"`    // Nominal leverage under calm market making (default: 2.5x)
	TargetVolBps   float64 `json:"target_vol_bps"`   // Target tick volatility in basis points (default: 15.0 bps)
	MaxDrawdownPct float64 `json:"max_drawdown_pct"` // Circuit breaker drawdown threshold (default: 2.0%)
	ClipFraction   float64 `json:"clip_fraction"`    // Fraction of max symbol allocation per quote clip (default: 0.20)
}

// DefaultDynamicLeverageConfig returns standard quantitative risk parameters.
func DefaultDynamicLeverageConfig() DynamicLeverageConfig {
	return DynamicLeverageConfig{
		MinLeverage:    0.5,
		MaxLeverage:    3.5,
		BaseLeverage:   2.5,
		TargetVolBps:   15.0,
		MaxDrawdownPct: 2.0,
		ClipFraction:   0.35,
	}
}

// DynamicLeverageState reports live leverage diagnostics for UI and telemetry.
type DynamicLeverageState struct {
	EffectiveLeverage float64 `json:"effective_leverage"` // Current active leverage multiplier (e.g. 2.45x)
	TargetLeverage    float64 `json:"target_leverage"`    // Raw unconstrained model target leverage
	MaxLeverage       float64 `json:"max_leverage"`       // Upper leverage cap (5.0x)
	MinLeverage       float64 `json:"min_leverage"`       // Lower leverage floor (0.5x)
	VolFactor         float64 `json:"vol_factor"`         // Volatility scaling factor (0.4 - 1.6)
	ToxicityFactor    float64 `json:"toxicity_factor"`    // VPIN toxicity dampener (0.2 - 1.0)
	CascadeFactor     float64 `json:"cascade_factor"`     // Hawkes cascade dampener (0.2 - 1.0)
	DrawdownFactor    float64 `json:"drawdown_factor"`    // Drawdown taper factor (0.0 - 1.0)
	RegimeName        string  `json:"regime_name"`        // e.g. "CALM_MAKER", "VOL_SCALED", "TOXIC_DEFENSE"
	RegimeDetail      string  `json:"regime_detail"`      // Explanatory breakdown string
}

// DynamicLeverageController automatically computes optimal leverage on every tick.
type DynamicLeverageController struct {
	mu  sync.RWMutex
	cfg DynamicLeverageConfig
}

// NewDynamicLeverageController constructs an automated leverage controller.
func NewDynamicLeverageController(cfg DynamicLeverageConfig) *DynamicLeverageController {
	if cfg.MinLeverage <= 0 {
		cfg.MinLeverage = 0.5
	}
	if cfg.MaxLeverage <= 0 {
		cfg.MaxLeverage = 5.0
	}
	if cfg.BaseLeverage <= 0 {
		cfg.BaseLeverage = 2.5
	}
	if cfg.TargetVolBps <= 0 {
		cfg.TargetVolBps = 15.0
	}
	if cfg.MaxDrawdownPct <= 0 {
		cfg.MaxDrawdownPct = 2.0
	}
	if cfg.ClipFraction <= 0 || cfg.ClipFraction > 1.0 {
		cfg.ClipFraction = 0.20
	}
	return &DynamicLeverageController{cfg: cfg}
}

// UpdateCloudParams safely updates leverage and clip sizing parameters from hot-reloaded cloud configurations.
func (c *DynamicLeverageController) UpdateCloudParams(baseLev, maxLev, clipFrac float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !math.IsNaN(maxLev) && !math.IsInf(maxLev, 0) && maxLev >= 1.0 {
		if maxLev > 5.0 {
			maxLev = 5.0
		}
		c.cfg.MaxLeverage = maxLev
	}
	if !math.IsNaN(baseLev) && !math.IsInf(baseLev, 0) && baseLev >= 1.0 {
		if baseLev > c.cfg.MaxLeverage {
			baseLev = c.cfg.MaxLeverage
		}
		c.cfg.BaseLeverage = baseLev
	}
	if !math.IsNaN(clipFrac) && !math.IsInf(clipFrac, 0) && clipFrac >= 0.05 {
		if clipFrac > 0.35 {
			clipFrac = 0.35
		}
		c.cfg.ClipFraction = clipFrac
	}
}

// Compute evaluates real-time market microstructure and computes optimal leverage and notional sizing.
func (c *DynamicLeverageController) Compute(volBps float64, vpin float64, hawkesBranching float64, currentDDPct float64, isCircuitHalted bool) DynamicLeverageState {
	c.mu.RLock()
	cfg := c.cfg
	c.mu.RUnlock()

	if isCircuitHalted {
		return DynamicLeverageState{
			EffectiveLeverage: 0.0,
			TargetLeverage:    0.0,
			MaxLeverage:       cfg.MaxLeverage,
			MinLeverage:       cfg.MinLeverage,
			DrawdownFactor:    0.0,
			RegimeName:        "HALTED",
			RegimeDetail:      "Circuit breaker tripped: zero leverage / emergency halt",
		}
	}

	// 1. Volatility Scaling (Inverse volatility parity)
	vol := math.Max(1.0, volBps)
	volFactor := cfg.TargetVolBps / vol
	if volFactor > 1.6 {
		volFactor = 1.6
	} else if volFactor < 0.4 {
		volFactor = 0.4
	}

	// 2. VPIN Toxicity De-leveraging
	toxFct := 1.0
	if vpin > 0.55 {
		toxFct = math.Max(0.20, 0.70-2.0*(vpin-0.55))
	} else if vpin > 0.35 {
		toxFct = 1.0 - 1.5*(vpin-0.35)
	}

	// 3. Hawkes Cascade Contagion Factor
	cascFct := 1.0
	if hawkesBranching >= 0.90 {
		cascFct = 0.25
	} else if hawkesBranching >= 0.70 {
		cascFct = 0.65
	}

	// 4. Drawdown Preservation Taper
	ddFct := 1.0
	if cfg.MaxDrawdownPct > 0 {
		ddRatio := currentDDPct / cfg.MaxDrawdownPct
		if ddRatio >= 1.0 {
			ddFct = 0.0
		} else {
			ddFct = math.Max(0.10, 1.0-ddRatio)
		}
	}

	// 5. Synthesize target & constrained effective leverage
	targetLev := cfg.BaseLeverage * volFactor * toxFct * cascFct * ddFct
	effectiveLev := targetLev
	if effectiveLev > cfg.MaxLeverage {
		effectiveLev = cfg.MaxLeverage
	} else if effectiveLev < cfg.MinLeverage {
		effectiveLev = cfg.MinLeverage
	}

	// Determine descriptive regime
	regime := "CALM_MAKER"
	detail := fmt.Sprintf("Optimal spread capture (%.1fx Kelly vol-parity)", effectiveLev)

	if cascFct < 0.5 {
		regime = "CASCADE_ALERT"
		detail = fmt.Sprintf("Hawkes cascade spike (branching %.2f): leverage cut to %.1fx", hawkesBranching, effectiveLev)
	} else if toxFct < 0.6 {
		regime = "TOXIC_DEFENSE"
		detail = fmt.Sprintf("Informed toxic flow (VPIN %.2f): leverage dialed down to %.1fx", vpin, effectiveLev)
	} else if volFactor < 0.7 {
		regime = "HIGH_VOLATILITY"
		detail = fmt.Sprintf("Elevated volatility (%.1f bps): leverage scaled to %.1fx", volBps, effectiveLev)
	} else if ddFct < 0.6 {
		regime = "DRAWDOWN_TAPER"
		detail = fmt.Sprintf("Capital preservation taper (DD: %.2f%%): leverage reduced to %.1fx", currentDDPct, effectiveLev)
	}

	return DynamicLeverageState{
		EffectiveLeverage: effectiveLev,
		TargetLeverage:    targetLev,
		MaxLeverage:       cfg.MaxLeverage,
		MinLeverage:       cfg.MinLeverage,
		VolFactor:         volFactor,
		ToxicityFactor:    toxFct,
		CascadeFactor:     cascFct,
		DrawdownFactor:    ddFct,
		RegimeName:        regime,
		RegimeDetail:      detail,
	}
}

// SymbolRiskProfile defines volatility weight and exchange order lot precision.
type SymbolRiskProfile struct {
	VolWeight float64 // Volatility parity weight (TargetVol / AssetVol)
	Decimals  int     // Exchange lot size decimal places
}

// DefaultSymbolProfiles maps major cryptocurrency pairs to quantitative volatility-parity weights.
var DefaultSymbolProfiles = map[string]SymbolRiskProfile{
	"BTC":  {VolWeight: 1.15, Decimals: 4}, // ~42% ann vol
	"ETH":  {VolWeight: 0.85, Decimals: 3}, // ~58% ann vol
	"SOL":  {VolWeight: 0.65, Decimals: 2}, // ~76% ann vol
	"BNB":  {VolWeight: 1.05, Decimals: 2}, // ~48% ann vol
	"DOGE": {VolWeight: 0.45, Decimals: 0}, // ~95% ann vol
	"XRP":  {VolWeight: 0.75, Decimals: 1}, // ~65% ann vol
	"AVAX": {VolWeight: 0.60, Decimals: 2}, // ~82% ann vol
	"LINK": {VolWeight: 0.70, Decimals: 2}, // ~71% ann vol
}

// ComputeOrderSize computes the quote size for a given asset given current portfolio equity and price.
// Implements cross-asset volatility parity (ETH receives proportional vol-scaled allocation).
func (c *DynamicLeverageController) ComputeOrderSize(equity float64, effectiveLev float64, price float64, numSymbols int) float64 {
	return c.ComputeOrderSizeForSymbol("BTCUSDT", equity, effectiveLev, price, numSymbols)
}

// ComputeOrderSizeForSymbol scales sizing per instrument based on inverse volatility parity:
// Higher-volatility assets (SOL, DOGE, AVAX) receive smaller fractional sizing to equate
// dollar variance risk, while preserving wider spread capture and fee rebates.
func (c *DynamicLeverageController) ComputeOrderSizeForSymbol(symbol string, equity float64, effectiveLev float64, price float64, numSymbols int) float64 {
	if equity <= 0 || price <= 0 || effectiveLev <= 0 {
		return 0.001
	}
	if numSymbols <= 0 {
		numSymbols = 2
	}
	// Allocate notional across traded universe
	baseNotional := (equity * effectiveLev) / float64(numSymbols)

	// Volatility parity weighting and decimal precision lookup
	uSym := strings.ToUpper(symbol)
	prof := SymbolRiskProfile{VolWeight: 1.0, Decimals: 3}
	for _, p := range []string{"BTC", "ETH", "SOL", "BNB", "DOGE", "XRP", "AVAX", "LINK"} {
		if strings.HasPrefix(uSym, p) {
			prof = DefaultSymbolProfiles[p]
			break
		}
	}

	notional := baseNotional * prof.VolWeight
	qty := notional / price

	p := math.Pow(10, float64(prof.Decimals))
	rounded := math.Round(qty*p) / p
	if rounded <= 0 && prof.Decimals > 0 {
		rounded = 1.0 / p
	}
	return rounded
}

// ComputeClipSizeForSymbol returns both the individual passive quote clip size (20% of max symbol allocation,
// floored above Binance's $5.00 USDT minimum notional) and the maximum inventory limit for that symbol.
func (c *DynamicLeverageController) ComputeClipSizeForSymbol(symbol string, equity float64, effectiveLev float64, price float64, numSymbols int) (clipQty float64, maxInvQty float64) {
	maxInvQty = c.ComputeOrderSizeForSymbol(symbol, equity, effectiveLev, price, numSymbols)
	if price <= 0 || maxInvQty <= 0 {
		return 0.001, 0.005
	}

	uSym := strings.ToUpper(symbol)
	prof := SymbolRiskProfile{VolWeight: 1.0, Decimals: 3}
	for _, p := range []string{"BTC", "ETH", "SOL", "BNB", "DOGE", "XRP", "AVAX", "LINK"} {
		if strings.HasPrefix(uSym, p) {
			prof = DefaultSymbolProfiles[p]
			break
		}
	}

	c.mu.RLock()
	clipFrac := c.cfg.ClipFraction
	c.mu.RUnlock()
	if clipFrac <= 0 || clipFrac > 1.0 {
		clipFrac = 0.20
	}

	// Each quote leg is clipFrac (default 20%) of the symbol's max target position,
	// with a minimum clip notional of $6.00 USDT to strictly satisfy Binance Futures $5.00 min notional.
	targetClip := maxInvQty * clipFrac
	minClip := 6.0 / price
	if targetClip < minClip {
		targetClip = minClip
	}
	if targetClip > maxInvQty {
		targetClip = maxInvQty
	}

	p := math.Pow(10, float64(prof.Decimals))
	clipQty = math.Round(targetClip*p) / p
	if clipQty <= 0 {
		if prof.Decimals > 0 {
			clipQty = 1.0 / p
		} else {
			clipQty = 1.0
		}
	}
	return clipQty, maxInvQty
}
