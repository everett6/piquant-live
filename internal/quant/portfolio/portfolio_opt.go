package portfolio

import (
	"errors"
	"math"
)

// RiskParityWeights computes equal risk contribution weights across N assets:
// w_i = (1 / sigma_i) / sum_j (1 / sigma_j)
func RiskParityWeights(vols []float64) ([]float64, error) {
	n := len(vols)
	if n == 0 {
		return nil, errors.New("risk_parity: empty volatility slice")
	}
	out := make([]float64, n)
	var sumInvVol float64
	for i := 0; i < n; i++ {
		v := vols[i]
		if v <= 1e-8 || math.IsNaN(v) {
			v = 1e-4
		}
		inv := 1.0 / v
		out[i] = inv
		sumInvVol += inv
	}
	if sumInvVol <= 0 {
		sumInvVol = float64(n)
	}
	for i := 0; i < n; i++ {
		out[i] /= sumInvVol
	}
	return out, nil
}

// BlackLittermanModel implements institutional Black-Litterman asset allocation blending
// equilibrium market returns with subjective/algorithmic alpha views.
type BlackLittermanModel struct {
	Tau          float64 // Uncertainty scalar on prior equilibrium (default: 0.05)
	RiskAversion float64 // Market risk aversion coefficient gamma (default: 2.5)
}

// NewBlackLittermanModel creates a Black-Litterman portfolio optimizer.
func NewBlackLittermanModel(tau, riskAversion float64) *BlackLittermanModel {
	if tau <= 0 {
		tau = 0.05
	}
	if riskAversion <= 0 {
		riskAversion = 2.5
	}
	return &BlackLittermanModel{
		Tau:          tau,
		RiskAversion: riskAversion,
	}
}

// Optimize computes optimal Black-Litterman weights:
//
//	w* = (gamma * Sigma)^{-1} * E[R]_{BL}
//
// where E[R]_{BL} blends prior equilibrium Pi with views Q (with confidence Omega).
// For 2 assets (BTC, ETH), this is computed with analytical matrix inversion on the stack (0 allocs).
func (bl *BlackLittermanModel) Optimize2Asset(
	sigma1, sigma2 float64, corr float64,
	priorWeight1, priorWeight2 float64,
	viewExpectedReturn1, viewExpectedReturn2 float64,
	viewConfidence1, viewConfidence2 float64,
) (w1, w2 float64, expectedRet1, expectedRet2 float64) {
	if math.IsNaN(sigma1) || math.IsInf(sigma1, 0) || sigma1 <= 0 {
		sigma1 = 1e-2
	}
	if math.IsNaN(sigma2) || math.IsInf(sigma2, 0) || sigma2 <= 0 {
		sigma2 = 1e-2
	}
	if math.IsNaN(corr) || math.IsInf(corr, 0) {
		corr = 0.50
	}
	if math.IsNaN(priorWeight1) || math.IsInf(priorWeight1, 0) {
		priorWeight1 = 0.50
	}
	if math.IsNaN(priorWeight2) || math.IsInf(priorWeight2, 0) {
		priorWeight2 = 0.50
	}
	if math.IsNaN(viewExpectedReturn1) || math.IsInf(viewExpectedReturn1, 0) {
		viewExpectedReturn1 = 0.0
	}
	if math.IsNaN(viewExpectedReturn2) || math.IsInf(viewExpectedReturn2, 0) {
		viewExpectedReturn2 = 0.0
	}

	s1 := math.Max(1e-4, sigma1)
	s2 := math.Max(1e-4, sigma2)
	rho := math.Max(-0.95, math.Min(0.95, corr))

	// Covariance matrix Sigma:
	// cov00 = s1^2, cov01 = rho*s1*s2, cov11 = s2^2
	cov00 := s1 * s1
	cov01 := rho * s1 * s2
	cov11 := s2 * s2

	// Implied equilibrium returns Pi = gamma * Sigma * w_mkt
	pi1 := bl.RiskAversion * (cov00*priorWeight1 + cov01*priorWeight2)
	pi2 := bl.RiskAversion * (cov01*priorWeight1 + cov11*priorWeight2)

	// Blend views with equilibrium:
	// confidence c in [0, 1] acts as weight on the algorithmic view vs equilibrium:
	// E[R]_i = (1 - c_i) * Pi_i + c_i * View_i
	c1 := math.Max(0.0, math.Min(1.0, viewConfidence1))
	c2 := math.Max(0.0, math.Min(1.0, viewConfidence2))

	er1 := (1.0-c1)*pi1 + c1*viewExpectedReturn1
	er2 := (1.0-c2)*pi2 + c2*viewExpectedReturn2

	// Solve Mean-Variance Optimal weights: w = (1 / gamma) * Sigma^{-1} * E[R]
	det := cov00*cov11 - cov01*cov01
	if det < 1e-12 {
		det = 1e-12
	}
	inv00 := cov11 / det
	inv01 := -cov01 / det
	inv11 := cov00 / det

	rawW1 := (1.0 / bl.RiskAversion) * (inv00*er1 + inv01*er2)
	rawW2 := (1.0 / bl.RiskAversion) * (inv01*er1 + inv11*er2)

	// Long-only projection for cash accounts (spot)
	if rawW1 < 0 {
		rawW1 = 0
	}
	if rawW2 < 0 {
		rawW2 = 0
	}
	sumW := rawW1 + rawW2
	if sumW > 1.0 {
		rawW1 /= sumW
		rawW2 /= sumW
	}

	return rawW1, rawW2, er1, er2
}

