package trend

import "math"

// Frozen pre-2024-01-01 split-conformal conviction gate parameters calibrated in
// research/quant_lab/hrp_hurst_alpha.py (n_cal_total=1393: 766 proper train, 627 holdout).
const (
	conformalFavCount           = 330
	conformalAdvCount           = 297
	conformalAlpha              = 0.15
	conformalGateFloor          = 0.45
	conformalTrendPreserve      = 0.58
	conformalEntropyCrisisFloor = -1.25
)

var (
	conformalCoef = [7]float64{
		0.52788807,
		0.06537315,
		0.19988367,
		-0.11276375,
		-0.36679063,
		0.15154526,
		0.18212211,
	}
	conformalMean = [6]float64{
		0.49896817,
		-0.10388283,
		0.58399906,
		0.00484211,
		0.00328713,
		0.07652176,
	}
	conformalStd = [6]float64{
		0.15350374,
		1.39511762,
		0.23454355,
		0.01203783,
		0.01134948,
		0.18566843,
	}
	conformalFavKnots = [33]float64{
		0.30620961, 0.50516505, 0.53632321, 0.55322564, 0.5636727, 0.57270856, 0.58719216, 0.59476849, 0.60843688,
		0.61795433, 0.62900225, 0.63198567, 0.63528274, 0.64170633, 0.6481494, 0.65495318, 0.66207651, 0.66658316,
		0.67114714, 0.67405542, 0.6783703, 0.68483821, 0.69238929, 0.70028724, 0.70912915, 0.71803157, 0.72370576,
		0.72942444, 0.7375719, 0.75339807, 0.76295946, 0.78215668, 0.87722096,
	}
	conformalAdvKnots = [33]float64{
		0.49833225, 0.5421467, 0.55482264, 0.57390932, 0.59284381, 0.60387949, 0.61008722, 0.61696636, 0.62376494,
		0.62651469, 0.63037919, 0.63509355, 0.63802276, 0.64019038, 0.64314289, 0.64598943, 0.64942791, 0.65576291,
		0.66049329, 0.66877881, 0.67449262, 0.67682117, 0.68277913, 0.6865047, 0.69135502, 0.69610527, 0.70583929,
		0.71598949, 0.7305622, 0.74365993, 0.7803219, 0.81948642, 0.90944531,
	}
	ss15C1, ss15C2, ss15C3, sqrt5 = func() (float64, float64, float64, float64) {
		const period = 15.0
		arg := math.Sqrt(2.0) * math.Pi / period
		a1 := math.Exp(-arg)
		b1 := 2.0 * a1 * math.Cos(arg)
		c2 := b1
		c3 := -a1 * a1
		return 1.0 - c2 - c3, c2, c3, math.Sqrt(5.0)
	}()
)

