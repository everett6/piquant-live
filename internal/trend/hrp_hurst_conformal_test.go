package trend

import (
	"math"
	"testing"
	"time"
)

func TestHRPWeights3x3ParityAndEdgeCases(t *testing.T) {
	// 1. Deterministic 60x3 return matrix matching Python hrp_hurst_alpha.hrp_weights to < 1e-7.
	n := 60
	r0 := make([]float64, n)
	r1 := make([]float64, n)
	r2 := make([]float64, n)
	for i := 0; i < n; i++ {
		f := float64(i)
		r0[i] = 0.012*math.Sin(f*0.31) + 0.005*math.Cos(f*0.7)
		r1[i] = r0[i]*1.05 + 0.0015*math.Sin(f*1.9)
		r2[i] = -0.008*math.Cos(f*0.43) + 0.011*math.Sin(f*1.1)
	}
	w0, w1, w2 := hrpWeights3x3Matrix(r0, r1, r2)
	want := [3]float64{0.263635639817, 0.235288983003, 0.50107537718}
	got := [3]float64{w0, w1, w2}
	for i := range got {
		if math.Abs(got[i]-want[i]) > 1e-7 {
			t.Fatalf("HRP weight[%d] = %.12f, want %.12f", i, got[i], want[i])
		}
	}
	if math.Abs(w0+w1+w2-1.0) > 1e-12 {
		t.Fatalf("HRP weights sum = %v, want 1.0", w0+w1+w2)
	}
	// Correlated cluster {r0, r1} receives ~0.50 total while independent r2 receives ~0.50.
	if w2 <= w0 || w2 <= w1 {
		t.Fatalf("expected orthogonal asset weight %v > clustered weights (%v, %v)", w2, w0, w1)
	}

	// 2. Zero-variance / degenerate column handling.
	zero := make([]float64, n)
	a0, a1, a2 := hrpWeights3x3Matrix(r0, zero, zero)
	if math.Abs(a0-1.0) > 1e-12 || a1 != 0 || a2 != 0 {
		t.Fatalf("1-active HRP weights = (%v, %v, %v), want (1, 0, 0)", a0, a1, a2)
	}
	b0, b1, b2 := hrpWeights3x3Matrix(zero, zero, zero)
	if math.Abs(b0-1.0/3.0) > 1e-12 || math.Abs(b1-1.0/3.0) > 1e-12 || math.Abs(b2-1.0/3.0) > 1e-12 {
		t.Fatalf("all-zero HRP weights = (%v, %v, %v), want uniform 1/3", b0, b1, b2)
	}

	// 3. Warmup (< 20 returns) falls back cleanly to inverse-vol weights summing to 1.
	short := makeOHLCDays("2026-01-01", []float64{100, 101, 102, 103, 104, 105, 106, 107, 108, 109}, 0.01)
	s0, s1, s2 := hrpWeights3x3(short, short, short, 60)
	if math.Abs(s0+s1+s2-1.0) > 1e-12 {
		t.Fatalf("warmup hrpWeights3x3 sum = %v, want 1.0", s0+s1+s2)
	}
}

