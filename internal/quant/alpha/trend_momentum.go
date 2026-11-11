package alpha

import (
	"math"
	"sort"
	"time"
)

// KalmanTrendFilter implements a 2-state linear Kalman filter tracking true latent asset price
// and latent trend velocity in real-time, removing high-frequency microstructure noise and bid-ask bounce.
// State vector: x = [price, velocity]^T
type KalmanTrendFilter struct {
	Price    float64 // State 0: estimated latent true price
	Velocity float64 // State 1: estimated latent price velocity (USD per second)

	// State covariance matrix P = [[p00, p01], [p10, p11]]
	P00 float64
	P01 float64
	P10 float64
	P11 float64

	ProcessNoiseVar float64 // Q: process disturbance variance
	MeasurementVar  float64 // R: measurement noise variance (bid-ask bounce / tick noise)

	LastUpdate time.Time
}

// NewKalmanTrendFilter initializes a noise-stripping trend filter.
func NewKalmanTrendFilter(processNoise, measurementNoise float64) *KalmanTrendFilter {
	if processNoise <= 0 {
		processNoise = 1e-4
	}
	if measurementNoise <= 0 {
		measurementNoise = 1e-2
	}
	return &KalmanTrendFilter{
		P00:             1.0,
		P11:             1.0,
		ProcessNoiseVar: processNoise,
		MeasurementVar:  measurementNoise,
	}
}

// Update processes a raw noisy market price observation and returns the filtered latent price and trend velocity.
func (kf *KalmanTrendFilter) Update(rawPrice float64, now time.Time) (filteredPrice float64, velocityBpsPerSec float64) {
	if rawPrice <= 0 || math.IsNaN(rawPrice) || math.IsInf(rawPrice, 0) {
		return kf.Price, 0
	}
	if kf.LastUpdate.IsZero() {
		kf.Price = rawPrice
		kf.Velocity = 0
		kf.LastUpdate = now
		return rawPrice, 0
	}

	dt := now.Sub(kf.LastUpdate).Seconds()
	if dt <= 0 {
		dt = 0.05 // default 50ms tick interval
	} else if dt > 60.0 {
		dt = 60.0 // bound large time jumps
	}
	kf.LastUpdate = now

	// 1. Prediction step:
	// Transition matrix F = [[1, dt], [0, 1]]
	// x_pred = F * x
	predPrice := kf.Price + kf.Velocity*dt
	predVel := kf.Velocity

	// Process noise covariance Q: continuous white noise acceleration model
	q := kf.ProcessNoiseVar
	q00 := (dt * dt * dt / 3.0) * q
	q01 := (dt * dt / 2.0) * q
	q11 := dt * q

	// P_pred = F * P * F^T + Q
	p00 := kf.P00 + dt*(kf.P10+kf.P01) + dt*dt*kf.P11 + q00
	p01 := kf.P01 + dt*kf.P11 + q01
	p10 := kf.P10 + dt*kf.P11 + q01
	p11 := kf.P11 + q11

	// 2. Innovation step:
	// Observation matrix H = [1, 0]
	// y = z - H * x_pred
	y := rawPrice - predPrice
	// Innovation covariance S = H * P_pred * H^T + R = p00 + R
	s := p00 + kf.MeasurementVar
	if s < 1e-12 {
		s = 1e-12
	}

	// 3. Kalman gain: K = P_pred * H^T / S = [p00/s, p10/s]^T
	k0 := p00 / s
	k1 := p10 / s

	// 4. State update: x = x_pred + K * y
	kf.Price = predPrice + k0*y
	kf.Velocity = predVel + k1*y

	// 5. Covariance update: P = (I - K * H) * P_pred
	kf.P00 = (1.0 - k0) * p00
	kf.P01 = (1.0 - k0) * p01
	kf.P10 = p10 - k1*p00
	kf.P11 = p11 - k1*p01

	if math.IsNaN(kf.Price) || math.IsInf(kf.Price, 0) || math.IsNaN(kf.Velocity) || math.IsInf(kf.Velocity, 0) {
		kf.Price = rawPrice
		kf.Velocity = 0
		kf.P00 = 1.0
		kf.P01 = 0.0
		kf.P10 = 0.0
		kf.P11 = 1.0
	}
	if math.IsNaN(kf.Velocity) || math.IsInf(kf.Velocity, 0) {
		kf.Velocity = 0
	}

	// Convert velocity into basis points per second
	if kf.Price > 0 {
		velocityBpsPerSec = (kf.Velocity / kf.Price) * 1e4
	}
	if math.IsNaN(velocityBpsPerSec) || math.IsInf(velocityBpsPerSec, 0) {
		velocityBpsPerSec = 0
	}
	return kf.Price, velocityBpsPerSec
}

