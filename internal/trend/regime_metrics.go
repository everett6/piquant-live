package trend

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"piquant-live/internal/durable"
)

// RegimeLabel represents the true market regime determined ex-post.
type RegimeLabel int

const (
	RegimeUnknown  RegimeLabel = iota
	RegimeBull                 // Strong positive returns ahead
	RegimeBear                 // Strong negative returns ahead
	RegimeSideways             // Low volatility, choppy
	RegimeVolatile             // High volatility, direction unclear
)

// Scale thresholds for converting gate scale to regime prediction.
const (
	scaleBullThresh     = 0.95 // Full risk on = bullish regime expected
	scaleBearThresh     = 0.55 // Risk off = bearish regime expected
	scaleVolatileThresh = 0.75 // Partial risk = uncertain/volatile
)

// Max pending observations before emergency cleanup.
const maxPendingObs = 10000

// RegimeGateMetrics tracks precision/recall for regime detection gates.
type RegimeGateMetrics struct {
	// Quantum gate (spectral entropy z-score based)
	quantumTP [4]atomic.Uint64 // True positives per regime
	quantumFP [4]atomic.Uint64 // False positives per regime
	quantumTN [4]atomic.Uint64 // True negatives per regime
	quantumFN [4]atomic.Uint64 // False negatives per regime

	// Conformal gate (Hurst + entropy + trend strength)
	conformalTP [4]atomic.Uint64
	conformalFP [4]atomic.Uint64
	conformalTN [4]atomic.Uint64
	conformalFN [4]atomic.Uint64

	// Total samples seen
	quantumTotal   atomic.Uint64
	conformalTotal atomic.Uint64

	// Pending observations awaiting forward-return labeling
	pendingQuantum   []pendingObs
	pendingConformal []pendingObs
	pendingMu        sync.Mutex
}

type pendingObs struct {
	timestamp time.Time
	gateScale float64
	gateType  string // "quantum" or "conformal"
	barDate   string // Date of decision bar in histB
	histLen   int    // Legacy fallback when barDate is empty
}

// Global metrics instance (initialized by initMetrics in blend.go or main)
var RegimeMetrics *RegimeGateMetrics

func init() {
	RegimeMetrics = &RegimeGateMetrics{}
}

func parseBarDateOrNow(barDate string) time.Time {
	if t, err := time.Parse("2006-01-02", barDate); err == nil {
		return t
	}
	return time.Now()
}

func obsDecisionTime(obs pendingObs) time.Time {
	if obs.barDate != "" {
		if t, err := time.Parse("2006-01-02", obs.barDate); err == nil {
			return t
		}
	}
	return obs.timestamp
}

// RecordQuantumGate records a quantum regime gate decision for later labeling.
// Stores the decision bar's date to avoid index drift when history rolls past historyCap.
func (m *RegimeGateMetrics) RecordQuantumGate(scale float64, barDate string) {
	if m == nil {
		return
	}
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()

	m.pendingQuantum = append(m.pendingQuantum, pendingObs{
		timestamp: parseBarDateOrNow(barDate),
		gateScale: scale,
		gateType:  "quantum",
		barDate:   barDate,
	})

	// Emergency cleanup if pending grows too large
	if len(m.pendingQuantum) > maxPendingObs {
		// Keep only the most recent half
		keep := m.pendingQuantum[len(m.pendingQuantum)/2:]
		m.pendingQuantum = keep
	}
}

// RecordConformalGate records a conformal regime gate decision for later labeling.
func (m *RegimeGateMetrics) RecordConformalGate(scale float64, barDate string) {
	if m == nil {
		return
	}
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()

	m.pendingConformal = append(m.pendingConformal, pendingObs{
		timestamp: parseBarDateOrNow(barDate),
		gateScale: scale,
		gateType:  "conformal",
		barDate:   barDate,
	})

	if len(m.pendingConformal) > maxPendingObs {
		keep := m.pendingConformal[len(m.pendingConformal)/2:]
		m.pendingConformal = keep
	}
}

