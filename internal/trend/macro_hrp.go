package trend

import (
	"bufio"
	"encoding/csv"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"piquant-live/internal/carry"
	"piquant-live/internal/execution"
)

// MacroEquityTickers is the 12-asset US equities, treasuries, commodities, and USD macro universe
// paired with the crypto Q57 ledger in hrp_hurst_alpha.py (SPY, QQQ, IWM, TLT, GLD, NVDA, AAPL, MSFT, AMZN, META, DBC, UUP).
var MacroEquityTickers = []string{
	"SPY", "QQQ", "IWM", "TLT", "GLD", "NVDA", "AAPL", "MSFT", "AMZN", "META", "DBC", "UUP",
}

// DefaultEquitiesDir returns ~/piquant-data/equities (or $PIQUANT_EQUITIES_DIR if set).
func DefaultEquitiesDir() string {
	if env := strings.TrimSpace(os.Getenv("PIQUANT_EQUITIES_DIR")); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "piquant-data", "equities")
}

// MacroAssetTarget describes one macro/equity ETF target allocation within the macro sleeve or full fund.
type MacroAssetTarget struct {
	Symbol    string  `json:"symbol"`
	Role      string  `json:"role"`
	TargetPct float64 `json:"target_pct"`
	LastClose float64 `json:"last_close"`
	Momentum  float64 `json:"momentum_score"`
	Vol20Pct  float64 `json:"vol20_pct"`
	TrendPos  float64 `json:"trend_pos"`
}

// MacroSleeveSummary captures historical simulation stats and current target weights for the 12-asset HRP macro/equities sleeve.
type MacroSleeveSummary struct {
	LastDate      string             `json:"last_date,omitempty"`
	Days          int                `json:"days"`
	TickersLoaded int                `json:"tickers_loaded"`
	Equity        float64            `json:"equity"`
	CAGR          float64            `json:"cagr_pct"`
	MaxDD         float64            `json:"max_dd_pct"`
	MeanDD        float64            `json:"mean_dd_pct"`
	UlcerIndex    float64            `json:"ulcer_index_pct"`
	Sharpe        float64            `json:"sharpe"`
	Calmar        float64            `json:"calmar"`
	InvestedPct   float64            `json:"invested_pct"`
	IdleSGOVPct   float64            `json:"idle_sgov_pct"`
	DailyDates    []string           `json:"-"`
	DailyRets     []float64          `json:"-"`
	Targets       []MacroAssetTarget `json:"targets"`
}

// FullFundSummary combines the Crypto HRP+Hurst+Conformal Q57 sleeve, 12-asset Macro HRP sleeve,
// C1 Carry, and SGOV T-bills into the unified institutional multi-asset Q57 portfolio.
type FullFundSummary struct {
	Name                  string             `json:"name"`
	LastDate              string             `json:"last_date,omitempty"`
	CryptoSleeveWeightPct float64            `json:"crypto_sleeve_weight_pct"`
	MacroSleeveWeightPct  float64            `json:"macro_sleeve_weight_pct"`
	CryptoBTCPortPct      float64            `json:"crypto_btc_port_pct"`
	CryptoETHPortPct      float64            `json:"crypto_eth_port_pct"`
	CryptoSOLPortPct      float64            `json:"crypto_sol_port_pct"`
	CarrySleevePct        float64            `json:"carry_sleeve_pct"`
	MacroInvestedPct      float64            `json:"macro_invested_pct"`
	IdleSGOVPct           float64            `json:"idle_sgov_pct"`
	MacroStats            MacroSleeveSummary `json:"macro_stats"`
	CryptoBlend           BlendSummary       `json:"crypto_blend"`
	MacroTargets          []MacroAssetTarget `json:"macro_targets"`
}

func macroAssetRole(sym string) string {
	switch strings.ToUpper(sym) {
	case "DBC":
		return "Commodities Trend"
	case "GLD":
		return "Gold Inflation Hedge"
	case "TLT":
		return "20Y+ US Treasuries"
	case "UUP":
		return "US Dollar Index"
	case "SPY":
		return "S&P 500 Core"
	case "QQQ":
		return "Nasdaq 100 Growth"
	case "IWM":
		return "Russell 2000 Small Cap"
	default:
		return "Mega-Cap Equity"
	}
}

