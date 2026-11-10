package orchestrator

import (
	"math"
	"sync"
	"time"
)

// StrategyMode defines the active quantitative operating regime.
type StrategyMode string

const (
	// ModeLowLatency (tau <= 15ms): Co-located / near-gateway.
	// Primary: Passive Queue-Reactive Market Making (GLFT, Cartea-Jaimungal, VPIN adverse selection damping).
	ModeLowLatency StrategyMode = "LOW_LATENCY_MAKER"

	// ModeMediumLatency (15ms < tau <= 150ms, e.g. California to Chicago/Tokyo):
	// Primary: Multi-Minute OBI Momentum & Hawkes Self-Exciting Cascades (30s - 15m holding horizon).
	ModeMediumLatency StrategyMode = "MEDIUM_LATENCY_OBI_MOMENTUM"

	// ModeHighLatency (tau > 150ms or Funding APR >= 12%):
	// Primary: Delta-Neutral Spot-Perpetual Funding Arbitrage (Basis Carry harvesting, zero directional risk).
	ModeHighLatency StrategyMode = "HIGH_LATENCY_FUNDING_CARRY"
)

// StrategyWeights allocates capital and conviction across the fund's quantitative pillars.
type StrategyWeights struct {
	PassiveMM      float64 `json:"passive_mm"`       // GLFT + Cartea-Jaimungal
	OBIMomentum    float64 `json:"obi_momentum"`     // Hawkes + Order Flow Imbalance (30s-15m)
	FundingCarry   float64 `json:"funding_carry"`    // Delta-neutral Spot-Perp Basis carry
	TrendFollowing float64 `json:"trend_following"`  // Multi-horizon TSMOM + Kalman
}

// OrchestratorConfig configures latency switching thresholds and smoothing parameters.
type OrchestratorConfig struct {
	LowLatencyThresholdMs    float64 // Default: 15.0 ms
	HighLatencyThresholdMs   float64 // Default: 150.0 ms
	HighFundingThresholdAPR  float64 // Default: 12.0% APR (triggers carry mode even on low latency)
	ExitFundingThresholdAPR  float64 // Default: 8.0% APR
	LatencyAlpha             float64 // EWMA smoothing factor for latency (default: 0.10)
	HysteresisBandPct        float64 // Default: 0.20 (+/- 20% threshold hysteresis to prevent flapping)
}

// DefaultOrchestratorConfig provides institutional configuration.
func DefaultOrchestratorConfig() OrchestratorConfig {
	return OrchestratorConfig{
		LowLatencyThresholdMs:   15.0,
		HighLatencyThresholdMs:  150.0,
		HighFundingThresholdAPR: 12.0,
		ExitFundingThresholdAPR: 8.0,
		LatencyAlpha:            0.10,
		HysteresisBandPct:       0.20,
	}
}

// OrchestratorReport summarizes current orchestrator state and model allocations.
type OrchestratorReport struct {
	CurrentMode          StrategyMode    `json:"current_mode"`
	SmoothedLatencyMs    float64         `json:"smoothed_latency_ms"`
	LastMeasuredLatencyMs float64        `json:"last_measured_latency_ms"`
	FundingAPR           float64         `json:"funding_apr"`
	Weights              StrategyWeights `json:"weights"`
	TargetHorizon        string          `json:"target_horizon"`
	TransitionsCount     int64           `json:"transitions_count"`
	TimeInCurrentMode    time.Duration   `json:"time_in_current_mode"`
	Description          string          `json:"description"`
}

// LatencyAdaptiveOrchestrator monitors network round-trip latency and funding rates,
// dynamically pivoting fund strategy weights between Passive MM, OBI Momentum, and Funding Carry.
type LatencyAdaptiveOrchestrator struct {
	mu sync.RWMutex

	cfg OrchestratorConfig

	currentMode        StrategyMode
	smoothedLatencyMs  float64
	lastMeasuredMs     float64
	latestFundingAPR   float64
	modeEnteredAt      time.Time
	transitionsCount   int64
	weights            StrategyWeights
}

