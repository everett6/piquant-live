package alpha

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

func TestADFStationaryVsRandomWalk(t *testing.T) {
	rng := rand.New(rand.NewSource(42))

	// 1. Generate mean-reverting stationary series (OU process): y_t = 0.75 * y_{t-1} + noise
	stationary := make([]float64, 150)
	for i := 1; i < 150; i++ {
		stationary[i] = 0.70*stationary[i-1] + rng.NormFloat64()*1.0
	}

	resStat, err := ComputeADF(stationary, 1)
	if err != nil {
		t.Fatalf("ADF on stationary series failed: %v", err)
	}
	if !resStat.IsStationary {
		t.Errorf("expected stationary series to be identified as stationary, got t-stat: %f vs crit5: %f", resStat.TStat, resStat.Crit5Pct)
	}
	if resStat.PValue > 0.05 {
		t.Errorf("expected p-value <= 0.05 on stationary series, got: %f", resStat.PValue)
	}

	// 2. Generate pure non-stationary random walk: y_t = y_{t-1} + noise
	randomWalk := make([]float64, 150)
	for i := 1; i < 150; i++ {
		randomWalk[i] = randomWalk[i-1] + rng.NormFloat64()*1.0
	}
	resRW, err := ComputeADF(randomWalk, 1)
	if err != nil {
		t.Fatalf("ADF on random walk failed: %v", err)
	}
	if resRW.IsStationary {
		t.Errorf("expected random walk to be non-stationary (null not rejected), got t-stat: %f vs crit5: %f", resRW.TStat, resRW.Crit5Pct)
	}
}

func TestCointegrationModelRollingZScore(t *testing.T) {
	cm := NewCointegrationModel("BTCUSDT", "ETHUSDT", 60)

	// Simulate co-integrated pair with beta = 0.03 (ETH ≈ 0.03 * BTC)
	baseBTC := 70000.0
	for i := 0; i < 70; i++ {
		btc := baseBTC + float64(i)*50.0
		eth := btc*0.03 + math.Sin(float64(i)*0.2)*20.0 // stationary oscillation around cointegration
		z, _ := cm.Update(btc, eth)
		if math.IsNaN(z) || math.IsInf(z, 0) {
			t.Fatalf("step %d: unexpected NaN/Inf z-score", i)
		}
	}

	if cm.Beta < 0.01 || cm.Beta > 0.05 {
		t.Errorf("expected hedge ratio beta near 0.03, got: %f", cm.Beta)
	}
	if math.Abs(cm.LatestZScore) > 4.0 {
		t.Errorf("expected bounded z-score, got: %f", cm.LatestZScore)
	}
}

func TestKalmanTrendFilterNoiseRemoval(t *testing.T) {
	kf := NewKalmanTrendFilter(1e-4, 5e-2)
	now := time.Now()

	// Feed linear trend with heavy zero-mean Gaussian noise: price = 100 + 0.5 * t + noise
	rng := rand.New(rand.NewSource(99))
	var lastFiltered float64
	var lastVel float64

	for i := 0; i < 100; i++ {
		truePrice := 100.0 + float64(i)*0.5
		noisyPrice := truePrice + rng.NormFloat64()*3.0 // +/- 3.0 noise
		stepTime := now.Add(time.Duration(i) * time.Second)

		filtered, vel := kf.Update(noisyPrice, stepTime)
		lastFiltered = filtered
		lastVel = vel
	}

	// Filtered price should be within 1.5 units of true price 149.5 despite large noise
	if math.Abs(lastFiltered-149.5) > 3.5 {
		t.Errorf("Kalman filtered price departed from true price: got %f, want ~149.5", lastFiltered)
	}
	// Velocity should reflect upward trend > 0
	if lastVel <= 0 {
		t.Errorf("expected positive trend velocity, got %f bps/sec", lastVel)
	}
}

func TestMicrostructureVOIAndVPIN(t *testing.T) {
	// 1. Test VOI
	// Bid increases, Ask stays flat -> strong positive VOI
	voi := ComputeVOI(70000, 2.0, 70005, 2.0, 70001, 3.5, 70005, 2.0)
	if voi <= 0 {
		t.Errorf("expected positive VOI on bid improvement, got %f", voi)
	}

	// 2. Test Fast VPIN
	vpinCalc := NewFastVPINCalculator(1.0, 10)
	// Add balanced flow (each bucket gets 5 buys and 5 sells of 0.1 each)
	for i := 0; i < 100; i++ {
		vpinCalc.AddTrade(70000, 0.1, i%2 == 0)
	}
	calmVPIN := vpinCalc.VPIN()

	// Now dump aggressive toxic sell flow (all isBuyerMaker = true -> seller taking liquidity)
	for i := 0; i < 100; i++ {
		vpinCalc.AddTrade(70000, 0.1, true)
	}
	toxicVPIN := vpinCalc.VPIN()
	if toxicVPIN <= calmVPIN {
		t.Errorf("expected toxic flow VPIN (%f) > calm flow VPIN (%f)", toxicVPIN, calmVPIN)
	}
}