// LabelPending uses forward returns to label pending observations.
// Call this periodically (e.g., daily) with fresh history.
// histB, histE, histS: CURRENT history (includes bars after decision time)
// lookbackDays: number of days of forward returns to use for labeling
func (m *RegimeGateMetrics) LabelPending(histB, histE, histS []Day, lookbackDays int) {
	if m == nil {
		return
	}
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()

	// Use the timestamp of the LAST bar in current history as "now" for cutoff
	// This aligns with history timestamps rather than wall clock (NTP-safe)
	var historyNow time.Time
	if len(histB) > 0 {
		if t, err := time.Parse("2006-01-02", histB[len(histB)-1].Date); err == nil {
			historyNow = t
		} else {
			historyNow = time.Now()
		}
	} else {
		historyNow = time.Now()
	}
	cutoff := historyNow.Add(-time.Duration(lookbackDays) * 24 * time.Hour)

	labelQuantum := func(obs pendingObs) {
		label := inferRegimeLabelFromCurrent(histB, histE, histS, obs.barDate, obs.histLen, lookbackDays)
		if label == RegimeUnknown {
			return
		}
		gateRegime := scaleToRegime(obs.gateScale)
		if gateRegime == RegimeUnknown {
			return
		}
		m.quantumTotal.Add(1)
		updateConfusion(&m.quantumTP, &m.quantumFP, &m.quantumTN, &m.quantumFN, gateRegime, label)
	}

	labelConformal := func(obs pendingObs) {
		label := inferRegimeLabelFromCurrent(histB, histE, histS, obs.barDate, obs.histLen, lookbackDays)
		if label == RegimeUnknown {
			return
		}
		gateRegime := scaleToRegime(obs.gateScale)
		if gateRegime == RegimeUnknown {
			return
		}
		m.conformalTotal.Add(1)
		updateConfusion(&m.conformalTP, &m.conformalFP, &m.conformalTN, &m.conformalFN, gateRegime, label)
	}

	// Process quantum pending
	var remainingQuantum []pendingObs
	for _, obs := range m.pendingQuantum {
		if !obsDecisionTime(obs).After(cutoff) {
			labelQuantum(obs)
		} else {
			remainingQuantum = append(remainingQuantum, obs)
		}
	}
	m.pendingQuantum = remainingQuantum

	// Process conformal pending
	var remainingConformal []pendingObs
	for _, obs := range m.pendingConformal {
		if !obsDecisionTime(obs).After(cutoff) {
			labelConformal(obs)
		} else {
			remainingConformal = append(remainingConformal, obs)
		}
	}
	m.pendingConformal = remainingConformal
}

func findBarIndexByDate(hist []Day, date string) int {
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Date == date {
			return i
		}
	}
	return -1
}

// inferRegimeLabelFromCurrent computes regime label from FORWARD returns
// using the current (extended) history starting from the decision bar (by barDate, or histLen-1 fallback)
// to lookbackDays forward.
func inferRegimeLabelFromCurrent(histB, histE, histS []Day, barDate string, histLen, lookbackDays int) RegimeLabel {
	if lookbackDays < 1 {
		return RegimeUnknown
	}
	var idxB, idxE, idxS int
	if barDate != "" {
		idxB = findBarIndexByDate(histB, barDate)
		idxE = findBarIndexByDate(histE, barDate)
		idxS = findBarIndexByDate(histS, barDate)
		if idxB < 0 || idxE < 0 || idxS < 0 {
			return RegimeUnknown
		}
	} else {
		if histLen < 1 {
			return RegimeUnknown
		}
		idxB, idxE, idxS = histLen-1, histLen-1, histLen-1
	}
	// Need: current history has at least startIdx + lookbackDays + 1 bars
	if len(histB) <= idxB+lookbackDays || len(histE) <= idxE+lookbackDays || len(histS) <= idxS+lookbackDays {
		return RegimeUnknown
	}

	// Forward returns from decision bar to +lookbackDays
	retB := forwardReturnFrom(histB, idxB, lookbackDays)
	retE := forwardReturnFrom(histE, idxE, lookbackDays)
	retS := forwardReturnFrom(histS, idxS, lookbackDays)

	// Equal-weight basket return
	basketRet := (retB + retE + retS) / 3.0

	// Volatility of forward returns
	vol := basketVolFrom(histB, histE, histS, idxB, idxE, idxS, lookbackDays)

	const (
		bullThresh     = 0.02  // >2% over lookback = bull
		bearThresh     = -0.02 // <-2% over lookback = bear
		volatileThresh = 0.04  // >4% vol = volatile
	)

	switch {
	case basketRet > bullThresh && vol < volatileThresh:
		return RegimeBull
	case basketRet < bearThresh && vol < volatileThresh:
		return RegimeBear
	case vol > volatileThresh:
		return RegimeVolatile
	default:
		return RegimeSideways
	}
}