// DynamicKellySizer calculates mathematically optimal bet sizing using Modified Half-Kelly.
type DynamicKellySizer struct {
	KellyFraction float64 // Fraction of full Kelly (default: 0.5 for Half-Kelly safety)
	MaxGrossAlloc float64 // Maximum capital allocation fraction (e.g. 0.85 = 85%)
	MinNotional   float64 // Minimum trade notional in USD (e.g. $5.00 on Binance)
}

// NewDynamicKellySizer creates a Kelly position sizing controller.
func NewDynamicKellySizer(fraction, maxGrossAlloc, minNotional float64) *DynamicKellySizer {
	if fraction <= 0 {
		fraction = 0.5 // Half-Kelly default
	}
	if maxGrossAlloc <= 0 {
		maxGrossAlloc = 0.85
	}
	if minNotional <= 0 {
		minNotional = 5.0
	}
	return &DynamicKellySizer{
		KellyFraction: fraction,
		MaxGrossAlloc: maxGrossAlloc,
		MinNotional:   minNotional,
	}
}

// ComputeBetSize calculates the optimal dollar notional and coin quantity:
//
//	Full Kelly: f* = (p * b - q) / b
//	Half Kelly: f_half = 0.5 * f*
//
// Modulated inversely by realized ATR volatility relative to baseline 35 bps.
func (ks *DynamicKellySizer) ComputeBetSize(
	equity float64, price float64, atrBps float64,
	winRate float64, payoffRatio float64, stopLossBps float64,
) (orderQty float64, notionalUSD float64) {
	if math.IsNaN(equity) || math.IsInf(equity, 0) || equity <= 0 ||
		math.IsNaN(price) || math.IsInf(price, 0) || price <= 0 {
		return 0, 0
	}
	if math.IsNaN(atrBps) || math.IsInf(atrBps, 0) || atrBps <= 0 {
		atrBps = 35.0
	}
	if math.IsNaN(winRate) || math.IsInf(winRate, 0) {
		winRate = 0.55
	}
	if math.IsNaN(payoffRatio) || math.IsInf(payoffRatio, 0) {
		payoffRatio = 0.833
	}
	if math.IsNaN(stopLossBps) || math.IsInf(stopLossBps, 0) || stopLossBps <= 0 {
		stopLossBps = 60.0
	}

	// Default parameters if uninitialized
	p := winRate
	if p <= 0.05 {
		p = 0.55 // default conservative win rate
	} else if p > 0.95 {
		p = 0.95
	}
	q := 1.0 - p

	b := payoffRatio
	if b <= 0.1 {
		b = 0.833 // default 50 bps win / 60 bps loss = 0.833
	}

	// 1. Classical Kelly edge: (p*b - q) / b
	fullKelly := (p*b - q) / b
	if fullKelly < 0.02 {
		fullKelly = 0.02 // maintain minimum participation floor
	} else if fullKelly > 0.40 {
		fullKelly = 0.40
	}

	halfKelly := ks.KellyFraction * fullKelly

	// 2. Volatility Scaling: scale inversely with ATR relative to baseline 35 bps
	vol := math.Max(10.0, atrBps)
	volFactor := math.Sqrt(35.0 / vol)
	volFactor = math.Max(0.40, math.Min(1.60, volFactor))

	// 3. Risk Budget: stop loss distance
	stopBps := math.Max(30.0, math.Max(stopLossBps, atrBps*1.25))
	targetRiskPct := halfKelly * volFactor * (stopBps * 1e-4) * 20.0
	if targetRiskPct > 0.025 {
		targetRiskPct = 0.025 // Hard cap: max 2.5% account equity risk per trade
	} else if targetRiskPct < 0.005 {
		targetRiskPct = 0.005
	}

	targetRiskUSD := equity * targetRiskPct
	stopDistUSD := price * (stopBps * 1e-4)
	if stopDistUSD <= 0 {
		stopDistUSD = price * 0.005
	}

	// 4. Base quantity derived from risk budget
	rawQty := targetRiskUSD / stopDistUSD
	rawNotional := rawQty * price

	// Bound to max portfolio allocation
	maxNotional := equity * ks.MaxGrossAlloc
	if rawNotional > maxNotional {
		rawNotional = maxNotional
		rawQty = rawNotional / price
	}
	if rawNotional < ks.MinNotional {
		rawNotional = ks.MinNotional
		rawQty = rawNotional / price
	}

	if math.IsNaN(rawQty) || math.IsInf(rawQty, 0) || rawQty <= 0 {
		rawQty = 0
		rawNotional = 0
	}

	return rawQty, rawNotional
}
