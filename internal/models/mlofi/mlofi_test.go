package mlofi

import (
	"math"
	"math/rand/v2"
	"testing"

	"piquant-live/internal/models/common"
)

// featureIndex maps a features.Names entry to a test-local constant so the
// assertions below read like the package doc rather than magic numbers.
const (
	fImbL1      = 0
	fImbL5      = 1
	fImbL20     = 2
	fMicroDev   = 3
	fSpreadBps  = 4
	fOFI1s      = 5
	fOFI5s      = 6
	fOFI30s     = 7
	fMLOFI1s    = 8
	fMLOFI5s    = 9
	fVol30s     = 10
	fRet1s      = 11
	fTradeImb5s = 13
)

func setOf(t *testing.T, want []int, s FeatureSet) {
	t.Helper()
	got, err := Features(s)
	if err != nil {
		t.Fatalf("Features(%q): %v", s, err)
	}
	if len(got) != len(want) {
		t.Fatalf("%s features = %v, want %v", s, got, want)
	}
	seen := map[int]bool{}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s features = %v, want %v", s, got, want)
		}
		if seen[got[i]] {
			t.Fatalf("%s repeats feature %d", s, got[i])
		}
		seen[got[i]] = true
		if !common.ValidFeature(got[i]) {
			t.Fatalf("%s uses out-of-range feature %d", s, got[i])
		}
	}
}

// The documented feature sets are a contract with the report: changing them
// silently would make two runs' "L20" incomparable.
func TestFeatureSetsMatchDocumentation(t *testing.T) {
	l1 := []int{fImbL1, fMicroDev, fSpreadBps, fOFI1s, fOFI5s}
	setOf(t, l1, SetL1)
	setOf(t, append(append([]int{}, l1...), fImbL5), SetL5)
	setOf(t, append(append([]int{}, l1...), fImbL5, fImbL20), SetL20)
	setOf(t, append(append([]int{}, l1...), fImbL5, fImbL20, fOFI30s, fMLOFI1s, fMLOFI5s), SetMulti)

	if len(FeatureSets) != 4 || FeatureSets[0] != SetL1 || FeatureSets[3] != SetMulti {
		t.Fatalf("FeatureSets = %v", FeatureSets)
	}
	if _, err := Features("L2"); err == nil {
		t.Fatal("unknown feature set accepted")
	}
	if _, err := New(Params{Set: "L2"}); err == nil {
		t.Fatal("New accepted an unknown feature set")
	}
}

// synth builds rows whose bps target is a known function of two features plus
// noise, so a correct fit can be checked against the generating coefficients.
func synth(n int, seed uint64) []common.Sample {
	rng := rand.New(rand.NewPCG(seed, 11))
	out := make([]common.Sample, n)
	for i := range out {
		var x [common.FeatureCount]float64
		x[fOFI1s] = rng.Float64()*2 - 1
		x[fMicroDev] = (rng.Float64()*2 - 1) * 0.5
		x[fSpreadBps] = 1 + rng.Float64()
		// Uninformative features must end up with a small weight; if the fit
		// leaks one into another this check catches it.
		x[fVol30s] = rng.Float64() * 20
		x[fRet1s] = rng.Float64()*2 - 1
		x[fTradeImb5s] = rng.Float64()*2 - 1
		y := 4*x[fOFI1s] + 2*x[fMicroDev] + 0.05*(rng.Float64()*2-1)
		out[i] = common.Sample{X: x, Y: y}
	}
	return out
}

func allHorizons(s []common.Sample) [][]common.Sample {
	out := make([][]common.Sample, len(common.Horizons))
	for i := range out {
		out[i] = s
	}
	return out
}

func TestFitAndForecastRecoversSignal(t *testing.T) {
	train := synth(5000, 1)
	m, err := New(Params{Set: SetL1, Lam: 1e-6, MinSamples: 100})
	if err != nil {
		t.Fatal(err)
	}
	if m.Fitted() {
		t.Fatal("an unfitted model reports Fitted")
	}
	if err := m.Fit(allHorizons(train)); err != nil {
		t.Fatal(err)
	}
	if !m.Fitted() {
		t.Fatal("model is not fitted after Fit")
	}
	for _, h := range common.Horizons {
		if c := m.Coefs(h); c == nil {
			t.Fatalf("no coefficients for %v", h)
		}
	}

	// A positive OFI with a positive microprice deviation must forecast up.
	var up [common.FeatureCount]float32
	up[fOFI1s], up[fMicroDev] = 0.5, 0.25
	var down [common.FeatureCount]float32
	down[fOFI1s], down[fMicroDev] = -0.5, -0.25
	fUp := m.Forecast(up[:], 7, 1_000, common.H30s)
	fDown := m.Forecast(down[:], 7, 1_000, common.H30s)
	if !fUp.Valid || fUp.Reason != "" {
		t.Fatalf("valid forecast rejected: %+v", fUp)
	}
	if fUp.Value <= 0 || fDown.Value >= 0 {
		t.Fatalf("forecasts %v and %v have the wrong sign", fUp.Value, fDown.Value)
	}
	if want := 4*0.5 + 2*0.25; math.Abs(fUp.Value-want) > 0.1 {
		t.Fatalf("forecast = %v, want ~%v", fUp.Value, want)
	}
	if fUp.Model != "MLOFI_Microprice/L1" || fUp.Horizon != common.H30s || fUp.Symbol != 7 || fUp.At != 1_000 {
		t.Fatalf("provenance not carried: %+v", fUp)
	}
	if fUp.Confidence <= 0 || fUp.Confidence > 1 {
		t.Fatalf("confidence %v out of range", fUp.Confidence)
	}
	// A larger predicted move must not be reported with more confidence.
	if m.Forecast([]float32{0, 0, 0, 0, 0, 0.05, 0}, 7, 1_000, common.H30s).Confidence < fUp.Confidence {
		t.Fatal("confidence is not monotone in the prediction size")
	}
	if m.Cost() <= 0 {
		t.Fatal("inference cost was not accounted")
	}
}

