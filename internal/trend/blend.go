package trend

import (
	"encoding/csv"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"piquant-live/internal/carry"
	"piquant-live/internal/execution"
)

const (
	S36TrendShare = 0.70
	S36CarryShare = 0.30
)

// BlendLeg pairs a tracked trend ledger name with its capital weight inside a composite blend.
type BlendLeg struct {
	Variant string
	Weight  float64
}

// BlendSummary scores a composite Safe-QD + C1 Carry + idle DTB3 portfolio (PREREG36/PREREG42).
type BlendSummary struct {
	Name              string  `json:"name"`
	TrendVariant      string  `json:"trend_variant"`
	Days              int     `json:"days"`
	CarryMatchedDays  int     `json:"carry_matched_days"`
	Equity            float64 `json:"equity"`
	AfterTaxEquity    float64 `json:"after_tax_equity"`
	EstTaxRatePct     float64 `json:"est_tax_rate_pct"`
	MaxDD             float64 `json:"max_dd"`
	MeanDD            float64 `json:"mean_dd"`
	UlcerIndex        float64 `json:"ulcer_index"`
	CDaR95            float64 `json:"cdar_95"`
	MaxUnderwaterDays int     `json:"max_underwater_days"`
	Sharpe            float64 `json:"sharpe"`
	LastDate          string  `json:"last_date,omitempty"`
	TargetBTCPortPct  float64 `json:"target_btc_port_pct"`
	TargetETHPortPct  float64 `json:"target_eth_port_pct"`
	TargetSOLPortPct  float64 `json:"target_sol_port_pct,omitempty"`
	IdleTrendCashPct  float64 `json:"idle_trend_cash_pct"`
	CarrySleevePct    float64 `json:"carry_sleeve_pct"`
	PrevBTCPortPct    float64 `json:"prev_btc_port_pct"`
	PrevETHPortPct    float64 `json:"prev_eth_port_pct"`
	PrevSOLPortPct    float64 `json:"prev_sol_port_pct,omitempty"`
	RebalanceDeltaPct float64 `json:"rebalance_delta_pct"`
	LatestDTB3Pct     float64 `json:"latest_dtb3_pct"`
	ExecutionHint     string  `json:"execution_hint,omitempty"`
}

// LoadDTB3Rates reads a FRED DTB3 CSV (columns date,rate) into YYYY-MM-DD -> annual percentage yield.
// Returns an empty map cleanly if the file does not exist.
func LoadDTB3Rates(path string) (map[string]float64, error) {
	out := map[string]float64{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	hdr, err := r.Read()
	if err != nil {
		if err == io.EOF {
			return out, nil
		}
		return nil, err
	}
	dateCol, rateCol := 0, 1
	for i, h := range hdr {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case "date":
			dateCol = i
		case "rate", "dtb3":
			rateCol = i
		}
	}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(rec) <= dateCol || len(rec) <= rateCol {
			continue
		}
		d := strings.TrimSpace(rec[dateCol])
		val, perr := strconv.ParseFloat(strings.TrimSpace(rec[rateCol]), 64)
		if d != "" && perr == nil && !math.IsNaN(val) && !math.IsInf(val, 0) {
			out[d] = val
		}
	}
	return out, nil
}

// ComputeS36Blend builds the PREREG36 composite equity/return curve combining 70% of trendVariant
// (S36_safe_qd_spot_1x or S36_safe_qd_fut_1x), DTB3 risk-free interest on idle trend cash, and
// 30% C1 carry daily returns (for any overlapping dates recorded in carryState; 0 when carry month is pending).
func ComputeS36Blend(st *State, carryState *carry.State, dtb3ByDate map[string]float64, trendVariant, outName string, plan *execution.PingExecutionPlan) (BlendSummary, bool) {
	return ComputeWeightedBlend(st, carryState, dtb3ByDate, []BlendLeg{{Variant: trendVariant, Weight: S36TrendShare}}, S36CarryShare, trendVariant, outName, plan)
}

