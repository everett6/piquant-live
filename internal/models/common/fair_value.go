package common

import (
	"math"
)

// FairValueMode defines how the un-skewed true fair value is calculated.
type FairValueMode string

const (
	FairValueMidPrice            FairValueMode = "MID_PRICE"
	FairValueMicroPrice          FairValueMode = "MICRO_PRICE"
	FairValueMultiLevelDepth     FairValueMode = "MULTI_LEVEL_DEPTH"
	FairValueCoinbaseLeadLag     FairValueMode = "COINBASE_LEAD_LAG"
)

// TribecaFairValueEngine implements the institutional Fair Value and Dynamic Target Inventory
// mechanics inspired by the flagship Tribeca market-making engine.
// All calculations execute in 0 B/op and 0 allocs/op.
type TribecaFairValueEngine struct {
	Mode               FairValueMode
	RiskAversionGamma  float64 // gamma (default: 0.10)
	OrderBookDensityKappa float64 // kappa (default: 1.5)
	MaxInventoryCap    float64 // Max allowed inventory units (e.g. 0.05 BTC)
}

// NewTribecaFairValueEngine initializes a Tribeca-style market making engine.
func NewTribecaFairValueEngine(mode FairValueMode, gamma, kappa, maxInv float64) *TribecaFairValueEngine {
	if mode == "" {
		mode = FairValueMicroPrice
	}
	if gamma <= 0 {
		gamma = 0.10
	}
	if kappa <= 0 {
		kappa = 1.5
	}
	if maxInv <= 0 {
		maxInv = 0.02
	}
	return &TribecaFairValueEngine{
		Mode:                  mode,
		RiskAversionGamma:     gamma,
		OrderBookDensityKappa: kappa,
		MaxInventoryCap:       maxInv,
	}
}

// ComputeFairValue calculates the fundamental asset value before inventory skew.
func (t *TribecaFairValueEngine) ComputeFairValue(
	bestBid, bestAsk float64,
	bidSize, askSize float64,
	top5BidsPrice, top5BidsSize []float64,
	top5AsksPrice, top5AsksSize []float64,
	coinbasePrice float64,
) float64 {
	mid := (bestBid + bestAsk) * 0.50
	if mid <= 0 || math.IsNaN(mid) || math.IsInf(mid, 0) {
		return 0.0
	}

	switch t.Mode {
	case FairValueCoinbaseLeadLag:
		if coinbasePrice > 0 && !math.IsNaN(coinbasePrice) && !math.IsInf(coinbasePrice, 0) {
			return 0.70*coinbasePrice + 0.30*mid
		}
		fallthrough

	case FairValueMicroPrice:
		totSize := bidSize + askSize
		if totSize > 0 {
			micro := (bestBid*askSize + bestAsk*bidSize) / totSize
			if !math.IsNaN(micro) && !math.IsInf(micro, 0) && micro > 0 {
				return micro
			}
		}
		return mid

	case FairValueMultiLevelDepth:
		var sumWeightedBid, sumBidQty float64
		var sumWeightedAsk, sumAskQty float64
		for i := 0; i < len(top5BidsPrice) && i < len(top5BidsSize); i++ {
			p := top5BidsPrice[i]
			q := top5BidsSize[i]
			if p > 0 && q > 0 {
				sumWeightedBid += p * q
				sumBidQty += q
			}
		}
		for i := 0; i < len(top5AsksPrice) && i < len(top5AsksSize); i++ {
			p := top5AsksPrice[i]
			q := top5AsksSize[i]
			if p > 0 && q > 0 {
				sumWeightedAsk += p * q
				sumAskQty += q
			}
		}
		if sumBidQty+sumAskQty > 0 {
			depthWeighted := (sumWeightedBid + sumWeightedAsk) / (sumBidQty + sumAskQty)
			if !math.IsNaN(depthWeighted) && !math.IsInf(depthWeighted, 0) && depthWeighted > 0 {
				return depthWeighted
			}
		}
		return mid

	case FairValueMidPrice:
		fallthrough
	default:
		return mid
	}
}

