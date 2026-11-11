package alpha

import (
	"errors"
	"math"
)

// ADFResult holds the statistical result of an Augmented Dickey-Fuller test for stationarity.
type ADFResult struct {
	TStat       float64 // Augmented Dickey-Fuller t-statistic
	PValue      float64 // Approximate asymptotic p-value (MacKinnon surface)
	Crit1Pct    float64 // 1% critical value (e.g. -3.43)
	Crit5Pct    float64 // 5% critical value (e.g. -2.86)
	Crit10Pct   float64 // 10% critical value (e.g. -2.57)
	IsStationary bool    // True if TStat < Crit5Pct (reject null of unit root at 5% significance)
	SampleCount int
}

// ComputeADF performs the Augmented Dickey-Fuller test on a 1D time-series slice.
// Fits the regression: \Delta y_t = c + \gamma * y_{t-1} + \sum_{i=1}^p \beta_i * \Delta y_{t-i} + \epsilon_t.
// Under the null hypothesis of a unit root, \gamma = 0.
func ComputeADF(series []float64, lags int) (ADFResult, error) {
	n := len(series)
	if n < 20 {
		return ADFResult{}, errors.New("adf: series length must be >= 20")
	}
	for _, v := range series {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return ADFResult{}, errors.New("adf: series contains NaN or Inf")
		}
	}
	if lags < 0 {
		lags = 1
	}
	if lags > (n-5)/3 {
		lags = (n - 5) / 3
	}

	// First differences: dy[t] = series[t] - series[t-1]
	dy := make([]float64, n-1)
	for t := 1; t < n; t++ {
		dy[t-1] = series[t] - series[t-1]
	}

	// Design matrix X and dependent variable Y
	// Rows T = (n - 1) - lags
	rows := len(dy) - lags
	cols := 2 + lags // intercept (1), y_{t-1} (1), and p lagged differences (\Delta y_{t-i})
	if rows <= cols {
		return ADFResult{}, errors.New("adf: insufficient degrees of freedom for requested lags")
	}

	// Normal equations: (X^T * X) * b = X^T * Y
	xtx := make([]float64, cols*cols)
	xty := make([]float64, cols)

	for r := 0; r < rows; r++ {
		// Dependent: \Delta y_{t} at index (r + lags)
		yVal := dy[r+lags]
		// Regressor row:
		// x[0] = 1.0 (constant)
		// x[1] = series[r + lags] (which is y_{t-1} relative to dy[r + lags])
		// x[2..] = dy[r + lags - 1], dy[r + lags - 2], ...
		xRow := make([]float64, cols)
		xRow[0] = 1.0
		xRow[1] = series[r+lags]
		for l := 0; l < lags; l++ {
			xRow[2+l] = dy[r+lags-1-l]
		}

		for i := 0; i < cols; i++ {
			xty[i] += xRow[i] * yVal
			for j := 0; j < cols; j++ {
				xtx[i*cols+j] += xRow[i] * xRow[j]
			}
		}
	}

	// Solve linear system via Gaussian elimination with partial pivoting
	b, invDiag, err := solveOLSWithInvDiag(xtx, xty, cols)
	if err != nil {
		return ADFResult{}, err
	}

	// Compute residual sum of squares (RSS)
	var rss float64
	for r := 0; r < rows; r++ {
		yVal := dy[r+lags]
		pred := b[0] + b[1]*series[r+lags]
		for l := 0; l < lags; l++ {
			pred += b[2+l] * dy[r+lags-1-l]
		}
		res := yVal - pred
		rss += res * res
	}

	dof := float64(rows - cols)
	if dof <= 0 {
		return ADFResult{}, errors.New("adf: zero degrees of freedom")
	}
	sigmaSq := rss / dof
	if sigmaSq < 1e-14 || math.IsNaN(sigmaSq) {
		sigmaSq = 1e-14
	}
	// Standard error of \hat{\gamma} (which is parameter b[1]):
	// SE(b[1]) = sqrt(sigmaSq * (X^T X)^{-1}_{1,1})
	inv11 := invDiag[1]
	if inv11 <= 0 || math.IsNaN(inv11) {
		inv11 = 1e-6
	}
	seGamma := math.Sqrt(sigmaSq * inv11)
	if seGamma < 1e-12 || math.IsNaN(seGamma) {
		seGamma = 1e-12
	}

	tStat := b[1] / seGamma
	if math.IsNaN(tStat) || math.IsInf(tStat, 0) {
		tStat = 0.0
	}

	// MacKinnon (1994) asymptotic critical values for model with constant
	crit1Pct := -3.43 - 5.999/float64(rows)
	crit5Pct := -2.86 - 2.738/float64(rows)
	crit10Pct := -2.57 - 1.551/float64(rows)

	// Approximate MacKinnon p-value
	pValue := approximateADFPValue(tStat)

	return ADFResult{
		TStat:       tStat,
		PValue:      pValue,
		Crit1Pct:    crit1Pct,
		Crit5Pct:    crit5Pct,
		Crit10Pct:   crit10Pct,
		IsStationary: tStat < crit5Pct, // Reject null of unit root at 5% level
		SampleCount: rows,
	}, nil
}