// hrpWeights3x3Matrix computes single-linkage Hierarchical Risk Parity (López de Prado 2016)
// weights directly from 3 aligned return slices with zero heap allocations.
func hrpWeights3x3Matrix(r0, r1, r2 []float64) (w0, w1, w2 float64) {
	tLen := len(r0)
	if tLen < 2 || len(r1) != tLen || len(r2) != tLen {
		return 1.0 / 3.0, 1.0 / 3.0, 1.0 / 3.0
	}

	var mu [3]float64
	for i := 0; i < tLen; i++ {
		if !math.IsNaN(r0[i]) && !math.IsInf(r0[i], 0) {
			mu[0] += r0[i]
		}
		if !math.IsNaN(r1[i]) && !math.IsInf(r1[i], 0) {
			mu[1] += r1[i]
		}
		if !math.IsNaN(r2[i]) && !math.IsInf(r2[i], 0) {
			mu[2] += r2[i]
		}
	}
	fn := float64(tLen)
	mu[0] /= fn
	mu[1] /= fn
	mu[2] /= fn

	var cov [3][3]float64
	for i := 0; i < tLen; i++ {
		var d [3]float64
		if !math.IsNaN(r0[i]) && !math.IsInf(r0[i], 0) {
			d[0] = r0[i] - mu[0]
		} else {
			d[0] = -mu[0]
		}
		if !math.IsNaN(r1[i]) && !math.IsInf(r1[i], 0) {
			d[1] = r1[i] - mu[1]
		} else {
			d[1] = -mu[1]
		}
		if !math.IsNaN(r2[i]) && !math.IsInf(r2[i], 0) {
			d[2] = r2[i] - mu[2]
		} else {
			d[2] = -mu[2]
		}
		cov[0][0] += d[0] * d[0]
		cov[0][1] += d[0] * d[1]
		cov[0][2] += d[0] * d[2]
		cov[1][1] += d[1] * d[1]
		cov[1][2] += d[1] * d[2]
		cov[2][2] += d[2] * d[2]
	}
	denom := float64(tLen - 1)
	for i := 0; i < 3; i++ {
		for j := i; j < 3; j++ {
			cov[i][j] /= denom
			cov[j][i] = cov[i][j]
		}
	}

	// Variance with ddof=1 for active detection matches np.std(..., ddof=1) > 1e-12.
	const minVar = 1e-24
	var valid [3]bool
	var nValid int
	for i := 0; i < 3; i++ {
		if cov[i][i] > minVar {
			valid[i] = true
			nValid++
		}
	}
	if nValid == 0 {
		return 1.0 / 3.0, 1.0 / 3.0, 1.0 / 3.0
	}
	var out [3]float64
	if nValid == 1 {
		for i := 0; i < 3; i++ {
			if valid[i] {
				out[i] = 1.0
				break
			}
		}
		return out[0], out[1], out[2]
	}
	if nValid == 2 {
		var a, b int
		first := true
		for i := 0; i < 3; i++ {
			if valid[i] {
				if first {
					a = i
					first = false
				} else {
					b = i
				}
			}
		}
		vA := math.Max(cov[a][a], 1e-18)
		vB := math.Max(cov[b][b], 1e-18)
		sA := math.Max(vA, 1e-12*math.Max(vA, 1e-12))
		sB := math.Max(vB, 1e-12*math.Max(vB, 1e-12))
		alpha := 1.0 - sA/(sA+sB)
		out[a] = alpha
		out[b] = 1.0 - alpha
		return out[0], out[1], out[2]
	}

	// All 3 assets active: correlation distance matrix d_ij = sqrt(0.5*(1 - rho_ij)).
	sd := [3]float64{
		math.Sqrt(math.Max(cov[0][0], 1e-24)),
		math.Sqrt(math.Max(cov[1][1], 1e-24)),
		math.Sqrt(math.Max(cov[2][2], 1e-24)),
	}
	corrDist := func(i, j int) float64 {
		rho := cov[i][j] / (sd[i] * sd[j])
		if math.IsNaN(rho) {
			rho = 0
		} else if rho > 1.0 {
			rho = 1.0
		} else if rho < -1.0 {
			rho = -1.0
		}
		v := 0.5 * (1.0 - rho)
		if v <= 0 {
			return 0
		}
		return math.Sqrt(v)
	}
	d01, d02, d12 := corrDist(0, 1), corrDist(0, 2), corrDist(1, 2)

	// Single-linkage hierarchy for 3 items: pick smallest distance pair (a < b) and singleton c.
	// Matches SciPy linkage(condensed, method="single") + leaves_list exact order [c, a, b].
	a, b, c := 0, 1, 2
	minD := d01
	if d02 < minD {
		minD = d02
		a, b, c = 0, 2, 1
	}
	if d12 < minD {
		a, b, c = 1, 2, 0
	}

	// Cluster [c] variance (1 item: ivp = [1]).
	diagC := math.Max(cov[c][c], 1e-18)
	varL := math.Max(diagC, 1e-12*math.Max(diagC, 1e-12))

	// Cluster [a, b] inverse-variance portfolio variance.
	diagA := math.Max(cov[a][a], 1e-18)
	diagB := math.Max(cov[b][b], 1e-18)
	invA := 1.0 / diagA
	invB := 1.0 / diagB
	invSum := invA + invB
	ivpA, ivpB := invA/invSum, invB/invSum
	cVarR := ivpA*ivpA*cov[a][a] + 2.0*ivpA*ivpB*cov[a][b] + ivpB*ivpB*cov[b][b]
	scaleFloorR := 1e-12 * math.Max(0.5*(diagA+diagB), 1e-12)
	varR := math.Max(cVarR, scaleFloorR)

	alpha1 := 0.5
	if denom1 := varL + varR; denom1 > 0 {
		alpha1 = 1.0 - varL/denom1
	}

	// Second split of [a, b] -> [a] vs [b].
	varA := math.Max(diagA, 1e-12*math.Max(diagA, 1e-12))
	varB := math.Max(diagB, 1e-12*math.Max(diagB, 1e-12))
	alpha2 := 0.5
	if denom2 := varA + varB; denom2 > 0 {
		alpha2 = 1.0 - varA/denom2
	}

	out[c] = alpha1
	out[a] = (1.0 - alpha1) * alpha2
	out[b] = (1.0 - alpha1) * (1.0 - alpha2)
	tot := out[0] + out[1] + out[2]
	if tot <= 0 {
		return 1.0 / 3.0, 1.0 / 3.0, 1.0 / 3.0
	}
	return out[0] / tot, out[1] / tot, out[2] / tot
}