// hrpWeightsN computes López de Prado (2016) single-linkage Hierarchical Risk Parity weights
// for N assets from column-major returns `rets[asset][bar]` (all columns sharing the same length T).
// Handles zero-variance/degenerate columns cleanly and matches SciPy single-linkage + leaves_list.
func hrpWeightsN(rets [][]float64) []float64 {
	nAssets := len(rets)
	if nAssets == 0 {
		return nil
	}
	if nAssets == 1 {
		return []float64{1.0}
	}
	tLen := len(rets[0])
	if tLen < 2 {
		w := make([]float64, nAssets)
		u := 1.0 / float64(nAssets)
		for i := range w {
			w[i] = u
		}
		return w
	}

	const minStd = 1e-12
	validIdx := make([]int, 0, nAssets)
	means := make([]float64, nAssets)
	for j := 0; j < nAssets; j++ {
		col := rets[j]
		if len(col) != tLen {
			continue
		}
		var fin int
		var sum float64
		for t := 0; t < tLen; t++ {
			v := col[t]
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				fin++
				sum += v
			}
		}
		if fin < 2 {
			continue
		}
		mu := sum / float64(tLen)
		var sumSq float64
		for t := 0; t < tLen; t++ {
			v := col[t]
			if math.IsNaN(v) || math.IsInf(v, 0) {
				v = 0
			}
			d := v - mu
			sumSq += d * d
		}
		std := math.Sqrt(sumSq / float64(tLen-1))
		if std > minStd {
			validIdx = append(validIdx, j)
			means[j] = mu
		}
	}

	out := make([]float64, nAssets)
	m := len(validIdx)
	if m == 0 {
		u := 1.0 / float64(nAssets)
		for i := range out {
			out[i] = u
		}
		return out
	}
	if m == 1 {
		out[validIdx[0]] = 1.0
		return out
	}

	// Build M x M sample covariance matrix on active columns (matching np.cov(r_clean, rowvar=False, ddof=1)).
	cov := make([]float64, m*m)
	denom := float64(tLen - 1)
	for a := 0; a < m; a++ {
		ja := validIdx[a]
		ca := rets[ja]
		muA := means[ja]
		for b := a; b < m; b++ {
			jb := validIdx[b]
			cb := rets[jb]
			muB := means[jb]
			var dot float64
			for t := 0; t < tLen; t++ {
				va, vb := ca[t], cb[t]
				if math.IsNaN(va) || math.IsInf(va, 0) {
					va = 0
				}
				if math.IsNaN(vb) || math.IsInf(vb, 0) {
					vb = 0
				}
				dot += (va - muA) * (vb - muB)
			}
			v := dot / denom
			cov[a*m+b] = v
			cov[b*m+a] = v
		}
	}

	diagSD := make([]float64, m)
	for a := 0; a < m; a++ {
		v := cov[a*m+a]
		if v < 1e-24 {
			v = 1e-24
		}
		diagSD[a] = math.Sqrt(v)
	}

	// Single-linkage agglomerative tree over 2*m - 1 nodes.
	// Cluster nodes are numbered 0..m-1 for singletons and m..2*m-2 for merges.
	totalNodes := 2*m - 1
	leftChild := make([]int, totalNodes)
	rightChild := make([]int, totalNodes)
	active := make([]bool, totalNodes)
	distMat := make([]float64, totalNodes*totalNodes)
	for i := 0; i < m; i++ {
		leftChild[i], rightChild[i] = -1, -1
		active[i] = true
		for j := i + 1; j < m; j++ {
			rho := cov[i*m+j] / (diagSD[i] * diagSD[j])
			if math.IsNaN(rho) {
				rho = 0
			} else if rho > 1.0 {
				rho = 1.0
			} else if rho < -1.0 {
				rho = -1.0
			}
			d := 0.5 * (1.0 - rho)
			if d < 0 {
				d = 0
			}
			dij := math.Sqrt(d)
			distMat[i*totalNodes+j] = dij
			distMat[j*totalNodes+i] = dij
		}
	}

	for step := 0; step < m-1; step++ {
		newNode := m + step
		bestI, bestJ := -1, -1
		bestD := math.Inf(1)
		for i := 0; i < newNode; i++ {
			if !active[i] {
				continue
			}
			rowOff := i * totalNodes
			for j := i + 1; j < newNode; j++ {
				if !active[j] {
					continue
				}
				d := distMat[rowOff+j]
				if d < bestD {
					bestD = d
					bestI, bestJ = i, j
				}
			}
		}
		active[bestI] = false
		active[bestJ] = false
		active[newNode] = true
		leftChild[newNode] = bestI
		rightChild[newNode] = bestJ
		for k := 0; k < newNode; k++ {
			if !active[k] {
				continue
			}
			dk := distMat[bestI*totalNodes+k]
			if dj := distMat[bestJ*totalNodes+k]; dj < dk {
				dk = dj
			}
			distMat[newNode*totalNodes+k] = dk
			distMat[k*totalNodes+newNode] = dk
		}
	}

	// Quasi-diagonal leaf seriation via iterative pre-order DFS from root (2*m - 2).
	sortOrder := make([]int, 0, m)
	dfsStack := make([]int, 1, totalNodes)
	dfsStack[0] = totalNodes - 1
	for len(dfsStack) > 0 {
		node := dfsStack[len(dfsStack)-1]
		dfsStack = dfsStack[:len(dfsStack)-1]
		if node < m {
			sortOrder = append(sortOrder, node)
		} else {
			// Push right first so left is popped first.
			dfsStack = append(dfsStack, rightChild[node], leftChild[node])
		}
	}

	// Recursive top-down inverse-variance bisection (iterative BFS queue over index slices).
	wSub := make([]float64, m)
	for i := range wSub {
		wSub[i] = 1.0
	}
	ivpBuf := make([]float64, m)
	clusterVar := func(items []int) float64 {
		var sumMean, ivpSum float64
		for k, idx := range items {
			dv := cov[idx*m+idx]
			if dv < 1e-18 {
				dv = 1e-18
			}
			sumMean += dv
			iv := 1.0 / dv
			ivpBuf[k] = iv
			ivpSum += iv
		}
		for k := range items {
			ivpBuf[k] /= ivpSum
		}
		var qv float64
		for a, ia := range items {
			wa := ivpBuf[a]
			rowOff := ia * m
			for b, ib := range items {
				qv += wa * cov[rowOff+ib] * ivpBuf[b]
			}
		}
		meanDiag := sumMean / float64(len(items))
		if meanDiag < 1e-12 {
			meanDiag = 1e-12
		}
		scaleFloor := 1e-12 * meanDiag
		if qv < scaleFloor {
			return scaleFloor
		}
		return qv
	}

	type clusterSpan struct{ l, r int }
	queue := []clusterSpan{{0, m}}
	for len(queue) > 0 {
		nextQ := queue[:0]
		var expanded []clusterSpan
		for _, sp := range queue {
			if sp.r-sp.l <= 1 {
				continue
			}
			mid := sp.l + (sp.r-sp.l)/2
			left := sortOrder[sp.l:mid]
			right := sortOrder[mid:sp.r]
			varL := clusterVar(left)
			varR := clusterVar(right)
			denomV := varL + varR
			alpha := 0.5
			if denomV > 0 {
				alpha = 1.0 - varL/denomV
			}
			for _, idx := range left {
				wSub[idx] *= alpha
			}
			for _, idx := range right {
				wSub[idx] *= 1.0 - alpha
			}
			if mid-sp.l > 1 {
				expanded = append(expanded, clusterSpan{sp.l, mid})
			}
			if sp.r-mid > 1 {
				expanded = append(expanded, clusterSpan{mid, sp.r})
			}
		}
		_ = nextQ
		queue = expanded
	}

	var totW float64
	for _, v := range wSub {
		totW += v
	}
	if totW <= 0 {
		u := 1.0 / float64(nAssets)
		for i := range out {
			out[i] = u
		}
		return out
	}
	for a, origIdx := range validIdx {
		out[origIdx] = wSub[a] / totW
	}
	return out
}

