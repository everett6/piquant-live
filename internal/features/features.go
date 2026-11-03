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

// ringLen is the power-of-two capacity of every rolling buffer (512 samples
// is 5+ minutes at typical 50 Hz updates, far more than any window).
const ringLen = 512

// Engine holds per-symbol streaming state. One goroutine owns one Engine.
type Engine struct {
	sym uint16

	// Price-change samples: the time and mid after the update, plus the
	// OFI terms attributed to that update.
	chT   [ringLen]int64
	chMid [ringLen]int64
	chV   [ringLen]float64
	chW   [ringLen]float64
	hCh   int
	nCh   int

	// Trade samples.
	trT [ringLen]int64
	trV [ringLen]float64
	hTr int
	nTr int

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

// Advance consumes one envelope and refreshes the cached state. Call it
// BEFORE applying the envelope to the book: OFI is the per-level size delta
// of exactly the levels the update touches, measured against the book as it
// stood before the update.
func (e *Engine) Advance(book *orderbook.Book, env *event.Envelope) bool {
	e.ready = book.Ready
	e.lastRecv = env.RecvTime
	switch env.Kind {
	case event.KindSnapshot:
		// The snapshot replaces the book and is the OFI baseline: the first
		// depth update is measured against it like any other transition.
		e.hCh, e.nCh, e.hTr, e.nTr = 0, 0, 0, 0
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
		ringPush(&e.trT, &e.hTr, &e.nTr, env.RecvTime)
		e.trV[(e.hTr+ringLen-1)&(ringLen-1)] = float64(q)
	}
	e.compute(book, env.RecvTime)
	return e.ready
}

// baseline records the book state right after a snapshot as a sample, so the
// first post-snapshot update has something to be measured against.
func (e *Engine) baseline(book *orderbook.Book, t int64) {
	ringPush(&e.chT, &e.hCh, &e.nCh, t)
	i := (e.hCh + ringLen - 1) & (ringLen - 1)
	bp, _ := book.Levels[orderbook.Bids].Best()
	ap, _ := book.Levels[orderbook.Asks].Best()
	e.chMid[i] = (bp + ap) / 2
	e.chV[i] = 0
	e.chW[i] = 0
}

func ringPush[T any](buf *[ringLen]T, h *int, n *int, v T) {
	buf[*h] = v
	*h = (*h + 1) & (ringLen - 1)
	if *n < ringLen {
		*n++
	}
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
	ringPush(&e.chT, &e.hCh, &e.nCh, t)
	i := (e.hCh + ringLen - 1) & (ringLen - 1)
	e.chMid[i] = (bp + ap) / 2
	e.chV[i] = world
	e.chW[i] = ml
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

	var ofi1, ofi5, ofi30, ml1, ml5 float64
	for i := 0; i < e.nCh; i++ {
		idx := (e.hCh - 1 - i + ringLen) & (ringLen - 1)
		age := t - e.chT[idx]
		if age >= win30s {
			continue
		}
		ofi30 += e.chV[idx]
		if age >= win5s {
			continue
		}
		ofi5 += e.chV[idx]
		ml5 += e.chW[idx]
		if age >= win1s {
			continue
		}
		ofi1 += e.chV[idx]
		ml1 += e.chW[idx]
	}
	q := float64(event.QtyScale)
	out[5] = float32(ofi1 / q)
	out[6] = float32(ofi5 / q)
	out[7] = float32(ofi30 / q)
	out[8] = float32(ml1 / q)
	out[9] = float32(ml5 / q)

	out[10] = float32(e.volatility(t))
	out[11] = float32(e.retBps(t, win1s, mid))
	out[12] = float32(e.retBps(t, win5s, mid))

	vol, cnt := 0.0, 0.0
	for i := 0; i < e.nTr; i++ {
		idx := (e.hTr - 1 - i + ringLen) & (ringLen - 1)
		age := t - e.trT[idx]
		if age >= win5s {
			break
		}
		vol += e.trV[idx]
		if age < win1s {
			cnt++
		}
	}
	out[13] = float32(vol / q)
	out[14] = float32(cnt)
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
	old, ok := e.midAt(t - horizon)
	if !ok || old <= 0 {
		return 0
	}
	return float64(mid-old) / float64(old) * 1e4
}

func (e *Engine) midAt(t int64) (int64, bool) {
	for i := 0; i < e.nCh; i++ {
		idx := (e.hCh - 1 - i + ringLen) & (ringLen - 1)
		if e.chT[idx] <= t {
			return e.chMid[idx], true
		}
	}
	return 0, false
}

func (e *Engine) volatility(t int64) float64 {
	var n float64
	var mean, m2 float64
	for i := 0; i < e.nCh; i++ {
		idx := (e.hCh - 1 - i + ringLen) & (ringLen - 1)
		now := e.chT[idx]
		if t-now >= win30s {
			break
		}
		old, ok := e.midAt(now - win1s)
		if !ok || old <= 0 {
			continue
		}
		r := float64(e.chMid[idx]-old) / float64(old) * 1e4
		n++
		d := r - mean
		mean += d / n
		m2 += d * (r - mean)
	}
	if n < 2 {
		return 0
	}
	return math.Sqrt(m2 / (n - 1))
}
