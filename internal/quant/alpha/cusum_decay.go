package alpha

import (
	"math"
	"sort"
)

// PREREG60 constants for the one-sided CUSUM decay detector. Must match research/quant_lab/decay_cusum.py.
const (
	CusumRefDays    = 1095
	CusumMinRefDays = 180
	CusumHaircut    = 0.5
	CusumARL0Days   = 1825.0
	cusumZClip      = 4.0
	cusumKFloor     = 0.01
	cusumWinsorQ    = 0.005
)

// CusumState is the detector state after the full live series.
type CusumState struct {
	S        float64 `json:"s"`
	H        float64 `json:"h"`
	K        float64 `json:"k"`
	Mu0      float64 `json:"mu0"`
	Sigma    float64 `json:"sigma"`
	RefDays  int     `json:"ref_days"`
	Alarm    bool    `json:"alarm"`
	AlarmDay int     `json:"alarm_day"` // 1-based live day of the first alarm, 0 if none
}

// siegmundARL0 approximates the in-control average run length of a CUSUM with drift k and threshold h.
func siegmundARL0(k, h float64) float64 {
	x := 2.0 * k * (h + 1.166)
	if x > 700 {
		return math.Inf(1)
	}
	return (math.Exp(x) - 1.0 - x) / (2.0 * k * k)
}

// cusumThreshold is the smallest h in [0, 1e4] with siegmundARL0(k, h) >= arl0, found by bisection.
func cusumThreshold(k, arl0 float64) float64 {
	lo, hi := 0.0, 1e4
	if siegmundARL0(k, lo) >= arl0 {
		return lo
	}
	for i := 0; i < 200; i++ {
		mid := 0.5 * (lo + hi)
		if siegmundARL0(k, mid) >= arl0 {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi
}

// quantileLinear matches numpy.quantile's default (linear) method on sorted data.
func quantileLinear(sorted []float64, q float64) float64 {
	pos := q * float64(len(sorted)-1)
	i := int(math.Floor(pos))
	if i+1 >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	f := pos - float64(i)
	return sorted[i] + f*(sorted[i+1]-sorted[i])
}

func finite(xs []float64) []float64 {
	out := make([]float64, 0, len(xs))
	for _, x := range xs {
		if !math.IsNaN(x) && !math.IsInf(x, 0) {
			out = append(out, x)
		}
	}
	return out
}

// CusumDecay tests live daily net returns against the pre-admission reference (PREREG60). ok is false when
// the reference has fewer than CusumMinRefDays finite days or no scale.
func CusumDecay(ref, live []float64) (CusumState, bool) {
	r := finite(ref)
	if len(r) > CusumRefDays {
		r = r[len(r)-CusumRefDays:]
	}
	if len(r) < CusumMinRefDays {
		return CusumState{}, false
	}
	sorted := append([]float64(nil), r...)
	sort.Float64s(sorted)
	lo, hi := quantileLinear(sorted, cusumWinsorQ), quantileLinear(sorted, 1-cusumWinsorQ)
	var sum, wsum float64
	for _, x := range r {
		sum += x
		wsum += math.Min(math.Max(x, lo), hi)
	}
	wmean := wsum / float64(len(r))
	var ss float64
	for _, x := range r {
		d := math.Min(math.Max(x, lo), hi) - wmean
		ss += d * d
	}
	sigma := math.Sqrt(ss / float64(len(r)-1))
	if !(sigma > 1e-12) {
		return CusumState{}, false
	}
	mu0 := CusumHaircut * sum / float64(len(r))
	k := cusumKFloor
	if mu0 > 0 {
		k = mu0 / (2 * sigma)
	}
	st := CusumState{H: cusumThreshold(k, CusumARL0Days), K: k, Mu0: mu0, Sigma: sigma, RefDays: len(r)}
	s := 0.0
	for i, x := range finite(live) {
		z := math.Min(math.Max((mu0-x)/sigma, -cusumZClip), cusumZClip) - k
		s = math.Max(0, s+z)
		if !st.Alarm && s >= st.H {
			st.Alarm, st.AlarmDay = true, i+1
		}
	}
	st.S = s
	return st, true
}
