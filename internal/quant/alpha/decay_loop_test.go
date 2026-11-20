package alpha

import (
	"math"
	"testing"
)

func TestAlphaDecayHealthAndSelfImprovingLoop(t *testing.T) {
	n := 80
	qd := make([]float64, n)
	r3 := make([]float64, n)
	carry := make([]float64, n)
	dtb3 := make([]float64, n)
	for i := 0; i < n; i++ {
		if i < 40 {
			qd[i] = 0.004
			r3[i] = 0.003
		} else {
			// QD enters alpha decay (-0.005/day) while R3 stays healthy
			qd[i] = -0.005
			r3[i] = 0.0035
		}
		carry[i] = 0.00025
		dtb3[i] = 0.0412 / 365.0
	}

	hQD := ComputeAlphaHealth("qd", qd, 0.35)
	if hQD.Status != DecaySuspended || hQD.ThrottleMult != 0.35 || hQD.EWMA20dNetBps >= 0 {
		t.Fatalf("expected decaying QD sleeve to be suspended at 0.35x, got %+v", hQD)
	}
	hR3 := ComputeAlphaHealth("r3", r3, 0.35)
	if hR3.Status != DecayHealthy || hR3.ThrottleMult != 1.0 {
		t.Fatalf("expected healthy R3 sleeve at 1.0x, got %+v", hR3)
	}

	alloc := RunSelfImprovingLoop(map[string][]float64{
		"qd":    qd,
		"r3":    r3,
		"carry": carry,
		"dtb3":  dtb3,
	}, 0.15, 0.25, 0.70)

	if alloc.DaysProcessed != n || len(alloc.Sleeves) != 4 {
		t.Fatalf("unexpected allocation summary: %+v", alloc)
	}
	if alloc.Weights["r3"] <= alloc.Weights["qd"] {
		t.Fatalf("self-improving loop should shift weight from decaying qd (%v) to healthy r3 (%v)",
			alloc.Weights["qd"], alloc.Weights["r3"])
	}
	if alloc.Weights["carry"] < 0.25-1e-4 {
		t.Fatalf("carry floor violated: %+v", alloc.Weights)
	}
	if alloc.Weights["qd"]+alloc.Weights["r3"] > 0.70+1e-4 {
		t.Fatalf("maxTrend cap violated: %+v", alloc.Weights)
	}
	sum := 0.0
	for _, v := range alloc.Weights {
		sum += v
	}
	if math.Abs(sum-1.0) > 1e-3 {
		t.Fatalf("weights must sum to 1.0, got %v (%+v)", sum, alloc.Weights)
	}
}