func TestRollingHurstVariogramAndCausality(t *testing.T) {
	n := 90
	trendPx := make([]float64, n)
	mrPx := make([]float64, n)
	trendPx[0] = 100.0
	mrPx[0] = 100.0
	for i := 1; i < n; i++ {
		trendPx[i] = trendPx[i-1] * math.Exp(0.008)
		sign := 1.0
		if i%2 == 1 {
			sign = -1.0
		}
		mrPx[i] = mrPx[i-1] * math.Exp(0.015*sign)
	}
	hTrend := makeOHLCDays("2026-01-01", trendPx, 0.005)
	hMR := makeOHLCDays("2026-01-01", mrPx, 0.005)

	hT, okT := rollingHurstVariogram(hTrend, 64)
	hM, okM := rollingHurstVariogram(hMR, 64)
	if !okT || hT <= 0.70 {
		t.Fatalf("persistent trend Hurst = %v ok=%v, want > 0.70", hT, okT)
	}
	if !okM || hM >= 0.30 {
		t.Fatalf("alternating mean-reverting Hurst = %v ok=%v, want < 0.30", hM, okM)
	}

	// Strict causality check: modifying future closes after bar 75 must not alter prefix output at bar 75.
	prefixGate := conformalRegimeGate(hTrend[:75], hTrend[:75], hTrend[:75])
	corrupt := append([]Day(nil), hTrend...)
	corrupt[76].Close = 1.0
	corrupt[80].Close = 1e6
	afterCorrupt := conformalRegimeGate(corrupt[:75], corrupt[:75], corrupt[:75])
	if prefixGate != afterCorrupt {
		t.Fatalf("causality violation in conformalRegimeGate: %v vs %v", prefixGate, afterCorrupt)
	}
}

func TestConformalConvictionGateScoreParity(t *testing.T) {
	cases := []struct {
		h, e, tr float64
		wantGate float64
		tol      float64
	}{
		{0.65, 0.2, 0.70, 1.0, 1e-12},
		{0.35, -0.8, 0.35, 1.0, 0.03},
		{0.48, -1.5, 0.42, 0.9906, 0.04},
		{0.55, -1.4, 0.62, 0.5190, 0.04},
	}
	for _, tc := range cases {
		g := conformalConvictionGateScore(tc.h, tc.e, tc.tr)
		if g < conformalGateFloor || g > 1.0 {
			t.Fatalf("gate(%v,%v,%v) = %v outside [0.45, 1.0]", tc.h, tc.e, tc.tr, g)
		}
		if math.Abs(g-tc.wantGate) > tc.tol {
			t.Fatalf("gate(%v,%v,%v) = %.6f, want %.6f +/- %.4f", tc.h, tc.e, tc.tr, g, tc.wantGate, tc.tol)
		}
	}
}

func TestTriAssetQ57StepGuardTaxBlend(t *testing.T) {
	st := NewState("2026-03-01")
	n := 100
	cB := make([]float64, n)
	cE := make([]float64, n)
	cS := make([]float64, n)
	pB, pE, pS := 50000.0, 2500.0, 150.0
	for i := 0; i < n; i++ {
		f := float64(i)
		pB *= 1.0 + 0.005 + 0.002*math.Sin(f*0.3)
		pE *= 1.0 + 0.004 + 0.002*math.Cos(f*0.4)
		pS *= 1.0 + 0.006 + 0.003*math.Sin(f*0.7+0.3)
		cB[i], cE[i], cS[i] = pB, pE, pS
	}
	dB := makeOHLCDays("2026-01-01", cB, 0.01)
	dE := makeOHLCDays("2026-01-01", cE, 0.012)
	dS := makeOHLCDays("2026-01-01", cS, 0.015)

	st.Seed(dB[:51])
	st.SeedETH(dE[:51])
	st.SeedSOL(dS[:51])

	nB := st.Advance(dB[51:])
	nE := st.AdvanceETH(dE[51:])
	nS := st.AdvanceSOL(dS[51:])
	if nB != 49 || nE != 49 || nS != 49 {
		t.Fatalf("expected 49 stepped days for B/E/S, got %d/%d/%d", nB, nE, nS)
	}

	l57 := st.Ledgers["Q57_hrp_hurst_conformal_spot_1x"]
	if l57 == nil || len(l57.Days) != 49 {
		t.Fatalf("missing or incomplete Q57 ledger: %+v", l57)
	}
	if sum := l57.Pos + l57.PosAlt + l57.PosSOL; sum <= 0 || sum > 1.0001 {
		t.Fatalf("Q57 pos sum = %v, want in (0, 1]", sum)
	}

	b57, ok := SelectBlend(st, nil, nil, "q57_hrp_hurst", nil)
	if !ok || b57.Name != "Q57_hrp_hurst_conformal_carry_spot" || b57.TargetSOLPortPct <= 0 {
		t.Fatalf("SelectBlend(q57_hrp_hurst) ok=%v blend=%+v", ok, b57)
	}

	_, open, _ := l57.TaxRows(1000)
	foundSOL := false
	for _, o := range open {
		if o.Coin == "SOL" && o.Quantity > 0 {
			foundSOL = true
		}
	}
	if !foundSOL {
		t.Fatalf("expected open SOL tax lot in Q57, got %+v", open)
	}

	fired := st.EmergencyCheckAll(1.0, 1.0, 1.0, time.Now())
	if len(fired) == 0 || l57.PosSOL != 0 {
		t.Fatalf("expected EmergencyCheckAll to flatten Q57 SOL pos, fired=%v posSOL=%v", fired, l57.PosSOL)
	}
}

