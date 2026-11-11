package paper

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"piquant-live/internal/engine"
)

func goalWithTrades(winPct float64, n int, winBps, lossBps float64) GoalState {
	g := newGoalState(7, 60)
	wins := int(math.Round(float64(n) * winPct / 100))
	for i := 0; i < n; i++ {
		if i < wins {
			g.recordTrade(winBps/1e4*200, 200)
		} else {
			g.recordTrade(lossBps/1e4*200, 200)
		}
	}
	return g
}

var t0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func TestGoalScalesUpOnlyWithEdgeWhenBehind(t *testing.T) {
	g := goalWithTrades(70, 25, 10, -8) // positive expectancy, 70% wins
	g.evaluate(t0, 0.5, 10, 0, 8, 0, 3) // behind 7%/mo pace (expected ~2.28% at day 10)
	if g.Status != "BEHIND_PACE_SCALING_UP" || math.Abs(g.LeverageMultiplier-1.1) > 1e-9 {
		t.Fatalf("want scale-up to 1.1, got %s x%.2f", g.Status, g.LeverageMultiplier)
	}
	for i := 0; i < 20; i++ {
		g.evaluate(t0, 0.5, 10, 0, 8, 0, 3)
	}
	if g.LeverageMultiplier > goalMaxLevMult+1e-9 {
		t.Fatalf("leverage multiplier exceeded cap: %v", g.LeverageMultiplier)
	}
}

func TestGoalNeverScalesUpOnNegativeEdge(t *testing.T) {
	g := goalWithTrades(40, 25, 10, -12)
	g.evaluate(t0, -1, 10, 0.5, 8, 0, 3)
	if g.Status != "DE_RISKING_NEGATIVE_EDGE" || g.LeverageMultiplier >= 1.0 {
		t.Fatalf("want de-risk, got %s x%.2f", g.Status, g.LeverageMultiplier)
	}
}

func TestGoalDeRisksOnDrawdownEvenWithEdge(t *testing.T) {
	g := goalWithTrades(70, 25, 10, -8)
	g.evaluate(t0, -3, 10, 4.5, 8, 0, 3) // 4.5% DD > half of the 8% limit
	if g.Status != "DE_RISKING_DRAWDOWN" || math.Abs(g.LeverageMultiplier-0.75) > 1e-9 {
		t.Fatalf("want drawdown de-risk to 0.75, got %s x%.2f", g.Status, g.LeverageMultiplier)
	}
}

func TestGoalWaitsForEvidenceAndPreservesWhenMet(t *testing.T) {
	g := goalWithTrades(100, 5, 10, 0)
	g.evaluate(t0, 0.1, 10, 0, 8, 0, 1)
	if g.Status != "BEHIND_PACE_GATHERING_EVIDENCE" || g.LeverageMultiplier != 1.0 {
		t.Fatalf("want no change before evidence, got %s x%.2f", g.Status, g.LeverageMultiplier)
	}
	g.evaluate(t0, 7.5, 20, 0, 8, 0, 1)
	if g.Status != "TARGET_MET_PRESERVING" || g.LeverageMultiplier >= 1.0 {
		t.Fatalf("want preservation after target, got %s x%.2f", g.Status, g.LeverageMultiplier)
	}
}

func TestGoalTightensSelectivityBelowTargetWinRate(t *testing.T) {
	g := goalWithTrades(50, 12, 10, -8)
	for i := 0; i < 10; i++ {
		g.evaluate(t0, 0, 5, 0, 8, 0, 3)
	}
	if math.Abs(g.ConvictionAdjust-goalMaxConvAdjust) > 1e-9 {
		t.Fatalf("conviction adjust = %v, want capped at %v", g.ConvictionAdjust, goalMaxConvAdjust)
	}
	g2 := goalWithTrades(80, 12, 10, -8)
	g2.ConvictionAdjust = 0.05
	g2.evaluate(t0, 0, 5, 0, 8, 0, 2)
	if math.Abs(g2.ConvictionAdjust-0.04) > 1e-9 {
		t.Fatalf("want relaxed to 0.04 above target with few trades, got %v", g2.ConvictionAdjust)
	}
}

func TestGoalPaceAndBenchmark(t *testing.T) {
	g := newGoalState(7, 60)
	g.BenchmarkStartPrice = 70000
	g.evaluate(t0, 2, 15, 0, 8, 63000, 3)
	if math.Abs(g.ExpectedByNowPct-((math.Sqrt(1.07)-1)*100)) > 1e-6 {
		t.Fatalf("expected-by-now = %v", g.ExpectedByNowPct)
	}
	if math.Abs(g.BenchmarkReturnPct+10) > 1e-9 || math.Abs(g.ExcessReturnPct-12) > 1e-9 {
		t.Fatalf("benchmark %v excess %v", g.BenchmarkReturnPct, g.ExcessReturnPct)
	}
}

