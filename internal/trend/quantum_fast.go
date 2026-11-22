package trend

import "math"

// garmanKlassVol estimates daily volatility over the trailing `window` OHLC bars using the
// Garman-Klass range estimator (0.5*ln(H/L)^2 - (2*ln(2)-1)*ln(C/O)^2, floored at 0).
// Falls back to sampleStdLastN when any bar in the window lacks valid OHLC values.
func garmanKlassVol(hist []Day, window int) (float64, bool) {
	if window < 2 || len(hist) < window {
		return 0, false
	}
	sub := hist[len(hist)-window:]
	var sumVar float64
	coeff := 2.0*math.Ln2 - 1.0
	for _, d := range sub {
		if d.Open <= 0 || d.High <= 0 || d.Low <= 0 || d.Close <= 0 || d.High < d.Low {
			return sampleStdLastN(hist, window)
		}
		hl := math.Log(d.High / d.Low)
		co := math.Log(d.Close / d.Open)
		v := 0.5*hl*hl - coeff*co*co
		if v > 0 {
			sumVar += v
		}
	}
	meanVar := sumVar / float64(window)
	if meanVar <= 0 {
		return sampleStdLastN(hist, window)
	}
	return math.Sqrt(meanVar), true
}

// rangeVolCrashBrake computes a causal crash-protection multiplier in [floor, 1.0] from the ratio
// of fast (e.g. 5d) to slow (e.g. 30d) Garman-Klass volatility. Returns 1.0 during warmup or calm regimes.
func rangeVolCrashBrake(hist []Day, fastWin, slowWin int, ratioThresh, floor float64) float64 {
	if fastWin < 2 || slowWin <= fastWin || ratioThresh <= 0 {
		return 1.0
	}
	vFast, okF := garmanKlassVol(hist, fastWin)
	vSlow, okS := garmanKlassVol(hist, slowWin)
	if !okF || !okS || vSlow <= 1e-8 {
		return 1.0
	}
	ratio := vFast / vSlow
	if ratio <= ratioThresh {
		return 1.0
	}
	maxRatio := ratioThresh * 2.0
	if ratio >= maxRatio {
		return floor
	}
	frac := (ratio - ratioThresh) / (maxRatio - ratioThresh)
	return 1.0 - (1.0-floor)*frac
}

// quantumSpectralEntropy3x3 computes the threshold-free Von Neumann spectral entropy
// S(rho) = -Tr(rho * ln(rho)) / ln(3) in [0, 1] for the rescaled density matrix rho = C / 3
// of trailing `window`-day returns across BTC, ETH, and SOL (Felippe et al., EPL 2023).
func quantumSpectralEntropy3x3(histB, histE, histS []Day, window int) (float64, bool) {
	if window < 5 || len(histB) < window+1 || len(histE) < window+1 || len(histS) < window+1 {
		return 0, false
	}
	subB := histB[len(histB)-(window+1):]
	subE := histE[len(histE)-(window+1):]
	subS := histS[len(histS)-(window+1):]

	rb := make([]float64, window)
	re := make([]float64, window)
	rs := make([]float64, window)
	var sumB, sumE, sumS float64
	for i := 1; i <= window; i++ {
		if subB[i-1].Close <= 0 || subE[i-1].Close <= 0 || subS[i-1].Close <= 0 {
			return 0, false
		}
		if subB[i].Close <= 0 || subE[i].Close <= 0 || subS[i].Close <= 0 {
			return 0, false
		}
		vb := subB[i].Close/subB[i-1].Close - 1.0
		ve := subE[i].Close/subE[i-1].Close - 1.0
		vs := subS[i].Close/subS[i-1].Close - 1.0
		rb[i-1], re[i-1], rs[i-1] = vb, ve, vs
		sumB += vb
		sumE += ve
		sumS += vs
	}
	fn := float64(window)
	muB, muE, muS := sumB/fn, sumE/fn, sumS/fn

	var vBB, vEE, vSS, cBE, cBS, cES float64
	for i := 0; i < window; i++ {
		db := rb[i] - muB
		de := re[i] - muE
		ds := rs[i] - muS
		vBB += db * db
		vEE += de * de
		vSS += ds * ds
		cBE += db * de
		cBS += db * ds
		cES += de * ds
	}
	if vBB <= 1e-18 || vEE <= 1e-18 || vSS <= 1e-18 {
		return 0, false
	}
	clampCorr := func(x float64) float64 {
		if x > 0.999999 {
			return 0.999999
		}
		if x < -0.999999 {
			return -0.999999
		}
		return x
	}
	c01 := clampCorr(cBE / math.Sqrt(vBB*vEE))
	c02 := clampCorr(cBS / math.Sqrt(vBB*vSS))
	c12 := clampCorr(cES / math.Sqrt(vEE*vSS))

	// Rescaled quantum density matrix rho = C / 3.0.
	const inv3 = 1.0 / 3.0
	a00, a11, a22 := inv3, inv3, inv3
	a01, a02, a12 := c01*inv3, c02*inv3, c12*inv3

	// Closed-form eigenvalues of a 3x3 real symmetric matrix (Smith 1961 / Kopp 2008).
	p1 := a01*a01 + a02*a02 + a12*a12
	var eig [3]float64
	if p1 < 1e-16 {
		eig = [3]float64{a00, a11, a22}
	} else {
		q := (a00 + a11 + a22) / 3.0
		p2 := (a00-q)*(a00-q) + (a11-q)*(a11-q) + (a22-q)*(a22-q) + 2.0*p1
		p := math.Sqrt(p2 / 6.0)
		b00, b11, b22 := (a00-q)/p, (a11-q)/p, (a22-q)/p
		b01, b02, b12 := a01/p, a02/p, a12/p
		detB := b00*(b11*b22-b12*b12) - b01*(b01*b22-b12*b02) + b02*(b01*b12-b11*b02)
		r := detB * 0.5
		if r > 1.0 {
			r = 1.0
		} else if r < -1.0 {
			r = -1.0
		}
		phi := math.Acos(r) / 3.0
		eig[0] = q + 2.0*p*math.Cos(phi)
		eig[2] = q + 2.0*p*math.Cos(phi+2.0*math.Pi/3.0)
		eig[1] = 3.0*q - eig[0] - eig[2]
	}

	var sumEig float64
	for i := range eig {
		if eig[i] < 0 {
			eig[i] = 0
		}
		sumEig += eig[i]
	}
	if sumEig <= 0 {
		return 0, false
	}

	var h float64
	for _, ev := range eig {
		lam := ev / sumEig
		if lam > 1e-15 {
			h -= lam * math.Log(lam)
		}
	}
	s := h / math.Log(3.0)
	if s < 0 {
		s = 0
	} else if s > 1 {
		s = 1
	}
	return s, true
}