// ComputeQ42Blends returns the two PREREG42 PASS low-expected-drawdown spot portfolios:
//   - Q42_dualspeed_60_40_carry_spot (30% Safe-QD + 30% R3_FastExit_QD + 40% C1 Carry + DTB3)
//   - Q42_balanced_50_50_carry_spot  (50% Safe-QD + 50% C1 Carry + DTB3)
func ComputeQ42Blends(st *State, carryState *carry.State, dtb3ByDate map[string]float64, plan *execution.PingExecutionPlan) []BlendSummary {
	var out []BlendSummary
	if dual, ok := ComputeWeightedBlend(st, carryState, dtb3ByDate,
		[]BlendLeg{{Variant: "Q42_safe_qd_spot_1x", Weight: 0.30}, {Variant: "Q42_r3_fastexit_qd_spot_1x", Weight: 0.30}},
		0.40, "Q42_safe_qd_spot_1x+Q42_r3_fastexit_qd_spot_1x", "Q42_dualspeed_60_40_carry_spot", plan); ok {
		out = append(out, dual)
	}
	if bal, ok := ComputeWeightedBlend(st, carryState, dtb3ByDate,
		[]BlendLeg{{Variant: "Q42_safe_qd_spot_1x", Weight: 0.50}},
		0.50, "Q42_safe_qd_spot_1x", "Q42_balanced_50_50_carry_spot", plan); ok {
		out = append(out, bal)
	}
	return out
}

// ComputeQ44Blends returns the PREREG44 1.0x..2.5x Volatility-Balanced + Buy-and-Hold Volatility Harvester portfolios:
//   - Q44_bh_vol_harvester_1x_2p5x_spot       (20% BH Vol-Harvester + 55% 1x..2.5x Safe-QD + 25% C1 Carry + DTB3)
//   - Q44_barbell_1x_bh_2p5x_dualspeed_spot   (25% BH Vol-Harvester + 25% 1x..2.5x QD + 25% 1x..2.5x R3 + 25% C1 Carry + DTB3)
func ComputeQ44Blends(st *State, carryState *carry.State, dtb3ByDate map[string]float64, plan *execution.PingExecutionPlan) []BlendSummary {
	var out []BlendSummary
	if harv, ok := ComputeWeightedBlend(st, carryState, dtb3ByDate,
		[]BlendLeg{{Variant: "Q44_bh_vol_harvester_spot_1x", Weight: 0.20}, {Variant: "Q44_qd_vol_1x_2p5x_spot", Weight: 0.55}},
		0.25, "Q44_bh_vol_harvester_spot_1x+Q44_qd_vol_1x_2p5x_spot", "Q44_bh_vol_harvester_1x_2p5x_spot", plan); ok {
		out = append(out, harv)
	}
	if barb, ok := ComputeWeightedBlend(st, carryState, dtb3ByDate,
		[]BlendLeg{
			{Variant: "Q44_bh_vol_harvester_spot_1x", Weight: 0.25},
			{Variant: "Q44_qd_vol_1x_2p5x_spot", Weight: 0.25},
			{Variant: "Q44_dualspeed_r3_1x_2p5x_spot", Weight: 0.25},
		},
		0.25, "Q44_bh_vol_harvester_spot_1x+Q44_qd_vol_1x_2p5x_spot+Q44_dualspeed_r3_1x_2p5x_spot", "Q44_barbell_1x_bh_2p5x_dualspeed_spot", plan); ok {
		out = append(out, barb)
	}
	return out
}

// ComputeQ51Blends returns the PREREG51 BTC+ETH+SOL inverse-vol-parity spot blend (60% trend + 40% C1 Carry + DTB3).
func ComputeQ51Blends(st *State, carryState *carry.State, dtb3ByDate map[string]float64, plan *execution.PingExecutionPlan) []BlendSummary {
	var out []BlendSummary
	if q51, ok := ComputeWeightedBlend(st, carryState, dtb3ByDate,
		[]BlendLeg{{Variant: "Q51_btc_eth_sol_volparity_spot", Weight: 0.60}},
		0.40, "Q51_btc_eth_sol_volparity_spot", "Q51_btc_eth_sol_volparity_spot", plan); ok {
		out = append(out, q51)
	}
	return out
}

// ComputeQ56Blends returns the PREREG56 synthesized Quantum + Garman-Klass Crash Brake + Dual-Mom + 3% Buffer spot blend.
func ComputeQ56Blends(st *State, carryState *carry.State, dtb3ByDate map[string]float64, plan *execution.PingExecutionPlan) []BlendSummary {
	var out []BlendSummary
	if q56, ok := ComputeWeightedBlend(st, carryState, dtb3ByDate,
		[]BlendLeg{{Variant: "Q56_quantum_fast_spot_1x", Weight: 0.60}},
		0.40, "Q56_quantum_fast_spot_1x", "Q56_quantum_fast_carry_spot", plan); ok {
		out = append(out, q56)
	}
	return out
}