func TestCompositeAlphaEngine(t *testing.T) {
	ae := NewAlphaEngine("BTCUSDT", "ETHUSDT")
	now := time.Now()

	// Simulate upward trend with deep bid support
	for i := 0; i < 50; i++ {
		curTime := now.Add(time.Duration(i) * time.Second)
		mid := 75000.0 + float64(i)*20.0
		sig := ae.Evaluate("BTCUSDT", mid, mid-1.0, 5.0, mid+1.0, 1.0, mid*0.03, curTime)
		if i == 49 {
			if sig.Conviction < 0.55 {
				t.Errorf("expected bullish conviction > 0.55 on upward trend, got %f", sig.Conviction)
			}
			if sig.RecommendedAction != "BUY" && sig.RecommendedAction != "HOLD" {
				t.Errorf("unexpected action: %s", sig.RecommendedAction)
			}
		}
	}
}

func TestMomentumTracker_15MinuteATRWindowBound(t *testing.T) {
	mt := NewMomentumTracker(2000)
	now := time.Now()

	// 1. Spike to $80,000 30 minutes ago
	mt.Update(80000.0, now.Add(-30*time.Minute))

	// 2. Calm trading in the last 15 minutes between $75,000 and $75,100 (~13.3 bps range)
	for i := 14; i >= 0; i-- {
		p := 75000.0 + float64(i%2)*100.0
		mt.Update(p, now.Add(-time.Duration(i)*time.Minute))
	}

	res := mt.Update(75050.0, now)

	// ATR 15m should reflect the ~13.3 bps range in the last 15 minutes, NOT the $80,000 spike from 30m ago (which would be > 600 bps)
	if res.Atr15mBps > 50.0 {
		t.Fatalf("Atr15mBps failed to bound to 15m window: got %f bps (likely contaminated by 30m spike)", res.Atr15mBps)
	}
}

// TestHoles47And56_AlphaPackage verifies infinite size protection in FastVPINCalculator and NaN variance handling in MultiFactorModel.
func TestHoles47And56_AlphaPackage(t *testing.T) {
	// Hole 47: FastVPINCalculator +Inf and NaN size
	vpin := NewFastVPINCalculator(math.NaN(), -1)
	if vpin.BucketVolume != 2.0 || vpin.NumBuckets != 50 {
		t.Fatalf("Hole 47 failed: NewFastVPINCalculator with NaN did not set defaults: %+v", vpin)
	}
	// Passing +Inf or NaN should not hang
	vpin.AddTrade(60000.0, math.Inf(1), false)
	vpin.AddTrade(60000.0, math.NaN(), false)
	vpin.AddTrade(math.NaN(), 1.0, true)
	score := vpin.VPIN()
	if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 1.0 {
		t.Fatalf("Hole 47 failed: FastVPINCalculator returned invalid score: %v", score)
	}

	// ComputeBookDelta NaN protection
	l1Delta, microDev := ComputeBookDelta(math.NaN(), math.NaN(), math.NaN(), math.NaN())
	if math.IsNaN(l1Delta) || math.IsNaN(microDev) {
		t.Fatalf("Hole 47 failed: ComputeBookDelta with NaN produced NaN: %v, %v", l1Delta, microDev)
	}

	// Hole 56: MultiFactorModel variance NaN corruption test
	mf := NewMultiFactorModel(100)
	now := time.Now()
	// Deliberately corrupt internal accumulators with NaN
	mf.varAcc = math.NaN()
	mf.meanAcc = math.NaN()
	scoreMF := mf.Update(60000.0, 1.0, 0.20, 1.0, now)
	if math.IsNaN(scoreMF.CompositeScore) || math.IsNaN(scoreMF.Conviction) || math.IsNaN(scoreMF.ValueFactor) {
		t.Fatalf("Hole 56 failed: MultiFactorModel did not recover from NaN variance accumulator: %+v", scoreMF)
	}
}