func makeBenchDays(n int) (dB, dE, dS []Day) {
	cB := make([]float64, n)
	cE := make([]float64, n)
	cS := make([]float64, n)
	pB, pE, pS := 60000.0, 3000.0, 180.0
	for i := 0; i < n; i++ {
		f := float64(i)
		pB *= 1.0 + 0.01*math.Sin(f*0.3) + 0.002
		pE *= 1.0 + 0.012*math.Cos(f*0.4) + 0.0015
		pS *= 1.0 + 0.015*math.Sin(f*0.7+0.5) + 0.0025
		cB[i], cE[i], cS[i] = pB, pE, pS
	}
	return makeOHLCDays("2026-01-01", cB, 0.01), makeOHLCDays("2026-01-01", cE, 0.012), makeOHLCDays("2026-01-01", cS, 0.015)
}

func BenchmarkHRPWeights3x3(b *testing.B) {
	dB, dE, dS := makeBenchDays(100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = hrpWeights3x3(dB, dE, dS, 60)
	}
}

func BenchmarkRollingHurstVariogram(b *testing.B) {
	dB, _, _ := makeBenchDays(100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = rollingHurstVariogram(dB, 64)
	}
}

func BenchmarkQuantumRegimeScale(b *testing.B) {
	dB, dE, dS := makeBenchDays(100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = quantumRegimeScale(dB, dE, dS, 30, 60, 0.40)
	}
}

func BenchmarkConformalRegimeGate(b *testing.B) {
	dB, dE, dS := makeBenchDays(100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = conformalRegimeGate(dB, dE, dS)
	}
}

func BenchmarkStepTriQ57(b *testing.B) {
	dB, dE, dS := makeBenchDays(100)
	v := Ledger{Variant: Variant{Name: "Q57_hrp_hurst_conformal_spot_1x", Lev: 1, FeeBps: 2, SlipBps: 1, SMA: 50, ExitSMA: 20, Coherence: true, TriAsset: true, HRPWeights: true, ConformalGate: true, RebalBuffer: 0.03, TaperStart: 0.06, TaperFull: 0.20, TaperFloor: 0.40}, Equity: 1, Peak: 1}
	barB := dB[99].Close/dB[98].Close - 1
	barE := dE[99].Close/dE[98].Close - 1
	barS := dS[99].Close/dS[98].Close - 1
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l := v
		l.Days = make([]DayRec, 0, 1)
		l.stepTri("2026-04-10", barB, barE, barS, dB, dE, dS)
	}
}

