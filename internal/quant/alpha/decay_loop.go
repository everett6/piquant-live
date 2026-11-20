package alpha

import (
	"math"
)

// DecayStatus classifies a signal's live out-of-sample health net of transaction costs.
type DecayStatus string

const (
	DecayHealthy  DecayStatus = "HEALTHY"
	DecayThrottled DecayStatus = "THROTTLED"
	DecaySuspended DecayStatus = "SUSPENDED_TO_TBILLS"
)

// SleeveAlphaHealth summarizes MFT alpha-decay diagnostics for a single strategy sleeve.
type SleeveAlphaHealth struct {
	Name            string      `json:"name"`
	Days            int         `json:"days"`
	Rolling30dSharpe float64    `json:"rolling_30d_sharpe"`
	Rolling60dSharpe float64    `json:"rolling_60d_sharpe"`
	EWMA20dNetBps   float64     `json:"ewma_20d_net_bps"`
	Lag1Autocorr    float64     `json:"lag1_autocorr"`
	ThrottleMult    float64     `json:"throttle_mult"`
	Status          DecayStatus `json:"status"`
}

// SelfImprovingAllocation is the daily output of the causal Multiplicative-Weights alpha-decay loop.
type SelfImprovingAllocation struct {
	Sleeves        []SleeveAlphaHealth `json:"sleeves"`
	Weights        map[string]float64  `json:"weights"`
	TBillSafeShare float64             `json:"tbill_safe_share"`
	DaysProcessed  int                 `json:"days_processed"`
}

// ComputeAlphaHealth computes causal EWMA net edge (bps/day), 30d/60d rolling Sharpe, lag-1 return
// persistence, and a smooth [floorMult, 1.0] decay throttle for one sleeve's daily net return stream.
func ComputeAlphaHealth(name string, netDailyRets []float64, floorMult float64) SleeveAlphaHealth {
	if floorMult <= 0 || floorMult > 1 {
		floorMult = 0.35
	}
	n := len(netDailyRets)
	h := SleeveAlphaHealth{
		Name:         name,
		Days:         n,
		ThrottleMult: 1.0,
		Status:       DecayHealthy,
	}
	if n == 0 {
		return h
	}

	// EWMA net return in bps/day (20-day span: alpha = 2/(20+1)).
	const span = 20.0
	ewmAlpha := 2.0 / (span + 1.0)
	ewm := netDailyRets[0]
	for i := 1; i < n; i++ {
		ewm = ewmAlpha*netDailyRets[i] + (1.0-ewmAlpha)*ewm
	}
	h.EWMA20dNetBps = round2Alpha(ewm * 10000.0)

	h.Rolling30dSharpe = round4Alpha(rollingSharpe(netDailyRets, 30))
	h.Rolling60dSharpe = round4Alpha(rollingSharpe(netDailyRets, 60))
	h.Lag1Autocorr = round4Alpha(lag1Autocorr(netDailyRets, 60))

	if n >= 20 {
		sr := h.Rolling60dSharpe
		switch {
		case sr >= 0.5:
			h.ThrottleMult = 1.0
			h.Status = DecayHealthy
		case sr <= -0.5:
			h.ThrottleMult = round4Alpha(floorMult)
			h.Status = DecaySuspended
		default:
			frac := (sr + 0.5) / 1.0
			h.ThrottleMult = round4Alpha(floorMult + (1.0-floorMult)*frac)
			h.Status = DecayThrottled
		}
	}
	return h
}