// LoadEquityCSVs reads daily OHLC CSVs (<dir>/<TICKER>.csv) into per-ticker Day slices sorted by YYYY-MM-DD.
// Missing ticker files are skipped without failing the remaining tickers.
func LoadEquityCSVs(dir string, tickers []string) (map[string][]Day, error) {
	out := make(map[string][]Day, len(tickers))
	if strings.TrimSpace(dir) == "" {
		return out, nil
	}
	for _, rawSym := range tickers {
		sym := strings.ToUpper(strings.TrimSpace(rawSym))
		if sym == "" {
			continue
		}
		path := filepath.Join(dir, sym+".csv")
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return out, err
		}
		r := csv.NewReader(bufio.NewReaderSize(f, 32<<10))
		hdr, herr := r.Read()
		if herr != nil {
			_ = f.Close()
			if herr == io.EOF {
				continue
			}
			return out, herr
		}
		colIdx := map[string]int{"date": 0, "open": 1, "high": 2, "low": 3, "close": 4}
		for i, h := range hdr {
			k := strings.ToLower(strings.TrimSpace(h))
			if _, ok := colIdx[k]; ok {
				colIdx[k] = i
			}
		}
		dCol, oCol, hCol, lCol, cCol := colIdx["date"], colIdx["open"], colIdx["high"], colIdx["low"], colIdx["close"]
		var days []Day
		for {
			rec, rerr := r.Read()
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				break
			}
			if len(rec) <= cCol || len(rec) <= dCol {
				continue
			}
			dateStr := strings.TrimSpace(rec[dCol])
			if len(dateStr) >= 10 {
				dateStr = dateStr[:10]
			}
			if len(dateStr) != 10 {
				continue
			}
			cVal, perr := strconv.ParseFloat(strings.TrimSpace(rec[cCol]), 64)
			if perr != nil || math.IsNaN(cVal) || math.IsInf(cVal, 0) || cVal <= 0 {
				continue
			}
			d := Day{Date: dateStr, Close: cVal}
			if oCol < len(rec) {
				if v, err := strconv.ParseFloat(strings.TrimSpace(rec[oCol]), 64); err == nil && v > 0 {
					d.Open = v
				}
			}
			if hCol < len(rec) {
				if v, err := strconv.ParseFloat(strings.TrimSpace(rec[hCol]), 64); err == nil && v > 0 {
					d.High = v
				}
			}
			if lCol < len(rec) {
				if v, err := strconv.ParseFloat(strings.TrimSpace(rec[lCol]), 64); err == nil && v > 0 {
					d.Low = v
				}
			}
			days = append(days, d)
		}
		_ = f.Close()
		if len(days) == 0 {
			continue
		}
		sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })
		dedup := make([]Day, 0, len(days))
		for _, d := range days {
			if len(dedup) > 0 && dedup[len(dedup)-1].Date == d.Date {
				dedup[len(dedup)-1] = d
			} else {
				dedup = append(dedup, d)
			}
		}
		out[sym] = dedup
	}
	return out, nil
}

