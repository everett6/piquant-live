package alpha

import (
	"math"
	"time"
)

// AlphaSignal represents an institutional multi-horizon trading forecast.
type AlphaSignal struct {
	Symbol           string
	Timestamp        time.Time
	DirectionalScore float64 // Continuous score in [-1.0, +1.0] (+1 = extreme bullish, -1 = extreme bearish)
	ExpectedEdgeBps  float64 // Expected price drift / return edge in basis points
	Conviction       float64 // Probability of upward drift P(Up) in [0.0, 1.0]

	// Pillar sub-signals
	StatArbZScore float64
	TrendScore    float64
	MicroDelta    float64
	VPIN          float64
	SpreadBps     float64

	RecommendedAction string // "BUY", "SELL", "HOLD", "FLATTEN"
}

// AlphaEngine orchestrates Statistical Arbitrage, Trend Following, and Microstructure predictors.
type AlphaEngine struct {
	StatArb     *CointegrationModel
	Momentum    *MomentumTracker
	VPINCalc    *FastVPINCalculator
	LatestMid   float64
	PrevMid     float64
	PrevBid     float64
	PrevBidSize float64
	PrevAsk     float64
	PrevAskSize float64
}

// NewAlphaEngine creates a composite quantitative alpha engine.
func NewAlphaEngine(leader, follower string) *AlphaEngine {
	return &AlphaEngine{
		StatArb:  NewCointegrationModel(leader, follower, 60),
		Momentum: NewMomentumTracker(2000),
		VPINCalc: NewFastVPINCalculator(2.0, 50),
	}
}

// Evaluate produces a composite alpha forecast from multi-horizon trends, stat-arb mean-reversion, and microstructure.
func (ae *AlphaEngine) Evaluate(symbol string, midPrice, bestBid, bidSize, bestAsk, askSize float64, peerPrice float64, now time.Time) AlphaSignal {
	if midPrice <= 0 {
		return AlphaSignal{Symbol: symbol, Timestamp: now, Conviction: 0.50, RecommendedAction: "HOLD"}
	}

	// 1. Trend & Momentum evaluation
	mom := ae.Momentum.Update(midPrice, now)

	// 2. Microstructure & Order Flow evaluation
	l1Delta, microDevBps := ComputeBookDelta(bestBid, bidSize, bestAsk, askSize)
	var voi float64
	if ae.PrevBid > 0 && ae.PrevAsk > 0 {
		voi = ComputeVOI(ae.PrevBid, ae.PrevBidSize, ae.PrevAsk, ae.PrevAskSize, bestBid, bidSize, bestAsk, askSize)
	}
	ae.PrevBid = bestBid
	ae.PrevBidSize = bidSize
	ae.PrevAsk = bestAsk
	ae.PrevAskSize = askSize

	liveVpin := ae.VPINCalc.VPIN()
	spreadBps := 1.0
	if bestAsk > bestBid && midPrice > 0 {
		spreadBps = (bestAsk - bestBid) / midPrice * 1e4
	}

	// 3. Statistical Arbitrage evaluation (if peer price available, e.g. BTC & ETH)
	statArbZ := 0.0
	if peerPrice > 0 {
		statArbZ, _ = ae.StatArb.Update(midPrice, peerPrice)
	}

	// 4. Multi-Horizon Composite Blending:
	// - Trend Following & Momentum: 50% weight (captures sustained multi-minute / multi-day / multi-week runs)
	// - Statistical Arbitrage / Mean Reversion: 30% weight (mean-reverts overextended spreads)
	// - Microstructure & Order Flow: 20% weight (fine-tunes execution entry timing)
	// For stat-arb, if symbol is the follower, positive Z-score means follower is overpriced (should short).
	statArbEdge := -statArbZ * 0.30

	zVOI := voi / 2.0
	if zVOI > 2.0 {
		zVOI = 2.0
	} else if zVOI < -2.0 {
		zVOI = -2.0
	}
	ratio := 0.0
	if spreadBps > 0.05 {
		ratio = microDevBps / spreadBps
		if ratio > 2.0 {
			ratio = 2.0
		} else if ratio < -2.0 {
			ratio = -2.0
		}
	}
	microEdge := 0.60*l1Delta + 0.40*ratio

	compositeScore := 0.50*mom.TrendScore + 0.30*statArbEdge + 0.20*microEdge
	if compositeScore > 1.0 {
		compositeScore = 1.0
	} else if compositeScore < -1.0 {
		compositeScore = -1.0
	}

	// Logistic sigmoid conviction: P(Up) in [0.0, 1.0]
	conviction := 1.0 / (1.0 + math.Exp(-2.2*compositeScore))

	// Expected edge in basis points
	expectedEdgeBps := compositeScore * math.Max(15.0, mom.Atr15mBps*0.50)

	action := "HOLD"
	if conviction >= 0.65 {
		action = "BUY"
	} else if conviction <= 0.35 {
		action = "SELL"
	}
	if liveVpin > 0.75 {
		// Toxic predatory flow alert: halt aggressive action
		action = "HOLD"
	}

	return AlphaSignal{
		Symbol:            symbol,
		Timestamp:         now,
		DirectionalScore:  compositeScore,
		ExpectedEdgeBps:   expectedEdgeBps,
		Conviction:        conviction,
		StatArbZScore:     statArbZ,
		TrendScore:        mom.TrendScore,
		MicroDelta:        l1Delta,
		VPIN:              liveVpin,
		SpreadBps:         spreadBps,
		RecommendedAction: action,
	}
}