// MultiHorizonMomentum tracks capital flows and returns across 30s, 5m, 15m, 1h, and 1d horizons.
type MultiHorizonMomentum struct {
	Ret30sBps float64
	Ret5mBps  float64
	Ret15mBps float64
	Ret1hBps  float64
	Ret1dBps  float64

	Atr15mBps float64
	Vol30sBps float64

	TrendScore float64 // Directional composite momentum score in [-1.0, 1.0]
}

// SnapshotPoint records an event time midpoint snapshot.
type SnapshotPoint struct {
	Time  time.Time
	Price float64
}

// MomentumTracker maintains a rolling circular snapshot window for multi-horizon trend calculations.
type MomentumTracker struct {
	History     []SnapshotPoint
	Kalman      *KalmanTrendFilter
	MaxCapacity int
}

// NewMomentumTracker creates a multi-timeframe momentum tracker.
func NewMomentumTracker(maxCapacity int) *MomentumTracker {
	if maxCapacity < 100 {
		maxCapacity = 2000
	}
	return &MomentumTracker{
		History:     make([]SnapshotPoint, 0, maxCapacity),
		Kalman:      NewKalmanTrendFilter(1e-4, 5e-3),
		MaxCapacity: maxCapacity,
	}
}

// Update ingests a new price observation and updates multi-horizon momentum returns and the Kalman noise-stripped trend.
func (mt *MomentumTracker) Update(rawPrice float64, now time.Time) MultiHorizonMomentum {
	if rawPrice <= 0 {
		return MultiHorizonMomentum{}
	}

	// 1. Filter price with Kalman filter
	filteredPrice, _ := mt.Kalman.Update(rawPrice, now)

	// Rate-limit history snapshots to at most 1 every 250ms
	n := len(mt.History)
	if n == 0 || now.Sub(mt.History[n-1].Time) >= 250*time.Millisecond {
		mt.History = append(mt.History, SnapshotPoint{Time: now, Price: filteredPrice})
		if len(mt.History) > mt.MaxCapacity {
			mt.History = mt.History[len(mt.History)-mt.MaxCapacity:]
		}
	}

	// 2. Multi-horizon lookbacks
	t30s := now.Add(-30 * time.Second)
	t5m := now.Add(-5 * time.Minute)
	t15m := now.Add(-15 * time.Minute)
	t1h := now.Add(-60 * time.Minute)
	t1d := now.Add(-24 * time.Hour)

	var p30s, p5m, p15m, p1h, p1d float64
	high15m := filteredPrice
	low15m := filteredPrice

	// History is strictly time-ordered (a point is only appended when now is at
	// least 250ms past the last one), so each lookback price is the latest
	// point at or before the cutoff, found by binary search; the 15m
	// high/low scan only touches the 15m window.
	hist := mt.History
	atOrBefore := func(t time.Time) float64 {
		i := sort.Search(len(hist), func(j int) bool { return hist[j].Time.After(t) })
		if i == 0 {
			return 0
		}
		return hist[i-1].Price
	}
	p30s = atOrBefore(t30s)
	p5m = atOrBefore(t5m)
	p15m = atOrBefore(t15m)
	p1h = atOrBefore(t1h)
	p1d = atOrBefore(t1d)
	start := sort.Search(len(hist), func(j int) bool { return !hist[j].Time.Before(t15m) })
	for i := start; i < len(hist); i++ {
		if pr := hist[i].Price; pr > high15m {
			high15m = pr
		} else if pr < low15m {
			low15m = pr
		}
	}

	var m MultiHorizonMomentum
	if p30s > 0 {
		m.Ret30sBps = (filteredPrice - p30s) / p30s * 1e4
	}
	if p5m > 0 {
		m.Ret5mBps = (filteredPrice - p5m) / p5m * 1e4
	}
	if p15m > 0 {
		m.Ret15mBps = (filteredPrice - p15m) / p15m * 1e4
	}
	if p1h > 0 {
		m.Ret1hBps = (filteredPrice - p1h) / p1h * 1e4
	}
	if p1d > 0 {
		m.Ret1dBps = (filteredPrice - p1d) / p1d * 1e4
	}

	if filteredPrice > 0 {
		m.Atr15mBps = (high15m - low15m) / filteredPrice * 1e4
	}
	if m.Atr15mBps < 10.0 {
		m.Atr15mBps = 10.0
	}

	// 3. Normalized Trend Following Score:
	// Weights structural capital flows across horizons with volatility normalization:
	// 30s: 15%, 5m: 25%, 15m: 30%, 1h: 20%, 1d: 10%
	z30s := m.Ret30sBps / 12.0
	z5m := m.Ret5mBps / 25.0
	z15m := m.Ret15mBps / 40.0
	z1h := m.Ret1hBps / 75.0
	z1d := m.Ret1dBps / 150.0

	zComposite := 0.15*z30s + 0.25*z5m + 0.30*z15m + 0.20*z1h + 0.10*z1d
	// Hyperbolic tangent score in [-1.0, 1.0]
	m.TrendScore = math.Tanh(zComposite * 0.75)
	return m
}