// forwardReturnFrom computes return from index `startIdx` to `startIdx + days`.
func forwardReturnFrom(hist []Day, startIdx, days int) float64 {
	endIdx := startIdx + days
	if endIdx >= len(hist) {
		return 0
	}
	start := hist[startIdx].Close
	end := hist[endIdx].Close
	if start <= 0 || end <= 0 || math.IsNaN(start) || math.IsNaN(end) || math.IsInf(start, 0) || math.IsInf(end, 0) {
		return 0
	}
	return end/start - 1.0
}

func validRegimeClose(c float64) bool {
	return c > 0 && !math.IsNaN(c) && !math.IsInf(c, 0)
}

// basketVolFrom computes volatility of forward returns from each asset's start index over `days` period.
func basketVolFrom(histB, histE, histS []Day, idxB, idxE, idxS, days int) float64 {
	if days < 2 || idxB < 0 || idxE < 0 || idxS < 0 {
		return 0
	}
	if idxB+days >= len(histB) || idxE+days >= len(histE) || idxS+days >= len(histS) {
		return 0
	}

	var sum, sum2 float64
	for i := 0; i < days; i++ {
		b0, e0, s0 := idxB+i, idxE+i, idxS+i
		cb0, cb1 := histB[b0].Close, histB[b0+1].Close
		ce0, ce1 := histE[e0].Close, histE[e0+1].Close
		cs0, cs1 := histS[s0].Close, histS[s0+1].Close
		if !validRegimeClose(cb0) || !validRegimeClose(cb1) ||
			!validRegimeClose(ce0) || !validRegimeClose(ce1) ||
			!validRegimeClose(cs0) || !validRegimeClose(cs1) {
			return 0
		}
		rB := cb1/cb0 - 1.0
		rE := ce1/ce0 - 1.0
		rS := cs1/cs0 - 1.0
		if math.IsNaN(rB) || math.IsNaN(rE) || math.IsNaN(rS) ||
			math.IsInf(rB, 0) || math.IsInf(rE, 0) || math.IsInf(rS, 0) {
			return 0
		}
		r := (rB + rE + rS) / 3.0
		sum += r
		sum2 += r * r
	}
	mean := sum / float64(days)
	v := (sum2 - float64(days)*mean*mean) / float64(days-1)
	if v <= 0 {
		return 0
	}
	return math.Sqrt(v)
}

// scaleToRegime converts a gate scale [0.45, 1.0] to a regime prediction.
func scaleToRegime(scale float64) RegimeLabel {
	if math.IsNaN(scale) || math.IsInf(scale, 0) {
		return RegimeUnknown
	}
	if scale >= scaleBullThresh {
		return RegimeBull
	}
	if scale <= scaleBearThresh {
		return RegimeBear
	}
	if scale <= scaleVolatileThresh {
		return RegimeVolatile
	}
	return RegimeSideways
}

func updateConfusion(tp, fp, tn, fn *[4]atomic.Uint64, predicted, actual RegimeLabel) {
	if predicted <= RegimeUnknown || actual <= RegimeUnknown || predicted > RegimeVolatile || actual > RegimeVolatile {
		return
	}
	p := int(predicted) - 1
	a := int(actual) - 1
	if p == a {
		tp[p].Add(1)
		// True negative for all other classes (0=Bull, 1=Bear, 2=Sideways, 3=Volatile)
		for i := 0; i < 4; i++ {
			if i != p {
				tn[i].Add(1)
			}
		}
	} else {
		fp[p].Add(1)
		fn[a].Add(1)
		// True negative for remaining classes
		for i := 0; i < 4; i++ {
			if i != p && i != a {
				tn[i].Add(1)
			}
		}
	}
}

// RegimeMetricsSnapshot returns a copy of current metrics for reporting.
type RegimeMetricsSnapshot struct {
	Quantum   RegimeConfusionMatrix
	Conformal RegimeConfusionMatrix
}

type RegimeConfusionMatrix struct {
	TP, FP, TN, FN [4]uint64
	Total          uint64
	Precision      [4]float64
	Recall         [4]float64
	F1             [4]float64
	Accuracy       float64
}

