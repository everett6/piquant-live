package alpha

import (
	"math"
	"testing"
)

// Values produced by research/quant_lab/decay_cusum.py on the same deterministic series.
func TestCusumDecayMatchesPython(t *testing.T) {
	ref := make([]float64, 400)
	for i := range ref {
		x := float64(i)
		ref[i] = 0.002 + 0.01*math.Sin(x*0.37) + 0.003*math.Cos(x*1.3)
	}
	live := make([]float64, 120)
	for j := range live {
		live[j] = -0.003 + 0.01*math.Sin(float64(j)*0.71)
	}
	st, ok := CusumDecay(ref, live)
	if !ok {
		t.Fatal("expected a usable reference")
	}
	want := map[string][2]float64{
		"mu0":   {st.Mu0, 0.0010662295382017815},
		"sigma": {st.Sigma, 0.007381857182862737},
		"k":     {st.K, 0.07221959947132776},
		"h":     {st.H, 20.59617974482092},
		"s":     {st.S, 55.115380834024435},
	}
	for name, v := range want {
		if math.Abs(v[0]-v[1]) > 1e-9*math.Max(1, math.Abs(v[1])) {
			t.Errorf("%s = %.15g, python %.15g", name, v[0], v[1])
		}
	}
	if !st.Alarm || st.AlarmDay != 43 || st.RefDays != 400 {
		t.Errorf("alarm=%v day=%d refDays=%d, want true 43 400", st.Alarm, st.AlarmDay, st.RefDays)
	}
}

func TestCusumDecayUnusableReference(t *testing.T) {
	if _, ok := CusumDecay(make([]float64, 179), nil); ok {
		t.Error("179 days must be too short")
	}
	if _, ok := CusumDecay(make([]float64, 500), nil); ok {
		t.Error("a flat reference has no scale")
	}
	nan := make([]float64, 300)
	for i := range nan {
		nan[i] = math.NaN()
	}
	if _, ok := CusumDecay(nan, []float64{0.01}); ok {
		t.Error("NaN reference must be unusable")
	}
}

func TestCusumThresholdHitsARL0(t *testing.T) {
	for _, k := range []float64{0.01, 0.04, 0.2} {
		h := cusumThreshold(k, CusumARL0Days)
		if got := siegmundARL0(k, h); math.Abs(got-CusumARL0Days)/CusumARL0Days > 1e-6 {
			t.Errorf("k=%v: ARL0 %v at h=%v", k, got, h)
		}
	}
}