// ComputeQ57Blends returns the PREREG57 HRP + Hurst Variogram + Pre-2024 Frozen Split-Conformal Conviction spot blend.
func ComputeQ57Blends(st *State, carryState *carry.State, dtb3ByDate map[string]float64, plan *execution.PingExecutionPlan) []BlendSummary {
	var out []BlendSummary
	if q57, ok := ComputeWeightedBlend(st, carryState, dtb3ByDate,
		[]BlendLeg{{Variant: "Q57_hrp_hurst_conformal_spot_1x", Weight: 0.60}},
		0.40, "Q57_hrp_hurst_conformal_spot_1x", "Q57_hrp_hurst_conformal_carry_spot", plan); ok {
		out = append(out, q57)
	}
	return out
}

// SelectBlend resolves one named portfolio ("q42_dualspeed", "q42_balanced", "q44_bh_harvester", "q44_barbell", "q51_volparity", "q56_quantum_fast", "q57_hrp_hurst", "s36_spot", or "s36_fut")
// into a composite BlendSummary, defaulting to Q42_dualspeed_60_40_carry_spot when available.
func SelectBlend(st *State, carryState *carry.State, dtb3ByDate map[string]float64, selector string, plan *execution.PingExecutionPlan) (BlendSummary, bool) {
	all := append(ComputeQ42Blends(st, carryState, dtb3ByDate, plan), ComputeQ44Blends(st, carryState, dtb3ByDate, plan)...)
	all = append(all, ComputeQ51Blends(st, carryState, dtb3ByDate, plan)...)
	all = append(all, ComputeQ56Blends(st, carryState, dtb3ByDate, plan)...)
	all = append(all, ComputeQ57Blends(st, carryState, dtb3ByDate, plan)...)
	findBlend := func(name string) (BlendSummary, bool) {
		for _, b := range all {
			if b.Name == name {
				return b, true
			}
		}
		return BlendSummary{}, false
	}
	switch strings.ToLower(strings.TrimSpace(selector)) {
	case "", "q42", "q42_dualspeed", "q42_dualspeed_60_40_carry_spot":
		if b, ok := findBlend("Q42_dualspeed_60_40_carry_spot"); ok {
			return b, true
		}
	case "q42_balanced", "q42_balanced_50_50_carry_spot":
		if b, ok := findBlend("Q42_balanced_50_50_carry_spot"); ok {
			return b, true
		}
	case "q44", "q44_bh_harvester", "q44_bh_vol_harvester_1x_2p5x_spot":
		if b, ok := findBlend("Q44_bh_vol_harvester_1x_2p5x_spot"); ok {
			return b, true
		}
	case "q44_barbell", "q44_barbell_1x_bh_2p5x_dualspeed_spot":
		if b, ok := findBlend("Q44_barbell_1x_bh_2p5x_dualspeed_spot"); ok {
			return b, true
		}
	case "q51", "q51_volparity", "q51_btc_eth_sol_volparity_spot":
		if b, ok := findBlend("Q51_btc_eth_sol_volparity_spot"); ok {
			return b, true
		}
	case "q56", "q56_quantum_fast", "q56_quantum_fast_carry_spot":
		if b, ok := findBlend("Q56_quantum_fast_carry_spot"); ok {
			return b, true
		}
	case "q57", "q57_hrp_hurst", "q57_hrp_conformal", "q57_hrp_hurst_conformal", "q57_hrp_hurst_conformal_carry_spot":
		if b, ok := findBlend("Q57_hrp_hurst_conformal_carry_spot"); ok {
			return b, true
		}
	case "s36", "s36_spot", "s36_qd_carry_blend_spot":
		return ComputeS36Blend(st, carryState, dtb3ByDate, "S36_safe_qd_spot_1x", "S36_qd_carry_blend_spot", plan)
	case "s36_fut", "s36_qd_carry_blend_fut":
		return ComputeS36Blend(st, carryState, dtb3ByDate, "S36_safe_qd_fut_1x", "S36_qd_carry_blend_fut", plan)
	}
	return ComputeS36Blend(st, carryState, dtb3ByDate, "S36_safe_qd_spot_1x", "S36_qd_carry_blend_spot", plan)
}

