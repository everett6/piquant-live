package common

import (
	"math"
	"testing"
)

func TestTribecaFairValueModes(t *testing.T) {
	bestBid := 60000.0
	bestAsk := 60010.0
	bidSize := 2.0
	askSize := 8.0 // Heavily skewed towards ask -> micro-price closer to bestBid

	top5BidsPrice := []float64{60000.0, 59990.0, 59980.0}
	top5BidsSize := []float64{2.0, 3.0, 5.0} // sum: 10.0 qty, avg: ~59987
	top5AsksPrice := []float64{60010.0, 60020.0, 60030.0}
	top5AsksSize := []float64{8.0, 5.0, 2.0} // sum: 15.0 qty, avg: ~60016
	coinbasePrice := 60015.0

	// 1. MidPrice Mode
	engineMid := NewTribecaFairValueEngine(FairValueMidPrice, 0.1, 1.5, 0.05)
	fvMid := engineMid.ComputeFairValue(bestBid, bestAsk, bidSize, askSize, top5BidsPrice, top5BidsSize, top5AsksPrice, top5AsksSize, coinbasePrice)
	expectedMid := 60005.0
	if math.Abs(fvMid-expectedMid) > 1e-4 {
		t.Fatalf("expected MidPrice %f, got %f", expectedMid, fvMid)
	}

	// 2. MicroPrice Mode
	// micro = (60000*8 + 60010*2) / 10 = (480000 + 120020) / 10 = 60002.0
	engineMicro := NewTribecaFairValueEngine(FairValueMicroPrice, 0.1, 1.5, 0.05)
	fvMicro := engineMicro.ComputeFairValue(bestBid, bestAsk, bidSize, askSize, top5BidsPrice, top5BidsSize, top5AsksPrice, top5AsksSize, coinbasePrice)
	expectedMicro := 60002.0
	if math.Abs(fvMicro-expectedMicro) > 1e-4 {
		t.Fatalf("expected MicroPrice %f, got %f", expectedMicro, fvMicro)
	}

	// 3. MultiLevelDepth Mode
	engineDepth := NewTribecaFairValueEngine(FairValueMultiLevelDepth, 0.1, 1.5, 0.05)
	fvDepth := engineDepth.ComputeFairValue(bestBid, bestAsk, bidSize, askSize, top5BidsPrice, top5BidsSize, top5AsksPrice, top5AsksSize, coinbasePrice)
	if fvDepth <= 59980.0 || fvDepth >= 60030.0 {
		t.Fatalf("expected multi-level depth within book bounds, got %f", fvDepth)
	}

	// 4. CoinbaseLeadLag Mode: 0.70*coinbase + 0.30*mid = 0.70*60015 + 0.30*60005 = 42010.5 + 18001.5 = 60012.0
	engineCB := NewTribecaFairValueEngine(FairValueCoinbaseLeadLag, 0.1, 1.5, 0.05)
	fvCB := engineCB.ComputeFairValue(bestBid, bestAsk, bidSize, askSize, top5BidsPrice, top5BidsSize, top5AsksPrice, top5AsksSize, coinbasePrice)
	expectedCB := 60012.0
	if math.Abs(fvCB-expectedCB) > 1e-4 {
		t.Fatalf("expected CoinbaseLeadLag %f, got %f", expectedCB, fvCB)
	}
}

func TestTribecaTargetInventory(t *testing.T) {
	maxCap := 0.05
	engine := NewTribecaFairValueEngine(FairValueMicroPrice, 0.1, 1.5, maxCap)

	// Neutral conviction, zero funding -> target = 0
	target0 := engine.ComputeTargetInventory(0.50, 0.0, false)
	if target0 != 0.0 {
		t.Fatalf("expected 0.0 target for neutral, got %f", target0)
	}

	// Bullish conviction (0.75), 0 funding -> positive target
	targetBull := engine.ComputeTargetInventory(0.75, 0.0, false)
	if targetBull <= 0.0 || targetBull > maxCap {
		t.Fatalf("expected positive target within cap, got %f", targetBull)
	}

	// Bearish conviction (0.25), 0 funding, perp/futures enabled -> negative target
	targetBear := engine.ComputeTargetInventory(0.25, 0.0, false)
	if targetBear >= 0.0 || targetBear < -maxCap {
		t.Fatalf("expected negative target for bear perp, got %f", targetBear)
	}

	// Bearish conviction (0.25), spot-only -> target cannot be negative
	targetBearSpot := engine.ComputeTargetInventory(0.25, 0.0, true)
	if targetBearSpot < 0.0 {
		t.Fatalf("expected non-negative target for spot-only, got %f", targetBearSpot)
	}

	// High funding carry APR (24% APR) -> positive target to harvest carry
	targetCarry := engine.ComputeTargetInventory(0.50, 24.0, false)
	if targetCarry <= 0.0 {
		t.Fatalf("expected positive target for high funding carry, got %f", targetCarry)
	}
}

func TestTribecaReservationQuotes(t *testing.T) {
	maxCap := 0.05
	engine := NewTribecaFairValueEngine(FairValueMicroPrice, 0.1, 1.5, maxCap)
	fairValue := 60000.0
	volatilityBps := 15.0
	minFeeFloorBps := 6.0

	// Case 1: Current inventory == Target inventory (q == q*)
	// Skew should be strictly ZERO (no restoring penalty!)
	qTarget := 0.03
	qCurrent := 0.03
	resPrice, bid, ask, halfSpread := engine.ComputeReservationQuotes(fairValue, qCurrent, qTarget, volatilityBps, minFeeFloorBps)
	if math.Abs(resPrice-fairValue) > 1e-4 {
		t.Fatalf("expected zero reservation price skew when q == q*, got resPrice=%f, fv=%f", resPrice, fairValue)
	}
	if halfSpread < minFeeFloorBps {
		t.Fatalf("halfSpread (%f) should meet minFeeFloorBps (%f)", halfSpread, minFeeFloorBps)
	}
	if bid >= fairValue || ask <= fairValue {
		t.Fatalf("bid/ask should straddle fair value: bid=%f, ask=%f, fv=%f", bid, ask, fairValue)
	}

	// Case 2: Long excess (q > q*) -> Reservation price skews downward to discourage buying and encourage selling
	qExcess := 0.05
	resPriceLong, bidLong, askLong, _ := engine.ComputeReservationQuotes(fairValue, qExcess, qTarget, volatilityBps, minFeeFloorBps)
	if resPriceLong >= fairValue {
		t.Fatalf("expected downward reservation skew for excess long inventory, got %f vs fv %f", resPriceLong, fairValue)
	}
	if bidLong >= bid || askLong >= ask {
		t.Fatalf("expected quotes shifted downward for excess long inventory")
	}

	// Case 3: Short excess (q < q*) -> Reservation price skews upward to encourage buying and discourage selling
	qDeficit := 0.01
	resPriceShort, bidShort, askShort, _ := engine.ComputeReservationQuotes(fairValue, qDeficit, qTarget, volatilityBps, minFeeFloorBps)
	if resPriceShort <= fairValue {
		t.Fatalf("expected upward reservation skew for deficit inventory, got %f vs fv %f", resPriceShort, fairValue)
	}
	if bidShort <= bid || askShort <= ask {
		t.Fatalf("expected quotes shifted upward for deficit inventory")
	}
}

func BenchmarkTribecaReservationQuotes(b *testing.B) {
	engine := NewTribecaFairValueEngine(FairValueMicroPrice, 0.1, 1.5, 0.05)
	fairValue := 60000.0
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.ComputeReservationQuotes(fairValue, 0.02, 0.03, 15.0, 6.0)
	}
}