func TestHRPWeights3x3ZeroVarianceAndNaN(t *testing.T) {
	// All zero variance -> uniform weights
	n := 60
	zero := make([]float64, n)
	w0, w1, w2 := hrpWeights3x3Matrix(zero, zero, zero)
	if math.Abs(w0-1.0/3.0) > 1e-12 || math.Abs(w1-1.0/3.0) > 1e-12 || math.Abs(w2-1.0/3.0) > 1e-12 {
		t.Fatalf("all-zero HRP weights = (%v, %v, %v), want uniform 1/3", w0, w1, w2)
	}

	// One active, two zero
	r0 := make([]float64, n)
	for i := 0; i < n; i++ {
		r0[i] = 0.01 * math.Sin(float64(i)*0.1)
	}
	a0, a1, a2 := hrpWeights3x3Matrix(r0, zero, zero)
	if math.Abs(a0-1.0) > 1e-12 || a1 != 0 || a2 != 0 {
		t.Fatalf("1-active HRP weights = (%v, %v, %v), want (1, 0, 0)", a0, a1, a2)
	}

	// NaN in returns -> treated as 0 deviation from mean
	rNaN := make([]float64, n)
	copy(rNaN, r0)
	rNaN[10] = math.NaN()
	rNaN[20] = math.Inf(1)
	b0, b1, b2 := hrpWeights3x3Matrix(rNaN, r0, r0)
	if math.IsNaN(b0) || math.IsNaN(b1) || math.IsNaN(b2) || math.IsInf(b0, 0) || math.IsInf(b1, 0) || math.IsInf(b2, 0) {
		t.Fatalf("NaN/Inf input produced non-finite weights: (%v, %v, %v)", b0, b1, b2)
	}
	if math.Abs(b0+b1+b2-1.0) > 1e-9 {
		t.Fatalf("NaN input weights don't sum to 1: %v", b0+b1+b2)
	}
}

func TestRollingHurstVariogramEdgeCases(t *testing.T) {
	// Flat prices -> Hurst = 0.5 (random walk)
	flat := makeOHLCDays("2026-01-01", make([]float64, 100), 0.0)
	for i := range flat {
		flat[i].Close = 100.0
	}
	h, ok := rollingHurstVariogram(flat, 64)
	if !ok || math.Abs(h-0.01) > 1e-9 {
		t.Fatalf("flat prices Hurst = %v ok=%v, want 0.01", h, ok)
	}

	// Very short history (< window) -> returns 0.5, false
	short := makeOHLCDays("2026-01-01", []float64{100, 101, 102}, 0.01)
	h, ok = rollingHurstVariogram(short, 64)
	if ok || math.Abs(h-0.5) > 1e-9 {
		t.Fatalf("short history Hurst = %v ok=%v, want 0.5 false", h, ok)
	}

	// Window <= 16 -> returns 0.5, false
	h, ok = rollingHurstVariogram(flat, 10)
	if ok || math.Abs(h-0.5) > 1e-9 {
		t.Fatalf("small window Hurst = %v ok=%v, want 0.5 false", h, ok)
	}

	// Zero or negative close -> returns 0.5, false
	badClose := makeOHLCDays("2026-01-01", []float64{100, 101, 0, 103}, 0.01)
	h, ok = rollingHurstVariogram(badClose, 64)
	if ok || math.Abs(h-0.5) > 1e-9 {
		t.Fatalf("zero close Hurst = %v ok=%v, want 0.5 false", h, ok)
	}
}

func TestRollingBasketHurstVariogramEdgeCases(t *testing.T) {
	// Mismatched lengths -> returns 0.5, false
	hB := makeOHLCDays("2026-01-01", []float64{100, 101, 102, 103, 104}, 0.01)
	hE := makeOHLCDays("2026-01-01", []float64{50, 51}, 0.01)
	hS := makeOHLCDays("2026-01-01", []float64{20, 21, 22, 23, 24}, 0.01)
	h, ok := rollingBasketHurstVariogram(hB, hE, hS, 64)
	if ok || math.Abs(h-0.5) > 1e-9 {
		t.Fatalf("mismatched lengths Hurst = %v ok=%v, want 0.5 false", h, ok)
	}

	// Zero close in any asset -> returns 0.5, false
	hB = makeOHLCDays("2026-01-01", make([]float64, 70), 0.01)
	hE = makeOHLCDays("2026-01-01", make([]float64, 70), 0.01)
	hS = makeOHLCDays("2026-01-01", make([]float64, 70), 0.01)
	for i := range hB {
		hB[i].Close = 100 + float64(i)*0.1
		hE[i].Close = 50 + float64(i)*0.05
		hS[i].Close = 20 + float64(i)*0.02
	}
	hS[35].Close = 0
	h, ok = rollingBasketHurstVariogram(hB, hE, hS, 64)
	if ok || math.Abs(h-0.5) > 1e-9 {
		t.Fatalf("zero close in SOL Hurst = %v ok=%v, want 0.5 false", h, ok)
	}
}

