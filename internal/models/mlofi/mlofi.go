// Package mlofi implements the MLOFI_Microprice model.
//
// Research question: do multilevel order-flow imbalance and microprice
// deviation carry information about the mid-price change over the next
// 30 s / 1 m / 5 m, and does adding book levels beyond the touch help?
//
// The model is a ridge regression per horizon over one of four feature sets,
// so the question "does L20 beat L1?" is answered by fitting the same model to
// different inputs and comparing out of sample. Nothing in the package assumes
// the answer is yes.
//
// # Features
//
// From internal/features (indices are stable, see features.Names):
//
//	0 imb_l1        (bid-ask)/(bid+ask) at the touch, [-1, 1]
//	1 imb_l5        same over the top 5 levels
//	2 imb_l20       same over the top 20 levels
//	3 micro_dev     (microprice-mid)/spread, [-0.5, 0.5]
//	4 spread_bps    spread in bps
//	5 ofi_1s        whole-book OFI over 1 s, base units
//	6 ofi_5s
//	7 ofi_30s
//	8 mlofi_1s      decayed multilevel OFI over 1 s
//	9 mlofi_5s
//
// # Feature sets
//
//	L1:  {imb_l1, micro_dev, spread_bps, ofi_1s, ofi_5s}
//	L5:  L1 features + {imb_l5}
//	L20: L5 features + {imb_l20}
//	Multi: every feature above, including both multilevel OFI terms
//
// # Target
//
// The target is the mid-price change over the horizon in bps, measured from
// the mid at the forecast instant. The evaluator builds the samples; this
// package never looks ahead.
package mlofi

import (
	"fmt"
	"time"

	"piquant-live/internal/models/common"
)

// FeatureSet names one input configuration.
type FeatureSet string

// Feature sets under comparison.
const (
	SetL1    FeatureSet = "L1"
	SetL5    FeatureSet = "L5"
	SetL20   FeatureSet = "L20"
	SetMulti FeatureSet = "multi"
)

// FeatureSets lists every set in report order.
var FeatureSets = []FeatureSet{SetL1, SetL5, SetL20, SetMulti}

// Features returns the feature indices of a set.
func Features(s FeatureSet) ([]int, error) {
	l1 := []int{0, 3, 4, 5, 6}
	switch s {
	case SetL1:
		return l1, nil
	case SetL5:
		return append(append([]int{}, l1...), 1), nil
	case SetL20:
		return append(append([]int{}, l1...), 1, 2), nil
	case SetMulti:
		return append(append([]int{}, l1...), 1, 2, 7, 8, 9), nil
	}
	return nil, fmt.Errorf("mlofi: unknown feature set %q", s)
}

// Params configures one fit.
type Params struct {
	Set        FeatureSet
	Lam        float64 // ridge penalty
	MinSamples int     // below this, the model refuses to fit
}

// Model is one fitted ridge per horizon over one feature set.
type Model struct {
	params Params
	idx    []int
	models map[common.Horizon]*common.Ridge
	cost   time.Duration
}

// New returns an unfitted model.
func New(p Params) (*Model, error) {
	idx, err := Features(p.Set)
	if err != nil {
		return nil, err
	}
	if p.Lam <= 0 {
		p.Lam = 1e-3
	}
	if p.MinSamples <= 0 {
		p.MinSamples = 200
	}
	return &Model{params: p, idx: idx, models: make(map[common.Horizon]*common.Ridge)}, nil
}

// Name implements common.Model.
func (m *Model) Name() string { return "MLOFI_Microprice/" + string(m.params.Set) }

// Version implements common.Model.
func (m *Model) Version() string {
	return fmt.Sprintf("ridge-l2=%g,min=%d", m.params.Lam, m.params.MinSamples)
}

// Features implements common.Model.
func (m *Model) Features() []int { return append([]int{}, m.idx...) }

// Horizons implements common.Model.
func (m *Model) Horizons() []common.Horizon { return common.Horizons }

// Cost implements common.Model.
func (m *Model) Cost() time.Duration { return m.cost }

// Fitted reports whether the model can forecast.
func (m *Model) Fitted() bool { return len(m.models) == len(common.Horizons) }

// Set returns the feature set this model was built for.
func (m *Model) Set() FeatureSet { return m.params.Set }

// Coefs returns the fitted weights for one horizon, for the report.
func (m *Model) Coefs(h common.Horizon) map[int]float64 {
	r, ok := m.models[h]
	if !ok {
		return nil
	}
	return r.Coefs(m.idx)
}

// Fit trains one ridge per horizon. samplesByHorizon[i] holds the training
// rows for common.Horizons[i]. A horizon with too few samples is left unfitted
// and reports an invalid forecast, rather than being trained on noise.
func (m *Model) Fit(samplesByHorizon [][]common.Sample) error {
	if len(samplesByHorizon) != len(common.Horizons) {
		return fmt.Errorf("mlofi: need samples for %d horizons, got %d",
			len(common.Horizons), len(samplesByHorizon))
	}
	m.models = make(map[common.Horizon]*common.Ridge, len(common.Horizons))
	for i, h := range common.Horizons {
		samples := samplesByHorizon[i]
		if len(samples) < m.params.MinSamples {
			continue
		}
		r := common.NewRidge(m.params.Lam)
		if err := r.Fit(samples, m.idx); err != nil {
			return err
		}
		m.models[h] = r
	}
	return nil
}

// Forecast implements common.Model. The confidence is the inverse of the
// prediction's magnitude relative to the model's fitted scale: a ridge model
// has no calibrated uncertainty, so this is a bounded monotone score, not a
// probability. The evaluator measures calibration; this field must not be
// relied on as if it were.
func (m *Model) Forecast(f []float32, sym uint16, at int64, h common.Horizon) common.Forecast {
	start := time.Now()
	out := common.Forecast{Model: m.Name(), Horizon: h, Symbol: sym, At: at}
	r, ok := m.models[h]
	if !ok {
		out.Reason = "horizon not fitted"
		out.Cost = time.Since(start)
		m.cost += out.Cost
		return out
	}
	var x [common.FeatureCount]float64
	for _, j := range m.idx {
		x[j] = float64(f[j])
	}
	v := r.Predict(&x)
	if !common.Finite(v) {
		out.Reason = "non-finite prediction"
		out.Cost = time.Since(start)
		m.cost += out.Cost
		return out
	}
	out.Value = v
	out.Confidence = 1 / (1 + abs(v)/10)
	out.Valid = true
	out.Cost = time.Since(start)
	m.cost += out.Cost
	return out
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