func (m *RegimeGateMetrics) Snapshot() RegimeMetricsSnapshot {
	if m == nil {
		return RegimeMetricsSnapshot{}
	}
	var snap RegimeMetricsSnapshot
	for i := 0; i < 4; i++ {
		snap.Quantum.TP[i] = m.quantumTP[i].Load()
		snap.Quantum.FP[i] = m.quantumFP[i].Load()
		snap.Quantum.TN[i] = m.quantumTN[i].Load()
		snap.Quantum.FN[i] = m.quantumFN[i].Load()
		snap.Conformal.TP[i] = m.conformalTP[i].Load()
		snap.Conformal.FP[i] = m.conformalFP[i].Load()
		snap.Conformal.TN[i] = m.conformalTN[i].Load()
		snap.Conformal.FN[i] = m.conformalFN[i].Load()
	}
	snap.Quantum.Total = m.quantumTotal.Load()
	snap.Conformal.Total = m.conformalTotal.Load()
	computeDerived(&snap.Quantum)
	computeDerived(&snap.Conformal)
	return snap
}

func computeDerived(cm *RegimeConfusionMatrix) {
	var totalTP, totalFP, totalTN, totalFN uint64
	for i := 0; i < 4; i++ {
		tp := float64(cm.TP[i])
		fp := float64(cm.FP[i])
		fn := float64(cm.FN[i])
		tn := float64(cm.TN[i])

		if tp+fp > 0 {
			cm.Precision[i] = tp / (tp + fp)
		}
		if tp+fn > 0 {
			cm.Recall[i] = tp / (tp + fn)
		}
		if cm.Precision[i]+cm.Recall[i] > 0 {
			cm.F1[i] = 2 * cm.Precision[i] * cm.Recall[i] / (cm.Precision[i] + cm.Recall[i])
		}

		totalTP += cm.TP[i]
		totalFP += cm.FP[i]
		totalTN += cm.TN[i]
		totalFN += cm.FN[i]
		_ = tn
	}
	total := float64(totalTP + totalTN + totalFP + totalFN)
	if total > 0 {
		cm.Accuracy = float64(totalTP+totalTN) / total
	}
}

// String returns a human-readable summary.
func (cm RegimeConfusionMatrix) String() string {
	names := [4]string{"Bull", "Bear", "Sideways", "Volatile"}
	var b strings.Builder
	b.WriteString("Regime Confusion Matrix:\n")
	b.WriteString(fmt.Sprintf("  Total samples: %d, Accuracy: %.3f\n", cm.Total, cm.Accuracy))
	b.WriteString(fmt.Sprintf("  %-10s %8s %8s %8s %8s\n", "Regime", "Prec", "Rec", "F1", "Support"))
	for i := 0; i < 4; i++ {
		support := cm.TP[i] + cm.FN[i]
		b.WriteString(fmt.Sprintf("  %-10s %8.3f %8.3f %8.3f %8d\n",
			names[i], cm.Precision[i], cm.Recall[i], cm.F1[i], support))
	}
	return b.String()
}

func (s RegimeMetricsSnapshot) String() string {
	var b strings.Builder
	b.WriteString("=== QUANTUM REGIME GATE ===\n")
	b.WriteString(s.Quantum.String())
	b.WriteString("\n=== CONFORMAL REGIME GATE ===\n")
	b.WriteString(s.Conformal.String())
	return b.String()
}

type persistedPendingObs struct {
	Timestamp time.Time `json:"timestamp"`
	GateScale float64   `json:"gate_scale"`
	GateType  string    `json:"gate_type"`
	BarDate   string    `json:"bar_date,omitempty"`
	HistLen   int       `json:"hist_len,omitempty"`
}

type persistedMatrix struct {
	TP    [4]uint64 `json:"tp"`
	FP    [4]uint64 `json:"fp"`
	TN    [4]uint64 `json:"tn"`
	FN    [4]uint64 `json:"fn"`
	Total uint64    `json:"total"`
}

type persistedRegimeMetrics struct {
	Quantum          persistedMatrix       `json:"quantum"`
	Conformal        persistedMatrix       `json:"conformal"`
	PendingQuantum   []persistedPendingObs `json:"pending_quantum,omitempty"`
	PendingConformal []persistedPendingObs `json:"pending_conformal,omitempty"`
}

