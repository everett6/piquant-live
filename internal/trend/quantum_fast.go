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

const (
	maxEntropyWindowBuf = 128
	maxEntropyHistBuf   = 192
)

var invLn3 = 1.0 / math.Log(3.0)

// quantumSpectralEntropyFromMoments computes S(rho) = -Tr(rho * ln(rho)) / ln(3) in [0, 1]
// directly from uncentered window sums and cross-products with zero heap allocations.
func quantumSpectralEntropyFromMoments(w, sumB, sumE, sumS, sumBB, sumEE, sumSS, sumBE, sumBS, sumES float64) (float64, bool) {
	if w < 5 {
		return 0, false
	}
	invW := 1.0 / w
	vBB := sumBB - sumB*sumB*invW
	vEE := sumEE - sumE*sumE*invW
	vSS := sumSS - sumS*sumS*invW
	if vBB <= 1e-18 || vEE <= 1e-18 || vSS <= 1e-18 {
		return 0, false
	}
	cBE := sumBE - sumB*sumE*invW
	cBS := sumBS - sumB*sumS*invW
	cES := sumES - sumE*sumS*invW

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
		q := (a00 + a11 + a22) * inv3
		p2 := (a00-q)*(a00-q) + (a11-q)*(a11-q) + (a22-q)*(a22-q) + 2.0*p1
		// Guard against negative p2 due to floating-point error
		if p2 < 0 {
			p2 = 0
		}
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
		phi := math.Acos(r) * inv3
		eig[0] = q + 2.0*p*math.Cos(phi)
		eig[2] = q + 2.0*p*math.Cos(phi+2.0*math.Pi*inv3)
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
	s := h * invLn3
	if s < 0 {
		s = 0
	} else if s > 1 {
		s = 1
	}
	return s, true
}