// approximateADFPValue provides standard approximation of the Dickey-Fuller distribution p-value.
func approximateADFPValue(t float64) float64 {
	if t < -4.5 {
		return 0.001
	} else if t < -3.5 {
		return 0.005 + (t+4.5)*0.005
	} else if t < -2.86 {
		return 0.01 + (t+3.5)*0.0625
	} else if t < -2.57 {
		return 0.05 + (t+2.86)*0.172
	} else if t < -1.95 {
		return 0.10 + (t+2.57)*0.322
	} else if t < 0.0 {
		return 0.30 + (t+1.95)*0.358
	}
	return 1.0 / (1.0 + math.Exp(-t*0.8))
}

// solveOLSWithInvDiag solves (X^T X) * b = X^T Y and estimates the diagonal of (X^T X)^{-1}.
func solveOLSWithInvDiag(aIn, bIn []float64, n int) (b []float64, invDiag []float64, err error) {
	// Copy matrix and augment with identity matrix to find inverse
	a := make([]float64, n*2*n)
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			a[i*(2*n)+j] = aIn[i*n+j]
		}
		a[i*(2*n)+n+i] = 1.0 // Identity matrix
	}

	for i := 0; i < n; i++ {
		// Partial pivoting
		pivot := i
		maxVal := math.Abs(a[i*(2*n)+i])
		for k := i + 1; k < n; k++ {
			if v := math.Abs(a[k*(2*n)+i]); v > maxVal {
				maxVal = v
				pivot = k
			}
		}
		if maxVal < 1e-12 {
			// Ridge regularize singular matrix diagonal slightly
			a[i*(2*n)+i] += 1e-5
		}
		if pivot != i {
			for j := 0; j < 2*n; j++ {
				a[i*(2*n)+j], a[pivot*(2*n)+j] = a[pivot*(2*n)+j], a[i*(2*n)+j]
			}
		}

		div := a[i*(2*n)+i]
		if math.Abs(div) < 1e-12 {
			div = 1e-12
		}
		for j := 0; j < 2*n; j++ {
			a[i*(2*n)+j] /= div
		}

		for k := 0; k < n; k++ {
			if k != i {
				factor := a[k*(2*n)+i]
				for j := 0; j < 2*n; j++ {
					a[k*(2*n)+j] -= factor * a[i*(2*n)+j]
				}
			}
		}
	}

	// Multiply (A^-1) * bIn
	b = make([]float64, n)
	invDiag = make([]float64, n)
	for i := 0; i < n; i++ {
		invDiag[i] = a[i*(2*n)+n+i]
		for j := 0; j < n; j++ {
			b[i] += a[i*(2*n)+n+j] * bIn[j]
		}
	}
	return b, invDiag, nil
}

// CointegrationModel tracks pair co-movement (e.g. BTC vs ETH) and generates statistical arbitrage signals.
type CointegrationModel struct {
	LeaderSymbol   string
	FollowerSymbol string
	LookbackWindow int

	HistoryLeader   []float64
	HistoryFollower []float64

	Beta         float64 // Dynamic rolling hedge ratio: Cov(X,Y)/Var(X)
	Alpha        float64 // Dynamic spread intercept
	RollingMean  float64 // Mean of the residual spread: spread = Y - beta*X
	RollingStd   float64 // Standard deviation of residual spread
	LatestSpread float64
	LatestZScore float64

	IsCointegrated bool
	ADFStat        float64
}

