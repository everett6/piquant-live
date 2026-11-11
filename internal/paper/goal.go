package paper

import (
	"fmt"
	"math"
	"time"
)

// GoalState is the self-adjusting controller that steers the run toward a monthly
// return target. It never raises leverage without measured edge: scaling up requires
// a positive rolling expectancy over enough trades, and any drawdown or negative edge
// forces de-risking. A return target cannot be guaranteed; the controller reports
// honestly when the strategy is behind pace without edge.
type GoalState struct {
	MonthlyTargetPct     float64   `json:"monthly_target_pct"`
	TargetWinRatePct     float64   `json:"target_win_rate_pct"`
	ExpectedByNowPct     float64   `json:"expected_by_now_pct"`  // compounded target pace at this point of the run
	ReturnPct            float64   `json:"return_pct"`           // actual return since start
	PaceGapPct           float64   `json:"pace_gap_pct"`         // actual - expected (negative = behind)
	ProjectedMonthlyPct  float64   `json:"projected_monthly_pct"` // current return extrapolated to 30 days
	LeverageMultiplier   float64   `json:"leverage_multiplier"`  // applied on top of dynamic leverage (capped by max)
	ConvictionAdjust     float64   `json:"conviction_adjust"`    // added to buy / subtracted from sell thresholds
	RollingTrades        int       `json:"rolling_trades"`
	RollingWinRatePct    float64   `json:"rolling_win_rate_pct"`
	RollingExpectancyBps float64   `json:"rolling_expectancy_bps"` // mean round-trip PnL in bps of equity
	BenchmarkSymbol      string    `json:"benchmark_symbol,omitempty"`
	BenchmarkStartPrice  float64   `json:"benchmark_start_price,omitempty"`
	BenchmarkReturnPct   float64   `json:"benchmark_return_pct"` // buy-and-hold over the same period
	ExcessReturnPct      float64   `json:"excess_return_pct"`    // strategy - benchmark
	Status               string    `json:"status"`
	Adjustments          int       `json:"adjustments"`
	LastEvaluation       time.Time `json:"last_evaluation"`
	RecentTradeBps       []float64 `json:"recent_trade_bps,omitempty"`
}

// Goal controller limits.
const (
	goalWindow          = 40
	goalMinTradesLev    = 20 // trades needed before leverage may be raised
	goalMinTradesSelect = 10 // trades needed before selectivity is tuned
	goalEvalEvery       = 15 * time.Minute
	goalMinLevMult      = 0.25
	goalMaxLevMult      = 1.5
	goalMaxConvAdjust   = 0.12
	goalMinEdgeWinRate  = 55.0
)

func newGoalState(monthlyTargetPct, targetWinRatePct float64) GoalState {
	if monthlyTargetPct <= 0 {
		monthlyTargetPct = 7.0
	}
	if targetWinRatePct <= 0 {
		targetWinRatePct = 60.0
	}
	return GoalState{MonthlyTargetPct: monthlyTargetPct, TargetWinRatePct: targetWinRatePct, LeverageMultiplier: 1.0, Status: "WARMING_UP"}
}

// recoverTowardNeutral slowly restores a de-risked multiplier toward 1.0 once the
// drawdown and negative-edge conditions have cleared (never above 1.0 here).
func (g *GoalState) recoverTowardNeutral() {
	if g.LeverageMultiplier < 1.0 {
		g.LeverageMultiplier = math.Min(1.0, g.LeverageMultiplier+0.05)
	}
}

// recordTrade adds one round-trip result (PnL in bps of equity at close).
func (g *GoalState) recordTrade(pnlUSD, equity float64) {
	if equity <= 0 || math.IsNaN(pnlUSD) || math.IsInf(pnlUSD, 0) {
		return
	}
	g.RecentTradeBps = append(g.RecentTradeBps, pnlUSD/equity*1e4)
	if len(g.RecentTradeBps) > goalWindow {
		g.RecentTradeBps = g.RecentTradeBps[len(g.RecentTradeBps)-goalWindow:]
	}
	g.refreshRolling()
}

