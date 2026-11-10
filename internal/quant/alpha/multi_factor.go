package alpha

import (
	"math"
	"time"
)

// MarketRegime represents the current macro/micro regime detected by the multi-factor model.
type MarketRegime string

const (
	RegimeTrending      MarketRegime = "TRENDING"
	RegimeMeanReverting MarketRegime = "MEAN_REVERTING"
	RegimeHighVolToxic  MarketRegime = "HIGH_VOL_TOXIC"
	RegimeNormal        MarketRegime = "NORMAL"
)

// MultiFactorScore encapsulates the computed factors and composite signal.
type MultiFactorScore struct {
	Timestamp      time.Time
	Regime         MarketRegime
	MomentumFactor float64 // F_mom in [-1, +1]
	ValueFactor    float64 // F_val in [-1, +1]
	VolFactor      float64 // F_vol in [-1, +1] (Quality/low-vol anomaly minus toxicity)
	CompositeScore float64 // Blended score in [-1, +1]
	Conviction     float64 // Logistic sigmoid P(Up) in [0, 1]
	WeightMom      float64
	WeightVal      float64
	WeightVol      float64
}

// MultiFactorModel implements the QuantMuse-inspired multi-factor quantitative engine
// combining Momentum, Value (Mean-Reversion), and Volatility/Quality factors.
// All inference executes in 0 B/op and 0 allocs/op.
type MultiFactorModel struct {
	// Ring buffers for time-series factor computations
	windowSize  int
	prices      []float64
	volumes     []float64
	count       int
	head        int

	// EWMA accumulators
	ewmaFast    float64 // Short alpha horizon (e.g. 10 ticks)
	ewmaSlow    float64 // Long alpha horizon (e.g. 60 ticks)
	ewmaAlphaF  float64
	ewmaAlphaS  float64

	// Variance tracking
	varAcc      float64
	meanAcc     float64

	// VWAP tracking
	cumVol      float64
	cumPV       float64

	// Adaptive Regime & Factor Weights
	CurrentRegime MarketRegime
	WeightMom     float64
	WeightVal     float64
	WeightVol     float64
}

// NewMultiFactorModel creates a new multi-factor engine.
func NewMultiFactorModel(windowSize int) *MultiFactorModel {
	if windowSize <= 10 {
		windowSize = 100
	}
	return &MultiFactorModel{
		windowSize:    windowSize,
		prices:        make([]float64, windowSize),
		volumes:       make([]float64, windowSize),
		ewmaAlphaF:    2.0 / (10.0 + 1.0),
		ewmaAlphaS:    2.0 / (60.0 + 1.0),
		CurrentRegime: RegimeNormal,
		WeightMom:     0.45,
		WeightVal:     0.35,
		WeightVol:     0.20,
	}
}

