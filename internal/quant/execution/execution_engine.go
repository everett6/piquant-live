package execution

import (
	"errors"
	"math"
	"time"
)

// ChildOrderSlice describes an individual execution slice dispatched by an optimal execution algorithm.
type ChildOrderSlice struct {
	Index          int
	TotalSlices    int
	ScheduledTime  time.Time
	Quantity       float64
	LimitPrice     float64
	Algorithm      string // "TWAP", "VWAP", "IMPLEMENTATION_SHORTFALL"
	PacingModifier float64
}

// TWAPRouter calculates uniform time-weighted average price order slicing.
type TWAPRouter struct {
	SliceInterval time.Duration
}

// NewTWAPRouter creates a TWAP execution scheduler.
func NewTWAPRouter(interval time.Duration) *TWAPRouter {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &TWAPRouter{SliceInterval: interval}
}

// ScheduleSlices divides totalQuantity into equal slices across totalDuration.
func (tw *TWAPRouter) ScheduleSlices(totalQuantity float64, totalDuration time.Duration, startTime time.Time) ([]ChildOrderSlice, error) {
	if totalQuantity <= 0 || totalDuration <= 0 {
		return nil, errors.New("twap: invalid quantity or duration")
	}

	numSlices := int(totalDuration / tw.SliceInterval)
	if numSlices < 1 {
		numSlices = 1
	}

	slices := make([]ChildOrderSlice, numSlices)
	sliceQty := totalQuantity / float64(numSlices)

	var allocated float64
	for i := 0; i < numSlices; i++ {
		q := sliceQty
		if i == numSlices-1 {
			q = totalQuantity - allocated // eliminate float rounding dust
		}
		slices[i] = ChildOrderSlice{
			Index:          i,
			TotalSlices:    numSlices,
			ScheduledTime:  startTime.Add(time.Duration(i) * tw.SliceInterval),
			Quantity:       q,
			Algorithm:      "TWAP",
			PacingModifier: 1.0,
		}
		allocated += q
	}
	return slices, nil
}

// VWAPProfile holds the 24-hour intraday volume distribution curve (UTC hours 0..23).
type VWAPProfile struct {
	HourlyVolumeFraction [24]float64
}

// DefaultCryptoVWAPProfile returns empirical 24-hour Bitcoin intraday volume distribution:
// Higher volume during US-EU session overlaps (13:00 - 17:00 UTC), lower during late Asian lulls (03:00 - 06:00 UTC).
func DefaultCryptoVWAPProfile() VWAPProfile {
	// Normalized weights summing to 1.0
	weights := [24]float64{
		0.038, 0.035, 0.032, 0.030, 0.028, 0.027, // 00:00 - 05:00 UTC (Asia early)
		0.031, 0.036, 0.042, 0.048, 0.050, 0.049, // 06:00 - 11:00 UTC (Europe open)
		0.053, 0.062, 0.068, 0.065, 0.061, 0.054, // 12:00 - 17:00 UTC (US-EU peak overlap)
		0.048, 0.044, 0.040, 0.038, 0.036, 0.035, // 18:00 - 23:00 UTC (US afternoon)
	}
	var sum float64
	for _, w := range weights {
		sum += w
	}
	var norm [24]float64
	for i, w := range weights {
		norm[i] = w / sum
	}
	return VWAPProfile{HourlyVolumeFraction: norm}
}

// VWAPRouter calculates volume-weighted average price order slicing matching market liquidity.
type VWAPRouter struct {
	Profile       VWAPProfile
	SliceInterval time.Duration
}

// NewVWAPRouter creates a VWAP execution scheduler.
func NewVWAPRouter(profile VWAPProfile, interval time.Duration) *VWAPRouter {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &VWAPRouter{
		Profile:       profile,
		SliceInterval: interval,
	}
}

// ScheduleSlices sizes each child slice proportional to expected intraday market volume.
func (vw *VWAPRouter) ScheduleSlices(totalQuantity float64, totalDuration time.Duration, startTime time.Time) ([]ChildOrderSlice, error) {
	if totalQuantity <= 0 || totalDuration <= 0 {
		return nil, errors.New("vwap: invalid quantity or duration")
	}

	numSlices := int(totalDuration / vw.SliceInterval)
	if numSlices < 1 {
		numSlices = 1
	}

	// Calculate expected volume weights across slices
	sliceWeights := make([]float64, numSlices)
	var sumWeights float64

	for i := 0; i < numSlices; i++ {
		sliceTime := startTime.Add(time.Duration(i) * vw.SliceInterval)
		utcHour := sliceTime.UTC().Hour()
		w := vw.Profile.HourlyVolumeFraction[utcHour]
		sliceWeights[i] = w
		sumWeights += w
	}

	if sumWeights <= 0 {
		sumWeights = float64(numSlices)
		for i := range sliceWeights {
			sliceWeights[i] = 1.0
		}
	}

	slices := make([]ChildOrderSlice, numSlices)
	var allocated float64
	for i := 0; i < numSlices; i++ {
		frac := sliceWeights[i] / sumWeights
		q := totalQuantity * frac
		if i == numSlices-1 {
			q = totalQuantity - allocated
		}
		slices[i] = ChildOrderSlice{
			Index:          i,
			TotalSlices:    numSlices,
			ScheduledTime:  startTime.Add(time.Duration(i) * vw.SliceInterval),
			Quantity:       q,
			Algorithm:      "VWAP",
			PacingModifier: frac * float64(numSlices),
		}
		allocated += q
	}
	return slices, nil
}