// quantumRegimeScale computes a causal risk-scaling multiplier in [floor, 1.0] from trailing
// zWin observations of 3x3 Von Neumann spectral entropy. When correlation entropy drops
// abruptly (herding / systemic risk-off compression), exposure tapers toward floor.
func quantumRegimeScale(histB, histE, histS []Day, window, zWin int, floor float64) float64 {
	if zWin < 20 || len(histB) < window+1 || len(histE) < window+1 || len(histS) < window+1 {
		return 1.0
	}
	sNow, ok := quantumSpectralEntropy3x3(histB, histE, histS, window)
	if !ok {
		return 1.0
	}
	samples := make([]float64, 0, zWin)
	for k := 0; k < zWin; k++ {
		eb := len(histB) - k
		ee := len(histE) - k
		es := len(histS) - k
		if eb < window+1 || ee < window+1 || es < window+1 {
			break
		}
		if s, sok := quantumSpectralEntropy3x3(histB[:eb], histE[:ee], histS[:es], window); sok {
			samples = append(samples, s)
		}
	}
	if len(samples) < 20 {
		return 1.0
	}
	var sum, sum2 float64
	for _, v := range samples {
		sum += v
		sum2 += v * v
	}
	fn := float64(len(samples))
	mu := sum / fn
	variance := (sum2 - fn*mu*mu) / (fn - 1.0)
	scale := 1.0
	if variance > 1e-12 {
		z := (sNow - mu) / math.Sqrt(variance)
		if z <= -2.5 {
			scale = floor
		} else if z < -1.0 {
			frac := (-1.0 - z) / 1.5
			scale = 1.0 - (1.0-floor)*frac
		}
	}
	if sNow < 0.35 {
		rawScale := floor + (1.0-floor)*(sNow/0.35)
		if rawScale < scale {
			scale = rawScale
		}
	}
	if scale < floor {
		return floor
	}
	return scale
}

// blendedMomentumPositive implements the PREREG53/54 absolute momentum cash gate across
// available 7d, 28d, and 60d simple returns. Returns true during early warmup (< 8 closes).
func blendedMomentumPositive(hist []Day) bool {
	if len(hist) < 8 {
		return true
	}
	cNow := hist[len(hist)-1].Close
	if cNow <= 0 {
		return false
	}
	var sum float64
	var cnt int
	for _, h := range [3]int{7, 28, 60} {
		if len(hist) > h {
			cPrev := hist[len(hist)-1-h].Close
			if cPrev > 0 {
				sum += cNow/cPrev - 1.0
				cnt++
			}
		}
	}
	if cnt == 0 {
		return true
	}
	return (sum / float64(cnt)) > 0.0
}
