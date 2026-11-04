package common

import (
	"math"
	"testing"
	"time"
)

func TestHawkesIntensityClustering(t *testing.T) {
	h := NewHawkesIntensity(10.0) // fast decay

	t0 := int64(1000000000)
	// Rapid burst of buy trades
	for i := 0; i < 5; i++ {
		h.Update(t0+int64(i*10000000), true, 1.5) // every 10ms
	}

	imb := h.Imbalance(t0 + 50000000)
	if imb <= 0.3 {
		t.Errorf("expected strong positive Hawkes buy imbalance after burst, got %v", imb)
	}

	// Long time passes, intensity should decay towards zero
	tLater := t0 + 5000000000 // 5 seconds later
	imbDecayed := h.Imbalance(tLater)
	if math.Abs(imbDecayed) > 0.15 {
		t.Errorf("expected decayed near-zero imbalance after 5s, got %v", imbDecayed)
	}
}

func TestVPINToxicity(t *testing.T) {
	vpin := NewVPINTracker(10.0, 5) // 10 vol per bucket, 5 buckets

	// Pure one-sided buying (severe toxicity / sweep)
	for i := 0; i < 5; i++ {
		vpin.AddTrade(true, 10.0)
	}

	tox := vpin.Toxicity()
	if tox < 0.95 {
		t.Errorf("expected near 1.0 toxicity for one-sided sweep, got %v", tox)
	}

	// Balanced trades (low toxicity)
	vpinBalanced := NewVPINTracker(10.0, 5)
	for i := 0; i < 5; i++ {
		vpinBalanced.AddTrade(true, 5.0)
		vpinBalanced.AddTrade(false, 5.0)
	}
	toxBalanced := vpinBalanced.Toxicity()
	if toxBalanced > 0.05 {
		t.Errorf("expected near 0.0 toxicity for balanced trades, got %v", toxBalanced)
	}
}

func TestStoikovMicropriceProperties(t *testing.T) {
	bestBid := int64(50000)
	bestAsk := int64(50010)
	mid := 50005.0

	// Equal quantities -> microprice = mid
	mpMid := StoikovMicroprice(bestBid, bestAsk, 100, 100)
	if math.Abs(mpMid-mid) > 0.01 {
		t.Errorf("expected microprice == mid for symmetric book, got %v", mpMid)
	}

	// Heavy bid quantity -> microprice strictly > mid
	mpHeavyBid := StoikovMicroprice(bestBid, bestAsk, 500, 50)
	if mpHeavyBid <= mid {
		t.Errorf("expected microprice > mid for heavy bid book, got %v", mpHeavyBid)
	}

	// Heavy ask quantity -> microprice strictly < mid
	mpHeavyAsk := StoikovMicroprice(bestBid, bestAsk, 50, 500)
	if mpHeavyAsk >= mid {
		t.Errorf("expected microprice < mid for heavy ask book, got %v", mpHeavyAsk)
	}
}

func TestTripleBarrierEvaluation(t *testing.T) {
	tb := TripleBarrierLabeler{
		PtBps:   10.0,
		SlBps:   5.0,
		Horizon: 30 * time.Second,
	}

	t0 := int64(1000000000)
	timestamps := []int64{t0 + 1e9, t0 + 2e9, t0 + 3e9}

	// Scenario 1: Hits Profit Target (+1)
	midsWin := []float64{100.0, 100.05, 100.12} // +12 bps
	resWin := tb.Evaluate(true, 100.0, midsWin, timestamps, t0)
	if resWin.Label != 1 || resWin.ReturnBps != 10.0 {
		t.Errorf("expected profit target hit (+1), got %+v", resWin)
	}

	// Scenario 2: Hits Stop Loss (-1)
	midsLoss := []float64{100.0, 99.96, 99.93} // -7 bps
	resLoss := tb.Evaluate(true, 100.0, midsLoss, timestamps, t0)
	if resLoss.Label != -1 || resLoss.ReturnBps != -5.0 {
		t.Errorf("expected stop loss hit (-1), got %+v", resLoss)
	}
}

func TestMetaLabelingFilter(t *testing.T) {
	base := NewRidge(1e-3)
	meta := NewMetaLabelingFilter(base, 10.0, 0.55)

	// Create samples where informative feature X0 drives direction and X1 drives trade magnitude / conviction
	samples := make([]Sample, 120)
	for i := 0; i < 120; i++ {
		x0 := float64(i%6) - 2.5     // direction signal
		x1 := float64((i*3)%10) * 0.5 // conviction signal (0.0 to 4.5)
		y := x0 * (x1 * 3.0)         // magnitude proportional to x1
		samples[i] = Sample{
			X: [FeatureCount]float64{x0, x1},
			Y: y,
		}
	}

	if err := meta.Fit(samples, []int{0, 1}); err != nil {
		t.Fatalf("MetaLabeling fit failed: %v", err)
	}

	// Strong conviction feature x1=4.0 with positive direction x0=2.0 -> clears hurdle
	var xStrong [FeatureCount]float64
	xStrong[0] = 2.0
	xStrong[1] = 4.0
	predStrong, probStrong, okStrong := meta.PredictWithMeta(&xStrong)
	if !okStrong || predStrong <= 0 || probStrong < 0.55 {
		t.Errorf("expected meta-labeler to pass high-conviction trade, got pred=%v prob=%v ok=%v", predStrong, probStrong, okStrong)
	}

	// Weak conviction feature x1=0.1 (low magnitude, eaten by fees) -> filtered out
	var xWeak [FeatureCount]float64
	xWeak[0] = 0.5
	xWeak[1] = 0.1
	predWeak, _, okWeak := meta.PredictWithMeta(&xWeak)
	if okWeak && predWeak != 0 {
		t.Errorf("expected meta-labeler to filter out noisy sub-fee trade, got pred=%v ok=%v", predWeak, okWeak)
	}
}
