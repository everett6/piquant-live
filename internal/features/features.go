// Package features turns book updates and trades into the model input vector
// without allocating. Book-derived quantities stay in fixed point int64 until
// the last step, where they are normalised to float32.
//
// Every window uses event time (the local receive timestamp carried in the
// envelope), not wall-clock time: replaying a capture produces exactly the same
// features it produced live, and a stalled feed makes every window go empty
// instead of silently repeating stale values.
package features

import (
	"math"

	"piquant-live/internal/event"
	"piquant-live/internal/orderbook"
)

// Names lists the feature vector layout. Keep in sync with Extract.
var Names = []string{
	"imb_l1", // (bid_q - ask_q) / (bid_q + ask_q)
	"imb_l5",
	"imb_l20",
	"micro_dev",  // (microprice - mid) / spread, in [-0.5, 0.5]
	"spread_bps", // spread / mid * 1e4
	"ofi_1s",     // whole-book OFI over 1 s, base units
	"ofi_5s",
	"ofi_30s",
	"mlofi_1s", // multi-level OFI with decay exp(-0.3*(l-1)), 1 s
	"mlofi_5s",
	"vol_30s_bps",  // realised volatility of 1 s mid returns, bps
	"ret_1s_bps",   // mid return over 1 s, bps
	"ret_5s_bps",   // mid return over 5 s, bps
	"trade_imb_5s", // (buy - sell) traded base units over 5 s, base units
	"trade_cnt_1s", // number of trades in the last second
	"queue_ratio",  // best bid qty / (best bid qty + best ask qty)
}

// NCore is the number of features produced by each Extract call.
const NCore = 16

func init() {
	if len(Names) != NCore {
		panic("features: Names and NCore out of sync")
	}
}

// Window lengths in event-time nanoseconds.
const (
	win1s  = int64(1e9)
	win5s  = int64(5 * 1e9)
	win30s = int64(30 * 1e9)
)

// ofiBeta is the multi-level OFI decay across book levels.
const ofiBeta = 0.3

// ringLen bounds per-symbol event history; retained duration depends on feed
// rate, while each feature window still expires by event time.
const ringLen = 512

// Engine holds per-symbol streaming state. One goroutine owns one Engine.
type Engine struct {
	sym uint16

	// Price-change samples: the time and mid after the update, plus the
	// OFI terms and one-second return attributed to that update.
	chT      [ringLen]int64
	chMid    [ringLen]int64
	chV      [ringLen]float64
	chW      [ringLen]float64
	chRet    [ringLen]float64
	chHasRet [ringLen]bool
	hCh      int
	nCh      int
	chSeq    uint64

	chFirst1, chFirst5, chFirst30 uint64
	ofi1, ofi5, ofi30             float64
	ml1, ml5                      float64
	volN                          uint64
	volMean, volM2                float64

	mid1Seq, mid5Seq     uint64
	mid1Valid, mid5Valid bool

	// Trade samples.
	trT   [ringLen]int64
	trV   [ringLen]float64
	hTr   int
	nTr   int
	trSeq uint64

	trFirst1, trFirst5 uint64
	trade5             float64
	tradeCount1        uint64

	lastRecv int64
	ready    bool
	emitted  bool
	emit     [NCore]float32
}

// New creates an engine for a symbol.
func New(sym uint16) *Engine { return &Engine{sym: sym} }

// Symbol returns the engine's symbol.
func (e *Engine) Symbol() uint16 { return e.sym }

// Reset clears all state, e.g. after a book resync. Built from a zero value so
// no carried-over sample spans the gap.
func (e *Engine) Reset() {
	sym := e.sym
	*e = Engine{sym: sym}
}

// Ready reports whether the last Advance saw a live book.
func (e *Engine) Ready() bool { return e.ready }

// Advance updates rolling state from one envelope. Call it BEFORE applying the
// envelope to the book: OFI is measured against the pre-update book.
func (e *Engine) Advance(book *orderbook.Book, env *event.Envelope) bool {
	e.ready = book.Ready
	e.lastRecv = env.RecvTime
	e.expire(env.RecvTime)
	switch env.Kind {
	case event.KindSnapshot:
		// The snapshot replaces the book and is the OFI baseline: the first
		// depth update is measured against it like any other transition.
		e.resetWindows()
		e.baseline(book, env.RecvTime)
	case event.KindDepth:
		if book.Ready && book.Anchored {
			e.update(book, &env.Depth, env.RecvTime)
		}
	case event.KindTrade:
		q := env.Trade.Qty
		if env.Trade.Side == event.SideSell {
			q = -q
		}
		e.pushTrade(env.RecvTime, float64(q))
	}
	e.compute(book, env.RecvTime)
	return e.ready
}