// Update feeds a new market observation and evaluates multi-factor alpha.
func (m *MultiFactorModel) Update(price, volume, vpin, spreadBps float64, now time.Time) MultiFactorScore {
	if price <= 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return MultiFactorScore{
			Timestamp:      now,
			Regime:         m.CurrentRegime,
			CompositeScore: 0.0,
			Conviction:     0.50,
			WeightMom:      m.WeightMom,
			WeightVal:      m.WeightVal,
			WeightVol:      m.WeightVol,
		}
	}
	if volume < 0 || math.IsNaN(volume) || math.IsInf(volume, 0) {
		volume = 0
	}
	if math.IsNaN(vpin) || math.IsInf(vpin, 0) || vpin < 0 {
		vpin = 0.50
	}
	if math.IsNaN(spreadBps) || math.IsInf(spreadBps, 0) || spreadBps < 0 {
		spreadBps = 1.0
	}

	// Update circular history
	m.prices[m.head] = price
	m.volumes[m.head] = volume
	m.head = (m.head + 1) % m.windowSize
	if m.count < m.windowSize {
		m.count++
	}

	// Initialize EWMAs if first point
	if m.ewmaFast == 0 {
		m.ewmaFast = price
		m.ewmaSlow = price
		m.meanAcc = price
	} else {
		m.ewmaFast = m.ewmaAlphaF*price + (1.0-m.ewmaAlphaF)*m.ewmaFast
		m.ewmaSlow = m.ewmaAlphaS*price + (1.0-m.ewmaAlphaS)*m.ewmaSlow
	}

	// Online Welford / EWMA variance estimation
	delta := price - m.meanAcc
	m.meanAcc += 0.05 * delta
	m.varAcc = 0.95*m.varAcc + 0.05*(delta*delta)
	rollingStd := math.Sqrt(math.Max(1e-8, m.varAcc))

	// VWAP tracking
	if volume > 0 {
		m.cumVol += volume
		m.cumPV += price * volume
	}
	vwap := price
	if m.cumVol > 0 {
		vwap = m.cumPV / m.cumVol
	}

	// 1. Momentum Factor (F_mom)
	// Ratio of fast EWMA vs slow EWMA + return over available history
	var momFastBps float64
	if m.ewmaSlow > 0 {
		momFastBps = (m.ewmaFast - m.ewmaSlow) / m.ewmaSlow * 1e4
	}
	// Normalize to [-1, +1] assuming 20 bps is significant momentum
	fMom := momFastBps / 20.0
	if fMom > 1.0 {
		fMom = 1.0
	} else if fMom < -1.0 {
		fMom = -1.0
	}

	// 2. Value Factor (F_val) - Mean-Reversion to VWAP / Mean
	// When price is significantly below VWAP (oversold), value factor is positive (BUY).
	// When price is significantly above VWAP (overbought), value factor is negative (SELL).
	zVWAP := 0.0
	if rollingStd > 0 {
		zVWAP = (vwap - price) / rollingStd
	}
	// Cap Z-score to [-2.5, +2.5] and normalize to [-1, +1]
	fVal := zVWAP / 2.5
	if fVal > 1.0 {
		fVal = 1.0
	} else if fVal < -1.0 {
		fVal = -1.0
	}

	// 3. Volatility / Quality Factor (F_vol)
	// Penalize toxic environments (VPIN > 0.60) or wide spreads (> 10 bps).
	// Higher quality / low-risk flow provides directional reinforcement.
	toxicityPenalty := 0.0
	if vpin > 0.50 {
		toxicityPenalty = (vpin - 0.50) * 2.0 // [0, 1]
	}
	spreadPenalty := 0.0
	if spreadBps > 5.0 {
		spreadPenalty = math.Min(1.0, (spreadBps-5.0)/15.0)
	}
	fVol := 1.0 - (0.60*toxicityPenalty + 0.40*spreadPenalty)
	// Align with prevailing momentum: if quality is high, scale with momentum direction
	if fMom >= 0 {
		fVol = fVol * math.Abs(fMom)
	} else {
		fVol = -fVol * math.Abs(fMom)
	}

	// 4. Dynamic Regime Detection & Factor Weight Adaptation
	// If VPIN is high (> 0.70) -> High Vol / Toxic Regime (defend capital, slash momentum)
	// If momentum is dominant (|fMom| > 0.60) -> Trending Regime (heavily favor momentum)
	// If mean-reversion is dominant (|zVWAP| > 1.5) and VPIN is low -> Mean-Reverting Regime
	if vpin > 0.70 {
		m.CurrentRegime = RegimeHighVolToxic
		m.WeightMom = 0.20
		m.WeightVal = 0.30
		m.WeightVol = 0.50
	} else if math.Abs(fMom) >= 0.50 {
		m.CurrentRegime = RegimeTrending
		m.WeightMom = 0.65
		m.WeightVal = 0.15
		m.WeightVol = 0.20
	} else if math.Abs(zVWAP) >= 1.20 {
		m.CurrentRegime = RegimeMeanReverting
		m.WeightMom = 0.20
		m.WeightVal = 0.60
		m.WeightVol = 0.20
	} else {
		m.CurrentRegime = RegimeNormal
		m.WeightMom = 0.45
		m.WeightVal = 0.35
		m.WeightVol = 0.20
	}

	// 5. Composite Score Blending
	composite := m.WeightMom*fMom + m.WeightVal*fVal + m.WeightVol*fVol
	if composite > 1.0 {
		composite = 1.0
	} else if composite < -1.0 {
		composite = -1.0
	}

	// Logistic sigmoid conviction: P(Up) in [0.0, 1.0]
	conviction := 1.0 / (1.0 + math.Exp(-2.5*composite))

	return MultiFactorScore{
		Timestamp:      now,
		Regime:         m.CurrentRegime,
		MomentumFactor: fMom,
		ValueFactor:    fVal,
		VolFactor:      fVol,
		CompositeScore: composite,
		Conviction:     conviction,
		WeightMom:      m.WeightMom,
		WeightVal:      m.WeightVal,
		WeightVol:      m.WeightVol,
	}
}