// hrpWeights3x3 computes causal López de Prado (2016) HRP allocation across BTC, ETH, and SOL
// over the trailing `window` daily returns. Falls back to triInvVolWeights during early warmup (< 20 returns).
func hrpWeights3x3(histB, histE, histS []Day, window int) (wB, wE, wS float64) {
	if window < 2 {
		window = 2
	}
	minLen := len(histB)
	if len(histE) < minLen {
		minLen = len(histE)
	}
	if len(histS) < minLen {
		minLen = len(histS)
	}
	nRet := minLen - 1
	if nRet < 20 {
		return triInvVolWeights(histB, histE, histS)
	}
	if nRet > window {
		nRet = window
	}
	subB := histB[len(histB)-(nRet+1):]
	subE := histE[len(histE)-(nRet+1):]
	subS := histS[len(histS)-(nRet+1):]

	const maxHRPWindow = 96
	var r0Buf, r1Buf, r2Buf [maxHRPWindow]float64
	var r0, r1, r2 []float64
	if nRet <= maxHRPWindow {
		r0, r1, r2 = r0Buf[:nRet], r1Buf[:nRet], r2Buf[:nRet]
	} else {
		r0 = make([]float64, nRet)
		r1 = make([]float64, nRet)
		r2 = make([]float64, nRet)
	}
	for i := 0; i < nRet; i++ {
		if subB[i].Close > 0 && subB[i+1].Close > 0 {
			r0[i] = subB[i+1].Close/subB[i].Close - 1.0
		}
		if subE[i].Close > 0 && subE[i+1].Close > 0 {
			r1[i] = subE[i+1].Close/subE[i].Close - 1.0
		}
		if subS[i].Close > 0 && subS[i+1].Close > 0 {
			r2[i] = subS[i+1].Close/subS[i].Close - 1.0
		}
	}
	return hrpWeights3x3Matrix(r0, r1, r2)
}

// rollingHurstFromLogPrices estimates H in [0.01, 0.99] via multi-lag order-2 variogram
// E[|Delta_tau ln P|^2] ~ tau^(2H) over tau in {1, 2, 4, 8, 16} inside the trailing `window` bars.
func rollingHurstFromLogPrices(logP []float64, window int) (float64, bool) {
	if window <= 16 || len(logP) < window {
		return 0.5, false
	}
	base := len(logP) - window
	taus := [5]int{1, 2, 4, 8, 16}
	var y [5]float64
	for li, tau := range taus {
		wEff := window - tau
		if wEff < 4 {
			return 0.5, false
		}
		start := base + tau
		end := base + window
		var sumD2 float64
		for i := start; i < end; i++ {
			d := logP[i] - logP[i-tau]
			sumD2 += d * d
		}
		meanD2 := sumD2 / float64(wEff)
		if meanD2 < 1e-16 {
			meanD2 = 1e-16
		}
		y[li] = math.Log(meanD2)
	}
	// OLS slope across x_k = ln(2^k) = k*ln(2) for k=0..4 has centered weights (-2, -1, 0, 1, 2) / (10 * ln(2)).
	slope := (-2.0*y[0] - y[1] + y[3] + 2.0*y[4]) / (10.0 * math.Ln2)
	h := 0.5 * slope
	if math.IsNaN(h) || math.IsInf(h, 0) {
		return 0.5, false
	}
	if h < 0.01 {
		h = 0.01
	} else if h > 0.99 {
		h = 0.99
	}
	return h, true
}

// rollingHurstVariogram estimates the causal Hurst exponent for one price series over `window` closes.
func rollingHurstVariogram(hist []Day, window int) (float64, bool) {
	if window <= 16 || len(hist) < window {
		return 0.5, false
	}
	sub := hist[len(hist)-window:]
	const maxHurstBuf = 128
	var buf [maxHurstBuf]float64
	var logP []float64
	if window <= maxHurstBuf {
		logP = buf[:window]
	} else {
		logP = make([]float64, window)
	}
	for i := 0; i < window; i++ {
		if sub[i].Close <= 0 {
			return 0.5, false
		}
		logP[i] = math.Log(sub[i].Close)
	}
	return rollingHurstFromLogPrices(logP, window)
}