// NewLatencyAdaptiveOrchestrator creates a new adaptive orchestrator.
func NewLatencyAdaptiveOrchestrator(cfg OrchestratorConfig) *LatencyAdaptiveOrchestrator {
	if cfg.LowLatencyThresholdMs <= 0 {
		cfg.LowLatencyThresholdMs = 15.0
	}
	if cfg.HighLatencyThresholdMs <= 0 {
		cfg.HighLatencyThresholdMs = 150.0
	}
	if cfg.LatencyAlpha <= 0 || cfg.LatencyAlpha > 1.0 {
		cfg.LatencyAlpha = 0.10
	}
	if cfg.HighFundingThresholdAPR <= 0 {
		cfg.HighFundingThresholdAPR = 12.0
	}

	o := &LatencyAdaptiveOrchestrator{
		cfg:               cfg,
		currentMode:       ModeMediumLatency, // Safe default for CA cloud/retail fiber (~80-120ms)
		modeEnteredAt:     time.Now(),
		smoothedLatencyMs: 80.0,
	}
	o.weights = o.computeWeights(ModeMediumLatency)
	return o
}

// RecordLatencyUpdate records a new ping / network RTT measurement and updates smoothed latency and active regime.
func (o *LatencyAdaptiveOrchestrator) RecordLatencyUpdate(latency time.Duration, fundingAPR float64, now time.Time) StrategyMode {
	o.mu.Lock()
	defer o.mu.Unlock()

	latMs := float64(latency.Nanoseconds()) / 1e6
	if math.IsNaN(latMs) || math.IsInf(latMs, 0) || latMs <= 0 {
		latMs = 50.0
	}
	if math.IsNaN(fundingAPR) || math.IsInf(fundingAPR, 0) {
		fundingAPR = 0.0
	}
	o.lastMeasuredMs = latMs
	o.latestFundingAPR = fundingAPR

	// EWMA latency smoothing
	alpha := o.cfg.LatencyAlpha
	if o.smoothedLatencyMs <= 0 {
		o.smoothedLatencyMs = latMs
	} else {
		o.smoothedLatencyMs = (1.0-alpha)*o.smoothedLatencyMs + alpha*latMs
	}

	newMode := o.evaluateRegime(o.smoothedLatencyMs, fundingAPR)
	if newMode != o.currentMode {
		o.currentMode = newMode
		o.modeEnteredAt = now
		o.transitionsCount++
		o.weights = o.computeWeights(newMode)
	}

	return o.currentMode
}

// evaluateRegime determines the optimal quantitative mode with hysteresis to prevent flapping.
func (o *LatencyAdaptiveOrchestrator) evaluateRegime(latMs, fundingAPR float64) StrategyMode {
	// 1. High carry condition: When funding rates skyrocket (>= 12% APR),
	// Delta-Neutral Spot-Perp Funding Arbitrage produces guaranteed alpha independent of latency!
	if fundingAPR >= o.cfg.HighFundingThresholdAPR {
		return ModeHighLatency
	}

	// If currently in High Latency mode due to funding, apply hysteresis before dropping out
	if o.currentMode == ModeHighLatency && fundingAPR > o.cfg.ExitFundingThresholdAPR && latMs > o.cfg.LowLatencyThresholdMs {
		return ModeHighLatency
	}

	lowThreshold := o.cfg.LowLatencyThresholdMs
	highThreshold := o.cfg.HighLatencyThresholdMs
	hysteresis := o.cfg.HysteresisBandPct

	switch o.currentMode {
	case ModeLowLatency:
		// Transition upward to Medium if latency exceeds threshold + hysteresis
		if latMs > lowThreshold*(1.0+hysteresis) {
			if latMs > highThreshold*(1.0+hysteresis) {
				return ModeHighLatency
			}
			return ModeMediumLatency
		}
		return ModeLowLatency

	case ModeMediumLatency:
		// Transition downward to Low if latency drops below threshold - hysteresis
		if latMs < lowThreshold*(1.0-hysteresis) {
			return ModeLowLatency
		}
		// Transition upward to High if latency exceeds high threshold + hysteresis
		if latMs > highThreshold*(1.0+hysteresis) {
			return ModeHighLatency
		}
		return ModeMediumLatency

	case ModeHighLatency:
		// Transition downward from High to Medium if latency drops below high threshold - hysteresis
		if latMs < highThreshold*(1.0-hysteresis) {
			if latMs < lowThreshold*(1.0-hysteresis) {
				return ModeLowLatency
			}
			return ModeMediumLatency
		}
		return ModeHighLatency

	default:
		return ModeMediumLatency
	}
}

