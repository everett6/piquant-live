package trend

import (
	"encoding/csv"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"piquant-live/internal/carry"
	"piquant-live/internal/execution"
)

const (
	S36TrendShare = 0.70
	S36CarryShare = 0.30
)

// BlendSummary scores the composite PREREG36 70% Safe-QD + 30% C1 Carry + idle DTB3 portfolio.
type BlendSummary struct {
	Name              string  `json:"name"`
	TrendVariant      string  `json:"trend_variant"`
	Days              int     `json:"days"`
	CarryMatchedDays  int     `json:"carry_matched_days"`
	Equity            float64 `json:"equity"`
	MaxDD             float64 `json:"max_dd"`
	Sharpe            float64 `json:"sharpe"`
	LastDate          string  `json:"last_date,omitempty"`
	TargetBTCPortPct  float64 `json:"target_btc_port_pct"`
	TargetETHPortPct  float64 `json:"target_eth_port_pct"`
	IdleTrendCashPct  float64 `json:"idle_trend_cash_pct"`
	CarrySleevePct    float64 `json:"carry_sleeve_pct"`
	PrevBTCPortPct    float64 `json:"prev_btc_port_pct"`
	PrevETHPortPct    float64 `json:"prev_eth_port_pct"`
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
	if st == nil {
		return BlendSummary{}, false
	}
	l, ok := st.Ledgers[trendVariant]
	if !ok || l == nil {
		return BlendSummary{}, false
	}

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

	if len(l.Days) == 0 {
		return BlendSummary{
			Name:             outName,
			TrendVariant:     trendVariant,
			Days:             0,
			Equity:           1,
			LastDate:         st.LastDate,
			IdleTrendCashPct: round2(S36TrendShare * 100),
			CarrySleevePct:   round2(S36CarryShare * 100),
			LatestDTB3Pct:    round2(latestDTB3()),
			ExecutionHint:    hint,
		}, true
	}

	carryRet := map[string]float64{}
	if carryState != nil {
		for _, d := range carryState.Days {
			carryRet[d.Date] = d.Ret
		}
	}

	var latestRate float64
	var latestRateDate string
	firstDate := l.Days[0].Date
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
	rets := make([]float64, 0, len(l.Days))
	matchedCarry := 0
	prevPosOut := 0.0
	prevRate := 0.0

	for i, d := range l.Days {
		curRate := lookupRate(d.Date)
		cRet, hasCarry := carryRet[d.Date]
		if hasCarry {
			matchedCarry++
		}
		bills := 0.0
		if i > 0 {
			idlePrev := math.Max(0, S36TrendShare*(1-prevPosOut))
			bills = idlePrev * (prevRate / 100.0 / 365.0)
		}
		tot := S36TrendShare*d.Ret + S36CarryShare*cRet + bills
		rets = append(rets, tot)
		eq *= 1 + tot
		if eq > peak {
			peak = eq
		}
		if dd := 1 - eq/peak; dd > maxDD {
			maxDD = dd
		}
		prevPosOut = d.PosOut
		prevRate = curRate
	}

	last := l.Days[len(l.Days)-1]
	prevB, prevE := 0.0, 0.0
	if len(l.Days) >= 2 {
		prev := l.Days[len(l.Days)-2]
		prevB = 0.5 * S36TrendShare * prev.PosB * 100
		prevE = 0.5 * S36TrendShare * prev.PosE * 100
	}
	curB := 0.5 * S36TrendShare * last.PosB * 100
	curE := 0.5 * S36TrendShare * last.PosE * 100
	delta := math.Abs(curB-prevB) + math.Abs(curE-prevE)

	// Annualized Sharpe across daily blend returns.
	var mean, s2 float64
	if len(rets) >= 2 {
		for _, r := range rets {
			mean += r
			s2 += r * r
		}
		n := float64(len(rets))
		mu := mean / n
		v := (s2 - n*mu*mu) / (n - 1)
		if v > 0 {
			mean = mu / math.Sqrt(v) * math.Sqrt(365)
		} else {
			mean = 0
		}
	}

	return BlendSummary{
		Name:              outName,
		TrendVariant:      trendVariant,
		Days:              len(l.Days),
		CarryMatchedDays:  matchedCarry,
		Equity:            round4(eq),
		MaxDD:             round4(maxDD),
		Sharpe:            round4(mean),
		LastDate:          last.Date,
		TargetBTCPortPct:  round2(curB),
		TargetETHPortPct:  round2(curE),
		IdleTrendCashPct:  round2(math.Max(0, S36TrendShare*100-curB-curE)),
		CarrySleevePct:    round2(S36CarryShare * 100),
		PrevBTCPortPct:    round2(prevB),
		PrevETHPortPct:    round2(prevE),
		RebalanceDeltaPct: round2(delta),
		LatestDTB3Pct:     round2(latestRate),
		ExecutionHint:     hint,
	}, true
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