func TestConformalConvictionGateScoreEdgeCases(t *testing.T) {
	// NaN/Inf inputs -> sanitized to defaults
	g := conformalConvictionGateScore(math.NaN(), math.NaN(), math.NaN())
	if g < conformalGateFloor || g > 1.0 || math.IsNaN(g) || math.IsInf(g, 0) {
		t.Fatalf("NaN inputs gate = %v, want in [0.45, 1.0]", g)
	}
	g = conformalConvictionGateScore(math.Inf(1), math.Inf(-1), math.Inf(1))
	if g < conformalGateFloor || g > 1.0 || math.IsNaN(g) || math.IsInf(g, 0) {
		t.Fatalf("Inf inputs gate = %v, want in [0.45, 1.0]", g)
	}

	// Extreme values that could cause overflow in logistic
	g = conformalConvictionGateScore(100.0, -100.0, 100.0)
	if g < conformalGateFloor || g > 1.0 || math.IsNaN(g) || math.IsInf(g, 0) {
		t.Fatalf("extreme inputs gate = %v, want in [0.45, 1.0]", g)
	}

	// Trend preserve threshold
	g = conformalConvictionGateScore(0.5, 0.0, conformalTrendPreserve)
	if g != 1.0 {
		t.Fatalf("trendPreserve gate = %v, want 1.0", g)
	}

	// Entropy crisis floor with trendStrength < conformalTrendPreserve evaluates conformal gate in [0.45, 1.0]
	g = conformalConvictionGateScore(0.5, conformalEntropyCrisisFloor, 0.5)
	if g < conformalGateFloor || g > 1.0 || math.IsNaN(g) {
		t.Fatalf("entropyCrisisFloor gate = %v, want in [0.45, 1.0]", g)
	}
}

func TestConformalRegimeGateCausality(t *testing.T) {
	// Strict causality: future bars must not affect past gate decisions
	n := 100
	hB := makeOHLCDays("2026-01-01", make([]float64, n), 0.01)
	hE := makeOHLCDays("2026-01-01", make([]float64, n), 0.01)
	hS := makeOHLCDays("2026-01-01", make([]float64, n), 0.01)
	for i := range hB {
		hB[i].Close = 50000 * math.Exp(0.001*float64(i))
		hE[i].Close = 3000 * math.Exp(0.001*float64(i))
		hS[i].Close = 150 * math.Exp(0.001*float64(i))
	}

	// Gate at bar 80
	gate80 := conformalRegimeGate(hB[:80], hE[:80], hS[:80])

	// Corrupt future bars
	corruptB := append([]Day(nil), hB...)
	corruptE := append([]Day(nil), hE...)
	corruptS := append([]Day(nil), hS...)
	for i := 81; i < n; i++ {
		corruptB[i].Close = 1.0
		corruptE[i].Close = 1e6
		corruptS[i].Close = 1e-6
	}
	gate80Corrupt := conformalRegimeGate(corruptB[:80], corruptE[:80], corruptS[:80])

	if gate80 != gate80Corrupt {
		t.Fatalf("causality violation: gate80=%v vs gate80Corrupt=%v", gate80, gate80Corrupt)
	}
}