// AdaptiveShortfallRouter implements dynamic Almgren-Chriss Implementation Shortfall execution.
// Continuously balances delay risk (variance of unexecuted position) against market impact,
// actively modulating execution speed based on real-time book liquidity and order flow toxicity.
type AdaptiveShortfallRouter struct {
	RiskAversionLambda float64 // Risk aversion parameter lambda (default: 1e-4)
	TempImpactEta      float64 // Temporary price impact coefficient eta (default: 0.05)
}

// NewAdaptiveShortfallRouter creates an adaptive Implementation Shortfall router.
func NewAdaptiveShortfallRouter(lambda, eta float64) *AdaptiveShortfallRouter {
	if lambda <= 0 {
		lambda = 1e-4
	}
	if eta <= 0 {
		eta = 0.05
	}
	return &AdaptiveShortfallRouter{
		RiskAversionLambda: lambda,
		TempImpactEta:      eta,
	}
}

// ComputeSchedule calculates child slice trajectory dynamically modulated by live order book depth and VPIN.
func (is *AdaptiveShortfallRouter) ComputeSchedule(
	totalQuantity float64,
	numSlices int,
	volatilityBps float64,
	top5Depth float64,
	liveVpin float64,
) []ChildOrderSlice {
	if numSlices <= 0 || totalQuantity <= 0 {
		return nil
	}

	// 1. Dynamic Liquidity Adaptation:
	// If VPIN is high (toxic predatory flow) or depth is thin, increase temporary impact eta -> slows down execution!
	// If depth is deep and VPIN is calm, decrease eta -> accelerates execution to minimize volatility risk!
	effectiveEta := is.TempImpactEta
	if liveVpin > 0.65 {
		effectiveEta *= 2.5 // Heavy predatory presence: slow down to avoid market impact
	} else if liveVpin < 0.25 {
		effectiveEta *= 0.70 // Calm book: accelerate execution
	}
	if top5Depth > 0 && top5Depth < 50000.0 {
		effectiveEta *= 1.8 // Thin book depth: reduce aggressive slicing
	}

	sigma := math.Max(1.0, volatilityBps)
	kappa := math.Sqrt(is.RiskAversionLambda * sigma * sigma / effectiveEta)
	bigT := float64(numSlices)
	denom := math.Sinh(kappa * bigT)

	slices := make([]ChildOrderSlice, numSlices)

	// Risk-neutral limit (kappa -> 0): uniform TWAP schedule
	if kappa*bigT < 1e-4 || math.IsInf(denom, 0) || denom <= 1e-12 {
		uniform := totalQuantity / bigT
		var sum float64
		for k := 0; k < numSlices; k++ {
			q := uniform
			if k == numSlices-1 {
				q = totalQuantity - sum
			}
			slices[k] = ChildOrderSlice{
				Index:          k,
				TotalSlices:    numSlices,
				Quantity:       q,
				Algorithm:      "IMPLEMENTATION_SHORTFALL_UNIFORM",
				PacingModifier: 1.0,
			}
			sum += q
		}
		return slices
	}

	prevRem := totalQuantity
	var sumAllocated float64
	for k := 0; k < numSlices-1; k++ {
		remNext := totalQuantity * math.Sinh(kappa*(bigT-float64(k+1))) / denom
		nk := prevRem - remNext
		if nk < 0 {
			nk = 0
		}
		slices[k] = ChildOrderSlice{
			Index:          k,
			TotalSlices:    numSlices,
			Quantity:       nk,
			Algorithm:      "IMPLEMENTATION_SHORTFALL_ADAPTIVE",
			PacingModifier: nk / (totalQuantity / bigT),
		}
		sumAllocated += nk
		prevRem = remNext
	}
	slices[numSlices-1] = ChildOrderSlice{
		Index:          numSlices - 1,
		TotalSlices:    numSlices,
		Quantity:       totalQuantity - sumAllocated,
		Algorithm:      "IMPLEMENTATION_SHORTFALL_ADAPTIVE",
		PacingModifier: (totalQuantity - sumAllocated) / (totalQuantity / bigT),
	}

	return slices
}