func (e *Engine) resetWindows() {
	e.hCh, e.nCh, e.chSeq = 0, 0, 0
	e.chFirst1, e.chFirst5, e.chFirst30 = 0, 0, 0
	e.ofi1, e.ofi5, e.ofi30, e.ml1, e.ml5 = 0, 0, 0, 0, 0
	e.volN, e.volMean, e.volM2 = 0, 0, 0
	e.mid1Seq, e.mid5Seq = 0, 0
	e.mid1Valid, e.mid5Valid = false, false
	e.hTr, e.nTr, e.trSeq = 0, 0, 0
	e.trFirst1, e.trFirst5 = 0, 0
	e.trade5, e.tradeCount1 = 0, 0
}

// baseline records the book state right after a snapshot as a sample, so the
// first post-snapshot update has something to be measured against.
func (e *Engine) baseline(book *orderbook.Book, t int64) {
	bp, _ := book.Levels[orderbook.Bids].Best()
	ap, _ := book.Levels[orderbook.Asks].Best()
	e.pushChange(t, (bp+ap)/2, 0, 0, 0, false)
}

// update records the flow and the mid caused by one depth update, measured
// against the pre-update book.
//
// The per-level n-th term (OFI of Cont, Kukanov and Stoikov applied one price
// level at a time) is the size change at that level: on the bid, a level that
// did not exist before or was resized at the same price:
//
//	e_n = q_n        if P_n > P_{n-1}   (new better bid)
//	e_n = q_n - q_{n-1}  if P_n == P_{n-1}  (resize at the same price)
//	e_n = 0          if P_n < P_{n-1}   (deletion below the touch: no impact)
//
// which, because prevQty is 0 for a level that is not in the book, reduces to
// q_n - q_{n-1} for every touched price. The asks are symmetric.
func (e *Engine) update(book *orderbook.Book, d *event.DepthEvent, t int64) {
	bidSide := d.Side == event.SideBuy
	var side *orderbook.Levels
	if bidSide {
		side = &book.Levels[orderbook.Bids]
	} else {
		side = &book.Levels[orderbook.Asks]
	}
	// The mid after this update: the touched side already reflects it, the
	// other side is still the pre-update book.
	var bp, ap int64
	if bidSide {
		bp, _ = side.Best()
		ap, _ = book.Levels[orderbook.Asks].Best()
	} else {
		bp, _ = book.Levels[orderbook.Bids].Best()
		ap, _ = side.Best()
	}
	var world, ml float64
	for i := 0; i < int(d.NUpdates); i++ {
		lv := &d.Updates[i]
		rk := rankAt(side, lv.Price, bidSide)
		delta := bidTerm(prevQty(side, lv.Price), lv.Qty)
		if !bidSide {
			delta = -delta
		}
		world += delta
		ml += delta * levelWeight(rk)
	}
	mid := (bp + ap) / 2
	old, ok := e.midAtCursor(t-win1s, &e.mid1Seq, &e.mid1Valid)
	var ret float64
	if ok && old > 0 {
		ret = float64(mid-old) / float64(old) * 1e4
	}
	e.pushChange(t, mid, world, ml, ret, ok && old > 0)
}

func (e *Engine) pushChange(t, mid int64, world, ml, ret float64, hasRet bool) {
	if e.nCh == ringLen {
		e.evictChange(e.chSeq - ringLen)
	} else {
		e.nCh++
	}
	i := e.hCh
	e.chT[i] = t
	e.chMid[i] = mid
	e.chV[i] = world
	e.chW[i] = ml
	e.chRet[i] = ret
	e.chHasRet[i] = hasRet
	e.hCh = (i + 1) & (ringLen - 1)
	e.chSeq++
	e.ofi1 += world
	e.ofi5 += world
	e.ofi30 += world
	e.ml1 += ml
	e.ml5 += ml
	if hasRet {
		e.addVolatility(ret)
	}
}

func (e *Engine) pushTrade(t int64, qty float64) {
	if e.nTr == ringLen {
		e.evictTrade(e.trSeq - ringLen)
	} else {
		e.nTr++
	}
	i := e.hTr
	e.trT[i] = t
	e.trV[i] = qty
	e.hTr = (i + 1) & (ringLen - 1)
	e.trSeq++
	e.trade5 += qty
	e.tradeCount1++
}