// Each horizon has its own fit; a model trained on one horizon must not
// publish a forecast for another.
func TestHorizonsAreIndependent(t *testing.T) {
	base := synth(1000, 2)
	byHorizon := [][]common.Sample{base, base[:0], nil}
	m, err := New(Params{Set: SetL1, Lam: 1e-4, MinSamples: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Fit(byHorizon); err != nil {
		t.Fatal(err)
	}
	if m.Fitted() {
		t.Fatal("30s-only fit reports every horizon fitted")
	}
	if f := m.Forecast(make([]float32, common.FeatureCount), 0, 0, common.H30s); !f.Valid {
		t.Fatalf("30s forecast invalid: %s", f.Reason)
	}
	for _, h := range []common.Horizon{common.H1m, common.H5m} {
		f := m.Forecast(make([]float32, common.FeatureCount), 0, 0, h)
		if f.Valid || f.Reason == "" {
			t.Fatalf("%v forecast = %+v, want invalid with a reason", h, f)
		}
		if m.Coefs(h) != nil {
			t.Fatalf("%v has coefficients without a fit", h)
		}
	}

	// Different data per horizon gives different weights: 30s leans on OFI,
	// 1m leans on micro_dev.
	ys := make([][]common.Sample, len(common.Horizons))
	a := synth(2000, 3)
	b := make([]common.Sample, len(a))
	for i := range a {
		b[i] = a[i]
		b[i].Y = 3 * a[i].X[fMicroDev]
	}
	ys[common.H30s], ys[common.H1m] = a, b
	m2, _ := New(Params{Set: SetL1, Lam: 1e-6, MinSamples: 100})
	if err := m2.Fit(ys); err != nil {
		t.Fatal(err)
	}
	c30, c1m := m2.Coefs(common.H30s), m2.Coefs(common.H1m)
	if math.Abs(c30[fOFI1s]) <= math.Abs(c30[fMicroDev]) {
		t.Fatalf("30s weights %v do not reflect the generating model", c30)
	}
	if math.Abs(c1m[fMicroDev]) <= math.Abs(c1m[fOFI1s]) {
		t.Fatalf("1m weights %v do not reflect the generating model", c1m)
	}
}

func TestFitRejectsWrongHorizonCount(t *testing.T) {
	m, _ := New(Params{Set: SetL1})
	if err := m.Fit([][]common.Sample{{}}); err == nil {
		t.Fatal("Fit accepted one horizon of samples")
	}
	if err := m.Fit(nil); err == nil {
		t.Fatal("Fit accepted no samples")
	}
}

// L20 must be able to predict with the same interface as L1 and must not read
// features outside its set: zeroing every unused feature must not change the
// forecast.
func TestFeatureSetsAreIsolated(t *testing.T) {
	train := synth(3000, 4)
	for _, s := range FeatureSets {
		m, err := New(Params{Set: s, Lam: 1e-3, MinSamples: 100})
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Fit(allHorizons(train)); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		idx := map[int]bool{}
		for _, j := range m.Features() {
			idx[j] = true
		}
		want, err := Features(s)
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range want {
			if !idx[j] {
				t.Fatalf("%s does not advertise feature %d", s, j)
			}
		}
		var a, b [common.FeatureCount]float32
		for j := 0; j < common.FeatureCount; j++ {
			a[j] = float32(j%5)/5 - 0.4
			b[j] = a[j]
			if !idx[j] {
				b[j] = 1e6 // any leak from an unused feature would show up here
			}
		}
		fa := m.Forecast(a[:], 1, 2, common.H1m)
		fb := m.Forecast(b[:], 1, 2, common.H1m)
		if !fa.Valid || !fb.Valid {
			t.Fatalf("%s forecast invalid: %+v / %+v", s, fa, fb)
		}
		if fa.Value != fb.Value {
			t.Fatalf("%s reads features outside its set: %v vs %v", s, fa.Value, fb.Value)
		}
	}
}

func TestForecastRejectsNonFinite(t *testing.T) {
	m, _ := New(Params{Set: SetL1, Lam: 1e-6, MinSamples: 10})
	if err := m.Fit(allHorizons(synth(200, 5))); err != nil {
		t.Fatal(err)
	}
	f := make([]float32, common.FeatureCount)
	f[fOFI1s] = float32(math.Inf(1))
	got := m.Forecast(f, 0, 0, common.H30s)
	if got.Valid || got.Reason == "" {
		t.Fatalf("non-finite input forecast = %+v", got)
	}
}

func BenchmarkForecast(b *testing.B) {
	m, _ := New(Params{Set: SetMulti, Lam: 1e-3, MinSamples: 100})
	if err := m.Fit(allHorizons(synth(2000, 6))); err != nil {
		b.Fatal(err)
	}
	f := make([]float32, common.FeatureCount)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := m.Forecast(f, 1, int64(i), common.H30s); !got.Valid {
			b.Fatal(got.Reason)
		}
	}
}