// SimulateMacroHRPSleeve evaluates the causal 12-asset Macro/Equities HRP + Cross-Sectional + MR
// strategy matching hrp_hurst_alpha.py:_simulate_hrp_equities_sleeve and extracts live target weights.
func SimulateMacroHRPSleeve(series map[string][]Day, dtb3ByDate map[string]float64) (MacroSleeveSummary, bool) {
	syms := make([]string, 0, len(series))
	for _, s := range MacroEquityTickers {
		if len(series[s]) >= 65 {
			syms = append(syms, s)
		}
	}
	for s, arr := range series {
		if len(arr) < 65 {
			continue
		}
		found := false
		for _, known := range syms {
			if known == s {
				found = true
				break
			}
		}
		if !found {
			syms = append(syms, s)
		}
	}
	if len(syms) == 0 {
		return MacroSleeveSummary{}, false
	}

	// Align on reference calendar ("SPY" if present, else longest series, matching yearly_goal_loop.py).
	refSym := "SPY"
	if len(series[refSym]) < 65 {
		refSym = syms[0]
		for _, s := range syms[1:] {
			if len(series[s]) > len(series[refSym]) {
				refSym = s
			}
		}
	}
	refDays := series[refSym]
	dates := make([]string, len(refDays))
	for i, d := range refDays {
		dates[i] = d.Date
	}
	nBars := len(dates)
	if nBars < 65 {
		return MacroSleeveSummary{}, false
	}
	nAssets := len(syms)

	px := make([][]float64, nAssets)
	rets := make([][]float64, nAssets)
	vol20 := make([][]float64, nAssets)
	mom := make([][]float64, nAssets)
	trPos := make([][]float64, nAssets)
	r5z := make([][]float64, nAssets)
	sqrt252 := math.Sqrt(252.0)

	for a, sym := range syms {
		arr := series[sym]
		byDate := make(map[string]float64, len(arr))
		for _, d := range arr {
			byDate[d.Date] = d.Close
		}
		p := make([]float64, nBars)
		for t, dt := range dates {
			if v, ok := byDate[dt]; ok {
				p[t] = v
			} else {
				p[t] = math.NaN()
			}
		}
		px[a] = p

		// Causal trend position computed on each ticker's own native daily closes (matching precompute_signals).
		nativeByDate := make(map[string]float64, len(arr))
		for k := range arr {
			sub := arr[:k+1]
			dual := 0.5 * (coherenceShare(sub, 50) + r3CoherenceShare(sub, 50, 20))
			nativeByDate[arr[k].Date] = dual
		}
		tp := make([]float64, nBars)
		for t, dt := range dates {
			if v, ok := nativeByDate[dt]; ok {
				tp[t] = v
			}
		}
		trPos[a] = tp

		r := make([]float64, nBars)
		for t := 1; t < nBars; t++ {
			if p[t] > 0 && p[t-1] > 0 {
				r[t] = p[t]/p[t-1] - 1.0
			}
		}
		rets[a] = r

		// Rolling 20d vol (min_periods=10) * sqrt(252).
		v20 := make([]float64, nBars)
		for t := 0; t < nBars; t++ {
			v20[t] = math.NaN()
			if t < 9 {
				continue
			}
			wStart := t - 19
			if wStart < 0 {
				wStart = 0
			}
			cnt := t - wStart + 1
			if cnt < 10 {
				continue
			}
			var s1, s2 float64
			for k := wStart; k <= t; k++ {
				s1 += r[k]
				s2 += r[k] * r[k]
			}
			fc := float64(cnt)
			mu := s1 / fc
			vr := (s2 - fc*mu*mu) / (fc - 1.0)
			if vr < 0 {
				vr = 0
			}
			v20[t] = math.Sqrt(vr) * sqrt252
		}
		vol20[a] = v20

		// Blended momentum ((m7 + m28 + m60)/3) / clip(vol20, 0.10).
		mm := make([]float64, nBars)
		r5 := make([]float64, nBars)
		rz := make([]float64, nBars)
		for t := 0; t < nBars; t++ {
			mm[t] = math.NaN()
			r5[t] = math.NaN()
			rz[t] = math.NaN()
			if t >= 5 && p[t] > 0 && p[t-5] > 0 {
				r5[t] = p[t]/p[t-5] - 1.0
			}
			if t >= 60 && p[t] > 0 && p[t-7] > 0 && p[t-28] > 0 && p[t-60] > 0 && !math.IsNaN(v20[t]) {
				m7 := p[t]/p[t-7] - 1.0
				m28 := p[t]/p[t-28] - 1.0
				m60 := p[t]/p[t-60] - 1.0
				vd := v20[t]
				if vd < 0.10 {
					vd = 0.10
				}
				mm[t] = ((m7 + m28 + m60) / 3.0) / vd
			}
		}
		// 60d rolling z-score of r5 (min_periods=20).
		for t := 0; t < nBars; t++ {
			if math.IsNaN(r5[t]) {
				continue
			}
			wStart := t - 59
			if wStart < 0 {
				wStart = 0
			}
			var s1, s2 float64
			var cnt int
			for k := wStart; k <= t; k++ {
				if !math.IsNaN(r5[k]) {
					s1 += r5[k]
					s2 += r5[k] * r5[k]
					cnt++
				}
			}
			if cnt >= 20 {
				fc := float64(cnt)
				mu := s1 / fc
				vr := (s2 - fc*mu*mu) / (fc - 1.0)
				if vr < 0 {
					vr = 0
				}
				sd := math.Sqrt(vr)
				if sd < 1e-4 {
					sd = 1e-4
				}
				rz[t] = (r5[t] - mu) / sd
			}
		}
		mom[a] = mm
		r5z[a] = rz
	}

	// Build daily risk-free DTB3 forward-filled series (/100/252).
	dailyRF := make([]float64, nBars)
	var latestRate float64
	if len(dtb3ByDate) > 0 {
		var minD string
		for d, r := range dtb3ByDate {
			if d <= dates[0] && d > minD {
				minD = d
				latestRate = r
			}
		}
	}
	for t, dt := range dates {
		if r, ok := dtb3ByDate[dt]; ok {
			latestRate = r
		}
		dailyRF[t] = (latestRate / 100.0) / 252.0
	}

	const (
		hrpWindow   = 60
		topK        = 3
		lftWeight   = 0.50
		xsWeight    = 0.40
		mrWeight    = 0.10
		rebalBuffer = 0.03
		costRate    = 1.5e-4
	)

	eq, peak, maxDD, sumDD, sumDD2 := 1.0, 1.0, 0.0, 0.0, 0.0
	prevW := make([]float64, nAssets)
	heldW := make([]float64, nAssets)
	outRets := make([]float64, nBars)
	hrpWinBuf := make([][]float64, nAssets)

	type scoreItem struct {
		mom float64
		idx int
		vol float64
	}
	scored := make([]scoreItem, 0, nAssets)
	targetW := make([]float64, nAssets)
	lastMom := make([]float64, nAssets)
	lastVol := make([]float64, nAssets)
	lastTr := make([]float64, nAssets)

	for t := 0; t < nBars; t++ {
		var turn, gross, pnl float64
		for a := 0; a < nAssets; a++ {
			turn += math.Abs(heldW[a] - prevW[a])
			gross += math.Abs(heldW[a])
			pnl += heldW[a] * rets[a][t]
		}
		idle := math.Max(0.0, 1.0-gross)
		var bills float64
		if t > 0 {
			bills = idle * dailyRF[t-1]
		}
		totR := pnl - turn*costRate + bills
		outRets[t] = totR

		eq *= 1.0 + totR
		if eq < 1e-12 {
			eq = 1e-12
		}
		if eq > peak {
			peak = eq
		}
		dd := math.Max(0.0, 1.0-eq/peak)
		if dd > maxDD {
			maxDD = dd
		}
		sumDD += dd
		sumDD2 += dd * dd
		dm := softTaper(dd, 0.08, 0.35, 0.35)

		scored = scored[:0]
		for a := 0; a < nAssets; a++ {
			mVal := mom[a][t]
			vVal := vol20[a][t]
			if t == nBars-1 {
				lastMom[a], lastVol[a], lastTr[a] = mVal, vVal, trPos[a][t]
			}
			if !math.IsNaN(mVal) && !math.IsInf(mVal, 0) && !math.IsNaN(vVal) && !math.IsInf(vVal, 0) && vVal > 0.05 {
				scored = append(scored, scoreItem{mom: mVal, idx: a, vol: vVal})
			}
		}
		sort.SliceStable(scored, func(i, j int) bool {
			return scored[i].mom > scored[j].mom
		})

		for a := 0; a < nAssets; a++ {
			targetW[a] = 0
		}
		if len(scored) > 0 {
			var totIV float64
			invV := make([]float64, nAssets)
			isActive := make([]bool, nAssets)
			for _, sc := range scored {
				vd := sc.vol
				if vd < 0.15 {
					vd = 0.15
				}
				iv := 1.0 / vd
				invV[sc.idx] = iv
				totIV += iv
				isActive[sc.idx] = true
			}

			wHRP := make([]float64, nAssets)
			if t >= 20 {
				wStart := t - hrpWindow + 1
				if wStart < 0 {
					wStart = 0
				}
				for a := 0; a < nAssets; a++ {
					hrpWinBuf[a] = rets[a][wStart : t+1]
				}
				wh := hrpWeightsN(hrpWinBuf)
				var sAct float64
				for a := 0; a < nAssets; a++ {
					if isActive[a] {
						sAct += wh[a]
					}
				}
				if sAct > 1e-12 {
					for a := 0; a < nAssets; a++ {
						if isActive[a] {
							wHRP[a] = wh[a] / sAct
						}
					}
				} else {
					for a := 0; a < nAssets; a++ {
						if isActive[a] {
							wHRP[a] = invV[a] / totIV
						}
					}
				}
			} else {
				for a := 0; a < nAssets; a++ {
					if isActive[a] {
						wHRP[a] = invV[a] / totIV
					}
				}
			}

			wLFT := make([]float64, nAssets)
			var lftG float64
			for _, sc := range scored {
				if sc.mom > 0.0 {
					v := wHRP[sc.idx] * trPos[sc.idx][t]
					wLFT[sc.idx] = v
					lftG += v
				}
			}
			if lftG > 1.0 {
				for a := 0; a < nAssets; a++ {
					wLFT[a] /= lftG
				}
			}

			wXS := make([]float64, nAssets)
			var ivTop float64
			lim := topK
			if len(scored) < lim {
				lim = len(scored)
			}
			for _, sc := range scored[:lim] {
				if sc.mom > 0.0 {
					ivTop += invV[sc.idx]
				}
			}
			if ivTop > 0 {
				for _, sc := range scored[:lim] {
					if sc.mom > 0.0 {
						wXS[sc.idx] = invV[sc.idx] / ivTop
					}
				}
			}

			wMR := make([]float64, nAssets)
			mrCount := 0
			for _, sc := range scored {
				rzv := r5z[sc.idx][t]
				if !math.IsNaN(rzv) && rzv < -1.5 && trPos[sc.idx][t] > 0.25 && sc.mom > -0.2 {
					mrCount++
				}
			}
			if mrCount > 0 {
				mrW := 1.0 / float64(mrCount)
				for _, sc := range scored {
					rzv := r5z[sc.idx][t]
					if !math.IsNaN(rzv) && rzv < -1.5 && trPos[sc.idx][t] > 0.25 && sc.mom > -0.2 {
						wMR[sc.idx] = mrW
					}
				}
			}

			for _, sc := range scored {
				a := sc.idx
				val := lftWeight*wLFT[a] + xsWeight*wXS[a] + mrWeight*wMR[a]
				if val < 0 {
					val = 0
				}
				targetW[a] = val * dm
			}
		}

		for a := 0; a < nAssets; a++ {
			prevW[a] = heldW[a]
			if math.Abs(targetW[a]-heldW[a]) >= rebalBuffer || targetW[a] == 0.0 {
				heldW[a] = targetW[a]
			}
		}
	}

	var rSum, rSum2 float64
	for _, r := range outRets {
		rSum += r
		rSum2 += r * r
	}
	fn := float64(nBars)
	meanR := rSum / fn
	varR := (rSum2 - fn*meanR*meanR) / (fn - 1.0)
	sharpeVal := 0.0
	if varR > 0 {
		sharpeVal = (meanR / math.Sqrt(varR)) * sqrt252
	}
	cagr := 0.0
	if eq > 0 {
		cagr = (math.Pow(eq, 252.0/fn) - 1.0) * 100.0
	}
	calmar := 0.0
	if maxDD > 1e-4 {
		calmar = cagr / (maxDD * 100.0)
	}

	var investedSum float64
	targets := make([]MacroAssetTarget, 0, nAssets)
	for a, sym := range syms {
		wPct := heldW[a] * 100.0
		investedSum += wPct
		mVal := lastMom[a]
		if math.IsNaN(mVal) {
			mVal = 0
		}
		vVal := lastVol[a]
		if math.IsNaN(vVal) {
			vVal = 0
		}
		targets = append(targets, MacroAssetTarget{
			Symbol:    sym,
			Role:      macroAssetRole(sym),
			TargetPct: round2(wPct),
			LastClose: math.Round(px[a][nBars-1]*100) / 100,
			Momentum:  math.Round(mVal*1000) / 1000,
			Vol20Pct:  round2(vVal * 100.0),
			TrendPos:  round2(lastTr[a]),
		})
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].TargetPct != targets[j].TargetPct {
			return targets[i].TargetPct > targets[j].TargetPct
		}
		return targets[i].Symbol < targets[j].Symbol
	})

	return MacroSleeveSummary{
		LastDate:      dates[nBars-1],
		Days:          nBars,
		TickersLoaded: nAssets,
		Equity:        math.Round(eq*10000) / 10000,
		CAGR:          round2(cagr),
		MaxDD:         round2(maxDD * 100.0),
		MeanDD:        round2((sumDD / fn) * 100.0),
		UlcerIndex:    round2(math.Sqrt(sumDD2/fn) * 100.0),
		Sharpe:        math.Round(sharpeVal*1000) / 1000,
		Calmar:        math.Round(calmar*1000) / 1000,
		InvestedPct:   round2(investedSum),
		IdleSGOVPct:   round2(math.Max(0, 100.0-investedSum)),
		DailyDates:    dates,
		DailyRets:     outRets,
		Targets:       targets,
	}, true
}