// ComputeTargetInventory determines the optimal institutional target position (q*).
// Rather than assuming the fund must always be flat (q* = 0), this allows holding structural
// swing positions during confirmed momentum or high funding carry regimes.
func (t *TribecaFairValueEngine) ComputeTargetInventory(
	conviction float64, // [0.0, 1.0] from composite alpha (0.50 = neutral)
	fundingAPR float64, // e.g. 15.0%
	isSpotOnly bool,
) float64 {
	if conviction <= 0 || conviction > 1.0 {
		conviction = 0.50
	}
	target := 0.0

	// 1. Directional Alpha Swing Target:
	// If conviction is strong (> 0.55), target inventory tilts long up to +80% of max cap.
	// If conviction is weak (< 0.45) and not spot-only, target inventory tilts short.
	if conviction >= 0.55 {
		tiltFactor := (conviction - 0.50) * 2.0 // [0.10, 1.0]
		target += t.MaxInventoryCap * 0.80 * tiltFactor
	} else if conviction <= 0.45 && !isSpotOnly {
		tiltFactor := (0.50 - conviction) * 2.0 // [0.10, 1.0]
		target -= t.MaxInventoryCap * 0.80 * tiltFactor
	}

	// 2. Delta-Neutral Spot-Perp Funding Carry Target:
	// When funding rates are high (>= 12% APR), holding long spot to harvest perp funding is optimal.
	if fundingAPR >= 12.0 {
		carryWeight := math.Min(1.0, (fundingAPR-12.0)/18.0)
		target += t.MaxInventoryCap * 0.75 * carryWeight
	}

	// Clamp target strictly within inventory cap
	if target > t.MaxInventoryCap {
		target = t.MaxInventoryCap
	} else if !isSpotOnly && target < -t.MaxInventoryCap {
		target = -t.MaxInventoryCap
	} else if isSpotOnly && target < 0 {
		target = 0.0
	}

	return target
}

// ComputeReservationQuotes calculates the inventory-skewed reservation price and optimal half-spread.
// Skew is measured relative to delta = (currentInventory - targetInventory):
// When on target, inventory restoring force is ZERO, allowing the bot to hold swing runners without fee churn!
func (t *TribecaFairValueEngine) ComputeReservationQuotes(
	fairValue float64,
	currentInventory float64,
	targetInventory float64,
	volatilityBps float64,
	minFeeFloorBps float64,
) (reservationPrice float64, optimalBid float64, optimalAsk float64, halfSpreadBps float64) {
	if fairValue <= 0 || math.IsNaN(fairValue) || math.IsInf(fairValue, 0) {
		return 0, 0, 0, minFeeFloorBps
	}

	// Inventory discrepancy relative to institutional target
	invDelta := currentInventory - targetInventory
	normDelta := 0.0
	if t.MaxInventoryCap > 0 {
		normDelta = invDelta / t.MaxInventoryCap
		if normDelta > 2.0 {
			normDelta = 2.0
		} else if normDelta < -2.0 {
			normDelta = -2.0
		}
	}

	sigma := math.Max(5.0, volatilityBps)
	gamma := t.RiskAversionGamma

	// Avellaneda-Stoikov reservation price: r(s, q - q*, sigma) = s - (q - q*) * gamma * sigma^2
	// In basis points relative to fair value
	var skewBps float64 = normDelta * (gamma * sigma * 0.40)
	if skewBps > 25.0 {
		skewBps = 25.0
	} else if skewBps < -25.0 {
		skewBps = -25.0
	}

	reservationPrice = fairValue - (skewBps * fairValue * 1e-4)

	// Optimal Avellaneda-Stoikov half spread:
	// delta = (gamma * sigma^2)/2 + (1/gamma) * ln(1 + gamma/kappa)
	kappa := t.OrderBookDensityKappa
	logTerm := math.Log(1.0 + gamma/kappa)
	halfSpreadBps = 0.50*(gamma*sigma*0.25) + (1.0/gamma)*logTerm*2.0

	// Fee hurdle floor: guarantee that quotes clear exchange fees
	if halfSpreadBps < minFeeFloorBps {
		halfSpreadBps = minFeeFloorBps
	}

	optimalBid = reservationPrice - (halfSpreadBps * fairValue * 1e-4)
	optimalAsk = reservationPrice + (halfSpreadBps * fairValue * 1e-4)

	return reservationPrice, optimalBid, optimalAsk, halfSpreadBps
}