func (g *GoalState) refreshRolling() {
	n, wins, losses, sum := 0, 0, 0, 0.0
	for _, b := range g.RecentTradeBps {
		n++
		sum += b
		if b > 1e-6 {
			wins++
		} else if b < -1e-6 {
			losses++
		}
	}
	g.RollingTrades = n
	if n > 0 {
		g.RollingExpectancyBps = sum / float64(n)
	}
	if wins+losses > 0 {
		g.RollingWinRatePct = float64(wins) / float64(wins+losses) * 100
	}
}

// evaluate updates pace, benchmark and the leverage / selectivity adjustments.
// daysElapsed is fractional days since start; currentDDPct is the drawdown from peak;
// ddLimitPct is the circuit-breaker limit; tradesPerDay is the recent close rate.
func (g *GoalState) evaluate(now time.Time, returnPct, daysElapsed, currentDDPct, ddLimitPct, benchPrice, tradesPerDay float64) {
	g.LastEvaluation = now
	g.ReturnPct = returnPct
	if daysElapsed < 1.0/24 {
		daysElapsed = 1.0 / 24
	}
	target := g.MonthlyTargetPct / 100
	g.ExpectedByNowPct = (math.Pow(1+target, daysElapsed/30.0) - 1) * 100
	g.PaceGapPct = returnPct - g.ExpectedByNowPct
	// Compounded extrapolation explodes on tiny samples: linear under 3 days, none under 1.
	switch {
	case daysElapsed < 1:
		g.ProjectedMonthlyPct = 0
	case daysElapsed < 3:
		g.ProjectedMonthlyPct = returnPct * 30.0 / daysElapsed
	default:
		g.ProjectedMonthlyPct = (math.Pow(1+math.Max(returnPct, -99.9)/100, 30.0/daysElapsed) - 1) * 100
	}
	if math.IsInf(g.ProjectedMonthlyPct, 0) || math.IsNaN(g.ProjectedMonthlyPct) {
		g.ProjectedMonthlyPct = 0
	}
	if g.BenchmarkStartPrice > 0 && benchPrice > 0 {
		g.BenchmarkReturnPct = (benchPrice/g.BenchmarkStartPrice - 1) * 100
		g.ExcessReturnPct = returnPct - g.BenchmarkReturnPct
	}
	g.refreshRolling()

	if g.LeverageMultiplier <= 0 {
		g.LeverageMultiplier = 1.0
	}
	prevLev, prevConv := g.LeverageMultiplier, g.ConvictionAdjust
	edgeKnown := g.RollingTrades >= goalMinTradesLev
	edgePositive := edgeKnown && g.RollingExpectancyBps > 0 && g.RollingWinRatePct >= goalMinEdgeWinRate
	behind := g.PaceGapPct < 0
	if ddLimitPct <= 0 {
		ddLimitPct = 2
	}

	switch {
	case currentDDPct > 0.5*ddLimitPct:
		g.LeverageMultiplier = math.Max(goalMinLevMult, g.LeverageMultiplier-0.25)
		g.Status = "DE_RISKING_DRAWDOWN"
	case edgeKnown && g.RollingExpectancyBps < 0:
		g.LeverageMultiplier = math.Max(goalMinLevMult, g.LeverageMultiplier-0.25)
		g.Status = "DE_RISKING_NEGATIVE_EDGE"
	case returnPct >= g.MonthlyTargetPct:
		if g.LeverageMultiplier > 0.75 { // only ever lower here, never re-risk after a de-risk
			g.LeverageMultiplier = math.Max(0.75, g.LeverageMultiplier-0.10)
		}
		g.Status = "TARGET_MET_PRESERVING"
	case behind && edgePositive:
		g.LeverageMultiplier = math.Min(goalMaxLevMult, g.LeverageMultiplier+0.10)
		g.Status = "BEHIND_PACE_SCALING_UP"
	case behind && !edgeKnown:
		g.Status = "BEHIND_PACE_GATHERING_EVIDENCE"
		g.recoverTowardNeutral()
	case behind:
		g.Status = "BEHIND_PACE_NO_EDGE_HOLDING"
		g.recoverTowardNeutral()
	default:
		g.Status = "ON_PACE"
		g.recoverTowardNeutral()
	}

	// Selectivity: raise the entry bar while the win rate is under target; relax slowly
	// only when comfortably above target and trades are scarce.
	if g.RollingTrades >= goalMinTradesSelect {
		if g.RollingWinRatePct < g.TargetWinRatePct {
			g.ConvictionAdjust = math.Min(goalMaxConvAdjust, g.ConvictionAdjust+0.02)
		} else if g.RollingWinRatePct >= g.TargetWinRatePct+5 && tradesPerDay < 6 {
			g.ConvictionAdjust = math.Max(0, g.ConvictionAdjust-0.01)
		}
	}
	if g.LeverageMultiplier != prevLev || g.ConvictionAdjust != prevConv {
		g.Adjustments++
	}
}