// NewCointegrationModel creates an institutional stat-arb tracking engine.
func NewCointegrationModel(leader, follower string, lookbackWindow int) *CointegrationModel {
	if lookbackWindow < 30 {
		lookbackWindow = 60
	}
	return &CointegrationModel{
		LeaderSymbol:   leader,
		FollowerSymbol: follower,
		LookbackWindow: lookbackWindow,
		Beta:           1.0,
		RollingStd:     1.0,
	}
}

// Update ingests new price observations for the pair (e.g. BTC and ETH prices).
// Calculates dynamic OLS hedge ratio, residual spread, and rolling Z-score.
func (cm *CointegrationModel) Update(priceLeader, priceFollower float64) (zScore float64, isCointegrated bool) {
	if priceLeader <= 0 || priceFollower <= 0 || math.IsNaN(priceLeader) || math.IsInf(priceLeader, 0) || math.IsNaN(priceFollower) || math.IsInf(priceFollower, 0) {
		return 0, false
	}

	cm.HistoryLeader = append(cm.HistoryLeader, priceLeader)
	cm.HistoryFollower = append(cm.HistoryFollower, priceFollower)

	if len(cm.HistoryLeader) > cm.LookbackWindow {
		cm.HistoryLeader = cm.HistoryLeader[1:]
		cm.HistoryFollower = cm.HistoryFollower[1:]
	}

	n := len(cm.HistoryLeader)
	if n < 20 {
		cm.LatestSpread = priceFollower - cm.Beta*priceLeader
		return 0, false
	}

	// Compute OLS hedge ratio: beta = Cov(X, Y) / Var(X)
	var sumX, sumY, sumXY, sumX2 float64
	for i := 0; i < n; i++ {
		x := cm.HistoryLeader[i]
		y := cm.HistoryFollower[i]
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}
	meanX := sumX / float64(n)
	meanY := sumY / float64(n)

	var covXY, varX float64
	for i := 0; i < n; i++ {
		dx := cm.HistoryLeader[i] - meanX
		dy := cm.HistoryFollower[i] - meanY
		covXY += dx * dy
		varX += dx * dx
	}
	if varX > 1e-12 {
		cm.Beta = covXY / varX
	}
	cm.Alpha = meanY - cm.Beta*meanX

	// Compute residual spread series: \epsilon_i = Y_i - \beta X_i - \alpha
	spreads := make([]float64, n)
	var spreadSum, spreadSumSq float64
	for i := 0; i < n; i++ {
		spr := cm.HistoryFollower[i] - cm.Beta*cm.HistoryLeader[i] - cm.Alpha
		spreads[i] = spr
		spreadSum += spr
		spreadSumSq += spr * spr
	}

	cm.RollingMean = spreadSum / float64(n)
	spreadVar := (spreadSumSq / float64(n)) - (cm.RollingMean * cm.RollingMean)
	if spreadVar > 1e-12 {
		cm.RollingStd = math.Sqrt(spreadVar)
	} else {
		cm.RollingStd = 1.0
	}

	cm.LatestSpread = priceFollower - cm.Beta*priceLeader - cm.Alpha
	if cm.RollingStd > 1e-12 {
		cm.LatestZScore = (cm.LatestSpread - cm.RollingMean) / cm.RollingStd
	} else {
		cm.LatestZScore = 0.0
	}
	// Clamp Z-score to realistic standard deviation bounds [-5.0, +5.0] and guard against NaN
	if math.IsNaN(cm.LatestZScore) || math.IsInf(cm.LatestZScore, 0) {
		cm.LatestZScore = 0.0
	} else if cm.LatestZScore > 5.0 {
		cm.LatestZScore = 5.0
	} else if cm.LatestZScore < -5.0 {
		cm.LatestZScore = -5.0
	}

	// Periodic ADF test on residual spread to verify co-integration holds
	if n >= 30 {
		adf, err := ComputeADF(spreads, 1)
		if err == nil {
			cm.IsCointegrated = adf.IsStationary
			cm.ADFStat = adf.TStat
		}
	}

	return cm.LatestZScore, cm.IsCointegrated
}