// rollingBasketHurstVariogram estimates the causal equal-log-basket Hurst exponent across BTC, ETH, SOL.
func rollingBasketHurstVariogram(histB, histE, histS []Day, window int) (float64, bool) {
	if window <= 16 || len(histB) < window || len(histE) < window || len(histS) < window {
		return 0.5, false
	}
	subB := histB[len(histB)-window:]
	subE := histE[len(histE)-window:]
	subS := histS[len(histS)-window:]
	const maxHurstBuf = 128
	var buf [maxHurstBuf]float64
	var logP []float64
	if window <= maxHurstBuf {
		logP = buf[:window]
	} else {
		logP = make([]float64, window)
	}
	const inv3 = 1.0 / 3.0
	for i := 0; i < window; i++ {
		if subB[i].Close <= 0 || subE[i].Close <= 0 || subS[i].Close <= 0 {
			return 0.5, false
		}
		logP[i] = (math.Log(subB[i].Close) + math.Log(subE[i].Close) + math.Log(subS[i].Close)) * inv3
	}
	return rollingHurstFromLogPrices(logP, window)
}

// supersmootherTrendFromSlice computes Ehlers 2-pole SuperSmoother 5-bar normalized velocity strength in [0, 1]
// matching hrp_hurst_alpha.py / fast_universal_alpha.py with zero allocations for slices <= 128 bars.
func supersmootherTrendFromSlice(px []float64) float64 {
	n := len(px)
	if n < 6 {
		return 0.5
	}
	c1, c2, c3 := ss15C1, ss15C2, ss15C3

	const maxSSBuf = 128
	var ssBuf [maxSSBuf]float64
	var ss []float64
	if n <= maxSSBuf {
		ss = ssBuf[:n]
	} else {
		ss = make([]float64, n)
	}
	ss[0] = px[0]
	ss[1] = px[1]
	for t := 2; t < n; t++ {
		ss[t] = c1*0.5*(px[t]+px[t-1]) + c2*ss[t-1] + c3*ss[t-2]
	}

	prevSS := ss[n-6]
	if prevSS < 1e-12 {
		prevSS = 1e-12
	}
	vel5 := (ss[n-1] - prevSS) / prevSS

	// Trailing 20-day return std over px (or available prefix >= 10).
	retWin := 20
	if n-1 < retWin {
		retWin = n - 1
	}
	volD := 0.005
	if retWin >= 10 {
		start := n - 1 - retWin
		var sum, sum2 float64
		for i := start; i < n-1; i++ {
			if px[i] <= 0 || px[i+1] <= 0 {
				continue
			}
			r := px[i+1]/px[i] - 1.0
			sum += r
			sum2 += r * r
		}
		fn := float64(retWin)
		mu := sum / fn
		v := (sum2 - fn*mu*mu) / (fn - 1.0)
		if v > 0 {
			volD = math.Sqrt(v)
		}
	}
	if volD < 1e-4 {
		volD = 1e-4
	}
	tr := 0.5 + 0.5*math.Tanh(vel5/(volD*sqrt5))
	if math.IsNaN(tr) {
		return 0.5
	}
	if tr < 0 {
		return 0
	}
	if tr > 1 {
		return 1
	}
	return tr
}

func supersmootherTrendStrength(hist []Day) float64 {
	const win = 64
	n := len(hist)
	if n < 6 {
		return 0.5
	}
	if n > win {
		hist = hist[n-win:]
		n = win
	}
	var buf [win]float64
	for i := 0; i < n; i++ {
		if hist[i].Close <= 0 {
			return 0.5
		}
		buf[i] = hist[i].Close
	}
	return supersmootherTrendFromSlice(buf[:n])
}

func basketSupersmootherTrendStrength(histB, histE, histS []Day) float64 {
	const win = 64
	minLen := len(histB)
	if len(histE) < minLen {
		minLen = len(histE)
	}
	if len(histS) < minLen {
		minLen = len(histS)
	}
	if minLen < 6 {
		return 0.5
	}
	n := minLen
	if n > win {
		n = win
	}
	subB := histB[len(histB)-n:]
	subE := histE[len(histE)-n:]
	subS := histS[len(histS)-n:]
	const inv3 = 1.0 / 3.0
	var buf [win]float64
	for i := 0; i < n; i++ {
		cB, cE, cS := subB[i].Close, subE[i].Close, subS[i].Close
		if cB <= 0 || cE <= 0 || cS <= 0 {
			return 0.5
		}
		if p := cB * cE * cS; p > 0 && !math.IsInf(p, 0) {
			buf[i] = math.Cbrt(p)
		} else {
			buf[i] = math.Exp((math.Log(cB) + math.Log(cE) + math.Log(cS)) * inv3)
		}
	}
	return supersmootherTrendFromSlice(buf[:n])
}