// updateLatencyStateLocked publishes measured latency and the active regime. Caller holds e.mu.
func (e *DryRunEngine) updateLatencyStateLocked() {
	var snap LatencySnapshot
	if e.latencyMon != nil {
		snap = e.latencyMon.Snapshot()
	}
	if e.orderLatMon != nil {
		os := e.orderLatMon.Snapshot()
		snap.OrderHost, snap.OrderP90Ms = os.Host, os.P90Ms
	}
	snap.EffectiveMs = float64(e.effectiveOrderLatency().Microseconds()) / 1000.0
	if e.quantOrch != nil {
		snap.Mode = string(e.quantOrch.Report(time.Now()).CurrentMode)
	}
	e.state.Latency = snap
}

// maybeEvaluateGoalLocked runs the goal controller every goalEvalEvery. Caller holds e.mu.
func (e *DryRunEngine) maybeEvaluateGoalLocked(now time.Time) {
	g := &e.state.Goal
	if !g.LastEvaluation.IsZero() && now.Sub(g.LastEvaluation) < goalEvalEvery {
		return
	}
	benchPrice := 0.0
	if sm := e.symMicro[g.BenchmarkSymbol]; sm != nil {
		benchPrice = sm.LastMid
	}
	if g.BenchmarkStartPrice <= 0 && benchPrice > 0 {
		g.BenchmarkStartPrice = benchPrice
	}
	returnPct, ddPct := 0.0, 0.0
	if e.state.InitialCapital > 0 {
		returnPct = (e.state.Equity/e.state.InitialCapital - 1) * 100
	}
	if e.state.PeakEquity > 0 && e.state.Equity < e.state.PeakEquity {
		ddPct = (e.state.PeakEquity - e.state.Equity) / e.state.PeakEquity * 100
	}
	days := 0.0
	if !e.state.StartTime.IsZero() {
		days = now.Sub(e.state.StartTime).Hours() / 24
	}
	tradesPerDay := 0.0
	if days > 0 {
		tradesPerDay = float64(e.state.WinningTrades+e.state.LosingTrades) / days
	}
	before := *g
	g.evaluate(now, returnPct, days, ddPct, e.cfg.MaxDrawdownPct, benchPrice, tradesPerDay)
	if g.LeverageMultiplier != before.LeverageMultiplier || g.ConvictionAdjust != before.ConvictionAdjust {
		fmt.Printf("[GOAL] %s | return %+.2f%% vs pace %+.2f%% | win %.0f%% over %d | exp %+.1f bps | lev x%.2f conv %+.2f\n",
			g.Status, g.ReturnPct, g.ExpectedByNowPct, g.RollingWinRatePct, g.RollingTrades,
			g.RollingExpectancyBps, g.LeverageMultiplier, g.ConvictionAdjust)
	}
}
