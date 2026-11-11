package alpha

import "math"

// MicrostructureSignals extracts real-time order book imbalances, order flow toxicity, and book deltas.
type MicrostructureSignals struct {
	Level1Delta float64 // (bidSize - askSize) / (bidSize + askSize) in [-1.0, 1.0]
	Level5Delta float64 // Top 5 depth imbalance in [-1.0, 1.0]
	MicroDevBps float64 // Microprice deviation from mid in bps
	VOI1s       float64 // Volume Order Imbalance over 1 second (base units)
	VPIN        float64 // Volume-Synchronized Probability of Toxicity in [0.0, 1.0]
	SpreadBps   float64 // Bid-Ask spread in bps
}

// ComputeVOI computes the Level-1 Volume Order Imbalance (Cont, Kukanov & Stoikov)
// between previous top-of-book and current top-of-book.
func ComputeVOI(prevBidPrice, prevBidSize, prevAskPrice, prevAskSize float64,
	curBidPrice, curBidSize, curAskPrice, curAskSize float64) float64 {

	var deltaVb float64
	if curBidPrice > prevBidPrice {
		deltaVb = curBidSize
	} else if curBidPrice == prevBidPrice {
		deltaVb = curBidSize - prevBidSize
	} else {
		deltaVb = -prevBidSize
	}

	var deltaVa float64
	if curAskPrice < prevAskPrice {
		deltaVa = curAskSize
	} else if curAskPrice == prevAskPrice {
		deltaVa = curAskSize - prevAskSize
	} else {
		deltaVa = -prevAskSize
	}

	return deltaVb - deltaVa
}

// ComputeBookDelta extracts order book depth delta and microprice deviation.
func ComputeBookDelta(bestBid, bidSize, bestAsk, askSize float64) (l1Delta float64, microDevBps float64) {
	if math.IsNaN(bestBid) || math.IsInf(bestBid, 0) || bestBid <= 0 ||
		math.IsNaN(bestAsk) || math.IsInf(bestAsk, 0) || bestAsk <= 0 ||
		math.IsNaN(bidSize) || math.IsInf(bidSize, 0) || bidSize < 0 ||
		math.IsNaN(askSize) || math.IsInf(askSize, 0) || askSize < 0 {
		return 0, 0
	}
	tot := bidSize + askSize
	if tot <= 0 {
		return 0, 0
	}
	l1Delta = (bidSize - askSize) / tot
	mid := (bestBid + bestAsk) * 0.5
	if mid <= 0 {
		return l1Delta, 0
	}
	microPrice := (bestBid*askSize + bestAsk*bidSize) / tot
	microDevBps = (microPrice - mid) / mid * 1e4
	if math.IsNaN(microDevBps) || math.IsInf(microDevBps, 0) {
		microDevBps = 0
	}
	return l1Delta, microDevBps
}

// FastVPINCalculator computes rolling Volume-Synchronized Probability of Toxicity (Easley, Lopez de Prado & O'Hara).
type FastVPINCalculator struct {
	BucketVolume float64
	NumBuckets   int

	CurrentBucketBuy  float64
	CurrentBucketSell float64
	CurrentBucketVol  float64

	BucketImbalances []float64
	Head             int
	Count            int
}

// NewFastVPINCalculator creates a VPIN calculator.
func NewFastVPINCalculator(bucketVolume float64, numBuckets int) *FastVPINCalculator {
	if math.IsNaN(bucketVolume) || math.IsInf(bucketVolume, 0) || bucketVolume <= 0 {
		bucketVolume = 2.0 // BTC units per bucket
	}
	if numBuckets <= 0 {
		numBuckets = 50
	}
	return &FastVPINCalculator{
		BucketVolume:     bucketVolume,
		NumBuckets:       numBuckets,
		BucketImbalances: make([]float64, numBuckets),
	}
}

// AddTrade processes a trade tick and returns the updated VPIN.
func (vc *FastVPINCalculator) AddTrade(price, size float64, isBuyerMaker bool) float64 {
	if math.IsNaN(size) || math.IsInf(size, 0) || size <= 0 {
		return vc.VPIN()
	}
	if math.IsNaN(price) || math.IsInf(price, 0) || price <= 0 {
		return vc.VPIN()
	}

	// Invert Binance isBuyerMaker convention:
	// If isBuyerMaker is true, the passive maker is buyer, meaning the aggressive taker was a SELLER.
	takerBuy := size
	takerSell := 0.0
	if isBuyerMaker {
		takerBuy = 0.0
		takerSell = size
	}

	remSize := size
	for remSize > 0 {
		space := vc.BucketVolume - vc.CurrentBucketVol
		if space <= 0 {
			imb := math.Abs(vc.CurrentBucketBuy - vc.CurrentBucketSell)
			vc.BucketImbalances[vc.Head] = imb
			vc.Head = (vc.Head + 1) % vc.NumBuckets
			if vc.Count < vc.NumBuckets {
				vc.Count++
			}
			vc.CurrentBucketBuy = 0
			vc.CurrentBucketSell = 0
			vc.CurrentBucketVol = 0
			continue
		}
		alloc := math.Min(remSize, space)
		if alloc <= 0 {
			break
		}

		if size > 0 {
			vc.CurrentBucketBuy += takerBuy * (alloc / size)
			vc.CurrentBucketSell += takerSell * (alloc / size)
		}
		vc.CurrentBucketVol += alloc
		remSize -= alloc

		if vc.CurrentBucketVol >= vc.BucketVolume {
			imb := math.Abs(vc.CurrentBucketBuy - vc.CurrentBucketSell)
			vc.BucketImbalances[vc.Head] = imb
			vc.Head = (vc.Head + 1) % vc.NumBuckets
			if vc.Count < vc.NumBuckets {
				vc.Count++
			}
			vc.CurrentBucketBuy = 0
			vc.CurrentBucketSell = 0
			vc.CurrentBucketVol = 0
		}
	}

	return vc.VPIN()
}

// VPIN returns the current toxicity score in [0.0, 1.0].
func (vc *FastVPINCalculator) VPIN() float64 {
	if vc.Count == 0 {
		return 0.15 // default calm baseline
	}
	var sumImb float64
	for i := 0; i < vc.Count; i++ {
		sumImb += vc.BucketImbalances[i]
	}
	totVol := float64(vc.Count) * vc.BucketVolume
	if totVol <= 0 {
		return 0.15
	}
	vpin := sumImb / totVol
	if vpin > 1.0 {
		vpin = 1.0
	} else if vpin < 0.0 {
		vpin = 0.0
	}
	return vpin
}
