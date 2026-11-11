package alpha

import (
	"math"
	"testing"
	"time"
)

func TestMultiFactorModelRegimes(t *testing.T) {
	mf := NewMultiFactorModel(100)
	now := time.Now()

	// 1. Initial warm up with stable price
	for i := 0; i < 30; i++ {
		mf.Update(60000.0, 1.0, 0.20, 1.5, now.Add(time.Duration(i)*time.Second))
	}

	// 2. Trend Regime: steady sharp increase in price
	var score MultiFactorScore
	for i := 1; i <= 30; i++ {
		p := 60000.0 + float64(i)*20.0 // +$600 move (~100 bps)
		score = mf.Update(p, 2.0, 0.25, 1.5, now.Add(time.Duration(30+i)*time.Second))
	}

	if score.Regime != RegimeTrending {
		t.Fatalf("expected RegimeTrending during continuous upward push, got %v", score.Regime)
	}
	if score.MomentumFactor <= 0.30 {
		t.Fatalf("expected strong positive momentum factor, got %f", score.MomentumFactor)
	}
	if score.Conviction <= 0.55 {
		t.Fatalf("expected high conviction P(Up) > 0.55, got %f", score.Conviction)
	}

	// 3. Toxic Regime: sudden toxic VPIN burst
	scoreToxic := mf.Update(60600.0, 10.0, 0.85, 8.0, now.Add(70*time.Second))
	if scoreToxic.Regime != RegimeHighVolToxic {
		t.Fatalf("expected RegimeHighVolToxic when VPIN=0.85, got %v", scoreToxic.Regime)
	}
	if scoreToxic.WeightVol != 0.50 {
		t.Fatalf("expected WeightVol to jump to 0.50 in toxic regime, got %f", scoreToxic.WeightVol)
	}
}

func TestMultiFactorModelMeanReversion(t *testing.T) {
	mf := NewMultiFactorModel(100)
	now := time.Now()

	// Establish high-volume VWAP anchor at 60,000
	for i := 0; i < 40; i++ {
		mf.Update(60000.0, 10.0, 0.20, 1.0, now.Add(time.Duration(i)*time.Second))
	}

	// Sharp quick dip on low volume (oversold vs VWAP)
	var scoreDip MultiFactorScore
	for i := 1; i <= 5; i++ {
		scoreDip = mf.Update(59700.0, 0.1, 0.30, 2.0, now.Add(time.Duration(40+i)*time.Second))
	}

	if scoreDip.ValueFactor <= 0.20 {
		t.Fatalf("expected positive value factor (mean-reversion long) when oversold, got %f", scoreDip.ValueFactor)
	}
}

func TestMultiFactorModelNaNSafety(t *testing.T) {
	mf := NewMultiFactorModel(100)
	now := time.Now()

	// Inject NaNs across all parameters
	s1 := mf.Update(math.NaN(), 1.0, 0.20, 1.5, now)
	if s1.Conviction != 0.50 || math.IsNaN(s1.CompositeScore) {
		t.Fatalf("expected safe conviction on NaN price, got %f", s1.Conviction)
	}

	s2 := mf.Update(60000.0, math.NaN(), math.NaN(), math.NaN(), now)
	if math.IsNaN(s2.CompositeScore) || math.IsNaN(s2.Conviction) {
		t.Fatalf("expected finite scores on NaN volume/vpin/spread, got %f, %f", s2.CompositeScore, s2.Conviction)
	}
}

func BenchmarkMultiFactorModelUpdate(b *testing.B) {
	mf := NewMultiFactorModel(100)
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mf.Update(60000.0+float64(i%100), 1.5, 0.35, 2.0, now)
	}
}