// ComputeWeightedBlend builds a composite portfolio combining weighted trend legs, DTB3 interest on idle
// trend cash, and carryShare allocated to C1 carry daily returns, reporting Expected Drawdown diagnostics.
func ComputeWeightedBlend(st *State, carryState *carry.State, dtb3ByDate map[string]float64, legs []BlendLeg, carryShare float64, trendLabel, outName string, plan *execution.PingExecutionPlan) (BlendSummary, bool) {
	if st == nil || len(legs) == 0 {
		return BlendSummary{}, false
	}
	ledgers := make([]*Ledger, len(legs))
	trendShare := 0.0
	for i, leg := range legs {
		l, ok := st.Ledgers[leg.Variant]
		if !ok || l == nil {
			return BlendSummary{}, false
		}
		ledgers[i] = l
		trendShare += leg.Weight
	}
	baseDays := ledgers[0].Days

	hint := "daily close decision (00:00 UTC); rebalance via 4x patient post-only slices"
	if plan != nil {
		hint = plan.Guidance
	}

	latestDTB3 := func() float64 {
		if r, exists := dtb3ByDate[st.LastDate]; exists {
			return r
		}
		var maxDate string
		var rate float64
		for d, r := range dtb3ByDate {
			if d > maxDate {
				maxDate = d
				rate = r
			}
		}
		return rate
	}

	if len(baseDays) == 0 {
		return BlendSummary{
			Name:             outName,
			TrendVariant:     trendLabel,
			Days:             0,
			Equity:           1,
			AfterTaxEquity:   1,
			EstTaxRatePct:    33.3,
			LastDate:         st.LastDate,
			IdleTrendCashPct: round2(trendShare * 100),
			CarrySleevePct:   round2(carryShare * 100),
			LatestDTB3Pct:    round2(latestDTB3()),
			ExecutionHint:    hint,
		}, true
	}

	extraByDate := make([]map[string]DayRec, len(legs))
	for li := 1; li < len(legs); li++ {
		m := make(map[string]DayRec, len(ledgers[li].Days))
		for _, rec := range ledgers[li].Days {
			m[rec.Date] = rec
		}
		extraByDate[li] = m
	}

	carryRet := map[string]float64{}
	if carryState != nil {
		for _, d := range carryState.Days {
			carryRet[d.Date] = d.Ret
		}
	}

	var latestRate float64
	var latestRateDate string
	firstDate := baseDays[0].Date
	for d, r := range dtb3ByDate {
		if d <= firstDate && d > latestRateDate {
			latestRateDate = d
			latestRate = r
		}
	}
	lookupRate := func(date string) float64 {
		if r, ok := dtb3ByDate[date]; ok {
			latestRate = r
			return r
		}
		return latestRate
	}

	eq := 1.0
	peak := 1.0
	maxDD := 0.0
	sumDD := 0.0
	sumDD2 := 0.0
	curUW := 0
	maxUW := 0
	dds := make([]float64, 0, len(baseDays))
	rets := make([]float64, 0, len(baseDays))
	matchedCarry := 0
	prevIdleCash := 0.0
	prevRate := 0.0
	var lastB, lastE, lastS, prevB, prevE, prevS float64

	for i, d := range baseDays {
		curRate := lookupRate(d.Date)
		cRet, hasCarry := carryRet[d.Date]
		if hasCarry {
			matchedCarry++
		}
		bills := 0.0
		if i > 0 {
			bills = prevIdleCash * (prevRate / 100.0 / 365.0)
		}

		legFactor := func(li int) float64 {
			if ledgers[li].Variant.TriAsset {
				return 1.0
			}
			return 0.5
		}
		f0 := legFactor(0)
		trendRet := legs[0].Weight * d.Ret
		weightedPos := legs[0].Weight * d.PosOut
		posB := f0 * legs[0].Weight * d.PosB * 100
		posE := f0 * legs[0].Weight * d.PosE * 100
		var posS float64
		if ledgers[0].Variant.TriAsset {
			posS = legs[0].Weight * d.PosS * 100
		}
		for li := 1; li < len(legs); li++ {
			if rec, ok := extraByDate[li][d.Date]; ok {
				fi := legFactor(li)
				trendRet += legs[li].Weight * rec.Ret
				weightedPos += legs[li].Weight * rec.PosOut
				posB += fi * legs[li].Weight * rec.PosB * 100
				posE += fi * legs[li].Weight * rec.PosE * 100
				if ledgers[li].Variant.TriAsset {
					posS += legs[li].Weight * rec.PosS * 100
				}
			}
		}

		tot := trendRet + carryShare*cRet + bills
		rets = append(rets, tot)
		eq *= 1 + tot
		if eq > peak {
			peak = eq
		}
		dd := 0.0
		if peak > 0 {
			dd = math.Max(0, 1-eq/peak)
		}
		if dd > maxDD {
			maxDD = dd
		}
		sumDD += dd
		sumDD2 += dd * dd
		dds = append(dds, dd)
		if dd > 1e-6 {
			curUW++
			if curUW > maxUW {
				maxUW = curUW
			}
		} else {
			curUW = 0
		}

		prevIdleCash = math.Max(0, trendShare-weightedPos)
		prevRate = curRate
		if i == len(baseDays)-2 {
			prevB, prevE, prevS = posB, posE, posS
		}
		if i == len(baseDays)-1 {
			lastB, lastE, lastS = posB, posE, posS
		}
	}

	n := float64(len(baseDays))
	meanDD := sumDD / n
	ulcer := math.Sqrt(sumDD2 / n)
	cdar95 := computeCDaR95(dds)
	delta := math.Abs(lastB-prevB) + math.Abs(lastE-prevE) + math.Abs(lastS-prevS)

	// Annualized Sharpe across daily blend returns.
	var sharpe float64
	if len(rets) >= 2 {
		var mean, s2 float64
		for _, r := range rets {
			mean += r
			s2 += r * r
		}
		rn := float64(len(rets))
		mu := mean / rn
		v := (s2 - rn*mu*mu) / (rn - 1)
		if v > 0 {
			sharpe = mu / math.Sqrt(v) * math.Sqrt(365)
		}
	}

	lastDate := baseDays[len(baseDays)-1].Date
	const defaultTaxRatePct = 33.3 // US Fed 24% + CA FTB 9.3% short-term reserve (internal/tax)
	afterTaxEq := eq
	if eq > 1.0 {
		afterTaxEq = 1.0 + (eq-1.0)*(1.0-defaultTaxRatePct/100.0)
	}
	return BlendSummary{
		Name:              outName,
		TrendVariant:      trendLabel,
		Days:              len(baseDays),
		CarryMatchedDays:  matchedCarry,
		Equity:            round4(eq),
		AfterTaxEquity:    round4(afterTaxEq),
		EstTaxRatePct:     defaultTaxRatePct,
		MaxDD:             round4(maxDD),
		MeanDD:            round4(meanDD),
		UlcerIndex:        round4(ulcer),
		CDaR95:            round4(cdar95),
		MaxUnderwaterDays: maxUW,
		Sharpe:            round4(sharpe),
		LastDate:          lastDate,
		TargetBTCPortPct:  round2(lastB),
		TargetETHPortPct:  round2(lastE),
		TargetSOLPortPct:  round2(lastS),
		IdleTrendCashPct:  round2(math.Max(0, trendShare*100-lastB-lastE-lastS)),
		CarrySleevePct:    round2(carryShare * 100),
		PrevBTCPortPct:    round2(prevB),
		PrevETHPortPct:    round2(prevE),
		PrevSOLPortPct:    round2(prevS),
		RebalanceDeltaPct: round2(delta),
		LatestDTB3Pct:     round2(latestRate),
		ExecutionHint:     hint,
	}, true
}

func computeCDaR95(dds []float64) float64 {
	if len(dds) == 0 {
		return 0
	}
	sorted := append([]float64(nil), dds...)
	sort.Float64s(sorted)
	// Linear-interpolated 95th percentile (matches numpy.percentile(dd, 95)), then mean of tail >= p95.
	pos := 0.95 * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	p95 := sorted[lo]
	if hi > lo {
		p95 += (pos - float64(lo)) * (sorted[hi] - sorted[lo])
	}
	var sum float64
	var cnt int
	for _, d := range sorted {
		if d >= p95 {
			sum += d
			cnt++
		}
	}
	if cnt == 0 {
		return p95
	}
	return sum / float64(cnt)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