func (e *Engine) expire(t int64) {
	for e.chFirst1 < e.chSeq && t-e.chT[e.chFirst1&(ringLen-1)] >= win1s {
		e.ofi1 -= e.chV[e.chFirst1&(ringLen-1)]
		e.ml1 -= e.chW[e.chFirst1&(ringLen-1)]
		e.chFirst1++
	}
	for e.chFirst5 < e.chSeq && t-e.chT[e.chFirst5&(ringLen-1)] >= win5s {
		e.ofi5 -= e.chV[e.chFirst5&(ringLen-1)]
		e.ml5 -= e.chW[e.chFirst5&(ringLen-1)]
		e.chFirst5++
	}
	for e.chFirst30 < e.chSeq && t-e.chT[e.chFirst30&(ringLen-1)] >= win30s {
		i := e.chFirst30 & (ringLen - 1)
		if e.chHasRet[i] {
			e.removeVolatility(e.chRet[i])
			e.chHasRet[i] = false
		}
		e.ofi30 -= e.chV[i]
		e.chFirst30++
	}
	for e.trFirst1 < e.trSeq && t-e.trT[e.trFirst1&(ringLen-1)] >= win1s {
		e.tradeCount1--
		e.trFirst1++
	}
	for e.trFirst5 < e.trSeq && t-e.trT[e.trFirst5&(ringLen-1)] >= win5s {
		e.trade5 -= e.trV[e.trFirst5&(ringLen-1)]
		e.trFirst5++
	}
}

func (e *Engine) evictChange(seq uint64) {
	i := seq & (ringLen - 1)
	if seq >= e.chFirst1 {
		e.ofi1 -= e.chV[i]
		e.ml1 -= e.chW[i]
		e.chFirst1 = seq + 1
	}
	if seq >= e.chFirst5 {
		e.ofi5 -= e.chV[i]
		e.ml5 -= e.chW[i]
		e.chFirst5 = seq + 1
	}
	if seq >= e.chFirst30 {
		e.ofi30 -= e.chV[i]
		e.chFirst30 = seq + 1
	}
	if e.chHasRet[i] {
		e.removeVolatility(e.chRet[i])
		e.chHasRet[i] = false
	}
	if e.mid1Valid && e.mid1Seq > 0 && e.mid1Seq-1 <= seq {
		e.mid1Valid = false
		e.mid1Seq = seq + 1
	}
	if e.mid5Valid && e.mid5Seq > 0 && e.mid5Seq-1 <= seq {
		e.mid5Valid = false
		e.mid5Seq = seq + 1
	}
}

func (e *Engine) evictTrade(seq uint64) {
	i := seq & (ringLen - 1)
	if seq >= e.trFirst1 {
		e.tradeCount1--
		e.trFirst1 = seq + 1
	}
	if seq >= e.trFirst5 {
		e.trade5 -= e.trV[i]
		e.trFirst5 = seq + 1
	}
}

func (e *Engine) addVolatility(v float64) {
	e.volN++
	d := v - e.volMean
	e.volMean += d / float64(e.volN)
	e.volM2 += d * (v - e.volMean)
}

func (e *Engine) removeVolatility(v float64) {
	if e.volN <= 1 {
		e.volN, e.volMean, e.volM2 = 0, 0, 0
		return
	}
	n := float64(e.volN)
	mean := (n*e.volMean - v) / (n - 1)
	e.volM2 -= (v - e.volMean) * (v - mean)
	e.volN--
	e.volMean = mean
	if e.volM2 < 0 {
		e.volM2 = 0
	}
}

// rankAt returns the 0-based rank the level at price occupies (or would
// occupy) on the side, 0 being the touch.
func rankAt(side *orderbook.Levels, price int64, bid bool) int {
	for j := 0; j < int(side.N); j++ {
		if (bid && side.P[j] <= price) || (!bid && side.P[j] >= price) {
			return j
		}
	}
	return int(side.N)
}

func prevQty(lv *orderbook.Levels, price int64) int64 {
	for j := 0; j < int(lv.N); j++ {
		if lv.P[j] == price {
			return lv.Q[j]
		}
	}
	return 0
}

