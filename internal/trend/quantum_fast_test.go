package trend

import (
	"math"
	"testing"
	"time"
)

func makeOHLCDays(start string, closes []float64, halfRange float64) []Day {
	t0, _ := time.Parse("2006-01-02", start)
	out := make([]Day, len(closes))
	for i, c := range closes {
		out[i] = Day{
			Date:  t0.AddDate(0, 0, i).Format("2006-01-02"),
			Open:  c,
			High:  c * (1.0 + halfRange),
			Low:   c * (1.0 - halfRange),
			Close: c,
		}
	}
	return out
}

func TestGarmanKlassVolAndCrashBrake(t *testing.T) {
	// Close-only fallback should still yield positive vol when close moves.
	closesOnly := make([]Day, 12)
	for i := range closesOnly {
		closesOnly[i] = Day{Date: "2026-01-01", Close: 100.0 + float64(i%3)*2.0}
	}
	vFallback, ok := garmanKlassVol(closesOnly, 5)
	if !ok || vFallback <= 0 {
		t.Fatalf("expected close-only fallback vol > 0, got %v ok=%v", vFallback, ok)
	}

	// Calm 30-day regime with 0.5% half-range -> crash brake stays 1.0
	calmCloses := make([]float64, 40)
	for i := range calmCloses {
		calmCloses[i] = 100.0 + 0.1*float64(i)
	}
	calm := makeOHLCDays("2026-01-01", calmCloses, 0.005)
	if b := rangeVolCrashBrake(calm, 5, 30, 2.20, 0.55); b != 1.0 {
		t.Fatalf("calm regime brake = %v, want 1.0", b)
	}

	// Sudden 5-day high-low range expansion (8% half-range) triggers crash brake < 1.0
	spike := make([]Day, len(calm))
	copy(spike, calm)
	for i := len(spike) - 5; i < len(spike); i++ {
		spike[i].High = spike[i].Close * 1.08
		spike[i].Low = spike[i].Close * 0.92
	}
	if b := rangeVolCrashBrake(spike, 5, 30, 2.20, 0.55); b >= 1.0 || b < 0.55 {
		t.Fatalf("spike regime brake = %v, want in [0.55, 1.0)", b)
	}
}

func TestQuantumSpectralEntropyAndCausality(t *testing.T) {
	n := 100
	rB := make([]float64, n)
	rE := make([]float64, n)
	rS := make([]float64, n)

	// Orthogonal/uncorrelated returns -> entropy near 1.0
	pB, pE, pS := 100.0, 50.0, 20.0
	for i := 0; i < n; i++ {
		f := float64(i)
		pB *= 1.0 + 0.02*math.Sin(f*0.7)
		pE *= 1.0 + 0.02*math.Cos(f*1.1)
		pS *= 1.0 + 0.02*math.Sin(f*1.9+0.5)
		rB[i], rE[i], rS[i] = pB, pE, pS
	}
	hB := makeOHLCDays("2026-01-01", rB, 0.01)
	hE := makeOHLCDays("2026-01-01", rE, 0.01)
	hS := makeOHLCDays("2026-01-01", rS, 0.01)

	sOrtho, ok := quantumSpectralEntropy3x3(hB, hE, hS, 30)
	if !ok || sOrtho < 0.70 {
		t.Fatalf("orthogonal entropy = %v ok=%v, want >= 0.70", sOrtho, ok)
	}

	// Perfectly collinear crash returns -> entropy collapses near 0.0 & regime scale drops toward floor
	cB, cE, cS := 100.0, 50.0, 20.0
	for i := 0; i < n; i++ {
		if i < 60 {
			f := float64(i)
			cB *= 1.0 + 0.015*math.Sin(f*0.7)
			cE *= 1.0 + 0.015*math.Cos(f*1.1)
			cS *= 1.0 + 0.015*math.Sin(f*1.9+0.5)
		} else {
			step := -0.02 - 0.005*float64(i%4)
			cB *= 1.0 + step
			cE *= 1.0 + step*1.001
			cS *= 1.0 + step*0.999
		}
		rB[i], rE[i], rS[i] = cB, cE, cS
	}
	kB := makeOHLCDays("2026-01-01", rB, 0.01)
	kE := makeOHLCDays("2026-01-01", rE, 0.01)
	kS := makeOHLCDays("2026-01-01", rS, 0.01)

	sCrisis, ok := quantumSpectralEntropy3x3(kB, kE, kS, 30)
	if !ok || sCrisis > 0.25 {
		t.Fatalf("collinear crisis entropy = %v ok=%v, want <= 0.25", sCrisis, ok)
	}
	scaleBefore := quantumRegimeScale(kB[:n-10], kE[:n-10], kS[:n-10], 30, 60, 0.40)
	// Corrupting future bars must leave prefix scale identical (strict causality).
	kBFuture := append(append([]Day(nil), kB[:n-10]...), makeOHLCDays("2026-04-10", []float64{1, 1000, 2}, 0.1)...)
	scaleAfterFutureCorrupt := quantumRegimeScale(kBFuture[:n-10], kE[:n-10], kS[:n-10], 30, 60, 0.40)
	if scaleBefore != scaleAfterFutureCorrupt {
		t.Fatalf("future leakage in quantumRegimeScale: %v vs %v", scaleBefore, scaleAfterFutureCorrupt)
	}
	scaleCrisis := quantumRegimeScale(kB, kE, kS, 30, 60, 0.40)
	if scaleCrisis >= 0.80 || scaleCrisis < 0.40 {
		t.Fatalf("crisis quantumRegimeScale = %v, want in [0.40, 0.80)", scaleCrisis)
	}
}