// Save persists confusion matrix counts and pending observations atomically to path.
func (m *RegimeGateMetrics) Save(path string) error {
	if m == nil {
		return nil
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("empty regime metrics path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	var payload persistedRegimeMetrics
	for i := 0; i < 4; i++ {
		payload.Quantum.TP[i] = m.quantumTP[i].Load()
		payload.Quantum.FP[i] = m.quantumFP[i].Load()
		payload.Quantum.TN[i] = m.quantumTN[i].Load()
		payload.Quantum.FN[i] = m.quantumFN[i].Load()
		payload.Conformal.TP[i] = m.conformalTP[i].Load()
		payload.Conformal.FP[i] = m.conformalFP[i].Load()
		payload.Conformal.TN[i] = m.conformalTN[i].Load()
		payload.Conformal.FN[i] = m.conformalFN[i].Load()
	}
	payload.Quantum.Total = m.quantumTotal.Load()
	payload.Conformal.Total = m.conformalTotal.Load()

	m.pendingMu.Lock()
	if len(m.pendingQuantum) > 0 {
		payload.PendingQuantum = make([]persistedPendingObs, len(m.pendingQuantum))
		for i, obs := range m.pendingQuantum {
			payload.PendingQuantum[i] = persistedPendingObs{
				Timestamp: obs.timestamp,
				GateScale: obs.gateScale,
				GateType:  obs.gateType,
				BarDate:   obs.barDate,
				HistLen:   obs.histLen,
			}
		}
	}
	if len(m.pendingConformal) > 0 {
		payload.PendingConformal = make([]persistedPendingObs, len(m.pendingConformal))
		for i, obs := range m.pendingConformal {
			payload.PendingConformal[i] = persistedPendingObs{
				Timestamp: obs.timestamp,
				GateScale: obs.gateScale,
				GateType:  obs.gateType,
				BarDate:   obs.barDate,
				HistLen:   obs.histLen,
			}
		}
	}
	m.pendingMu.Unlock()

	return durable.WriteJSON(path, payload, "  ")
}

// Load restores confusion matrix counts and pending observations from path if it exists.
func (m *RegimeGateMetrics) Load(path string) error {
	if m == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	b, _, err := durable.ReadJSON(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var payload persistedRegimeMetrics
	if err := json.Unmarshal(b, &payload); err != nil {
		return fmt.Errorf("parse regime metrics %s: %w", path, err)
	}

	for i := 0; i < 4; i++ {
		m.quantumTP[i].Store(payload.Quantum.TP[i])
		m.quantumFP[i].Store(payload.Quantum.FP[i])
		m.quantumTN[i].Store(payload.Quantum.TN[i])
		m.quantumFN[i].Store(payload.Quantum.FN[i])
		m.conformalTP[i].Store(payload.Conformal.TP[i])
		m.conformalFP[i].Store(payload.Conformal.FP[i])
		m.conformalTN[i].Store(payload.Conformal.TN[i])
		m.conformalFN[i].Store(payload.Conformal.FN[i])
	}
	m.quantumTotal.Store(payload.Quantum.Total)
	m.conformalTotal.Store(payload.Conformal.Total)

	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	m.pendingQuantum = make([]pendingObs, 0, len(payload.PendingQuantum))
	for _, obs := range payload.PendingQuantum {
		m.pendingQuantum = append(m.pendingQuantum, pendingObs{
			timestamp: obs.Timestamp,
			gateScale: obs.GateScale,
			gateType:  obs.GateType,
			barDate:   obs.BarDate,
			histLen:   obs.HistLen,
		})
	}
	if len(m.pendingQuantum) > maxPendingObs {
		m.pendingQuantum = m.pendingQuantum[len(m.pendingQuantum)/2:]
	}
	m.pendingConformal = make([]pendingObs, 0, len(payload.PendingConformal))
	for _, obs := range payload.PendingConformal {
		m.pendingConformal = append(m.pendingConformal, pendingObs{
			timestamp: obs.Timestamp,
			gateScale: obs.GateScale,
			gateType:  obs.GateType,
			barDate:   obs.BarDate,
			histLen:   obs.HistLen,
		})
	}
	if len(m.pendingConformal) > maxPendingObs {
		m.pendingConformal = m.pendingConformal[len(m.pendingConformal)/2:]
	}
	return nil
}
