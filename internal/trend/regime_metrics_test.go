package trend

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestRegimeMetricsSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "regime_metrics.json")

	// Loading a non-existent file should succeed with empty metrics.
	m := &RegimeGateMetrics{}
	if err := m.Load(path); err != nil {
		t.Fatalf("Load missing file failed: %v", err)
	}

	// Populate confusion matrix counts and pending observations.
	m.quantumTotal.Store(5)
	m.quantumTP[0].Store(2)
	m.quantumFP[1].Store(1)
	m.quantumTN[2].Store(3)
	m.quantumFN[3].Store(1)

	m.conformalTotal.Store(7)
	m.conformalTP[1].Store(3)
	m.conformalFP[0].Store(2)
	m.conformalTN[3].Store(4)
	m.conformalFN[2].Store(1)

	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	m.pendingMu.Lock()
	m.pendingQuantum = append(m.pendingQuantum, pendingObs{
		timestamp: t0,
		gateScale: 1.0,
		gateType:  "quantum",
		histLen:   10,
	})
	m.pendingConformal = append(m.pendingConformal, pendingObs{
		timestamp: t0.Add(24 * time.Hour),
		gateScale: 0.5,
		gateType:  "conformal",
		histLen:   11,
	})
	m.pendingMu.Unlock()

	if err := m.Save(path); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Load into a fresh instance and verify all counts + pending observations match.
	m2 := &RegimeGateMetrics{}
	if err := m2.Load(path); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	snap := m2.Snapshot()
	if snap.Quantum.Total != 5 || snap.Quantum.TP[0] != 2 || snap.Quantum.FP[1] != 1 || snap.Quantum.TN[2] != 3 || snap.Quantum.FN[3] != 1 {
		t.Fatalf("quantum snapshot mismatch: %+v", snap.Quantum)
	}
	if snap.Conformal.Total != 7 || snap.Conformal.TP[1] != 3 || snap.Conformal.FP[0] != 2 || snap.Conformal.TN[3] != 4 || snap.Conformal.FN[2] != 1 {
		t.Fatalf("conformal snapshot mismatch: %+v", snap.Conformal)
	}

	m2.pendingMu.Lock()
	qLen := len(m2.pendingQuantum)
	cLen := len(m2.pendingConformal)
	var qObs, cObs pendingObs
	if qLen > 0 {
		qObs = m2.pendingQuantum[0]
	}
	if cLen > 0 {
		cObs = m2.pendingConformal[0]
	}
	m2.pendingMu.Unlock()

	if qLen != 1 || !qObs.timestamp.Equal(t0) || qObs.gateScale != 1.0 || qObs.histLen != 10 {
		t.Fatalf("pendingQuantum mismatch: len=%d obs=%+v", qLen, qObs)
	}
	if cLen != 1 || !cObs.timestamp.Equal(t0.Add(24*time.Hour)) || cObs.gateScale != 0.5 || cObs.histLen != 11 {
		t.Fatalf("pendingConformal mismatch: len=%d obs=%+v", cLen, cObs)
	}

	// Verify LabelPending labels loaded pending observations when forward history is available.
	hist := make([]Day, 50)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := range hist {
		hist[i] = Day{
			Date:  base.AddDate(0, 0, i).Format("2006-01-02"),
			Close: 100 + float64(i)*2, // steady uptrend
		}
	}
	m2.LabelPending(hist, hist, hist, 5)
	snap2 := m2.Snapshot()
	if snap2.Quantum.Total != 6 || snap2.Conformal.Total != 8 {
		t.Fatalf("expected LabelPending to increment totals to 6 and 8, got %d and %d", snap2.Quantum.Total, snap2.Conformal.Total)
	}
}

func TestRegimeMetricsBarDateAcrossCapAndUnknownGuards(t *testing.T) {
	m := &RegimeGateMetrics{}
	// Build 410 bars; after 400-bar cap, bars[0] is 2026-01-11 (index 0 of capped slice).
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	all := make([]Day, 410)
	for i := range all {
		all[i] = Day{
			Date:  base.AddDate(0, 0, i).Format("2006-01-02"),
			Close: 100 * math.Pow(1.01, float64(i)),
		}
	}
	// Decision was recorded at bar index 395 (date = all[395].Date) when len was 400 (capped).
	decisionDate := all[395].Date
	m.RecordQuantumGate(1.0, decisionDate)
	m.RecordConformalGate(1.0, decisionDate)
	// Also record an observation whose date is no longer in the capped 400-bar slice.
	m.RecordQuantumGate(1.0, "2020-01-01")
	m.RecordConformalGate(1.0, "2020-01-01")

	// Backdate timestamps so LabelPending processes them relative to last bar.
	m.pendingMu.Lock()
	for i := range m.pendingQuantum {
		m.pendingQuantum[i].timestamp = base
	}
	for i := range m.pendingConformal {
		m.pendingConformal[i].timestamp = base
	}
	m.pendingMu.Unlock()

	capped := all[10:] // 400 bars: all[10]..all[409], decisionDate is at index 385, +5 bars = 390 < 400
	m.LabelPending(capped, capped, capped, 5)
	snap := m.Snapshot()
	if snap.Quantum.Total != 1 || snap.Conformal.Total != 1 {
		t.Fatalf("expected exactly 1 labeled sample each (unknown skipped), got q=%d c=%d", snap.Quantum.Total, snap.Conformal.Total)
	}
	if snap.Quantum.TP[0] != 1 || snap.Conformal.TP[0] != 1 {
		t.Fatalf("expected Bull TP=1 for both gates, got q=%v c=%v", snap.Quantum.TP, snap.Conformal.TP)
	}

	// basketVolFrom should return 0 on non-positive / NaN / Inf closes.
	bad := append([]Day(nil), capped[:10]...)
	bad[3].Close = 0
	if vol := basketVolFrom(bad, bad, bad, 0, 0, 0, 5); vol != 0 {
		t.Fatalf("expected 0 vol for zero close, got %v", vol)
	}
}

func TestRegimeMetricsBarDateLabelingWithoutWallClock(t *testing.T) {
	m := &RegimeGateMetrics{}
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	hist := make([]Day, 10)
	for i := range hist {
		hist[i] = Day{
			Date:  base.AddDate(0, 0, i).Format("2006-01-02"),
			Close: 100 * math.Pow(1.01, float64(i)),
		}
	}

	// Record using a historical barDate without mutating obs.timestamp.
	m.RecordQuantumGate(1.0, hist[0].Date)
	m.RecordConformalGate(1.0, hist[0].Date)

	// After 4 days (< lookbackDays=5), observations must remain pending.
	m.LabelPending(hist[:5], hist[:5], hist[:5], 5)
	if snap := m.Snapshot(); snap.Quantum.Total != 0 || snap.Conformal.Total != 0 {
		t.Fatalf("labeled too early: q=%d c=%d", snap.Quantum.Total, snap.Conformal.Total)
	}

	// At exact lookbackDays=5 (2024-01-06 vs 2024-01-01), both must be labeled.
	m.LabelPending(hist[:6], hist[:6], hist[:6], 5)
	if snap := m.Snapshot(); snap.Quantum.Total != 1 || snap.Conformal.Total != 1 {
		t.Fatalf("expected 1 labeled sample each at exact lookbackDays, got q=%d c=%d", snap.Quantum.Total, snap.Conformal.Total)
	}
}