// Extract writes the NCore raw features for the current book state into dst
// and returns how many were written; it returns 0 until the first event was
// processed or if dst is too small. It never allocates.
//
// Features are emitted at event instants (from Advance), so replay and live
// produce identical values for the same event stream regardless of how often
// the consumer reads them.
func (e *Engine) Extract(book *orderbook.Book, dst []float32) int {
	if len(dst) < NCore || !e.emitted || !book.Ready {
		return 0
	}
	bids := &book.Levels[orderbook.Bids]
	asks := &book.Levels[orderbook.Asks]
	dst[0] = imbalance(bids.Q[0], asks.Q[0])
	dst[1] = imbalance(book.DepthSum(orderbook.Bids, 5), book.DepthSum(orderbook.Asks, 5))
	dst[2] = imbalance(book.DepthSum(orderbook.Bids, event.MaxLevels), book.DepthSum(orderbook.Asks, event.MaxLevels))
	// Copy into the public Part-2 region field by field. (A bulk copy of the
	// array onto the caller's slice via append has no stable cost guarantee in
	// the hot path, so the fixed tail is copied explicitly.)
	dst[3] = e.emit[3]
	dst[4] = e.emit[4]
	dst[5] = e.emit[5]
	dst[6] = e.emit[6]
	dst[7] = e.emit[7]
	dst[8] = e.emit[8]
	dst[9] = e.emit[9]
	dst[10] = e.emit[10]
	dst[11] = e.emit[11]
	dst[12] = e.emit[12]
	dst[13] = e.emit[13]
	dst[14] = e.emit[14]
	dst[15] = e.emit[15]
	return NCore
}

// compute fills e.emit from the current state at instant t.
func (e *Engine) compute(book *orderbook.Book, t int64) {
	out := (&e.emit)
	e.emitted = true
	if !book.Ready || !book.Anchored {
		for i := range out {
			out[i] = 0
		}
		return
	}
	bids := &book.Levels[orderbook.Bids]
	asks := &book.Levels[orderbook.Asks]
	bp, bq := bids.P[0], bids.Q[0]
	ap, aq := asks.P[0], asks.Q[0]
	mid := (bp + ap) / 2
	if mid > 0 && ap > bp {
		out[3] = float32((microPrice(bp, bq, ap, aq) - float64(mid)) / float64(ap-bp))
		out[4] = float32(float64(ap-bp) / float64(mid) * 1e4)
	} else {
		out[3], out[4] = 0, 0
	}

	q := float64(event.QtyScale)
	out[5] = float32(e.ofi1 / q)
	out[6] = float32(e.ofi5 / q)
	out[7] = float32(e.ofi30 / q)
	out[8] = float32(e.ml1 / q)
	out[9] = float32(e.ml5 / q)

	out[10] = float32(e.volatility())
	out[11] = float32(e.retBps(t, win1s, mid))
	out[12] = float32(e.retBps(t, win5s, mid))

	out[13] = float32(e.trade5 / q)
	out[14] = float32(e.tradeCount1)
	out[15] = imbalance(bq, aq)
}

// levelWeight is the multi-level OFI decay for an update that lands at rank
// rk at the touch (rk is 0 for the best level). Rank 20 and deeper are capped.
func levelWeight(rk int) float64 {
	if rk > event.MaxLevels-1 {
		rk = event.MaxLevels - 1
	}
	return levelDecay[rk]
}

var levelDecay = func() (w [event.MaxLevels]float64) {
	for i := range w {
		w[i] = math.Exp(-ofiBeta * float64(i))
	}
	return w
}()

func bidTerm(prevQ, q int64) float64 { return float64(q - prevQ) }

func imbalance(b, a int64) float32 {
	s := b + a
	if s <= 0 {
		return 0
	}
	return float32(float64(b-a) / float64(s))
}

func microPrice(bp, bq, ap, aq int64) float64 {
	if bq+aq <= 0 {
		return float64((bp + ap) / 2)
	}
	return (float64(ap)*float64(bq) + float64(bp)*float64(aq)) / float64(bq+aq)
}

func (e *Engine) retBps(t, horizon int64, mid int64) float64 {
	seq, valid := &e.mid1Seq, &e.mid1Valid
	if horizon == win5s {
		seq, valid = &e.mid5Seq, &e.mid5Valid
	}
	old, ok := e.midAtCursor(t-horizon, seq, valid)
	if !ok || old <= 0 {
		return 0
	}
	return float64(mid-old) / float64(old) * 1e4
}

func (e *Engine) midAtCursor(t int64, seq *uint64, valid *bool) (int64, bool) {
	oldest := e.chSeq - uint64(e.nCh)
	if *seq < oldest {
		*seq = oldest
		*valid = false
	} else if *valid && *seq > 0 && e.chT[(*seq-1)&(ringLen-1)] > t {
		*valid = false
		*seq = oldest
	}
	for *seq < e.chSeq && e.chT[*seq&(ringLen-1)] <= t {
		(*seq)++
		*valid = true
	}
	if !*valid {
		return 0, false
	}
	return e.chMid[(*seq-1)&(ringLen-1)], true
}

func (e *Engine) volatility() float64 {
	if e.volN < 2 {
		return 0
	}
	return math.Sqrt(e.volM2 / float64(e.volN-1))
}