func TestLatencyMonitorStatsAndFailures(t *testing.T) {
	m := NewLatencyMonitor("wss://stream.binance.us:9443/stream?streams=x", time.Second)
	if m.addr != "stream.binance.us:9443" {
		t.Fatalf("addr = %s", m.addr)
	}
	if got := endpointAddr("wss://example.com/ws"); got != "example.com:443" {
		t.Fatalf("default port addr = %s", got)
	}
	for _, v := range []float64{10, 12, 14, 16, 18, 20, 22, 24, 26, 100} {
		m.Record(v)
	}
	s := m.Snapshot()
	if s.Samples != 10 || s.P50Ms != 20 || s.P90Ms != 26 || s.LastMs != 100 {
		t.Fatalf("unexpected snapshot %+v", s)
	}
	m2 := NewLatencyMonitor("wss://x:1", time.Second)
	m2.probe = func(ctx context.Context, addr string) (time.Duration, error) { return 0, errors.New("down") }
	for i := 0; i < 3; i++ {
		m2.probeOnce(context.Background())
	}
	if s := m2.Snapshot(); s.LastMs != failedProbeMs || s.Failures != 3 {
		t.Fatalf("unreachable venue not treated as high latency: %+v", s)
	}
}

func TestMeasuredLatencyDrivesOrderTimingAndRegime(t *testing.T) {
	cfg := DefaultDryRunConfig(t.TempDir())
	cfg.Symbols = []string{"BTCUSDT"}
	eng, err := NewDryRunEngine(cfg, engine.NewSPSCRing(64), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := NewLatencyMonitor("wss://x:1", time.Second)
	for i := 0; i < 5; i++ {
		m.Record(300)
	}
	eng.SetLatencyMonitor(m)
	if got := eng.effectiveOrderLatency(); got != 305*time.Millisecond {
		t.Fatalf("effective latency = %v, want p90+5ms", got)
	}
	for i := 0; i < 80; i++ {
		eng.evaluateQuotesAndGating("BTCUSDT", 50000, t0.Add(time.Duration(i)*time.Second))
	}
	eng.mu.Lock()
	eng.updateLatencyStateLocked()
	mode := eng.state.Latency.Mode
	eng.mu.Unlock()
	if mode != "HIGH_LATENCY_FUNDING_CARRY" {
		t.Fatalf("300ms latency should select the low-frequency regime, got %q", mode)
	}
	if p := profileForMode(mode); p.EntryOffsetMult <= 1 || p.ConvictionAdd <= 0 {
		t.Fatalf("slow regime must widen entries and raise conviction: %+v", p)
	}
}

func TestGoalStatePersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultDryRunConfig(dir)
	cfg.Symbols = []string{"BTCUSDT"}
	eng, err := NewDryRunEngine(cfg, engine.NewSPSCRing(64), nil)
	if err != nil {
		t.Fatal(err)
	}
	eng.mu.Lock()
	eng.state.Goal.LeverageMultiplier, eng.state.Goal.ConvictionAdjust = 1.3, 0.04
	eng.mu.Unlock()
	if err := eng.saveCheckpoint(); err != nil {
		t.Fatal(err)
	}
	cfg.MonthlyTargetPct = 8
	eng2, err := NewDryRunEngine(cfg, engine.NewSPSCRing(64), nil)
	if err != nil {
		t.Fatal(err)
	}
	g := eng2.State().Goal
	if g.LeverageMultiplier != 1.3 || g.ConvictionAdjust != 0.04 || g.MonthlyTargetPct != 8 {
		t.Fatalf("goal state not restored: %+v", g)
	}
}

func TestGoalNoReRiskAfterDeRiskAndGradualRecovery(t *testing.T) {
	g := newGoalState(7, 60)
	g.LeverageMultiplier = 0.25
	g.evaluate(t0, 8, 20, 0, 8, 0, 1) // target met right after a de-risk
	if g.LeverageMultiplier != 0.25 {
		t.Fatalf("target-met must not raise leverage after de-risk, got %v", g.LeverageMultiplier)
	}
	g.evaluate(t0, 0.1, 10, 0, 8, 0, 1) // behind, no evidence: recover slowly
	if math.Abs(g.LeverageMultiplier-0.30) > 1e-9 {
		t.Fatalf("want gradual recovery to 0.30, got %v", g.LeverageMultiplier)
	}
	g2 := newGoalState(7, 60)
	g2.evaluate(t0, 0.1, 1.0/24, 0, 8, 0, 1)
	if g2.ProjectedMonthlyPct != 0 {
		t.Fatalf("projection after 1h must be 0, got %v", g2.ProjectedMonthlyPct)
	}
}

func TestDisableCloudParamsIgnoresOverrides(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultDryRunConfig(dir)
	cfg.Symbols = []string{"BTCUSDT"}
	cfg.DisableCloudParams = true
	eng, err := NewDryRunEngine(cfg, engine.NewSPSCRing(64), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "optimal_params.json"), []byte(`{"min_half_spread_bps": 7.5}`), 0o644); err != nil {
		t.Fatal(err)
	}
	before := eng.minHalfSpreadFloorBps
	eng.maybeReloadCloudParams()
	if eng.minHalfSpreadFloorBps != before {
		t.Fatalf("cloud params applied despite DisableCloudParams: %v", eng.minHalfSpreadFloorBps)
	}
}