// quantumSpectralEntropyFromReturns computes S(rho) = -Tr(rho * ln(rho)) / ln(3) in [0, 1]
// for three aligned return slices without heap allocations.
func quantumSpectralEntropyFromReturns(rb, re, rs []float64) (float64, bool) {
	w := len(rb)
	if w < 5 || len(re) != w || len(rs) != w {
		return 0, false
	}
	var sumB, sumE, sumS float64
	for i := 0; i < w; i++ {
		sumB += rb[i]
		sumE += re[i]
		sumS += rs[i]
	}
	fn := float64(w)
	muB, muE, muS := sumB/fn, sumE/fn, sumS/fn

	var vBB, vEE, vSS, cBE, cBS, cES float64
	for i := 0; i < w; i++ {
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
	return quantumSpectralEntropyFromMoments(fn, 0, 0, 0, vBB, vEE, vSS, cBE, cBS, cES)
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

	var rbBuf, reBuf, rsBuf [maxEntropyWindowBuf]float64
	var rb, re, rs []float64
	if window <= maxEntropyWindowBuf {
		rb, re, rs = rbBuf[:window], reBuf[:window], rsBuf[:window]
	} else {
		rb = make([]float64, window)
		re = make([]float64, window)
		rs = make([]float64, window)
	}
	for i := 1; i <= window; i++ {
		cB0, cE0, cS0 := subB[i-1].Close, subE[i-1].Close, subS[i-1].Close
		cB1, cE1, cS1 := subB[i].Close, subE[i].Close, subS[i].Close
		if cB0 <= 0 || cE0 <= 0 || cS0 <= 0 || cB1 <= 0 || cE1 <= 0 || cS1 <= 0 {
			return 0, false
		}
		rb[i-1] = cB1/cB0 - 1.0
		re[i-1] = cE1/cE0 - 1.0
		rs[i-1] = cS1/cS0 - 1.0
	}
	return quantumSpectralEntropyFromReturns(rb, re, rs)
}

// quantumSpectralEntropyStats3x3 computes the latest 3x3 Von Neumann spectral entropy and its
// rolling z-score over `zWin` trailing bars in a single allocation-free pass.
func quantumSpectralEntropyStats3x3(histB, histE, histS []Day, window, zWin int) (sNow, zScore float64, ok bool) {
	if window < 5 || zWin < 20 {
		return 0, 0, false
	}
	minLen := len(histB)
	if len(histE) < minLen {
		minLen = len(histE)
	}
	if len(histS) < minLen {
		minLen = len(histS)
	}
	if minLen < window+1 {
		return 0, 0, false
	}
	maxSteps := minLen - window
	if maxSteps > zWin {
		maxSteps = zWin
	}
	if maxSteps < 20 {
		return 0, 0, false
	}
	nRet := window + maxSteps - 1
	subB := histB[len(histB)-(nRet+1):]
	subE := histE[len(histE)-(nRet+1):]
	subS := histS[len(histS)-(nRet+1):]

	var rbBuf, reBuf, rsBuf [maxEntropyHistBuf]float64
	var validBuf [maxEntropyHistBuf]bool
	var rb, re, rs []float64
	var valid []bool
	if nRet <= maxEntropyHistBuf {
		rb, re, rs, valid = rbBuf[:nRet], reBuf[:nRet], rsBuf[:nRet], validBuf[:nRet]
	} else {
		rb = make([]float64, nRet)
		re = make([]float64, nRet)
		rs = make([]float64, nRet)
		valid = make([]bool, nRet)
	}
	for i := 0; i < nRet; i++ {
		cB0, cE0, cS0 := subB[i].Close, subE[i].Close, subS[i].Close
		cB1, cE1, cS1 := subB[i+1].Close, subE[i+1].Close, subS[i+1].Close
		if cB0 > 0 && cE0 > 0 && cS0 > 0 && cB1 > 0 && cE1 > 0 && cS1 > 0 {
			rb[i] = cB1/cB0 - 1.0
			re[i] = cE1/cE0 - 1.0
			rs[i] = cS1/cS0 - 1.0
			valid[i] = true
		}
	}

	var sum, sum2 float64
	var cnt int
	var sumB, sumE, sumS, sumBB, sumEE, sumSS, sumBE, sumBS, sumES float64
	var validCnt int
	fw := float64(window)
	lastStart := maxSteps - 1
	latestValid := false

	for start := 0; start < maxSteps; start++ {
		if start == 0 || start&31 == 0 {
			sumB, sumE, sumS = 0, 0, 0
			sumBB, sumEE, sumSS = 0, 0, 0
			sumBE, sumBS, sumES = 0, 0, 0
			validCnt = 0
			end := start + window
			for i := start; i < end; i++ {
				if !valid[i] {
					continue
				}
				b, e, sVal := rb[i], re[i], rs[i]
				sumB += b
				sumE += e
				sumS += sVal
				sumBB += b * b
				sumEE += e * e
				sumSS += sVal * sVal
				sumBE += b * e
				sumBS += b * sVal
				sumES += e * sVal
				validCnt++
			}
		} else {
			out := start - 1
			if valid[out] {
				b, e, sVal := rb[out], re[out], rs[out]
				sumB -= b
				sumE -= e
				sumS -= sVal
				sumBB -= b * b
				sumEE -= e * e
				sumSS -= sVal * sVal
				sumBE -= b * e
				sumBS -= b * sVal
				sumES -= e * sVal
				validCnt--
			}
			in := start + window - 1
			if valid[in] {
				b, e, sVal := rb[in], re[in], rs[in]
				sumB += b
				sumE += e
				sumS += sVal
				sumBB += b * b
				sumEE += e * e
				sumSS += sVal * sVal
				sumBE += b * e
				sumBS += b * sVal
				sumES += e * sVal
				validCnt++
			}
		}
		if validCnt != window {
			continue
		}
		s, sok := quantumSpectralEntropyFromMoments(fw, sumB, sumE, sumS, sumBB, sumEE, sumSS, sumBE, sumBS, sumES)
		if !sok {
			continue
		}
		if start == lastStart {
			sNow = s
			latestValid = true
		}
		sum += s
		sum2 += s * s
		cnt++
	}
	if !latestValid || cnt < 20 {
		return 0, 0, false
	}
	fn := float64(cnt)
	mu := sum / fn
	variance := (sum2 - fn*mu*mu) / (fn - 1.0)
	if variance > 1e-12 {
		zScore = (sNow - mu) / math.Sqrt(variance)
	}
	return sNow, zScore, true
}

// quantumRegimeScale computes a causal risk-scaling multiplier in [floor, 1.0] from trailing
// zWin observations of 3x3 Von Neumann spectral entropy. When correlation entropy drops
// abruptly (herding / systemic risk-off compression), exposure tapers toward floor.
func quantumRegimeScale(histB, histE, histS []Day, window, zWin int, floor float64) float64 {
	sNow, z, ok := quantumSpectralEntropyStats3x3(histB, histE, histS, window, zWin)
	if !ok {
		return 1.0
	}
	scale := 1.0
	if z <= -2.5 {
		scale = floor
	} else if z < -1.0 {
		frac := (-1.0 - z) / 1.5
		scale = 1.0 - (1.0-floor)*frac
	}
	if sNow < 0.35 {
		rawScale := floor + (1.0-floor)*(sNow/0.35)
		if rawScale < scale {
			scale = rawScale
		}
	}
	if scale < floor {
		scale = floor
	}

	// Instrumentation: record gate decision for later labeling
	if RegimeMetrics != nil && len(histB) > 0 {
		RegimeMetrics.RecordQuantumGate(scale, histB[len(histB)-1].Date)
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