// computeWeights allocates strategic weights according to operating mode.
func (o *LatencyAdaptiveOrchestrator) computeWeights(mode StrategyMode) StrategyWeights {
	switch mode {
	case ModeLowLatency:
		// Ultra-fast: Queue-reactive market making dominates
		return StrategyWeights{
			PassiveMM:      0.70,
			OBIMomentum:    0.20,
			FundingCarry:   0.05,
			TrendFollowing: 0.05,
		}
	case ModeMediumLatency:
		// Medium latency (California ~80-120ms): Multi-minute OBI momentum & Hawkes cascades dominate
		return StrategyWeights{
			PassiveMM:      0.20,
			OBIMomentum:    0.50,
			FundingCarry:   0.15,
			TrendFollowing: 0.15,
		}
	case ModeHighLatency:
		// High latency / High carry: Delta-neutral basis carry dominates with macro trend following
		return StrategyWeights{
			PassiveMM:      0.05,
			OBIMomentum:    0.15,
			FundingCarry:   0.55,
			TrendFollowing: 0.25,
		}
	default:
		return StrategyWeights{
			PassiveMM:      0.25,
			OBIMomentum:    0.25,
			FundingCarry:   0.25,
			TrendFollowing: 0.25,
		}
	}
}

// BlendAlpha calculates a unified composite score [-1.0, 1.0] by blending predictions from all 4 sub-engines
// using the dynamic latency-adapted strategy weights.
func (o *LatencyAdaptiveOrchestrator) BlendAlpha(
	passiveScore float64,
	obiMomentumScore float64,
	fundingCarryScore float64,
	trendScore float64,
) (blendedScore float64, dominantHorizon string) {
	if math.IsNaN(passiveScore) || math.IsInf(passiveScore, 0) {
		passiveScore = 0.0
	}
	if math.IsNaN(obiMomentumScore) || math.IsInf(obiMomentumScore, 0) {
		obiMomentumScore = 0.0
	}
	if math.IsNaN(fundingCarryScore) || math.IsInf(fundingCarryScore, 0) {
		fundingCarryScore = 0.0
	}
	if math.IsNaN(trendScore) || math.IsInf(trendScore, 0) {
		trendScore = 0.0
	}

	o.mu.RLock()
	w := o.weights
	mode := o.currentMode
	o.mu.RUnlock()

	blended := w.PassiveMM*passiveScore +
		w.OBIMomentum*obiMomentumScore +
		w.FundingCarry*fundingCarryScore +
		w.TrendFollowing*trendScore

	// Clamp to [-1.0, +1.0] and guard against NaN
	if math.IsNaN(blended) || math.IsInf(blended, 0) {
		blended = 0.0
	} else if blended > 1.0 {
		blended = 1.0
	} else if blended < -1.0 {
		blended = -1.0
	}

	switch mode {
	case ModeLowLatency:
		dominantHorizon = "Sub-Second Queue Priority (100ms - 5s)"
	case ModeMediumLatency:
		dominantHorizon = "Multi-Minute Structural Wave (30s - 15m)"
	case ModeHighLatency:
		dominantHorizon = "Macro Carry & Multi-Day Swing (8h - Weeks)"
	default:
		dominantHorizon = "Adaptive Swing"
	}

	return blended, dominantHorizon
}

// Report returns a snapshot of the orchestrator's state and active allocation weights.
func (o *LatencyAdaptiveOrchestrator) Report(now time.Time) OrchestratorReport {
	o.mu.RLock()
	defer o.mu.RUnlock()

	var desc string
	var horizon string
	switch o.currentMode {
	case ModeLowLatency:
		desc = "Low-latency co-located environment. Passive queue-reactive market making active (GLFT + Cartea-Jaimungal) capturing exchange maker rebates."
		horizon = "Sub-Second (100ms - 5s)"
	case ModeMediumLatency:
		desc = "Medium-latency cloud/retail fiber environment (e.g. California to exchange). Order flow imbalance momentum & Hawkes cascades active (30s - 15m holding horizon) avoiding sub-second fee drag."
		horizon = "Multi-Minute (30s - 15m)"
	case ModeHighLatency:
		desc = "High-latency / High-yield environment. Delta-neutral Spot-Perp basis carry harvesting active (30-50%+ APR) with macro trend following; latency completely insulated."
		horizon = "Multi-Hour / Multi-Day (8h funding cycles)"
	}

	return OrchestratorReport{
		CurrentMode:           o.currentMode,
		SmoothedLatencyMs:     math.Round(o.smoothedLatencyMs*100) / 100,
		LastMeasuredLatencyMs: math.Round(o.lastMeasuredMs*100) / 100,
		FundingAPR:            math.Round(o.latestFundingAPR*100) / 100,
		Weights:               o.weights,
		TargetHorizon:         horizon,
		TransitionsCount:      o.transitionsCount,
		TimeInCurrentMode:     now.Sub(o.modeEnteredAt),
		Description:           desc,
	}
}