func lookupConformalCDF(knots *[33]float64, p float64) float64 {
	if p <= knots[0] {
		return 0.0
	}
	if p >= knots[32] {
		return 1.0
	}
	for i := 0; i < 32; i++ {
		lo, hi := knots[i], knots[i+1]
		if p <= hi {
			if hi <= lo {
				return (float64(i) + 0.5) / 32.0
			}
			return (float64(i) + (p-lo)/(hi-lo)) / 32.0
		}
	}
	return 1.0
}

// conformalConvictionGateScore evaluates the pre-2024 frozen split-conformal transducer gate
// on (hurst, entropyZ, trendStrength) returning a risk multiplier in [0.45, 1.0].
func conformalConvictionGateScore(hurst, entropyZ, trendStrength float64) float64 {
	if math.IsNaN(hurst) || math.IsInf(hurst, 0) {
		hurst = 0.5
	}
	if math.IsNaN(entropyZ) || math.IsInf(entropyZ, 0) {
		entropyZ = 0.0
	}
	if math.IsNaN(trendStrength) || math.IsInf(trendStrength, 0) {
		trendStrength = 0.5
	}

	if trendStrength >= conformalTrendPreserve && entropyZ >= conformalEntropyCrisisFloor {
		return 1.0
	}

	posH := math.Max(0.0, hurst-0.50)
	negH := math.Max(0.0, 0.50-hurst)
	negTr50 := math.Max(0.0, 0.50-trendStrength)
	negTr55 := math.Max(0.0, 0.55-trendStrength)
	negEnt := math.Max(0.0, -entropyZ)

	feats := [6]float64{
		hurst,
		entropyZ,
		trendStrength,
		posH * negTr50,
		negH * negTr50,
		negEnt * negTr55,
	}
	z := conformalCoef[0]
	for j := 0; j < 6; j++ {
		z += conformalCoef[j+1] * ((feats[j] - conformalMean[j]) / conformalStd[j])
	}
	if z > 20.0 {
		z = 20.0
	} else if z < -20.0 {
		z = -20.0
	}
	pt := 1.0 / (1.0 + math.Exp(-z))

	uFav := lookupConformalCDF(&conformalFavKnots, pt)
	uAdv := lookupConformalCDF(&conformalAdvKnots, pt)
	pValFav := (1.0 + uFav*float64(conformalFavCount)) / (float64(conformalFavCount) + 1.0)
	pValAdv := (1.0 + (1.0-uAdv)*float64(conformalAdvCount)) / (float64(conformalAdvCount) + 1.0)
	denom := pValFav + pValAdv
	conv := 0.5
	if denom > 0 {
		conv = pValFav / denom
	}
	if conv >= 0.50 && pValFav > conformalAlpha {
		return 1.0
	}
	frac := (conv - 0.15) / 0.35
	if frac < 0.0 {
		frac = 0.0
	} else if frac > 1.0 {
		frac = 1.0
	}
	return conformalGateFloor + (1.0-conformalGateFloor)*frac
}

// conformalRegimeGate computes the strictly causal Q57 split-conformal regime gate across BTC, ETH, and SOL
// in [0.45, 1.0] with zero heap allocations. Returns 1.0 during early warmup (< 65 bars).
func conformalRegimeGate(histB, histE, histS []Day) float64 {
	hurst, okH := rollingBasketHurstVariogram(histB, histE, histS, 64)
	_, entZ, okE := quantumSpectralEntropyStats3x3(histB, histE, histS, 30, 60)
	if !okH || !okE {
		return 1.0
	}
	trBTC := supersmootherTrendStrength(histB)
	trBask := basketSupersmootherTrendStrength(histB, histE, histS)
	trendStrength := 0.5*trBTC + 0.5*trBask
	scale := conformalConvictionGateScore(hurst, entZ, trendStrength)

	// Instrumentation: record gate decision for later labeling
	if RegimeMetrics != nil && len(histB) > 0 {
		RegimeMetrics.RecordConformalGate(scale, histB[len(histB)-1].Date)
	}

	return scale
}
