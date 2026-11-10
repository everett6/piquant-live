package portfolio

import (
	"math"
	"testing"
)

func TestRiskParityWeights(t *testing.T) {
	// Asset 1 (BTC): vol = 30 bps, Asset 2 (ETH): vol = 60 bps
	// Risk parity should allocate 2x more weight to Asset 1 than Asset 2
	vols := []float64{30.0, 60.0}
	weights, err := RiskParityWeights(vols)
	if err != nil {
		t.Fatalf("RiskParityWeights failed: %v", err)
	}

	if math.Abs(weights[0]-0.6667) > 0.01 {
		t.Errorf("expected BTC weight ~0.6667, got %f", weights[0])
	}
	if math.Abs(weights[1]-0.3333) > 0.01 {
		t.Errorf("expected ETH weight ~0.3333, got %f", weights[1])
	}
	if math.Abs((weights[0]+weights[1])-1.0) > 1e-6 {
		t.Errorf("expected weights sum to 1.0, got %f", weights[0]+weights[1])
	}
}

func TestBlackLittermanOptimization(t *testing.T) {
	bl := NewBlackLittermanModel(0.05, 2.5)

	// Equal prior market weights (0.5 BTC, 0.5 ETH)
	// Algorithmic view strongly favors BTC over ETH
	w1, w2, er1, er2 := bl.Optimize2Asset(
		0.02, 0.04, 0.65, // sigma1, sigma2, correlation
		0.50, 0.50, // prior market weights
		0.15, 0.02, // view returns: BTC +15%, ETH +2%
		0.80, 0.80, // high confidence in views
	)

	if er1 <= er2 {
		t.Errorf("expected blended return BTC (%f) > ETH (%f)", er1, er2)
	}
	if w1 <= w2 {
		t.Errorf("expected optimal weight BTC (%f) > ETH (%f)", w1, w2)
	}
	if w1+w2 > 1.0001 {
		t.Errorf("expected sum of weights <= 1.0, got %f", w1+w2)
	}
}

func TestDynamicHalfKellySizer(t *testing.T) {
	ks := NewDynamicKellySizer(0.5, 0.80, 5.0)

	// Test calm market (ATR 35 bps) vs volatile market (ATR 100 bps)
	equity := 200.0
	price := 75000.0

	qtyCalm, notionalCalm := ks.ComputeBetSize(equity, price, 35.0, 0.58, 0.85, 60.0)
	qtyVolatile, notionalVolatile := ks.ComputeBetSize(equity, price, 100.0, 0.58, 0.85, 60.0)

	if qtyCalm <= 0 || notionalCalm < 5.0 {
		t.Errorf("expected valid calm order size, got qty=%f notional=%f", qtyCalm, notionalCalm)
	}
	if qtyVolatile <= 0 || notionalVolatile < 5.0 {
		t.Errorf("expected valid volatile order size, got qty=%f notional=%f", qtyVolatile, notionalVolatile)
	}

	// Inverse volatility scaling must allocate less capital in 100 bps ATR than 35 bps ATR
	if notionalVolatile >= notionalCalm {
		t.Errorf("expected volatile notional (%f) < calm notional (%f)", notionalVolatile, notionalCalm)
	}
}