func TestTriAssetQ51AndQ56StepGuardTaxBlend(t *testing.T) {
	st := NewState("2026-03-01")
	n := 80
	cB := make([]float64, n)
	cE := make([]float64, n)
	cS := make([]float64, n)
	for i := 0; i < n; i++ {
		cB[i] = 50000.0 * math.Pow(1.005, float64(i))
		cE[i] = 2500.0 * math.Pow(1.004, float64(i))
		cS[i] = 150.0 * math.Pow(1.006, float64(i))
	}
	dB := makeOHLCDays("2026-01-10", cB, 0.01)
	dE := makeOHLCDays("2026-01-10", cE, 0.012)
	dS := makeOHLCDays("2026-01-10", cS, 0.015)

	st.Seed(dB[:51])
	st.SeedETH(dE[:51])
	st.SeedSOL(dS[:51])

	nB := st.Advance(dB[51:])
	nE := st.AdvanceETH(dE[51:])
	nS := st.AdvanceSOL(dS[51:])
	if nB != 29 || nE != 29 || nS != 29 {
		t.Fatalf("expected 29 stepped days for B/E/S, got %d/%d/%d", nB, nE, nS)
	}
	// Idempotent repeat must process 0 new days
	if again := st.AdvanceSOL(dS[51:]); again != 0 {
		t.Fatalf("AdvanceSOL not idempotent: %d", again)
	}

	l51 := st.Ledgers["Q51_btc_eth_sol_volparity_spot"]
	l56 := st.Ledgers["Q56_quantum_fast_spot_1x"]
	if len(l51.Days) != 29 || len(l56.Days) != 29 {
		t.Fatalf("unexpected ledger lengths: Q51=%d Q56=%d", len(l51.Days), len(l56.Days))
	}
	if sum := l51.Pos + l51.PosAlt + l51.PosSOL; sum <= 0 || sum > 1.0001 {
		t.Fatalf("Q51 pos sum = %v, want in (0, 1]", sum)
	}
	if sum := l56.Pos + l56.PosAlt + l56.PosSOL; sum <= 0 || sum > 1.0001 {
		t.Fatalf("Q56 pos sum = %v, want in (0, 1]", sum)
	}

	// Verify blend selection
	b51, ok := SelectBlend(st, nil, nil, "q51_volparity", nil)
	if !ok || b51.TargetSOLPortPct <= 0 {
		t.Fatalf("SelectBlend(q51_volparity) ok=%v blend=%+v", ok, b51)
	}
	b56, ok := SelectBlend(st, nil, nil, "q56_quantum_fast", nil)
	if !ok || b56.TargetSOLPortPct <= 0 {
		t.Fatalf("SelectBlend(q56_quantum_fast) ok=%v blend=%+v", ok, b56)
	}

	// Tax rows include SOL for tri-asset ledgers
	_, open, _ := l56.TaxRows(1000)
	foundSOL := false
	for _, o := range open {
		if o.Coin == "SOL" && o.Quantity > 0 {
			foundSOL = true
		}
	}
	if !foundSOL {
		t.Fatalf("expected open SOL tax lot in Q56, got %+v", open)
	}

	// EmergencyCheckAll flattens PosSOL on 40% crash
	fired := st.EmergencyCheckAll(1.0, 1.0, 1.0, time.Now())
	if len(fired) == 0 || l56.PosSOL != 0 {
		t.Fatalf("expected EmergencyCheckAll to flatten Q56 SOL pos, fired=%v posSOL=%v", fired, l56.PosSOL)
	}
}