func trailingAsymmetricVol(rets []float64) (float64, bool) {
	n := len(rets)
	if n < 10 {
		return 0, false
	}
	stdLast := func(lb int) float64 {
		start := n - lb
		if start < 0 {
			start = 0
		}
		cnt := n - start
		if cnt < 2 {
			return 0
		}
		var s1, s2 float64
		for i := start; i < n; i++ {
			s1 += rets[i]
			s2 += rets[i] * rets[i]
		}
		fc := float64(cnt)
		mu := s1 / fc
		v := (s2 - fc*mu*mu) / (fc - 1.0)
		if v < 0 {
			v = 0
		}
		return math.Sqrt(v)
	}
	return math.Max(stdLast(10), stdLast(20)), true
}

// ComputeQ57FullFund combines the live Q57 crypto blend with the 12-asset Macro/Equities HRP sleeve
// via PREREG53 asymmetric fast-risk-off inverse-volatility weighting.
func ComputeQ57FullFund(st *State, carryState *carry.State, dtb3ByDate map[string]float64, equitiesDir string, plan *execution.PingExecutionPlan) (FullFundSummary, bool) {
	series, err := LoadEquityCSVs(equitiesDir, MacroEquityTickers)
	if err != nil || len(series) == 0 {
		return FullFundSummary{}, false
	}
	macroSum, ok := SimulateMacroHRPSleeve(series, dtb3ByDate)
	if !ok {
		return FullFundSummary{}, false
	}

	var cryptoBlend BlendSummary
	if st != nil {
		if cb, okB := SelectBlend(st, carryState, dtb3ByDate, "q57_hrp_hurst", plan); okB {
			cryptoBlend = cb
		}
	}

	// Causal PREREG53 asymmetric fast-risk-off inverse-vol weight between Crypto Q57 and Macro HRP.
	wCrypto := 0.35
	if st != nil {
		if l57 := st.Ledgers["Q57_hrp_hurst_conformal_spot_1x"]; l57 != nil && len(l57.Days) >= 10 {
			cRets := make([]float64, len(l57.Days))
			for i, d := range l57.Days {
				cRets[i] = d.Ret
			}
			vC, okC := trailingAsymmetricVol(cRets)
			vM, okM := trailingAsymmetricVol(macroSum.DailyRets)
			if okC && okM {
				invC := 1.0 / math.Max(vC, 1e-4)
				invM := 1.0 / math.Max(vM, 1e-4)
				wCrypto = math.Max(0.20, math.Min(0.80, invC/(invC+invM)))
			}
		}
	}
	wMacro := 1.0 - wCrypto

	macroScaled := make([]MacroAssetTarget, len(macroSum.Targets))
	var macroInvested float64
	for i, t := range macroSum.Targets {
		scaled := t
		scaled.TargetPct = round2(t.TargetPct * wMacro)
		macroInvested += scaled.TargetPct
		macroScaled[i] = scaled
	}

	cryptoBTC := round2(cryptoBlend.TargetBTCPortPct * wCrypto)
	cryptoETH := round2(cryptoBlend.TargetETHPortPct * wCrypto)
	cryptoSOL := round2(cryptoBlend.TargetSOLPortPct * wCrypto)
	carryPct := round2(cryptoBlend.CarrySleevePct * wCrypto)
	cryptoIdle := round2(cryptoBlend.IdleTrendCashPct * wCrypto)
	macroIdle := round2(math.Max(0, wMacro*100.0-macroInvested))
	idleSGOV := round2(cryptoIdle + macroIdle)

	lastDate := macroSum.LastDate
	if cryptoBlend.LastDate > lastDate {
		lastDate = cryptoBlend.LastDate
	}

	return FullFundSummary{
		Name:                  "Q57_full_multi_asset_fund_1x",
		LastDate:              lastDate,
		CryptoSleeveWeightPct: round2(wCrypto * 100.0),
		MacroSleeveWeightPct:  round2(wMacro * 100.0),
		CryptoBTCPortPct:      cryptoBTC,
		CryptoETHPortPct:      cryptoETH,
		CryptoSOLPortPct:      cryptoSOL,
		CarrySleevePct:        carryPct,
		MacroInvestedPct:      round2(macroInvested),
		IdleSGOVPct:           idleSGOV,
		MacroStats:            macroSum,
		CryptoBlend:           cryptoBlend,
		MacroTargets:          macroScaled,
	}, true
}