// RunSelfImprovingLoop executes a strictly causal Exponential-Gradient / Multiplicative-Weights update
// across aligned daily net return streams (e.g. "qd", "r3", "carry", "dtb3") so decaying sleeves
// automatically shed weight into healthy sleeves and risk-free DTB3 T-bills.
func RunSelfImprovingLoop(sleeveRets map[string][]float64, eta, minCarry, maxTrend float64) SelfImprovingAllocation {
	order := []string{"qd", "r3", "carry", "dtb3"}
	w := map[string]float64{"qd": 0.35, "r3": 0.25, "carry": 0.30, "dtb3": 0.10}
	if eta <= 0 {
		eta = 0.15
	}
	if minCarry < 0 || minCarry > 0.9 {
		minCarry = 0.25
	}
	if maxTrend <= 0 || maxTrend > 1-minCarry {
		maxTrend = 1.0 - minCarry
	}

	minLen := -1
	for _, k := range order {
		l := len(sleeveRets[k])
		if minLen < 0 || l < minLen {
			minLen = l
		}
	}
	if minLen <= 0 {
		minLen = 0
	}

	for t := 0; t < minLen; t++ {
		if t >= 20 {
			for _, k := range order {
				sd := windowStd(sleeveRets[k], t, 45)
				if sd < 0.005 {
					sd = 0.005
				}
				step := sleeveRets[k][t] / sd
				if step > 3.0 {
					step = 3.0
				} else if step < -3.0 {
					step = -3.0
				}
				w[k] *= math.Exp(eta * step)
			}
			projectSimplex(w, minCarry, maxTrend)
		}
	}

	health := make([]SleeveAlphaHealth, 0, len(order))
	for _, k := range order {
		health = append(health, ComputeAlphaHealth(k, sleeveRets[k], 0.35))
	}
	rounded := make(map[string]float64, len(w))
	for k, v := range w {
		rounded[k] = round4Alpha(v)
	}
	return SelfImprovingAllocation{
		Sleeves:        health,
		Weights:        rounded,
		TBillSafeShare: rounded["dtb3"],
		DaysProcessed:  minLen,
	}
}

func projectSimplex(w map[string]float64, minCarry, maxTrend float64) {
	sum := 0.0
	for _, v := range w {
		if v > 0 {
			sum += v
		}
	}
	if sum <= 0 {
		w["qd"], w["r3"], w["carry"], w["dtb3"] = 0.35, 0.25, 0.30, 0.10
		return
	}
	for k := range w {
		w[k] /= sum
	}
	if w["carry"] < minCarry {
		deficit := minCarry - w["carry"]
		w["carry"] = minCarry
		other := w["qd"] + w["r3"] + w["dtb3"]
		if other > 1e-9 {
			scale := math.Max(0, (other-deficit)/other)
			w["qd"] *= scale
			w["r3"] *= scale
			w["dtb3"] *= scale
		}
	}
	if trendSum := w["qd"] + w["r3"]; trendSum > maxTrend && trendSum > 1e-9 {
		excess := trendSum - maxTrend
		scale := maxTrend / trendSum
		w["qd"] *= scale
		w["r3"] *= scale
		w["dtb3"] += excess
	}
}

func rollingSharpe(rets []float64, win int) float64 {
	if len(rets) < 20 {
		return 0
	}
	start := len(rets) - win
	if start < 0 {
		start = 0
	}
	sub := rets[start:]
	var sum, sum2 float64
	for _, r := range sub {
		sum += r
		sum2 += r * r
	}
	fn := float64(len(sub))
	mu := sum / fn
	v := (sum2 - fn*mu*mu) / (fn - 1)
	if v <= 0 {
		return 0
	}
	return mu / math.Sqrt(v) * math.Sqrt(365.0)
}

func windowStd(rets []float64, endIdx, win int) float64 {
	start := endIdx - win + 1
	if start < 0 {
		start = 0
	}
	sub := rets[start : endIdx+1]
	if len(sub) < 2 {
		return 0.01
	}
	var sum, sum2 float64
	for _, r := range sub {
		sum += r
		sum2 += r * r
	}
	fn := float64(len(sub))
	mu := sum / fn
	v := (sum2 - fn*mu*mu) / (fn - 1)
	if v <= 0 {
		return 0.01
	}
	return math.Sqrt(v)
}

func lag1Autocorr(rets []float64, win int) float64 {
	if len(rets) < 10 {
		return 0
	}
	start := len(rets) - win
	if start < 0 {
		start = 0
	}
	sub := rets[start:]
	if len(sub) < 5 {
		return 0
	}
	var sx, sy, sxx, syy, sxy float64
	n := float64(len(sub) - 1)
	for i := 1; i < len(sub); i++ {
		x, y := sub[i-1], sub[i]
		sx += x
		sy += y
		sxx += x * x
		syy += y * y
		sxy += x * y
	}
	num := n*sxy - sx*sy
	den := math.Sqrt((n*sxx - sx*sx) * (n*syy - sy*sy))
	if den <= 1e-12 {
		return 0
	}
	return num / den
}

func round2Alpha(v float64) float64 { return math.Round(v*100) / 100 }
func round4Alpha(v float64) float64 { return math.Round(v*10000) / 10000 }
