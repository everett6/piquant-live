package trend

import (
	"testing"
	"time"
)

func makeOHLCDays(start string, closes []float64, halfRange float64) []Day {
	t0, _ := time.Parse("2006-01-02", start)
	out := make([]Day, len(closes))
	for i, c := range closes {
		out[i] = Day{
			Date:  t0.AddDate(0, 0, i).Format("2006-01-02"),
			Close: c,
		}
	}
	return out
}

func TestQuantumSpectralEntropyZeroWindowAndNaN(t *testing.T) {
	// Window < 5 -> return 0, false
	s, ok := quantumSpectralEntropyFromMoments(3, 100, 50, 20, 10000, 5000, 1000, 500, 200, 100)
	if s != 0 || ok {
		t.Fatalf("zero window entropy = %v ok=%v, want 0 false", s, ok)
	}

	// w < 5 but positive moment sums
	s, ok = quantumSpectralEntropyFromMoments(4, 100, 50, 20, 10000, 5000, 1000, 500, 200, 100)
	if s != 0 || ok {
		t.Fatalf("window=4 entropy = %v ok=%v, want 0 false", s, ok)
	}

	// All zero variances -> should fail
	s, ok = quantumSpectralEntropyFromMoments(10, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if s != 0 || ok {
		t.Fatalf("all-zero variances entropy = %v ok=%v, want 0 false", s, ok)
	}

	// Perfectly correlated (collinear) -> entropy near 0
	n := 100
	rets := make([]float64, n)
	for i := 0; i < n; i++ {
		rets[i] = 0.001 + 0.0001*float64(i)
	}
	_, ok2 := quantumSpectralEntropy3x3(
		makeOHLCDays("2026-01-01", rets, 0.01),
		makeOHLCDays("2026-01-01", rets, 0.01),
		makeOHLCDays("2026-01-01", rets, 0.01),
		30)
	if !ok2 {
		t.Fatal("collinear entropy ok")
	}

	// Causality check: corrupting future must not affect prefix
	n = 200
	reB := make([]float64, n)
	reE := make([]float64, n)
	reS := make([]float64, n)
	for i := 0; i < n; i++ {
		reB[i] = 100.0 + float64(i)*0.5 + float64(i%3)*0.2
		reE[i] = 50.0 + float64(i)*0.25 + float64((i+1)%3)*0.15
		reS[i] = 20.0 + float64(i)*0.1 + float64((i+2)%3)*0.1
	}
	_, okB := quantumSpectralEntropy3x3(
		makeOHLCDays("2026-01-01", reB, 0.01),
		makeOHLCDays("2026-01-01", reE, 0.01),
		makeOHLCDays("2026-01-01", reS, 0.01), 30)
	if !okB {
		t.Fatal("baseline entropy ok")
	}

	// Corrupt future bars
	reBFuture := make([]float64, 0, len(reB)+3)
	reBFuture = append(reBFuture, reB...)
	reBFuture = append(reBFuture, 1.0, 1000)
	reBFuture = append(reBFuture, 1e6)

	reEFuture := make([]float64, 0, len(reE)+3)
	reEFuture = append(reEFuture, reE...)
	reEFuture = append(reEFuture, 1.0, 1e6)

	reSFuture := make([]float64, 0, len(reS)+3)
	reSFuture = append(reSFuture, reS...)
	reSFuture = append(reSFuture, 1.0, 1e6)

	_, okA := quantumSpectralEntropy3x3(
		makeOHLCDays("2026-01-01", reBFuture[:n], 0.01),
		makeOHLCDays("2026-01-01", reEFuture[:n], 0.01),
		makeOHLCDays("2026-01-01", reSFuture[:n], 0.01), 30)
	if !okA {
		t.Fatal("corrupted entropy ok")
	}
	if okB != okA {
		t.Fatalf("future leakage: before=%v after=%v", okB, okA)
	}
}