package paper

import "math"

// ExitStats accumulates net round-trip results (bps of exit notional) for one exit reason.
type ExitStats struct {
	N         int64   `json:"n"`
	Wins      int64   `json:"wins"`
	SumBps    float64 `json:"sum_bps"`
	SumSqBps  float64 `json:"sum_sq_bps"`
	SumWinBps float64 `json:"sum_win_bps"`
	SumLosBps float64 `json:"sum_loss_bps"`
	MeanBps   float64 `json:"mean_bps"`
}

func (s *ExitStats) add(bps float64) {
	s.N++
	s.SumBps += bps
	s.SumSqBps += bps * bps
	if bps > 0 {
		s.Wins++
		s.SumWinBps += bps
	} else {
		s.SumLosBps += bps
	}
	s.MeanBps = s.SumBps / float64(s.N)
}

// ExpectancyState is the live version of BREAKEVEN.md: profit needs mean net bps per trade > 0, and the
// realised win rate must beat p* = |avg loss| / (avg win + |avg loss|) computed from this run's own trades.
type ExpectancyState struct {
	ByReason            map[string]*ExitStats `json:"by_reason"`
	All                 ExitStats             `json:"all"`
	StdErrBps           float64               `json:"stderr_bps"`
	Upper95Bps          float64               `json:"upper95_bps"` // mean + 1.96 * stderr
	WinRatePct          float64               `json:"win_rate_pct"`
	AvgWinBps           float64               `json:"avg_win_bps"`
	AvgLossBps          float64               `json:"avg_loss_bps"`
	BreakevenWinRatePct float64               `json:"breakeven_win_rate_pct"`
	EdgeVsBreakevenPct  float64               `json:"edge_vs_breakeven_pct"` // win rate minus breakeven, pp
}

// Record adds one closed round trip. pnlUSDT includes entry and exit fees.
func (x *ExpectancyState) Record(reason string, pnlUSDT, notionalUSDT float64) {
	if notionalUSDT <= 0 || math.IsNaN(pnlUSDT) || math.IsInf(pnlUSDT, 0) || math.IsNaN(notionalUSDT) {
		return
	}
	if reason == "" {
		reason = "OTHER"
	}
	bps := pnlUSDT / notionalUSDT * 1e4
	if x.ByReason == nil {
		x.ByReason = make(map[string]*ExitStats)
	}
	r := x.ByReason[reason]
	if r == nil {
		r = &ExitStats{}
		x.ByReason[reason] = r
	}
	r.add(bps)
	x.All.add(bps)

	a := &x.All
	n := float64(a.N)
	x.WinRatePct = float64(a.Wins) / n * 100
	x.StdErrBps = 0
	if a.N > 1 {
		v := (a.SumSqBps - n*a.MeanBps*a.MeanBps) / (n - 1)
		x.StdErrBps = math.Sqrt(math.Max(v, 0) / n)
	}
	x.Upper95Bps = a.MeanBps + 1.96*x.StdErrBps
	x.AvgWinBps, x.AvgLossBps = 0, 0
	if a.Wins > 0 {
		x.AvgWinBps = a.SumWinBps / float64(a.Wins)
	}
	if losses := a.N - a.Wins; losses > 0 {
		x.AvgLossBps = a.SumLosBps / float64(losses)
	}
	x.BreakevenWinRatePct, x.EdgeVsBreakevenPct = 0, 0
	if x.AvgWinBps > 0 && x.AvgLossBps < 0 {
		x.BreakevenWinRatePct = -x.AvgLossBps / (x.AvgWinBps - x.AvgLossBps) * 100
		x.EdgeVsBreakevenPct = x.WinRatePct - x.BreakevenWinRatePct
	}
}

// NegativeEdgeConfirmed reports whether, after at least minTrades closes, even the upper 95% bound of mean
// net bps per trade is below zero (minTrades <= 0 disables the check).
func (x *ExpectancyState) NegativeEdgeConfirmed(minTrades int) bool {
	return minTrades > 0 && x.All.N >= int64(minTrades) && x.Upper95Bps < 0
}
